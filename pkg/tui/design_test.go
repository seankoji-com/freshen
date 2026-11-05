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

// The Actions Recent view must use the same completion ordering as the
// Overview's recent panel, not the transport ID order.
func TestActionsRecentOrdersByCompletion(t *testing.T) {
	m := smokeFixtureModel()
	m.ActiveFocus = FocusJobs
	m.ActionsFilter = 2
	now := time.Now()
	newer := &jobs.RunItem{ID: 10, Number: 1, Repo: "newer", Workflow: "CI", Status: jobs.JobPassed, JobsKnown: true,
		StartedAt: now.Add(-3 * time.Minute), CompletedAt: now.Add(-1 * time.Minute)}
	older := &jobs.RunItem{ID: 99, Number: 2, Repo: "older", Workflow: "CI", Status: jobs.JobPassed, JobsKnown: true,
		StartedAt: now.Add(-12 * time.Minute), CompletedAt: now.Add(-10 * time.Minute)}
	m.JobQueue = []*jobs.JobItem{
		{ID: "run:99", RunID: 99, Repo: "older", Run: older, IsRunHeader: true, Status: jobs.JobPassed},
		{ID: "run:10", RunID: 10, Repo: "newer", Run: newer, IsRunHeader: true, Status: jobs.JobPassed},
	}
	entries := m.entries()
	if len(entries) < 1 || !strings.Contains(entries[0].title, "newer") {
		t.Fatalf("Actions Recent did not put the newest completion first: %+v", entries)
	}
}

// An archived status and the archived flag are the same fact; counts must not
// disagree with the health panel's label.
func TestArchivedCountMatchesStatus(t *testing.T) {
	m := smokeFixtureModel()
	m.Repos = append(m.Repos, &git.RepoItem{Name: "old", Path: "/tmp/old", Status: git.StatusArchived})
	s := m.buildOrgSummary()
	if s.archived != 1 {
		t.Fatalf("archived count = %d, want 1", s.archived)
	}
	view := stripped(m.summaryContent(160, 40))
	if strings.Contains(view, "0 archived") {
		t.Fatalf("Overview says 0 archived while an archived repository is present:\n%s", view)
	}
}

// A not-cloned repository must not repeat its status as its branch detail.
func TestHealthPanelDoesNotRepeatStatus(t *testing.T) {
	m := smokeFixtureModel()
	m.Repos[1].IsNew = true
	m.Repos[1].CurrentBranch = ""
	m.Repos[1].HasLoadedCounts = true
	view := stripped(m.repoHealthPanel(m.buildOrgSummary(), 100, 6))
	if strings.Contains(view, "not cloned  not cloned") {
		t.Fatalf("health panel repeated the not-cloned status:\n%s", view)
	}
}

// A complete snapshot with only open-PR review items is "review", not
// "partial" (that word is reserved for incomplete data).
func TestSnapshotChipNamesReviewNotPartial(t *testing.T) {
	m := smokeFixtureModel()
	label, _ := m.snapshotChip()
	if label != "review" {
		t.Fatalf("complete review-only snapshot chip = %q, want review", label)
	}
}

// A push from the default branch is refused before any worker starts.
func TestDefaultBranchPushRefuses(t *testing.T) {
	m := smokeFixtureModel()
	m.ActionTarget = &git.RepoItem{Name: "delta", Path: "/tmp/delta", CurrentBranch: "main", DefaultBranch: "main"}
	cmd := m.executeAction("push")
	if cmd != nil || m.BusyAction != "" || !strings.Contains(m.ToastMsg, "default branch") {
		t.Fatalf("default-branch push was not refused: cmd=%v busy=%q toast=%q", cmd, m.BusyAction, m.ToastMsg)
	}
}

// Count cells distinguish unknown and stale values, and divergence names both
// directions.
func TestCountCellAndDivergence(t *testing.T) {
	if got := countCell(0, false, false); got != "—" {
		t.Fatalf("unknown count = %q, want em dash", got)
	}
	if got := countCell(2, true, true); got != "2*" {
		t.Fatalf("stale count = %q, want 2*", got)
	}
	r := &git.RepoItem{HasUpstream: true, Ahead: 2, Behind: 1}
	if got := repoDivergence(r); got != "↑2 ↓1" {
		t.Fatalf("divergence = %q, want ↑2 ↓1", got)
	}
	r = &git.RepoItem{HasUpstream: true, Behind: 3}
	if got := repoDivergence(r); got != "↓3 behind" {
		t.Fatalf("divergence = %q, want ↓3 behind", got)
	}
}

// A runner refresh failure is partial data, not a review-only snapshot.
func TestRunnerFailureChipIsPartial(t *testing.T) {
	m := smokeFixtureModel()
	m.RunnerFetchFailed = true
	label, kind := m.snapshotChip()
	if label != "partial" || kind != kindWarning {
		t.Fatalf("runner failure chip = %q/%d, want partial/warning", label, kind)
	}
}

// CI is matched by the GitHub repository name when the local folder is aliased.
func TestRepositoryCIAlias(t *testing.T) {
	m := smokeFixtureModel()
	m.Repos[0].Name = "local-alpha"
	m.Repos[0].GHRepoName = "alpha"
	m.ActiveFocus = FocusRepos
	m.handleWindowSizeMsg(tea.WindowSizeMsg{Width: 120, Height: 30})
	if content := stripped(m.listContent()); !strings.Contains(content, "CI ↻ #42") {
		t.Fatalf("aliased repository lost its CI state:\n%s", content)
	}
}

// Entering a concern opens the tab that carries its signal.
func TestConcernsOpensReviewTab(t *testing.T) {
	m := smokeFixtureModel()
	m.ActiveFocus = FocusConcerns
	m.SelectedIndex = 0
	m.ScreenCursor[FocusConcerns] = m.Repos[0].Path + "/" + m.Repos[0].Name
	press(&m, "enter")
	if !m.Detail || m.ActiveTab != TabPRs {
		t.Fatalf("Concerns opened detail=%v tab=%v, want PRs", m.Detail, m.ActiveTab)
	}
}

// The push confirmation names the branch, changed files and commit message.
func TestPushConfirmationNamesFilesAndMessage(t *testing.T) {
	m := smokeFixtureModel()
	m.ActionTarget = m.Repos[0]
	m.ActionTarget.BranchDetails.ChangedFiles = []string{"M one.go", "A two.go"}
	m.PendingAction = "push"
	content := stripped(m.menuContent())
	for _, want := range []string{"Branch feat/screens → PR base main", "Changed files (2)", "Commit message: WIP: Updates on branch 'feat/screens'"} {
		if !strings.Contains(content, want) {
			t.Fatalf("push confirmation missing %q:\n%s", want, content)
		}
	}
}

// The repository table carries the newest CI run beside the branch.
func TestRepositoryTableShowsCI(t *testing.T) {
	m := smokeFixtureModel()
	m.ActiveFocus = FocusRepos
	m.handleWindowSizeMsg(tea.WindowSizeMsg{Width: 120, Height: 30})
	if content := stripped(m.listContent()); !strings.Contains(content, "CI ↻ #42") {
		t.Fatalf("repository table has no CI state:\n%s", content)
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
	selectedIdx := strings.Index(lines[1], "CI ↻")
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
	if !strings.Contains(frame, "REPOSITORY") || !strings.Contains(frame, "SIGNALS") || !strings.Contains(frame, "PRs") {
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
