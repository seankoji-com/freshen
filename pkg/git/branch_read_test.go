package git

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
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

// runnerBlocksOn installs a runner whose commands matching block wait for
// their context to end. Every other command runs for real with its deadline
// removed, so under a shortened branchReadTimeout only the deliberately hung
// read can time out, however loaded the machine is. fail makes matching
// commands fail at once instead. The GitHub default-branch tier is stubbed
// out so no test reaches the network.
func runnerBlocksOn(t *testing.T, fail bool, match func(args []string) bool) {
	t.Helper()
	original, originalGH := runner, ghDefaultBranch
	real := &execRunner{}
	runner = &contextFakeRunner{fn: func(ctx context.Context, name string, args []string) ([]byte, error) {
		if match(args) {
			if fail {
				return nil, errors.New("exit status 128: fatal: simulated read failure")
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return real.Run(context.WithoutCancel(ctx), name, args...)
	}}
	ghDefaultBranch = func(context.Context, string) (string, error) {
		return "", errors.New("gh disabled in tests")
	}
	t.Cleanup(func() { runner, ghDefaultBranch = original, originalGH })
}

func hasArgs(want ...string) func([]string) bool {
	return func(args []string) bool {
		for _, w := range want {
			if !slices.Contains(args, w) {
				return false
			}
		}
		return true
	}
}

// Each preflight read ends at its own deadline with an error, never a
// placeholder value the sync could act on.
func TestPreflightReadsBoundThemselves(t *testing.T) {
	dir := t.TempDir()
	for name, read := range map[string]func() error{
		"checkout":       func() error { _, err := readIsGitRepo(context.Background(), dir); return err },
		"current branch": func() error { _, err := readCurrentBranch(context.Background(), dir); return err },
		"default branch": func() error { _, err := readDefaultBranch(context.Background(), dir, true); return err },
		"merge":          func() error { _, err := readMergeInProgress(context.Background(), dir); return err },
		"dirty":          func() error { _, err := readDirty(context.Background(), dir); return err },
	} {
		t.Run(name, func(t *testing.T) {
			shortBranchReads(t)
			runnerBlocksOn(t, false, func([]string) bool { return true })
			start := time.Now()
			err := read()
			if waited := time.Since(start); waited > 5*time.Second {
				t.Fatalf("hung read took %s", waited)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("hung read returned %v, want the deadline", err)
			}
		})
	}
}

// A missing path is not a checkout and needs no git; a directory git rejects
// is not one either; a hung check is an error, which IsGitRepo reports as not
// a checkout.
func TestReadIsGitRepoTellsMissingFromFailed(t *testing.T) {
	original := runner
	t.Cleanup(func() { runner = original })
	runner = &contextFakeRunner{fn: func(context.Context, string, []string) ([]byte, error) {
		t.Fatal("git ran for a missing path")
		return nil, nil
	}}
	if ok, err := readIsGitRepo(context.Background(), filepath.Join(t.TempDir(), "absent")); ok || err != nil {
		t.Fatalf("missing path: %v, %v", ok, err)
	}

	runner = &contextFakeRunner{fn: func(context.Context, string, []string) ([]byte, error) {
		return nil, errors.New("exit status 128: fatal: not a git repository")
	}}
	if ok, err := readIsGitRepo(context.Background(), t.TempDir()); ok || err != nil {
		t.Fatalf("plain directory: %v, %v", ok, err)
	}

	// A checkout git cannot read is a failed read, with git's diagnostic,
	// never a missing repository to clone over.
	checkout := t.TempDir()
	if err := os.Mkdir(filepath.Join(checkout, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, diagnostic := range []string{
		"fatal: detected dubious ownership in repository",
		"fatal: cannot change to '/x': Permission denied",
		"fatal: bad config line 1 in file .git/config",
	} {
		runner = &contextFakeRunner{fn: func(context.Context, string, []string) ([]byte, error) {
			return nil, errors.New("exit status 128: " + diagnostic)
		}}
		if ok, err := readIsGitRepo(context.Background(), checkout); ok || err == nil || !strings.Contains(err.Error(), diagnostic) {
			t.Fatalf("%s: got %v, %v; want the diagnostic as an error", diagnostic, ok, err)
		}
	}

	shortBranchReads(t)
	runnerBlocksOn(t, false, func([]string) bool { return true })
	dir := t.TempDir()
	if _, err := readIsGitRepo(context.Background(), dir); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hung check returned %v", err)
	}
	if IsGitRepo(dir) {
		t.Fatal("IsGitRepo reported a hung check as a checkout")
	}
}

// A sync whose preflight read hangs or fails stops before changing anything:
// no clone, checkout, stash or pull, and the reason is reported.
func TestSyncStopsBeforeMutationWhenPreflightReadFails(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fail  bool
		match func([]string) bool
	}{
		{"checkout check hangs", false, hasArgs("--is-inside-work-tree")},
		{"checkout check fails", true, hasArgs("--is-inside-work-tree")},
		{"current branch hangs", false, hasArgs("symbolic-ref", "--short", "HEAD")},
		{"current branch fails", true, func(args []string) bool {
			return hasArgs("symbolic-ref", "--short", "HEAD")(args) || hasArgs("rev-parse", "--short", "HEAD")(args)
		}},
		{"default branch hangs", false, hasArgs("symbolic-ref", "refs/remotes/origin/HEAD")},
		{"merge check hangs", false, hasArgs("MERGE_HEAD")},
		{"merge check fails", true, hasArgs("MERGE_HEAD")},
		{"dirty check hangs", false, hasArgs("status", "--porcelain")},
		{"dirty check fails", true, hasArgs("status", "--porcelain")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSyncFixture(t)
			f.pushUpstream()
			head := runGit(t, f.work, "rev-parse", "HEAD")
			shortBranchReads(t)
			runnerBlocksOn(t, tc.fail, tc.match)

			done := make(chan *RepoItem, 1)
			go func() { done <- f.sync(false) }()
			var item *RepoItem
			select {
			case item = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("sync did not finish while its preflight read hung")
			}
			if item.Status != StatusError || item.StatusMsg != "Read Err" {
				t.Fatalf("sync status %s %q; logs: %v", item.Status, item.StatusMsg, item.Logs)
			}
			reason := "simulated read failure"
			if !tc.fail {
				reason = "git did not answer before the deadline"
			}
			if last := item.Logs[len(item.Logs)-1]; !strings.Contains(last, reason) || !strings.Contains(last, "nothing was") {
				t.Fatalf("failure not reported: %q", last)
			}
			if got := runGit(t, f.work, "rev-parse", "HEAD"); got != head {
				t.Fatalf("HEAD moved from %s to %s", head, got)
			}
			if _, err := os.Stat(filepath.Join(f.work, "upstream.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("sync pulled despite the failed read: %v", err)
			}
			if n := f.stashCount(); n != 0 {
				t.Fatalf("sync left %d stash entries", n)
			}
		})
	}
}

// A sync whose branch listing hangs still pulls and finishes, and reports the
// listing as failed rather than holding its worker (and IsSyncing) open.
func TestSyncFinishesWhenBranchReadHangs(t *testing.T) {
	f := newSyncFixture(t)
	f.pushUpstream()
	shortBranchReads(t)
	hangGitOn(t, "branch")
	// Only the hung listing may reach the shortened deadline.
	runnerBlocksOn(t, false, func([]string) bool { return false })

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

// blockStat makes statPath hang for path until the test ends, counting calls.
func blockStat(t *testing.T, path string) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	release := make(chan struct{})
	original := statPath
	statPath = func(name string) (fs.FileInfo, error) {
		if name == path {
			calls.Add(1)
			<-release
		}
		return original(name)
	}
	t.Cleanup(func() {
		close(release)
		statPath = original
	})
	return &calls
}

// A filesystem that never answers the checkout probe fails the read at its
// deadline, naming the filesystem. Later callers wait on the same probe
// rather than start another, so a hung path holds one goroutine at most.
func TestCheckoutProbeIsBounded(t *testing.T) {
	shortBranchReads(t)
	dir := t.TempDir()
	calls := blockStat(t, dir)
	for range 3 {
		returned := make(chan error, 1)
		go func() {
			_, err := readIsGitRepo(context.Background(), dir)
			returned <- err
		}()
		var err error
		select {
		case err = <-returned:
		case <-time.After(5 * time.Second):
			t.Fatal("hung probe did not return at its deadline")
		}
		if !errors.Is(err, errPathUnanswered) || readFailure(err) != "the filesystem did not answer before the deadline" {
			t.Fatalf("hung probe returned %v (%q)", err, readFailure(err))
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d probes started for one hung path", n)
	}
}

// A sync whose checkout probe hangs stops with a read error at the deadline
// and changes nothing.
func TestSyncStopsWhenCheckoutProbeHangs(t *testing.T) {
	f := newSyncFixture(t)
	f.pushUpstream()
	head := runGit(t, f.work, "rev-parse", "HEAD")
	shortBranchReads(t)
	blockStat(t, f.work)

	done := make(chan *RepoItem, 1)
	go func() { done <- f.sync(false) }()
	var item *RepoItem
	select {
	case item = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("sync did not finish while its checkout probe hung")
	}
	if item.Status != StatusError || item.StatusMsg != "Read Err" {
		t.Fatalf("sync status %s %q; logs: %v", item.Status, item.StatusMsg, item.Logs)
	}
	if last := item.Logs[len(item.Logs)-1]; !strings.Contains(last, "filesystem did not answer") || !strings.Contains(last, "nothing was changed") {
		t.Fatalf("failure not reported: %q", last)
	}
	if got := runGit(t, f.work, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD moved from %s to %s", head, got)
	}
}

// A checkout that never finishes is stopped at checkoutTimeout, and when the
// caller's context ends (quit), with the failure logged and the branch kept.
func TestSwitchBranchIsBounded(t *testing.T) {
	hangGitOn(t, "checkout")
	original := checkoutTimeout
	t.Cleanup(func() { checkoutTimeout = original })
	checkoutTimeout = 200 * time.Millisecond

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, ctx := range map[string]context.Context{"deadline": context.Background(), "quit": cancelled} {
		t.Run(name, func(t *testing.T) {
			item := &RepoItem{Name: "work", Path: t.TempDir(), CurrentBranch: "feature"}
			returned := make(chan error, 1)
			go func() { returned <- SwitchBranch(ctx, item, "main") }()
			var err error
			select {
			case err = <-returned:
			case <-time.After(5 * time.Second):
				t.Fatal("hung checkout was not stopped")
			}
			if err == nil || !strings.Contains(err.Error(), "stopped before it finished") {
				t.Fatalf("hung checkout returned %v", err)
			}
			if item.CurrentBranch != "feature" || len(item.Logs) == 0 || !strings.Contains(item.Logs[len(item.Logs)-1], "Failed to switch") {
				t.Fatalf("failed switch recorded wrongly: %q %v", item.CurrentBranch, item.Logs)
			}
		})
	}
}

// A first clone whose gh never answers is stopped at cloneTimeout, without
// trying the git fallback past the deadline, and says a partial clone may
// remain.
func TestSyncStopsHungClone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the gh and git wrappers are POSIX shell scripts")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not available")
	}
	bin := t.TempDir()
	for name, script := range map[string]string{
		"gh":  "#!/bin/sh\nexec " + sleep + " 60\n",
		"git": "#!/bin/sh\nif [ \"$1\" = clone ]; then exec " + sleep + " 60; fi\nexec " + realGit + " \"$@\"\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	original := cloneTimeout
	t.Cleanup(func() { cloneTimeout = original })
	cloneTimeout = 300 * time.Millisecond

	item := &RepoItem{Name: "fresh", GHRepoName: "fresh", URL: "https://example.invalid/org/fresh.git", Path: filepath.Join(t.TempDir(), "fresh"), IsNew: true}
	done := make(chan struct{})
	go func() {
		SyncRepository(context.Background(), item, nil, false)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("sync did not finish while its clone hung")
	}
	if item.Status != StatusError || item.StatusMsg != "Clone Err" {
		t.Fatalf("sync status %s %q; logs: %v", item.Status, item.StatusMsg, item.Logs)
	}
	if last := item.Logs[len(item.Logs)-1]; !strings.Contains(last, "did not finish within") || !strings.Contains(last, "partial clone") {
		t.Fatalf("stopped clone not reported: %q", last)
	}
}
