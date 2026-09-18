package builder

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"frostroot/internal/recipe"
)

// sampleRecipe returns a valid recipe with sudo, systemd and a non-default
// timezone.
func sampleRecipe() recipe.Recipe {
	return recipe.Recipe{
		Image:    recipe.Image{Name: "cpp-lab", Release: "22.04", Arch: "amd64"},
		User:     recipe.User{Name: "student", Sudo: true},
		WSL:      recipe.WSL{Systemd: true, DefaultUser: "student"},
		Locale:   recipe.Locale{Lang: "en_US.UTF-8", Timezone: "Europe/Istanbul"},
		Packages: recipe.Packages{Include: []string{"git", "build-essential", "cmake"}},
	}
}

// sampleStage returns a Stage whose files are all in stageDir.
func sampleStage(stageDir string) Stage {
	return Stage{
		WSLConfPath:         filepath.Join(stageDir, "wsl.conf"),
		SudoersPath:         filepath.Join(stageDir, "sudoers"),
		ProvisionScriptPath: filepath.Join(stageDir, "provision.sh"),
		DpkgStatusPath:      filepath.Join(stageDir, "dpkg-status"),
	}
}

// renderProvisionScript renders the provision script for imageRecipe, failing
// the test on error.
func renderProvisionScript(t *testing.T, imageRecipe recipe.Recipe) string {
	t.Helper()
	script, err := RenderProvisionScript(imageRecipe)
	if err != nil {
		t.Fatal(err)
	}
	return script
}

func TestPackagesToInstallAddsEssentialsOnce(t *testing.T) {
	packages := PackagesToInstall(recipe.Recipe{Packages: recipe.Packages{Include: []string{"git", "sudo"}}})
	for _, wantPackage := range []string{"git", "sudo", "systemd", "systemd-sysv", "dbus", "locales", "tzdata", "passwd", "ca-certificates"} {
		if !slices.Contains(packages, wantPackage) {
			t.Errorf("PackagesToInstall = %q, missing %s", packages, wantPackage)
		}
	}
	sudoIndex := slices.Index(packages, "sudo")
	if slices.Contains(packages[sudoIndex+1:], "sudo") {
		t.Errorf("PackagesToInstall = %q, lists sudo twice", packages)
	}
}

func TestPackagesToInstallKeepsRequestedOrderFirst(t *testing.T) {
	packages := PackagesToInstall(recipe.Recipe{Packages: recipe.Packages{Include: []string{"cmake", "git", "cmake"}}})
	if len(packages) < 2 || packages[0] != "cmake" || packages[1] != "git" || slices.Contains(packages[2:], "cmake") {
		t.Errorf("PackagesToInstall = %q, want cmake, git, then the essentials", packages)
	}
	if got := PackagesToInstall(recipe.Recipe{}); !slices.Equal(got, EssentialPackages) {
		t.Errorf("PackagesToInstall(empty recipe) = %q, want exactly the essential packages", got)
	}
}

func TestPackagesToInstallAddsWhatAVirtualEnvironmentNeeds(t *testing.T) {
	withoutPython := PackagesToInstall(recipe.Recipe{Packages: recipe.Packages{Include: []string{"git"}}})
	for _, unwanted := range PythonPackages {
		if slices.Contains(withoutPython, unwanted) {
			t.Errorf("PackagesToInstall = %q, want no %s for a recipe without python packages", withoutPython, unwanted)
		}
	}

	imageRecipe := recipe.Recipe{
		Packages: recipe.Packages{Include: []string{"git"}},
		Python:   &recipe.Python{Include: []string{"numpy"}},
	}
	packages := PackagesToInstall(imageRecipe)
	for _, wantPackage := range PythonPackages {
		if !slices.Contains(packages, wantPackage) {
			t.Errorf("PackagesToInstall = %q, missing %s", packages, wantPackage)
		}
	}
	if packages[0] != "git" {
		t.Errorf("PackagesToInstall = %q, want the requested packages first", packages)
	}
	// numpy is not an apt package: it must never reach mmdebstrap.
	if slices.Contains(packages, "numpy") {
		t.Errorf("PackagesToInstall = %q, want no PyPI name in the apt list", packages)
	}
}

func TestRenderWSLConf(t *testing.T) {
	testCases := []struct {
		name   string
		adjust func(*recipe.Recipe)
		want   string
	}{
		{
			// The [time] section is from the Task 0 spike: without it WSL
			// replaces the recipe's timezone with the Windows one every time
			// the distribution starts.
			name:   "systemd and sudo",
			adjust: func(*recipe.Recipe) {},
			want:   "[boot]\nsystemd=true\n\n[user]\ndefault=student\n\n[time]\nuseWindowsTimezone=false\n",
		},
		{
			name:   "without systemd",
			adjust: func(imageRecipe *recipe.Recipe) { imageRecipe.WSL.Systemd = false },
			want:   "[user]\ndefault=student\n\n[time]\nuseWindowsTimezone=false\n",
		},
		{
			name:   "default user falls back to the user name",
			adjust: func(imageRecipe *recipe.Recipe) { imageRecipe.WSL.DefaultUser = "" },
			want:   "[boot]\nsystemd=true\n\n[user]\ndefault=student\n\n[time]\nuseWindowsTimezone=false\n",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			imageRecipe := sampleRecipe()
			testCase.adjust(&imageRecipe)
			if got := RenderWSLConf(imageRecipe); got != testCase.want {
				t.Fatalf("RenderWSLConf =\n%s\nwant\n%s", got, testCase.want)
			}
		})
	}
}

func TestRenderSudoers(t *testing.T) {
	if got := RenderSudoers(sampleRecipe()); got != "student ALL=(ALL) NOPASSWD:ALL\n" {
		t.Errorf("RenderSudoers = %q", got)
	}
	withoutSudo := sampleRecipe()
	withoutSudo.User.Sudo = false
	if got := RenderSudoers(withoutSudo); got != "" {
		t.Errorf("RenderSudoers without sudo = %q, want no file", got)
	}
}

func TestWriteStageWritesRenderedFiles(t *testing.T) {
	stageDir := filepath.Join(t.TempDir(), "stage")
	imageRecipe := sampleRecipe()
	stage, err := WriteStage(stageDir, imageRecipe, StageOptions{RecordForLock: true})
	if err != nil {
		t.Fatal(err)
	}
	wantContentByPath := map[string]string{
		stage.WSLConfPath:         RenderWSLConf(imageRecipe),
		stage.SudoersPath:         RenderSudoers(imageRecipe),
		stage.ProvisionScriptPath: renderProvisionScript(t, imageRecipe),
	}
	for path, wantContent := range wantContentByPath {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != wantContent {
			t.Errorf("%s holds %q, want %q", path, content, wantContent)
		}
	}
	if stage.DpkgStatusPath != filepath.Join(stageDir, "dpkg-status") {
		t.Errorf("DpkgStatusPath = %q", stage.DpkgStatusPath)
	}
	if stage.AptListsDir != filepath.Join(stageDir, "lists") || stage.SourcesListPath != "" {
		t.Errorf("AptListsDir = %q, SourcesListPath = %q; want the lists inside the stage and no sources.list", stage.AptListsDir, stage.SourcesListPath)
	}
	if stage.ExtendedStatesPath != filepath.Join(stageDir, "extended-states") || stage.AutoMarksPath != "" {
		t.Errorf("ExtendedStatesPath = %q, AutoMarksPath = %q; want the image's marks downloaded into the stage and none uploaded", stage.ExtendedStatesPath, stage.AutoMarksPath)
	}
}

func TestWriteStageWithoutSudo(t *testing.T) {
	imageRecipe := sampleRecipe()
	imageRecipe.User.Sudo = false
	stage, err := WriteStage(t.TempDir(), imageRecipe, StageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if stage.SudoersPath != "" {
		t.Fatalf("SudoersPath = %q, want no sudoers file staged", stage.SudoersPath)
	}
	if stage.AptListsDir != "" || stage.ExtendedStatesPath != "" {
		t.Fatalf("AptListsDir = %q, ExtendedStatesPath = %q; want neither when not recording for a lock", stage.AptListsDir, stage.ExtendedStatesPath)
	}
}

func TestWriteStageAutoMarksForOfflineBuilds(t *testing.T) {
	// An offline build restores apt's auto marks from the lock: the rendered
	// extended_states is staged and uploaded right before the status
	// download, after everything else has been installed and provisioned.
	autoMarks := "Package: libc6\nArchitecture: amd64\nAuto-Installed: 1\n\n"
	stage, err := WriteStage(t.TempDir(), sampleRecipe(), StageOptions{AutoMarks: autoMarks})
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(stage.AutoMarksPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != autoMarks {
		t.Errorf("staged auto marks = %q, want the rendered file as given", content)
	}
	hooks := CustomizeHooks(stage)
	if len(hooks) < 2 || hooks[len(hooks)-2] != "upload "+shellQuote(stage.AutoMarksPath)+" /var/lib/apt/extended_states" || !strings.HasPrefix(hooks[len(hooks)-1], "download /var/lib/dpkg/status ") {
		t.Errorf("hooks = %q, want the upload of the auto marks right before the status download", hooks)
	}
	if strings.Contains(strings.Join(hooks, "\n"), "download /var/lib/apt/extended_states") {
		t.Errorf("hooks = %q, want no download of the marks offline: the lock has them", hooks)
	}

	without, err := WriteStage(t.TempDir(), sampleRecipe(), StageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if without.AutoMarksPath != "" || strings.Contains(strings.Join(CustomizeHooks(without), "\n"), "extended_states") {
		t.Errorf("with no marks to restore nothing is staged or uploaded: %+v", without)
	}
}

func TestWriteStageSourcesListForOfflineBuilds(t *testing.T) {
	sourceLines := []string{"deb http://archive.ubuntu.com/ubuntu jammy main universe", "deb http://archive.ubuntu.com/ubuntu jammy-updates main universe"}
	stage, err := WriteStage(t.TempDir(), sampleRecipe(), StageOptions{SourceLines: sourceLines})
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(stage.SourcesListPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != strings.Join(sourceLines, "\n")+"\n" {
		t.Errorf("sources.list = %q", content)
	}
	hooks := CustomizeHooks(stage)
	uploadIndex := slices.IndexFunc(hooks, func(hook string) bool { return strings.HasSuffix(hook, " /etc/apt/sources.list") })
	provisionIndex := slices.IndexFunc(hooks, func(hook string) bool { return strings.HasPrefix(hook, "chroot ") })
	if uploadIndex < 0 || uploadIndex > provisionIndex {
		t.Errorf("hooks = %q, want the sources.list uploaded before provisioning", hooks)
	}
}

func TestShellQuoteSurvivesTheShell(t *testing.T) {
	// shellQuote is the second line of defense after recipe validation. Prove
	// it with a real shell rather than by reading the escaping.
	hostileValues := []string{
		"student", "", "it's", `a"b`, "$(touch /tmp/pwned)", "`id`", "a b\tc", "x'; rm -rf / #", `back\slash`, "new\nline",
	}
	for _, value := range hostileValues {
		printed, err := exec.Command("sh", "-c", "printf %s "+shellQuote(value)).Output()
		if err != nil {
			t.Fatalf("shellQuote(%q): %v", value, err)
		}
		if string(printed) != value {
			t.Errorf("shellQuote(%q) came back from the shell as %q", value, printed)
		}
	}
}

func TestCustomizeHooksOrderAndContent(t *testing.T) {
	stage := sampleStage("/w/stage")
	stage.AptListsDir = "/w/stage/lists"
	stage.ExtendedStatesPath = "/w/stage/extended-states"
	hooks := CustomizeHooks(stage)
	wantHooks := []string{
		"upload '/w/stage/wsl.conf' /etc/wsl.conf",
		"upload '/w/stage/sudoers' /etc/sudoers.d/90-frostroot",
		`chroot "$1" /bin/sh -c "$(cat '/w/stage/provision.sh')" frostroot-provision`,
		// The host's files go after everything that needs the network:
		// resolv.conf is how a hook resolves a name.
		`rm -f "$1/etc/resolv.conf" "$1/etc/hostname"`,
		// copy-out puts "lists" inside its destination, so the stage
		// directory is named, not the lists directory.
		"copy-out /var/lib/apt/lists '/w/stage'",
		// apt writes extended_states only once it has marked something, and
		// download fails on a missing file.
		`test -e "$1/var/lib/apt/extended_states" || touch "$1/var/lib/apt/extended_states"`,
		"download /var/lib/apt/extended_states '/w/stage/extended-states'",
		// Last, so that the status reflects everything installed.
		"download /var/lib/dpkg/status '/w/stage/dpkg-status'",
	}
	if !slices.Equal(hooks, wantHooks) {
		t.Fatalf("CustomizeHooks =\n%s\nwant\n%s", strings.Join(hooks, "\n"), strings.Join(wantHooks, "\n"))
	}
	if without := CustomizeHooks(sampleStage("/w/stage")); len(without) != 5 || strings.Contains(strings.Join(without, "\n"), "/var/lib/apt/") {
		t.Errorf("without AptListsDir and ExtendedStatesPath the hooks must not copy the lists out or touch apt's marks: %q", without)
	}
}

func TestCustomizeHooksWithoutSudoOmitSudoers(t *testing.T) {
	stage := sampleStage("/w/stage")
	stage.SudoersPath = ""
	if allHooks := strings.Join(CustomizeHooks(stage), "\n"); strings.Contains(allHooks, "sudoers") {
		t.Fatalf("hooks mention sudoers for a user without sudo:\n%s", allHooks)
	}
}

func TestHooksAndScriptNeverSwallowFailures(t *testing.T) {
	everything := strings.Join(CustomizeHooks(sampleStage("/w/stage")), "\n") + "\n" + renderProvisionScript(t, sampleRecipe())
	for _, failureSwallower := range []string{"|| true", "|| :", "set +e"} {
		if strings.Contains(everything, failureSwallower) {
			t.Errorf("hooks must fail closed, found %q", failureSwallower)
		}
	}
}

func TestProvisionHookRunsTheStagedScriptWithHostilePaths(t *testing.T) {
	// The work directory comes from XDG_CACHE_HOME, so its path is not ours to
	// choose. Run the real hook text with a stand-in for chroot and check that
	// the staged script, and only it, runs.
	stageDir := filepath.Join(t.TempDir(), `it's a "dir" $(touch pwned) `+"`id`")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stage := sampleStage(stageDir)
	if err := os.WriteFile(stage.ProvisionScriptPath, []byte("echo \"provisioned as $0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var provisionHook string
	for _, hook := range CustomizeHooks(stage) {
		if strings.HasPrefix(hook, "chroot ") {
			provisionHook = hook
		}
	}
	if provisionHook == "" {
		t.Fatal("CustomizeHooks has no chroot hook")
	}
	// mmdebstrap runs shell hooks as: sh -c HOOK exec ROOTDIR
	scriptWithFakeChroot := `chroot() { shift; "$@"; }` + "\n" + provisionHook
	command := exec.Command("sh", "-c", scriptWithFakeChroot, "exec", "/fake/root")
	command.Dir = t.TempDir()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("running the hook: %v: %s", err, output)
	}
	if string(output) != "provisioned as frostroot-provision\n" {
		t.Errorf("hook output = %q, want the staged script's output", output)
	}
	if _, err := os.Stat(filepath.Join(command.Dir, "pwned")); err == nil {
		t.Error("part of a stage path was executed as shell")
	}
}

func TestRenderProvisionScriptContent(t *testing.T) {
	script := renderProvisionScript(t, sampleRecipe())
	wantLines := []string{
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
	}
	for _, wantLine := range wantLines {
		if !strings.Contains(script, wantLine) {
			t.Errorf("provision script lacks %q", wantLine)
		}
	}
}

func TestRenderProvisionScriptChecksResultsNotExitCodes(t *testing.T) {
	// locale-gen exits 0 without generating anything when /etc/locale.gen is
	// empty, and validation cannot see tzdata. Both must be verified in the
	// image.
	script := renderProvisionScript(t, sampleRecipe())
	if strings.Count(script, "have_locale") < 3 {
		t.Error("the locale should be checked before and after locale-gen")
	}
	for _, failureMessage := range []string{"does not exist in this image", "was not generated"} {
		if !strings.Contains(script, failureMessage) {
			t.Errorf("provision script lacks the explicit failure %q", failureMessage)
		}
	}
}

func TestRenderProvisionScriptWithoutSudo(t *testing.T) {
	imageRecipe := sampleRecipe()
	imageRecipe.User.Sudo = false
	script := renderProvisionScript(t, imageRecipe)
	if strings.Contains(script, "sudoers") || strings.Contains(script, "visudo") {
		t.Errorf("provision script handles sudoers for a user without sudo:\n%s", script)
	}
}

func TestRenderProvisionScriptDefaults(t *testing.T) {
	imageRecipe := sampleRecipe()
	imageRecipe.Locale = recipe.Locale{}
	script := renderProvisionScript(t, imageRecipe)
	for _, wantLine := range []string{"lang='en_US.UTF-8'", "tz='UTC'", "want='en_us.utf8'"} {
		if !strings.Contains(script, wantLine) {
			t.Errorf("provision script lacks default %q", wantLine)
		}
	}
}

func TestRenderProvisionScriptLocaleNames(t *testing.T) {
	testCases := []struct {
		locale       string
		wantCharmap  string
		wantLocaleID string
	}{
		{locale: "en_US.UTF-8", wantCharmap: "UTF-8", wantLocaleID: "en_us.utf8"},
		{locale: "tr_TR.utf8", wantCharmap: "UTF-8", wantLocaleID: "tr_tr.utf8"},
		{locale: "C.UTF-8", wantCharmap: "UTF-8", wantLocaleID: "c.utf8"},
		{locale: "de_DE.ISO-8859-1", wantCharmap: "ISO-8859-1", wantLocaleID: "de_de.iso88591"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.locale, func(t *testing.T) {
			imageRecipe := sampleRecipe()
			imageRecipe.Locale.Lang = testCase.locale
			script := renderProvisionScript(t, imageRecipe)
			if !strings.Contains(script, "charmap='"+testCase.wantCharmap+"'") {
				t.Errorf("script lacks charmap %q", testCase.wantCharmap)
			}
			if !strings.Contains(script, "want='"+testCase.wantLocaleID+"'") {
				t.Errorf("script lacks locale id %q", testCase.wantLocaleID)
			}
		})
	}
}

func TestRenderProvisionScriptIsValidShell(t *testing.T) {
	for _, sudo := range []bool{true, false} {
		imageRecipe := sampleRecipe()
		imageRecipe.User.Sudo = sudo
		syntaxCheck := exec.Command("sh", "-n")
		syntaxCheck.Stdin = strings.NewReader(renderProvisionScript(t, imageRecipe))
		if output, err := syntaxCheck.CombinedOutput(); err != nil {
			t.Errorf("sh -n with sudo=%v: %v: %s", sudo, err, output)
		}
	}
	// Validation rejects a certificate file named like this; the quoting is
	// the second line of defense, and a real shell is what proves it.
	imageRecipe := sampleRecipe()
	imageRecipe.Certificates = &recipe.Certificates{Include: []string{"certs/corp.pem", "certs/x'; touch /tmp/pwned #.pem"}}
	syntaxCheck := exec.Command("sh", "-n")
	syntaxCheck.Stdin = strings.NewReader(renderProvisionScript(t, imageRecipe))
	if output, err := syntaxCheck.CombinedOutput(); err != nil {
		t.Errorf("sh -n with certificates: %v: %s", err, output)
	}
}
