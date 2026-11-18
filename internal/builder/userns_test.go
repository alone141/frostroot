package builder

import (
	"errors"
	"testing"
)

func TestIsWSL1(t *testing.T) {
	testCases := []struct {
		name    string
		release string
		want    bool
	}{
		{name: "WSL 1 on Windows 10 1809", release: "4.4.0-17763-Microsoft", want: true},
		{name: "WSL 1 with a trailing newline", release: "4.4.0-19041-Microsoft\n", want: true},
		{name: "WSL 2", release: "5.15.153.1-microsoft-standard-WSL2"},
		{name: "WSL 2 with a trailing newline", release: "6.6.87.2-microsoft-standard-WSL2\n"},
		{name: "Ubuntu's kernel", release: "6.8.0-45-generic"},
		{name: "the lowercase name alone", release: "4.4.0-17763-microsoft"},
		{name: "empty", release: ""},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			readFile := func(path string) ([]byte, error) {
				if path != kernelReleasePath {
					t.Errorf("read %s, want only %s", path, kernelReleasePath)
				}
				return []byte(testCase.release), nil
			}
			if got := isWSL1(readFile); got != testCase.want {
				t.Errorf("isWSL1 with release %q = %t, want %t", testCase.release, got, testCase.want)
			}
		})
	}
}

// TestIsWSL1WithAnUnreadableRelease: a release that cannot be read proves
// nothing, and the build goes on to the checks that follow.
func TestIsWSL1WithAnUnreadableRelease(t *testing.T) {
	readFile := func(string) ([]byte, error) { return nil, errors.New("permission denied") }
	if isWSL1(readFile) {
		t.Error("isWSL1 with an unreadable release = true, want false")
	}
	if err := checkNotWSL1(readFile); err != nil {
		t.Errorf("checkNotWSL1 with an unreadable release = %v, want nil", err)
	}
}
