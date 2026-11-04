package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/seankoji-com/freshen/pkg/git"
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
