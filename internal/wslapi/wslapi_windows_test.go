//go:build windows

package wslapi

import (
	"errors"
	"testing"
)

// TestIsRegisteredAnswersForANameNoOneHas calls the real wslapi.dll, and
// only to ask: it registers nothing. A name with a character no
// distribution name has cannot be registered.
func TestIsRegisteredAnswersForANameNoOneHas(t *testing.T) {
	registered, err := IsRegistered("frostroot test: no such distribution")
	if errors.Is(err, ErrNoWSL) {
		t.Skipf("this Windows has no WSL: %v", err)
	}
	if err != nil {
		t.Fatalf("IsRegistered = %v", err)
	}
	if registered {
		t.Error("IsRegistered = true for a name no distribution can have")
	}
}
