// Package textdiff compares two texts line by line, for showing someone
// what a file is about to become. It is the classic longest-common-
// subsequence diff over lines, with no hunks: a recipe is short, and the
// point is to read the whole thing with the changes marked.
package textdiff

import "strings"

// Kind says what happened to a line.
type Kind int

// The kinds of line.
const (
	Same    Kind = iota // in both texts
	Removed             // in the old text only
	Added               // in the new text only
)

// Line is one line of the comparison.
type Line struct {
	Kind Kind
	Text string
}

// Result is the comparison of two texts.
type Result struct {
	Lines   []Line
	Added   int
	Removed int
}

// Changed is how many lines differ, added and removed together.
func (r Result) Changed() int { return r.Added + r.Removed }

// String renders the comparison the way a unified diff marks lines: two
// spaces, "- " or "+ " before each, one per line, newline-terminated.
func (r Result) String() string {
	var rendered strings.Builder
	for _, line := range r.Lines {
		switch line.Kind {
		case Removed:
			rendered.WriteString("- ")
		case Added:
			rendered.WriteString("+ ")
		case Same:
			rendered.WriteString("  ")
		}
		rendered.WriteString(line.Text)
		rendered.WriteByte('\n')
	}
	return rendered.String()
}

// maxTableCells bounds the LCS table. Above it the texts are not what this
// package is for, and the comparison is every old line removed and every
// new line added, which is still true.
const maxTableCells = 4_000_000

// Lines compares old with new, line by line. Trailing newlines do not make
// an extra empty line; Windows line endings compare equal to Unix ones.
func Lines(oldText, newText string) Result {
	oldLines, newLines := splitLines(oldText), splitLines(newText)
	if len(oldLines)*len(newLines) > maxTableCells {
		return replaceAll(oldLines, newLines)
	}
	// lengths[i][j] is the length of the longest common subsequence of
	// oldLines[i:] and newLines[j:].
	lengths := make([][]int, len(oldLines)+1)
	for i := range lengths {
		lengths[i] = make([]int, len(newLines)+1)
	}
	for i := len(oldLines) - 1; i >= 0; i-- {
		for j := len(newLines) - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				lengths[i][j] = lengths[i+1][j+1] + 1
			} else {
				lengths[i][j] = max(lengths[i+1][j], lengths[i][j+1])
			}
		}
	}
	var result Result
	i, j := 0, 0
	for i < len(oldLines) && j < len(newLines) {
		switch {
		case oldLines[i] == newLines[j]:
			result.Lines = append(result.Lines, Line{Kind: Same, Text: oldLines[i]})
			i++
			j++
		case lengths[i+1][j] >= lengths[i][j+1]:
			result.Lines = append(result.Lines, Line{Kind: Removed, Text: oldLines[i]})
			result.Removed++
			i++
		default:
			result.Lines = append(result.Lines, Line{Kind: Added, Text: newLines[j]})
			result.Added++
			j++
		}
	}
	for ; i < len(oldLines); i++ {
		result.Lines = append(result.Lines, Line{Kind: Removed, Text: oldLines[i]})
		result.Removed++
	}
	for ; j < len(newLines); j++ {
		result.Lines = append(result.Lines, Line{Kind: Added, Text: newLines[j]})
		result.Added++
	}
	return result
}

// replaceAll is the comparison of two texts with nothing in common.
func replaceAll(oldLines, newLines []string) Result {
	result := Result{Added: len(newLines), Removed: len(oldLines)}
	for _, line := range oldLines {
		result.Lines = append(result.Lines, Line{Kind: Removed, Text: line})
	}
	for _, line := range newLines {
		result.Lines = append(result.Lines, Line{Kind: Added, Text: line})
	}
	return result
}

// splitLines splits text into lines, without the newline characters,
// without a phantom empty line after a final newline, and without the
// byte-order mark an editor such as Notepad puts before the first line of a
// file saved as "UTF-8 with BOM": recipe.Load reads past it, so a file that
// differs by nothing else parses to the same recipe, and the diff must say
// so rather than show two lines a reader cannot tell apart.
func splitLines(text string) []string {
	text = strings.TrimPrefix(text, "\xef\xbb\xbf")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}
