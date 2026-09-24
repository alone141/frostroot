package recipe

import (
	"strings"
	"testing"
)

// TestLockRecordsAnUnverifiedResolve: a resolve made with --insecure is a
// fact about the lock's hashes, so the lock carries it; a verified resolve
// writes nothing, and every lock from before the flag reads as verified.
func TestLockRecordsAnUnverifiedResolve(t *testing.T) {
	if (Lockfile{}).PythonResolvedUnverified() {
		t.Error("a lock with no Python side claims an unverified resolve")
	}
	lock := sampleLock()
	lock.Python = &LockPython{Requested: []string{"numpy"}, Venv: "/opt/frostroot/venv", Interpreter: "3.12.3", PipVersion: "24.3.1"}
	_, content := saveAndRead(t, lock)
	if strings.Contains(content, "transport") || lock.PythonResolvedUnverified() {
		t.Errorf("a verified resolve writes no transport line:\n%s", content)
	}

	lock.Python.Transport = TransportUnverified
	path, content := saveAndRead(t, lock)
	if !strings.Contains(content, "transport = 'unverified'\n") {
		t.Errorf("the lock does not say the resolve was unverified:\n%s", content)
	}
	reloaded, err := LoadLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.PythonResolvedUnverified() {
		t.Errorf("reloaded lock = %+v, want the unverified resolve read back", reloaded.Python)
	}
}
