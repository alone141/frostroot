package capture

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

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
		AreaThirdPartyPackages: {"golang-1.24 (ppa.launchpadcontent.net)"}, // docker-ce's source is in the recipe now
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
		"### Packages from third-party sources (1)", "- golang-1.24 (ppa.launchpadcontent.net)",
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
