package sources

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"frostroot/internal/pgp"
	"frostroot/internal/recipe"
)

// dockerKey is Docker's real public key, armored.
func dockerKey(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "pgp", "testdata", "docker.asc"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// githubCLIKey is GitHub's real keyring, binary, and it holds two primary
// keys: the catalog pins both.
func githubCLIKey(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "pgp", "testdata", "github-cli.gpg"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// fakeClient answers URLs from a map; unknown URLs fail.
type fakeClient struct {
	answers  map[string][]byte
	requests []string
}

func (c *fakeClient) Get(_ context.Context, url string) ([]byte, error) {
	c.requests = append(c.requests, url)
	body, found := c.answers[url]
	if !found {
		return nil, fmt.Errorf("%s: HTTP 404 Not Found", url)
	}
	return body, nil
}

func TestCatalogEntriesAreValidSources(t *testing.T) {
	seen := map[string]bool{}
	for _, entry := range Catalog() {
		if seen[entry.Name] {
			t.Errorf("%s appears twice", entry.Name)
		}
		seen[entry.Name] = true
		if len(entry.Fingerprints) == 0 || entry.KeyURL == "" || entry.Title == "" || entry.Category == "" {
			t.Errorf("incomplete entry %+v", entry)
		}
		pinned := map[string]bool{}
		for _, fingerprint := range entry.Fingerprints {
			if len(fingerprint) != 40 || strings.ToUpper(fingerprint) != fingerprint {
				t.Errorf("%s: %q is not 40 uppercase hex digits", entry.Name, fingerprint)
			}
			if pinned[fingerprint] {
				t.Errorf("%s pins %s twice", entry.Name, fingerprint)
			}
			pinned[fingerprint] = true
		}
		for _, suite := range []string{"focal", "jammy", "noble"} {
			source := entry.Source(suite)
			if problems := recipe.CheckSource(source); len(problems) != 0 {
				t.Errorf("%s on %s: %v", entry.Name, suite, problems)
			}
			if strings.Contains(source.URL+source.Suite, "{") {
				t.Errorf("%s on %s: placeholder left in %+v", entry.Name, suite, source)
			}
			if source.Key != "keys/"+entry.Name+".asc" {
				t.Errorf("%s: key path %q", entry.Name, source.Key)
			}
		}
	}
	llvm, _ := Lookup("llvm")
	if got := llvm.Source("jammy"); got.URL != "https://apt.llvm.org/jammy" || got.Suite != "llvm-toolchain-jammy" {
		t.Errorf("llvm on jammy = %+v", got)
	}
	docker, _ := Lookup("docker")
	if got := docker.Source("noble"); got.Suite != "" || !reflect.DeepEqual(got.Components, []string{"stable"}) {
		t.Errorf("docker on noble = %+v, want the release suite and stable", got)
	}
	if _, found := Lookup("nope"); found {
		t.Error("Lookup found a source that is not there")
	}
}

func TestPPA(t *testing.T) {
	for _, text := range []string{"deadsnakes/ppa", "ppa:deadsnakes/ppa", "  deadsnakes/ppa "} {
		owner, name, err := ParsePPA(text)
		if err != nil || owner != "deadsnakes" || name != "ppa" {
			t.Errorf("ParsePPA(%q) = %q, %q, %v", text, owner, name, err)
		}
	}
	for _, bad := range []string{"deadsnakes", "Dead/ppa", "a/b/c", "", "owner/na me"} {
		if _, _, err := ParsePPA(bad); err == nil {
			t.Errorf("ParsePPA(%q) succeeded", bad)
		}
	}
	source := PPA("deadsnakes", "ppa")
	want := recipe.Source{Name: "ppa-deadsnakes-ppa", URL: "https://ppa.launchpadcontent.net/deadsnakes/ppa/ubuntu", Key: "keys/ppa-deadsnakes-ppa.asc"}
	if !reflect.DeepEqual(source, want) {
		t.Errorf("PPA = %+v, want %+v", source, want)
	}
	if problems := recipe.CheckSource(PPA("some.owner", "my+ppa")); len(problems) != 0 {
		t.Errorf("a PPA with dots and pluses must still be a valid source: %v", problems)
	}
	owner, name, isPPA := PPAOf("https://ppa.launchpadcontent.net/git-core/ppa/ubuntu/")
	if !isPPA || owner != "git-core" || name != "ppa" {
		t.Errorf("PPAOf = %q, %q, %v", owner, name, isPPA)
	}
	for _, notPPA := range []string{"https://download.docker.com/linux/ubuntu", "https://ppa.launchpadcontent.net/x", "https://ppa.launchpadcontent.net/a/b/debian"} {
		if _, _, isPPA := PPAOf(notPPA); isPPA {
			t.Errorf("PPAOf(%q) = true", notPPA)
		}
	}
	if got := PPAFilesURL("deadsnakes", "ppa", "python3.12_3.12.4-1_amd64.deb"); got != "https://launchpad.net/~deadsnakes/+archive/ubuntu/ppa/+files/python3.12_3.12.4-1_amd64.deb" {
		t.Errorf("PPAFilesURL = %q", got)
	}
	if Describe(source) != "PPA deadsnakes/ppa" || Describe(recipe.Source{Name: "docker"}) != "Docker" || Describe(recipe.Source{Name: "x", URL: "https://x.example/repo"}) != "https://x.example/repo" {
		t.Error("Describe")
	}
}

func TestFetchKeyForCatalogEntry(t *testing.T) {
	docker, _ := Lookup("docker")
	client := &fakeClient{answers: map[string][]byte{docker.KeyURL: dockerKey(t)}}
	fetched, err := FetchKey(context.Background(), client, docker.Source("noble"))
	if err != nil {
		t.Fatal(err)
	}
	if fetched.Fingerprint != docker.Fingerprints[0] || fetched.SourceURL != docker.KeyURL {
		t.Errorf("fetched = %+v", fetched)
	}
	key, err := pgp.ParsePublicKey(fetched.Armored)
	if err != nil || !key.Armored || key.Fingerprint != docker.Fingerprints[0] {
		t.Errorf("Armored does not parse back to the key: %v, %+v", err, key)
	}
}

func TestFetchKeyForPPA(t *testing.T) {
	source := PPA("deadsnakes", "ppa")
	// The fake Launchpad says the PPA is signed by Docker's key, and the
	// fake keyserver serves that key: the check is about consistency.
	docker, _ := Lookup("docker")
	client := &fakeClient{answers: map[string][]byte{
		"https://api.launchpad.net/1.0/~deadsnakes/+archive/ubuntu/ppa": []byte(`{"name": "ppa", "signing_key_fingerprint": "` + strings.ToLower(docker.Fingerprints[0]) + `"}`),
		keyserverURL(docker.Fingerprints[0]):                            dockerKey(t),
	}}
	fetched, err := FetchKey(context.Background(), client, source)
	if err != nil {
		t.Fatal(err)
	}
	if fetched.Fingerprint != docker.Fingerprints[0] || len(client.requests) != 2 {
		t.Errorf("fetched = %+v, requests %q", fetched, client.requests)
	}
}

func TestFetchKeyRefusesAKeyAppendedToThePinnedOne(t *testing.T) {
	// What a compromised key host serves: the pinned key, then one of its
	// own. Reading only the first fingerprint, the pin matches — and the
	// whole file would become the source's signed-by keyring, where apt
	// accepts a Release signed by either key.
	docker, _ := Lookup("docker")
	pinnedKey, err := pgp.ParsePublicKey(dockerKey(t))
	if err != nil {
		t.Fatal(err)
	}
	appended := append(append([]byte(nil), pinnedKey.Binary...), githubCLIKey(t)...)

	// The premise: the appended file still names the pinned key first.
	parsed, err := pgp.ParsePublicKey(appended)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Fingerprint != docker.Fingerprints[0] {
		t.Fatalf("the appended file should still lead with the pinned key, got %s", parsed.Fingerprint)
	}

	client := &fakeClient{answers: map[string][]byte{docker.KeyURL: appended}}
	_, err = FetchKey(context.Background(), client, docker.Source("noble"))
	if !errors.Is(err, ErrFingerprintMismatch) {
		t.Fatalf("err = %v, want ErrFingerprintMismatch", err)
	}
	// The error names the key that is not pinned, not the one that is.
	if !strings.Contains(err.Error(), "2C61 0620") {
		t.Errorf("the error should name the appended key: %v", err)
	}
}

func TestFetchKeyAcceptsAFileWhoseKeysAreAllPinned(t *testing.T) {
	// GitHub really does serve two primary keys in one file, so pinning the
	// set has to accept that and not only single-key files.
	githubCLI, _ := Lookup("github-cli")
	if len(githubCLI.Fingerprints) != 2 {
		t.Fatalf("github-cli pins %d keys, want the two its keyring holds", len(githubCLI.Fingerprints))
	}
	client := &fakeClient{answers: map[string][]byte{githubCLI.KeyURL: githubCLIKey(t)}}
	fetched, err := FetchKey(context.Background(), client, githubCLI.Source("noble"))
	if err != nil {
		t.Fatal(err)
	}
	if fetched.Fingerprint != githubCLI.Fingerprints[0] {
		t.Errorf("fetched = %+v, want the first pinned key", fetched)
	}
	// What was accepted is what gets written: the armored file that becomes
	// the signed-by keyring still holds both pinned keys and nothing else.
	written, err := pgp.ParsePublicKey(fetched.Armored)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(written.Fingerprints, githubCLI.Fingerprints) {
		t.Errorf("the written keyring holds %v, want exactly the pinned set %v", written.Fingerprints, githubCLI.Fingerprints)
	}
}

func TestFirstUnpinned(t *testing.T) {
	const docker = "9DC858229FC7DD38854AE2D88D81803C0EBFCD88"
	const github = "2C6106201985B60E6C7AC87323F3D4EA75716059"
	testCases := []struct {
		name    string
		fetched []string
		pinned  []string
		want    string // "" means every fetched key is pinned
	}{
		{"the pinned key alone", []string{docker}, []string{docker}, ""},
		{"a keyserver answering in lowercase", []string{strings.ToLower(docker)}, []string{docker}, ""},
		{"both pinned keys, in order", []string{docker, github}, []string{docker, github}, ""},
		{"both pinned keys, in the other order", []string{github, docker}, []string{docker, github}, ""},
		// Fewer keys than pinned is not a mismatch: the file trusts less
		// than it is allowed to, which is what a rotation looks like.
		{"only one of the pinned keys", []string{github}, []string{docker, github}, ""},
		{"a key appended to the pinned one", []string{docker, github}, []string{docker}, github},
		{"only a key that is not pinned", []string{github}, []string{docker}, github},
		{"nothing pinned at all", []string{docker}, nil, docker},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			unpinned, found := firstUnpinned(testCase.fetched, testCase.pinned)
			if found != (testCase.want != "") || unpinned != testCase.want {
				t.Errorf("firstUnpinned = %q, %v; want %q", unpinned, found, testCase.want)
			}
		})
	}
}

func TestFetchKeyFailures(t *testing.T) {
	docker, _ := Lookup("docker")
	otherKey := dockerKey(t)
	testCases := []struct {
		name      string
		source    recipe.Source
		answers   map[string][]byte
		wantError error
		wantText  string
	}{
		{
			name:      "custom source",
			source:    recipe.Source{Name: "corp", URL: "https://apt.corp.example/ubuntu", Key: "keys/corp.asc"},
			wantError: ErrNoKeySource,
			wantText:  "keys/corp.asc",
		},
		{
			name:   "fingerprint mismatch",
			source: func() recipe.Source { entry, _ := Lookup("kitware"); return entry.Source("noble") }(),
			answers: map[string][]byte{
				"https://apt.kitware.com/keys/kitware-archive-latest.asc": otherKey, // Docker's key where Kitware's should be
			},
			wantError: ErrFingerprintMismatch,
			wantText:  "9DC8 5822",
		},
		{
			name:     "key server down",
			source:   docker.Source("noble"),
			wantText: "404",
		},
		{
			name:     "not a key",
			source:   docker.Source("noble"),
			answers:  map[string][]byte{docker.KeyURL: []byte("<html>oops</html>")},
			wantText: "not an OpenPGP public key",
		},
		{
			name:     "Launchpad without a key",
			source:   PPA("nobody", "empty"),
			answers:  map[string][]byte{"https://api.launchpad.net/1.0/~nobody/+archive/ubuntu/empty": []byte(`{"signing_key_fingerprint": null}`)},
			wantText: "no signing key is published",
		},
		{
			name:     "Launchpad answers garbage",
			source:   PPA("nobody", "broken"),
			answers:  map[string][]byte{"https://api.launchpad.net/1.0/~nobody/+archive/ubuntu/broken": []byte(`<html>`)},
			wantText: "answer of Launchpad",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			client := &fakeClient{answers: testCase.answers}
			_, err := FetchKey(context.Background(), client, testCase.source)
			if err == nil {
				t.Fatal("FetchKey succeeded, want an error")
			}
			if testCase.wantError != nil && !errors.Is(err, testCase.wantError) {
				t.Errorf("err = %v, want %v", err, testCase.wantError)
			}
			if !strings.Contains(err.Error(), testCase.wantText) {
				t.Errorf("err = %v, want it to mention %q", err, testCase.wantText)
			}
		})
	}
}

// TestPPASourceNameFitsAndSeparates: a PPA name must fit the recipe's limit,
// and two long ones must not land on the same name. Short names must not
// change at all: they are already in recipes and in keys/<name>.asc.
func TestPPASourceNameFitsAndSeparates(t *testing.T) {
	for ppa, want := range map[string]string{
		"deadsnakes/ppa":             "ppa-deadsnakes-ppa",
		"git-core/ppa":               "ppa-git-core-ppa",
		"longsleep/golang-backports": "ppa-longsleep-golang-backports",
	} {
		owner, name, err := ParsePPA(ppa)
		if err != nil {
			t.Fatal(err)
		}
		if got := PPA(owner, name).Name; got != want {
			t.Errorf("PPA(%q).Name = %q, want the unchanged %q", ppa, got, want)
		}
	}
	long := []string{"canonical-server/server-backports", "canonical-server/server-backports-two", "someone-else/a-very-long-archive-name"}
	seen := map[string]string{}
	for _, ppa := range long {
		owner, name, err := ParsePPA(ppa)
		if err != nil {
			t.Fatal(err)
		}
		got := PPA(owner, name).Name
		if len(got) > recipe.MaxSourceNameLength {
			t.Errorf("PPA(%q).Name = %q, %d characters (limit %d)", ppa, got, len(got), recipe.MaxSourceNameLength)
		}
		if problems := recipe.CheckSource(PPA(owner, name)); len(problems) > 0 {
			t.Errorf("PPA(%q) is not a valid source: %v", ppa, problems)
		}
		if other, taken := seen[got]; taken {
			t.Errorf("%s and %s both became %q", other, ppa, got)
		}
		seen[got] = ppa
	}
}

func TestShortName(t *testing.T) {
	for name, want := range map[string]string{
		// The usual PPA: owner/ppa, eighteen characters down to ten.
		"ppa-deadsnakes-ppa": "deadsnakes",
		// An archive of its own name keeps it.
		"ppa-ondrej-php": "ondrej-php",
		// A catalog or hand-written source is called what it is called.
		"docker":  "docker",
		"kitware": "kitware",
		"corp":    "corp",
		// Nothing is left of a name that is only its prefix and suffix.
		"ppa-ppa": "ppa",
		"ppa-":    "",
		"":        "",
	} {
		if got := ShortName(name); got != want {
			t.Errorf("ShortName(%q) = %q, want %q", name, got, want)
		}
	}
	// Every PPA the form can produce still says which PPA it is.
	for _, owner := range []string{"deadsnakes", "git-core", "a"} {
		short := ShortName(PPA(owner, "ppa").Name)
		if short != owner {
			t.Errorf("ShortName of %s/ppa = %q, want %q", owner, short, owner)
		}
	}
}
