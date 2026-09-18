package capture

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/md5"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"frostroot/internal/builder"
	"frostroot/internal/pgp"
	"frostroot/internal/recipe"
)

// fakeKeyPacket is the smallest OpenPGP public key pgp accepts.
var fakeKeyPacket = []byte{0x99, 0x00, 0x03, 0x04, 0x00, 0x00}

// buildRoot creates a fake root filesystem from path to content; a value
// starting with "-> " makes a symlink to the rest of it.
func buildRoot(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for relative, content := range files {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if target, isLink := strings.CutPrefix(content, "-> "); isLink {
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// md5Of returns the checksum dpkg would record for content.
func md5Of(content string) string {
	sum := md5.Sum([]byte(content))
	return hex.EncodeToString(sum[:])
}

// dpkgStanza renders one status stanza.
func dpkgStanza(name, priority, status string, extra ...string) string {
	lines := []string{"Package: " + name, "Status: " + status, "Priority: " + priority, "Architecture: amd64", "Version: 1"}
	lines = append(lines, extra...)
	return strings.Join(lines, "\n") + "\n\n"
}

const (
	installed          = "install ok installed"
	shippedAdduserConf = "# adduser defaults\nDIR_MODE=0750\n"
	editedAdduserConf  = "# adduser defaults\nDIR_MODE=0755\n"
)

// wslMachine is a laptop's WSL distribution grown over a semester.
func wslMachine() map[string]string {
	status := dpkgStanza("dpkg", "required", installed) +
		dpkgStanza("bash", "required", installed) +
		dpkgStanza("sudo", "important", installed) +
		dpkgStanza("adduser", "important", installed, "Conffiles:", " /etc/adduser.conf "+md5Of(shippedAdduserConf)) +
		dpkgStanza("git", "optional", installed, "Conffiles:", " /etc/bash_completion.d/git-prompt "+md5Of("git prompt")) +
		dpkgStanza("curl", "optional", installed) +
		dpkgStanza("libcurl4", "optional", installed) +
		dpkgStanza("golang-1.24", "optional", installed) +
		dpkgStanza("mytool", "optional", installed) +
		dpkgStanza("docker-ce", "optional", installed) +
		dpkgStanza("ubuntu-wsl", "optional", installed) +
		dpkgStanza("oldthing", "optional", "deinstall ok config-files")
	inlineKey := strings.ReplaceAll(strings.TrimRight(string(pgp.Armor(fakeKeyPacket)), "\n"), "\n\n", "\n.\n")
	return map[string]string{
		// Third-party sources: Docker (deb822, key file) and an inline-key
		// source are carried; a PPA without signed-by and a flat repository
		// are not; deb-src installs nothing.
		"etc/apt/sources.list.d/docker.sources": "Types: deb\nURIs: https://download.docker.com/linux/ubuntu\nSuites: noble\nComponents: stable\nSigned-By: /etc/apt/keyrings/docker.gpg\n",
		"etc/apt/keyrings/docker.gpg":           string(fakeKeyPacket),
		"etc/apt/sources.list.d/corp.sources":   "Types: deb deb-src\nURIs: https://apt.corp.example/ubuntu/\nSuites: noble noble-extras\nComponents: main tools\nSigned-By:\n " + strings.ReplaceAll(inlineKey, "\n", "\n ") + "\n",
		"etc/apt/sources.list.d/flat.list":      "deb [signed-by=/etc/apt/keyrings/docker.gpg] https://flat.example/repo ./\ndeb-src https://ppa.launchpadcontent.net/longsleep/golang-backports/ubuntu noble main\n",
		"etc/os-release":                        "NAME=\"Ubuntu\"\nID=ubuntu\nVERSION_ID=\"24.04\"\nVERSION_CODENAME=noble\n",
		"etc/hostname":                          "Melik's Laptop\n",
		"etc/wsl.conf":                          "[boot]\nsystemd=true\n\n[user]\ndefault=melik\n",
		"etc/passwd":                            "root:x:0:0:root:/root:/bin/bash\nmelik:x:1000:1000::/home/melik:/bin/bash\nother:x:1001:1001::/home/other:/bin/bash\nnobody:x:65534:65534::/nonexistent:/usr/sbin/nologin\n",
		"etc/group":                             "root:x:0:\nsudo:x:27:melik\ndocker:x:999:melik,other\nmelik:x:1000:\nplugdev:x:46:melik\n",
		"etc/default/locale":                    "LANG=en_US.UTF-8\n",
		"etc/timezone":                          "Europe/Istanbul\n",
		"etc/adduser.conf":                      editedAdduserConf,
		"etc/bash_completion.d/git-prompt":      "git prompt",
		"etc/profile.d/go.sh":                   "export PATH=$PATH:/usr/lib/go-1.24/bin\n",
		"etc/alternatives/awk":                  "-> /usr/bin/mawk",
		"etc/ld.so.cache":                       "cache",
		"etc/systemd/system/myapp.service":      "[Service]\nExecStart=/usr/local/bin/mytool\n",
		"etc/systemd/system/owned.service":      "[Service]\n",
		"etc/cron.d/backup":                     "0 3 * * * root /usr/local/bin/backup\n",
		"etc/apt/sources.list.d/ubuntu.sources": "Types: deb\nURIs: http://archive.ubuntu.com/ubuntu/\nSuites: noble noble-updates\nComponents: main universe\n",
		"etc/apt/sources.list.d/golang.list":    "deb https://ppa.launchpadcontent.net/longsleep/golang-backports/ubuntu noble main\n",
		"var/lib/dpkg/status":                   status,
		"var/lib/apt/extended_states":           "Package: libcurl4\nArchitecture: amd64\nAuto-Installed: 1\n\n",
		"var/lib/dpkg/info/git.list":            "/.\n/etc\n/etc/bash_completion.d\n/etc/bash_completion.d/git-prompt\n",
		"var/lib/dpkg/info/adduser.list":        "/etc/adduser.conf\n",
		"var/lib/dpkg/info/owned.list":          "/etc/systemd/system/owned.service\n",
		"var/lib/apt/lists/archive.ubuntu.com_ubuntu_dists_noble_main_binary-amd64_Packages":                                  "Package: git\nVersion: 1\n\nPackage: curl\nVersion: 1\n\nPackage: libcurl4\nVersion: 1\n\nPackage: bash\nVersion: 1\n\n",
		"var/lib/apt/lists/ppa.launchpadcontent.net_longsleep_golang-backports_ubuntu_dists_noble_main_binary-amd64_Packages": "Package: golang-1.24\nVersion: 1\n\n",
		"var/lib/apt/lists/download.docker.com_linux_ubuntu_dists_noble_stable_binary-amd64_Packages":                         "Package: docker-ce\nVersion: 1\n\n",
		"usr/local/bin/mytool": "#!/bin/sh\n",
		"opt/myide/bin/ide":    "binary",
		"usr/local/lib/python3.12/dist-packages/requests-2.32.3.dist-info/METADATA": "Name: requests\n",
		"usr/local/lib/node_modules/typescript/package.json":                        "{}",
		"usr/local/lib/node_modules/npm/package.json":                               "{}",
		"home/melik/.bashrc":         "alias ll='ls -l'\n",
		"home/melik/.ssh/id_ed25519": "PRIVATE KEY",
		"home/melik/.cargo/bin/rg":   "binary",
		"home/melik/.local/lib/python3.12/site-packages/numpy-2.0.dist-info/METADATA": "Name: numpy\n",
		"home/melik/project/main.cpp": "int main() {}\n",
		"home/other/.bashrc":          "",
	}
}

func findingByArea(t *testing.T, snapshot Snapshot, area string) Finding {
	t.Helper()
	for _, finding := range snapshot.Findings {
		if finding.Area == area {
			return finding
		}
	}
	t.Fatalf("no finding for area %q", area)
	return Finding{}
}

func TestReadWSLMachine(t *testing.T) {
	snapshot, err := Read(buildRoot(t, wslMachine()))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ImageName != "melik-s-laptop" || snapshot.Release != "24.04" || snapshot.Arch != "amd64" {
		t.Errorf("identity = %q %q %q", snapshot.ImageName, snapshot.Release, snapshot.Arch)
	}
	if snapshot.UserName != "melik" || !snapshot.Sudo || !snapshot.Systemd {
		t.Errorf("user = %q sudo %v systemd %v", snapshot.UserName, snapshot.Sudo, snapshot.Systemd)
	}
	if snapshot.Locale != "en_US.UTF-8" || snapshot.Timezone != "Europe/Istanbul" {
		t.Errorf("locale = %q timezone %q", snapshot.Locale, snapshot.Timezone)
	}
	// Required/important, automatic, metapackage and removed packages are
	// out; what someone asked for stays, sorted.
	if want := []string{"curl", "docker-ce", "git", "golang-1.24", "mytool"}; !slices.Equal(snapshot.Packages, want) {
		t.Errorf("packages = %q, want %q", snapshot.Packages, want)
	}
	if snapshot.InstalledCount != 11 {
		t.Errorf("installed = %d, want 11 (the removed package does not count)", snapshot.InstalledCount)
	}
	wantSources := []recipe.Source{ // source files are read in name order
		{Name: "apt-corp-example", URL: "https://apt.corp.example/ubuntu", Components: []string{"main", "tools"}, Key: "keys/apt-corp-example.asc"},
		{Name: "apt-corp-example-2", URL: "https://apt.corp.example/ubuntu", Suite: "noble-extras", Components: []string{"main", "tools"}, Key: "keys/apt-corp-example-2.asc"},
		{Name: "docker", URL: "https://download.docker.com/linux/ubuntu", Components: []string{"stable"}, Key: "keys/docker.asc"},
	}
	if !reflect.DeepEqual(snapshot.Sources, wantSources) {
		t.Errorf("sources =\n%+v\nwant\n%+v", snapshot.Sources, wantSources)
	}
	for _, source := range wantSources {
		key, err := pgp.ParsePublicKey(snapshot.Keys[source.Name])
		if err != nil || !key.Armored {
			t.Errorf("key of %s: %v", source.Name, err)
		}
	}
	if !slices.ContainsFunc(snapshot.Evidence, func(line string) bool {
		return strings.Contains(line, `source "docker" (Docker) from /etc/apt/sources.list.d/docker.sources, key /etc/apt/keyrings/docker.gpg`)
	}) {
		t.Errorf("evidence should name the carried source and its key: %q", snapshot.Evidence)
	}
	thirdPartySources := findingByArea(t, snapshot, AreaThirdPartySources)
	if thirdPartySources.Count != 2 || !strings.Contains(thirdPartySources.Examples[0], "flat.list (https://flat.example/repo ./): a flat repository") || !strings.Contains(thirdPartySources.Examples[1], "golang.list (https://ppa.launchpadcontent.net/longsleep/golang-backports/ubuntu noble): no signed-by key") {
		t.Errorf("third-party sources = %+v", thirdPartySources)
	}
	expectations := map[string][]string{
		AreaThirdPartyPackages: {"golang-1.24 (ppa.launchpadcontent.net/longsleep/golang-backports/ubuntu noble)"}, // docker-ce's source is in the recipe now
		AreaUnsourcedPackages:  {"mytool"},
		AreaModifiedConfig:     {"/etc/adduser.conf"},
		AreaAddedEtc:           {"/etc/profile.d/go.sh"},
		AreaOutsideApt: {
			"/opt/myide", "/usr/local/bin/mytool", "cargo binaries (~/.cargo/bin)",
			"npm (global): typescript", "pip (system): requests 2.32.3", "pip (user): numpy 2.0",
		},
		AreaServices:   {"/etc/cron.d/backup", "/etc/systemd/system/myapp.service"},
		AreaOtherUsers: {"other (uid 1001)"},
		AreaGroups:     {"docker"},
	}
	for area, wantExamples := range expectations {
		finding := findingByArea(t, snapshot, area)
		if finding.Unavailable != "" || !slices.Equal(finding.Examples, wantExamples) || finding.Count != len(wantExamples) {
			t.Errorf("%s = %+v, want examples %q", area, finding, wantExamples)
		}
	}
	home := findingByArea(t, snapshot, AreaHome)
	wantHome := []string{".bashrc", ".cargo/", ".local/", ".ssh (secrets: never copy)", "and 1 other files and directories"}
	if !slices.Equal(home.Examples, wantHome) || home.Count != 5 {
		t.Errorf("home = %+v, want %q", home, wantHome)
	}
	for _, finding := range snapshot.Findings {
		if finding.Advice == "" {
			t.Errorf("%s has no advice", finding.Area)
		}
	}
}

func TestReadNeverReadsHomeContents(t *testing.T) {
	files := wslMachine()
	root := buildRoot(t, files)
	// Secrets are unreadable to prove capture does not need to read them.
	for _, secret := range []string{"home/melik/.ssh/id_ed25519", "etc/shadow", "etc/sudoers"} {
		path := filepath.Join(root, filepath.FromSlash(secret))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("secret"), 0o000); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Read(root); err != nil {
		t.Fatalf("unreadable secrets must not matter: %v", err)
	}
}

func TestReadBareServerFallsBack(t *testing.T) {
	root := buildRoot(t, map[string]string{
		"etc/os-release":      "ID=ubuntu\nVERSION_ID=\"22.04\"\n",
		"etc/hostname":        "---\n",
		"etc/passwd":          "root:x:0:0::/root:/bin/bash\nbob:x:1001:1001::/home/bob:/bin/bash\nalice:x:1000:1000::/home/alice:/bin/bash\n",
		"etc/group":           "alice:x:1000:\nbob:x:1001:\n",
		"etc/localtime":       "-> ../usr/share/zoneinfo/Asia/Tokyo",
		"var/lib/dpkg/status": dpkgStanza("dpkg", "required", installed) + dpkgStanza("vim", "optional", installed) + dpkgStanza("libvim", "optional", installed),
	})
	snapshot, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ImageName != fallbackImageName {
		t.Errorf("image name = %q, want the fallback for an unusable hostname", snapshot.ImageName)
	}
	if snapshot.UserName != "alice" || snapshot.Sudo || !snapshot.Systemd {
		t.Errorf("user = %q sudo %v systemd %v; want the lowest uid, no sudo, systemd on", snapshot.UserName, snapshot.Sudo, snapshot.Systemd)
	}
	if snapshot.Locale != fallbackLocale || snapshot.Timezone != "Asia/Tokyo" {
		t.Errorf("locale = %q timezone %q", snapshot.Locale, snapshot.Timezone)
	}
	// No automatic marks: every non-base package is listed, and the report
	// says why.
	if want := []string{"libvim", "vim"}; !slices.Equal(snapshot.Packages, want) {
		t.Errorf("packages = %q, want %q", snapshot.Packages, want)
	}
	if !slices.ContainsFunc(snapshot.Evidence, func(line string) bool { return strings.Contains(line, "kept no record") }) {
		t.Errorf("evidence should explain the missing automatic marks: %q", snapshot.Evidence)
	}
	if finding := findingByArea(t, snapshot, AreaThirdPartyPackages); finding.Unavailable == "" {
		t.Errorf("without apt indexes, origins cannot be checked: %+v", finding)
	}
	if finding := findingByArea(t, snapshot, AreaAddedEtc); finding.Unavailable == "" {
		t.Errorf("without dpkg file lists, ownership cannot be checked: %+v", finding)
	}
	if finding := findingByArea(t, snapshot, AreaOtherUsers); !slices.Equal(finding.Examples, []string{"bob (uid 1001)"}) {
		t.Errorf("other users = %+v", finding)
	}
}

func TestReadRefusesWhatItCannotDescribe(t *testing.T) {
	ubuntuStatus := dpkgStanza("dpkg", "required", installed)
	testCases := []struct {
		name    string
		files   map[string]string
		wantErr error
	}{
		{name: "empty directory", files: map[string]string{}, wantErr: ErrNotLinuxRoot},
		{name: "no dpkg", files: map[string]string{"etc/os-release": "ID=ubuntu\nVERSION_ID=\"24.04\"\n"}, wantErr: ErrNotLinuxRoot},
		{name: "debian", files: map[string]string{"etc/os-release": "ID=debian\nVERSION_ID=\"12\"\n", "var/lib/dpkg/status": ubuntuStatus}, wantErr: ErrNotUbuntu},
		{name: "unsupported release", files: map[string]string{"etc/os-release": "ID=ubuntu\nVERSION_ID=\"23.10\"\n", "var/lib/dpkg/status": ubuntuStatus}, wantErr: ErrUnsupportedRelease},
		{name: "arm64", files: map[string]string{"etc/os-release": "ID=ubuntu\nVERSION_ID=\"24.04\"\n", "var/lib/dpkg/status": strings.Replace(ubuntuStatus, "amd64", "arm64", 1)}, wantErr: ErrUnsupportedArch},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := Read(buildRoot(t, testCase.files))
			if !errors.Is(err, testCase.wantErr) {
				t.Errorf("Read() error = %v, want %v", err, testCase.wantErr)
			}
		})
	}
}

func TestRecipeFromSnapshotValidates(t *testing.T) {
	snapshot, err := Read(buildRoot(t, wslMachine()))
	if err != nil {
		t.Fatal(err)
	}
	imageRecipe := snapshot.Recipe()
	if imageRecipe.WSL.DefaultUser != "melik" || imageRecipe.Image.Arch != "amd64" {
		t.Errorf("recipe = %+v", imageRecipe)
	}
	if problems := recipe.Validate(imageRecipe); len(problems) != 0 {
		t.Errorf("captured recipe does not validate: %v", problems)
	}
}

func TestReportAndSummary(t *testing.T) {
	snapshot, err := Read(buildRoot(t, wslMachine()))
	if err != nil {
		t.Fatal(err)
	}
	report := snapshot.Report()
	for _, wantText := range []string{
		"# frostroot capture report", "## Captured", "## Not captured",
		"- Ubuntu 24.04, amd64", "user \"melik\" from /etc/wsl.conf",
		"### Packages from third-party sources (1)", "- golang-1.24 (ppa.launchpadcontent.net/longsleep/golang-backports/ubuntu noble)",
		"### Modified configuration files (1)", "- /etc/adduser.conf",
		"### The user's home directory (5)", ".ssh (secrets: never copy)",
	} {
		if !strings.Contains(report, wantText) {
			t.Errorf("report lacks %q:\n%s", wantText, report)
		}
	}
	for _, area := range []string{AreaThirdPartySources, AreaUnsourcedPackages, AreaAddedEtc, AreaOutsideApt, AreaServices, AreaOtherUsers, AreaGroups} {
		if !strings.Contains(report, "### "+area) {
			t.Errorf("report lacks the %s section", area)
		}
	}
	summary := snapshot.Summary()
	if len(summary) != len(snapshot.Findings) || !strings.Contains(strings.Join(summary, "\n"), "Modified configuration files") {
		t.Errorf("summary = %q", summary)
	}
}

func TestReportSaysWhenNothingWasFound(t *testing.T) {
	report := Snapshot{Findings: []Finding{
		{Area: "Quiet area"},
		{Area: "Blind area", Unavailable: "the files were missing."},
		{Area: "Busy area", Count: 25, Examples: make([]string, 25), Advice: "Do something."},
	}}.Report()
	for _, wantText := range []string{"### Quiet area\n\nNothing found.", "### Blind area\n\nCould not check: the files were missing.", "### Busy area (25)", "- and 5 more"} {
		if !strings.Contains(report, wantText) {
			t.Errorf("report lacks %q:\n%s", wantText, report)
		}
	}
}

func TestReadStanzas(t *testing.T) {
	stanzas := readStanzas(strings.NewReader("Package: a\nConffiles:\n /etc/a 0123\n /etc/b 4567 obsolete\nDescription: first\n more\n\n\nPackage: b\nbroken line\nStatus: install ok installed\n"))
	if len(stanzas) != 2 || stanzas[0]["Package"] != "a" || stanzas[1]["Status"] != "install ok installed" {
		t.Fatalf("stanzas = %+v", stanzas)
	}
	if lines := continuationLines(stanzas[0]["Conffiles"]); !slices.Equal(lines, []string{"/etc/a 0123", "/etc/b 4567 obsolete"}) {
		t.Errorf("conffile lines = %q", lines)
	}
	if stanzas[0]["Description"] != "first\nmore" {
		t.Errorf("description = %q", stanzas[0]["Description"])
	}
}

func TestImageNameFromHostname(t *testing.T) {
	for hostname, want := range map[string]string{"cpp-lab": "cpp-lab", "Melik's Laptop": "melik-s-laptop", "DESKTOP-ABC123": "desktop-abc123", "---": fallbackImageName, "": fallbackImageName, ".hidden.": "hidden"} {
		if got := imageNameFromHostname(hostname, fallbackImageName); got != want {
			t.Errorf("imageNameFromHostname(%q) = %q, want %q", hostname, got, want)
		}
	}
}

func TestParseSourceFiles(t *testing.T) {
	oneLine := "# comment\ndeb [arch=amd64 signed-by=/k.gpg,/other.gpg] https://ppa.example/ubuntu noble main universe\ndeb-src http://archive.ubuntu.com/ubuntu noble main\ndeb http://plain.example/repo noble main\nbroken\n"
	got := parseOneLineSources("/etc/apt/sources.list", oneLine)
	want := []aptSource{
		{file: "/etc/apt/sources.list", uri: "https://ppa.example/ubuntu", suite: "noble", components: []string{"main", "universe"}, signedBy: "/k.gpg"},
		{file: "/etc/apt/sources.list", uri: "http://plain.example/repo", suite: "noble", components: []string{"main"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("one-line =\n%+v\nwant\n%+v", got, want)
	}
	deb822 := "Types: deb\nURIs: http://tr.archive.ubuntu.com/ubuntu/ http://security.ubuntu.com/ubuntu/\nSuites: noble noble-updates\nComponents: main\n\nTypes: deb-src\nURIs: https://src.example\nSuites: noble\n\nTypes: deb\nURIs: https://off.example\nSuites: noble\nEnabled: no\n"
	entries := parseDeb822Sources("/etc/apt/sources.list.d/ubuntu.sources", deb822)
	if len(entries) != 4 || !isUbuntuArchiveURI(entries[0].uri) || entries[3].suite != "noble-updates" {
		t.Errorf("deb822 = %+v, want two URIs times two suites, no deb-src, nothing disabled", entries)
	}
	if isUbuntuArchiveURI("https://ppa.launchpadcontent.net/x/y/ubuntu") || isUbuntuArchive("notubuntu.com_dists_x_Packages") {
		t.Error("third-party hosts must not count as the Ubuntu archive")
	}
	if got := deb822SignedBy("\n-----BEGIN PGP PUBLIC KEY BLOCK-----\n.\nmQIN\n-----END PGP PUBLIC KEY BLOCK-----"); got != "-----BEGIN PGP PUBLIC KEY BLOCK-----\n\nmQIN\n-----END PGP PUBLIC KEY BLOCK-----\n" {
		t.Errorf("inline key = %q", got)
	}
}

func TestSourceNameFor(t *testing.T) {
	used := map[string]bool{}
	for uri, want := range map[string]string{
		"https://download.docker.com/linux/ubuntu/":                          "docker",
		"https://ppa.launchpadcontent.net/deadsnakes/ppa/ubuntu":             "deadsnakes", // the catalog knows this PPA
		"https://ppa.launchpadcontent.net/longsleep/golang-backports/ubuntu": "ppa-longsleep-golang-backports",
		"https://apt.corp.example/ubuntu":                                    "apt-corp-example",
		"http://127.0.0.1:8099":                                              "127-0-0-1",
	} {
		if got := sourceNameFor(uri, "noble", "noble", used); got != want {
			t.Errorf("sourceNameFor(%q) = %q, want %q", uri, got, want)
		}
	}
	used["apt-corp-example"] = true
	if got := sourceNameFor("https://apt.corp.example/ubuntu", "noble-extras", "noble", used); got != "apt-corp-example-2" {
		t.Errorf("second name = %q", got)
	}
	// Docker on another suite is not the catalog's Docker entry.
	if got := sourceNameFor("https://download.docker.com/linux/ubuntu", "jammy", "noble", map[string]bool{}); got != "download-docker-com" {
		t.Errorf("docker on jammy = %q", got)
	}
}

func TestSourcesForRecipeReportsUnreadableKeys(t *testing.T) {
	root := buildRoot(t, map[string]string{
		"etc/apt/sources.list.d/a.list": "deb [signed-by=/etc/apt/keyrings/missing.gpg] https://a.example/ubuntu noble main\n",
		"etc/apt/sources.list.d/b.list": "deb [signed-by=/etc/apt/keyrings/html.gpg] https://b.example/ubuntu noble main\n",
		"etc/apt/keyrings/html.gpg":     "<html>",
	})
	carried, left := systemRoot(root).sourcesForRecipe("noble")
	if len(carried) != 0 || len(left) != 2 {
		t.Fatalf("carried %+v, left %+v", carried, left)
	}
	if !strings.Contains(left[0].reason, "missing.gpg could not be read") || !strings.Contains(left[1].reason, "not an OpenPGP public key") {
		t.Errorf("reasons = %q, %q", left[0].reason, left[1].reason)
	}
}

func TestIsGeneratedEtcPath(t *testing.T) {
	for path, want := range map[string]bool{
		"/etc/alternatives/awk": true, "/etc/systemd/system/multi-user.target.wants/x.service": true, "/etc/passwd-": true,
		"/etc/ssl/certs/ca-certificates.crt": true, "/etc/apt/sources.list.d/x.list": true, "/etc/ca-certificates.conf.dpkg-old": true,
		"/etc/systemd/system/myapp.service": true, // reported under services, not here
		"/etc/profile.d/go.sh":              false, "/etc/nginx/nginx.conf": false, "/etc/myapp/config.yaml": false,
	} {
		if got := isGeneratedEtcPath(path); got != want {
			t.Errorf("isGeneratedEtcPath(%q) = %v, want %v", path, got, want)
		}
	}
}

// TestSourcesForRecipeCarriesTheSignedCopy is the duplicate URI+suite case:
// a vendor's current instructions add a .sources file beside the .list an
// older version left, and both name one repository. Deciding on the first
// copy alone dropped the signed one silently, so the repository reached
// neither the recipe nor the report.
func TestSourcesForRecipeCarriesTheSignedCopy(t *testing.T) {
	root := buildRoot(t, map[string]string{
		"etc/apt/keyrings/docker.asc": string(pgp.Armor(fakeKeyPacket)),
		// Read first, and unsigned: on its own it could only be reported.
		"etc/apt/sources.list.d/docker.list": "deb https://download.docker.com/linux/ubuntu noble stable\n",
		"etc/apt/sources.list.d/docker.sources": "Types: deb\nURIs: https://download.docker.com/linux/ubuntu\n" +
			"Suites: noble\nComponents: edge\nSigned-By: /etc/apt/keyrings/docker.asc\n",
	})
	carried, left := systemRoot(root).sourcesForRecipe("noble")
	if len(carried) != 1 || len(left) != 0 {
		t.Fatalf("carried %+v, left %+v", carried, left)
	}
	source := carried[0].source
	if source.Name != "docker" || source.URL != "https://download.docker.com/linux/ubuntu" {
		t.Errorf("source = %+v", source)
	}
	// Both lines were live, so both components are what the machine fetched.
	if !slices.Equal(source.Components, []string{"stable", "edge"}) {
		t.Errorf("components = %v, want [stable edge]", source.Components)
	}
	// The key came from the .sources copy, and folding the two is visible.
	if !strings.Contains(carried[0].from, "docker.sources") || !strings.Contains(carried[0].from, "also listed in") {
		t.Errorf("from = %q", carried[0].from)
	}
}

// TestSourcesForRecipeStillReportsAnUnsignedOnlyRepository keeps the other
// half honest: with no signed copy, the repository is still reported.
func TestSourcesForRecipeStillReportsAnUnsignedOnlyRepository(t *testing.T) {
	root := buildRoot(t, map[string]string{
		"etc/apt/sources.list.d/docker.list":  "deb https://download.docker.com/linux/ubuntu noble stable\n",
		"etc/apt/sources.list.d/docker2.list": "deb https://download.docker.com/linux/ubuntu noble edge\n",
	})
	carried, left := systemRoot(root).sourcesForRecipe("noble")
	if len(carried) != 0 || len(left) != 1 {
		t.Fatalf("carried %+v, left %+v", carried, left)
	}
	if !strings.Contains(left[0].reason, "no signed-by key") {
		t.Errorf("reason = %q", left[0].reason)
	}
}

// TestSourcesForRecipePrefersTheMostSpecificFailure: when no copy can be
// carried, the copy that named a key says more than the one that did not.
func TestSourcesForRecipePrefersTheMostSpecificFailure(t *testing.T) {
	root := buildRoot(t, map[string]string{
		"etc/apt/sources.list.d/a.list": "deb https://x.example/ubuntu noble main\n",
		"etc/apt/sources.list.d/b.list": "deb [signed-by=/etc/apt/keyrings/gone.gpg] https://x.example/ubuntu noble main\n",
	})
	_, left := systemRoot(root).sourcesForRecipe("noble")
	if len(left) != 1 || !strings.Contains(left[0].reason, "gone.gpg could not be read") {
		t.Fatalf("left = %+v", left)
	}
}

// TestPackageOriginsSeparateRepositoriesOnOneHost is the PPA case: every
// Launchpad PPA lives on ppa.launchpadcontent.net, so attributing packages
// to the host alone let one carried PPA vouch for all the others.
func TestPackageOriginsSeparateRepositoriesOnOneHost(t *testing.T) {
	const launchpad = "var/lib/apt/lists/ppa.launchpadcontent.net_"
	root := systemRoot(buildRoot(t, map[string]string{
		launchpad + "deadsnakes_ppa_ubuntu_dists_noble_main_binary-amd64_Packages":             "Package: python3.13\n",
		launchpad + "longsleep_golang-backports_ubuntu_dists_noble_main_binary-amd64_Packages": "Package: golang-1.24\n",
	}))
	origins := root.packageOrigins()
	// Only deadsnakes is in the recipe.
	carriedIndexes := map[aptIndex]bool{
		{prefix: builder.AptListPrefix("https://ppa.launchpadcontent.net/deadsnakes/ppa/ubuntu"), suite: "noble"}: true,
	}
	thirdParty, unsourced := thirdPartyPackageFindings([]string{"python3.13", "golang-1.24"}, origins, carriedIndexes)
	if unsourced.Count != 0 {
		t.Errorf("unsourced = %+v", unsourced)
	}
	want := []string{"golang-1.24 (ppa.launchpadcontent.net/longsleep/golang-backports/ubuntu noble)"}
	if !slices.Equal(thirdParty.Examples, want) {
		t.Errorf("third-party = %v, want %v", thirdParty.Examples, want)
	}
}

func TestAptIndexOf(t *testing.T) {
	index, ok := aptIndexOf("ppa.launchpadcontent.net_longsleep_golang-backports_ubuntu_dists_noble_main_binary-amd64_Packages")
	if !ok || index.prefix != "ppa.launchpadcontent.net_longsleep_golang-backports_ubuntu" || index.suite != "noble" {
		t.Fatalf("aptIndexOf = %+v, ok %v", index, ok)
	}
	// Apt writes a literal underscore as %5f, so Describe reads the URL back.
	escaped, _ := aptIndexOf("ex.com_my%5frepo_ubuntu_dists_noble_main_binary-amd64_Packages")
	if got := escaped.Describe(); got != "ex.com/my_repo/ubuntu noble" {
		t.Errorf("Describe = %q", got)
	}
	if _, ok := aptIndexOf("weird_file_name_Packages"); ok {
		t.Error("a name without a dists segment must not be attributed")
	}
}

// TestCaptureKeepsSignedByInsideTheRoot: with --root DIR the tree was
// written by another machine, so a Signed-By that climbs out of it, or a
// symlink that leaves it without climbing, must not read this machine's
// files into the recipe.
func TestCaptureKeepsSignedByInsideTheRoot(t *testing.T) {
	outside := t.TempDir()
	hostKey := filepath.Join(outside, "host.asc")
	if err := os.WriteFile(hostKey, pgp.Armor(fakeKeyPacket), 0o644); err != nil {
		t.Fatal(err)
	}
	root := buildRoot(t, map[string]string{
		"etc/apt/sources.list.d/climb.list": "deb [signed-by=/etc/apt/keyrings/../../../../" +
			strings.TrimPrefix(hostKey, "/") + "] https://climb.example/ubuntu noble main\n",
		"etc/apt/sources.list.d/link.list": "deb [signed-by=/etc/apt/keyrings/link.asc] https://link.example/ubuntu noble main\n",
		"etc/apt/keyrings/link.asc":        "-> " + hostKey,
	})
	carried, left := systemRoot(root).sourcesForRecipe("noble")
	if len(carried) != 0 {
		t.Fatalf("a key outside the root was carried: %+v", carried)
	}
	if len(left) != 2 {
		t.Fatalf("left = %+v", left)
	}
	for _, source := range left {
		if !strings.Contains(source.reason, "outside") {
			t.Errorf("reason = %q, want it to say the key is outside the root", source.reason)
		}
	}
}

// A Signed-By with a harmless ".." that stays inside the root still works:
// the rule is about leaving, not about the characters.
func TestCaptureAllowsDotDotThatStaysInsideTheRoot(t *testing.T) {
	root := buildRoot(t, map[string]string{
		"etc/apt/sources.list.d/x.list": "deb [signed-by=/etc/apt/keyrings/../keyrings/x.asc] https://x.example/ubuntu noble main\n",
		"etc/apt/keyrings/x.asc":        string(pgp.Armor(fakeKeyPacket)),
	})
	carried, left := systemRoot(root).sourcesForRecipe("noble")
	if len(carried) != 1 || len(left) != 0 {
		t.Fatalf("carried %+v, left %+v", carried, left)
	}
}

// TestParseOneLineSourcesStripsComments: apt takes "#" as a comment to the
// end of the line wherever it sits, so a hand-added note after the
// components is not two more components. It used to make CheckSource refuse
// the source, which reported a working repository as one it could not carry.
func TestParseOneLineSourcesStripsComments(t *testing.T) {
	entries := parseOneLineSources("/etc/apt/sources.list", strings.Join([]string{
		"deb [signed-by=/k.gpg] https://example.com noble main # vendor said so",
		"# a whole-line comment",
		"   ",
		"deb [signed-by=/k.gpg] https://two.example noble main universe",
		"deb [signed-by=/k.gpg] https://hash.example/a#b noble main", // apt drops this line
	}, "\n"))
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want the two apt would use", entries)
	}
	if !slices.Equal(entries[0].components, []string{"main"}) {
		t.Errorf("components = %v, want [main]", entries[0].components)
	}
	if !slices.Equal(entries[1].components, []string{"main", "universe"}) {
		t.Errorf("components = %v, want [main universe]", entries[1].components)
	}
}

// selfSignedPEM returns one certificate, for fixtures that need a real one.
func selfSignedPEM(t *testing.T, commonName string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// TestCaptureCarriesTheMachinesCertificateAuthorities: the authorities an
// organization added live in /usr/local/share/ca-certificates, which is
// outside /etc and so outside everything else capture reads. They are
// exactly what a machine behind a TLS-inspecting proxy needs in its image.
func TestCaptureCarriesTheMachinesCertificateAuthorities(t *testing.T) {
	corp := selfSignedPEM(t, "corp-root")
	root := systemRoot(buildRoot(t, map[string]string{
		"usr/local/share/ca-certificates/corp root.crt": string(corp),
		"usr/local/share/ca-certificates/notes.txt":     "not a certificate, and not a .crt",
		"usr/local/share/ca-certificates/broken.crt":    "-----BEGIN CERTIFICATE-----\nnonsense\n-----END CERTIFICATE-----\n",
	}))
	carried, left := root.certificatesForRecipe()
	if len(carried) != 1 {
		t.Fatalf("carried %+v, want the one real certificate", carried)
	}
	// The file name becomes one the recipe accepts, and the path is relative.
	if carried[0].path != "certs/corp-root.pem" {
		t.Errorf("path = %q, want certs/corp-root.pem", carried[0].path)
	}
	if err := recipe.CheckCertificatePath(carried[0].path); err != nil {
		t.Errorf("the path the recipe would hold is invalid: %v", err)
	}
	if !strings.Contains(string(carried[0].pem), "BEGIN CERTIFICATE") {
		t.Errorf("pem = %q", carried[0].pem)
	}
	// A .crt that is not a certificate is reported, not written as one.
	if len(left) != 1 || !strings.Contains(left[0].description, "broken.crt") {
		t.Errorf("left = %+v, want the unreadable .crt reported", left)
	}
}

// TestCaptureReportsTrustNoPackageOwns: update-ca-certificates rebuilds
// /etc/ssl/certs, so a file put there by hand is not carried and will not
// survive into an image. Capture's rule is that it says so.
func TestCaptureReportsTrustNoPackageOwns(t *testing.T) {
	root := systemRoot(buildRoot(t, map[string]string{
		"etc/ssl/certs/ca-certificates.crt": "the generated bundle",
		"etc/ssl/certs/DigiCert.pem":        "owned by the ca-certificates package",
		"etc/ssl/certs/by-hand.pem":         "put here by someone",
	}))
	owned := map[string]bool{"/etc/ssl/certs/DigiCert.pem": true}
	finding := root.unaccountedTrustFinding(owned, true, nil)
	if finding.Count != 1 || !slices.Equal(finding.Examples, []string{"/etc/ssl/certs/by-hand.pem"}) {
		t.Errorf("finding = %+v, want only the hand-placed file", finding)
	}
	// Without dpkg's file lists the area is unavailable, not empty.
	if unavailable := root.unaccountedTrustFinding(nil, false, nil); unavailable.Unavailable == "" {
		t.Error("with no ownership data the area must say it could not be checked")
	}
}
