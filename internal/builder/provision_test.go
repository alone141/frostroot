package builder

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"frostroot/internal/recipe"
)

func testRecipe() recipe.Recipe {
	return recipe.Recipe{
		Image:    recipe.Image{Name: "cpp-lab", Release: "22.04", Arch: "amd64"},
		User:     recipe.User{Name: "student", Sudo: true},
		WSL:      recipe.WSL{Systemd: true, DefaultUser: "student"},
		Locale:   recipe.Locale{Lang: "en_US.UTF-8", Timezone: "Europe/Istanbul"},
		Packages: recipe.Packages{Include: []string{"git", "build-essential", "cmake"}},
	}
}

func TestMergeIncludeAddsEssentialsIncludingSystemd(t *testing.T) {
	got := MergeInclude([]string{"git", "sudo"})
	for _, need := range []string{"git", "sudo", "systemd", "systemd-sysv", "dbus", "locales", "tzdata", "passwd", "ca-certificates"} {
		if !contains(got, need) {
			t.Fatalf("missing %s in %v", need, got)
		}
	}
	if count(got, "sudo") != 1 {
		t.Fatalf("sudo duplicated: %v", got)
	}
}

func TestMergeIncludeKeepsUserOrderFirst(t *testing.T) {
	got := MergeInclude([]string{"cmake", "git", "cmake"})
	if len(got) < 2 || got[0] != "cmake" || got[1] != "git" || count(got, "cmake") != 1 {
		t.Fatalf("got %v", got)
	}
	if len(MergeInclude(nil)) != len(Essentials) {
		t.Fatalf("empty include must still carry the essentials: %v", MergeInclude(nil))
	}
}

func TestRenderWSLConf(t *testing.T) {
	body := RenderWSLConf(testRecipe())
	want := "[boot]\nsystemd=true\n\n[user]\ndefault=student\n\n[time]\nuseWindowsTimezone=false\n"
	if body != want {
		t.Fatalf("wsl.conf:\n%s\nwant:\n%s", body, want)
	}
}

func TestRenderWSLConfPinsTimezone(t *testing.T) {
	// Task 0 spike: without this WSL replaces the recipe's timezone with the
	// Windows one every time the distro starts.
	body := RenderWSLConf(testRecipe())
	if !strings.Contains(body, "[time]\nuseWindowsTimezone=false\n") {
		t.Fatalf("wsl.conf must pin the recipe timezone:\n%s", body)
	}
}

func TestRenderWSLConfWithoutSystemd(t *testing.T) {
	r := testRecipe()
	r.WSL.Systemd = false
	body := RenderWSLConf(r)
	if strings.Contains(body, "systemd") || strings.Contains(body, "[boot]") {
		t.Fatalf("wsl.conf: %s", body)
	}
	if !strings.Contains(body, "default=student") {
		t.Fatalf("wsl.conf: %s", body)
	}
}

func TestRenderWSLConfDefaultUserFallsBack(t *testing.T) {
	r := testRecipe()
	r.WSL.DefaultUser = ""
	if body := RenderWSLConf(r); !strings.Contains(body, "default=student") {
		t.Fatalf("wsl.conf: %s", body)
	}
}

func TestRenderSudoers(t *testing.T) {
	if got := RenderSudoers(testRecipe()); got != "student ALL=(ALL) NOPASSWD:ALL\n" {
		t.Fatalf("sudoers: %q", got)
	}
	r := testRecipe()
	r.User.Sudo = false
	if got := RenderSudoers(r); got != "" {
		t.Fatalf("no sudo means no sudoers file, got %q", got)
	}
}

func TestWriteStageWritesRenderedFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "stage")
	r := testRecipe()
	st, err := WriteStage(dir, r)
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		st.WSLConf:   RenderWSLConf(r),
		st.Sudoers:   RenderSudoers(r),
		st.Provision: RenderProvision(r),
	} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != want {
			t.Fatalf("%s: got %q", path, body)
		}
	}
	if st.StatusOut != filepath.Join(dir, "dpkg-status") {
		t.Fatalf("status path %q", st.StatusOut)
	}
}

func TestWriteStageNoSudo(t *testing.T) {
	r := testRecipe()
	r.User.Sudo = false
	st, err := WriteStage(t.TempDir(), r)
	if err != nil {
		t.Fatal(err)
	}
	if st.Sudoers != "" {
		t.Fatalf("sudoers should not be staged: %q", st.Sudoers)
	}
}

func TestSQSurvivesTheShell(t *testing.T) {
	// sq is the second line of defence after validate. Prove it with a real
	// shell rather than by eyeballing the escaping.
	for _, v := range []string{
		"student", "", "it's", `a"b`, "$(touch /tmp/pwned)", "`id`", "a b\tc", "x'; rm -rf / #", `back\slash`, "new\nline",
	} {
		out, err := exec.Command("sh", "-c", "printf %s "+sq(v)).Output()
		if err != nil {
			t.Fatalf("%q: %v", v, err)
		}
		if string(out) != v {
			t.Fatalf("sq(%q) came back as %q", v, out)
		}
	}
}

func stageFor(dir string) Stage {
	return Stage{
		WSLConf:   filepath.Join(dir, "wsl.conf"),
		Sudoers:   filepath.Join(dir, "sudoers"),
		Provision: filepath.Join(dir, "provision.sh"),
		StatusOut: filepath.Join(dir, "dpkg-status"),
	}
}

func TestCustomizeHooksOrderAndContent(t *testing.T) {
	hooks := CustomizeHooks(stageFor("/w/stage"))
	want := []string{
		"upload '/w/stage/wsl.conf' /etc/wsl.conf",
		"upload '/w/stage/sudoers' /etc/sudoers.d/90-frostroot",
		`chroot "$1" /bin/sh -c "$(cat '/w/stage/provision.sh')" frostroot-provision`,
		// Last, so the status reflects everything installed.
		"download /var/lib/dpkg/status '/w/stage/dpkg-status'",
	}
	if strings.Join(hooks, "\n") != strings.Join(want, "\n") {
		t.Fatalf("hooks:\n%s\nwant:\n%s", strings.Join(hooks, "\n"), strings.Join(want, "\n"))
	}
}

func TestCustomizeHooksNoSudoOmitsSudoers(t *testing.T) {
	st := stageFor("/w/stage")
	st.Sudoers = ""
	all := strings.Join(CustomizeHooks(st), "\n")
	if strings.Contains(all, "sudoers") {
		t.Fatalf("no sudo means no sudoers hook:\n%s", all)
	}
}

func TestCustomizeHooksNeverSwallowFailures(t *testing.T) {
	all := strings.Join(CustomizeHooks(stageFor("/w/stage")), "\n") + "\n" + RenderProvision(testRecipe())
	for _, bad := range []string{"|| true", "|| :", "set +e"} {
		if strings.Contains(all, bad) {
			t.Fatalf("hooks must fail closed, found %q:\n%s", bad, all)
		}
	}
}

func TestProvisionHookRunsTheStagedScriptWithHostilePaths(t *testing.T) {
	// The work directory comes from XDG_CACHE_HOME, so its path is not ours to
	// choose. Execute the real hook text with a stand-in for chroot and make
	// sure the staged script, and only it, runs.
	dir := filepath.Join(t.TempDir(), `it's a "dir" $(touch pwned) `+"`id`")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	st := stageFor(dir)
	if err := os.WriteFile(st.Provision, []byte("echo \"provisioned as $0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var hook string
	for _, h := range CustomizeHooks(st) {
		if strings.HasPrefix(h, "chroot ") {
			hook = h
		}
	}
	if hook == "" {
		t.Fatal("no chroot hook")
	}
	// mmdebstrap runs shell hooks as: sh -c HOOK exec ROOTDIR
	script := `chroot() { shift; "$@"; }` + "\n" + hook
	cmd := exec.Command("sh", "-c", script, "exec", "/fake/root")
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if string(out) != "provisioned as frostroot-provision\n" {
		t.Fatalf("got %q", out)
	}
	if _, err := os.Stat(filepath.Join(cmd.Dir, "pwned")); !os.IsNotExist(err) {
		t.Fatal("a stage path was executed as shell")
	}
}

func TestRenderProvisionContent(t *testing.T) {
	body := RenderProvision(testRecipe())
	for _, need := range []string{
		"set -eu",
		"user='student'",
		"lang='en_US.UTF-8'",
		"charmap='UTF-8'",
		"want='en_us.utf8'",
		"tz='Europe/Istanbul'",
		`test -f "/usr/share/zoneinfo/$tz"`,
		`ln -sfn "/usr/share/zoneinfo/$tz" /etc/localtime`,
		"> /etc/timezone",
		`useradd --create-home --shell /bin/bash --user-group "$user"`,
		"chmod 0440 /etc/sudoers.d/90-frostroot",
		"visudo -cqf /etc/sudoers.d/90-frostroot",
		">> /etc/locale.gen",
		"locale-gen",
		`update-locale "LANG=$lang"`,
		"rm -f /etc/resolv.conf /etc/hostname",
	} {
		if !strings.Contains(body, need) {
			t.Fatalf("provision script missing %q:\n%s", need, body)
		}
	}
}

func TestRenderProvisionChecksResultsNotExitCodes(t *testing.T) {
	// locale-gen exits 0 without generating anything when /etc/locale.gen is
	// empty, and validate cannot see tzdata. Both must be verified in the image.
	body := RenderProvision(testRecipe())
	if strings.Count(body, "have_locale") < 3 {
		t.Fatalf("expected the locale to be checked before and after locale-gen:\n%s", body)
	}
	if !strings.Contains(body, "does not exist in this image") || !strings.Contains(body, "was not generated") {
		t.Fatalf("expected explicit failures:\n%s", body)
	}
}

func TestRenderProvisionNoSudo(t *testing.T) {
	r := testRecipe()
	r.User.Sudo = false
	if body := RenderProvision(r); strings.Contains(body, "sudoers") || strings.Contains(body, "visudo") {
		t.Fatalf("no sudo means no sudoers handling:\n%s", body)
	}
}

func TestRenderProvisionDefaults(t *testing.T) {
	r := testRecipe()
	r.Locale = recipe.Locale{}
	body := RenderProvision(r)
	for _, need := range []string{"lang='en_US.UTF-8'", "tz='UTC'", "want='en_us.utf8'"} {
		if !strings.Contains(body, need) {
			t.Fatalf("missing %q:\n%s", need, body)
		}
	}
}

func TestRenderProvisionLocaleNames(t *testing.T) {
	for _, tc := range []struct{ lang, charmap, want string }{
		{"en_US.UTF-8", "UTF-8", "en_us.utf8"},
		{"tr_TR.utf8", "UTF-8", "tr_tr.utf8"},
		{"C.UTF-8", "UTF-8", "c.utf8"},
		{"de_DE.ISO-8859-1", "ISO-8859-1", "de_de.iso88591"},
	} {
		r := testRecipe()
		r.Locale.Lang = tc.lang
		body := RenderProvision(r)
		if !strings.Contains(body, "charmap='"+tc.charmap+"'") || !strings.Contains(body, "want='"+tc.want+"'") {
			t.Fatalf("%s: wrong charmap or locale id:\n%s", tc.lang, body)
		}
	}
}

func TestRenderProvisionIsValidShell(t *testing.T) {
	for _, sudo := range []bool{true, false} {
		r := testRecipe()
		r.User.Sudo = sudo
		cmd := exec.Command("sh", "-n")
		cmd.Stdin = strings.NewReader(RenderProvision(r))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("sudo=%v: %v: %s", sudo, err, out)
		}
	}
}

func contains(xs []string, w string) bool {
	for _, x := range xs {
		if x == w {
			return true
		}
	}
	return false
}

func count(xs []string, w string) int {
	n := 0
	for _, x := range xs {
		if x == w {
			n++
		}
	}
	return n
}
