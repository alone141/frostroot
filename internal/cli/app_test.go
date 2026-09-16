package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testdata(name string) string { return filepath.Join("..", "..", "testdata", name) }

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func recipeDir(t *testing.T, fixture string) string {
	t.Helper()
	dir := t.TempDir()
	copyFile(t, testdata(fixture), filepath.Join(dir, "frostroot.toml"))
	return dir
}

func TestValidateOK(t *testing.T) {
	var out, errb bytes.Buffer
	app := App{Stdout: &out, Stderr: &errb, Dir: recipeDir(t, "valid.toml")}
	if code := app.Run([]string{"validate"}); code != 0 {
		t.Fatalf("code %d stderr %s", code, errb.String())
	}
	if out.String() != "frostroot.toml: ok (cpp-lab, Ubuntu 22.04 amd64, 3 packages requested)\n" {
		t.Fatalf("stdout should confirm what was checked: %q", out.String())
	}
	if errb.Len() != 0 {
		t.Fatalf("stderr should be empty: %q", errb.String())
	}
}

func TestValidateReportsProblems(t *testing.T) {
	var out, errb bytes.Buffer
	app := App{Stdout: &out, Stderr: &errb, Dir: recipeDir(t, "bad-user.toml")}
	if code := app.Run([]string{"validate"}); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(errb.String(), "user name") {
		t.Fatalf("stderr %s", errb.String())
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	dir := t.TempDir()
	body := "[image]\nname = \"../x\"\nrelease = \"18.04\"\narch = \"amd64\"\n\n[user]\nname = \"root\"\n\n[packages]\ninclude = [\"ok\", \"no way\"]\n"
	if err := os.WriteFile(filepath.Join(dir, "frostroot.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	var errb bytes.Buffer
	app := App{Stdout: io.Discard, Stderr: &errb, Dir: dir}
	if code := app.Run([]string{"validate"}); code != 1 {
		t.Fatalf("code %d", code)
	}
	for _, want := range []string{"image name", "unknown ubuntu release", "user name", `"no way"`} {
		if !strings.Contains(errb.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, errb.String())
		}
	}
}

func TestValidateBadRelease(t *testing.T) {
	var errb bytes.Buffer
	app := App{Stdout: io.Discard, Stderr: &errb, Dir: recipeDir(t, "bad-release.toml")}
	if code := app.Run([]string{"validate"}); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(errb.String(), "18.04") {
		t.Fatalf("stderr %s", errb.String())
	}
}

func TestValidateBadLocale(t *testing.T) {
	var errb bytes.Buffer
	app := App{Stdout: io.Discard, Stderr: &errb, Dir: recipeDir(t, "bad-locale.toml")}
	if code := app.Run([]string{"validate"}); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(errb.String(), "locale lang") {
		t.Fatalf("stderr %s", errb.String())
	}
}

func TestValidateUnknownField(t *testing.T) {
	var errb bytes.Buffer
	app := App{Stdout: io.Discard, Stderr: &errb, Dir: recipeDir(t, "unknown-field.toml")}
	if code := app.Run([]string{"validate"}); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(errb.String(), "[package]") {
		t.Fatalf("stderr should point at the typo: %s", errb.String())
	}
}

func TestValidateMissingFile(t *testing.T) {
	var errb bytes.Buffer
	app := App{Stdout: io.Discard, Stderr: &errb, Dir: t.TempDir()}
	if code := app.Run([]string{"validate"}); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(errb.String(), "frostroot.toml") || !strings.Contains(errb.String(), "frostroot init") {
		t.Fatalf("stderr should name the file and suggest init: %s", errb.String())
	}
}

func TestValidateRejectsArguments(t *testing.T) {
	var errb bytes.Buffer
	app := App{Stdout: io.Discard, Stderr: &errb, Dir: recipeDir(t, "valid.toml")}
	if code := app.Run([]string{"validate", "other.toml"}); code != 1 {
		t.Fatalf("code %d", code)
	}
}

func TestUnknownVerbAndNoArgs(t *testing.T) {
	for _, args := range [][]string{{}, {"frobnicate"}, {"--bogus"}} {
		var errb bytes.Buffer
		app := App{Stdout: io.Discard, Stderr: &errb, Dir: t.TempDir()}
		if code := app.Run(args); code != 1 {
			t.Fatalf("args %v: code %d", args, code)
		}
		if !strings.Contains(errb.String(), "usage") {
			t.Fatalf("args %v: stderr %s", args, errb.String())
		}
	}
}

func TestHelp(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}} {
		var out bytes.Buffer
		app := App{Stdout: &out, Stderr: io.Discard, Dir: t.TempDir()}
		if code := app.Run(args); code != 0 {
			t.Fatalf("args %v: code %d", args, code)
		}
		for _, verb := range []string{"init", "validate", "build"} {
			if !strings.Contains(out.String(), verb) {
				t.Fatalf("args %v: usage should list %s: %s", args, verb, out.String())
			}
		}
	}
}
