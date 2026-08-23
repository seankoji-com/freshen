package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/seankoji-com/freshen/pkg/git"
	"github.com/seankoji-com/freshen/pkg/jobs"
)

// newTestModel builds a Model with a no-op context/cancel/WaitGroup for tests
// that don't exercise shutdown behavior directly.
func newTestModel(targetDir, targetOrg string) Model {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	return NewModel(targetDir, targetOrg, 4, ctx, cancel, &wg)
}

func TestFocusedRunViewportRendering(t *testing.T) {
	m := newTestModel("/tmp/test", "test-org")
	m.Width = 120
	m.Height = 40
	m.ActiveFocus = FocusJobs
	// Row 0 is the synthetic initiator header, row 1 the synthetic run
	// header (all 4 jobs share RunID 100), row 2 is the first job ("build").
	m.SelectedJobIndex = 2
	m.JobQueue = []*jobs.JobItem{
		{ID: "run-100", Name: "myrepo / ci", Repo: "myrepo", Status: jobs.JobRunning, RunID: 100, IsRunHeader: true, Event: "push", Branch: "main"},
		{ID: "#101", Name: "myrepo / ci / build", Repo: "myrepo", Status: jobs.JobRunning, RunID: 100, Event: "push", Branch: "main", Duration: "1m 30s", RunnerName: "runner-1"},
		{ID: "#102", Name: "myrepo / ci / test", Repo: "myrepo", Status: jobs.JobPassed, RunID: 100, Event: "push", Branch: "main", Duration: "45s", RunnerName: "runner-2"},
		{ID: "#103", Name: "myrepo / ci / lint", Repo: "myrepo", Status: jobs.JobQueued, RunID: 100, Event: "push", Branch: "main", Duration: "-", RunnerName: ""},
	}

	// Test without focus - should show individual job details
	m.FocusedRunID = 0
	m.updateViewport()
	unfocused := m.Viewport.View()
	if strings.Contains(unfocused, "RUN SUMMARY") || strings.Contains(unfocused, "3 jobs") {
		t.Errorf("unfocused viewport should not show run summary")
	}
	if !strings.Contains(unfocused, "Runner:") {
		t.Errorf("unfocused viewport should show Runner field")
	}

	// Test with focus - should show run summary
	m.FocusedRunID = 100
	m.updateViewport()
	focused := m.Viewport.View()
	if !strings.Contains(focused, "RUN SUMMARY") {
		t.Errorf("focused viewport should show 'RUN SUMMARY' header, got:\n%s", focused)
	}
	if !strings.Contains(focused, "3 jobs") {
		t.Errorf("focused viewport should show '3 jobs' total")
	}
	if !strings.Contains(focused, "build") || !strings.Contains(focused, "test") || !strings.Contains(focused, "lint") {
		t.Errorf("focused viewport should list all child jobs")
	}
	if !strings.Contains(focused, "1 running") {
		t.Errorf("focused viewport should show count of running jobs")
	}
	if !strings.Contains(focused, "1 passed") {
		t.Errorf("focused viewport should show count of passed jobs")
	}
	if !strings.Contains(focused, "1 queued") {
		t.Errorf("focused viewport should show count of queued jobs")
	}
	if !strings.Contains(focused, "Press Enter or Esc to unfocus") {
		t.Errorf("focused viewport should show unfocus instruction")
	}
}

func TestEnterUnfocusesWhenFocusedRunMatches(t *testing.T) {
	m := newTestModel("/tmp/test", "test-org")
	m.ActiveFocus = FocusJobs
	m.SelectedJobIndex = 0
	m.FocusedRunID = 100
	m.JobQueue = []*jobs.JobItem{
		{ID: "#101", Name: "myrepo / build", Repo: "myrepo", Status: jobs.JobRunning, RunID: 100},
	}

	msg := tea.KeyMsg{Type: tea.KeyEnter}
	newModel, _ := m.Update(msg)
	updated := newModel.(Model)

	if updated.FocusedRunID != 0 {
		t.Errorf("pressing Enter when FocusedRunID matches should unfocus (set to 0), got %d", updated.FocusedRunID)
	}
}

func TestEnterFocusesOnSelectedJobRun(t *testing.T) {
	m := newTestModel("/tmp/test", "test-org")
	m.ActiveFocus = FocusJobs
	// Row 0 is this job's synthetic initiator header (every job gets one);
	// row 1 is the job itself, since it's not part of a multi-job run.
	m.SelectedJobIndex = 1
	m.FocusedRunID = 0
	m.JobQueue = []*jobs.JobItem{
		{ID: "#101", Name: "myrepo / build", Repo: "myrepo", Status: jobs.JobRunning, RunID: 100},
	}

	msg := tea.KeyMsg{Type: tea.KeyEnter}
	newModel, _ := m.Update(msg)
	updated := newModel.(Model)

	if updated.FocusedRunID != 100 {
		t.Errorf("pressing Enter should focus on the selected job's run (100), got %d", updated.FocusedRunID)
	}
}

// Enter on an initiator header (row 0, above any single job) focuses the
// initiator instead of a run — there's no run to focus at that row.
func TestEnterFocusesOnSelectedInitiator(t *testing.T) {
	m := newTestModel("/tmp/test", "test-org")
	m.ActiveFocus = FocusJobs
	m.SelectedJobIndex = 0
	m.JobQueue = []*jobs.JobItem{
		{ID: "#101", Name: "myrepo / build", Repo: "myrepo", Status: jobs.JobRunning, RunID: 100, PRNumber: 7},
	}

	msg := tea.KeyMsg{Type: tea.KeyEnter}
	newModel, _ := m.Update(msg)
	updated := newModel.(Model)

	if updated.FocusedInitiatorKey != "myrepo#pr:7" {
		t.Errorf("pressing Enter on the initiator header should set FocusedInitiatorKey, got %q", updated.FocusedInitiatorKey)
	}
	if updated.FocusedRunID != 0 {
		t.Errorf("focusing an initiator should not also set FocusedRunID, got %d", updated.FocusedRunID)
	}
}

// buildJobQueueRows groups jobs by initiator (PR, else branch/event) and
// visually collates each initiator's jobs, inserting a run header above any
// run with more than one job — this is the panel's single source of truth
// for row layout, shared by rendering and click/keyboard selection.
func TestBuildJobQueueRows(t *testing.T) {
	t.Run("single job under one initiator gets only an initiator header", func(t *testing.T) {
		queue := []*jobs.JobItem{
			{Repo: "repo1", Name: "repo1 / build", RunID: 1, PRNumber: 7},
		}
		rows := buildJobQueueRows(queue)
		if len(rows) != 2 {
			t.Fatalf("expected 2 rows (initiator header + job), got %d: %+v", len(rows), rows)
		}
		if rows[0].kind != rowKindInitiatorHeader || rows[0].initiatorKey != "repo1#pr:7" {
			t.Errorf("expected row 0 to be the initiator header for repo1#pr:7, got %+v", rows[0])
		}
		if rows[1].kind != rowKindJob || rows[1].itemIndex != 0 {
			t.Errorf("expected row 1 to be the job itself, got %+v", rows[1])
		}
	})

	t.Run("multi-job run gets both an initiator and a run header", func(t *testing.T) {
		queue := []*jobs.JobItem{
			{Repo: "repo1", Name: "repo1 / lint", RunID: 1, PRNumber: 7},
			{Repo: "repo1", Name: "repo1 / test", RunID: 1, PRNumber: 7},
		}
		rows := buildJobQueueRows(queue)
		wantKinds := []jobQueueRowKind{rowKindInitiatorHeader, rowKindRunHeader, rowKindJob, rowKindJob}
		if len(rows) != len(wantKinds) {
			t.Fatalf("expected %d rows, got %d: %+v", len(wantKinds), len(rows), rows)
		}
		for i, want := range wantKinds {
			if rows[i].kind != want {
				t.Errorf("row %d: expected kind %v, got %v", i, want, rows[i].kind)
			}
		}
	})

	t.Run("collates an initiator's jobs even when interleaved with another initiator's in the queue", func(t *testing.T) {
		queue := []*jobs.JobItem{
			{Repo: "repo1", Name: "repo1 / a", RunID: 1, PRNumber: 7}, // initiator A
			{Repo: "repo1", Name: "repo1 / b", RunID: 2, PRNumber: 9}, // initiator B
			{Repo: "repo1", Name: "repo1 / c", RunID: 1, PRNumber: 7}, // initiator A again
		}
		rows := buildJobQueueRows(queue)
		var order []string
		for _, r := range rows {
			if r.kind == rowKindInitiatorHeader {
				order = append(order, r.initiatorKey)
			}
		}
		if len(order) != 2 || order[0] != "repo1#pr:7" || order[1] != "repo1#pr:9" {
			t.Fatalf("expected initiator headers in first-appearance order [repo1#pr:7 repo1#pr:9], got %v", order)
		}
		// Initiator A's two jobs (queue indices 0 and 2) must be contiguous,
		// not split across B's header.
		var aItemIndices []int
		for _, r := range rows {
			if r.kind == rowKindJob && jobInitiatorKey(queue[r.itemIndex]) == "repo1#pr:7" {
				aItemIndices = append(aItemIndices, r.itemIndex)
			}
		}
		if len(aItemIndices) != 2 || aItemIndices[0] != 0 || aItemIndices[1] != 2 {
			t.Errorf("expected initiator A's jobs (queue[0], queue[2]) collated together, got item indices %v", aItemIndices)
		}
	})

	t.Run("a fork PR job with no PRNumber falls back to branch grouping", func(t *testing.T) {
		queue := []*jobs.JobItem{
			{Repo: "repo1", Name: "repo1 / build", RunID: 1, PRNumber: 0, Branch: "feature/x"},
		}
		rows := buildJobQueueRows(queue)
		if rows[0].initiatorKey != "repo1#branch:feature/x" {
			t.Errorf("expected fallback to branch grouping, got initiator key %q", rows[0].initiatorKey)
		}
	})
}

func TestEscUnfocusesRun(t *testing.T) {
	m := newTestModel("/tmp/test", "test-org")
	m.ActiveFocus = FocusJobs
	m.FocusedRunID = 100
	m.JobQueue = []*jobs.JobItem{
		{ID: "#101", Name: "myrepo / build", Repo: "myrepo", Status: jobs.JobRunning, RunID: 100},
	}

	msg := tea.KeyMsg{Type: tea.KeyEsc}
	newModel, _ := m.Update(msg)
	updated := newModel.(Model)

	if updated.FocusedRunID != 0 {
		t.Errorf("pressing Esc should unfocus (set FocusedRunID to 0), got %d", updated.FocusedRunID)
	}
}

func TestViewLineCountNeverExceedsTerminalHeight(t *testing.T) {
	m := newTestModel("/tmp/test", "test-org")
	m.Width = 120
	m.Height = 40
	m.Repos = []*git.RepoItem{
		{Name: "repo1", CurrentBranch: "main", Status: git.StatusUpToDate},
		{Name: "repo2", CurrentBranch: "master", Status: git.StatusUpToDate},
	}
	m.Runners = []*jobs.RunnerItem{
		{ID: "runner-1", Name: "carey-mac-alpha", Status: jobs.RunnerIdle, Platform: "Linux/ARM64"},
	}
	m.JobQueue = []*jobs.JobItem{
		{ID: "#101", Name: "repo1 / test", Status: jobs.JobRunning, Repo: "repo1"},
	}

	view := m.View()
	lines := strings.Split(view, "\n")

	if len(lines) > m.Height {
		for i, l := range lines {
			fmt.Printf("[%d] %q\n", i, l)
		}
		t.Errorf("View() total line count (%d) exceeds terminal height (%d)", len(lines), m.Height)
	}
}

func TestHeaderBannerAndColumnHeadersSticky(t *testing.T) {
	m := newTestModel("/tmp/test", "test-org")
	m.Width = 120
	m.Height = 40
	m.Repos = []*git.RepoItem{
		{Name: "repo1", CurrentBranch: "main", Status: git.StatusUpToDate},
	}

	view := m.View()

	if !strings.Contains(view, "FRESHEN") {
		t.Errorf("View() missing top banner header 'FRESHEN'")
	}

	if !strings.Contains(view, "REPOSITORY") || !strings.Contains(view, "BRANCH") {
		t.Errorf("View() missing repository column titles ('REPOSITORY', 'BRANCH')")
	}
}

func TestRightPaneHeaderInViewInitially(t *testing.T) {
	m := newTestModel("/tmp/test", "test-org")
	m.Width = 120
	m.Height = 40
	m.Repos = []*git.RepoItem{
		{Name: ".dotfiles", CurrentBranch: "master", Status: git.StatusUpToDate},
	}
	m.updateViewport()

	if m.Viewport.YOffset != 0 {
		t.Errorf("expected Viewport YOffset to be 0 (top in view initially), got %d", m.Viewport.YOffset)
	}
}

func TestLoadJobQueueCmdRepoNameFallback(t *testing.T) {
	m := newTestModel("/tmp/test", "test-org")
	// Simulate local disk repos where GHRepoName is not yet populated from GitHub org sync
	m.Repos = []*git.RepoItem{
		{Name: ".dotfiles", GHRepoName: "", IsArchived: false},
		{Name: "freshen", GHRepoName: "", IsArchived: false},
		{Name: "archived-repo", GHRepoName: "", IsArchived: true},
	}

	cmd := m.loadJobQueueCmd()
	if cmd == nil {
		t.Fatalf("expected non-nil tea.Cmd from loadJobQueueCmd()")
	}
}

func TestRunnerView_NoWordBusy_HyperlinkJobTitle(t *testing.T) {
	m := newTestModel("/tmp/test", "test-org")
	m.Width = 120
	m.Height = 40
	m.ActiveFocus = FocusRunners
	m.SelectedRunnerIndex = 0

	m.Runners = []*jobs.RunnerItem{
		{ID: "runner-1", Name: "carey-mac-gamma", Status: jobs.RunnerRunning, Platform: "Linux/ARM64"},
	}
	m.JobQueue = []*jobs.JobItem{
		{
			ID:         "#93207615858",
			Name:       ".dotfiles / parallel-review / Reviewer - SRE",
			Repo:       ".dotfiles",
			Status:     jobs.JobRunning,
			RunnerName: "carey-mac-gamma",
			RunID:      31298579511,
			GHJobID:    93207615858,
		},
	}

	m.updateViewport()
	viewContent := m.Viewport.View()

	// 1. Current Job line must NOT contain standalone word "Busy"
	if strings.Contains(viewContent, "Busy (") {
		t.Errorf("expected Current Job to omit 'Busy (...)', got:\n%s", viewContent)
	}

	// 2. Current Job line must display the job title
	if !strings.Contains(viewContent, "parallel-review") {
		t.Errorf("expected Current Job to contain workflow job title, got:\n%s", viewContent)
	}

	// 3. Must contain OSC 8 hyperlink escape code pointing to GitHub Actions job URL
	expectedURL := "https://github.com/test-org/.dotfiles/actions/runs/31298579511/job/93207615858"
	if !strings.Contains(viewContent, expectedURL) {
		t.Errorf("expected Viewport to contain GitHub Actions job hyperlink URL %q", expectedURL)
	}
}

func TestTableOrderInFocusRunners(t *testing.T) {
	m := newTestModel("/tmp/test", "test-org")
	m.Width = 120
	m.Height = 40
	m.ActiveFocus = FocusRunners

	m.Runners = []*jobs.RunnerItem{
		{ID: "runner-1", Name: "carey-mac-alpha", Status: jobs.RunnerRunning, Platform: "Linux/ARM64"},
	}
	m.JobQueue = []*jobs.JobItem{
		{
			ID:         "#101",
			Name:       ".dotfiles / test",
			Status:     jobs.JobRunning,
			RunnerName: "carey-mac-alpha",
		},
	}

	m.updateViewport()
	viewContent := m.Viewport.View()

	allRunnersIdx := strings.Index(viewContent, "RUNNERS MATCHING")
	if allRunnersIdx == -1 {
		allRunnersIdx = strings.Index(viewContent, "ALL RUNNERS")
	}
	queuedJobsIdx := strings.Index(viewContent, "QUEUED / RUNNING JOBS ON")

	if allRunnersIdx == -1 || queuedJobsIdx == -1 {
		t.Fatalf("expected both 'RUNNERS MATCHING' and 'QUEUED / RUNNING JOBS ON' headers in view")
	}

	if allRunnersIdx > queuedJobsIdx {
		t.Errorf("expected 'ALL RUNNERS' table (idx %d) to appear BEFORE 'QUEUED / RUNNING JOBS ON THIS RUNNER' (idx %d)", allRunnersIdx, queuedJobsIdx)
	}
}

func TestPollingToastNotifications(t *testing.T) {
	m := newTestModel("/tmp/test", "test-org")
	m.Width = 120
	m.Height = 40

	m.JobQueue = []*jobs.JobItem{
		{ID: "#101", Name: ".dotfiles / build", Status: jobs.JobQueued},
	}

	// 1. Test Job status transition QUEUED -> RUNNING produces ToastMsg
	updatedQueue := []*jobs.JobItem{
		{ID: "#101", Name: ".dotfiles / build", Status: jobs.JobRunning, RunnerName: "carey-mac-beta"},
	}

	// Simulate receiving loadedJobQueueMsg
	msg := loadedJobQueueMsg{queue: updatedQueue, err: nil}
	newM, _ := m.Update(msg)
	updatedModel := newM.(Model)

	if updatedModel.ToastMsg == "" {
		t.Errorf("expected ToastMsg to be set on job state transition, got empty string")
	}
	if !strings.Contains(updatedModel.ToastMsg, "started running on carey-mac-beta") {
		t.Errorf("expected ToastMsg to contain 'started running on carey-mac-beta', got: %q", updatedModel.ToastMsg)
	}

	// 2. Test Runner status update produces NO ToastMsg (runner noise exclusion)
	runnerMsg := loadedRunnersMsg{
		runners: []*jobs.RunnerItem{
			{ID: "runner-1", Name: "carey-mac-alpha", Status: jobs.RunnerOffline},
		},
		err: nil,
	}
	m.ToastMsg = ""
	newM2, _ := m.Update(runnerMsg)
	updatedModel2 := newM2.(Model)

	if updatedModel2.ToastMsg != "" {
		t.Errorf("expected NO ToastMsg for runner status changes (ephemeral runner noise exclusion), got: %q", updatedModel2.ToastMsg)
	}
}

func TestHyperlinksInView(t *testing.T) {
	m := newTestModel("/tmp/test", "test-org")
	m.Width = 120
	m.Height = 40
	m.ActiveFocus = FocusJobs
	// Row 0 is this job's synthetic initiator header (every job gets one);
	// row 1 is the job itself.
	m.SelectedJobIndex = 1

	m.JobQueue = []*jobs.JobItem{
		{
			ID:         "#93207615858",
			Name:       ".dotfiles / test",
			Repo:       ".dotfiles",
			Status:     jobs.JobRunning,
			RunnerName: "carey-mac-alpha",
			RunID:      31298579511,
			GHJobID:    93207615858,
		},
	}

	m.updateViewport()
	viewContent := m.Viewport.View()

	// Verify OSC 8 Hyperlink syntax \x1b]8;;url\x1b\
	if !strings.Contains(viewContent, "\x1b]8;;https://github.com/test-org/.dotfiles/actions/runs/31298579511/job/93207615858\x1b\\") {
		t.Errorf("expected FocusJobs header to contain valid OSC 8 hyperlink for Job ID")
	}
}

func TestIssue37Fixes(t *testing.T) {
	m := newTestModel("/tmp/test", "test-org")
	m.Width = 120
	m.Height = 40
	m.IsOrgSyncing = false
	m.Repos = []*git.RepoItem{
		{Name: "myrepo", CurrentBranch: "feat/long-branch-name-feature-x", Status: git.StatusUpToDate},
	}

	// 1. Verify tab bar active styling
	m.ActiveTab = TabLogs
	tabBar := m.renderTabBar()
	if !strings.Contains(tabBar, "[1 Logs]") || !strings.Contains(tabBar, "[2 Branches & Worktrees]") {
		t.Errorf("renderTabBar missing expected tab headers, got: %q", tabBar)
	}

	// 2. Verify cellBranchStyle width is at least 20
	if cellBranchStyle.GetWidth() < 20 {
		t.Errorf("expected cellBranchStyle width to be at least 20, got %d", cellBranchStyle.GetWidth())
	}

	// 3. Verify View includes extended branch name beyond 16 chars
	view := m.View()
	if !strings.Contains(view, "feat/long-branch") {
		t.Errorf("expected View to contain extended branch name, got:\n%s", view)
	}
}

func TestRepoTableCountsDifferentiation(t *testing.T) {
	m := newTestModel("/tmp/test", "test-org")
	m.Width = 120
	m.Height = 40
	m.IsOrgSyncing = false
	m.ActiveFocus = FocusRepos

	m.Repos = []*git.RepoItem{
		{
			Name:            "repo-loading",
			GHRepoName:      "repo-loading",
			HasLoadedCounts: false,
			HasLoadedPRs:    false,
			HasLoadedIssues: false,
		},
		{
			Name:            "repo-zero",
			GHRepoName:      "repo-zero",
			HasLoadedCounts: true,
			OpenPRsCount:    0,
			OpenIssuesCount: 0,
		},
		{
			Name:            "repo-counts",
			GHRepoName:      "repo-counts",
			HasLoadedCounts: true,
			OpenPRsCount:    5,
			OpenIssuesCount: 12,
		},
	}

	viewContent := m.View()
	if !strings.Contains(viewContent, "?") {
		t.Errorf("expected view to contain '?' for loading counts, got:\n%s", viewContent)
	}
	if !strings.Contains(viewContent, "—") {
		t.Errorf("expected view to contain '—' (em-dash) for confirmed zero counts, got:\n%s", viewContent)
	}
	if !strings.Contains(viewContent, "5") {
		t.Errorf("expected view to contain formatted number 5, got:\n%s", viewContent)
	}
}

func TestTabBranchesGroupedRendering(t *testing.T) {
	m := newTestModel("/tmp/test", "test-org")
	m.Width = 120
	m.Height = 40
	m.IsOrgSyncing = false
	m.ActiveFocus = FocusRepos
	m.ActiveTab = TabBranches

	m.Repos = []*git.RepoItem{
		{
			Name:          "test-repo",
			GHRepoName:    "test-repo",
			CurrentBranch: "main",
			DefaultBranch: "main",
			BranchDetails: git.BranchWorktreeDetails{
				Branches: []string{
					"* main",
					"feature-abc",
					"remotes/origin/HEAD -> origin/main",
					"remotes/origin/main",
					"remotes/origin/feature-abc",
				},
				Worktrees: []string{"/tmp/test/test-repo  [main]"},
			},
		},
	}

	m.updateViewport()
	viewContent := m.Viewport.View()

	if !strings.Contains(viewContent, "Local Branches:") {
		t.Errorf("expected Viewport to contain 'Local Branches:' header, got:\n%s", viewContent)
	}
	if !strings.Contains(viewContent, "Remote Branches:") {
		t.Errorf("expected Viewport to contain 'Remote Branches:' header, got:\n%s", viewContent)
	}
}

func TestFooterSeparationAndLineWidthBounds(t *testing.T) {
	m := newTestModel("/tmp/test", "test-org")
	m.Width = 160
	m.Height = 40
	m.IsOrgSyncing = false
	m.Repos = []*git.RepoItem{
		{Name: "repo1", CurrentBranch: "main", Status: git.StatusUpToDate},
	}

	view := m.View()
	lines := strings.Split(view, "\n")

	if len(lines) != m.Height {
		t.Errorf("expected View() total line count to equal m.Height (%d), got %d lines", m.Height, len(lines))
	}

	lastLine := lines[len(lines)-1]
	if !strings.Contains(lastLine, "Focus") || !strings.Contains(lastLine, "Quit") {
		t.Errorf("expected last line of View() to be footer keybindings help, got: %q", lastLine)
	}

	for i, line := range lines {
		if lipgloss.Width(line) > m.Width {
			t.Errorf("line %d width (%d) exceeds m.Width (%d): %q", i, lipgloss.Width(line), m.Width, line)
		}
	}
}

func TestPanelBoundaryCrossingToasts(t *testing.T) {
	m := newTestModel("/tmp/test", "test-org")
	m.Repos = []*git.RepoItem{
		{Name: "repo1", CurrentBranch: "main", Status: git.StatusUpToDate},
	}
	m.Runners = []*jobs.RunnerItem{
		{ID: "runner-1", Name: "carey-mac-alpha", Status: jobs.RunnerIdle},
	}
	m.JobQueue = []*jobs.JobItem{
		{ID: "#101", Name: "repo1 / test", Status: jobs.JobRunning, Repo: "repo1"},
	}

	// 1. Move down from Repos to Runners
	m.ActiveFocus = FocusRepos
	m.SelectedIndex = 0
	msgDown := tea.KeyMsg{Type: tea.KeyDown}

	m2, _ := m.Update(msgDown)
	updated2 := m2.(Model)
	if updated2.ActiveFocus != FocusRunners {
		t.Errorf("expected ActiveFocus FocusRunners, got %v", updated2.ActiveFocus)
	}
	if !strings.Contains(updated2.ToastMsg, "Focused Runners Panel") {
		t.Errorf("expected ToastMsg to contain Focused Runners Panel, got %q", updated2.ToastMsg)
	}

	// 2. Move down from Runners to Jobs
	m3, _ := updated2.Update(msgDown)
	updated3 := m3.(Model)
	if updated3.ActiveFocus != FocusJobs {
		t.Errorf("expected ActiveFocus FocusJobs, got %v", updated3.ActiveFocus)
	}
	if !strings.Contains(updated3.ToastMsg, "Focused Jobs Panel") {
		t.Errorf("expected ToastMsg to contain Focused Jobs Panel, got %q", updated3.ToastMsg)
	}

	// 3. Move up from Jobs to Runners
	msgUp := tea.KeyMsg{Type: tea.KeyUp}
	m4, _ := updated3.Update(msgUp)
	updated4 := m4.(Model)
	if updated4.ActiveFocus != FocusRunners {
		t.Errorf("expected ActiveFocus FocusRunners, got %v", updated4.ActiveFocus)
	}
	if !strings.Contains(updated4.ToastMsg, "Focused Runners Panel") {
		t.Errorf("expected ToastMsg to contain Focused Runners Panel, got %q", updated4.ToastMsg)
	}

	// 4. Move up from Runners to Repos
	m5, _ := updated4.Update(msgUp)
	updated5 := m5.(Model)
	if updated5.ActiveFocus != FocusRepos {
		t.Errorf("expected ActiveFocus FocusRepos, got %v", updated5.ActiveFocus)
	}
	if !strings.Contains(updated5.ToastMsg, "Focused Repositories Panel") {
		t.Errorf("expected ToastMsg to contain Focused Repositories Panel, got %q", updated5.ToastMsg)
	}
}

func TestTruncateStringMultiByteUTF8(t *testing.T) {
	// Toast containing multi-byte arrow glyphs and emoji
	multibyteStr := " ⚠ Job queue may be incomplete: 18 repo(s) had errors: ⚡ carey-mac-alpha ↓ Runners"
	truncated := truncateString(multibyteStr, 30)

	if !utf8.ValidString(truncated) {
		t.Errorf("truncateString produced invalid UTF-8 string: %q", truncated)
	}

	runes := []rune(truncated)
	if len(runes) > 30 {
		t.Errorf("expected rune count <= 30, got %d for %q", len(runes), truncated)
	}
}

// applyRepoSnapshot merges the sync-owned fields of a background snapshot onto
// the live item, without clobbering issue/PR state that other commands own.
func TestApplyRepoSnapshotMergesSyncFieldsOnly(t *testing.T) {
	m := newTestModel("/tmp", "acme")
	live := &git.RepoItem{
		Name:            "alpha",
		GHRepoName:      "alpha",
		Status:          git.StatusPending,
		Logs:            []string{"stale"},
		IssuesList:      []git.IssueItem{{Number: 3, Title: "keep me"}},
		HasLoadedIssues: true,
		OpenIssuesCount: 3,
	}
	m.Repos = []*git.RepoItem{live, {Name: "beta", Status: git.StatusPending}}

	m.applyRepoSnapshot(&git.RepoItem{
		Name:          "alpha",
		Status:        git.StatusUpdated,
		StatusMsg:     "Updated",
		CurrentBranch: "main",
		Stashed:       true,
		Logs:          []string{"one", "two"},
	})

	if live.Status != git.StatusUpdated || live.StatusMsg != "Updated" {
		t.Errorf("sync fields not applied: %s / %s", live.Status, live.StatusMsg)
	}
	if live.CurrentBranch != "main" || !live.Stashed {
		t.Errorf("branch/stash state not applied: %+v", live)
	}
	if len(live.Logs) != 2 {
		t.Errorf("expected the snapshot's logs, got %v", live.Logs)
	}
	// Issue state is loaded by a different command and must survive the merge.
	if !live.HasLoadedIssues || len(live.IssuesList) != 1 || live.OpenIssuesCount != 3 {
		t.Errorf("snapshot clobbered issue state: %+v", live)
	}
	if m.Repos[1].Status != git.StatusPending {
		t.Errorf("snapshot leaked onto an unrelated repo: %s", m.Repos[1].Status)
	}
}

// A snapshot for a repo that is no longer in the model (e.g. a periodic refresh
// dropped it) must be ignored rather than panic.
func TestApplyRepoSnapshotUnknownRepo(t *testing.T) {
	m := newTestModel("/tmp", "acme")
	m.Repos = []*git.RepoItem{{Name: "alpha", Status: git.StatusPending}}

	m.applyRepoSnapshot(&git.RepoItem{Name: "ghost", Status: git.StatusUpdated})
	m.applyRepoSnapshot(nil)

	if m.Repos[0].Status != git.StatusPending {
		t.Errorf("unknown snapshot altered the model: %s", m.Repos[0].Status)
	}
}

// startSyncCmd must hand workers private clones, never the model's own items.
func TestStartSyncCmdSkipsArchivedAndClones(t *testing.T) {
	m := newTestModel("/tmp", "acme")
	archived := &git.RepoItem{Name: "old", IsArchived: true, Status: git.StatusArchived}
	m.Repos = []*git.RepoItem{archived}

	// Every candidate is archived, so the stream finishes immediately without
	// starting a worker.
	msg := m.startSyncCmd(m.Repos, true, false)()
	finished, ok := msg.(syncFinishedMsg)
	if !ok || !finished.bulk {
		t.Fatalf("expected a bulk syncFinishedMsg, got %#v", msg)
	}
	if archived.Status != git.StatusArchived {
		t.Errorf("archived repo was touched: %s", archived.Status)
	}
}

// --- Orchestration command / message-handler error-branch coverage (#70) ---

func TestHandleLoadedRunnersMsg(t *testing.T) {
	t.Run("error surfaces toast and failure flag", func(t *testing.T) {
		m := newTestModel("/tmp/test", "test-org")
		m.IsRunnersLoading = true
		m.handleLoadedRunnersMsg(loadedRunnersMsg{err: errors.New("boom")})

		if m.IsRunnersLoading {
			t.Errorf("expected IsRunnersLoading false after load completes")
		}
		if !m.RunnerFetchFailed {
			t.Errorf("expected RunnerFetchFailed true on error")
		}
		if m.ConsecutiveErrors[fetchSourceRunners] != 1 {
			t.Errorf("expected runners ConsecutiveErrors == 1, got %d", m.ConsecutiveErrors[fetchSourceRunners])
		}
		if m.ToastPriority != 2 {
			t.Errorf("expected error-priority toast (2), got %d", m.ToastPriority)
		}
		if !strings.Contains(m.ToastMsg, "Runner fetch failed") || !strings.Contains(m.ToastMsg, "boom") {
			t.Errorf("expected toast to mention the fetch failure, got %q", m.ToastMsg)
		}
	})

	t.Run("success clears failure flag and merges runners", func(t *testing.T) {
		m := newTestModel("/tmp/test", "test-org")
		m.RunnerFetchFailed = true
		m.ConsecutiveErrors = map[string]int{fetchSourceRunners: 3, fetchSourceJobQueue: 4}
		m.handleLoadedRunnersMsg(loadedRunnersMsg{runners: []*jobs.RunnerItem{{ID: "r1", Name: "runner-1"}}})

		if m.RunnerFetchFailed {
			t.Errorf("expected RunnerFetchFailed to clear on success")
		}
		if m.ConsecutiveErrors[fetchSourceRunners] != 0 {
			t.Errorf("expected runners ConsecutiveErrors reset to 0, got %d", m.ConsecutiveErrors[fetchSourceRunners])
		}
		// A healthy runner fetch must not cancel the job-queue poller's own
		// backoff — the counters are tracked per source.
		if m.ConsecutiveErrors[fetchSourceJobQueue] != 4 {
			t.Errorf("expected jobQueue ConsecutiveErrors untouched at 4, got %d", m.ConsecutiveErrors[fetchSourceJobQueue])
		}
		if len(m.Runners) != 1 {
			t.Errorf("expected 1 runner merged in, got %d", len(m.Runners))
		}
	})

	t.Run("success with no runners does not toast", func(t *testing.T) {
		m := newTestModel("/tmp/test", "test-org")
		m.handleLoadedRunnersMsg(loadedRunnersMsg{runners: nil})

		// The empty-runner state is surfaced in the runners panel rather than
		// as a recurring toast (see TestRenderRunnersPanelEmptyStateVariants).
		if m.ToastMsg != "" {
			t.Errorf("expected no toast for the empty-runners case, got %q", m.ToastMsg)
		}
		if m.RunnerFetchFailed {
			t.Errorf("expected RunnerFetchFailed to stay clear on a successful empty load")
		}
	})
}

func TestHandleLoadedJobQueueMsg(t *testing.T) {
	t.Run("error with empty queue sets failure flag and toast", func(t *testing.T) {
		m := newTestModel("/tmp/test", "test-org")
		m.IsJobQueueLoading = true
		cmd := m.handleLoadedJobQueueMsg(loadedJobQueueMsg{err: errors.New("api down")})

		if m.IsJobQueueLoading {
			t.Errorf("expected IsJobQueueLoading false after load completes")
		}
		if !m.JobQueueFetchFailed {
			t.Errorf("expected JobQueueFetchFailed true on error")
		}
		if m.ConsecutiveErrors[fetchSourceJobQueue] != 1 {
			t.Errorf("expected jobQueue ConsecutiveErrors == 1, got %d", m.ConsecutiveErrors[fetchSourceJobQueue])
		}
		if m.ConsecutiveErrors[fetchSourceRunners] != 0 {
			t.Errorf("expected runners ConsecutiveErrors untouched at 0, got %d", m.ConsecutiveErrors[fetchSourceRunners])
		}
		if m.ToastPriority != 2 || !strings.Contains(m.ToastMsg, "Job queue may be incomplete") {
			t.Errorf("expected error toast about the incomplete job queue, got %q (priority %d)", m.ToastMsg, m.ToastPriority)
		}
		if cmd != nil {
			t.Errorf("expected nil cmd when the errored fetch returned no partial data")
		}
	})

	t.Run("error with partial queue still applies it and returns a log-fetch cmd", func(t *testing.T) {
		m := newTestModel("/tmp/test", "test-org")
		m.ActiveFocus = FocusJobs
		partial := []*jobs.JobItem{
			{ID: "run-1", Repo: "repo1", Name: "repo1 / ci", Status: jobs.JobRunning, RunID: 1, IsRunHeader: true},
			{ID: "#1", Repo: "repo1", Name: "repo1 / ci / build", Status: jobs.JobRunning, RunID: 1},
		}
		// Row 0 is the synthetic initiator header, row 1 the synthetic run
		// header (both jobs share RunID 1), row 2 is the first actual job row.
		m.SelectedJobIndex = 2
		cmd := m.handleLoadedJobQueueMsg(loadedJobQueueMsg{queue: partial, err: errors.New("timeout")})

		if !m.JobQueueFetchFailed {
			t.Errorf("expected JobQueueFetchFailed true on error")
		}
		if len(m.JobQueue) == 0 {
			t.Errorf("expected partial queue data to still be applied to model state")
		}
		if cmd == nil {
			t.Errorf("expected a log-fetch cmd for the running selected job despite the error")
		}
	})

	t.Run("success resets failure state", func(t *testing.T) {
		m := newTestModel("/tmp/test", "test-org")
		m.JobQueueFetchFailed = true
		m.ConsecutiveErrors = map[string]int{fetchSourceJobQueue: 5, fetchSourceRunners: 2}
		m.handleLoadedJobQueueMsg(loadedJobQueueMsg{queue: nil})

		if m.JobQueueFetchFailed {
			t.Errorf("expected JobQueueFetchFailed to clear on success")
		}
		if m.ConsecutiveErrors[fetchSourceJobQueue] != 0 {
			t.Errorf("expected jobQueue ConsecutiveErrors reset to 0, got %d", m.ConsecutiveErrors[fetchSourceJobQueue])
		}
		if m.ConsecutiveErrors[fetchSourceRunners] != 2 {
			t.Errorf("expected runners ConsecutiveErrors untouched at 2, got %d", m.ConsecutiveErrors[fetchSourceRunners])
		}
	})
}

func TestHandleLoadedJobLogsMsg(t *testing.T) {
	t.Run("error with no existing logs surfaces the failure on the job", func(t *testing.T) {
		m := newTestModel("/tmp/test", "test-org")
		m.JobQueue = []*jobs.JobItem{{ID: "#1", Name: "repo1 / ci / build", RunID: 1}}
		m.handleLoadedJobLogsMsg(loadedJobLogsMsg{jobID: "#1", err: errors.New("connection reset")})

		logs := m.JobQueue[0].Logs
		if len(logs) != 1 || !strings.Contains(logs[0], "log fetch failed") || !strings.Contains(logs[0], "connection reset") {
			t.Errorf("expected job Logs to surface the fetch error, got %v", logs)
		}
	})

	t.Run("error for an unmatched job id is a no-op", func(t *testing.T) {
		m := newTestModel("/tmp/test", "test-org")
		m.JobQueue = []*jobs.JobItem{{ID: "#1", Name: "repo1 / ci / build", RunID: 1, Logs: []string{"existing log line"}}}
		m.handleLoadedJobLogsMsg(loadedJobLogsMsg{jobID: "#unknown", err: errors.New("boom")})

		logs := m.JobQueue[0].Logs
		if len(logs) != 1 || logs[0] != "existing log line" {
			t.Errorf("expected untouched job logs when jobID doesn't match, got %v", logs)
		}
	})

	t.Run("success overwrites logs and gh job id", func(t *testing.T) {
		m := newTestModel("/tmp/test", "test-org")
		m.JobQueue = []*jobs.JobItem{{ID: "#1", Name: "repo1 / ci / build", RunID: 1}}
		m.handleLoadedJobLogsMsg(loadedJobLogsMsg{jobID: "#1", ghJobID: 42, logs: []string{"line1", "line2"}})

		if len(m.JobQueue[0].Logs) != 2 || m.JobQueue[0].GHJobID != 42 {
			t.Errorf("expected logs and GHJobID to be updated, got logs=%v ghJobID=%d", m.JobQueue[0].Logs, m.JobQueue[0].GHJobID)
		}
	})
}

func TestHandleOrgSyncedMsg(t *testing.T) {
	t.Run("error surfaces toast and reports handled", func(t *testing.T) {
		m := newTestModel("/tmp/test", "test-org")
		m.IsOrgSyncing = true
		_, handled := m.handleOrgSyncedMsg(orgSyncedMsg{err: errors.New("gh auth expired")})

		if m.IsOrgSyncing {
			t.Errorf("expected IsOrgSyncing false after load completes")
		}
		if !handled {
			t.Errorf("expected handled=true on error")
		}
		if m.ToastPriority != 2 || !strings.Contains(m.ToastMsg, "Fetch failed") || !strings.Contains(m.ToastMsg, "gh auth expired") {
			t.Errorf("expected error toast about the fetch failure, got %q (priority %d)", m.ToastMsg, m.ToastPriority)
		}
	})

	t.Run("success without autoSync does not trigger a parallel sync", func(t *testing.T) {
		m := newTestModel("/tmp/test", "test-org")
		cmd, handled := m.handleOrgSyncedMsg(orgSyncedMsg{repos: []*git.RepoItem{{Name: "repo1"}}, autoSync: false})

		if handled {
			t.Errorf("expected handled=false on success")
		}
		if cmd != nil {
			t.Errorf("expected nil cmd when autoSync is false")
		}
		if m.IsSyncing {
			t.Errorf("expected IsSyncing to remain false when autoSync is false")
		}
	})

	t.Run("success with autoSync starts a parallel sync restricted to safe actions", func(t *testing.T) {
		orig := syncRepositoryFn
		defer func() { syncRepositoryFn = orig }()
		called := make(chan bool, 1) // carries the safeOnly arg it was invoked with
		syncRepositoryFn = func(ctx context.Context, item *git.RepoItem, emit git.SyncProgress, safeOnly bool) {
			called <- safeOnly
		}

		m := newTestModel("/tmp/test", "test-org")
		cmd, handled := m.handleOrgSyncedMsg(orgSyncedMsg{repos: []*git.RepoItem{{Name: "repo1"}}, autoSync: true})

		if handled {
			t.Errorf("expected handled=false on success")
		}
		if !m.IsSyncing {
			t.Errorf("expected IsSyncing true when autoSync triggers a parallel sync")
		}
		if cmd == nil {
			t.Fatalf("expected a non-nil sync cmd when autoSync is true")
		}

		select {
		case safeOnly := <-called:
			if !safeOnly {
				t.Error("the passive startup sync must call git.SyncRepository with safeOnly=true, so it never auto-switches or auto-rebases a feature branch")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("expected syncRepositoryFn to be invoked for the loaded repo")
		}

		// Drain the returned cmd so the stream (and its background worker)
		// resolves cleanly; since our fake never emits a snapshot, the
		// channel closes once the worker returns and this yields a
		// syncFinishedMsg.
		msg := cmd()
		if _, ok := msg.(syncFinishedMsg); !ok {
			t.Errorf("expected syncFinishedMsg, got %T", msg)
		}
	})
}

// TestStartSyncCmdSemaphoreCapsConcurrency proves the worker pool's
// semaphore only allows the configured concurrency limit (4, from
// newTestModel) of syncs to run at once, and that a 5th repo's sync is held
// back until a slot frees.
func TestStartSyncCmdSemaphoreCapsConcurrency(t *testing.T) {
	orig := syncRepositoryFn
	defer func() { syncRepositoryFn = orig }()

	const repoCount = 5 // one more than the concurrency limit (4)
	started := make(chan struct{}, repoCount)
	release := make(chan struct{})
	syncRepositoryFn = func(ctx context.Context, item *git.RepoItem, emit git.SyncProgress, safeOnly bool) {
		started <- struct{}{}
		<-release
	}

	m := newTestModel("/tmp/test", "test-org")
	repos := make([]*git.RepoItem, repoCount)
	for i := range repos {
		repos[i] = &git.RepoItem{Name: fmt.Sprintf("repo%d", i)}
	}
	m.Repos = repos

	cmd := m.startSyncCmd(m.Repos, true, false)
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()

	// Exactly 4 (the concurrency cap) should start without any release.
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatalf("expected 4 syncs to start concurrently, only observed %d", i)
		}
	}
	select {
	case <-started:
		t.Fatal("5th sync started before any slot was freed; semaphore did not cap concurrency")
	case <-time.After(100 * time.Millisecond):
		// expected: the 5th is blocked acquiring the semaphore.
	}

	// Free one slot; the 5th should now be able to start.
	release <- struct{}{}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("expected the 5th sync to start once a slot freed")
	}

	// Release the remaining 4 in-flight syncs so the command can finish.
	for i := 0; i < 4; i++ {
		release <- struct{}{}
	}

	select {
	case msg := <-done:
		if _, ok := msg.(syncFinishedMsg); !ok {
			t.Errorf("expected syncFinishedMsg, got %T", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("startSyncCmd did not complete after all slots were released")
	}
}

// TestStartSyncCmdContextCancellationStopsPendingWork proves that cancelling
// the model's context stops repos that haven't yet been dispatched from ever
// starting, even though already-dispatched syncs are allowed to finish (the
// loop breaks on ctx.Err() rather than aborting in-flight work).
func TestStartSyncCmdContextCancellationStopsPendingWork(t *testing.T) {
	orig := syncRepositoryFn
	defer func() { syncRepositoryFn = orig }()

	const repoCount = 6 // more than the concurrency limit (4), so repos remain pending
	started := make(chan struct{}, repoCount)
	release := make(chan struct{}, repoCount)
	syncRepositoryFn = func(ctx context.Context, item *git.RepoItem, emit git.SyncProgress, safeOnly bool) {
		started <- struct{}{}
		<-release
	}

	m := newTestModel("/tmp/test", "test-org")
	repos := make([]*git.RepoItem, repoCount)
	for i := range repos {
		repos[i] = &git.RepoItem{Name: fmt.Sprintf("repo%d", i)}
	}
	m.Repos = repos

	cmd := m.startSyncCmd(m.Repos, true, false)
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()

	// Let the concurrency cap's worth of syncs start and occupy every slot;
	// at this point at least 2 repos are still pending (never yet checked
	// against ctx or the semaphore).
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatalf("expected 4 syncs to start, only observed %d", i)
		}
	}

	m.cancel()

	// Free every slot generously. At most one more repo (whichever had
	// already passed its ctx check before cancellation landed) may still
	// start, but the loop breaks on the first ctx.Err() it observes, so the
	// last pending repo can never start once cancellation has landed.
	totalStarted := 4
	for i := 0; i < repoCount; i++ {
		release <- struct{}{}
		select {
		case <-started:
			totalStarted++
		case <-time.After(300 * time.Millisecond):
		}
	}

	if totalStarted >= repoCount {
		t.Errorf("expected fewer than %d syncs to run once cancellation landed, got %d", repoCount, totalStarted)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("startSyncCmd did not complete after releasing in-flight syncs")
	}
}

func TestLoadedRunnersMsgPermissionDenied(t *testing.T) {
	m := newTestModel("/tmp", "test-org")
	permErr := fmt.Errorf("gh api: gh: You must be an org admin or have the runners and runner groups fine-grained permission. (HTTP 403)")

	m.JobQueue = []*jobs.JobItem{
		{ID: "#1", Name: "myrepo / ci", Status: jobs.JobRunning, RunnerName: "actions-worker-1", RunnerID: "100"},
	}

	newModel, _ := m.Update(loadedRunnersMsg{err: permErr})
	updated := newModel.(Model)

	if !updated.RunnerPermissionDenied {
		t.Errorf("expected RunnerPermissionDenied to be true on HTTP 403 error")
	}
	if updated.ToastMsg != "" {
		t.Errorf("expected no error toast on runner permission denied, got %q", updated.ToastMsg)
	}
	if updated.ConsecutiveErrors[fetchSourceRunners] != 0 {
		t.Errorf("expected runners ConsecutiveErrors to be 0 for permission denied, got %d", updated.ConsecutiveErrors[fetchSourceRunners])
	}
	if len(updated.Runners) != 1 || updated.Runners[0].Name != "actions-worker-1" {
		t.Errorf("expected runner to be extracted from active job queue on permission denied, got %+v", updated.Runners)
	}

	updated.Width = 120
	updated.Height = 40
	view := updated.View()
	if !strings.Contains(view, "actions-worker-1") {
		t.Errorf("expected view to contain extracted runner name, got:\n%s", view)
	}
}

func TestIsRunnerPermissionError(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{fmt.Errorf("gh api: gh: You must be an org admin or have the runners and runner groups fine-grained permission. (HTTP 403)"), true},
		{fmt.Errorf("HTTP 403: Forbidden"), true},
		{fmt.Errorf("must be an org admin to access this resource"), true},
		{fmt.Errorf("permission denied"), true},
		{fmt.Errorf("network connection timed out"), false},
		{fmt.Errorf("500 Internal Server Error"), false},
	}

	for _, tt := range tests {
		got := isRunnerPermissionError(tt.err)
		if got != tt.want {
			t.Errorf("isRunnerPermissionError(%v) = %v; want %v", tt.err, got, tt.want)
		}
	}
}

func TestRenderRunnersPanelEmptyStateVariants(t *testing.T) {
	m := newTestModel("/tmp", "test-org")
	m.Width = 120
	m.Height = 40

	// 1. Normal empty state
	m.IsRunnersLoading = false
	m.RunnerFetchFailed = false
	m.RunnerPermissionDenied = false
	viewNormal := m.View()
	if !strings.Contains(viewNormal, "No active self-hosted runners or jobs detected.") {
		t.Errorf("expected normal empty state message, got:\n%s", viewNormal)
	}

	// 2. Generic fetch failure
	m.RunnerFetchFailed = true
	m.RunnerPermissionDenied = false
	viewFail := m.View()
	if !strings.Contains(viewFail, "Failed to fetch registered runners.") {
		t.Errorf("expected fetch failed message, got:\n%s", viewFail)
	}

	// 3. Permission denied with no active jobs
	m.RunnerFetchFailed = true
	m.RunnerPermissionDenied = true
	viewPerm := m.View()
	if !strings.Contains(viewPerm, "No active self-hosted runners or jobs detected.") {
		t.Errorf("expected clean message without 403 error text, got:\n%s", viewPerm)
	}
}

func TestRenderStatusBadgeCloned(t *testing.T) {
	m := newTestModel("/tmp", "test-org")
	item := &git.RepoItem{
		Name:   "repo1",
		Status: git.StatusCloned,
	}
	badge := m.renderStatusBadge(item)
	if badge == "" {
		t.Errorf("expected non-empty badge for StatusCloned")
	}
}

func TestVimNavigationJK(t *testing.T) {
	m := newTestModel("/tmp", "test-org")
	m.Repos = []*git.RepoItem{
		{Name: "repo1", Status: git.StatusUpToDate},
		{Name: "repo2", Status: git.StatusUpToDate},
	}
	m.Runners = []*jobs.RunnerItem{
		{ID: "r1", Name: "runner1", Status: jobs.RunnerIdle},
	}
	m.JobQueue = []*jobs.JobItem{
		{ID: "#1", Name: "job1", Status: jobs.JobRunning},
	}

	// 1. Press 'j' to move down repo list
	m.ActiveFocus = FocusRepos
	m.SelectedIndex = 0
	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = newM.(Model)
	if m.SelectedIndex != 1 || m.ActiveFocus != FocusRepos {
		t.Errorf("expected SelectedIndex=1, ActiveFocus=FocusRepos after 'j', got idx=%d focus=%d", m.SelectedIndex, m.ActiveFocus)
	}

	// 2. Press 'j' at bottom of repos to transition to runners
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = newM.(Model)
	if m.ActiveFocus != FocusRunners {
		t.Errorf("expected transition to FocusRunners, got %d", m.ActiveFocus)
	}

	// 3. Press 'k' at top of runners to transition back to repos
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m = newM.(Model)
	if m.ActiveFocus != FocusRepos || m.SelectedIndex != 1 {
		t.Errorf("expected transition back to FocusRepos at bottom, got focus=%d idx=%d", m.ActiveFocus, m.SelectedIndex)
	}

	// 4. Press 'k' to move up in repos
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m = newM.(Model)
	if m.SelectedIndex != 0 {
		t.Errorf("expected SelectedIndex=0 after 'k', got %d", m.SelectedIndex)
	}
}

func TestTabNumberKeys1234(t *testing.T) {
	m := newTestModel("/tmp", "test-org")
	m.Repos = []*git.RepoItem{
		{Name: "repo1", GHRepoName: "repo1", Status: git.StatusUpToDate},
	}

	// Test '2' -> TabBranches
	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	m = newM.(Model)
	if m.ActiveTab != TabBranches {
		t.Errorf("expected TabBranches after '2', got %d", m.ActiveTab)
	}

	// Test '3' -> TabIssues
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	m = newM.(Model)
	if m.ActiveTab != TabIssues {
		t.Errorf("expected TabIssues after '3', got %d", m.ActiveTab)
	}

	// Test '4' -> TabPRs
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'4'}})
	m = newM.(Model)
	if m.ActiveTab != TabPRs {
		t.Errorf("expected TabPRs after '4', got %d", m.ActiveTab)
	}

	// Test '1' -> TabLogs
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	m = newM.(Model)
	if m.ActiveTab != TabLogs {
		t.Errorf("expected TabLogs after '1', got %d", m.ActiveTab)
	}
}

func TestSyncAllKeyAS(t *testing.T) {
	m := newTestModel("/tmp", "test-org")
	m.Repos = []*git.RepoItem{
		{Name: "repo1", Status: git.StatusPending},
	}

	newM, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = newM.(Model)

	if !m.IsSyncing {
		t.Errorf("expected IsSyncing to be true after pressing 'a'")
	}
	if cmd == nil {
		t.Errorf("expected cmd to be returned for parallel sync")
	}
	if !strings.Contains(m.ToastMsg, "Starting parallel sync") {
		t.Errorf("expected sync toast message, got %q", m.ToastMsg)
	}
}

func TestMouseWheelScrolling(t *testing.T) {
	m := newTestModel("/tmp", "test-org")
	m.Width = 100
	m.Height = 30
	m.Repos = []*git.RepoItem{
		{Name: "repo1", Logs: make([]string, 50)},
	}
	m.updateViewport()

	// Initial offset is 0
	if m.Viewport.YOffset != 0 {
		t.Errorf("expected initial YOffset=0, got %d", m.Viewport.YOffset)
	}

	// Scroll down
	wheelDown := tea.MouseMsg{Button: tea.MouseButtonWheelDown}
	newM, _ := m.Update(wheelDown)
	m = newM.(Model)
	if m.Viewport.YOffset == 0 {
		t.Errorf("expected YOffset > 0 after MouseButtonWheelDown")
	}

	// Scroll up
	wheelUp := tea.MouseMsg{Button: tea.MouseButtonWheelUp}
	newM, _ = m.Update(wheelUp)
	m = newM.(Model)
	if m.Viewport.YOffset != 0 {
		t.Errorf("expected YOffset=0 after scrolling back up, got %d", m.Viewport.YOffset)
	}
}

func TestMultiResolutionRendering(t *testing.T) {
	resolutions := []struct {
		w, h int
	}{
		{80, 24},
		{100, 30},
		{120, 40},
		{160, 50},
		{200, 60},
	}

	for _, res := range resolutions {
		m := newTestModel("/tmp", "test-org")
		m.Width = res.w
		m.Height = res.h
		m.Repos = []*git.RepoItem{
			{Name: "alpha-long-repository-name", CurrentBranch: "feat/cool-feature", Status: git.StatusUpdated, OpenIssuesCount: 3, OpenPRsCount: 1},
			{Name: "beta", CurrentBranch: "main", Status: git.StatusUpToDate},
		}
		m.Runners = []*jobs.RunnerItem{
			{ID: "r1", Name: "mac-mini-runner", Status: jobs.RunnerRunning, Platform: "macOS/ARM64", Tags: []string{"self-hosted"}},
		}
		m.JobQueue = []*jobs.JobItem{
			{ID: "#42", Name: "alpha / ci / build", Status: jobs.JobRunning, Duration: "1m 12s", RunnerName: "mac-mini-runner"},
		}

		view := m.View()
		lines := strings.Split(view, "\n")
		if len(lines) > res.h {
			for i, l := range lines {
				t.Logf("[%d] (w=%d) %q", i, lipgloss.Width(l), l)
			}
			t.Errorf("Resolution %dx%d: rendered %d lines exceeding max height %d", res.w, res.h, len(lines), res.h)
		}
		for i, line := range lines {
			lineWidth := lipgloss.Width(line)
			if lineWidth > res.w {
				t.Errorf("Resolution %dx%d Line %d: width %d exceeds max width %d (%q)", res.w, res.h, i, lineWidth, res.w, line)
			}
		}
	}
}

func TestTabKeyJumpsBetweenLeftPanels(t *testing.T) {
	m := newTestModel("/tmp", "test-org")
	m.ActiveFocus = FocusRepos

	// 1. Tab: Repos -> Runners
	newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = newM.(Model)
	if m.ActiveFocus != FocusRunners {
		t.Errorf("expected FocusRunners after Tab, got %d", m.ActiveFocus)
	}

	// 2. Tab: Runners -> Jobs
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = newM.(Model)
	if m.ActiveFocus != FocusJobs {
		t.Errorf("expected FocusJobs after Tab, got %d", m.ActiveFocus)
	}

	// 3. Tab: Jobs -> Repos
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = newM.(Model)
	if m.ActiveFocus != FocusRepos {
		t.Errorf("expected FocusRepos after Tab, got %d", m.ActiveFocus)
	}

	// 4. Shift+Tab: Repos -> Jobs
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = newM.(Model)
	if m.ActiveFocus != FocusJobs {
		t.Errorf("expected FocusJobs after Shift+Tab, got %d", m.ActiveFocus)
	}

	// 5. Shift+Tab: Jobs -> Runners
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = newM.(Model)
	if m.ActiveFocus != FocusRunners {
		t.Errorf("expected FocusRunners after Shift+Tab, got %d", m.ActiveFocus)
	}

	// 6. Shift+Tab: Runners -> Repos
	newM, _ = m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = newM.(Model)
	if m.ActiveFocus != FocusRepos {
		t.Errorf("expected FocusRepos after Shift+Tab, got %d", m.ActiveFocus)
	}
}

func TestRepoTableNoLineWrappingAndAlignment(t *testing.T) {
	widths := []int{80, 100, 120, 140, 180, 240}

	for _, w := range widths {
		m := newTestModel("/tmp/test", "test-org")
		m.Width = w
		m.Height = 40
		m.ActiveFocus = FocusRepos
		m.IsOrgSyncing = false
		m.SelectedIndex = 0
		m.Repos = []*git.RepoItem{
			{Name: ".dotfiles", CurrentBranch: "master", Status: git.StatusUpToDate, OpenPRsCount: 2, OpenIssuesCount: 9, HasLoadedCounts: true},
			{Name: "kumon-automation", CurrentBranch: "feat/long-branch-name-12345", Status: git.StatusCloned, OpenPRsCount: 12, OpenIssuesCount: 45, HasLoadedCounts: true},
			{Name: "careynas.net", CurrentBranch: "main", Status: git.StatusStashedApplied, OpenPRsCount: 0, OpenIssuesCount: 1, HasLoadedCounts: true},
		}

		view := m.View()
		lines := strings.Split(view, "\n")

		// The repo table should never wrap rows onto subsequent lines (such as a solitary '9' or '45')
		for lineIdx, line := range lines {
			trimmed := strings.TrimSpace(line)
			// A line containing only a number or punctuation indicates wrapping
			if trimmed == "9" || trimmed == "45" || trimmed == "2" || trimmed == "12" {
				t.Errorf("Width %d Line %d: orphan wrapped number found: %q\nFull View:\n%s", w, lineIdx, trimmed, view)
			}
		}

		// The first selected row (.dotfiles) should contain both PR count '2' and Issue count '9' on the same line
		foundDotfilesWithCounts := false
		for _, line := range lines {
			if strings.Contains(line, ".dotf") {
				if strings.Contains(line, "2") && strings.Contains(line, "9") {
					foundDotfilesWithCounts = true
				} else {
					t.Errorf("Width %d: .dotfiles row missing PR or Issue count: %q", w, line)
				}
			}
		}
		if !foundDotfilesWithCounts {
			t.Errorf("Width %d: .dotfiles row with counts not found in view", w)
		}
	}
}

func TestExactUserScreenshotLayout(t *testing.T) {
	widths := []int{80, 100, 120, 140, 160}
	for _, w := range widths {
		m := newTestModel("/Users/seankoji/repos", "seankoji-com")
		m.Width = w
		m.Height = 35
		m.ActiveFocus = FocusRepos
		m.IsOrgSyncing = false
		m.SelectedIndex = 0
		m.Repos = []*git.RepoItem{
			{Name: ".dotfiles", CurrentBranch: "fix/allow-zen-cleanup", Status: git.StatusPending, OpenPRsCount: 0, OpenIssuesCount: 7, HasLoadedCounts: true},
			{Name: "carey-family", CurrentBranch: "main", Status: git.StatusPending, OpenPRsCount: 0, OpenIssuesCount: 0, HasLoadedCounts: true},
			{Name: "carey-finance", CurrentBranch: "master", Status: git.StatusPending, OpenPRsCount: 4, OpenIssuesCount: 12, HasLoadedCounts: true},
			{Name: "careynas.net", CurrentBranch: "main", Status: git.StatusPending, OpenPRsCount: 2, OpenIssuesCount: 1, HasLoadedCounts: true},
			{Name: "claude-plugins", CurrentBranch: "master", Status: git.StatusPending, OpenPRsCount: 2, OpenIssuesCount: 0, HasLoadedCounts: true},
			{Name: "crabbie", CurrentBranch: "main", Status: git.StatusPending, OpenPRsCount: 0, OpenIssuesCount: 0, HasLoadedCounts: true},
			{Name: "croo2", CurrentBranch: "main", Status: git.StatusPending, OpenPRsCount: 0, OpenIssuesCount: 0, HasLoadedCounts: true},
			{Name: "freshen", CurrentBranch: "feat/complete-oss-readiness", Status: git.StatusPending, OpenPRsCount: 1, OpenIssuesCount: 0, HasLoadedCounts: true},
			{Name: "frugalbar", CurrentBranch: "", Status: git.StatusPending, OpenPRsCount: 0, OpenIssuesCount: 0, HasLoadedCounts: true},
			{Name: "github", CurrentBranch: "main", Status: git.StatusPending, OpenPRsCount: 0, OpenIssuesCount: 1, HasLoadedCounts: true},
			{Name: "gnrl", CurrentBranch: "master", Status: git.StatusPending, OpenPRsCount: 0, OpenIssuesCount: 0, HasLoadedCounts: true},
			{Name: "homebrew-tap", CurrentBranch: "", Status: git.StatusPending, OpenPRsCount: 0, OpenIssuesCount: 0, HasLoadedCounts: true},
			{Name: "kayo-watch", CurrentBranch: "main", Status: git.StatusPending, OpenPRsCount: 0, OpenIssuesCount: 0, HasLoadedCounts: true},
			{Name: "kumon-automation", CurrentBranch: "main", Status: git.StatusPending, OpenPRsCount: 0, OpenIssuesCount: 4, HasLoadedCounts: true},
			{Name: "mecha-madhu", CurrentBranch: "master", Status: git.StatusPending, OpenPRsCount: 0, OpenIssuesCount: 3, HasLoadedCounts: true},
		}

		view := m.View()
		lines := strings.Split(view, "\n")

		if len(lines) > m.Height {
			t.Errorf("Width %d: line count %d exceeds height %d", w, len(lines), m.Height)
		}

		for i, line := range lines {
			lineWidth := lipgloss.Width(line)
			if lineWidth > w {
				t.Errorf("Width %d Line %d: rendered width %d exceeds window width %d: %q", w, i, lineWidth, w, line)
			}
			trimmed := strings.TrimSpace(line)
			if trimmed == "7" || trimmed == "12" || trimmed == "4" || trimmed == "1" || trimmed == "3" {
				t.Errorf("Width %d Line %d: detected orphan wrapped number %q in view:\n%s", w, i, trimmed, view)
			}
		}

		// Verify row 0 contains both the branch and the issue count 7 on the same line
		foundDotfiles := false
		for _, line := range lines {
			if strings.Contains(line, ".dotf") {
				foundDotfiles = true
				if !strings.Contains(line, "7") {
					t.Errorf("Width %d: .dotfiles row missing issue count 7 on the same line: %q", w, line)
				}
			}
		}
		if !foundDotfiles {
			t.Errorf("Width %d: .dotfiles row not found in view", w)
		}
	}
}

func TestInteractiveLayoutTransitions(t *testing.T) {
	m := newTestModel("/Users/seankoji/repos", "seankoji-com")
	m.Width = 100
	m.Height = 30
	m.IsOrgSyncing = false
	m.Repos = []*git.RepoItem{
		{Name: ".dotfiles", CurrentBranch: "fix/allow-zen-cleanup", Status: git.StatusPending, OpenPRsCount: 0, OpenIssuesCount: 7, HasLoadedCounts: true},
		{Name: "carey-finance", CurrentBranch: "master", Status: git.StatusPending, OpenPRsCount: 4, OpenIssuesCount: 12, HasLoadedCounts: true},
	}
	m.Runners = []*jobs.RunnerItem{
		{ID: "r1", Name: "alpha-runner", Status: jobs.RunnerRunning, Tags: []string{"self-hosted"}},
	}
	m.JobQueue = []*jobs.JobItem{
		{ID: "#10", Name: "freshen / test", Status: jobs.JobRunning, RunnerName: "alpha-runner"},
	}

	keys := []string{"j", "j", "w", "w", "w", "tab", "shift+tab", "1", "2", "3", "4", "right", "left", "k"}
	for _, k := range keys {
		newM, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		m = newM.(Model)

		view := m.View()
		lines := strings.Split(view, "\n")
		if len(lines) > m.Height {
			t.Errorf("After key %q: total lines %d exceeds terminal height %d", k, len(lines), m.Height)
		}
		for i, line := range lines {
			if lipgloss.Width(line) > m.Width {
				t.Errorf("After key %q Line %d: width %d exceeds %d", k, i, lipgloss.Width(line), m.Width)
			}
		}
	}

	// Window resize event
	newM, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = newM.(Model)
	view := m.View()
	lines := strings.Split(view, "\n")
	if len(lines) > m.Height {
		t.Errorf("After resize: total lines %d exceeds terminal height %d", len(lines), m.Height)
	}
	for i, line := range lines {
		if lipgloss.Width(line) > m.Width {
			t.Errorf("After resize Line %d: width %d exceeds %d", i, lipgloss.Width(line), m.Width)
		}
	}
}
