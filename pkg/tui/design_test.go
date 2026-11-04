package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/seankoji-com/freshen/pkg/git"
	"github.com/seankoji-com/freshen/pkg/jobs"
)

// A loading snapshot must not render reassuring zeros for data it has not
// read yet; this was a baseline finding.
func TestOverviewLoadingDoesNotInventZeros(t *testing.T) {
	m := smokeFixtureModel()
	m.Repos = nil
	m.JobQueue = nil
	m.Runners = nil
	m.IsOrgSyncing = true
	m.IsJobQueueLoading = true
	m.IsRunnersLoading = true
	view := stripped(m.summaryContent(118, 25))
	for _, misleading := range []string{"0 need attention", "0 recent failures", "0 offline"} {
		if strings.Contains(view, misleading) {
			t.Fatalf("loading Overview presented %q as a result:\n%s", misleading, view)
		}
	}
	if !strings.Contains(view, "Loading") {
		t.Fatalf("loading Overview does not say it is loading:\n%s", view)
	}
}

// A degraded snapshot keeps cached rows but the repository tile must carry the
// warning, not a success check.
func TestOverviewDegradedSnapshotWarns(t *testing.T) {
	m := smokeFixtureModel()
	m.OrgRefreshFailed = true
	view := stripped(m.summaryContent(118, 25))
	if !strings.Contains(view, "attention") && !strings.Contains(view, "unavailable") {
		t.Fatalf("degraded Overview looks healthy:\n%s", view)
	}
}

// The attention desk separates blockers from review work once there is room.
func TestAttentionGroupsFailuresAndReview(t *testing.T) {
	m := smokeFixtureModel()
	m.Repos[1].Status = git.StatusError
	m.Repos[1].StatusMsg = "Pull failed"
	view := stripped(m.summaryAttentionRows(m.buildOrgSummary(), 90, 12))
	for _, want := range []string{"FAILURES (1)", "REVIEW", "beta"} {
		if !strings.Contains(view, want) {
			t.Fatalf("grouped attention missing %q:\n%s", want, view)
		}
	}
}

// A click inside the read-only preview must not retarget the list selection.
func TestPreviewClickDoesNotChangeSelection(t *testing.T) {
	m := smokeFixtureModel()
	m.ActiveFocus = FocusRepos
	m.handleWindowSizeMsg(tea.WindowSizeMsg{Width: 160, Height: 40})
	m.moveSelection(0)
	before := m.SelectedIndex
	m.screenMouse(tea.MouseMsg{X: 130, Y: 6, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if m.SelectedIndex != before {
		t.Fatalf("preview click changed selection from %d to %d", before, m.SelectedIndex)
	}
}

// Recent runs sort by completion time, not by the transport's ID order.
func TestRecentRunsSortByCompletion(t *testing.T) {
	m := smokeFixtureModel()
	m.JobQueue = nil
	now := time.Now()
	newer := &jobs.RunItem{ID: 10, Number: 1, Repo: "newer", Workflow: "CI", Status: jobs.JobPassed, JobsKnown: true,
		StartedAt: now.Add(-3 * time.Minute), CompletedAt: now.Add(-1 * time.Minute)}
	older := &jobs.RunItem{ID: 99, Number: 2, Repo: "older", Workflow: "CI", Status: jobs.JobPassed, JobsKnown: true,
		StartedAt: now.Add(-12 * time.Minute), CompletedAt: now.Add(-10 * time.Minute)}
	m.JobQueue = []*jobs.JobItem{
		{ID: "run:99", RunID: 99, Repo: "older", Run: older, IsRunHeader: true, Status: jobs.JobPassed},
		{ID: "run:10", RunID: 10, Repo: "newer", Run: newer, IsRunHeader: true, Status: jobs.JobPassed},
	}
	panel := stripped(m.recentPanel(m.buildOrgSummary(), 80, 5))
	lines := strings.Split(panel, "\n")
	if len(lines) < 2 || !strings.Contains(lines[1], "newer") {
		t.Fatalf("recent runs did not put the newest completion first:\n%s", panel)
	}
}

// A truncated in-flight list must say so, and the header must count every row
// it claims to show.
func TestInFlightDisclosesOverflow(t *testing.T) {
	m := demoFixtureModel()
	s := m.buildOrgSummary()
	panel := stripped(m.inFlightPanel(s, 46, 3))
	if !strings.Contains(panel, "IN FLIGHT") || !strings.Contains(panel, "more") {
		t.Fatalf("truncated in-flight list hid its overflow:\n%s", panel)
	}
}

// The selected table row must keep the detail column exactly where the
// unselected rows put it.
func TestTableSelectionKeepsColumnAlignment(t *testing.T) {
	m := smokeFixtureModel()
	m.ActiveFocus = FocusRepos
	m.handleWindowSizeMsg(tea.WindowSizeMsg{Width: 120, Height: 30})
	m.moveSelection(0)
	lines := strings.Split(stripped(m.listContentWidth(118)), "\n")
	if len(lines) < 3 {
		t.Fatalf("short table:\n%s", strings.Join(lines, "\n"))
	}
	selectedIdx := strings.Index(lines[1], "feat/screens")
	unselectedIdx := strings.Index(lines[2], "main")
	if selectedIdx < 0 || unselectedIdx < 0 {
		t.Fatalf("detail missing:\n%s", strings.Join(lines, "\n"))
	}
	selectedCol := ansi.StringWidth(lines[1][:selectedIdx])
	unselectedCol := ansi.StringWidth(lines[2][:unselectedIdx])
	if selectedCol != unselectedCol {
		t.Fatalf("selection shifted the detail column: selected=%d unselected=%d\n%s", selectedCol, unselectedCol, strings.Join(lines, "\n"))
	}
}

// Wide lists use one row per item, and the preview column names itself.
func TestWideListUsesTableAndPreview(t *testing.T) {
	m := smokeFixtureModel()
	m.ActiveFocus = FocusRepos
	m.handleWindowSizeMsg(tea.WindowSizeMsg{Width: 160, Height: 40})
	frame := stripped(m.View())
	if !strings.Contains(frame, "REPOSITORY") || !strings.Contains(frame, "BRANCH · COUNTS") {
		t.Fatalf("wide list has no column headings:\n%s", frame)
	}
	if !strings.Contains(frame, "PREVIEW") || !strings.Contains(frame, "Branches") {
		t.Fatalf("wide list has no preview:\n%s", frame)
	}
}

// A click on the first table row selects the first item, after the column
// heading row.
func TestTableMouseSelectionUsesOneRowPerItem(t *testing.T) {
	m := smokeFixtureModel()
	m.ActiveFocus = FocusRepos
	m.handleWindowSizeMsg(tea.WindowSizeMsg{Width: 120, Height: 30})
	m.ScreenCursor[FocusRepos] = ""
	m.SelectedIndex = -1
	m.screenMouse(tea.MouseMsg{X: 5, Y: 4, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if m.SelectedIndex != 0 {
		t.Fatalf("table click selected %d, want 0", m.SelectedIndex)
	}
}
