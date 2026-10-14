package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/seankoji-com/freshen/pkg/proc"
)

// wrapCommands puts POSIX shell wrappers first on PATH, by command name. Each
// script may use $REAL_GIT and $SLEEP.
func wrapCommands(t *testing.T, scripts map[string]string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the wrappers are POSIX shell scripts")
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
	for name, body := range scripts {
		script := "#!/bin/sh\nREAL_GIT=" + realGit + "\nSLEEP=" + sleep + "\n" + body + "\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func shortWaitDelay(t *testing.T) {
	t.Helper()
	original := proc.WaitDelay
	proc.WaitDelay = 200 * time.Millisecond
	t.Cleanup(func() { proc.WaitDelay = original })
}

// A git whose hook or helper keeps stdout open after git exits cannot hold
// the branch listing: each read returns once WaitDelay passes, with git's
// own output intact.
func TestBranchReadsBoundHeldOutput(t *testing.T) {
	f := newSyncFixture(t)
	shortWaitDelay(t)
	wrapCommands(t, map[string]string{"git": `"$SLEEP" 3 &
exec "$REAL_GIT" "$@"`})

	start := time.Now()
	details := GetRepoBranchDetails(context.Background(), f.work, "main")
	if waited := time.Since(start); waited > 2500*time.Millisecond {
		t.Fatalf("three reads took %s; each waited for the child holding stdout", waited)
	}
	if details.BranchesErr != "" || details.WorktreesErr != "" || details.StatusErr != "" {
		t.Fatalf("reads failed: %+v", details)
	}
	if len(details.LocalBranches) == 0 || !strings.Contains(details.LocalBranches[0], "main") {
		t.Fatalf("branch listing lost: %+v", details)
	}
}

// A pull whose hook leaves a process holding its output still finishes the
// sync, and its exit status, not the held pipe, decides the outcome.
func TestSyncPullBoundsHeldOutput(t *testing.T) {
	f := newSyncFixture(t)
	f.pushUpstream()
	shortWaitDelay(t)
	runnerBlocksOn(t, false, func([]string) bool { return false })
	wrapCommands(t, map[string]string{"git": `if [ "$3" = pull ]; then "$SLEEP" 3 & fi
exec "$REAL_GIT" "$@"`})

	done := make(chan *RepoItem, 1)
	go func() { done <- f.sync(false) }()
	var item *RepoItem
	select {
	case item = <-done:
	case <-time.After(2500 * time.Millisecond):
		t.Fatal("sync waited for the process holding the pull's output")
	}
	if item.Status != StatusUpdated {
		t.Fatalf("sync status %s; logs: %v", item.Status, item.Logs)
	}
	if f.readWork("upstream.txt") != "new\n" {
		t.Fatal("pull did not land")
	}
}

// A clone stopped at its deadline takes the processes it started with it: a
// child gh left writing into the checkout cannot keep writing after the sync
// reports the clone stopped.
func TestStoppedCloneLeavesNoWriter(t *testing.T) {
	wrapCommands(t, map[string]string{
		"gh":  `mkdir -p "$4"; (while :; do echo x >> "$4/marker"; "$SLEEP" 0.02; done) & wait`,
		"git": `exec "$REAL_GIT" "$@"`,
	})
	shortWaitDelay(t)
	original := cloneTimeout
	t.Cleanup(func() { cloneTimeout = original })
	cloneTimeout = 500 * time.Millisecond

	path := filepath.Join(t.TempDir(), "fresh")
	marker := filepath.Join(path, "marker")
	item := &RepoItem{Name: "fresh", GHRepoName: "fresh", Path: path, IsNew: true}
	SyncRepository(context.Background(), item, nil, false)
	if item.Status != StatusError || item.StatusMsg != "Clone Err" {
		t.Fatalf("sync status %s %q; logs: %v", item.Status, item.StatusMsg, item.Logs)
	}
	if last := item.Logs[len(item.Logs)-1]; !strings.Contains(last, "was stopped") || strings.Contains(last, "still running") {
		t.Fatalf("stopped clone reported as %q", last)
	}
	before := fileSize(marker)
	if before == 0 {
		t.Fatal("the fake clone never wrote; the test proves nothing")
	}
	time.Sleep(300 * time.Millisecond)
	if after := fileSize(marker); after != before {
		t.Fatalf("a process the clone started kept writing: %d -> %d bytes", before, after)
	}
	if err := checkUnfinished(path); err != nil {
		t.Fatalf("a fully stopped clone still holds its path: %v", err)
	}
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// A delete the filesystem never answers returns at the action's deadline,
// and the path stays held: sync, switch, prune, publish and another delete
// all refuse it until the removal ends, then work again.
func TestDeleteIsBoundedAndHoldsItsPath(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "archived")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	original := removeAll
	removeAll = func(p string) error {
		<-release
		return original(p)
	}
	released := false
	t.Cleanup(func() {
		if !released {
			close(release)
		}
		removeAll = original
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := DeleteLocalRepo(ctx, workspace, path)
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("hung delete took %s", waited)
	}
	if !errors.Is(err, ErrUnfinishedChange) || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("hung delete returned %v", err)
	}

	item := &RepoItem{Name: "archived", Path: path, DefaultBranch: "main", CurrentBranch: "main"}
	refusals := map[string]error{
		"delete": DeleteLocalRepo(context.Background(), workspace, path),
		"switch": SwitchBranch(context.Background(), item.Clone(), "main"),
		"push":   CommitPushPRAndSwitchDefault(context.Background(), item.Clone()),
	}
	_, refusals["prune"] = PruneBranchesAndWorktrees(context.Background(), item.Clone())
	for name, err := range refusals {
		if !errors.Is(err, ErrUnfinishedChange) {
			t.Fatalf("%s while the delete ran: %v", name, err)
		}
	}
	synced := item.Clone()
	SyncRepository(context.Background(), synced, nil, false)
	if synced.StatusMsg != "Busy" || !strings.Contains(synced.Logs[len(synced.Logs)-1], "nothing was changed") {
		t.Fatalf("sync while the delete ran: %s %q %v", synced.Status, synced.StatusMsg, synced.Logs)
	}
	if err := checkUnfinished(filepath.Join(workspace, "other")); err != nil {
		t.Fatalf("an unrelated path was held: %v", err)
	}

	released = true
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for checkUnfinished(path) != nil {
		if time.Now().After(deadline) {
			t.Fatal("the path stayed held after the delete ended")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the delete did not complete once the filesystem answered: %v", err)
	}
}

// Creating a clone's directory shares the clone's deadline: a filesystem
// that never answers fails the sync with a clone error instead of holding it.
func TestCloneDirectoryCreationIsBounded(t *testing.T) {
	release := make(chan struct{})
	original := mkdirAll
	mkdirAll = func(p string, perm os.FileMode) error {
		<-release
		return original(p, perm)
	}
	t.Cleanup(func() {
		close(release)
		mkdirAll = original
		// Let the held directory creation finish before the next test.
		deadline := time.Now().Add(5 * time.Second)
		for len(heldPaths()) > 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
	})
	originalTimeout := cloneTimeout
	t.Cleanup(func() { cloneTimeout = originalTimeout })
	cloneTimeout = 200 * time.Millisecond

	item := &RepoItem{Name: "fresh", GHRepoName: "fresh", Path: filepath.Join(t.TempDir(), "org", "fresh"), IsNew: true}
	done := make(chan struct{})
	go func() {
		SyncRepository(context.Background(), item, nil, false)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sync did not finish while creating its directory hung")
	}
	if item.Status != StatusError || item.StatusMsg != "Clone Err" || !strings.Contains(item.Logs[len(item.Logs)-1], "still running") {
		t.Fatalf("sync status %s %q; logs: %v", item.Status, item.StatusMsg, item.Logs)
	}
}

func heldPaths() []string {
	unfinished.Lock()
	defer unfinished.Unlock()
	var paths []string
	for held := range unfinished.held {
		paths = append(paths, held.path)
	}
	return paths
}

// A clone that lands but whose branch reads fail keeps the clone, leaves the
// unread branches empty rather than "HEAD", and says so in the log.
func TestCloneReportsUnreadBranches(t *testing.T) {
	for name, tc := range map[string]struct {
		fail                  func([]string) bool
		wantCurrent, wantDflt string
		wantInLog             []string
	}{
		"current branch": {
			fail:        hasArgs("--short", "HEAD"),
			wantCurrent: "", wantDflt: "main",
			wantInLog: []string{"checked-out branch"},
		},
		"both branches": {
			fail: func(args []string) bool {
				return hasArgs("--short", "HEAD")(args) || hasArgs("refs/remotes/origin/HEAD")(args) || hasArgs("show-ref")(args)
			},
			wantCurrent: "", wantDflt: "",
			wantInLog: []string{"checked-out branch", "default branch"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newSyncFixture(t)
			wrapCommands(t, map[string]string{"gh": `exec "$REAL_GIT" clone "` + f.origin + `" "$4"`})
			runnerBlocksOn(t, true, tc.fail)

			item := &RepoItem{Name: "fresh", GHRepoName: "fresh", Path: filepath.Join(t.TempDir(), "fresh"), IsNew: true}
			SyncRepository(context.Background(), item, nil, false)
			if item.Status != StatusCloned || item.StatusMsg != "Branch unread" || item.IsNew {
				t.Fatalf("clone recorded as %s %q (new %v); logs: %v", item.Status, item.StatusMsg, item.IsNew, item.Logs)
			}
			if item.CurrentBranch != tc.wantCurrent || item.OriginalBranch != tc.wantCurrent || item.DefaultBranch != tc.wantDflt {
				t.Fatalf("branches %q/%q, want %q/%q", item.CurrentBranch, item.DefaultBranch, tc.wantCurrent, tc.wantDflt)
			}
			logs := strings.Join(item.Logs, "\n")
			for _, want := range append(tc.wantInLog, "press r to refresh") {
				if !strings.Contains(logs, want) {
					t.Fatalf("log lacks %q: %s", want, logs)
				}
			}
			if _, err := os.Stat(filepath.Join(item.Path, "tracked.txt")); err != nil {
				t.Fatalf("the clone did not land: %v", err)
			}
		})
	}
}

// A refresh's checkout probe tells a missing directory from one it could not
// check, and gives up at its deadline.
func TestDirExistsTellsMissingFromFailed(t *testing.T) {
	dir := t.TempDir()
	if ok, err := DirExists(context.Background(), dir); !ok || err != nil {
		t.Fatalf("existing dir: %v %v", ok, err)
	}
	if ok, err := DirExists(context.Background(), filepath.Join(dir, "missing")); ok || err != nil {
		t.Fatalf("missing dir: %v %v", ok, err)
	}
	shortBranchReads(t)
	hung := filepath.Join(dir, "hung")
	blockStat(t, hung)
	start := time.Now()
	if ok, err := DirExists(context.Background(), hung); ok || !errors.Is(err, errPathUnanswered) {
		t.Fatalf("hung probe: %v %v", ok, err)
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("hung probe took %s", waited)
	}
}
