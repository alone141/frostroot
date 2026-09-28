package builder

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"frostroot/internal/distro"
	"frostroot/internal/recipe"
)

func fedora44(t *testing.T) distro.FedoraRelease {
	t.Helper()
	release, err := distro.LookupFedora("44", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	return release
}

func TestRenderFedoraReposOnline(t *testing.T) {
	text, err := renderFedoraRepos(fedoraRepositories{release: fedora44(t)})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[fedora]\nname=fedora\nmetalink=https://mirrors.fedoraproject.org/metalink?repo=fedora-44&arch=x86_64\n",
		"[updates]\nname=updates\nmetalink=https://mirrors.fedoraproject.org/metalink?repo=updates-released-f44&arch=x86_64\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("repository file lacks %q:\n%s", want, text)
		}
	}
	// Every package checked, against the pinned key frostroot carries; no
	// key fetched, no store replaced, nothing unverified.
	if strings.Count(text, "gpgcheck=1\n") != 2 || strings.Count(text, "gpgkey=file:///etc/frostroot-keys/RPM-GPG-KEY-fedora-44-primary\n") != 2 {
		t.Errorf("each repository should check packages against the pinned key:\n%s", text)
	}
	for _, unwanted := range []string{"http://", "fedoraproject.org/fedora.gpg", "baseurl", "sslcacert", "sslverify"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("repository file holds %q:\n%s", unwanted, text)
		}
	}
}

func TestRenderFedoraReposOfflineAndTrust(t *testing.T) {
	local := map[string]string{"fedora": "/var/tmp/work root/repos/fedora", "updates": "/var/tmp/work root/repos/updates"}
	text, err := renderFedoraRepos(fedoraRepositories{release: fedora44(t), local: local, caBundle: "/etc/frostroot-trust/ca-bundle.pem", insecure: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"baseurl=file:///var/tmp/work%20root/repos/fedora\n",
		"baseurl=file:///var/tmp/work%20root/repos/updates\n",
		"sslcacert=/etc/frostroot-trust/ca-bundle.pem\n",
		"sslverify=0\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("repository file lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "metalink") {
		t.Errorf("an offline repository reaches for the network:\n%s", text)
	}
	// A line break would start a setting of its own in the file.
	local["fedora"] = "/var/tmp/x\ngpgcheck=0"
	if _, err := renderFedoraRepos(fedoraRepositories{release: fedora44(t), local: local}); err == nil {
		t.Error("a local directory with a line break was written into the repository file")
	}
}

func TestRenderFedoraMkosiConf(t *testing.T) {
	tools := renderFedoraMkosiConf(fedoraMkosiConf{release: fedora44(t), packages: []string{"dnf5", "rpm"}})
	image := renderFedoraMkosiConf(fedoraMkosiConf{release: fedora44(t), image: true, recommend: true, packages: []string{"git", "sudo"}})
	for _, want := range []string{"Distribution=fedora\nRelease=44\nArchitecture=x86-64\n", "Format=directory\nOutput=tools\n", "WithRecommends=no\n", "Packages=dnf5\n         rpm\n"} {
		if !strings.Contains(tools, want) {
			t.Errorf("the tools tree's mkosi.conf lacks %q:\n%s", want, tools)
		}
	}
	for _, want := range []string{"Format=tar\nCompressOutput=no\nOutput=image\n", "WithRecommends=yes\n", "WithDocs=yes\n", "CleanPackageMetadata=no\n", "Packages=git\n         sudo\n"} {
		if !strings.Contains(image, want) {
			t.Errorf("the image's mkosi.conf lacks %q:\n%s", want, image)
		}
	}
	for _, conf := range []string{tools, image} {
		if strings.Contains(conf, "/") {
			t.Errorf("mkosi.conf names a path, which the command line could then not give:\n%s", conf)
		}
	}
}

func TestFedoraPackagesToInstall(t *testing.T) {
	imageRecipe := sampleFedoraRecipe()
	imageRecipe.Packages.Include = []string{"git", "sudo", "NetworkManager"}
	imageRecipe.Locale.Lang = "tr_TR.UTF-8"
	got := FedoraPackagesToInstall(imageRecipe)
	if want := []string{"git", "sudo", "NetworkManager", "glibc-langpack-tr", "systemd"}; !slices.Equal(got[:len(want)], want) {
		t.Errorf("FedoraPackagesToInstall starts %q, want %q", got[:len(want)], want)
	}
	if slices.Index(got, "sudo") != 1 || strings.Count(strings.Join(got, " "), "sudo") != 1 {
		t.Errorf("sudo is listed twice or moved: %q", got)
	}
	for _, name := range FedoraBasePackages {
		if !slices.Contains(got, name) {
			t.Errorf("%s of the base is missing: %q", name, got)
		}
	}
	imageRecipe.Locale.Lang = "C.UTF-8"
	if got := FedoraPackagesToInstall(imageRecipe); slices.ContainsFunc(got, func(name string) bool { return strings.HasPrefix(name, "glibc-langpack-") }) {
		t.Errorf("C.UTF-8 is glibc's own, and needs no langpack: %q", got)
	}
}

// runFedoraScript runs script through a real sh, with each program named in
// stubs replaced by a shell function of that body, and env set. Functions
// take precedence over builtins and over PATH, which a script may reset, so
// no stubbed program can run for real on the host. It returns the combined
// output and the error.
func runFedoraScript(t *testing.T, script string, stubs map[string]string, env map[string]string) (string, error) {
	t.Helper()
	var prelude strings.Builder
	for name, body := range stubs {
		prelude.WriteString(name + "() {\n" + body + "\n}\n")
	}
	command := exec.Command("sh", "-c", prelude.String()+script)
	command.Env = []string{"PATH=" + os.Getenv("PATH")}
	for name, value := range env {
		command.Env = append(command.Env, name+"="+value)
	}
	output, err := command.CombinedOutput()
	return string(output), err
}

// hostileDir makes a directory whose name a shell would split, expand or
// substitute if it were ever unquoted.
func hostileDir(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), `it's a "`+name+`" $(touch pwned) `+"`id`")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestFedoraImageFinalizeRecordsThenCleans(t *testing.T) {
	for _, offline := range []bool{false, true} {
		script, err := renderFedoraImageFinalize(fedora44(t), offline)
		if err != nil {
			t.Fatal(err)
		}
		buildRoot, outputDir, sourceDir := hostileDir(t, "root"), hostileDir(t, "out"), hostileDir(t, "src")
		leftovers := []string{
			"usr/lib/sysimage/libdnf5/transaction_history.sqlite", "usr/lib/sysimage/rpm/rpmdb.sqlite-shm",
			"var/cache/ldconfig/aux-cache", "etc/resolv.conf", "usr/lib/sysimage/libdnf5/packages.toml",
		}
		for _, leftover := range leftovers {
			path := filepath.Join(buildRoot, leftover)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("left by the build"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(sourceDir, fedoraReasonsFile), []byte("reasons from the lock"), 0o644); err != nil {
			t.Fatal(err)
		}
		// The stand-in dnf5 answers the installed query with one package and
		// the available one with its location, after checking it got the
		// image as its install root.
		dnf5 := `case "$*" in
	*"--installroot=$BUILDROOT "*) ;;
	*) echo "dnf5 not pointed at the image: $*" >&2; exit 2 ;;
esac
case "$*" in
	*"--installed"*) echo 'git-0:2.55.0-1.fc44.x86_64|User|updates|git-2.55.0-1.fc44.src.rpm' ;;
	*"--available"*"git-0:2.55.0-1.fc44.x86_64"*) echo 'git-0:2.55.0-1.fc44.x86_64|updates|https://m.example/Packages/g/git-2.55.0-1.fc44.x86_64.rpm' ;;
	*) echo "unexpected: $*" >&2; exit 2 ;;
esac`
		env := map[string]string{"BUILDROOT": buildRoot, "OUTPUTDIR": outputDir, "SRCDIR": sourceDir}
		output, err := runFedoraScript(t, script, map[string]string{"dnf5": dnf5}, env)
		if err != nil {
			t.Fatalf("offline %v: %v: %s", offline, err, output)
		}
		installed, err := os.ReadFile(filepath.Join(outputDir, fedoraInstalledFile))
		if err != nil || !strings.HasPrefix(string(installed), "git-0:2.55.0-1.fc44.x86_64|User|") {
			t.Errorf("offline %v: installed record = %q, %v", offline, installed, err)
		}
		_, locationsErr := os.Stat(filepath.Join(outputDir, fedoraLocationsFile))
		if offline == (locationsErr == nil) {
			t.Errorf("offline %v: a locations record exists = %v; only an online build records locations", offline, locationsErr == nil)
		}
		reasons, err := os.ReadFile(filepath.Join(buildRoot, "usr/lib/sysimage/libdnf5/packages.toml"))
		if offline && (err != nil || string(reasons) != "reasons from the lock") {
			t.Errorf("offline: dnf5's reasons = %q, %v; want the lock's put back", reasons, err)
		}
		for _, leftover := range leftovers[:4] {
			if _, err := os.Stat(filepath.Join(buildRoot, leftover)); !os.IsNotExist(err) {
				t.Errorf("offline %v: %s is still in the image: %v", offline, leftover, err)
			}
		}
		// A dnf5 that fails fails the script: nothing swallows it.
		if _, err := runFedoraScript(t, script, map[string]string{"dnf5": "return 3"}, env); err == nil {
			t.Errorf("offline %v: the script succeeded with a failing dnf5", offline)
		}
	}
}

func TestFedoraToolsFinalizeReadsTheToolsTreesDatabase(t *testing.T) {
	buildRoot, outputDir := hostileDir(t, "tools"), hostileDir(t, "out")
	if err := os.MkdirAll(filepath.Join(buildRoot, ".rpmdb"), 0o755); err != nil {
		t.Fatal(err)
	}
	rpm := `case "$*" in
	"--root $BUILDROOT --dbpath /.rpmdb -qa --queryformat "*) echo 'dnf5|0|5.4.6.0|1.fc44|x86_64|dnf5-5.4.6.0-1.fc44.src.rpm' ;;
	*) echo "unexpected: $*" >&2; exit 2 ;;
esac`
	env := map[string]string{"BUILDROOT": buildRoot, "OUTPUTDIR": outputDir}
	if output, err := runFedoraScript(t, fedoraToolsFinalize, map[string]string{"rpm": rpm}, env); err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	if record, err := os.ReadFile(filepath.Join(outputDir, fedoraToolsInstalledFile)); err != nil || !strings.HasPrefix(string(record), "dnf5|0|5.4.6.0|") {
		t.Errorf("tools record = %q, %v", record, err)
	}
	if _, err := runFedoraScript(t, fedoraToolsFinalize, map[string]string{"rpm": "return 1"}, env); err == nil {
		t.Error("the script succeeded with a failing rpm")
	}
}

func TestFedoraProvisionScript(t *testing.T) {
	imageRecipe := sampleFedoraRecipe()
	imageRecipe.Locale = recipe.Locale{Lang: "tr_TR.UTF-8", Timezone: "Europe/Istanbul"}
	script, err := RenderFedoraProvisionScript(imageRecipe)
	if err != nil {
		t.Fatal(err)
	}
	sourceDir := hostileDir(t, "src")
	var calls []string
	log := filepath.Join(t.TempDir(), "calls")
	record := func(name string) string { return `printf '%s\n' "` + name + ` $*" >> "$CALLS"` }
	stubs := map[string]string{"locale": `echo tr_TR.utf8`}
	for _, name := range []string{"useradd", "usermod", "install", "visudo", "ln", "test"} {
		stubs[name] = record(name)
	}
	// The script writes /etc/locale.conf itself; run it against a stand-in
	// by rewriting the one absolute write it makes.
	etc := t.TempDir()
	script = strings.ReplaceAll(script, "> /etc/locale.conf", `> "$ETC/locale.conf"`)
	output, err := runFedoraScript(t, script, stubs, map[string]string{"SRCDIR": sourceDir, "CALLS": log, "ETC": etc})
	if err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	logged, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	calls = strings.Split(strings.TrimSpace(string(logged)), "\n")
	for _, want := range []string{
		"test -f /usr/share/zoneinfo/Europe/Istanbul",
		"ln -sfn ../usr/share/zoneinfo/Europe/Istanbul /etc/localtime",
		"useradd --create-home --shell /bin/bash --user-group student",
		"usermod --append --groups wheel student",
		"install -m 0440 -o root -g root " + sourceDir + "/sudoers /etc/sudoers.d/90-frostroot",
		"visudo -cqf /etc/sudoers.d/90-frostroot",
		"install -m 0644 -o root -g root " + sourceDir + "/wsl.conf /etc/wsl.conf",
	} {
		if !slices.Contains(calls, want) {
			t.Errorf("the script did not run %q; it ran:\n%s", want, strings.Join(calls, "\n"))
		}
	}
	if localeConf, err := os.ReadFile(filepath.Join(etc, "locale.conf")); err != nil || string(localeConf) != "LANG=tr_TR.UTF-8\n" {
		t.Errorf("locale.conf = %q, %v", localeConf, err)
	}
	// A locale the image lacks fails the build rather than the first login.
	stubs["locale"] = `echo C.utf8`
	if _, err := runFedoraScript(t, script, stubs, map[string]string{"SRCDIR": sourceDir, "CALLS": log, "ETC": etc}); err == nil {
		t.Error("the script succeeded without the recipe's locale in the image")
	}
}

func TestFedoraScriptsNeverSwallowFailures(t *testing.T) {
	provision, err := RenderFedoraProvisionScript(sampleFedoraRecipe())
	if err != nil {
		t.Fatal(err)
	}
	online, err := renderFedoraImageFinalize(fedora44(t), false)
	if err != nil {
		t.Fatal(err)
	}
	offline, err := renderFedoraImageFinalize(fedora44(t), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, script := range []string{provision, online, offline, fedoraToolsFinalize} {
		if !strings.Contains(script, "set -eu\n") {
			t.Errorf("script does not stop at the first failure:\n%s", script)
		}
		for _, swallower := range []string{"|| true", "|| :", "set +e"} {
			if strings.Contains(script, swallower) {
				t.Errorf("script swallows a failure with %q:\n%s", swallower, script)
			}
		}
	}
}
