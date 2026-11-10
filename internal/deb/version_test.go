package deb

import "testing"

// TestCompareVersions is dpkg's own ordering, from its documentation and
// test suite, and the shapes the Ubuntu archive and vendors publish.
func TestCompareVersions(t *testing.T) {
	tests := []struct {
		older, newer string
	}{
		{"1.0", "1.0.1"},
		{"1.0~rc1", "1.0"},
		{"1.0~rc1", "1.0~rc2"},
		{"1.0~", "1.0"},
		{"1.0~~", "1.0~"},
		{"1.0", "1.0a"},
		{"1.0", "1.0+b1"},
		{"1.0a", "1.0+"},
		{"2.0", "1:0.1"},
		{"1.0-1", "1.0-2"},
		{"1.0-9", "1.0-10"},
		{"2.38-4ubuntu2.9", "2.38-4ubuntu2.12"},
		{"5:29.8.1-1~ubuntu.24.04~noble", "5:29.10.0-1~ubuntu.24.04~noble"},
		{"5:27.0.0-1", "5:29.8.1-1"},
		{"1:2.34.1-1ubuntu1.11", "1:2.34.1-1ubuntu1.17"},
		{"0.9", "0.10"},
		{"1.2.3-1", "1.2.3-1build1"},
	}
	for _, test := range tests {
		if got := CompareVersions(test.older, test.newer); got >= 0 {
			t.Errorf("CompareVersions(%q, %q) = %d, want it older", test.older, test.newer, got)
		}
		if got := CompareVersions(test.newer, test.older); got <= 0 {
			t.Errorf("CompareVersions(%q, %q) = %d, want it newer", test.newer, test.older, got)
		}
	}
	for _, same := range [][2]string{{"1.0", "1.0"}, {"1.0", "0:1.0"}, {"1.0", "1.0-0"}, {"1.00", "1.0"}, {"", ""}} {
		if got := CompareVersions(same[0], same[1]); got != 0 {
			t.Errorf("CompareVersions(%q, %q) = %d, want the same version", same[0], same[1], got)
		}
	}
}
