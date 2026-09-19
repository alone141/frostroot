package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"frostroot/internal/distro"
	"frostroot/internal/form"
	"frostroot/internal/index"
	"frostroot/internal/index/indextest"
	"frostroot/internal/recipe"
)

// TestMain keeps every test of the package away from the cache of whoever
// runs it: the plain form reads a cached package index when there is one,
// and a developer who has run frostroot init has one.
func TestMain(m *testing.M) {
	cacheHome, err := os.MkdirTemp("", "frostroot-cli-test-cache-")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("XDG_CACHE_HOME", cacheHome); err != nil {
		panic(err)
	}
	exitCode := m.Run()
	_ = os.RemoveAll(cacheHome)
	os.Exit(exitCode)
}

var noblePackages = []indextest.Package{
	{Name: "ninja-build", Version: "1.11.1-2", Section: "universe/devel", Description: "small build system closest in spirit to Make"},
	{Name: "jq", Version: "1.7.1-3build1", Section: "utils", Description: "lightweight and flexible command-line JSON processor"},
	{Name: "valgrind", Version: "1:3.22.0-0ubuntu3", Section: "devel", Description: "instrumentation framework for building dynamic analysis tools"},
}

// environmentWith is an App.Getenv over a map.
func environmentWith(variables map[string]string) func(string) string {
	return func(name string) string { return variables[name] }
}

// runPlainInit runs init --plain with a cache home of the test's own.
func runPlainInit(t *testing.T, cacheHome string, answers []string, args ...string) (exitCode int, stdout, stderr string, recipeDir string) {
	t.Helper()
	recipeDir = t.TempDir()
	var stdoutBuffer, stderrBuffer bytes.Buffer
	app := App{
		Stdout: &stdoutBuffer, Stderr: &stderrBuffer, RecipeDir: recipeDir, ReadFile: noHostFile,
		Prompt: &scriptedPrompt{answers: answers},
		Getenv: environmentWith(map[string]string{"XDG_CACHE_HOME": cacheHome}),
	}
	exitCode = app.Run(append([]string{"init", "--plain"}, args...))
	return exitCode, stdoutBuffer.String(), stderrBuffer.String(), recipeDir
}

func TestPlainInitWarnsFromACachedIndexAndNeverFetches(t *testing.T) {
	archive := indextest.Serve(t, "noble", noblePackages)
	cacheHome := t.TempDir()
	answers := answersWith(map[int]string{answerRelease: "24.04", answerOtherPackages: "jq ninja-buld docker-ce"})

	// Nothing cached: nothing is known, nothing is said, nothing is fetched.
	exitCode, stdout, stderr, _ := runPlainInit(t, cacheHome, answers, "--mirror", archive.URL)
	if exitCode != exitSuccess || strings.Contains(stdout, "Not in Ubuntu's") {
		t.Fatalf("without a cache: exit %d\nstdout: %s\nstderr: %s", exitCode, stdout, stderr)
	}
	if archive.Requests() != 0 {
		t.Fatalf("--plain made %d requests; it never fetches", archive.Requests())
	}

	// The full-screen form would have fetched; stand in for it.
	release, err := distro.Lookup("24.04", distro.SupportedArch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := index.Open(context.Background(), index.Options{Release: release, Mirror: archive.URL, CacheDir: index.CacheDir(environmentWith(map[string]string{"XDG_CACHE_HOME": cacheHome}))}); err != nil {
		t.Fatal(err)
	}
	fetchRequests := archive.Requests()

	exitCode, stdout, stderr, recipeDir := runPlainInit(t, cacheHome, answers, "--mirror", archive.URL)
	if exitCode != exitSuccess {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", exitCode, stdout, stderr)
	}
	for _, wantText := range []string{"Not in Ubuntu's noble archive:", "ninja-buld  nearest: ninja-build", "  docker-ce\n", "no other source", "Wrote frostroot.toml."} {
		if !strings.Contains(stdout, wantText) {
			t.Errorf("stdout lacks %q:\n%s", wantText, stdout)
		}
	}
	warningStart, heading := strings.Index(stdout, "Not in Ubuntu's"), strings.Index(stdout, "This is what frostroot.toml will say:")
	if warningStart < 0 || heading < 0 || warningStart > heading {
		t.Fatalf("the warning belongs between the summary and the recipe:\n%s", stdout)
	}
	if warning := stdout[warningStart:heading]; strings.Contains(warning, "jq") {
		t.Errorf("jq is in the archive and should not be warned about:\n%s", warning)
	}
	if archive.Requests() != fetchRequests {
		t.Errorf("--plain made %d requests with a cache present", archive.Requests()-fetchRequests)
	}
	// A warning, never a refusal.
	if got := loadWrittenRecipe(t, recipeDir).Packages.Include; !slices.Equal(got, []string{"jq", "ninja-buld", "docker-ce"}) {
		t.Errorf("packages = %v", got)
	}

	// With a source chosen the warning allows for it, and says why: --plain
	// fetches nothing, so Docker's repository was never read and may well
	// hold the name the archive lacks.
	withSource := answersWith(map[int]string{answerRelease: "24.04", answerOtherPackages: "docker-ce", answerSources: "docker"})
	_, stdout, _, _ = runPlainInit(t, cacheHome, withSource, "--mirror", archive.URL)
	if !strings.Contains(stdout, "docker could not be read, so it may provide them") {
		t.Errorf("stdout should allow for the docker source, and name it:\n%s", stdout)
	}
}

func TestIndexFlagsAreCheckedBeforeTheForm(t *testing.T) {
	notCertificates := filepath.Join(t.TempDir(), "bundle.pem")
	if err := os.WriteFile(notCertificates, []byte("not a certificate\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prompt := &scriptedPrompt{}
	var stderr bytes.Buffer
	app := App{Stdout: &bytes.Buffer{}, Stderr: &stderr, RecipeDir: t.TempDir(), Prompt: prompt, ReadFile: noHostFile}
	if exitCode := app.Run([]string{"init", "--plain", "--ca-bundle", notCertificates}); exitCode != exitUserError {
		t.Errorf("exit code = %d, want a user error", exitCode)
	}
	if !strings.Contains(stderr.String(), "--ca-bundle") || len(prompt.questionsAsked) != 0 {
		t.Errorf("stderr %q, %d questions asked; want the flag named and no question", stderr.String(), len(prompt.questionsAsked))
	}
	for _, command := range []string{"init", "edit", "capture"} {
		var usage bytes.Buffer
		app := App{Stdout: &bytes.Buffer{}, Stderr: &usage, RecipeDir: t.TempDir(), ReadFile: noHostFile}
		if exitCode := app.Run([]string{command, "--help"}); exitCode != exitSuccess {
			t.Errorf("%s --help: exit %d", command, exitCode)
		}
		for _, wantText := range []string{"-mirror", "-ca-bundle", "-refresh-index", "only suggest names", "--plain never"} {
			if !strings.Contains(usage.String(), wantText) {
				t.Errorf("%s --help lacks %q:\n%s", command, wantText, usage.String())
			}
		}
	}
}

func TestPackageIndexesOpenEachReleaseOnce(t *testing.T) {
	archive := indextest.Serve(t, "noble", noblePackages)
	app := (&App{Getenv: environmentWith(map[string]string{"XDG_CACHE_HOME": t.TempDir()})}).withDefaults()
	indexes, ok := app.packageIndexes(&indexFlags{mirror: archive.URL, refresh: true})
	if !ok {
		t.Fatal("packageIndexes refused plain flags")
	}
	var offlineCalls []bool
	realOpen := indexes.open
	indexes.open = func(ctx context.Context, options index.Options) (*index.Index, error) {
		offlineCalls = append(offlineCalls, options.Offline)
		if options.Offline && options.Refresh {
			t.Error("an offline open must not ask for a refresh")
		}
		return realOpen(ctx, options)
	}

	if known := indexes.Known(form.IndexRequest{Release: "24.04"}); known != nil {
		t.Errorf("Known before anything is fetched or cached = %v, want nil", known)
	}
	opened, err := indexes.Open(context.Background(), form.IndexRequest{Release: "24.04"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	matches, total := opened.Search("ninja", "", 10)
	if total != 1 || matches[0] != (form.Match{Name: "ninja-build", Version: "1.11.1-2", Component: "main", Section: "devel", Description: "small build system closest in spirit to Make"}) {
		t.Errorf("Search = %+v of %d", matches, total)
	}
	if sections := opened.SectionsMatching(""); len(sections) != 2 || sections[0] != (form.SectionCount{Name: "devel", Count: 2}) {
		t.Errorf("SectionsMatching = %+v", sections)
	}
	if !opened.Has("jq") || opened.Has("ninja-buld") || !slices.Equal(opened.Nearest("ninja-buld", 3), []string{"ninja-build"}) {
		t.Error("Has and Nearest should answer from the index")
	}
	if !strings.HasPrefix(opened.Describe(), "noble · 3 packages") {
		t.Errorf("Describe = %q", opened.Describe())
	}
	// The summary asks again, and so may the picker: no second opening.
	if indexes.Known(form.IndexRequest{Release: "24.04"}) == nil {
		t.Error("Known after Open should be the opened index")
	}
	if _, err := indexes.Open(context.Background(), form.IndexRequest{Release: "24.04"}, nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(offlineCalls, []bool{true, false}) {
		t.Errorf("opens (offline?) = %v, want the first Known and the one Open", offlineCalls)
	}

	if _, err := indexes.Open(context.Background(), form.IndexRequest{Release: "99.04"}, nil); !errors.Is(err, distro.ErrUnknownRelease) {
		t.Errorf("an unknown release: err = %v", err)
	}
	var none *packageIndexes
	if none.Known(form.IndexRequest{Release: "24.04"}) != nil {
		t.Error("no indexes, nothing known")
	}
}

var pypiProjects = []string{"requests", "requests-oauthlib", "numpy", "Flask-SQLAlchemy", "pytest"}

func TestPlainInitWarnsAboutPythonNamesFromACachedIndex(t *testing.T) {
	pypi := indextest.ServePyPI(t, pypiProjects, map[string]string{"requests": "Python HTTP for Humans."})
	cacheHome := t.TempDir()
	answers := answersWith(map[int]string{
		answerRelease:        "24.04",
		answerPythonPackages: "requests reqeusts Flask_SQLAlchemy kubernetes",
	})

	// Nothing cached: --plain says nothing and fetches nothing.
	exitCode, stdout, stderr, _ := runPlainInit(t, cacheHome, answers, "--python-index", pypi.URL)
	if exitCode != exitSuccess || strings.Contains(stdout, "Not on PyPI") {
		t.Fatalf("without a cache: exit %d\nstdout: %s\nstderr: %s", exitCode, stdout, stderr)
	}
	if pypi.Requests() != 0 {
		t.Fatalf("--plain made %d requests; it never fetches", pypi.Requests())
	}

	// The full-screen form would have fetched; stand in for it.
	if _, err := index.OpenPyPI(context.Background(), index.Options{
		Mirror:   pypi.URL,
		CacheDir: index.CacheDir(environmentWith(map[string]string{"XDG_CACHE_HOME": cacheHome})),
	}); err != nil {
		t.Fatal(err)
	}
	fetchRequests := pypi.Requests()

	exitCode, stdout, stderr, recipeDir := runPlainInit(t, cacheHome, answers, "--python-index", pypi.URL)
	if exitCode != exitSuccess {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", exitCode, stdout, stderr)
	}
	// The names are padded to the widest, so the suggestions line up.
	for _, wantText := range []string{"Not on PyPI:", "  reqeusts    nearest: requests\n", "  kubernetes\n", "pip cannot resolve them"} {
		if !strings.Contains(stdout, wantText) {
			t.Errorf("stdout lacks %q:\n%s", wantText, stdout)
		}
	}
	// Flask_SQLAlchemy is Flask-SQLAlchemy under PEP 503 and must not be
	// reported missing; requests is simply there.
	// Only the warning block: the recipe below it names every package, the
	// misspelled ones included.
	_, warning, hasWarning := strings.Cut(stdout, "Not on PyPI:")
	warning, _, _ = strings.Cut(warning, "index of your own.")
	if !hasWarning {
		t.Fatalf("no warning at all:\n%s", stdout)
	}
	for _, unwanted := range []string{"Flask_SQLAlchemy", "  requests\n"} {
		if strings.Contains(warning, unwanted) {
			t.Errorf("the warning should not name %q:\n%s", unwanted, warning)
		}
	}
	if pypi.Requests() != fetchRequests {
		t.Errorf("--plain made %d requests with a cache present", pypi.Requests()-fetchRequests)
	}
	// A warning, never a refusal.
	if got := loadWrittenRecipe(t, recipeDir).PythonPackages(); !slices.Equal(got, []string{"requests", "reqeusts", "Flask_SQLAlchemy", "kubernetes"}) {
		t.Errorf("python packages = %v", got)
	}
}

func TestPackageIndexesOpenPyPIOnceAndSummarizeOnDemand(t *testing.T) {
	pypi := indextest.ServePyPI(t, pypiProjects, map[string]string{"requests": "Python HTTP for Humans."})
	app := (&App{Getenv: environmentWith(map[string]string{"XDG_CACHE_HOME": t.TempDir()})}).withDefaults()
	indexes, ok := app.packageIndexes(&indexFlags{pythonIndex: pypi.URL})
	if !ok {
		t.Fatal("packageIndexes refused plain flags")
	}
	if known := indexes.KnownPython(); known != nil {
		t.Errorf("KnownPython before anything is fetched = %v, want nil", known)
	}
	opened, err := indexes.OpenPython(context.Background(), form.IndexRequest{Release: "24.04"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	matches, total := opened.Search("requests", "", 10)
	if total != 2 || matches[0].Name != "requests" {
		t.Errorf("Search = %+v of %d", matches, total)
	}
	if matches[0].Version != "" || matches[0].Section != "" {
		t.Errorf("PyPI publishes no version or section; got %+v", matches[0])
	}
	if sections := opened.SectionsMatching(""); sections != nil {
		t.Errorf("SectionsMatching = %v, want none", sections)
	}
	if !opened.Has("Flask_SQLAlchemy") || !slices.Equal(opened.Nearest("reqeusts", 3), []string{"requests"}) {
		t.Error("Has should normalize and Nearest should suggest")
	}
	if !strings.HasPrefix(opened.Describe(), "PyPI · 5 projects") {
		t.Errorf("Describe = %q", opened.Describe())
	}
	if indexes.KnownPython() == nil {
		t.Error("KnownPython after OpenPython should be the opened index")
	}

	// The summary is fetched only when asked for, and then remembered.
	summaries, hasSummaries := opened.(form.PackageSummaries)
	if !hasSummaries {
		t.Fatal("the PyPI index should offer summaries")
	}
	if _, known := summaries.Summary("requests"); known {
		t.Error("nothing is known before anything is asked")
	}
	summaries.FetchSummary(context.Background(), "requests")
	if summary, known := summaries.Summary("requests"); !known || summary != "Python HTTP for Humans." {
		t.Errorf("Summary = %q, %v", summary, known)
	}
	// A project PyPI has no summary for is remembered as having none.
	summaries.FetchSummary(context.Background(), "numpy")
	if summary, known := summaries.Summary("numpy"); !known || summary != "" {
		t.Errorf("Summary(numpy) = %q, %v; want a known-empty summary", summary, known)
	}
}

// dockerSource is what a recipe's [[sources]] entry for Docker resolves to.
func dockerSource(url string) recipe.Source {
	return recipe.Source{Name: "docker", URL: url, Components: []string{"main"}, Key: "keys/docker.asc"}
}

func TestPackageIndexesSearchTheSourcesTheRecipeAdds(t *testing.T) {
	archive := indextest.Serve(t, "noble", noblePackages)
	docker := indextest.Serve(t, "noble", []indextest.Package{
		{Name: "docker-ce", Version: "5:27.3.1-1~ubuntu.24.04~noble", Section: "admin", Description: "Docker: the open-source application container engine"},
	})
	app := (&App{Getenv: environmentWith(map[string]string{"XDG_CACHE_HOME": t.TempDir()})}).withDefaults()
	indexes, ok := app.packageIndexes(&indexFlags{mirror: archive.URL})
	if !ok {
		t.Fatal("packageIndexes refused plain flags")
	}
	request := form.IndexRequest{Release: "24.04", Sources: []recipe.Source{dockerSource(docker.URL)}}

	opened, err := indexes.Open(context.Background(), request, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The name that sent someone to a third-party source in the first place
	// is now a name the picker finds, with the repository it comes from.
	match, isThere := opened.Lookup("docker-ce")
	if !isThere || match.Origin != "docker" || match.Version != "5:27.3.1-1~ubuntu.24.04~noble" {
		t.Errorf("docker-ce = %+v, %v; want the source's package", match, isThere)
	}
	if !opened.Has("ninja-build") {
		t.Error("the release's own packages must still be there")
	}
	if matches, total := opened.Search("docker", "", 10); total != 1 || matches[0].Name != "docker-ce" {
		t.Errorf("Search = %+v of %d", matches, total)
	}
	if !strings.Contains(opened.Describe(), "noble + docker") {
		t.Errorf("Describe = %q, want both repositories named", opened.Describe())
	}

	// The same request again is the same index, and the archive is not
	// fetched a second time when only the sources change.
	requests := archive.Requests()
	if _, err := indexes.Open(context.Background(), request, nil); err != nil {
		t.Fatal(err)
	}
	withoutDocker := form.IndexRequest{Release: "24.04"}
	plain, err := indexes.Open(context.Background(), withoutDocker, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plain.Has("docker-ce") {
		t.Error("a request naming no source must not answer with one")
	}
	if archive.Requests() != requests {
		t.Errorf("the archive was fetched again: %d requests, want %d", archive.Requests(), requests)
	}
}

func TestPackageIndexesSearchTheSourcesWhenTheArchiveCannotBeRead(t *testing.T) {
	// The rule runs both ways: a repository that cannot be read is left out
	// and named, and that includes Ubuntu's own archive. A recipe that adds
	// Docker can still be told what Docker has.
	docker := indextest.Serve(t, "noble", []indextest.Package{
		{Name: "docker-ce", Version: "5:27.3.1-1~ubuntu.24.04~noble", Section: "admin", Description: "Docker: the open-source application container engine"},
	})
	app := (&App{Getenv: environmentWith(map[string]string{"XDG_CACHE_HOME": t.TempDir()})}).withDefaults()
	indexes, ok := app.packageIndexes(&indexFlags{mirror: "http://127.0.0.1:1/ubuntu"})
	if !ok {
		t.Fatal("packageIndexes refused plain flags")
	}
	request := form.IndexRequest{Release: "24.04", Sources: []recipe.Source{dockerSource(docker.URL)}}
	opened, err := indexes.Open(context.Background(), request, nil)
	if err != nil {
		t.Fatalf("an unreadable archive must not take the sources with it: %v", err)
	}
	if !opened.Has("docker-ce") {
		t.Error("the source that could be read must still be searchable")
	}
	if !strings.Contains(opened.Describe(), "archive not reachable") {
		t.Errorf("Describe = %q, want the archive named as missing", opened.Describe())
	}
	repositories, isMerged := opened.(form.PackageRepositories)
	if !isMerged {
		t.Fatalf("the index cannot say what it searched: %T", opened)
	}
	searched, missing := repositories.Sources()
	if !slices.Contains(missing, "archive") || !slices.Contains(searched, "docker") {
		t.Errorf("Sources = %v, %v; want the archive missing and the source searched", searched, missing)
	}

	// With nothing at all to read there is no index, as before.
	bare, err := indexes.Open(context.Background(), form.IndexRequest{Release: "24.04"}, nil)
	if bare != nil || err == nil {
		t.Errorf("with no repository at all: %v, %v; want no index and the archive's error", bare, err)
	}
}

func TestPackageIndexesSurviveASourceThatCannotBeRead(t *testing.T) {
	archive := indextest.Serve(t, "noble", noblePackages)
	app := (&App{Getenv: environmentWith(map[string]string{"XDG_CACHE_HOME": t.TempDir()})}).withDefaults()
	indexes, ok := app.packageIndexes(&indexFlags{mirror: archive.URL})
	if !ok {
		t.Fatal("packageIndexes refused plain flags")
	}
	// A vendor being down is no reason for the picker to stop working: the
	// archive is searched, and the line above the results says what is
	// missing so a name absent for that reason does not read as a name that
	// does not exist.
	unreachable := recipe.Source{Name: "docker", URL: "http://127.0.0.1:1/linux/ubuntu", Key: "keys/docker.asc"}
	opened, err := indexes.Open(context.Background(), form.IndexRequest{Release: "24.04", Sources: []recipe.Source{unreachable}}, nil)
	if err != nil {
		t.Fatalf("a source that cannot be read must not fail the index: %v", err)
	}
	if !opened.Has("ninja-build") {
		t.Error("the archive must still be searchable")
	}
	if !strings.Contains(opened.Describe(), "docker not reachable") {
		t.Errorf("Describe = %q, want the source named as missing", opened.Describe())
	}

	// An answer missing a repository is not remembered as the answer: the
	// same question asked again tries the repository again, so a vendor that
	// was down for a moment is searched once it is back.
	docker := indextest.Serve(t, "noble", []indextest.Package{
		{Name: "docker-ce", Version: "5:27.3.1-1~ubuntu.24.04~noble", Section: "admin", Description: "Docker: the open-source application container engine"},
	})
	attempts := 0
	realOpenSource := indexes.openSource
	indexes.openSource = func(ctx context.Context, options index.Options, source index.Source) (*index.Index, error) {
		attempts++
		source.URL = docker.URL
		return realOpenSource(ctx, options, source)
	}
	recovered, err := indexes.Open(context.Background(), form.IndexRequest{Release: "24.04", Sources: []recipe.Source{unreachable}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || !recovered.Has("docker-ce") {
		t.Errorf("attempts = %d, docker-ce found = %v; want the source tried again and searched", attempts, recovered.Has("docker-ce"))
	}
}
