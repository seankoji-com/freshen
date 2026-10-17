package tui

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/seankoji-com/freshen/pkg/git"
	"github.com/seankoji-com/freshen/pkg/proc"
)

// A completed branch switch whose re-read of branch state hangs still
// delivers its result at the read deadline: BusyAction clears, the switch is
// reported and kept, and the hung sections keep their earlier entries.
func TestBlockedPostActionReadDeliversResult(t *testing.T) {
	originalSwitch, originalCurrent, originalRead, originalTimeout := switchBranch, getCurrentBranch, getRepoBranchDetails, repoDetailTimeout
	repoDetailTimeout = 100 * time.Millisecond
	switchBranch = func(_ context.Context, item *git.RepoItem, branch string) error {
		item.CurrentBranch = branch
		item.Logs = append(item.Logs, "Switched branch to '"+branch+"'.")
		return nil
	}
	getCurrentBranch = func(ctx context.Context, _ string) string {
		<-ctx.Done() // git hangs until the read deadline kills it
		return "HEAD"
	}
	getRepoBranchDetails = func(ctx context.Context, path, defaultBranch string) git.BranchWorktreeDetails {
		<-ctx.Done()
		return git.GetRepoBranchDetails(ctx, path, defaultBranch)
	}
	t.Cleanup(func() {
		switchBranch, getCurrentBranch, getRepoBranchDetails, repoDetailTimeout = originalSwitch, originalCurrent, originalRead, originalTimeout
	})

	m := sized(smokeFixtureModel(), 120, 50)
	m.Repos[0].BranchDetails = git.BranchWorktreeDetails{LocalBranches: []string{"* feat/screens", "main"}}
	m.Repos[0].OriginalBranch = "feat/screens" // on its own branch, so switch goes to main
	m.ActionTarget = m.Repos[0].Clone()
	cmd := m.executeAction("switch")
	if m.BusyAction == "" {
		t.Fatal("switch did not mark the repository busy")
	}
	start := time.Now()
	msgs := runCmd(cmd)
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("hung re-read held the action result for %s", waited)
	}
	for _, msg := range msgs {
		next, _ := m.Update(msg)
		m = next.(Model)
	}
	alpha := m.Repos[0]
	if m.BusyAction != "" || !strings.Contains(m.ToastMsg, "Switch branch complete") {
		t.Fatalf("result not delivered: busy=%q toast=%q", m.BusyAction, m.ToastMsg)
	}
	if alpha.CurrentBranch != "main" {
		t.Fatalf("failed re-read discarded the switch: current=%q", alpha.CurrentBranch)
	}
	if alpha.BranchDetails.BranchesErr == "" || !slices.Contains(alpha.BranchDetails.LocalBranches, "* feat/screens") {
		t.Fatalf("hung read not reported with earlier entries: %+v", alpha.BranchDetails)
	}
	if !slices.ContainsFunc(alpha.Logs, func(l string) bool { return strings.Contains(l, "Could not re-read the checked-out branch") }) {
		t.Fatalf("failed branch re-read not logged: %v", alpha.Logs)
	}
}

// A detail read that started before a prune or sync snapshot answers after
// it. The older read must not restore deleted branches or report a stale
// working tree; a read started after the snapshot still applies.
func TestStaleBranchReadCannotOverwriteNewerSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(m *Model, snapshot *git.RepoItem)
	}{
		{"prune", func(m *Model, snapshot *git.RepoItem) {
			next, _ := m.Update(actionResultMsg{id: "prune", label: "Prune", path: snapshot.Path, repo: snapshot})
			*m = next.(Model)
		}},
		{"sync", func(m *Model, snapshot *git.RepoItem) {
			next, _ := m.Update(repoSyncMsg{repo: snapshot, snapshots: make(chan *git.RepoItem), bulk: true})
			*m = next.(Model)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := getRepoBranchDetails
			getRepoBranchDetails = func(context.Context, string, string) git.BranchWorktreeDetails {
				return git.BranchWorktreeDetails{
					LocalBranches: []string{"* feat/screens", "feat/old"},
					ChangedFiles:  []string{"M README.md"},
				}
			}
			t.Cleanup(func() { getRepoBranchDetails = original })

			m := sized(smokeFixtureModel(), 120, 50)
			press(&m, "1")
			oldRead := press(&m, "enter")
			press(&m, "]")
			if m.RepoDetailLoading != m.Repos[0].Path {
				t.Fatalf("detail read not started: %q", m.RepoDetailLoading)
			}
			stale := repoDetailsResponse(t, oldRead) // read before the mutation

			snapshot := m.Repos[0].Clone()
			snapshot.Status = git.StatusUpdated
			snapshot.BranchDetails = git.BranchWorktreeDetails{LocalBranches: []string{"* feat/screens"}}
			tc.apply(&m, snapshot)

			next, _ := m.Update(stale)
			m = next.(Model)
			if m.RepoDetailLoading != "" {
				t.Fatalf("discarded read left loading set: %q", m.RepoDetailLoading)
			}
			got := m.Repos[0].BranchDetails
			if slices.Contains(got.LocalBranches, "feat/old") || len(got.ChangedFiles) != 0 {
				t.Fatalf("stale read overwrote the newer snapshot: %+v", got)
			}
			view := stripped(m.Viewport.View())
			if strings.Contains(view, "feat/old") || !strings.Contains(view, "working tree is clean") {
				t.Fatalf("detail shows the stale read:\n%s", view)
			}

			// A read started after the snapshot is current and applies.
			fresh := press(&m, "r")
			next, _ = m.Update(repoDetailsResponse(t, fresh))
			m = next.(Model)
			if !slices.Contains(m.Repos[0].BranchDetails.LocalBranches, "feat/old") {
				t.Fatalf("current read was discarded: %+v", m.Repos[0].BranchDetails)
			}
		})
	}
}

// refreshRecords is what an organization refresh reports for the fixture:
// fresh records with no branch details, alpha on branch.
func refreshRecords(m Model, branch string, generation uint64) orgSyncedMsg {
	var repos []*git.RepoItem
	for _, r := range m.Repos {
		repos = append(repos, &git.RepoItem{Name: r.Name, GHRepoName: r.GHRepoName, Path: r.Path, CurrentBranch: r.CurrentBranch, DefaultBranch: r.DefaultBranch, Status: git.StatusPending, Logs: []string{}})
	}
	repos[0].CurrentBranch = branch
	return orgSyncedMsg{repos: repos, generation: generation}
}

// A refresh that finds the open repository on another branch never shows its
// unread details as empty or clean: it reads them again, whether or not a
// read was already running, and the new read applies.
func TestRefreshThatMovesTheCheckoutRereadsOpenDetails(t *testing.T) {
	for _, inFlight := range []bool{true, false} {
		t.Run(map[bool]string{true: "read in flight", false: "no read in flight"}[inFlight], func(t *testing.T) {
			reads := 0
			original := getRepoBranchDetails
			getRepoBranchDetails = func(context.Context, string, string) git.BranchWorktreeDetails {
				reads++
				return git.BranchWorktreeDetails{LocalBranches: []string{"* main", "feat/screens"}, Worktrees: []string{"/tmp/freshen-fixture/alpha  abc123 [main]"}}
			}
			t.Cleanup(func() { getRepoBranchDetails = original })

			m := sized(smokeFixtureModel(), 120, 50)
			press(&m, "1")
			oldRead := press(&m, "enter")
			press(&m, "]")
			if !inFlight {
				next, _ := m.Update(repoDetailsResponse(t, oldRead))
				m = next.(Model)
			}

			// update, not Update: the branch-changed toast's 6s expiry tick
			// would otherwise run with the re-read command.
			next, refreshCmd := m.update(refreshRecords(m, "main", 0))
			m = next.(Model)
			if !m.Repos[0].BranchDetails.Unread {
				t.Fatalf("details not marked unread: %+v", m.Repos[0].BranchDetails)
			}
			view := stripped(m.Viewport.View())
			if strings.Contains(view, "working tree is clean") || strings.Contains(view, "none found") {
				t.Fatalf("unread details shown as read:\n%s", view)
			}

			var reread repoDetailsLoadedMsg
			if inFlight {
				var restart tea.Cmd
				next, restart = m.Update(repoDetailsResponse(t, oldRead)) // discarded, restarts
				m = next.(Model)
				reread = repoDetailsResponse(t, restart)
			} else {
				reread = repoDetailsResponse(t, refreshCmd)
			}
			if m.RepoDetailLoading != m.Repos[0].Path {
				t.Fatalf("no new read is running: %q", m.RepoDetailLoading)
			}
			if view := stripped(m.Viewport.View()); !strings.Contains(view, "Loading branches") {
				t.Fatalf("re-read not shown as loading:\n%s", view)
			}
			next, _ = m.Update(reread)
			m = next.(Model)
			got := m.Repos[0].BranchDetails
			if got.Unread || !slices.Contains(got.LocalBranches, "* main") {
				t.Fatalf("re-read not applied: %+v", got)
			}
			if reads != 2 { // the first read, then exactly one re-read
				t.Fatalf("%d branch reads, want 2", reads)
			}
		})
	}
}

// Unread details that are not being read say so, with a retry hint.
func TestUnreadBranchDetailsSaySo(t *testing.T) {
	m := sized(smokeFixtureModel(), 120, 50)
	m.Repos[0].BranchDetails = git.BranchWorktreeDetails{Unread: true}
	m.Repos[0].CurrentBranch = "main"
	m.SelectedIndex = 0
	m.Detail = true
	m.ActiveTab = TabBranches
	m.updateViewport()
	view := stripped(m.Viewport.View())
	if !strings.Contains(view, "Not read since the checkout moved to main") || !strings.Contains(view, "Press r") || strings.Contains(view, "clean") {
		t.Fatalf("unread details not stated:\n%s", view)
	}
	// The message wraps inside the frame at every size.
	for _, size := range [][2]int{{80, 24}, {200, 50}, {30, 10}} {
		lines := strings.Split(renderAt(m, size[0], size[1]), "\n")
		if len(lines) != size[1] {
			t.Fatalf("%v: height %d", size, len(lines))
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > size[0] {
				t.Fatalf("%v: overflow %q", size, stripped(line))
			}
		}
	}
}

// A refresh that read a repository before a switch or sync snapshot landed
// answers after it. The repository keeps the snapshot's branch, status and
// details, raises no "branch changed" toast, and the next action targets the
// snapshot's branch. A refresh started after the snapshot still applies.
func TestLateRefreshCannotOverwriteNewerSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(m *Model, snapshot *git.RepoItem)
	}{
		{"switch", func(m *Model, snapshot *git.RepoItem) {
			next, _ := m.Update(actionResultMsg{id: "switch", label: "Switch branch", path: snapshot.Path, repo: snapshot})
			*m = next.(Model)
		}},
		{"sync", func(m *Model, snapshot *git.RepoItem) {
			next, _ := m.Update(repoSyncMsg{repo: snapshot, snapshots: make(chan *git.RepoItem), bulk: true})
			*m = next.(Model)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := sized(smokeFixtureModel(), 120, 50)
			m.Repos[0].OriginalBranch = "feat/screens"
			if m.startOrgRefresh(false, false) == nil {
				t.Fatal("refresh did not start")
			}
			late := refreshRecords(m, "feat/screens", m.OrgRefreshGeneration) // read before the switch

			snapshot := m.Repos[0].Clone()
			snapshot.CurrentBranch = "main"
			snapshot.Status = git.StatusSwitchedDefault
			snapshot.StatusMsg = "Switched"
			snapshot.BranchDetails = git.BranchWorktreeDetails{LocalBranches: []string{"* main", "feat/screens"}}
			tc.apply(&m, snapshot)
			m.clearToast()

			next, _ := m.Update(late)
			m = next.(Model)
			alpha := m.Repos[0]
			if alpha.CurrentBranch != "main" || alpha.Status != git.StatusSwitchedDefault || !slices.Contains(alpha.BranchDetails.LocalBranches, "* main") {
				t.Fatalf("late refresh overwrote the snapshot: branch=%q status=%s details=%+v", alpha.CurrentBranch, alpha.Status, alpha.BranchDetails)
			}
			if strings.Contains(m.ToastMsg, "Branch changed") {
				t.Fatalf("late refresh raised %q", m.ToastMsg)
			}
			m.SelectedIndex = 0
			m.captureActionTarget()
			if m.ActionTarget == nil || m.ActionTarget.CurrentBranch != "main" {
				t.Fatalf("action target uses the late refresh: %+v", m.ActionTarget)
			}

			// A refresh started after the snapshot is newer and applies.
			m.IsOrgSyncing = false
			m.startOrgRefresh(false, false)
			next, _ = m.Update(refreshRecords(m, "feat/other", m.OrgRefreshGeneration))
			m = next.(Model)
			if m.Repos[0].CurrentBranch != "feat/other" {
				t.Fatalf("newer refresh was discarded: %q", m.Repos[0].CurrentBranch)
			}
		})
	}
}

// A switch, push or prune whose git or gh never answers is stopped at
// repoActionTimeout: the result arrives, BusyAction clears, the failure says
// it was stopped, and steps that finished before it stay logged.
func TestHungRepositoryActionIsStopped(t *testing.T) {
	originals := []any{switchBranch, publishRepo, pruneRepo, getCurrentBranch, getRepoBranchDetails, repoActionTimeout}
	t.Cleanup(func() {
		switchBranch = originals[0].(func(context.Context, *git.RepoItem, string) error)
		publishRepo = originals[1].(func(context.Context, *git.RepoItem) error)
		pruneRepo = originals[2].(func(context.Context, *git.RepoItem) (git.PruneResult, error))
		getCurrentBranch = originals[3].(func(context.Context, string) string)
		getRepoBranchDetails = originals[4].(func(context.Context, string, string) git.BranchWorktreeDetails)
		repoActionTimeout = originals[5].(time.Duration)
	})
	repoActionTimeout = 100 * time.Millisecond
	switchBranch = func(ctx context.Context, _ *git.RepoItem, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}
	publishRepo = func(ctx context.Context, item *git.RepoItem) error {
		item.Logs = append(item.Logs, "Pushed branch 'feat/screens' to origin.")
		<-ctx.Done() // gh pr create hangs
		return ctx.Err()
	}
	pruneRepo = func(ctx context.Context, _ *git.RepoItem) (git.PruneResult, error) {
		<-ctx.Done()
		return git.PruneResult{DeletedBranches: []string{"feat/old"}}, ctx.Err()
	}
	getCurrentBranch = func(context.Context, string) string { return "feat/screens" }
	getRepoBranchDetails = func(context.Context, string, string) git.BranchWorktreeDetails {
		return git.BranchWorktreeDetails{LocalBranches: []string{"* feat/screens"}}
	}

	for id, partial := range map[string]string{"switch": "", "push": "Pushed branch 'feat/screens' to origin.", "prune": "Deleted local branch: feat/old"} {
		t.Run(id, func(t *testing.T) {
			m := sized(smokeFixtureModel(), 120, 50)
			m.ActionTarget = m.Repos[0].Clone()
			cmd := m.executeAction(id)
			if m.BusyAction == "" {
				t.Fatal("action did not mark the repository busy")
			}
			delivered := make(chan []tea.Msg, 1)
			go func() { delivered <- runCmd(cmd) }()
			var msgs []tea.Msg
			select {
			case msgs = <-delivered:
			case <-time.After(5 * time.Second):
				t.Fatalf("hung %s held its result past the action deadline", id)
			}
			for _, msg := range msgs {
				next, _ := m.Update(msg)
				m = next.(Model)
			}
			if m.BusyAction != "" || !strings.Contains(m.ToastMsg, "failed") || !strings.Contains(m.ToastMsg, "stopped after") {
				t.Fatalf("stopped action not reported: busy=%q toast=%q", m.BusyAction, m.ToastMsg)
			}
			logs := m.Repos[0].Logs
			if !slices.ContainsFunc(logs, func(l string) bool { return strings.Contains(l, "was stopped after") }) {
				t.Fatalf("stop not logged: %v", logs)
			}
			if partial != "" && !slices.Contains(logs, partial) {
				t.Fatalf("finished step %q lost: %v", partial, logs)
			}
		})
	}
}

// A switch whose git checkout the kernel never lets exit, which no kill can
// end, still delivers its result soon after the deadline: BusyAction clears
// and the repository log says the checkout may still be running, instead of
// the action holding the repository until quit.
func TestUnreapableSwitchFreesTheRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init"},
		{"branch", "feature"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	originalTimeout, originalDelay, originalReap := repoActionTimeout, proc.WaitDelay, proc.ReapTimeout
	repoActionTimeout, proc.WaitDelay, proc.ReapTimeout = 300*time.Millisecond, 200*time.Millisecond, 200*time.Millisecond
	release := make(chan struct{})
	restore := proc.BlockReaping(func(args []string) bool { return slices.Contains(args, "checkout") }, release, 100*time.Millisecond)
	t.Cleanup(func() {
		close(release)
		// Killing the checkout read proc.ReapTimeout on exec's cancellation
		// goroutine, and the switch returns without waiting for it. git
		// frees the path only once the checkout is reaped, after that
		// goroutine has finished, so wait for that before restoring the
		// tunables.
		deadline := time.Now().Add(5 * time.Second)
		for strings.Contains(git.GetRepoBranchDetails(context.Background(), dir, "main").BranchesErr, "has not finished") {
			if time.Now().After(deadline) {
				t.Error("the repository stayed held after the checkout was reaped")
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		restore()
		repoActionTimeout, proc.WaitDelay, proc.ReapTimeout = originalTimeout, originalDelay, originalReap
	})

	m := sized(smokeFixtureModel(), 120, 50)
	m.Repos[0].Path = dir
	m.Repos[0].CurrentBranch, m.Repos[0].OriginalBranch, m.Repos[0].DefaultBranch = "main", "feature", "main"
	m.ActionTarget = m.Repos[0].Clone()
	cmd := m.executeAction("switch")
	delivered := make(chan []tea.Msg, 1)
	go func() { delivered <- runCmd(cmd) }()
	var msgs []tea.Msg
	select {
	case msgs = <-delivered:
	case <-time.After(10 * time.Second):
		t.Fatal("the switch held its result while the checkout could not be reaped")
	}
	for _, msg := range msgs {
		next, _ := m.Update(msg)
		m = next.(Model)
	}
	if m.BusyAction != "" || !strings.Contains(m.ToastMsg, "failed") {
		t.Fatalf("switch not reported: busy=%q toast=%q", m.BusyAction, m.ToastMsg)
	}
	if !slices.ContainsFunc(m.Repos[0].Logs, func(l string) bool { return strings.Contains(l, "may still be running") }) {
		t.Fatalf("held checkout not logged: %v", m.Repos[0].Logs)
	}
}

// A clone whose branches could not be read tells the user to press r. In the
// repository detail, r reads them again: a read that still fails says why and
// keeps the clone's "Branch unread" warning, while a later read that succeeds
// resolves the header, clears the warning and reads branches against the
// default it found, and a branch a refresh filled during the read keeps that
// value.
func TestDetailRefreshRereadsUnresolvedBranches(t *testing.T) {
	originalCheckout, originalRead := readLocalCheckout, getRepoBranchDetails
	readable := false
	readLocalCheckout = func(context.Context, string) (git.LocalCheckout, error) {
		if !readable {
			return git.LocalCheckout{IsRepo: true, CurrentBranch: "feat/clone"}, errors.New("could not read the default branch (timed out)")
		}
		return git.LocalCheckout{IsRepo: true, CurrentBranch: "feat/clone", DefaultBranch: "main"}, nil
	}
	var defaults []string
	getRepoBranchDetails = func(_ context.Context, _, defaultBranch string) git.BranchWorktreeDetails {
		defaults = append(defaults, defaultBranch)
		return git.BranchWorktreeDetails{LocalBranches: []string{"* feat/clone", "main"}}
	}
	t.Cleanup(func() { readLocalCheckout, getRepoBranchDetails = originalCheckout, originalRead })
	retryLogs := func(r *git.RepoItem) int {
		return len(slices.DeleteFunc(slices.Clone(r.Logs), func(l string) bool { return !strings.Contains(l, "could not read the default branch (timed out)") }))
	}

	for _, filledDuringRead := range []bool{false, true} {
		t.Run(map[bool]string{false: "recovers", true: "keeps a newer value"}[filledDuringRead], func(t *testing.T) {
			readable, defaults = false, nil
			m := sized(smokeFixtureModel(), 120, 50)
			alpha := m.Repos[0]
			alpha.CurrentBranch, alpha.OriginalBranch, alpha.DefaultBranch = "", "", ""
			alpha.Status, alpha.StatusMsg = git.StatusCloned, "Branch unread"
			alpha.Logs = []string{"󰀪 Cloned, but could not read the checked-out branch (timed out) or the default branch (timed out); press r to refresh before acting on it."}
			press(&m, "1")
			next, _ := m.Update(repoDetailsResponse(t, press(&m, "enter")))
			m = next.(Model)
			for retry := 1; retry <= 2; retry++ {
				if alpha.CurrentBranch != "feat/clone" || alpha.DefaultBranch != "" {
					t.Fatalf("read %d: current=%q default=%q, want the branch it read and the default unresolved", retry, alpha.CurrentBranch, alpha.DefaultBranch)
				}
				if alpha.StatusMsg != "Branch unread" || retryLogs(alpha) != retry || !strings.Contains(m.ToastMsg, "could not read the default branch (timed out)") {
					t.Fatalf("read %d failure not reported: status=%q toast=%q logs=%q", retry, alpha.StatusMsg, m.ToastMsg, alpha.Logs)
				}
				list := m
				press(&list, "esc")
				if view := stripped(renderAt(list, 120, 50)); !strings.Contains(view, "Branch unread") {
					t.Fatalf("read %d: repository list dropped the warning:\n%s", retry, view)
				}
				if retry == 1 {
					next, _ = m.Update(repoDetailsResponse(t, press(&m, "r")))
					m = next.(Model)
				}
			}

			readable = true
			m.clearToast()
			read := press(&m, "r")
			if filledDuringRead {
				alpha.DefaultBranch = "trunk"
			}
			next, _ = m.Update(repoDetailsResponse(t, read))
			m = next.(Model)
			wantDefault := map[bool]string{false: "main", true: "trunk"}[filledDuringRead]
			if alpha.CurrentBranch != "feat/clone" || alpha.OriginalBranch != "feat/clone" || alpha.DefaultBranch != wantDefault {
				t.Fatalf("refresh did not resolve the branches: current=%q original=%q default=%q", alpha.CurrentBranch, alpha.OriginalBranch, alpha.DefaultBranch)
			}
			if !slices.Equal(defaults, []string{"", "", "main"}) {
				t.Fatalf("branch reads used defaults %q, want the unread one twice then the one just read", defaults)
			}
			if alpha.Status != git.StatusCloned || alpha.StatusMsg != "Cloned" || m.ToastMsg != "" || retryLogs(alpha) != 2 || !strings.Contains(alpha.Logs[0], "press r to refresh") {
				t.Fatalf("recovery left status=%v %q toast=%q logs=%q", alpha.Status, alpha.StatusMsg, m.ToastMsg, alpha.Logs)
			}
			if view := stripped(renderAt(m, 120, 50)); !strings.Contains(view, "branch feat/clone (default: "+wantDefault+")") || strings.Contains(view, "Branch unread") {
				t.Fatalf("header does not show the resolved branches without the warning:\n%s", view)
			}
			press(&m, "esc")
			if view := stripped(renderAt(m, 120, 50)); !strings.Contains(view, "alpha") || strings.Contains(view, "Branch unread") {
				t.Fatalf("repository list still shows the warning:\n%s", view)
			}
		})
	}
}
