package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// hangGitOn puts a git wrapper first on PATH that never answers "git -C <dir>
// <hangOn> ..." and runs the real git for every other command.
func hangGitOn(t *testing.T, hangOn string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the git wrapper is a POSIX shell script")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not available")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$3\" = \"" + hangOn + "\" ]; then exec " + sleep + " 60; fi\nexec " + realGit + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func shortBranchReads(t *testing.T) {
	t.Helper()
	original := branchReadTimeout
	branchReadTimeout = 200 * time.Millisecond
	t.Cleanup(func() { branchReadTimeout = original })
}

// A caller with no deadline of its own still gets an answer: the branch,
// worktree and status reads share branchReadTimeout.
func TestGetRepoBranchDetailsBoundsItsOwnReads(t *testing.T) {
	shortBranchReads(t)
	hangGitOn(t, "branch")

	start := time.Now()
	details := GetRepoBranchDetails(context.Background(), t.TempDir(), "main")
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("hung branch read took %s", waited)
	}
	for name, reason := range map[string]string{"branches": details.BranchesErr, "worktrees": details.WorktreesErr, "status": details.StatusErr} {
		if reason != "git did not answer before the deadline" {
			t.Fatalf("%s: %q", name, reason)
		}
	}
}

func TestGetOriginalBranchBoundsItsOwnRead(t *testing.T) {
	shortBranchReads(t)
	original := runner
	runner = &contextFakeRunner{fn: func(ctx context.Context, _ string, _ []string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	t.Cleanup(func() { runner = original })

	start := time.Now()
	if got := GetOriginalBranch(context.Background(), "/nowhere"); got != "HEAD" {
		t.Fatalf("failed read returned %q", got)
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("hung read took %s", waited)
	}
}

// A sync whose branch listing hangs still pulls and finishes, and reports the
// listing as failed rather than holding its worker (and IsSyncing) open.
func TestSyncFinishesWhenBranchReadHangs(t *testing.T) {
	f := newSyncFixture(t)
	f.pushUpstream()
	shortBranchReads(t)
	hangGitOn(t, "branch")

	done := make(chan *RepoItem, 1)
	go func() { done <- f.sync(false) }()
	var item *RepoItem
	select {
	case item = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("sync did not finish while its branch read hung")
	}
	if item.Status != StatusUpdated {
		t.Fatalf("sync status %s; logs: %v", item.Status, item.Logs)
	}
	if item.BranchDetails.BranchesErr != "git did not answer before the deadline" {
		t.Fatalf("hung read not reported: %+v", item.BranchDetails)
	}
	if f.readWork("upstream.txt") != "new\n" {
		t.Fatal("pull did not land")
	}
}
