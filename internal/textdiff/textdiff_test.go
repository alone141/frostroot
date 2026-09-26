package textdiff

import (
	"strings"
	"testing"
)

func TestLines(t *testing.T) {
	tests := []struct {
		name        string
		oldText     string
		newText     string
		wantString  string
		wantAdded   int
		wantRemoved int
	}{
		{
			name:       "identical",
			oldText:    "a\nb\n",
			newText:    "a\nb\n",
			wantString: "  a\n  b\n",
		},
		{
			// Notepad's "UTF-8 with BOM": recipe.Load reads past the mark,
			// so the file is the same recipe and the diff must not show a
			// first line that differs by a character no one can see.
			name:       "byte-order mark",
			oldText:    "\xef\xbb\xbfa\nb\n",
			newText:    "a\nb\n",
			wantString: "  a\n  b\n",
		},
		{
			name:       "a line changed",
			oldText:    "[image]\nname = \"lab\"\nrelease = \"24.04\"\n",
			newText:    "[image]\nname = \"cpp-lab\"\nrelease = \"24.04\"\n",
			wantString: "  [image]\n- name = \"lab\"\n+ name = \"cpp-lab\"\n  release = \"24.04\"\n",
			wantAdded:  1, wantRemoved: 1,
		},
		{
			name:       "a line added at the end",
			oldText:    "a\nb\n",
			newText:    "a\nb\nc\n",
			wantString: "  a\n  b\n+ c\n",
			wantAdded:  1,
		},
		{
			name:        "a line removed from the start",
			oldText:     "# my note\na\n",
			newText:     "a\n",
			wantString:  "- # my note\n  a\n",
			wantRemoved: 1,
		},
		{
			name:       "a new file",
			oldText:    "",
			newText:    "a\nb\n",
			wantString: "+ a\n+ b\n",
			wantAdded:  2,
		},
		{
			name:        "a file emptied",
			oldText:     "a\n",
			newText:     "",
			wantString:  "- a\n",
			wantRemoved: 1,
		},
		{
			name:       "windows line endings and a missing final newline compare equal",
			oldText:    "a\r\nb\r\n",
			newText:    "a\nb",
			wantString: "  a\n  b\n",
		},
		{
			name:       "both empty",
			oldText:    "",
			newText:    "",
			wantString: "",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := Lines(test.oldText, test.newText)
			if got := result.String(); got != test.wantString {
				t.Errorf("String() =\n%s\nwant\n%s", got, test.wantString)
			}
			if result.Added != test.wantAdded || result.Removed != test.wantRemoved {
				t.Errorf("added %d removed %d, want added %d removed %d", result.Added, result.Removed, test.wantAdded, test.wantRemoved)
			}
			if result.Changed() != test.wantAdded+test.wantRemoved {
				t.Errorf("Changed() = %d", result.Changed())
			}
		})
	}
}

func TestLinesGivesUpGracefullyOnHugeTexts(t *testing.T) {
	huge := strings.Repeat("line\n", 3000)
	result := Lines(huge, huge+"extra\n")
	// Over the table bound the answer is coarse, but it is not wrong: every
	// line is accounted for, and the count is honest about that.
	if result.Removed != 3000 || result.Added != 3001 {
		t.Errorf("removed %d added %d, want everything removed and everything added", result.Removed, result.Added)
	}
}
