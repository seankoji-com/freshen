package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/seankoji-com/freshen/pkg/git"
	"github.com/seankoji-com/freshen/pkg/jobs"
)

func TestFocusedRunViewportRendering(t *testing.T) {
	m := NewModel("/tmp/test", "test-org")
	m.Width = 120
	m.Height = 40
	m.ActiveFocus = FocusJobs
	m.SelectedJobIndex = 1
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
	m := NewModel("/tmp/test", "test-org")
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
	m := NewModel("/tmp/test", "test-org")
	m.ActiveFocus = FocusJobs
	m.SelectedJobIndex = 0
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

func TestEscUnfocusesRun(t *testing.T) {
	m := NewModel("/tmp/test", "test-org")
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
	m := NewModel("/tmp/test", "test-org")
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
	m := NewModel("/tmp/test", "test-org")
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
	m := NewModel("/tmp/test", "test-org")
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
	m := NewModel("/tmp/test", "test-org")
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
	m := NewModel("/tmp/test", "test-org")
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
	m := NewModel("/tmp/test", "test-org")
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
	m := NewModel("/tmp/test", "test-org")
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
	m := NewModel("/tmp/test", "test-org")
	m.Width = 120
	m.Height = 40
	m.ActiveFocus = FocusJobs
	m.SelectedJobIndex = 0

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
	m := NewModel("/tmp/test", "test-org")
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
