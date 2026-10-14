package tui

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/seankoji-com/freshen/pkg/git"
)

// stubOrgRefresh makes an organization refresh read repos from GitHub, an
// empty workspace listing, and probe checkouts through probe, with no
// external calls.
func stubOrgRefresh(t *testing.T, repos []git.GHRepoInfo, probe func(ctx context.Context, path string) (bool, error)) {
	t.Helper()
	origRepos, origCounts, origScan, origProbe := fetchOrgRepos, fetchOrgRepoCounts, scanLocalDirectoryContext, dirExists
	fetchOrgRepos = func(context.Context, string) ([]git.GHRepoInfo, error) { return repos, nil }
	fetchOrgRepoCounts = func(context.Context, string) (map[string]git.RepoCounts, error) {
		return map[string]git.RepoCounts{"alpha": {Issues: 7, PRs: 1}}, nil
	}
	scanLocalDirectoryContext = func(context.Context, string) ([]string, error) { return nil, nil }
	dirExists = probe
	t.Cleanup(func() {
		fetchOrgRepos, fetchOrgRepoCounts, scanLocalDirectoryContext, dirExists = origRepos, origCounts, origScan, origProbe
	})
}

func repoNamed(repos []*git.RepoItem, name string) *git.RepoItem {
	for _, r := range repos {
		if r.Name == name {
			return r
		}
	}
	return nil
}

// A refresh that cannot tell whether a checkout exists, because the probe
// failed or never answered, keeps what it knew about the repository, lists an
// unknown one as unreadable (never "not cloned", and an archived one is not
// dropped), and says the local metadata is incomplete.
func TestRefreshProbeFailureKeepsRepositories(t *testing.T) {
	for name, probeErr := range map[string]func(ctx context.Context) error{
		"failed": func(context.Context) error { return &fs.PathError{Op: "stat", Path: "x", Err: fs.ErrPermission} },
		"hung": func(ctx context.Context) error {
			// The real probe gives up at its own deadline; this one stands
			// in for it after a moment, or when the refresh ends.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(50 * time.Millisecond):
				return context.DeadlineExceeded
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := sized(smokeFixtureModel(), 120, 40)
			m.ctx = context.Background()
			dir := m.TargetDir
			stubOrgRefresh(t, []git.GHRepoInfo{
				{Name: "alpha", URL: "https://github.com/fixture-org/alpha"},
				{Name: "old", URL: "https://github.com/fixture-org/old", IsArchived: true},
				{Name: "unknown", URL: "https://github.com/fixture-org/unknown"},
				{Name: "brandnew", URL: "https://github.com/fixture-org/brandnew"},
			}, func(ctx context.Context, path string) (bool, error) {
				if filepath.Base(path) == "brandnew" {
					return false, nil
				}
				return false, probeErr(ctx)
			})

			cmd := m.startOrgRefresh(false, true)
			msg, ok := cmd().(orgSyncedMsg)
			if !ok {
				t.Fatal("refresh did not report")
			}
			if msg.localErr == nil || !strings.Contains(msg.localErr.Error(), "could not check 3 local checkouts") {
				t.Fatalf("probe failures not reported: %v", msg.localErr)
			}
			next, _ := m.Update(msg)
			m = next.(Model)

			alpha := repoNamed(m.Repos, "alpha")
			if alpha == nil || alpha.IsNew || alpha.CurrentBranch != "feat/screens" || alpha.Status != git.StatusUpdated || !alpha.LocalMetadataStale {
				t.Fatalf("known repository lost its state: %+v", alpha)
			}
			if alpha.OpenIssuesCount != 7 {
				t.Fatalf("fresh counts not applied: %d", alpha.OpenIssuesCount)
			}
			for _, name := range []string{"old", "unknown"} {
				r := repoNamed(m.Repos, name)
				if r == nil {
					t.Fatalf("%s dropped when its probe failed", name)
				}
				if r.IsNew || !r.CheckoutUnknown || r.Status != git.StatusError || r.StatusMsg != "Read Err" || !strings.Contains(strings.Join(r.Logs, "\n"), "Could not check whether "+filepath.Join(dir, name)) {
					t.Fatalf("%s recorded as %+v", name, r)
				}
			}
			if brandnew := repoNamed(m.Repos, "brandnew"); brandnew == nil || !brandnew.IsNew {
				t.Fatalf("a confirmed-missing checkout is not offered for cloning: %+v", brandnew)
			}
			if !strings.Contains(m.ToastMsg, "Local repository metadata is incomplete") {
				t.Fatalf("toast %q", m.ToastMsg)
			}

			// Nothing destructive, and no clone, for a checkout freshen could
			// not see: an archived one offers no delete, an active one only a
			// sync that probes again before acting.
			ids := func(name string) []string {
				m.SelectedIndex = slices.IndexFunc(m.Repos, func(r *git.RepoItem) bool { return r.Name == name })
				m.Detail = true
				m.captureActionTarget()
				if m.ActionTarget == nil || m.ActionTarget.Name != name {
					t.Fatalf("action target %+v, want %s", m.ActionTarget, name)
				}
				var out []string
				for _, a := range m.menuActions() {
					out = append(out, a.id+":"+a.label)
				}
				return out
			}
			if got := ids("old"); slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, "delete") }) {
				t.Fatalf("delete offered for an unprobed archived checkout: %v", got)
			}
			got := ids("unknown")
			if !slices.Contains(got, "sync:Sync repository") || slices.ContainsFunc(got, func(s string) bool {
				return strings.HasPrefix(s, "switch") || strings.HasPrefix(s, "push") || strings.HasPrefix(s, "prune") || strings.Contains(s, "Clone")
			}) {
				t.Fatalf("actions for an unprobed checkout: %v", got)
			}
		})
	}
}

// A clone's snapshot clears the repository's "new" flag at once, and a
// refresh that read the repository before the clone landed cannot set it
// back, so the clone is not offered again and its details load.
func TestCloneSnapshotClearsNewAgainstLateRefresh(t *testing.T) {
	m := sized(smokeFixtureModel(), 120, 40)
	m.Repos[1].IsNew = true
	m.Repos[1].CurrentBranch, m.Repos[1].DefaultBranch = "", ""
	if m.startOrgRefresh(false, false) == nil {
		t.Fatal("refresh did not start")
	}
	late := refreshRecords(m, m.Repos[0].CurrentBranch, m.OrgRefreshGeneration)
	late.repos[1].IsNew = true // read before the clone landed

	snapshot := m.Repos[1].Clone()
	snapshot.IsNew = false
	snapshot.Status = git.StatusCloned
	snapshot.StatusMsg = "Cloned"
	snapshot.CurrentBranch, snapshot.OriginalBranch, snapshot.DefaultBranch = "main", "main", "main"
	next, _ := m.Update(repoSyncMsg{repo: snapshot, snapshots: make(chan *git.RepoItem), bulk: true})
	m = next.(Model)
	if m.Repos[1].IsNew {
		t.Fatal("the clone's snapshot left the repository marked not cloned")
	}

	next, _ = m.Update(late)
	m = next.(Model)
	beta := repoNamed(m.Repos, "beta")
	if beta.IsNew || beta.Status != git.StatusCloned || beta.CurrentBranch != "main" {
		t.Fatalf("late refresh undid the clone: new=%v status=%s branch=%q", beta.IsNew, beta.Status, beta.CurrentBranch)
	}
	m.SelectedIndex = slices.Index(m.Repos, beta)
	m.Detail = true
	m.captureActionTarget()
	if m.ActionTarget == nil || m.ActionTarget.Name != "beta" {
		t.Fatalf("action target %+v", m.ActionTarget)
	}
	for _, a := range m.menuActions() {
		if a.label == "Clone repository" {
			t.Fatal("clone offered again after it landed")
		}
	}
	if !slices.ContainsFunc(m.menuActions(), func(a uiAction) bool { return a.id == "switch" }) {
		t.Fatal("cloned repository does not offer its branch actions")
	}
}

// A refresh whose probe fails for a repository it never saw before still
// cannot surface it as deletable, even after an older record is merged.
func TestUnprobedRecordNeverClaimsAbsence(t *testing.T) {
	item := unprobedRepo(nil, git.GHRepoInfo{Name: "x", IsArchived: true}, "x", "/w/x", errors.New("stat /w/x: permission denied"))
	if item.IsNew || !item.CheckoutUnknown || item.ErrorErr == nil {
		t.Fatalf("unprobed record: %+v", item)
	}
	prev := &git.RepoItem{Name: "x", Path: "/w/x", CurrentBranch: "main", Status: git.StatusUpdated, HasLoadedCounts: true}
	kept := unprobedRepo(prev, git.GHRepoInfo{Name: "x", IsArchived: true}, "x", "/w/x", errors.New("boom"))
	if kept.CurrentBranch != "main" || kept.Status != git.StatusUpdated || !kept.IsArchived || !kept.LocalMetadataStale || !kept.CountsStale || kept.CheckoutUnknown {
		t.Fatalf("earlier record not kept: %+v", kept)
	}
}

// stubCheckoutReads answers each checkout read by directory name.
func stubCheckoutReads(t *testing.T, read func(name string) (git.LocalCheckout, error)) {
	t.Helper()
	original := readLocalCheckout
	readLocalCheckout = func(_ context.Context, path string) (git.LocalCheckout, error) { return read(filepath.Base(path)) }
	t.Cleanup(func() { readLocalCheckout = original })
}

// A refresh whose git reads fail keeps what it knew: a checkout check that
// fails never reads as "not a checkout" (a known repository keeps its state,
// an unknown one is listed unreadable), a failed branch read never blanks a
// known branch or reports it changed, and the toast says the local metadata
// is incomplete. Both the organization and the local-only refresh.
func TestRefreshKeepsStateWhenCheckoutReadsFail(t *testing.T) {
	checkErr := errors.New("could not tell whether it is a checkout (fatal: detected dubious ownership)")
	cases := map[string]struct {
		alpha     func() (git.LocalCheckout, error)
		wantAlpha func(t *testing.T, alpha *git.RepoItem)
	}{
		"checkout check": {
			alpha: func() (git.LocalCheckout, error) { return git.LocalCheckout{}, checkErr },
			wantAlpha: func(t *testing.T, alpha *git.RepoItem) {
				if alpha.IsNew || alpha.CurrentBranch != "feat/screens" || alpha.DefaultBranch != "main" || alpha.Status != git.StatusUpdated {
					t.Fatalf("known repository lost its state: %+v", alpha)
				}
			},
		},
		"current branch": {
			alpha: func() (git.LocalCheckout, error) {
				return git.LocalCheckout{IsRepo: true, DefaultBranch: "main"}, errors.New("could not read the checked-out branch (exit status 128)")
			},
			wantAlpha: func(t *testing.T, alpha *git.RepoItem) {
				if alpha.IsNew || alpha.CurrentBranch != "feat/screens" || alpha.DefaultBranch != "main" {
					t.Fatalf("failed branch read blanked the branch: %+v", alpha)
				}
			},
		},
		"default branch": {
			alpha: func() (git.LocalCheckout, error) {
				return git.LocalCheckout{IsRepo: true, CurrentBranch: "feat/screens"}, errors.New("could not read the default branch (exit status 128)")
			},
			wantAlpha: func(t *testing.T, alpha *git.RepoItem) {
				if alpha.CurrentBranch != "feat/screens" || alpha.DefaultBranch != "main" {
					t.Fatalf("failed default read blanked the default: %+v", alpha)
				}
			},
		},
	}
	for _, org := range []bool{true, false} {
		for name, tc := range cases {
			t.Run(fmt.Sprintf("org=%v/%s", org, name), func(t *testing.T) {
				m := sized(smokeFixtureModel(), 120, 40)
				m.ctx = context.Background()
				stubOrgRefresh(t, []git.GHRepoInfo{
					{Name: "alpha", URL: "https://github.com/fixture-org/alpha"},
					{Name: "beta", URL: "https://github.com/fixture-org/beta"},
					{Name: "unknown", URL: "https://github.com/fixture-org/unknown"},
				}, func(context.Context, string) (bool, error) { return true, nil })
				scanLocalDirectoryContext = func(context.Context, string) ([]string, error) {
					return []string{"alpha", "beta", "unknown"}, nil
				}
				if !org {
					m.TargetOrg = ""
				}
				stubCheckoutReads(t, func(dir string) (git.LocalCheckout, error) {
					switch dir {
					case "alpha":
						return tc.alpha()
					case "unknown":
						return git.LocalCheckout{}, checkErr
					}
					return git.LocalCheckout{IsRepo: true, CurrentBranch: "main", DefaultBranch: "main"}, nil
				})

				msg, ok := m.startOrgRefresh(false, true)().(orgSyncedMsg)
				if !ok {
					t.Fatal("refresh did not report")
				}
				if msg.localErr == nil || !strings.Contains(msg.localErr.Error(), "alpha: could not") {
					t.Fatalf("read failure not reported: %v", msg.localErr)
				}
				next, _ := m.Update(msg)
				m = next.(Model)

				alpha := repoNamed(m.Repos, "alpha")
				if alpha == nil {
					t.Fatal("known repository dropped when its read failed")
				}
				tc.wantAlpha(t, alpha)
				if !alpha.LocalMetadataStale {
					t.Fatalf("kept state not marked stale: %+v", alpha)
				}
				unknown := repoNamed(m.Repos, "unknown")
				if unknown == nil || unknown.IsNew || !unknown.CheckoutUnknown || unknown.StatusMsg != "Read Err" {
					t.Fatalf("unreadable checkout recorded as %+v", unknown)
				}
				if beta := repoNamed(m.Repos, "beta"); beta == nil || beta.CurrentBranch != "main" {
					t.Fatalf("a readable checkout was not refreshed: %+v", beta)
				}
				if strings.Contains(m.ToastMsg, "Branch changed") || !strings.Contains(m.ToastMsg, "Local repository metadata is incomplete") {
					t.Fatalf("toast %q", m.ToastMsg)
				}
			})
		}
	}
}
