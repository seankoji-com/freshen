package tui

import (
	"math"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// relativeLuminance is the WCAG 2.x definition for an sRGB hex colour.
func relativeLuminance(hex string) float64 {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return 0
	}
	parse := func(i int) float64 {
		var v int
		for _, c := range hex[i : i+2] {
			v *= 16
			switch {
			case c >= '0' && c <= '9':
				v += int(c - '0')
			case c >= 'a' && c <= 'f':
				v += int(c-'a') + 10
			case c >= 'A' && c <= 'F':
				v += int(c-'A') + 10
			}
		}
		return float64(v) / 255
	}
	lin := func(c float64) float64 {
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(parse(0)) + 0.7152*lin(parse(2)) + 0.0722*lin(parse(4))
}

func contrastRatio(a, b string) float64 {
	la, lb := relativeLuminance(a), relativeLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func colourHex(c lipgloss.TerminalColor) string {
	if v, ok := c.(lipgloss.Color); ok {
		return string(v)
	}
	return ""
}

// Every body-text colour the theme puts on the default surface or the
// selection surface keeps WCAG AA contrast (4.5:1). This is a measured check,
// not an estimate; the default background is the one the capture harness uses.
func TestThemeBodyContrast(t *testing.T) {
	saved := th
	t.Cleanup(func() { th = saved })
	th = newTheme(false)

	const background = "#101613"
	surface := colourHex(th.surface)
	if surface == "" {
		t.Fatal("theme surface is not a concrete colour")
	}
	colours := map[string]lipgloss.TerminalColor{
		"text": th.text, "muted": th.muted, "primary": th.primary,
		"secondary": th.secondary, "success": th.green, "warning": th.yellow,
		"failure": th.red, "running": th.blue,
	}
	for name, c := range colours {
		hex := colourHex(c)
		if hex == "" {
			t.Fatalf("%s: not a concrete colour", name)
		}
		for label, bg := range map[string]string{"background": background, "surface": surface} {
			if ratio := contrastRatio(hex, bg); ratio < 4.5 {
				t.Errorf("%s on %s is %.2f:1, want >= 4.5:1", name, label, ratio)
			}
		}
	}
}

// The wide Overview must use the height it is given, not leave half the body
// empty. This is the defect that opened the design run.
func TestOverviewWideLayoutUsesSpace(t *testing.T) {
	m := demoFixtureModel()
	m.ActiveFocus = FocusOverview
	lines := strings.Split(stripped(m.summaryContent(198, 45)), "\n")
	used := 0
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			used++
		}
	}
	if used < 30 {
		t.Fatalf("200x50 Overview uses %d of 45 body rows:\n%s", used, strings.Join(lines, "\n"))
	}
}

// The preview column is read-only chrome and must never make a frame wider or
// taller than the terminal.
func TestPreviewFramesStayInsideTheTerminal(t *testing.T) {
	for _, focus := range []FocusType{FocusRepos, FocusJobs, FocusRunners, FocusConcerns} {
		for _, size := range [][2]int{{140, 30}, {160, 40}, {200, 50}} {
			m := smokeFixtureModel()
			m.ActiveFocus = focus
			m.handleWindowSizeMsg(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			frame := m.View()
			lines := strings.Split(frame, "\n")
			if len(lines) != size[1] {
				t.Fatalf("%v %v: %d rows", focus, size, len(lines))
			}
			for i, line := range lines {
				if w := ansi.StringWidth(line); w > size[0] {
					t.Fatalf("%v %v: row %d is %d cells: %q", focus, size, i, w, stripped(line))
				}
			}
		}
	}
}

func BenchmarkOverviewView200(b *testing.B) {
	m := smokeFixtureModel()
	m.ActiveFocus = FocusOverview
	m.handleWindowSizeMsg(tea.WindowSizeMsg{Width: 200, Height: 50})
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = m.View()
	}
}
