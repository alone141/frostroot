package builder

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"frostroot/internal/recipe"
)

// ParseDpkgStatus reads a dpkg status file (/var/lib/dpkg/status, downloaded
// out of the image by a hook) and returns every installed package, sorted by
// name and then architecture.
//
// Parsing the file in Go means no host tool is involved: dpkg-query --root
// needs dpkg >= 1.21 on the host, which Ubuntu 20.04 and Debian 11 lack.
//
// A package counts when the third word of its Status field is "installed",
// whatever the selection state: "hold ok installed" is as present in the image
// as "install ok installed". Removed packages that left config files behind
// are listed in the file too, and are skipped.
func ParseDpkgStatus(r io.Reader) ([]recipe.LockPackage, error) {
	var out []recipe.LockPackage
	var cur map[string]string
	var bad []string
	flush := func() {
		if cur == nil {
			return
		}
		defer func() { cur = nil }()
		status := strings.Fields(cur["Status"])
		if len(status) != 3 || status[2] != "installed" {
			return
		}
		p := recipe.LockPackage{Name: cur["Package"], Version: cur["Version"], Arch: cur["Architecture"]}
		if p.Name == "" || p.Version == "" || p.Arch == "" {
			bad = append(bad, fmt.Sprintf("%q", p.Name))
			return
		}
		out = append(out, p)
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // long Depends/Description fields
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			continue // continuation of a multi-line field
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if cur == nil {
			cur = map[string]string{}
		}
		cur[key] = strings.TrimSpace(val)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading dpkg status: %w", err)
	}
	flush()

	if len(bad) > 0 {
		return nil, fmt.Errorf("dpkg status: installed package(s) missing Package, Version or Architecture: %s", strings.Join(bad, ", "))
	}
	if len(out) == 0 {
		return nil, errors.New("no installed packages found in dpkg status")
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Arch < out[j].Arch
	})
	return out, nil
}
