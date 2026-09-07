package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/seankoji-com/freshen/pkg/git"
	"github.com/seankoji-com/freshen/pkg/jobs"
)

func newTestModel(targetDir, targetOrg string) Model {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	return NewModel(targetDir, targetOrg, 4, ctx, cancel, &wg)
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

func TestApplyRepoSnapshotUnknownRepo(t *testing.T) {
	m := newTestModel("/tmp", "acme")
	m.Repos = []*git.RepoItem{{Name: "alpha", Status: git.StatusPending}}

	m.applyRepoSnapshot(&git.RepoItem{Name: "ghost", Status: git.StatusUpdated})
	m.applyRepoSnapshot(nil)

	if m.Repos[0].Status != git.StatusPending {
		t.Errorf("unknown snapshot altered the model: %s", m.Repos[0].Status)
	}
}

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

	t.Run("partial queue applies without eagerly fetching logs", func(t *testing.T) {
		m := newTestModel("/tmp/test", "test-org")
		m.ActiveFocus = FocusJobs
		partial := []*jobs.JobItem{
			{ID: "run-1", Repo: "repo1", Name: "repo1 / ci", Status: jobs.JobRunning, RunID: 1, IsRunHeader: true},
			{ID: "#1", Repo: "repo1", Name: "repo1 / ci / build", Status: jobs.JobRunning, RunID: 1},
		}
		// Row 0 is the synthetic initiator header, row 1 the synthetic run
		// header (both jobs share RunID 1), row 2 is the first actual job row.
		cmd := m.handleLoadedJobQueueMsg(loadedJobQueueMsg{queue: partial, err: errors.New("timeout")})

		if !m.JobQueueFetchFailed {
			t.Errorf("expected JobQueueFetchFailed true on error")
		}
		if len(m.JobQueue) == 0 {
			t.Errorf("expected partial queue data to still be applied to model state")
		}
		if cmd != nil {
			t.Error("overview must not fetch logs")
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
	updated.ActiveFocus = FocusRunners
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
