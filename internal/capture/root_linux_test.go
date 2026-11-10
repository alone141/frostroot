//go:build linux

package capture

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestCaptureReadsNothingOutsideTheRoot: capture kept a few of its reads
// inside --root DIR (signed-by keys, Conffiles, symlinked certificates) and
// read the rest wherever a symlink pointed, so a tree someone else prepared
// could have capture describe this machine as that one, and quote its
// files in the report.
func TestCaptureReadsNothingOutsideTheRoot(t *testing.T) {
	hostDir := t.TempDir()
	hostFile := func(name, content string) string {
		t.Helper()
		path := filepath.Join(hostDir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	files := map[string]string{
		osReleasePath:         "ID=ubuntu\nVERSION_ID=\"24.04\"\nVERSION_CODENAME=noble\n",
		"var/lib/dpkg/status": dpkgStanza("dpkg", "required", installed) + dpkgStanza("git", "optional", installed),
		hostnamePath:          "-> " + hostFile("hostname", "this-very-host\n"),
		timezonePath:          "-> " + hostFile("timezone", "Antarctica/Troll\n"),
		passwdPath:            "-> " + hostFile("passwd", "root:x:0:0::/root:/bin/sh\nhostuser:x:1000:1000::/home/hostuser:/bin/bash\n"),
	}
	snapshot, err := Read(buildRoot(t, files))
	if err != nil {
		t.Fatal(err)
	}
	report := snapshot.Report()
	for _, hostValue := range []string{"this-very-host", "Antarctica/Troll", "hostuser"} {
		if strings.Contains(report, hostValue) || snapshot.ImageName == hostValue || snapshot.Timezone == hostValue || snapshot.UserName == hostValue {
			t.Errorf("capture took %q from a file outside the root", hostValue)
		}
	}

	// A dpkg status that leads outside the root is refused, not read.
	files["var/lib/dpkg/status"] = "-> " + hostFile("status", dpkgStanza("dpkg", "required", installed))
	if _, err := Read(buildRoot(t, files)); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Errorf("Read with the status outside the root = %v, want a refusal saying so", err)
	}
}

// TestCaptureDoesNotBlockOnAFIFO: a FIFO where capture reads a file
// blocked the open before anything was printed, and a link to a device
// grew the read without end.
func TestCaptureDoesNotBlockOnAFIFO(t *testing.T) {
	root := buildRoot(t, map[string]string{
		osReleasePath:         "ID=ubuntu\nVERSION_ID=\"24.04\"\nVERSION_CODENAME=noble\n",
		"var/lib/dpkg/status": dpkgStanza("dpkg", "required", installed),
		"usr/local/share/ca-certificates/corp.crt": "placeholder",
	})
	for _, relative := range []string{timezonePath, hostnamePath, "usr/local/share/ca-certificates/corp.crt"} {
		path := filepath.Join(root, filepath.FromSlash(relative))
		_ = os.Remove(path)
		if err := syscall.Mkfifo(path, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	result := make(chan error, 1)
	go func() {
		_, err := Read(root)
		result <- err
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Errorf("Read = %v, want a snapshot without what the FIFOs stand for", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("capture is blocked on a FIFO")
	}
}
