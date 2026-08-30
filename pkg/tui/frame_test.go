package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/seankoji-com/freshen/pkg/git"
	"github.com/seankoji-com/freshen/pkg/jobs"
)

// renderAt renders m at the given terminal size, driving the resize through
// the real Update path (tea.WindowSizeMsg) rather than a direct field
// assignment — a direct m.Width/m.Height/m.Viewport.Width assignment would
// bypass handleWindowSizeMsg entirely, including the negative-width floor
// clamps it applies (see update.go), so it would never exercise the code
// path those clamps protect.
func renderAt(m Model, w, h int) string {
	updated, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m = updated.(Model)
	return m.View()
}

// stripped normalizes a rendered frame for exact-match comparison: it strips
// real ANSI/SGR escape codes and OSC 8 hyperlink wrappers (see Hyperlink)
// via github.com/charmbracelet/x/ansi's terminal-state-machine-based Strip,
// leaving visible text — including PUA/Nerd-Font glyphs, which live well
// outside the escape-sequence byte ranges Strip recognizes — untouched.
func stripped(s string) string {
	return ansi.Strip(s)
}

// smokeFixtureModel builds a Model populated with representative Repos,
// Runners, and JobQueue data so every focus/tab combination — and each of
// the OVERALL JOB QUEUE panel's detail-viewport sub-states — has real
// content to render.
//
// JobQueue is shaped to produce, via buildJobQueueRows:
//
//	row 0: initiator header for "alpha#pr:5"      (2 jobs, shared RunID 100)
//	row 1: run header for RunID 100
//	row 2: job #1 (alpha / ci / build, running)
//	row 3: job #2 (alpha / ci / test, passed)
//	row 4: initiator header for "beta#branch:dev"  (1 job, RunID 200)
//	row 5: job #3 (beta / ci / lint, queued)
func smokeFixtureModel() Model {
	m := newTestModel("/tmp/smoke", "smoke-org")
	m.IsOrgSyncing = false
	m.Repos = []*git.RepoItem{
		{
			Name: "alpha", GHRepoName: "alpha", CurrentBranch: "feat/x", DefaultBranch: "main",
			Status: git.StatusUpdated, StatusMsg: "Updated", OpenPRsCount: 1, OpenIssuesCount: 1,
			HasLoadedCounts: true,
			HasLoadedIssues: true,
			IssuesList:      []git.IssueItem{{Number: 1, Title: "fix thing", URL: "https://github.com/acme/alpha/issues/1"}},
			HasLoadedPRs:    true,
			PRsList:         []git.PRItem{{Number: 2, Title: "add thing", HeadRefName: "feat/x", URL: "https://github.com/acme/alpha/pull/2"}},
			Logs:            []string{"[12:00:00] git pull", "Up to date"},
			BranchDetails: git.BranchWorktreeDetails{
				LocalBranches:  []string{"main", "feat/x"},
				RemoteBranches: []string{"origin/main"},
				Worktrees:      []string{"/tmp/smoke/alpha"},
				ChangedFiles:   []string{"README.md"},
			},
		},
		{Name: "beta", GHRepoName: "beta", CurrentBranch: "dev", DefaultBranch: "main", Status: git.StatusPending, HasLoadedCounts: true},
	}
	m.SelectedIndex = 0

	m.Runners = []*jobs.RunnerItem{
		{ID: "r1", Name: "mac-alpha", Status: jobs.RunnerRunning, Platform: "macOS/ARM64", Tags: []string{"self-hosted"}, CurrentJob: "alpha / ci / build"},
		{ID: "r2", Name: "mac-beta", Status: jobs.RunnerIdle, Platform: "macOS/ARM64", Tags: []string{"self-hosted"}},
	}
	m.SelectedRunnerIndex = 0

	m.JobQueue = []*jobs.JobItem{
		{ID: "#1", Name: "alpha / ci / build", Repo: "alpha", Status: jobs.JobRunning, RunID: 100, PRNumber: 5, PRTitle: "add thing", RunnerName: "mac-alpha", Duration: "1m 0s", Seconds: 60},
		{ID: "#2", Name: "alpha / ci / test", Repo: "alpha", Status: jobs.JobPassed, RunID: 100, PRNumber: 5, PRTitle: "add thing", RunnerName: "mac-alpha", Duration: "45s", Seconds: 45},
		{ID: "#3", Name: "beta / ci / lint", Repo: "beta", Status: jobs.JobQueued, RunID: 200, Branch: "dev", Duration: "-"},
	}
	m.SelectedJobIndex = 2 // a plain job row: exercises the single-job-detail sub-state

	m.Width = 120
	m.Height = 40
	return m
}

// assertRendersWithoutPanic renders m at its own Width/Height through the
// real resize path (renderAt -> tea.WindowSizeMsg -> handleWindowSizeMsg ->
// updateViewport -> View) and fails the (sub)test on panic or empty output.
// It deliberately does not call updateViewport() directly on a
// field-assigned model: that combination leaves Viewport.Width/Height at
// their NewModel defaults (60x15) while m.Width/m.Height say something else,
// i.e. a frame the running app can never actually produce.
func assertRendersWithoutPanic(t *testing.T, name string, m Model) string {
	t.Helper()
	var view string
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("%s: panic: %v", name, r)
			}
		}()
		view = renderAt(m, m.Width, m.Height)
	}()
	if strings.TrimSpace(view) == "" {
		t.Fatalf("%s: View() returned empty output", name)
	}
	return view
}

// TestSmokeAllViewsRenderWithoutPanic drives every ActiveFocus x ActiveTab
// combination (the FocusRepos|FocusRunners|FocusJobs x TabLogs|TabBranches|
// TabIssues|TabPRs matrix), crossed with toast visible/hidden, through
// View() — plus, separately, each of the OVERALL JOB QUEUE panel's 4
// detail-viewport sub-states (runner-tag-match list, run summary, initiator
// summary, single-job detail) at least once — asserting non-empty output and
// no panic throughout.
func TestSmokeAllViewsRenderWithoutPanic(t *testing.T) {
	focuses := []FocusType{FocusRepos, FocusRunners, FocusJobs}
	tabs := []TabType{TabLogs, TabBranches, TabIssues, TabPRs}

	for _, focus := range focuses {
		for _, tab := range tabs {
			for _, toastOn := range []bool{false, true} {
				name := fmt.Sprintf("focus=%d/tab=%d/toast=%v", focus, tab, toastOn)
				t.Run(name, func(t *testing.T) {
					m := smokeFixtureModel()
					m.ActiveFocus = focus
					m.ActiveTab = tab
					if toastOn {
						m.setToast(" test toast", 1)
					}
					assertRendersWithoutPanic(t, name, m)
				})
			}
		}
	}

	// --- Detail-viewport sub-states (FocusJobs), each exercised at least once ---

	t.Run("substate=runner-tag-match-list", func(t *testing.T) {
		m := smokeFixtureModel()
		m.ActiveFocus = FocusRunners
		assertRendersWithoutPanic(t, "runner-tag-match-list", m)

		// Assert against the detail viewport, not the whole frame: the left
		// RUNNERS panel prints "mac-alpha" too, so a full-frame Contains check
		// passes even when the tag-match list is empty. Resize through the
		// real Update path first (same as renderAt/assertRendersWithoutPanic
		// above) — a direct m.updateViewport() call on the outer m here would
		// read Viewport.Width/Height still at NewModel's 60x15 default, not
		// the 120x40 frame just rendered above.
		updated, _ := m.Update(tea.WindowSizeMsg{Width: m.Width, Height: m.Height})
		m = updated.(Model)
		vp := m.Viewport.View()
		if !strings.Contains(vp, "RUNNERS MATCHING") || !strings.Contains(vp, "mac-alpha") {
			t.Errorf("expected tag-match list in the detail viewport, got:\n%s", vp)
		}
	})

	t.Run("substate=initiator-summary", func(t *testing.T) {
		m := smokeFixtureModel()
		m.ActiveFocus = FocusJobs
		m.SelectedJobIndex = 0 // initiator header row for "alpha#pr:5"
		view := assertRendersWithoutPanic(t, "initiator-summary", m)
		if !strings.Contains(view, "FOCUSED INITIATOR") {
			t.Errorf("expected initiator-summary view, got:\n%s", view)
		}
	})

	t.Run("substate=run-summary", func(t *testing.T) {
		m := smokeFixtureModel()
		m.ActiveFocus = FocusJobs
		m.SelectedJobIndex = 1 // run header row for RunID 100
		view := assertRendersWithoutPanic(t, "run-summary", m)
		if !strings.Contains(view, "FOCUSED RUN") {
			t.Errorf("expected run-summary view, got:\n%s", view)
		}
	})

	t.Run("substate=single-job-detail", func(t *testing.T) {
		m := smokeFixtureModel()
		m.ActiveFocus = FocusJobs
		m.SelectedJobIndex = 5 // beta's lone job row (no run header above it)
		view := assertRendersWithoutPanic(t, "single-job-detail", m)
		if !strings.Contains(view, "#3") {
			t.Errorf("expected single-job-detail view for job #3, got:\n%s", view)
		}
	})

	// --- Branches the fixture above pins false/populated, exercised here ---

	t.Run("width-zero-early-return", func(t *testing.T) {
		m := smokeFixtureModel()
		m.Width = 0
		m.Height = 0
		view := assertRendersWithoutPanic(t, "width-zero-early-return", m)
		if !strings.Contains(view, "Initializing") {
			t.Errorf("expected the m.Width==0 early-return placeholder, got:\n%s", view)
		}
	})

	t.Run("org-syncing", func(t *testing.T) {
		m := smokeFixtureModel()
		m.IsOrgSyncing = true
		assertRendersWithoutPanic(t, "org-syncing", m)
	})

	t.Run("runners-fetch-failed", func(t *testing.T) {
		m := smokeFixtureModel()
		m.Runners = nil
		m.IsRunnersLoading = false
		m.RunnerFetchFailed = true
		m.RunnerPermissionDenied = false
		view := assertRendersWithoutPanic(t, "runners-fetch-failed", m)
		if !strings.Contains(view, "Failed to fetch registered runners") {
			t.Errorf("expected the runner-fetch-failed message, got:\n%s", view)
		}
	})

	t.Run("runners-permission-denied", func(t *testing.T) {
		m := smokeFixtureModel()
		m.Runners = nil
		m.IsRunnersLoading = false
		m.RunnerFetchFailed = true
		m.RunnerPermissionDenied = true
		// RunnerPermissionDenied suppresses the fetch-failed message (view.go's
		// `if m.RunnerFetchFailed && !m.RunnerPermissionDenied` guard) in favor
		// of the generic no-runners message — assert no-panic only, matching
		// the smoke table's own stated purpose.
		assertRendersWithoutPanic(t, "runners-permission-denied", m)
	})
}

// TestWidthSweepNeverPanicsAndFillsWhenSpaceAllows renders across a sweep of
// widths — including widths well below the layout's practical floor — for
// each ActiveFocus, driving resize via m.Update(tea.WindowSizeMsg{...}) (the
// same real resize path renderAt uses) rather than direct field assignment,
// since a direct assignment would bypass the update.go floor clamps entirely
// and never exercise the negative-width strings.Repeat panic they fix. For
// w >= 80, every rendered line must exactly fill the requested width; below
// that, view.go's own paneInnerWidth 30-column floor makes exact-fill
// mathematically unfalsifiable, so only "no panic" is asserted.
func TestWidthSweepNeverPanicsAndFillsWhenSpaceAllows(t *testing.T) {
	widths := []int{1, 5, 10, 20, 30, 50, 80, 120, 200}
	focuses := []FocusType{FocusRepos, FocusRunners, FocusJobs}
	// ActiveTab is swept too: TabIssues/TabPRs render through their own
	// m.Viewport.Width-6 title wrappers and TabBranches through its own list
	// paths, none of which the FocusRepos default tab (TabLogs) reaches.
	tabs := []TabType{TabLogs, TabBranches, TabIssues, TabPRs}

	for _, w := range widths {
		for _, focus := range focuses {
			for _, tab := range tabs {
				t.Run(fmt.Sprintf("w=%d/focus=%d/tab=%d", w, focus, tab), func(t *testing.T) {
					m := smokeFixtureModel()
					m.ActiveFocus = focus
					m.ActiveTab = tab

					var view string
					func() {
						defer func() {
							if r := recover(); r != nil {
								t.Fatalf("panic at width %d, focus %d, tab %d: %v", w, focus, tab, r)
							}
						}()
						view = renderAt(m, w, 40)
					}()

					if view == "" {
						t.Fatalf("width %d focus %d tab %d: empty view", w, focus, tab)
					}

					if w >= 80 {
						maxWidth := 0
						for _, line := range strings.Split(view, "\n") {
							if lw := lipgloss.Width(line); lw > maxWidth {
								maxWidth = lw
							}
						}
						if maxWidth != w {
							t.Errorf("width %d focus %d tab %d: max rendered line width %d, want exact fill %d", w, focus, tab, maxWidth, w)
						}
					}
				})
			}
		}
	}
}
