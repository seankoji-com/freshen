package git

import (
	"context"
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
