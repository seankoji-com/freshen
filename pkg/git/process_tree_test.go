package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
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
	// The child outlives any plausible scheduling delay by far, so the
	// bound below separates waiting for it from a slow machine.
	wrapCommands(t, map[string]string{"git": `"$SLEEP" 60 &
exec "$REAL_GIT" "$@"`})

	start := time.Now()
	details := GetRepoBranchDetails(context.Background(), f.work, "main")
	if waited := time.Since(start); waited > 30*time.Second {
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
	wrapCommands(t, map[string]string{"git": `if [ "$3" = pull ]; then "$SLEEP" 60 & fi
exec "$REAL_GIT" "$@"`})

	done := make(chan *RepoItem, 1)
	go func() { done <- f.sync(false) }()
	var item *RepoItem
	select {
	case item = <-done:
	case <-time.After(30 * time.Second):
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
	// Every git and gh call fails at once, so an operation that wrongly
	// proceeds fails the test instead of reaching GitHub or hanging.
	wrapCommands(t, map[string]string{"git": "exit 1", "gh": "exit 1"})
	runnerBlocksOn(t, true, func([]string) bool { return true })
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
	returned := make(chan error, 1)
	go func() { returned <- DeleteLocalRepo(ctx, workspace, path) }()
	var err error
	select {
	case err = <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("hung delete did not return at its deadline")
	}
	if !errors.Is(err, ErrUnfinishedChange) || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("hung delete returned %v", err)
	}

	item := &RepoItem{Name: "archived", Path: path, DefaultBranch: "main", CurrentBranch: "main"}
	quick, cancelQuick := context.WithTimeout(context.Background(), time.Second)
	defer cancelQuick()
	refusals := map[string]error{
		"delete": DeleteLocalRepo(quick, workspace, path),
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

// A delete whose context has already ended, or ends while the path is still
// being validated, removes nothing: it reports that it was not started, and a
// validation that finishes after the deadline cannot start the removal.
func TestCancelledDeleteNeverStartsRemoval(t *testing.T) {
	newArchived := func(t *testing.T) (workspace, path string) {
		workspace = t.TempDir()
		path = filepath.Join(workspace, "archived")
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
		return workspace, path
	}
	var removed []string
	originalRemove := removeAll
	removeAll = func(p string) error {
		removed = append(removed, p)
		return originalRemove(p)
	}
	t.Cleanup(func() { removeAll = originalRemove })

	t.Run("already cancelled", func(t *testing.T) {
		removed = nil
		workspace, path := newArchived(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := DeleteLocalRepo(ctx, workspace, path)
		if err == nil || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "nothing was changed") {
			t.Fatalf("cancelled delete returned %v", err)
		}
		// A worker that was started anyway holds the path until it returns;
		// wait for that before checking it removed nothing.
		deadline := time.Now().Add(5 * time.Second)
		for checkUnfinished(path) != nil {
			if time.Now().After(deadline) {
				t.Fatal("the path stayed held")
			}
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(50 * time.Millisecond)
		if len(removed) != 0 {
			t.Fatalf("a cancelled delete started removing %v", removed)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("the directory is gone: %v", err)
		}
	})

	t.Run("validation outlives the deadline", func(t *testing.T) {
		removed = nil
		workspace, path := newArchived(t)
		release := make(chan struct{})
		validated := make(chan struct{})
		originalValidate := validateWorkspacePath
		validateWorkspacePath = func(w, p string) error {
			<-release
			defer close(validated)
			return originalValidate(w, p)
		}
		t.Cleanup(func() { validateWorkspacePath = originalValidate })

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		err := DeleteLocalRepo(ctx, workspace, path)
		if err == nil || errors.Is(err, ErrUnfinishedChange) || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "nothing was changed") {
			t.Fatalf("delete stopped during validation returned %v", err)
		}
		// The validation is still reading, so the path stays held until it
		// returns; nothing may act on it meanwhile.
		if checkUnfinished(path) == nil {
			t.Fatal("the path was not held while validation still ran")
		}
		close(release)
		<-validated
		deadline := time.Now().Add(5 * time.Second)
		for checkUnfinished(path) != nil {
			if time.Now().After(deadline) {
				t.Fatal("the path stayed held after validation returned")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if len(removed) != 0 {
			t.Fatalf("validation that finished after the deadline started removing %v", removed)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("the directory is gone: %v", err)
		}
	})
}

// A checkout the kernel never lets exit, which no kill can end, still returns
// the switch soon after its deadline. The checkout's path stays held until
// the process is finally reaped: a sync refuses it and no other git command,
// not even a read, starts on it meanwhile.
func TestUnreapableCheckoutHoldsThePath(t *testing.T) {
	f := newSyncFixture(t)
	runGit(t, f.work, "branch", "feature")
	shortWaitDelay(t)
	originalReap := proc.ReapTimeout
	proc.ReapTimeout = 200 * time.Millisecond
	t.Cleanup(func() { proc.ReapTimeout = originalReap })
	release := make(chan struct{})
	restore := proc.BlockReaping(func(args []string) bool { return slices.Contains(args, "checkout") }, release, 100*time.Millisecond)
	released := false
	t.Cleanup(func() {
		if !released {
			close(release)
		}
		restore()
	})

	item := &RepoItem{Name: "work", Path: f.work, CurrentBranch: "main", DefaultBranch: "main"}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	returned := make(chan error, 1)
	go func() { returned <- SwitchBranch(ctx, item, "feature") }()
	var err error
	select {
	case err = <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("the switch waited for a checkout that could not be reaped")
	}
	if !errors.Is(err, proc.ErrSurvived) || !strings.Contains(err.Error(), "may still be running") {
		t.Fatalf("SwitchBranch() = %v", err)
	}
	if held := checkUnfinished(f.work); held == nil || !strings.Contains(held.Error(), "git checkout") {
		t.Fatalf("the checkout's path is not held: %v", held)
	}
	synced := item.Clone()
	SyncRepository(context.Background(), synced, nil, false)
	if synced.StatusMsg != "Busy" {
		t.Fatalf("sync on the held path: %s %q %v", synced.Status, synced.StatusMsg, synced.Logs)
	}
	details := GetRepoBranchDetails(context.Background(), f.work, "main")
	if !strings.Contains(details.BranchesErr, "has not finished") {
		t.Fatalf("a read started on the held path: %+v", details)
	}

	released = true
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for checkUnfinished(f.work) != nil {
		if time.Now().After(deadline) {
			t.Fatal("the path stayed held after the checkout was reaped")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A prune's worktree removal that the kernel never lets exit holds both the
// repository it runs in and the worktree it deletes until it is reaped. The
// worktree may itself be listed as a repository, so a sync, read or delete of
// it is refused meanwhile, not just one of the main checkout.
func TestUnreapableWorktreeRemovalHoldsTheWorktree(t *testing.T) {
	f := newSyncFixture(t)
	worktree := filepath.Join(t.TempDir(), "feature")
	runGit(t, f.work, "worktree", "add", "-b", "feature", worktree)
	worktree = registeredWorktreePath(t, f.work, "feature")
	withFakeGHDefaultBranch(t, func(string) (string, error) { return "main", nil })
	shortWaitDelay(t)
	originalReap := proc.ReapTimeout
	proc.ReapTimeout = 200 * time.Millisecond
	t.Cleanup(func() { proc.ReapTimeout = originalReap })

	started, release := make(chan struct{}), make(chan struct{})
	var startOnce, releaseOnce sync.Once
	restore := proc.BlockReaping(func(args []string) bool {
		if slices.Contains(args, "worktree") && slices.Contains(args, "remove") {
			startOnce.Do(func() { close(started) })
			return true
		}
		return false
	}, release, 100*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	var result PruneResult
	var err error
	go func() {
		defer close(finished)
		result, err = PruneBranchesAndWorktrees(ctx, &RepoItem{Name: "work", Path: f.work, DefaultBranch: "main"})
	}()
	// The prune reads proc's tunables until it returns, and the removal
	// until it is reaped, which is what frees the paths; wait for both before
	// restoring them.
	t.Cleanup(func() {
		cancel()
		releaseOnce.Do(func() { close(release) })
		<-finished
		deadline := time.Now().Add(5 * time.Second)
		for checkUnfinished(f.work) != nil || checkUnfinished(worktree) != nil {
			if time.Now().After(deadline) {
				t.Error("a path stayed held after the worktree removal was reaped")
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		restore()
	})

	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the prune never removed the worktree")
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("the prune waited for a worktree removal that could not be reaped")
	}
	if !errors.Is(err, context.Canceled) || len(result.RemovedWorktrees) != 0 ||
		!slices.ContainsFunc(result.Failures, func(failure string) bool { return strings.Contains(failure, proc.ErrSurvived.Error()) }) {
		t.Fatalf("PruneBranchesAndWorktrees() = %+v, %v", result, err)
	}
	for _, path := range []string{f.work, worktree} {
		if held := checkUnfinished(path); held == nil || !strings.Contains(held.Error(), "git worktree") {
			t.Fatalf("%s is not held: %v", path, held)
		}
	}
	synced := &RepoItem{Name: "feature", Path: worktree, CurrentBranch: "feature", DefaultBranch: "main"}
	SyncRepository(context.Background(), synced, nil, false)
	if synced.StatusMsg != "Busy" {
		t.Fatalf("sync on the held worktree: %s %q %v", synced.Status, synced.StatusMsg, synced.Logs)
	}
	if details := GetRepoBranchDetails(context.Background(), worktree, "main"); !strings.Contains(details.BranchesErr, "has not finished") {
		t.Fatalf("a read started on the held worktree: %+v", details)
	}
	if err := DeleteLocalRepo(context.Background(), filepath.Dir(worktree), worktree); !errors.Is(err, ErrUnfinishedChange) {
		t.Fatalf("DeleteLocalRepo() on the held worktree = %v", err)
	}

	releaseOnce.Do(func() { close(release) })
	deadline := time.Now().Add(5 * time.Second)
	for checkUnfinished(worktree) != nil {
		if time.Now().After(deadline) {
			t.Fatal("the worktree stayed held after its removal was reaped")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCommandPaths(t *testing.T) {
	root := t.TempDir()
	repo, worktree, dest := filepath.Join(root, "repo"), filepath.Join(root, "elsewhere", "wt"), filepath.Join(root, "dest")
	for _, tc := range []struct {
		args []string
		dir  string
		want []string
	}{
		{[]string{"git", "-C", repo, "status"}, "", []string{repo}},
		{[]string{"git", "-C", repo, "worktree", "remove", worktree}, "", []string{repo, worktree}},
		{[]string{"git", "-C", repo, "worktree", "remove", "--force", "wt"}, "", []string{repo, filepath.Join(repo, "wt")}},
		{[]string{"git", "worktree", "remove", worktree}, repo, []string{repo, worktree}},
		{[]string{"git", "clone", "https://example.invalid/r", dest}, "", []string{dest}},
		{[]string{"gh", "repo", "clone", "org/r", dest}, "", []string{dest}},
		{[]string{"gh", "api", "user"}, "", nil},
	} {
		cmd := exec.Command(tc.args[0], tc.args[1:]...)
		cmd.Dir = tc.dir
		if got := commandPaths(cmd); !slices.Equal(got, tc.want) {
			t.Errorf("commandPaths(%q) = %q, want %q", tc.args, got, tc.want)
		}
	}
}

// A gh clone that fails while a git it started keeps writing has that git
// stopped before the git fallback runs, so the fallback never races it on the
// same path.
func TestFailedGhCloneStopsItsChildBeforeFallback(t *testing.T) {
	wrapCommands(t, map[string]string{
		"gh": `mkdir -p "$4"
( while :; do echo x >> "$4/marker"; "$SLEEP" 0.02; done ) </dev/null >/dev/null 2>&1 &
while [ ! -s "$4/marker" ]; do "$SLEEP" 0.01; done
exit 1`,
		"git": `if [ "$1" = clone ]; then
  a=$(wc -c < "$3/marker"); "$SLEEP" 0.3; b=$(wc -c < "$3/marker")
  echo "$a $b" > "$3.fallback"; exit 1
fi
exec "$REAL_GIT" "$@"`,
	})
	path := filepath.Join(t.TempDir(), "fresh")
	item := &RepoItem{Name: "fresh", GHRepoName: "fresh", URL: "https://example.invalid/fresh.git", Path: path, IsNew: true}
	SyncRepository(context.Background(), item, nil, false)
	if item.Status != StatusError || !strings.Contains(item.Logs[len(item.Logs)-1], "Failed to clone") {
		t.Fatalf("sync status %s %q; logs: %v", item.Status, item.StatusMsg, item.Logs)
	}
	raw, err := os.ReadFile(path + ".fallback")
	if err != nil {
		t.Fatalf("the git fallback did not run: %v", err)
	}
	sizes := strings.Fields(string(raw))
	if len(sizes) != 2 || sizes[0] == "0" || sizes[0] != sizes[1] {
		t.Fatalf("the clone's child kept writing while the fallback ran: marker sizes %q", raw)
	}
	if err := checkUnfinished(path); err != nil {
		t.Fatalf("a fully stopped clone still holds its path: %v", err)
	}
}

// Cancelling CloneRepo's context stops the clone. gh runs in its own process
// group, so the caller's context is the only way a Ctrl+C reaches it.
func TestCloneRepoStopsWhenCancelled(t *testing.T) {
	wrapCommands(t, map[string]string{"gh": `exec "$SLEEP" 60`})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := CloneRepo(ctx, "org", "repo", filepath.Join(t.TempDir(), "repo"))
	if err == nil {
		t.Fatal("a cancelled clone reported success")
	}
	// The fake gh sleeps for 60s; returning well before that proves the
	// cancellation stopped it rather than the clone finishing on its own.
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("CloneRepo returned %s after its context ended", elapsed)
	}
}
