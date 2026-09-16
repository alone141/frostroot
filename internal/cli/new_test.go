package cli

import (
	"testing"

	"frostroot/internal/builder"
)

func TestNewUsesRealMmdebstrap(t *testing.T) {
	app := New()
	if app.Builder == nil {
		t.Fatal("no builder")
	}
	if _, ok := app.Builder.Bootstrap.(*builder.Mmdebstrap); !ok {
		t.Fatalf("New must build with mmdebstrap, got %T", app.Builder.Bootstrap)
	}
	if app.Prompt == nil || app.WSLPath == nil || app.Getenv == nil || app.Dir == "" || app.GOOS == "" {
		t.Fatalf("defaults not filled: %+v", app)
	}
}
