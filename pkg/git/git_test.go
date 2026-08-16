package git

import (
	"context"
	"os/exec"
	"testing"
)

func TestRepoItemCloneIsDeep(t *testing.T) {
	original := &RepoItem{
		Name:       "alpha",
		Status:     StatusPending,
		Logs:       []string{"first"},
		IssuesList: []IssueItem{{Number: 1, Title: "issue"}},
		PRsList:    []PRItem{{Number: 2, Title: "pr"}},
		BranchDetails: BranchWorktreeDetails{
			Branches:      []string{"main"},
			LocalBranches: []string{"main"},
			Worktrees:     []string{"wt"},
			ChangedFiles:  []string{"a.go"},
		},
	}

	clone := original.Clone()

	// Appends on either side must stay invisible to the other, even when the
	// original slice has spare capacity.
	original.Logs = append(original.Logs, "second")
	original.IssuesList = append(original.IssuesList, IssueItem{Number: 9})
	original.PRsList = append(original.PRsList, PRItem{Number: 9})
	original.BranchDetails.Branches = append(original.BranchDetails.Branches, "topic")
	original.Status = StatusError

	if len(clone.Logs) != 1 || clone.Logs[0] != "first" {
		t.Errorf("clone logs aliased the original: %v", clone.Logs)
	}
	if len(clone.IssuesList) != 1 || len(clone.PRsList) != 1 {
		t.Errorf("clone issue/PR lists aliased the original")
	}
	if len(clone.BranchDetails.Branches) != 1 {
		t.Errorf("clone branch details aliased the original: %v", clone.BranchDetails.Branches)
	}
	if clone.Status != StatusPending {
		t.Errorf("clone status changed with the original: %s", clone.Status)
	}
	if clone.Name != "alpha" {
		t.Errorf("clone lost scalar fields: %+v", clone)
	}
}

// newTestRepo builds a throwaway repository on a "main" branch with no remote,
// so a sync runs entirely offline and lands in the pull-error path.
func newTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	steps := [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@example.invalid"},
		{"config", "user.name", "freshen test"},
		{"commit", "--allow-empty", "-m", "initial"},
	}
	for _, args := range steps {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v unavailable in this environment: %v (%s)", args, err, out)
		}
	}
	return dir
}

// SyncRepository must hand the progress callback snapshots that it never writes
// to again, so the TUI can read them from another goroutine while the sync is
// still running. Run under -race, this fails if snapshots alias the live item.
func TestSyncRepositoryPublishesOwnedSnapshots(t *testing.T) {
	item := &RepoItem{Name: "probe", Path: newTestRepo(t), Logs: []string{}}

	snapshots := make(chan *RepoItem, 64)
	readerDone := make(chan int)

	// Stands in for the render path: reads snapshots on another goroutine while
	// the sync keeps mutating its own copy.
	go func() {
		seen := 0
		for snapshot := range snapshots {
			seen++
			for _, line := range snapshot.Logs {
				_ = len(line)
			}
			_ = snapshot.Status
			_ = snapshot.CurrentBranch
		}
		readerDone <- seen
	}()

	SyncRepository(context.Background(), item, func(snapshot *RepoItem) {
		snapshots <- snapshot
	})
	close(snapshots)

	if seen := <-readerDone; seen < 2 {
		t.Fatalf("expected the sync to publish progress, got %d snapshots", seen)
	}
	if item.Status != StatusError {
		t.Errorf("a repo with no remote should end in the pull-error path, got %s", item.Status)
	}
	if len(item.Logs) == 0 {
		t.Error("expected the sync to have written its log to the live item")
	}
}

// A nil emitter is the batch-mode path: no snapshots, same in-place mutation.
func TestSyncRepositoryWithoutProgress(t *testing.T) {
	item := &RepoItem{Name: "probe", Path: newTestRepo(t), Logs: []string{}}

	SyncRepository(context.Background(), item, nil)

	if len(item.Logs) == 0 {
		t.Error("expected sync logs on the item")
	}
	if item.Status == StatusPending {
		t.Error("expected the sync to have moved the status off PENDING")
	}
}

// A cancelled context must abort before any work or logging happens.
func TestSyncRepositoryRespectsCancelledContext(t *testing.T) {
	item := &RepoItem{Name: "probe", Path: t.TempDir(), Logs: []string{}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	published := 0
	SyncRepository(ctx, item, func(*RepoItem) { published++ })

	if published != 0 || len(item.Logs) != 0 {
		t.Errorf("cancelled sync did work anyway: %d snapshots, %d logs", published, len(item.Logs))
	}
}

func TestSyncRepositoryClonesUnclonedRepo(t *testing.T) {
	src := newTestRepo(t)
	dest := t.TempDir() + "/deep/nested/dir/cloned-repo"

	item := &RepoItem{
		Name: "cloned-repo",
		URL:  src,
		Path: dest,
		Logs: []string{},
	}

	SyncRepository(context.Background(), item, nil)

	if item.Status != StatusCloned {
		t.Fatalf("expected status CLONED, got %s (logs: %v)", item.Status, item.Logs)
	}
	if item.StatusMsg != "Cloned" {
		t.Errorf("expected StatusMsg 'Cloned', got %q", item.StatusMsg)
	}
	if !IsGitRepo(dest) {
		t.Errorf("expected %s to be a git repository after clone", dest)
	}
	if item.DefaultBranch != "main" {
		t.Errorf("expected default branch main, got %s", item.DefaultBranch)
	}
	if item.CurrentBranch != "main" {
		t.Errorf("expected current branch main, got %s", item.CurrentBranch)
	}
	if item.OriginalBranch != "main" {
		t.Errorf("expected original branch main, got %s", item.OriginalBranch)
	}
	if item.IsNew {
		t.Errorf("expected IsNew to be false after successful clone")
	}
}

func TestSyncRepositoryUnclonedRepoWithoutTarget(t *testing.T) {
	item := &RepoItem{
		Name: "missing-repo",
		Path: t.TempDir() + "/nonexistent",
		Logs: []string{},
	}

	SyncRepository(context.Background(), item, nil)

	if item.Status != StatusError {
		t.Fatalf("expected status ERROR when no target URL/name, got %s", item.Status)
	}
	if item.StatusMsg != "Not Found" {
		t.Errorf("expected StatusMsg 'Not Found', got %q", item.StatusMsg)
	}
}
