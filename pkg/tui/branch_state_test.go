package tui

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/seankoji-com/freshen/pkg/git"
)

// A completed branch switch whose re-read of branch state hangs still
// delivers its result at the read deadline: BusyAction clears, the switch is
// reported and kept, and the hung sections keep their earlier entries.
func TestBlockedPostActionReadDeliversResult(t *testing.T) {
	originalSwitch, originalCurrent, originalRead, originalTimeout := switchBranch, getCurrentBranch, getRepoBranchDetails, repoDetailTimeout
	repoDetailTimeout = 100 * time.Millisecond
	switchBranch = func(item *git.RepoItem, branch string) error {
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
