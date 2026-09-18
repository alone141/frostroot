package builder

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"frostroot/internal/distro"
	"frostroot/internal/recipe"
)

// fakeKeyPacket is the smallest OpenPGP public key pgp accepts.
var fakeKeyPacket = []byte{0x99, 0x00, 0x03, 0x04, 0x00, 0x00}

// sampleSources are two extra sources: Docker on the release suite with a
// component, and a PPA on the default suite and component.
func sampleSources() []recipe.Source {
	return []recipe.Source{
		{Name: "docker", URL: "https://download.docker.com/linux/ubuntu/", Components: []string{"stable"}, Key: "keys/docker.asc"},
		{Name: "ppa-git-core-ppa", URL: "https://ppa.launchpadcontent.net/git-core/ppa/ubuntu", Key: "keys/ppa-git-core-ppa.asc"},
	}
}

// writeSampleKeys puts a fake key file for every sample source into
// recipeDir.
func writeSampleKeys(t *testing.T, recipeDir string) {
	t.Helper()
	for _, source := range sampleSources() {
		path := recipe.KeyPath(recipeDir, source)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, fakeKeyPacket, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// sampleAptListsWithSources adds Docker's and the PPA's indexes to the
// archive's: docker-ce from Docker, and git from the PPA, superseding the
// archive's git.
func sampleAptListsWithSources() map[string]string {
	lists := sampleAptLists()
	lists["download.docker.com_linux_ubuntu_dists_jammy_stable_binary-amd64_Packages"] = "Package: docker-ce\nArchitecture: amd64\nVersion: 5:27.0.3-1~ubuntu.22.04~jammy\nFilename: dists/jammy/pool/stable/amd64/docker-ce_27.0.3-1~ubuntu.22.04~jammy_amd64.deb\nSize: 25000000\nSHA256: dddd\n"
	lists["ppa.launchpadcontent.net_git-core_ppa_ubuntu_dists_jammy_main_binary-amd64_Packages"] = "Package: git\nArchitecture: amd64\nVersion: 1:2.46.0-0ppa1~ubuntu22.04.1\nFilename: pool/main/g/git/git_2.46.0-0ppa1~ubuntu22.04.1_amd64.deb\nSize: 4000000\nSHA256: 9999\n"
	return lists
}

// sampleDpkgStatusWithSources lists the packages the indexes above provide.
const sampleDpkgStatusWithSources = "Package: libc6\nStatus: install ok installed\nArchitecture: amd64\nVersion: 2.35-0ubuntu3.8\n\n" +
	"Package: git\nStatus: install ok installed\nArchitecture: amd64\nVersion: 1:2.46.0-0ppa1~ubuntu22.04.1\n\n" +
	"Package: docker-ce\nStatus: install ok installed\nArchitecture: amd64\nVersion: 5:27.0.3-1~ubuntu.22.04~jammy\n"

func TestSourceLines(t *testing.T) {
	release, err := distro.Lookup("22.04", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	lines := SourceLines(release, "", sampleSources(), "/etc/apt/keyrings")
	want := []string{
		"deb http://archive.ubuntu.com/ubuntu jammy main restricted universe multiverse",
		"deb http://archive.ubuntu.com/ubuntu jammy-updates main restricted universe multiverse",
		"deb http://archive.ubuntu.com/ubuntu jammy-security main restricted universe multiverse",
		"deb [signed-by=/etc/apt/keyrings/frostroot-docker.gpg] https://download.docker.com/linux/ubuntu jammy stable",
		"deb [signed-by=/etc/apt/keyrings/frostroot-ppa-git-core-ppa.gpg] https://ppa.launchpadcontent.net/git-core/ppa/ubuntu jammy main",
	}
	if !slices.Equal(lines, want) {
		t.Errorf("SourceLines =\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	mirrored := SourceLines(release, "http://mirror.example/ubuntu", sampleSources(), "/w/stage/keys")
	if !strings.HasPrefix(mirrored[0], "deb http://mirror.example/ubuntu jammy ") || !strings.Contains(mirrored[3], "signed-by=/w/stage/keys/frostroot-docker.gpg] https://download.docker.com") {
		t.Errorf("mirror must replace the archive only, keyringDir the key paths:\n%s", strings.Join(mirrored, "\n"))
	}
	if plain := SourceLines(release, "", nil, "/etc/apt/keyrings"); len(plain) != 3 {
		t.Errorf("without sources: %q", plain)
	}
}

func TestIndexOrigins(t *testing.T) {
	release, _ := distro.Lookup("22.04", "amd64")
	origins := indexOrigins(release, "http://archive.ubuntu.com/ubuntu", sampleSources())
	if got := aptListPrefix("https://download.docker.com/linux/ubuntu/"); got != "download.docker.com_linux_ubuntu" {
		t.Errorf("aptListPrefix = %q", got)
	}
	if got := aptListPrefix("http://127.0.0.1:8099"); got != "127.0.0.1:8099" {
		t.Errorf("aptListPrefix with a port = %q", got)
	}
	testCases := map[string]string{
		"archive.ubuntu.com_ubuntu_dists_jammy_main_binary-amd64_Packages":                    "",
		"archive.ubuntu.com_ubuntu_dists_jammy-security_universe_binary-amd64_Packages":       "",
		"download.docker.com_linux_ubuntu_dists_jammy_stable_binary-amd64_Packages":           "docker",
		"ppa.launchpadcontent.net_git-core_ppa_ubuntu_dists_jammy_main_binary-amd64_Packages": "ppa-git-core-ppa",
		"ppa.launchpadcontent.net_git-core_ppa_ubuntu_dists_noble_main_binary-amd64_Packages": "!",
		"download.docker.com_linux_ubuntu_dists_jammy-updates_stable_binary-amd64_Packages":   "!",
		"evil.example_dists_jammy_main_binary-amd64_Packages":                                 "!",
		"archive.ubuntu.com_ubuntu-ports_dists_jammy_main_binary-amd64_Packages":              "!",
		"archive.ubuntu.com_ubuntu_dists_jammy_main_binary-amd64_Packages.gz":                 "",
	}
	for fileName, wantSource := range testCases {
		origin, known := originOf(fileName, origins)
		if wantSource == "!" {
			if known {
				t.Errorf("%s: attributed to %q, want unknown", fileName, origin.source)
			}
			continue
		}
		if !known || origin.source != wantSource {
			t.Errorf("%s: origin %+v known %v, want source %q", fileName, origin, known, wantSource)
		}
	}
	// Ranking: security > updates > release > first source > second source.
	ranks := map[string]int{}
	for _, origin := range origins {
		ranks[origin.source+"|"+origin.suite] = origin.rank
	}
	order := []int{ranks["ppa-git-core-ppa|jammy"], ranks["docker|jammy"], ranks["|jammy"], ranks["|jammy-updates"], ranks["|jammy-security"]}
	if !slices.IsSorted(order) || slices.Compact(slices.Clone(order))[0] != order[0] || len(slices.Compact(slices.Clone(order))) != len(order) {
		t.Errorf("ranks = %v, want strictly increasing from the last source to the security pocket", ranks)
	}
}

func TestBuildWithSources(t *testing.T) {
	options, _ := newTestOptions(t)
	options.KeepWork = true // the stage is inspected below
	writeSampleKeys(t, options.RecipeDir)
	imageRecipe := sampleRecipe()
	imageRecipe.Sources = sampleSources()
	imageRecipe.Packages.Include = []string{"git", "docker-ce"}
	bootstrapper := &fakeBootstrapper{dpkgStatus: sampleDpkgStatusWithSources, aptLists: sampleAptListsWithSources()}
	result, err := (&Builder{Bootstrapper: bootstrapper}).Build(context.Background(), imageRecipe, options)
	if err != nil {
		t.Fatal(err)
	}
	spec := bootstrapper.lastSpec
	stageKeys := filepath.Join(spec.WorkDir, "stage", "keys")
	// mmdebstrap gets the archive lines plus one per source naming the staged key.
	if len(spec.SourceLines) != 5 || spec.SourceLines[3] != "deb [signed-by="+stageKeys+"/frostroot-docker.gpg] https://download.docker.com/linux/ubuntu jammy stable" {
		t.Errorf("SourceLines = %q", spec.SourceLines)
	}
	for _, name := range []string{"docker", "ppa-git-core-ppa"} {
		keyContent, err := os.ReadFile(filepath.Join(stageKeys, KeyringFileName(name)))
		if err != nil || string(keyContent) != string(fakeKeyPacket) {
			t.Errorf("staged key for %s: %v, %x", name, err, keyContent)
		}
	}
	hooks := strings.Join(spec.CustomizeHooks, "\n")
	for _, wantHook := range []string{
		`mkdir -p "$1/etc/apt/keyrings"`,
		"upload '" + stageKeys + "/frostroot-docker.gpg' /etc/apt/keyrings/frostroot-docker.gpg",
		"upload '" + stageKeys + "/frostroot-ppa-git-core-ppa.gpg' /etc/apt/keyrings/frostroot-ppa-git-core-ppa.gpg",
		"' /etc/apt/sources.list",
	} {
		if !strings.Contains(hooks, wantHook) {
			t.Errorf("hooks lack %q:\n%s", wantHook, hooks)
		}
	}
	if mkdirIndex, uploadIndex := strings.Index(hooks, "mkdir -p"), strings.Index(hooks, "frostroot-docker.gpg"); mkdirIndex > uploadIndex {
		t.Error("the keyring directory must be created before the keys are uploaded")
	}
	sourcesList, err := os.ReadFile(filepath.Join(spec.WorkDir, "stage", "sources.list"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sourcesList), "signed-by=/etc/apt/keyrings/frostroot-docker.gpg] https://download.docker.com") || strings.Contains(string(sourcesList), spec.WorkDir) {
		t.Errorf("the image's sources.list must name the image's keyrings, never the stage:\n%s", sourcesList)
	}

	lock, err := recipe.LoadLock(result.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(lock.Sources) != 5 || !strings.Contains(lock.Sources[4], "signed-by=/etc/apt/keyrings/frostroot-ppa-git-core-ppa.gpg") {
		t.Errorf("lock sources = %q, want the image's five lines", lock.Sources)
	}
	wantRepositories := []recipe.LockRepository{ // the key checksum is checked for shape below
		{Name: "docker", URL: "https://download.docker.com/linux/ubuntu", Suite: "jammy", Components: []string{"stable"}},
		{Name: "ppa-git-core-ppa", URL: "https://ppa.launchpadcontent.net/git-core/ppa/ubuntu", Suite: "jammy", Components: []string{"main"}},
	}
	if len(lock.Repositories) != 2 {
		t.Fatalf("Repositories = %+v", lock.Repositories)
	}
	for index, want := range wantRepositories {
		got := lock.Repositories[index]
		if got.Name != want.Name || got.URL != want.URL || got.Suite != want.Suite || !slices.Equal(got.Components, want.Components) || len(got.KeySHA256) != 64 {
			t.Errorf("repository %d = %+v, want %+v (with a 64-digit key checksum)", index, got, want)
		}
	}
	sourceOf := map[string]string{}
	for _, locked := range lock.Packages {
		sourceOf[locked.Name] = locked.Source
	}
	if sourceOf["docker-ce"] != "docker" || sourceOf["git"] != "ppa-git-core-ppa" || sourceOf["libc6"] != "" {
		t.Errorf("package sources = %v", sourceOf)
	}
	for _, locked := range lock.Packages {
		if locked.Name == "docker-ce" && locked.Filename != "dists/jammy/pool/stable/amd64/docker-ce_27.0.3-1~ubuntu.22.04~jammy_amd64.deb" {
			t.Errorf("docker-ce filename = %q, want Docker's own layout", locked.Filename)
		}
	}
}

func TestBuildRefusesUnusableSourceKeys(t *testing.T) {
	testCases := []struct {
		name    string
		prepare func(t *testing.T, recipeDir string)
	}{
		{name: "key missing", prepare: func(*testing.T, string) {}},
		{name: "key is not a key", prepare: func(t *testing.T, recipeDir string) {
			t.Helper()
			writeSampleKeys(t, recipeDir)
			if err := os.WriteFile(filepath.Join(recipeDir, "keys", "docker.asc"), []byte("<html>"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			options, workRoot := newTestOptions(t)
			testCase.prepare(t, options.RecipeDir)
			imageRecipe := sampleRecipe()
			imageRecipe.Sources = sampleSources()
			bootstrapper := &fakeBootstrapper{}
			result, err := (&Builder{Bootstrapper: bootstrapper}).Build(context.Background(), imageRecipe, options)
			if !errors.Is(err, ErrSourceKey) || !strings.Contains(err.Error(), "docker") {
				t.Fatalf("Build error = %v, want ErrSourceKey naming docker", err)
			}
			if bootstrapper.runCount != 0 || result.WorkDir != "" {
				t.Error("nothing may run without the keys")
			}
			assertNotCreated(t, workRoot)
		})
	}
}

func TestBuildOfflineComparesSources(t *testing.T) {
	options, _ := newTestOptions(t)
	writeSampleKeys(t, options.RecipeDir)
	imageRecipe := sampleRecipe()
	imageRecipe.Sources = sampleSources()
	imageRecipe.Packages.Include = []string{"git", "docker-ce"}
	bootstrapper := &fakeBootstrapper{dpkgStatus: sampleDpkgStatusWithSources, aptLists: sampleAptListsWithSources()}
	if _, err := (&Builder{Bootstrapper: bootstrapper}).Build(context.Background(), imageRecipe, options); err != nil {
		t.Fatal(err)
	}
	release, _ := distro.Lookup("22.04", "amd64")
	lock, err := recipe.LoadLock(filepath.Join(options.RecipeDir, LockFileName))
	if err != nil {
		t.Fatal(err)
	}
	if differences := repositoryDifferences(lock, imageRecipe.Sources, release); len(differences) != 0 {
		t.Errorf("an unchanged recipe differs: %q", differences)
	}
	changed := sampleSources()
	changed[0].Components = []string{"test"}
	added := append(sampleSources(), recipe.Source{Name: "corp", URL: "https://apt.corp.example", Key: "keys/corp.asc"})
	removed := sampleSources()[:1]
	for name, sources := range map[string][]recipe.Source{"changed": changed, "added": added, "removed": removed} {
		differences := repositoryDifferences(lock, sources, release)
		if len(differences) != 1 || !strings.Contains(differences[0], "source "+name) {
			t.Errorf("%s: differences = %q", name, differences)
		}
	}
	// And the offline build itself refuses.
	options.Offline = true
	imageRecipe.Sources = changed
	_, err = (&Builder{Bootstrapper: &offlineFakeBootstrapper{}}).Build(context.Background(), imageRecipe, options)
	if !errors.Is(err, ErrLockMismatch) || !strings.Contains(err.Error(), "source changed in the recipe: docker") {
		t.Errorf("offline build error = %v", err)
	}
}
