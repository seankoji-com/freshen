package tui

import (
	"fmt"
	"math"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/seankoji-com/freshen/pkg/git"
	"github.com/seankoji-com/freshen/pkg/jobs"
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
	th = newThemeMode(false, false)

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

// The light palette keeps the same WCAG AA floor on a white terminal and the
// light selection surface.
func TestLightThemeBodyContrast(t *testing.T) {
	saved := th
	t.Cleanup(func() { th = saved })
	th = newThemeMode(false, true)
	surface := colourHex(th.surface)
	if surface == "" {
		t.Fatal("light theme surface is not a concrete colour")
	}
	colours := map[string]lipgloss.TerminalColor{
		"text": th.text, "muted": th.muted, "primary": th.primary,
		"secondary": th.secondary, "success": th.green, "warning": th.yellow,
		"failure": th.red, "running": th.blue,
	}
	for name, c := range colours {
		hex := colourHex(c)
		for label, bg := range map[string]string{"white": "#FFFFFF", "surface": surface} {
			if ratio := contrastRatio(hex, bg); ratio < 4.5 {
				t.Errorf("light %s on %s is %.2f:1, want >= 4.5:1", name, label, ratio)
			}
		}
	}
}

// Light progress fills stay visible as non-text graphics (3:1).
func TestLightProgressFillContrast(t *testing.T) {
	saved := th
	t.Cleanup(func() { th = saved })
	th = newThemeMode(false, true)
	for _, k := range []statusKind{kindSuccess, kindFailure, kindRunning, kindQueued, kindWarning, kindSkipped, kindUnknown} {
		fill := th.progressFill(k)
		if ratio := contrastRatio(fill, "#FFFFFF"); ratio < 3.0 {
			t.Errorf("light progress fill %s (kind %d) is %.2f:1 on white", fill, k, ratio)
		}
	}
}

// GitHub prefixes job-log lines with an ISO timestamp. A severity colour must
// survive that prefix instead of being reset by the timestamp span.
func TestTimestampedErrorLogStaysRed(t *testing.T) {
	previous := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(0)
	t.Cleanup(func() { lipgloss.SetColorProfile(previous) })
	saved := th
	t.Cleanup(func() { th = saved })
	th = newThemeMode(false, false)

	out := highlightLogLine("2026-11-05T01:02:03Z ##[error]HTTP 403")
	if !strings.Contains(out, "240;113;120") {
		t.Fatalf("timestamped error line lost its red span: %q", out)
	}
	if !strings.HasSuffix(out, "\x1b[0m") {
		t.Fatalf("error line is not a single styled span: %q", out)
	}
	out = highlightLogLine("2026-11-05T01:02:03Z FAIL github.com/x/y 0.12s")
	if !strings.Contains(out, "240;113;120") {
		t.Fatalf("timestamped error line lost its red span: %q", out)
	}
	if !strings.HasSuffix(out, "\x1b[0m") {
		t.Fatalf("error line is not a single styled span: %q", out)
	}
}

// Log severity must not treat package names such as errors/conflict as
// failures, and a success glyph must win over an error word in the branch.
func TestLogSeverityClassification(t *testing.T) {
	previous := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(0)
	t.Cleanup(func() { lipgloss.SetColorProfile(previous) })
	saved := th
	t.Cleanup(func() { th = saved })
	th = newThemeMode(false, false)

	cases := []struct {
		line, colour string
	}{
		{"ok  github.com/acme/beta/errors 0.02s", "201;139"},
		{"2026-11-05T01:02:03Z ok  github.com/acme/beta/conflict 0.03s", "201;139"},
		{"2026-11-05T01:02:03Z ok  github.com/acme/beta/errors 0.03s", "201;139"},
		{"--- PASS: TestCases/FAIL (0.00s)", "201;139"},
		{"ok  github.com/acme/FAIL 0.10s", "201;139"},
		{"--- FAIL: TestCases/PASS (0.00s)", "240;113;120"},
		{"--- PASS: TestParse_error", "201;139"},
		{"󰄬 Switched branch to 'fix/error-handling'.", "201;139"},
		{"󰀪 git merge --abort failed", "245;158;11"},
		{"Pull failed: conflict", "240;113;120"},
		{"Tests: 1 failed, 20 passed", "240;113;120"},
		{"FAIL package/TestPASS", "240;113;120"},
		{"Tests failed.", "240;113;120"},
		{"Automatic merge failed; fix conflicts", "240;113;120"},
		{"error: HTTP 403", "240;113;120"},
		{"󰅙 something happened", "240;113;120"},
	}
	for _, tc := range cases {
		out := highlightLogLine(tc.line)
		if !strings.Contains(out, tc.colour) {
			t.Errorf("%q rendered %q, want colour %s", tc.line, out, tc.colour)
		}
	}
	for _, neutral := range []string{
		"Starting sync for conflict-resolver",
		"Branch: fix/error-handling | Default: main",
		"Committing and pushing branch 'fix/panic-recovery'",
		"Branch: conflict | Default: main",
		"Starting sync for panic",
	} {
		if out := highlightLogLine(neutral); strings.Contains(out, "240;113;120") {
			t.Errorf("neutral line %q rendered as failure: %q", neutral, out)
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

func BenchmarkEntriesLarge(b *testing.B) {
	m := smokeFixtureModel()
	m.ActiveFocus = FocusRepos
	m.Repos = nil
	for i := 0; i < 200; i++ {
		m.Repos = append(m.Repos, &git.RepoItem{Name: fmt.Sprintf("repo-%03d", i), GHRepoName: fmt.Sprintf("repo-%03d", i), Path: fmt.Sprintf("/tmp/repo-%03d", i), CurrentBranch: "main", DefaultBranch: "main", Status: git.StatusUpToDate, HasLoadedCounts: true})
	}
	m.JobQueue = nil
	for i := 0; i < 400; i++ {
		r := &jobs.RunItem{ID: int64(i + 1), Number: i + 1, Repo: fmt.Sprintf("repo-%03d", i%200), Workflow: "CI", Status: jobs.JobPassed, JobsKnown: true}
		m.JobQueue = append(m.JobQueue, &jobs.JobItem{ID: fmt.Sprintf("run:%d", i+1), RunID: int64(i + 1), Repo: r.Repo, Run: r, IsRunHeader: true, Status: jobs.JobPassed})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.entries()
	}
}
