package tui

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/seankoji-com/freshen/pkg/git"
)

// runCmd runs cmd and every command it batches to completion, with no
// deadline of its own, so a request can end on its production deadline.
func runCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var msgs []tea.Msg
		for _, c := range batch {
			msgs = append(msgs, runCmd(c)...)
		}
		return msgs
	}
	return []tea.Msg{msg}
}

func repoDetailsResponse(t *testing.T, cmd tea.Cmd) repoDetailsLoadedMsg {
	t.Helper()
	for _, msg := range runCmd(cmd) {
		if loaded, ok := msg.(repoDetailsLoadedMsg); ok {
			return loaded
		}
	}
	t.Fatal("command made no branch read")
	return repoDetailsLoadedMsg{}
}

// A branch read whose git command never answers ends at the request-wide
// deadline. Loading clears, the repository queued behind it starts, and the
// hung repository states the failure with a retry hint while keeping its
// earlier entries, labelled, instead of showing "none found" or "clean".
func TestBlockedBranchReadTimesOutAndHandsOff(t *testing.T) {
	originalRead, originalTimeout := getRepoBranchDetails, repoDetailTimeout
	repoDetailTimeout = 100 * time.Millisecond
	getRepoBranchDetails = func(ctx context.Context, path, defaultBranch string) git.BranchWorktreeDetails {
		if strings.HasSuffix(path, "/alpha") {
			<-ctx.Done() // git hangs until the request's deadline kills it
			return git.GetRepoBranchDetails(ctx, path, defaultBranch)
		}
		return git.BranchWorktreeDetails{LocalBranches: []string{"* main"}, Worktrees: []string{path + " 0123abc [main]"}}
	}
	t.Cleanup(func() { getRepoBranchDetails, repoDetailTimeout = originalRead, originalTimeout })

	m := sized(smokeFixtureModel(), 120, 50)
	m.Repos[0].BranchDetails = git.BranchWorktreeDetails{
		LocalBranches: []string{"* feat/screens", "main"},
		Worktrees:     []string{"/tmp/freshen-fixture/alpha 0123abc [feat/screens]"},
		ChangedFiles:  []string{"M README.md"},
	}
	press(&m, "1")
	readAlpha := press(&m, "enter")
	press(&m, "esc")
	press(&m, "down")
	press(&m, "enter")
	press(&m, "]")
	beta := m.Repos[1]
	if m.RepoDetailLoading != m.Repos[0].Path || m.RepoDetailQueued != beta.Path {
		t.Fatalf("beta not queued behind alpha: loading=%q queued=%q", m.RepoDetailLoading, m.RepoDetailQueued)
	}

	start := time.Now()
	alphaDone := repoDetailsResponse(t, readAlpha)
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("hung read took %s to end", waited)
	}
	next, readBeta := m.Update(alphaDone)
	m = next.(Model)
	if m.RepoDetailLoading != beta.Path || m.RepoDetailQueued != "" {
		t.Fatalf("timed-out alpha read did not hand off: loading=%q queued=%q", m.RepoDetailLoading, m.RepoDetailQueued)
	}
	next, _ = m.Update(repoDetailsResponse(t, readBeta))
	m = next.(Model)
	if m.RepoDetailLoading != "" {
		t.Fatalf("beta read left loading set: %q", m.RepoDetailLoading)
	}
	betaView := stripped(m.Viewport.View())
	if !strings.Contains(betaView, "* main") || strings.Contains(betaView, "unavailable") || strings.Contains(betaView, "Loading branches") {
		t.Fatalf("beta detail after handoff:\n%s", betaView)
	}

	// Alpha's detail as the user sees it on return, before any new read.
	m.SelectedIndex = 0
	m.updateViewport()
	view := stripped(m.Viewport.View())
	for _, want := range []string{
		"Some git reads failed. Press r to retry.",
		"Local branches  ! unavailable",
		"Could not read: git did not answer before the deadline",
		"From an earlier read (2):",
		"* feat/screens",
		"Changed files  ! unavailable",
		"M README.md",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("alpha detail missing %q:\n%s", want, view)
		}
	}
	for _, stale := range []string{"none found", "working tree is clean", "Loading branches"} {
		if strings.Contains(view, stale) {
			t.Fatalf("alpha detail shows %q after a failed read:\n%s", stale, view)
		}
	}

	// r, as the hint says, retries the read.
	if press(&m, "r") == nil || m.RepoDetailLoading != m.Repos[0].Path {
		t.Fatalf("r did not retry alpha's read: %q", m.RepoDetailLoading)
	}
}

// A failed read with nothing cached says so rather than reading as empty.
func TestFailedBranchReadIsNotShownAsEmpty(t *testing.T) {
	m := sized(smokeFixtureModel(), 120, 50)
	press(&m, "1")
	press(&m, "enter")
	press(&m, "]")
	failed := m.Repos[0].Clone()
	failed.BranchDetails = git.BranchWorktreeDetails{
		LocalBranches: []string{"* feat/screens"},
		Worktrees:     []string{"/tmp/freshen-fixture/alpha 0123abc [feat/screens]"},
		StatusErr:     "fatal: index file corrupt",
	}
	next, _ := m.Update(repoDetailsLoadedMsg{repo: failed})
	m = next.(Model)
	view := stripped(m.Viewport.View())
	for _, want := range []string{"Some git reads failed. Press r to retry.", "Local branches (1)", "Changed files  ! unavailable", "Could not read: fatal: index file corrupt"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "working tree is clean") || strings.Contains(view, "From an earlier read") {
		t.Fatalf("unreadable status shown as clean or as earlier data:\n%s", view)
	}

	// A later successful read clears the failure.
	ok := m.Repos[0].Clone()
	ok.BranchDetails = git.BranchWorktreeDetails{LocalBranches: []string{"* feat/screens"}}
	m.RepoDetailLoading = ok.Path
	next, _ = m.Update(repoDetailsLoadedMsg{repo: ok})
	m = next.(Model)
	view = stripped(m.Viewport.View())
	if strings.Contains(view, "failed") || !strings.Contains(view, "working tree is clean") {
		t.Fatalf("successful read kept the old failure:\n%s", view)
	}
}

// Opening another job while a log request is in flight cancels that request
// and loads the open job's log now; the left job's late answer is discarded.
func TestOpeningAnotherJobReplacesLogRequest(t *testing.T) {
	var calls atomic.Int32
	inFlight := make(chan context.Context, 1)
	original := fetchJobLogs
	fetchJobLogs = func(ctx context.Context, _, _ string, _, ghJobID int64, _ string, _ int) ([]string, int64, error) {
		calls.Add(1)
		if ghJobID == 2 { // the running job's request is slow
			inFlight <- ctx
			<-ctx.Done()
			return nil, 0, ctx.Err()
		}
		return []string{"build passed"}, ghJobID, nil
	}
	t.Cleanup(func() { fetchJobLogs = original })

	m := sized(smokeFixtureModel(), 80, 24)
	press(&m, "2")
	press(&m, "enter")
	press(&m, "down")
	openRunning := press(&m, "enter") // #2, running
	if m.LogLoading != "#2" {
		t.Fatalf("running job log request not started: %q", m.LogLoading)
	}
	runningDone := make(chan tea.Msg, 1)
	sendLogResponses(openRunning, runningDone)
	runningCtx := <-inFlight

	press(&m, "esc")
	press(&m, "up")
	openCompleted := press(&m, "enter") // #1, passed
	if m.OpenJobID != "#1" || m.LogLoading != "#1" {
		t.Fatalf("completed job's log request dropped: open=%q loading=%q", m.OpenJobID, m.LogLoading)
	}
	select {
	case <-runningCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the left job's request kept running")
	}
	for _, msg := range drain(openCompleted) {
		next, _ := m.Update(msg)
		m = next.(Model)
	}
	next, refetch := m.Update(<-runningDone)
	m = next.(Model)
	drain(refetch)

	if logs := m.openJob().Logs; len(logs) != 1 || logs[0] != "build passed" {
		t.Fatalf("completed job's log not loaded: %v", logs)
	}
	if m.LogLoading != "" || m.LogErrors["#2"] != "" || calls.Load() != 2 {
		t.Fatalf("late cancelled answer changed state: loading=%q err=%q calls=%d", m.LogLoading, m.LogErrors["#2"], calls.Load())
	}
	if view := stripped(m.jobDetailContent()); !strings.Contains(view, "build passed") || strings.Contains(view, "not loaded") || strings.Contains(view, "Loading logs") {
		t.Fatalf("completed job detail:\n%s", view)
	}
}
