package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// interruptingKeyClient stands in for a network that has stalled: its first
// request interrupts the command, as Ctrl-C would, and then waits until its
// own context ends. A request whose context the interrupt does not end
// waits until the test gives up on it.
type interruptingKeyClient struct {
	interrupt context.CancelFunc
	giveUp    <-chan struct{}
	mutex     sync.Mutex
	requests  []string
}

func (c *interruptingKeyClient) Get(ctx context.Context, url string) ([]byte, error) {
	c.mutex.Lock()
	c.requests = append(c.requests, url)
	c.mutex.Unlock()
	c.interrupt()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.giveUp:
		return nil, errors.New("the test gave up on this request")
	}
}

// TestCaptureInterruptedWhileFetchingKeys: the recipe is written before the
// keys are fetched, and capture --plain catches Ctrl-C until it ends, so an
// interrupt during the fetch ended nothing: each request ran out its own
// timeout. The interrupt now ends the fetch in flight and tries no other
// key; the recipe, its report and the certificates it names stay, and the
// exit says interrupted.
func TestCaptureInterruptedWhileFetchingKeys(t *testing.T) {
	recipeDir := t.TempDir()
	root := fakeUbuntuRoot(t, "24.04")
	withLocalAuthority(t, root, corpCertificatePEM(t))
	interrupted, interrupt := context.WithCancel(context.Background())
	defer interrupt()
	giveUp := make(chan struct{})
	client := &interruptingKeyClient{interrupt: interrupt, giveUp: giveUp}
	var stdout, stderr bytes.Buffer
	app := App{
		Stdout: &stdout, Stderr: &stderr, RecipeDir: recipeDir, ReadFile: noHostFile,
		Prompt:           &scriptedPrompt{answers: answersWith(map[int]string{answerSources: "docker", answerPPAs: "deadsnakes/ppa"})},
		KeyClient:        client,
		interruptContext: func() (context.Context, context.CancelFunc) { return interrupted, func() {} },
	}
	exited := make(chan int, 1)
	go func() { exited <- app.Run([]string{"capture", "--root", root, "--plain"}) }()
	var exitCode int
	select {
	case exitCode = <-exited:
	case <-time.After(15 * time.Second):
		close(giveUp)
		<-exited
		t.Fatal("capture was still fetching a key 15 seconds after the interrupt")
	}

	if exitCode != exitInterrupted {
		t.Errorf("exit code = %d, want %d; stderr %s", exitCode, exitInterrupted, stderr.String())
	}
	if len(client.requests) != 1 {
		t.Errorf("requests = %q, want only the one the interrupt ended", client.requests)
	}
	want := "frostroot capture: interrupted; frostroot.toml is written without the keys of docker, ppa-deadsnakes-ppa, so frostroot validate will refuse it until every key is in place; frostroot edit fetches them again\n"
	if !strings.Contains(stderr.String(), want) {
		t.Errorf("stderr lacks %q:\n%s", want, stderr.String())
	}
	if strings.Count(stderr.String(), "\n") != 1 {
		t.Errorf("the interrupt should be the one line on stderr:\n%s", stderr.String())
	}
	for _, kept := range []string{recipeFileName, captureReportFileName, filepath.Join("certs", "corp-root.pem")} {
		if _, err := os.Stat(filepath.Join(recipeDir, kept)); err != nil {
			t.Errorf("an interrupt after the recipe was written lost %s: %v", kept, err)
		}
	}
}

// TestCaptureKeyFailureIsNotAnInterrupt: with the signal handling in place
// and no signal, a key the server does not have fails as it always did.
func TestCaptureKeyFailureIsNotAnInterrupt(t *testing.T) {
	recipeDir := t.TempDir()
	root := fakeUbuntuRoot(t, "24.04")
	var stdout, stderr bytes.Buffer
	app := App{
		Stdout: &stdout, Stderr: &stderr, RecipeDir: recipeDir, ReadFile: noHostFile,
		Prompt:    &scriptedPrompt{answers: answersWith(map[int]string{answerSources: "docker"})},
		KeyClient: &fakeKeyClient{},
		interruptContext: func() (context.Context, context.CancelFunc) {
			return context.WithCancel(context.Background())
		},
	}
	if exitCode := app.Run([]string{"capture", "--root", root, "--plain"}); exitCode != exitUserError {
		t.Errorf("exit code = %d, want %d; stderr %s", exitCode, exitUserError, stderr.String())
	}
	if strings.Contains(stderr.String(), "interrupted") || !strings.Contains(stderr.String(), "frostroot.toml is written, but frostroot validate will refuse it") {
		t.Errorf("stderr:\n%s", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(recipeDir, captureReportFileName)); err != nil {
		t.Errorf("the report: %v", err)
	}
}
