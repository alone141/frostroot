package capture

import (
	"fmt"
	"strings"
)

// maxReportExamples bounds how many items a report section lists.
const maxReportExamples = 20

// Report renders the snapshot as Markdown: what was captured and from where,
// then every area a recipe cannot carry, with counts and examples.
func (s Snapshot) Report() string {
	var report strings.Builder
	fmt.Fprintf(&report, "# frostroot capture report\n\nRead from `%s`. The recipe next to this file, `frostroot.toml`, holds everything in the first section; nothing in the second section is in it.\n\n", s.Root)
	report.WriteString("## Captured\n\n")
	fmt.Fprintf(&report, "- Ubuntu %s, %s\n", s.Release, s.Arch)
	for _, line := range s.Evidence {
		fmt.Fprintf(&report, "- %s\n", line)
	}
	report.WriteString("\n## Not captured\n\n")
	report.WriteString("Capture reads what apt knows and a few configuration files, and copies nothing else. Each area below was checked.\n")
	for _, finding := range s.Findings {
		report.WriteString("\n")
		writeFinding(&report, finding)
	}
	return report.String()
}

// writeFinding renders one report section.
func writeFinding(report *strings.Builder, finding Finding) {
	switch {
	case finding.Unavailable != "":
		fmt.Fprintf(report, "### %s\n\nCould not check: %s\n", finding.Area, finding.Unavailable)
	case finding.Count == 0:
		fmt.Fprintf(report, "### %s\n\nNothing found.\n", finding.Area)
	default:
		fmt.Fprintf(report, "### %s (%d)\n\n%s\n\n", finding.Area, finding.Count, finding.Advice)
		for index, example := range finding.Examples {
			if index == maxReportExamples {
				fmt.Fprintf(report, "- and %d more\n", len(finding.Examples)-maxReportExamples)
				break
			}
			fmt.Fprintf(report, "- %s\n", example)
		}
	}
}

// Summary returns one line per report area for the terminal.
func (s Snapshot) Summary() []string {
	lines := make([]string, 0, len(s.Findings))
	for _, finding := range s.Findings {
		switch {
		case finding.Unavailable != "":
			lines = append(lines, fmt.Sprintf("%-36s could not check", finding.Area))
		case finding.Count == 0:
			lines = append(lines, fmt.Sprintf("%-36s nothing found", finding.Area))
		default:
			lines = append(lines, fmt.Sprintf("%-36s %d", finding.Area, finding.Count))
		}
	}
	return lines
}
