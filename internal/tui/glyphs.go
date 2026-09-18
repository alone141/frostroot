package tui

import (
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
)

// glyphSet is what the screens draw with: box drawing and symbols on a
// terminal that shows them, ASCII on one whose locale says it may not. A
// WSL session started from some Windows tools runs with LANG=C, and there
// the check marks and the pane border are mojibake.
type glyphSet struct {
	done, failed, pending string
	ellipsis              string
	scrollHint            string
	border                lipgloss.Border
	barFull, barEmpty     rune
	spinner               spinner.Spinner
	ascii                 bool
}

var (
	unicodeGlyphs = glyphSet{
		done: "✓", failed: "✗", pending: "·", ellipsis: "…", scrollHint: "↑/↓ PgUp/PgDn",
		border: lipgloss.RoundedBorder(), barFull: '█', barEmpty: '░', spinner: spinner.Dot,
	}
	asciiGlyphs = glyphSet{
		done: "+", failed: "x", pending: "-", ellipsis: "...", scrollHint: "Up/Down PgUp/PgDn",
		border:  lipgloss.Border{Top: "-", Bottom: "-", Left: "|", Right: "|", TopLeft: "+", TopRight: "+", BottomLeft: "+", BottomRight: "+"},
		barFull: '#', barEmpty: '.', spinner: spinner.Line, ascii: true,
	}
)

// lookupEnvironment is os.LookupEnv; tests replace it to choose a locale.
var lookupEnvironment = os.LookupEnv

// localeVariables are consulted in the order the C library gives them
// precedence: the first one set decides.
var localeVariables = []string{"LC_ALL", "LC_CTYPE", "LANG"}

// glyphsForTerminal picks the set from the locale. A value without UTF-8
// in it means ASCII; no variable set at all means a modern terminal, which
// is UTF-8.
func glyphsForTerminal() glyphSet {
	for _, name := range localeVariables {
		value, isSet := lookupEnvironment(name)
		if !isSet || value == "" {
			continue
		}
		if isUTF8Locale(value) {
			return unicodeGlyphs
		}
		return asciiGlyphs
	}
	return unicodeGlyphs
}

// isUTF8Locale reports whether a locale value such as en_US.UTF-8 or
// C.utf8 names a UTF-8 character set.
func isUTF8Locale(value string) bool {
	lowered := strings.ToLower(value)
	return strings.Contains(lowered, "utf-8") || strings.Contains(lowered, "utf8")
}

// paneStyleWith is the frame around a pane of text, drawn with border.
func paneStyleWith(border lipgloss.Border) lipgloss.Style {
	return lipgloss.NewStyle().Border(border).BorderForeground(lipgloss.Color("8")).Padding(0, 1)
}

// titledPane renders content in a pane of contentWidth cells with title in
// its top edge. The title takes the place of as much of the edge as it is
// wide, so the edge stays exactly as long as the pane.
func titledPane(glyphs glyphSet, title string, contentWidth int, content string) string {
	// lipgloss's Width includes the padding, so the pane is two cells wider
	// than the content it holds.
	pane := paneStyleWith(glyphs.border).Width(contentWidth + 2).Render(content)
	label := " " + title + " "
	edge := strings.Repeat(glyphs.border.Top, lipgloss.Width(label))
	corner := glyphs.border.TopLeft + glyphs.border.Top
	return strings.Replace(pane, corner+edge, corner+dimStyle.Render(label), 1)
}

// fitLines cuts every line of text to width cells, ending a cut line with
// the ellipsis, so that nothing wraps inside a pane. The log file and the
// recipe hold the whole lines; a pane is for reading, not for copying.
func fitLines(text string, width int, ellipsis string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for index, line := range lines {
		lines[index] = truncateWith(line, width, ellipsis)
	}
	return strings.Join(lines, "\n")
}

// shortenLeft cuts text to at most width cells by dropping its start, the
// way a path is shortened so that its file name stays.
func shortenLeft(text string, width int, ellipsis string) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(text) <= width {
		return text
	}
	ellipsisWidth := lipgloss.Width(ellipsis)
	if width <= ellipsisWidth {
		return string([]rune(ellipsis)[:width])
	}
	runes := []rune(text)
	dropped := len(runes)
	for cells := 0; dropped > 0; dropped-- {
		cells += lipgloss.Width(string(runes[dropped-1]))
		if cells > width-ellipsisWidth {
			break
		}
	}
	return ellipsis + string(runes[dropped:])
}

// truncateWith cuts text to at most width cells, ending it with ellipsis
// when it was longer.
func truncateWith(text string, width int, ellipsis string) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(text) <= width {
		return text
	}
	ellipsisWidth := lipgloss.Width(ellipsis)
	if width <= ellipsisWidth {
		return string([]rune(ellipsis)[:width])
	}
	runes := []rune(text)
	kept := 0
	for cells := 0; kept < len(runes); kept++ {
		cells += lipgloss.Width(string(runes[kept]))
		if cells > width-ellipsisWidth {
			break
		}
	}
	return string(runes[:kept]) + ellipsis
}
