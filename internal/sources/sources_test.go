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
		if len(entry.Fingerprint) != 40 || strings.ToUpper(entry.Fingerprint) != entry.Fingerprint || entry.KeyURL == "" || entry.Title == "" || entry.Category == "" {
			t.Errorf("incomplete entry %+v", entry)
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
	if fetched.Fingerprint != docker.Fingerprint || fetched.SourceURL != docker.KeyURL {
		t.Errorf("fetched = %+v", fetched)
	}
	key, err := pgp.ParsePublicKey(fetched.Armored)
	if err != nil || !key.Armored || key.Fingerprint != docker.Fingerprint {
		t.Errorf("Armored does not parse back to the key: %v, %+v", err, key)
	}
}

func TestFetchKeyForPPA(t *testing.T) {
	source := PPA("deadsnakes", "ppa")
	// The fake Launchpad says the PPA is signed by Docker's key, and the
	// fake keyserver serves that key: the check is about consistency.
	docker, _ := Lookup("docker")
	client := &fakeClient{answers: map[string][]byte{
		"https://api.launchpad.net/1.0/~deadsnakes/+archive/ubuntu/ppa": []byte(`{"name": "ppa", "signing_key_fingerprint": "` + strings.ToLower(docker.Fingerprint) + `"}`),
		keyserverURL(docker.Fingerprint):                                dockerKey(t),
	}}
	fetched, err := FetchKey(context.Background(), client, source)
	if err != nil {
		t.Fatal(err)
	}
	if fetched.Fingerprint != docker.Fingerprint || len(client.requests) != 2 {
		t.Errorf("fetched = %+v, requests %q", fetched, client.requests)
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
			wantText: "names no signing key",
		},
		{
			name:     "Launchpad answers garbage",
			source:   PPA("nobody", "broken"),
			answers:  map[string][]byte{"https://api.launchpad.net/1.0/~nobody/+archive/ubuntu/broken": []byte(`<html>`)},
			wantText: "Launchpad's answer",
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
