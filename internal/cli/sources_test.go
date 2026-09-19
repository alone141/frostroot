package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"frostroot/internal/pgp"
	"frostroot/internal/recipe"
	"frostroot/internal/sources"
)

// fakeKeyClient answers URLs from a map and records what was asked.
type fakeKeyClient struct {
	answers  map[string][]byte
	requests []string
}

func (c *fakeKeyClient) Get(_ context.Context, url string) ([]byte, error) {
	c.requests = append(c.requests, url)
	body, found := c.answers[url]
	if !found {
		return nil, fmt.Errorf("%s: HTTP 404 Not Found", url)
	}
	return body, nil
}

// dockerKeyFixture is Docker's real public key.
func dockerKeyFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "pgp", "testdata", "docker.asc"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// runInitWithKeyClient runs a plain init whose key fetches go to client.
func runInitWithKeyClient(recipeDir string, answers []string, client sources.Client) (exitCode int, stdout, stderr string) {
	var stdoutBuffer, stderrBuffer bytes.Buffer
	app := App{Stdout: &stdoutBuffer, Stderr: &stderrBuffer, RecipeDir: recipeDir, Prompt: &scriptedPrompt{answers: answers}, ReadFile: noHostFile, KeyClient: client}
	exitCode = app.Run([]string{"init"})
	return exitCode, stdoutBuffer.String(), stderrBuffer.String()
}

func TestInitWithSourcesFetchesTheirKeys(t *testing.T) {
	recipeDir := t.TempDir()
	docker, _ := sources.Lookup("docker")
	dockerKey := dockerKeyFixture(t)
	// The fake Launchpad says deadsnakes is signed by Docker's key, which the
	// fake keyserver then serves: consistency is what is checked.
	client := &fakeKeyClient{answers: map[string][]byte{
		docker.KeyURL: dockerKey,
		"https://api.launchpad.net/1.0/~deadsnakes/+archive/ubuntu/ppa":                                []byte(`{"signing_key_fingerprint": "` + docker.Fingerprints[0] + `"}`),
		"https://keyserver.ubuntu.com/pks/lookup?op=get&options=mr&search=0x" + docker.Fingerprints[0]: dockerKey,
	}}
	answers := answersWith(map[int]string{answerRelease: "22.04", answerSources: "docker", answerPPAs: "deadsnakes/ppa"})
	exitCode, stdout, stderr := runInitWithKeyClient(recipeDir, answers, client)
	if exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr)
	}
	imageRecipe := loadWrittenRecipe(t, recipeDir)
	if len(imageRecipe.Sources) != 2 || imageRecipe.Sources[0].Name != "docker" || imageRecipe.Sources[1].Name != "ppa-deadsnakes-ppa" {
		t.Fatalf("Sources = %+v", imageRecipe.Sources)
	}
	for _, source := range imageRecipe.Sources {
		data, err := os.ReadFile(recipe.KeyPath(recipeDir, source))
		if err != nil {
			t.Fatal(err)
		}
		key, err := pgp.ParsePublicKey(data)
		if err != nil || !key.Armored || key.Fingerprint != docker.Fingerprints[0] {
			t.Errorf("%s: %v, %+v", source.Key, err, key)
		}
	}
	for _, wantText := range []string{"Saved the signing key of docker from https://download.docker.com/linux/ubuntu/gpg into keys/docker.asc (fingerprint 9DC8 5822", "Saved the signing key of ppa-deadsnakes-ppa from https://keyserver.ubuntu.com"} {
		if !strings.Contains(stdout, wantText) {
			t.Errorf("stdout lacks %q:\n%s", wantText, stdout)
		}
	}
	recipeText, err := os.ReadFile(filepath.Join(recipeDir, "frostroot.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, wantText := range []string{"[[sources]]\nname = \"docker\"\nurl = \"https://download.docker.com/linux/ubuntu\"\ncomponents = [\"stable\"]\nkey = \"keys/docker.asc\"\n", "# Extra apt sources"} {
		if !strings.Contains(string(recipeText), wantText) {
			t.Errorf("recipe lacks %q:\n%s", wantText, recipeText)
		}
	}
	exitCode, validateOut, validateErr := runValidateIn(recipeDir)
	if exitCode != exitSuccess || !strings.Contains(validateOut, "2 extra sources") {
		t.Errorf("validate: exit %d, stdout %q, stderr %q", exitCode, validateOut, validateErr)
	}
}

func TestInitWithSourcesKeepsExistingKeys(t *testing.T) {
	recipeDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(recipeDir, "keys"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recipeDir, "keys", "docker.asc"), dockerKeyFixture(t), 0o644); err != nil {
		t.Fatal(err)
	}
	client := &fakeKeyClient{} // answers nothing: no fetch may happen
	exitCode, stdout, stderr := runInitWithKeyClient(recipeDir, answersWith(map[int]string{answerSources: "docker"}), client)
	if exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr)
	}
	if len(client.requests) != 0 || strings.Contains(stdout, "Saved the signing key") {
		t.Errorf("a key already there must not be fetched: requests %q", client.requests)
	}
}

func TestInitWithSourcesFingerprintMismatch(t *testing.T) {
	recipeDir := t.TempDir()
	kitware, _ := sources.Lookup("kitware")
	client := &fakeKeyClient{answers: map[string][]byte{kitware.KeyURL: dockerKeyFixture(t)}} // the wrong key
	exitCode, _, stderr := runInitWithKeyClient(recipeDir, answersWith(map[int]string{answerSources: "kitware"}), client)
	if exitCode != exitUserError {
		t.Fatalf("exit code = %d, want %d", exitCode, exitUserError)
	}
	for _, wantText := range []string{"holds a key that is not pinned", "9DC8 5822", "4DBE BE3E", "frostroot.toml is written", "frostroot edit fetches them again"} {
		if !strings.Contains(stderr, wantText) {
			t.Errorf("stderr lacks %q:\n%s", wantText, stderr)
		}
	}
	if _, err := os.Stat(filepath.Join(recipeDir, "keys", "kitware.asc")); err == nil {
		t.Error("a key with the wrong fingerprint must not be written")
	}
	imageRecipe := loadWrittenRecipe(t, recipeDir)
	if len(imageRecipe.Sources) != 1 || imageRecipe.Sources[0].Name != "kitware" {
		t.Errorf("the recipe must still be written: %+v", imageRecipe.Sources)
	}
	// validate refuses until the key is there, and says what to do.
	exitCode, _, validateErr := runValidateIn(recipeDir)
	if exitCode != exitUserError || !strings.Contains(validateErr, "source kitware: key file keys/kitware.asc") || !strings.Contains(validateErr, "frostroot edit fetches") {
		t.Errorf("validate: exit %d, stderr %q", exitCode, validateErr)
	}
}

func runEditWithKeyClient(recipeDir string, answers []string, client sources.Client) (exitCode int, stdout, stderr string) {
	var stdoutBuffer, stderrBuffer bytes.Buffer
	app := App{Stdout: &stdoutBuffer, Stderr: &stderrBuffer, RecipeDir: recipeDir, Prompt: &scriptedPrompt{answers: answers}, ReadFile: noHostFile, KeyClient: client}
	exitCode = app.Run([]string{"edit"})
	return exitCode, stdoutBuffer.String(), stderrBuffer.String()
}

func TestEditFetchesMissingSourceKeys(t *testing.T) {
	// init writes the recipe even when a key fetch fails, and tells the
	// user to run edit. edit must open that recipe and fetch the keys,
	// not refuse at load because the files are not there yet.
	recipeDir := newRecipeDir(t, "sources.toml")
	docker, _ := sources.Lookup("docker")
	dockerKey := dockerKeyFixture(t)
	client := &fakeKeyClient{answers: map[string][]byte{
		docker.KeyURL: dockerKey,
		"https://api.launchpad.net/1.0/~deadsnakes/+archive/ubuntu/ppa":                                []byte(`{"signing_key_fingerprint": "` + docker.Fingerprints[0] + `"}`),
		"https://keyserver.ubuntu.com/pks/lookup?op=get&options=mr&search=0x" + docker.Fingerprints[0]: dockerKey,
	}}
	exitCode, stdout, stderr := runEditWithKeyClient(recipeDir, answersWith(map[int]string{answerWrite: "y"}), client)
	if exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr)
	}
	for _, name := range []string{"docker", "ppa-deadsnakes-ppa"} {
		if _, err := os.Stat(filepath.Join(recipeDir, "keys", name+".asc")); err != nil {
			t.Errorf("edit did not save the key of %s: %v", name, err)
		}
	}
	if !strings.Contains(stdout, "Saved the signing key of docker") {
		t.Errorf("stdout lacks the saved-key line:\n%s", stdout)
	}
}

func TestValidateRecipeWithSourcesAndKeys(t *testing.T) {
	recipeDir := newRecipeDir(t, "sources.toml")
	exitCode, _, stderr := runValidateIn(recipeDir)
	if exitCode != exitUserError || !strings.Contains(stderr, "keys/docker.asc") {
		t.Fatalf("without keys: exit %d, stderr %q", exitCode, stderr)
	}
	for _, name := range []string{"docker", "ppa-deadsnakes-ppa"} {
		path := filepath.Join(recipeDir, "keys", name+".asc")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, dockerKeyFixture(t), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	exitCode, stdout, stderr := runValidateIn(recipeDir)
	if exitCode != exitSuccess || !strings.Contains(stdout, "2 packages requested, 2 extra sources") {
		t.Fatalf("with keys: exit %d, stdout %q, stderr %q", exitCode, stdout, stderr)
	}
}
