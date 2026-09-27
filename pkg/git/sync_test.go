package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// syncFixture is a working clone with a local bare "origin", so a sync can
// pull and fetch for real without any network.
type syncFixture struct {
	t      *testing.T
	origin string
	work   string
	peer   string // a second clone used to push upstream commits
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func configureClone(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "config", "user.email", "test@example.invalid")
	runGit(t, dir, "config", "user.name", "freshen test")
	runGit(t, dir, "config", "commit.gpgsign", "false")
}

func newSyncFixture(t *testing.T) *syncFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	// Windows runners default to core.autocrlf=true, which rewrites the
	// fixture files to CRLF on checkout. Pin it off for every git process
	// in the test, including the ones the sync code spawns.
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.autocrlf")
	t.Setenv("GIT_CONFIG_VALUE_0", "false")
	root := t.TempDir()
	f := &syncFixture{
		t:      t,
		origin: filepath.Join(root, "origin.git"),
		work:   filepath.Join(root, "work"),
		peer:   filepath.Join(root, "peer"),
	}
	runGit(t, root, "init", "--bare", "-b", "main", f.origin)
	runGit(t, root, "clone", f.origin, f.peer)
	configureClone(t, f.peer)
	f.commit(f.peer, "tracked.txt", "base\n", "initial")
	runGit(t, f.peer, "push", "origin", "main")
	runGit(t, root, "clone", f.origin, f.work)
	configureClone(t, f.work)
	return f
}

func (f *syncFixture) commit(dir, name, content, msg string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	runGit(f.t, dir, "add", name)
	runGit(f.t, dir, "commit", "-m", msg)
}

// pushUpstream lands a new commit on origin/main that the work clone lacks.
func (f *syncFixture) pushUpstream() {
	f.t.Helper()
	f.commit(f.peer, "upstream.txt", "new\n", "upstream change")
	runGit(f.t, f.peer, "push", "origin", "main")
}

func (f *syncFixture) stashCount() int {
	f.t.Helper()
	out := runGit(f.t, f.work, "stash", "list")
	if out == "" {
		return 0
	}
	return len(strings.Split(out, "\n"))
}

func (f *syncFixture) readWork(name string) string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.work, name))
	if err != nil {
		f.t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func (f *syncFixture) sync(safeOnly bool) *RepoItem {
	f.t.Helper()
	item := &RepoItem{Name: "work", Path: f.work, Logs: []string{}}
	SyncRepository(context.Background(), item, nil, safeOnly)
	return item
}

func TestSyncDirtyDefaultBranchDropsItsStash(t *testing.T) {
	f := newSyncFixture(t)
	f.pushUpstream()
	if err := os.WriteFile(filepath.Join(f.work, "tracked.txt"), []byte("local edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	item := f.sync(true)

	if item.Status != StatusStashedApplied {
		t.Fatalf("status = %s, want %s; logs:\n%s", item.Status, StatusStashedApplied, strings.Join(item.Logs, "\n"))
	}
	if got := f.readWork("tracked.txt"); got != "local edit\n" {
		t.Errorf("local edit lost: tracked.txt = %q", got)
	}
	if got := f.readWork("upstream.txt"); got != "new\n" {
		t.Errorf("upstream change not pulled: upstream.txt = %q", got)
	}
	if n := f.stashCount(); n != 0 {
		t.Errorf("sync left %d stash entries behind, want 0", n)
	}
	if item.Stashed {
		t.Error("item.Stashed should be false once the stash is restored")
	}
}

func TestSyncDirtyDefaultBranchRestoresChangesWhenPullFails(t *testing.T) {
	f := newSyncFixture(t)
	// A stash the user made themselves must survive untouched.
	if err := os.WriteFile(filepath.Join(f.work, "tracked.txt"), []byte("older work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, f.work, "stash", "push", "-m", "user stash")
	if err := os.WriteFile(filepath.Join(f.work, "tracked.txt"), []byte("local edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Point origin somewhere that does not exist so the pull fails, while
	// origin/HEAD still resolves the default branch locally.
	runGit(t, f.work, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))

	item := f.sync(true)

	if item.Status != StatusError || item.StatusMsg != "Pull Error" {
		t.Fatalf("status = %s/%s, want ERROR/Pull Error; logs:\n%s", item.Status, item.StatusMsg, strings.Join(item.Logs, "\n"))
	}
	if got := f.readWork("tracked.txt"); got != "local edit\n" {
		t.Errorf("local edit not restored after failed pull: tracked.txt = %q", got)
	}
	if n := f.stashCount(); n != 1 {
		t.Fatalf("stash entries = %d, want only the user's own", n)
	}
	if top := runGit(t, f.work, "stash", "list", "--format=%s"); !strings.Contains(top, "user stash") {
		t.Errorf("remaining stash = %q, want the user's stash", top)
	}
}

func TestSyncDirtyFeatureBranchRebasesWithAutostash(t *testing.T) {
	f := newSyncFixture(t)
	runGit(t, f.work, "checkout", "-b", "feature")
	f.commit(f.work, "feature.txt", "feature\n", "feature work")
	f.pushUpstream()
	if err := os.WriteFile(filepath.Join(f.work, "tracked.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	item := f.sync(false)

	if item.Status != StatusRebased {
		t.Fatalf("status = %s, want %s; logs:\n%s", item.Status, StatusRebased, strings.Join(item.Logs, "\n"))
	}
	if got := f.readWork("tracked.txt"); got != "wip\n" {
		t.Errorf("uncommitted work lost: tracked.txt = %q", got)
	}
	if got := f.readWork("upstream.txt"); got != "new\n" {
		t.Errorf("feature branch not rebased onto upstream: upstream.txt = %q", got)
	}
	if n := f.stashCount(); n != 0 {
		t.Errorf("autostash left %d stash entries behind", n)
	}
	if branch := runGit(t, f.work, "symbolic-ref", "--short", "HEAD"); branch != "feature" {
		t.Errorf("checkout moved to %q, want to stay on feature", branch)
	}
}

func TestSyncCleanFeatureBranchSurfacesTheSwitch(t *testing.T) {
	f := newSyncFixture(t)
	runGit(t, f.work, "checkout", "-b", "feature")

	item := f.sync(false)

	if item.Status != StatusSwitchedDefault {
		t.Fatalf("status = %s, want %s; logs:\n%s", item.Status, StatusSwitchedDefault, strings.Join(item.Logs, "\n"))
	}
	joined := strings.Join(item.Logs, "\n")
	if !strings.Contains(joined, "Switching the checkout to 'main'") || !strings.Contains(joined, "Switched from 'feature' to 'main'") {
		t.Errorf("branch switch not surfaced in logs:\n%s", joined)
	}
}

func TestDisableTerminalPromptsRespectsExplicitValue(t *testing.T) {
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	DisableTerminalPrompts()
	if got := os.Getenv("GIT_TERMINAL_PROMPT"); got != "1" {
		t.Errorf("GIT_TERMINAL_PROMPT = %q, want the explicit 1 kept", got)
	}

	t.Setenv("GIT_TERMINAL_PROMPT", "")
	if err := os.Unsetenv("GIT_TERMINAL_PROMPT"); err != nil {
		t.Fatal(err)
	}
	DisableTerminalPrompts()
	if got := os.Getenv("GIT_TERMINAL_PROMPT"); got != "0" {
		t.Errorf("GIT_TERMINAL_PROMPT = %q, want 0", got)
	}
}

// userStash leaves a stash entry the user made themselves on the work clone.
func (f *syncFixture) userStash(msg string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.work, "tracked.txt"), []byte("older work\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
	runGit(f.t, f.work, "stash", "push", "-m", msg)
}

func (f *syncFixture) stashSubjects() string {
	f.t.Helper()
	return runGit(f.t, f.work, "stash", "list", "--format=%gs")
}

func TestSyncDirtyDefaultBranchBacksOutConflictedMerge(t *testing.T) {
	f := newSyncFixture(t)
	f.userStash("user stash")
	// A local commit and an upstream commit both rewrite tracked.txt, so the
	// pull genuinely stops in a conflicted merge.
	f.commit(f.work, "tracked.txt", "local commit\n", "local change")
	head := runGit(t, f.work, "rev-parse", "HEAD")
	f.commit(f.peer, "tracked.txt", "upstream commit\n", "upstream change")
	runGit(t, f.peer, "push", "origin", "main")
	// The dirty state is an untracked file, which the auto-stash must carry.
	if err := os.WriteFile(filepath.Join(f.work, "wip.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	item := f.sync(true)

	if item.Status != StatusError || item.StatusMsg != "Pull Error" {
		t.Fatalf("status = %s/%s, want ERROR/Pull Error; logs:\n%s", item.Status, item.StatusMsg, strings.Join(item.Logs, "\n"))
	}
	if _, err := os.Stat(filepath.Join(f.work, ".git", "MERGE_HEAD")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("merge was not backed out: MERGE_HEAD stat err = %v", err)
	}
	if got := runGit(t, f.work, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved to %s, want %s", got, head)
	}
	if got := f.readWork("tracked.txt"); got != "local commit\n" {
		t.Errorf("tracked.txt = %q, want the committed local version", got)
	}
	if got := f.readWork("wip.txt"); got != "wip\n" {
		t.Errorf("untracked work not restored: wip.txt = %q", got)
	}
	if got := f.stashSubjects(); !strings.HasSuffix(got, ": user stash") || strings.Contains(got, "\n") {
		t.Errorf("stash list = %q, want only the user's own entry", got)
	}
	if item.Stashed {
		t.Error("item.Stashed should be false once the stash is restored")
	}
}

func TestRestoreStashTargetsItsOwnEntry(t *testing.T) {
	f := newSyncFixture(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(f.work, "tracked.txt"), []byte("ours\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, f.work, "stash", "push", "-m", "freshen auto-stash test")
	ours, err := findAutoStash(ctx, f.work, "freshen auto-stash test")
	if err != nil || ours == "" {
		t.Fatalf("findAutoStash = %q, %v", ours, err)
	}
	// The user stashes on top, so ours is no longer stash@{0}.
	if err := os.WriteFile(filepath.Join(f.work, "user.txt"), []byte("user\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, f.work, "stash", "push", "-u", "-m", "user stash")

	if err := restoreStash(ctx, f.work, ours); err != nil {
		t.Fatalf("restoreStash: %v", err)
	}
	if got := f.readWork("tracked.txt"); got != "ours\n" {
		t.Errorf("tracked.txt = %q, want our stashed change", got)
	}
	if _, err := os.Stat(filepath.Join(f.work, "user.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the user's stash was applied: user.txt stat err = %v", err)
	}
	if got := f.stashSubjects(); !strings.HasSuffix(got, ": user stash") || strings.Contains(got, "\n") {
		t.Errorf("stash list = %q, want only the user's own entry", got)
	}
}

func TestRestoreStashRefusesWhenItsEntryIsGone(t *testing.T) {
	f := newSyncFixture(t)
	f.userStash("user stash")
	missing := runGit(t, f.work, "rev-parse", "HEAD") // never a stash entry

	err := restoreStash(context.Background(), f.work, missing)

	if !errors.Is(err, errStashMissing) {
		t.Fatalf("restoreStash err = %v, want errStashMissing", err)
	}
	if got := f.readWork("tracked.txt"); got != "base\n" {
		t.Errorf("tree changed: tracked.txt = %q", got)
	}
	if n := f.stashCount(); n != 1 {
		t.Errorf("stash entries = %d, want the user's one untouched", n)
	}
}

func TestFindAutoStashMatchesOnlyItsMessage(t *testing.T) {
	f := newSyncFixture(t)
	ctx := context.Background()
	for i, msg := range []string{"freshen auto-stash A", "freshen auto-stash AB"} {
		if err := os.WriteFile(filepath.Join(f.work, "tracked.txt"), []byte(fmt.Sprintf("%d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
		runGit(t, f.work, "stash", "push", "-m", msg)
	}
	a, err := findAutoStash(ctx, f.work, "freshen auto-stash A")
	if err != nil {
		t.Fatal(err)
	}
	if want := runGit(t, f.work, "rev-parse", "stash@{1}"); a != want {
		t.Errorf("findAutoStash(A) = %s, want stash@{1} %s", a, want)
	}
	if none, err := findAutoStash(ctx, f.work, "freshen auto-stash C"); err != nil || none != "" {
		t.Errorf("findAutoStash(C) = %q, %v; want no match", none, err)
	}
}

func TestSyncDirtyFeatureBranchReportsAutostashConflict(t *testing.T) {
	f := newSyncFixture(t)
	runGit(t, f.work, "checkout", "-b", "feature")
	f.commit(f.work, "feature.txt", "feature\n", "feature work")
	f.commit(f.peer, "tracked.txt", "upstream\n", "upstream edit")
	runGit(t, f.peer, "push", "origin", "main")
	// Uncommitted work on the same file: the rebase itself succeeds, but
	// re-applying the autostash conflicts.
	if err := os.WriteFile(filepath.Join(f.work, "tracked.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	item := f.sync(false)

	if item.Status != StatusRebaseConflict {
		t.Fatalf("status = %s, want %s; logs:\n%s", item.Status, StatusRebaseConflict, strings.Join(item.Logs, "\n"))
	}
	if !item.Stashed {
		t.Error("item.Stashed = false, want true: the autostash is still in the stash list")
	}
	if n := f.stashCount(); n != 1 {
		t.Errorf("stash entries = %d, want the retained autostash", n)
	}
}

// TestHelperPrintTerminalPrompt is not a real test: the subprocess test below
// re-runs the test binary with it selected to report the inherited value.
func TestHelperPrintTerminalPrompt(t *testing.T) {
	if os.Getenv("FRESHEN_TEST_HELPER") != "1" {
		t.Skip("helper process only")
	}
	v, ok := os.LookupEnv("GIT_TERMINAL_PROMPT")
	fmt.Printf("GIT_TERMINAL_PROMPT=%q set=%v\n", v, ok)
}

// The package starts git with exec.CommandContext and no explicit Env, so a
// child inherits the process environment. Pin that the value
// DisableTerminalPrompts sets actually reaches a spawned process.
func TestDisableTerminalPromptsReachesSubprocesses(t *testing.T) {
	t.Setenv("GIT_TERMINAL_PROMPT", "")
	if err := os.Unsetenv("GIT_TERMINAL_PROMPT"); err != nil {
		t.Fatal(err)
	}
	DisableTerminalPrompts()
	t.Setenv("FRESHEN_TEST_HELPER", "1")

	cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestHelperPrintTerminalPrompt$")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper process: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), `GIT_TERMINAL_PROMPT="0" set=true`) {
		t.Errorf("subprocess did not inherit GIT_TERMINAL_PROMPT=0:\n%s", out)
	}
}
