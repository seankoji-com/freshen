package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/seankoji-com/freshen/pkg/git"
	"github.com/seankoji-com/freshen/pkg/jobs"
)

type terminalDemo struct{ Model }

func (m terminalDemo) Init() tea.Cmd { return m.Spinner.Tick }
func (m terminalDemo) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Sync commands start workers while being constructed, so intercept actions
	// before Update, not merely by dropping its returned command.
	if k, ok := msg.(tea.KeyMsg); ok && !m.Filtering {
		block := strings.Contains("sboyc", k.String()) && len(k.String()) == 1
		if k.String() == "enter" && m.PendingAction != "" {
			block = true
			m.PendingAction = ""
		}
		if k.String() == "enter" && m.MenuOpen {
			actions := m.menuActions()
			if len(actions) > 0 && !actions[m.MenuIndex].confirm {
				block = true
				m.MenuOpen = false
			}
		}
		if block {
			m.setToast("Demo: external operation disabled", 1)
			return m, nil
		}
	}
	// Quit only through bindings the production handler honours right now, so
	// the demo cannot mask a hint that advertises a suspended key.
	keys := m.keys()
	next, cmd := m.Model.Update(msg)
	updated := next.(Model)
	settleDemoRequests(&updated)
	next = updated
	if k, ok := msg.(tea.KeyMsg); ok && (key.Matches(k, keys.ForceQuit) || key.Matches(k, keys.Quit)) {
		return next, tea.Quit
	}
	if _, ok := msg.(spinner.TickMsg); ok {
		return terminalDemo{next.(Model)}, cmd
	}
	return terminalDemo{next.(Model)}, nil
}

// settleDemoRequests completes, with the seeded data, every request the demo
// dropped: it runs no commands, so nothing else would clear their loading flags.
func settleDemoRequests(m *Model) {
	now := time.Now()
	m.LogLoading = ""
	m.RunLoading = false
	m.RepoDetailLoading = ""
	m.RepoDetailQueued = ""
	if m.IsRunnersLoading {
		m.IsRunnersLoading = false
		m.RunnerFetchFailed = false
		m.LastRunnersRefresh = now
	}
	if m.IsJobQueueLoading {
		m.IsJobQueueLoading = false
		m.LastActionsRefresh = now
	}
	if m.IsOrgSyncing {
		m.IsOrgSyncing = false
		m.LastOrgRefresh = now
	}
	for _, r := range m.Repos {
		if r.IsLoadingIssues {
			r.IsLoadingIssues, r.HasLoadedIssues = false, true
		}
		if r.IsLoadingPRs {
			r.IsLoadingPRs, r.HasLoadedPRs = false, true
		}
	}
	m.updateViewport()
}

func TestTerminalDemo(t *testing.T) {
	if os.Getenv("FRESHEN_DEMO") != "1" {
		t.Skip("manual terminal fixture")
	}
	m := demoFixtureModel()
	if _, err := tea.NewProgram(terminalDemo{m}, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run(); err != nil {
		t.Fatal(err)
	}
}

// demoFixtureModel widens the smoke fixture so the demo exercises realistic
// list lengths and populated repository tabs. It is demo-only: unit tests keep
// the small deterministic fixture.
func demoFixtureModel() Model {
	m := smokeFixtureModel()
	now := time.Now()
	m.Repos = append(m.Repos,
		&git.RepoItem{Name: "gamma", GHRepoName: "gamma", Path: "/tmp/freshen-fixture/gamma", CurrentBranch: "docs/install", DefaultBranch: "main", Status: git.StatusUpToDate, StatusMsg: "Up to date", HasLoadedCounts: true, OpenPRsCount: 1, OpenIssuesCount: 0},
		&git.RepoItem{Name: "delta", GHRepoName: "delta", Path: "/tmp/freshen-fixture/delta", CurrentBranch: "main", DefaultBranch: "main", Status: git.StatusUpdated, StatusMsg: "Updated", HasUnstagedChanges: true, HasLoadedCounts: true},
		&git.RepoItem{Name: "epsilon", GHRepoName: "epsilon", Path: "/tmp/freshen-fixture/epsilon", CurrentBranch: "main", DefaultBranch: "main", Status: git.StatusArchived, StatusMsg: "Archived", IsArchived: true, HasLoadedCounts: true},
		&git.RepoItem{Name: "zeta", GHRepoName: "zeta", Path: "/tmp/freshen-fixture/zeta", CurrentBranch: "release/2.4", DefaultBranch: "main", Status: git.StatusError, StatusMsg: "Pull failed: diverged", HasLoadedCounts: true, OpenIssuesCount: 2},
	)
	m.Repos[0].IssuesList = []git.IssueItem{
		{Number: 118, Title: "Overview drops review counts on narrow terminals", URL: "https://github.com/fixture-org/alpha/issues/118"},
		{Number: 112, Title: "Runner poll keeps running on screens without runners", URL: "https://github.com/fixture-org/alpha/issues/112"},
		{Number: 104, Title: "Document the alias mapping", URL: "https://github.com/fixture-org/alpha/issues/104"},
		{Number: 97, Title: "Log tail should say when GitHub withholds logs", URL: "https://github.com/fixture-org/alpha/issues/97"},
	}
	m.Repos[0].PRsList = []git.PRItem{
		{Number: 42, Title: "Build the new screens", HeadRefName: "feat/screens", URL: "https://github.com/fixture-org/alpha/pull/42"},
		{Number: 39, Title: "Keep the previous log tail on failure", HeadRefName: "fix/log-tail", URL: "https://github.com/fixture-org/alpha/pull/39"},
	}
	m.Repos[0].HasLoadedIssues, m.Repos[0].HasLoadedPRs = true, true
	m.Repos[2].PRsList = []git.PRItem{{Number: 7, Title: "Clarify the docs install path", HeadRefName: "docs/install", URL: "https://github.com/fixture-org/gamma/pull/7"}}
	m.Repos[2].HasLoadedPRs = true
	m.Repos[5].IssuesList = []git.IssueItem{
		{Number: 33, Title: "Release branch diverged from main", URL: "https://github.com/fixture-org/zeta/issues/33"},
		{Number: 31, Title: "Document the 2.4 rollback", URL: "https://github.com/fixture-org/zeta/issues/31"},
	}
	m.Repos[5].HasLoadedIssues = true

	passed := &jobs.RunItem{ID: 99, Number: 41, Attempt: 1, Repo: "alpha", Workflow: "CI", Title: "Refresh the log fixture", Branch: "main", Event: "push", Status: jobs.JobPassed, JobsKnown: true,
		CreatedAt: now.Add(-26 * time.Minute), StartedAt: now.Add(-25 * time.Minute), CompletedAt: now.Add(-23 * time.Minute)}
	failed := &jobs.RunItem{ID: 98, Number: 40, Attempt: 1, Repo: "beta", Workflow: "Release", Title: "Publish beta artefacts", Branch: "main", Event: "push", Status: jobs.JobFailed, JobsKnown: true,
		CreatedAt: now.Add(-14 * time.Minute), StartedAt: now.Add(-13 * time.Minute), CompletedAt: now.Add(-9 * time.Minute)}
	queued := &jobs.RunItem{ID: 101, Number: 43, Attempt: 1, Repo: "gamma", Workflow: "Docs", Title: "Deploy the docs site", Branch: "docs/install", Event: "pull_request", Status: jobs.JobWaiting, JobsKnown: true,
		CreatedAt: now.Add(-90 * time.Second)}
	m.JobQueue = append(m.JobQueue,
		&jobs.JobItem{ID: "run:99", RunID: 99, Repo: "alpha", Run: passed, IsRunHeader: true, Status: jobs.JobPassed},
		&jobs.JobItem{ID: "run:98", RunID: 98, Repo: "beta", Run: failed, IsRunHeader: true, Status: jobs.JobFailed},
		&jobs.JobItem{ID: "run:101", RunID: 101, Repo: "gamma", Run: queued, IsRunHeader: true, Status: jobs.JobWaiting},
		&jobs.JobItem{ID: "#41", GHJobID: 41, RunID: 98, Repo: "beta", Run: failed, Name: "beta / publish", Status: jobs.JobFailed, Duration: "3m 40s",
			Steps: []jobs.GHJobStep{{Number: 1, Name: "Checkout", Status: "completed", Conclusion: "success"}, {Number: 2, Name: "Upload assets", Status: "completed", Conclusion: "failure"}}},
	)
	m.Runners = append(m.Runners,
		&jobs.RunnerItem{ID: "runner-2", Name: "linux-builder", Platform: "Linux/X64", Status: jobs.RunnerIdle, Tags: []string{"self-hosted", "Linux", "X64"}},
		&jobs.RunnerItem{ID: "runner-3", Name: "windows-builder", Platform: "Windows/X64", Status: jobs.RunnerOffline, Tags: []string{"self-hosted", "Windows"}},
	)

	// A developer's real workspace has more than six repositories. Generate a
	// larger, varied snapshot so the demo exercises list and preview density.
	statuses := []git.RepoStatus{git.StatusUpToDate, git.StatusUpdated, git.StatusPending, git.StatusError, git.StatusArchived, git.StatusUpToDate, git.StatusUpdated, git.StatusUpToDate}
	names := []string{"eta", "theta", "iota", "kappa", "lambda", "mu", "nu", "xi", "omicron", "pi", "rho", "sigma"}
	for i, name := range names {
		st := statuses[i%len(statuses)]
		repo := &git.RepoItem{
			Name: name, GHRepoName: name, Path: "/tmp/freshen-fixture/" + name,
			CurrentBranch: "main", DefaultBranch: "main", Status: st,
			HasLoadedCounts: true, OpenPRsCount: i % 3, OpenIssuesCount: i % 4,
		}
		if st == git.StatusPending {
			repo.IsNew = true
		}
		if st == git.StatusError {
			repo.StatusMsg = "Pull failed: diverged"
		}
		if st == git.StatusArchived {
			repo.IsArchived = true
		}
		if i%5 == 0 {
			repo.HasUnstagedChanges = true
		}
		for p := 0; p < repo.OpenPRsCount; p++ {
			repo.PRsList = append(repo.PRsList, git.PRItem{Number: 100 + p, Title: fmt.Sprintf("%s follow-up %d", name, p+1), HeadRefName: "main", URL: fmt.Sprintf("https://github.com/fixture-org/%s/pull/%d", name, 100+p)})
		}
		for n := 0; n < repo.OpenIssuesCount; n++ {
			repo.IssuesList = append(repo.IssuesList, git.IssueItem{Number: 200 + n, Title: fmt.Sprintf("%s report %d", name, n+1), URL: fmt.Sprintf("https://github.com/fixture-org/%s/issues/%d", name, 200+n)})
		}
		repo.HasLoadedPRs, repo.HasLoadedIssues = true, true
		m.Repos = append(m.Repos, repo)
	}
	m.Repos[0].BranchDetails = git.BranchWorktreeDetails{
		LocalBranches:  []string{"* feat/screens", "main", "fix/log-tail"},
		RemoteBranches: []string{"origin/feat/screens", "origin/main"},
		Worktrees: []string{
			"/tmp/freshen-fixture/alpha  feat/screens",
			"/tmp/freshen-fixture/alpha-wt  fix/log-tail",
		},
		ChangedFiles: []string{"M pkg/tui/summary.go", "A pkg/tui/ui.go"},
	}

	runStatuses := []jobs.JobStatus{jobs.JobPassed, jobs.JobFailed, jobs.JobQueued, jobs.JobPassed, jobs.JobRunning, jobs.JobCancelled, jobs.JobPassed, jobs.JobFailed, jobs.JobPassed, jobs.JobSkipped}
	for i, st := range runStatuses {
		id := 260 - i
		run := &jobs.RunItem{ID: int64(id), Number: 70 - i, Attempt: 1, Repo: names[i%len(names)], Workflow: "CI", Title: "Routine check " + fmt.Sprint(i+1),
			Branch: "main", Event: "push", Status: st, JobsKnown: true,
			CreatedAt: now.Add(-time.Duration(i+1) * 12 * time.Minute)}
		if st != jobs.JobQueued && st != jobs.JobWaiting {
			run.StartedAt = run.CreatedAt.Add(time.Minute)
			if terminalStatus(st) {
				run.CompletedAt = run.StartedAt.Add(time.Duration(2+i%4) * time.Minute)
			}
		}
		m.JobQueue = append(m.JobQueue, &jobs.JobItem{ID: fmt.Sprintf("run:%d", id), RunID: int64(id), Repo: run.Repo, Run: run, IsRunHeader: true, Status: st})
	}
	for i, st := range []jobs.RunnerStatus{jobs.RunnerIdle, jobs.RunnerIdle, jobs.RunnerOffline, jobs.RunnerMaintenance, jobs.RunnerIdle, jobs.RunnerRunning} {
		m.Runners = append(m.Runners, &jobs.RunnerItem{
			ID: fmt.Sprintf("runner-%d", i+4), Name: fmt.Sprintf("builder-%02d", i+4),
			Platform: []string{"Linux/X64", "macOS/ARM64", "Windows/X64"}[i%3],
			Status:   st, Tags: []string{"self-hosted", fmt.Sprintf("pool-%d", i%2)},
		})
	}
	m.SelectedTagIndex = 0

	m.LastOrgRefresh = now.Add(-2 * time.Minute)
	m.LastActionsRefresh = now.Add(-8 * time.Second)
	m.LastRunnersRefresh = now.Add(-4 * time.Second)
	m.handleWindowSizeMsg(tea.WindowSizeMsg{Width: 100, Height: 28})
	return m
}
