package capture

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"frostroot/internal/recipe"
)

// Files capture reads, relative to the root.
const (
	osReleasePath     = "etc/os-release"
	hostnamePath      = "etc/hostname"
	wslConfPath       = "etc/wsl.conf"
	defaultLocalePath = "etc/default/locale"
	timezonePath      = "etc/timezone"
	localtimePath     = "etc/localtime"
	passwdPath        = "etc/passwd"
	groupPath         = "etc/group"
	sudoersDropInDir  = "etc/sudoers.d"
)

// firstHumanUID is where Ubuntu's adduser starts ordinary accounts.
const firstHumanUID = 1000

// nobodyUID is the pseudo-user at the top of the range, never a person.
const nobodyUID = 65534

// systemRoot resolves paths under the captured root.
type systemRoot string

func (root systemRoot) path(relative string) string {
	return filepath.Join(string(root), filepath.FromSlash(relative))
}

// contains reports whether path is the root or sits under it. Join has
// already folded away any "..", so this catches a path that climbed out.
func (root systemRoot) contains(path string) bool {
	relative, err := filepath.Rel(string(root), path)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

// pathInRoot resolves a path the captured system chose itself, such as a
// Signed-By option or a passwd home directory, and refuses one that leaves
// the root. With the default --root / nothing can leave; with --root DIR it
// matters, because that tree was written by some other machine and a
// "../../.." or a symlink in it would otherwise read this one. Symlinks are
// resolved too, since leaving the root takes no ".." at all. A path that
// does not exist yet cannot be resolved, and is left for the caller to
// report as the unreadable file it is.
func (root systemRoot) pathInRoot(relative string) (string, bool) {
	resolved := root.path(relative)
	if !root.contains(resolved) {
		return "", false
	}
	realRoot, err := filepath.EvalSymlinks(string(root))
	if err != nil {
		realRoot = string(root)
	}
	realPath, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		return resolved, true // not there: ReadFile says so, and says which file
	}
	return resolved, systemRoot(realRoot).contains(realPath)
}

// readText returns a file's content, or "" and false when it cannot be read.
func (root systemRoot) readText(relative string) (string, bool) {
	content, err := os.ReadFile(root.path(relative))
	if err != nil {
		return "", false
	}
	return string(content), true
}

// keyValueFile parses lines of KEY=value or key = value, ignoring comments
// and, when sections is true, tracking [section] headers so keys are
// returned as "section.key". Quotes around values are removed.
func keyValueFile(content string, sections bool) map[string]string {
	values := map[string]string{}
	section := ""
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if sections && strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		if sections && section != "" {
			key = section + "." + strings.ToLower(key)
		}
		values[key] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return values
}

// osRelease is what /etc/os-release says about the distribution.
type osRelease struct {
	id      string // "ubuntu"
	version string // "24.04"
}

func (root systemRoot) osRelease() (osRelease, bool) {
	content, ok := root.readText(osReleasePath)
	if !ok {
		return osRelease{}, false
	}
	values := keyValueFile(content, false)
	return osRelease{id: values["ID"], version: values["VERSION_ID"]}, values["ID"] != ""
}

// imageNameSeparators replaces every run of characters an image name cannot
// hold.
var imageNameSeparators = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// imageNameFromHostname turns a hostname into a valid image name, or returns
// fallback when nothing usable is left.
func imageNameFromHostname(hostname, fallback string) string {
	name := imageNameSeparators.ReplaceAllString(strings.ToLower(strings.TrimSpace(hostname)), "-")
	name = strings.Trim(name, "._-")
	if recipe.CheckImageName(name) != nil {
		return fallback
	}
	return name
}

// wslConf is what /etc/wsl.conf says.
type wslConf struct {
	present     bool
	defaultUser string
	systemd     bool // true when the file or the key is missing
}

func (root systemRoot) wslConf() wslConf {
	content, ok := root.readText(wslConfPath)
	if !ok {
		return wslConf{systemd: true}
	}
	values := keyValueFile(content, true)
	systemd := true
	if setting, found := values["boot.systemd"]; found {
		systemd = strings.EqualFold(setting, "true")
	}
	return wslConf{present: true, defaultUser: values["user.default"], systemd: systemd}
}

// locale returns LANG from /etc/default/locale when it passes the recipe's
// rule; found is false when the file has no acceptable LANG.
func (root systemRoot) locale() (lang string, found bool) {
	content, ok := root.readText(defaultLocalePath)
	if !ok {
		return "", false
	}
	lang = keyValueFile(content, false)["LANG"]
	if lang == "" || recipe.CheckLocale(lang) != nil {
		return "", false
	}
	return lang, true
}

// timezone returns the zone from /etc/timezone, or from the /etc/localtime
// symlink's target under zoneinfo/, when it passes the recipe's rule.
func (root systemRoot) timezone() (zone, source string, found bool) {
	if content, ok := root.readText(timezonePath); ok {
		zone = strings.TrimSpace(content)
		if zone != "" && recipe.CheckTimezone(zone) == nil {
			return zone, "/" + timezonePath, true
		}
	}
	target, err := os.Readlink(root.path(localtimePath))
	if err != nil {
		return "", "", false
	}
	_, zone, hasZoneinfo := strings.Cut(filepath.ToSlash(target), "zoneinfo/")
	if !hasZoneinfo || recipe.CheckTimezone(zone) != nil {
		return "", "", false
	}
	return zone, "/" + localtimePath, true
}

// account is a line of /etc/passwd.
type account struct {
	name string
	uid  int
	gid  int
	home string
}

// accounts returns the human accounts of /etc/passwd: uid from 1000 up,
// excluding nobody, sorted by uid.
func (root systemRoot) accounts() []account {
	content, ok := root.readText(passwdPath)
	if !ok {
		return nil
	}
	var humans []account
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Split(line, ":")
		if len(fields) < 7 {
			continue
		}
		uid, uidErr := strconv.Atoi(fields[2])
		gid, gidErr := strconv.Atoi(fields[3])
		if uidErr != nil || gidErr != nil || uid < firstHumanUID || uid >= nobodyUID {
			continue
		}
		humans = append(humans, account{name: fields[0], uid: uid, gid: gid, home: fields[5]})
	}
	slices.SortFunc(humans, func(a, b account) int { return a.uid - b.uid })
	return humans
}

// groupsOf returns the names of the groups in /etc/group that list userName
// as a member, plus the group with the user's primary gid.
func (root systemRoot) groupsOf(userName string, primaryGID int) []string {
	content, ok := root.readText(groupPath)
	if !ok {
		return nil
	}
	var groups []string
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Split(line, ":")
		if len(fields) < 4 {
			continue
		}
		gid, err := strconv.Atoi(fields[2])
		members := strings.Split(fields[3], ",")
		if (err == nil && gid == primaryGID) || slices.Contains(members, userName) {
			groups = append(groups, fields[0])
		}
	}
	slices.Sort(groups)
	return slices.Compact(groups)
}

// sudoGroups are the groups Ubuntu's sudoers grants sudo to.
var sudoGroups = []string{"sudo", "admin"}

// sudoEvidence says whether userName can sudo and where that was seen:
// membership of the sudo or admin group, or a readable drop-in naming the
// user. /etc/sudoers itself is not readable without root and is not read.
func (root systemRoot) sudoEvidence(userName string, groups []string) (hasSudo bool, evidence string) {
	for _, group := range sudoGroups {
		if slices.Contains(groups, group) {
			return true, "member of group " + group
		}
	}
	entries, err := os.ReadDir(root.path(sudoersDropInDir))
	if err != nil {
		return false, ""
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == "README" {
			continue
		}
		content, ok := root.readText(sudoersDropInDir + "/" + entry.Name())
		if !ok {
			continue // unreadable drop-ins are the normal case without root
		}
		for _, line := range strings.Split(content, "\n") {
			if fields := strings.Fields(line); len(fields) > 0 && fields[0] == userName {
				return true, "/" + sudoersDropInDir + "/" + entry.Name()
			}
		}
	}
	return false, ""
}
