package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// padTo truncates or pads a single rendered row to exactly width cells. Layout
// code uses it so backgrounds and rules end on the terminal edge instead of
// depending on the text length.
func padTo(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = ansi.Truncate(s, width, "…")
	if gap := width - ansi.StringWidth(s); gap > 0 {
		s += strings.Repeat(" ", gap)
	}
	return s
}

// fitLine clips a row to width without padding.
func fitLine(s string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(s, width, "…")
}

// rule renders a label followed by a rule to exactly width cells. A leading
// space keeps the label off the frame edge.
func rule(label string, width int) string {
	if width <= 0 {
		return ""
	}
	lead := th.tileLabel.Render(label)
	if label == "" {
		lead = ""
	}
	used := ansi.StringWidth(lead)
	if used >= width {
		return ansi.Truncate(lead, width, "…")
	}
	return lead + " " + th.divider.Render(strings.Repeat("─", max(0, width-used-1)))
}

// columnHeader is the muted table heading row: labels aligned to the same
// columns as the rows below.
func columnHeader(cols []string, widths []int, gap int) string {
	var sb strings.Builder
	for i, c := range cols {
		if i > 0 {
			sb.WriteString(strings.Repeat(" ", gap))
		}
		sb.WriteString(th.tableHeader.Render(padTo(c, widths[i])))
	}
	return strings.TrimRight(sb.String(), " ")
}

// joinColumns places cells side by side at fixed widths. The final cell takes
// the remaining width so a row never overflows.

func joinLeft(left, right string, width, leftW int) string {
	if width <= 0 {
		return ""
	}
	leftW = min(max(8, leftW), max(0, width-2))
	left = padTo(left, leftW)
	rest := width - leftW - 2
	if rest < 4 {
		return fitLine(left, width)
	}
	return left + "  " + fitLine(right, rest)
}

// tint applies a background style across a complete row width.
func tint(style lipgloss.Style, text string, width int) string {
	return style.Width(max(0, width)).Render(padTo(text, width))
}
