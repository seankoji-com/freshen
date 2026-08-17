package git

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeRunner is a test double for CommandRunner. fn receives the command
// name and args and returns the fake stdout/error for that invocation.
type fakeRunner struct {
	fn func(name string, args []string) ([]byte, error)
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	return f.fn(name, args)
}

// withFakeRunner installs a fakeRunner as the package runner for the
// duration of the test and restores the original CommandRunner on cleanup.
func withFakeRunner(t *testing.T, fn func(name string, args []string) ([]byte, error)) {
	t.Helper()
	orig := runner
	runner = &fakeRunner{fn: fn}
	t.Cleanup(func() { runner = orig })
}

// cmdKey builds a canonical lookup key for a command invocation.
func cmdKey(name string, args []string) string {
	return name + " " + strings.Join(args, " ")
}

type cmdResponse struct {
	out []byte
	err error
}

// withScriptedRunner installs a fakeRunner whose responses are looked up by
// cmdKey(name, args). Any unscripted invocation fails the test.
func withScriptedRunner(t *testing.T, responses map[string]cmdResponse) {
	t.Helper()
	withFakeRunner(t, func(name string, args []string) ([]byte, error) {
		key := cmdKey(name, args)
		resp, ok := responses[key]
		if !ok {
			t.Fatalf("unexpected command: %s", key)
		}
		return resp.out, resp.err
	})
}

// withFakeGHDefaultBranch swaps the `gh repo view` tier of GetDefaultBranch for
// the duration of the test and restores the real one on cleanup.
func withFakeGHDefaultBranch(t *testing.T, fn func(path string) (string, error)) {
	t.Helper()
	orig := ghDefaultBranch
	ghDefaultBranch = func(_ context.Context, path string) (string, error) { return fn(path) }
	t.Cleanup(func() { ghDefaultBranch = orig })
}

func TestFetchOrgRepos(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		want := []GHRepoInfo{
			{Name: "freshen", IsArchived: false, URL: "https://github.com/myorg/freshen", SSHURL: "git@github.com:myorg/freshen.git"},
			{Name: "archived-repo", IsArchived: true, URL: "https://github.com/myorg/archived-repo", SSHURL: "git@github.com:myorg/archived-repo.git"},
		}
		body, err := json.Marshal(want)
		if err != nil {
			t.Fatalf("marshal fixture: %v", err)
		}

		withFakeRunner(t, func(name string, args []string) ([]byte, error) {
			if name != "gh" {
				t.Fatalf("unexpected command name %q", name)
			}
			if len(args) < 3 || args[0] != "repo" || args[1] != "list" || args[2] != "myorg" {
				t.Fatalf("unexpected args %v", args)
			}
			return body, nil
		})

		got, err := FetchOrgRepos("myorg")
		if err != nil {
			t.Fatalf("FetchOrgRepos() error = %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("FetchOrgRepos() = %+v, want %+v", got, want)
		}
	})

	t.Run("runner error", func(t *testing.T) {
		withFakeRunner(t, func(name string, args []string) ([]byte, error) {
			return nil, errors.New("exit status 1: gh: command not found")
		})

		_, err := FetchOrgRepos("myorg")
		if err == nil {
			t.Fatal("FetchOrgRepos() error = nil, want an error")
		}
		if !strings.Contains(err.Error(), "failed to fetch gh repos for org myorg") {
			t.Errorf("FetchOrgRepos() error = %q, want it to mention org myorg", err.Error())
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		withFakeRunner(t, func(name string, args []string) ([]byte, error) {
			return []byte("not json"), nil
		})

		_, err := FetchOrgRepos("myorg")
		if err == nil {
			t.Fatal("FetchOrgRepos() error = nil, want a JSON parse error")
		}
		if !strings.Contains(err.Error(), "failed to parse gh JSON output") {
			t.Errorf("FetchOrgRepos() error = %q, want a JSON parse error", err.Error())
		}
	})
}

func TestFetchOrgRepoCounts(t *testing.T) {
	const fixture = `{
		"data": {
			"organization": {
				"repositories": {
					"nodes": [
						{"name": "freshen", "issues": {"totalCount": 3}, "pullRequests": {"totalCount": 1}},
						{"name": "careynas.net", "issues": {"totalCount": 0}, "pullRequests": {"totalCount": 2}}
					]
				}
			}
		}
	}`

	t.Run("success", func(t *testing.T) {
		withFakeRunner(t, func(name string, args []string) ([]byte, error) {
			if name != "gh" || len(args) < 2 || args[0] != "api" || args[1] != "graphql" {
				t.Fatalf("unexpected command: %s %v", name, args)
			}
			return []byte(fixture), nil
		})

		got, err := FetchOrgRepoCounts("myorg")
		if err != nil {
			t.Fatalf("FetchOrgRepoCounts() error = %v", err)
		}
		want := map[string]RepoCounts{
			"freshen":      {Issues: 3, PRs: 1},
			"careynas.net": {Issues: 0, PRs: 2},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("FetchOrgRepoCounts() = %+v, want %+v", got, want)
		}
	})

	t.Run("runner error", func(t *testing.T) {
		withFakeRunner(t, func(name string, args []string) ([]byte, error) {
			return nil, errors.New("exit status 1: bad credentials")
		})

		_, err := FetchOrgRepoCounts("myorg")
		if err == nil {
			t.Fatal("FetchOrgRepoCounts() error = nil, want an error")
		}
		if !strings.Contains(err.Error(), "gh api graphql failed for org myorg") {
			t.Errorf("FetchOrgRepoCounts() error = %q, want it to mention org myorg", err.Error())
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		withFakeRunner(t, func(name string, args []string) ([]byte, error) {
			return []byte("not json"), nil
		})

		_, err := FetchOrgRepoCounts("myorg")
		if err == nil {
			t.Fatal("FetchOrgRepoCounts() error = nil, want a JSON parse error")
		}
	})
}

func TestBranchWorktreeDetailsGetLocalBranches(t *testing.T) {
	tests := []struct {
		name string
		d    BranchWorktreeDetails
		want []string
	}{
		{
			name: "derives local branches from raw list, skipping remotes",
			d: BranchWorktreeDetails{
				Branches: []string{
					"* main",
					"  feature/foo",
					"+ feature/bar",
					"  remotes/origin/main",
					"  remotes/origin/HEAD -> origin/main",
				},
			},
			want: []string{"* main", "  feature/foo", "+ feature/bar"},
		},
		{
			name: "prefers precomputed LocalBranches when set",
			d: BranchWorktreeDetails{
				Branches:      []string{"* main", "  remotes/origin/main"},
				LocalBranches: []string{"precomputed"},
			},
			want: []string{"precomputed"},
		},
		{
			name: "empty branches yields nil",
			d:    BranchWorktreeDetails{},
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.d.GetLocalBranches()
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("GetLocalBranches() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestBranchWorktreeDetailsGetRemoteBranches(t *testing.T) {
	tests := []struct {
		name string
		d    BranchWorktreeDetails
		want []string
	}{
		{
			name: "derives remote branches from raw list, skipping locals",
			d: BranchWorktreeDetails{
				Branches: []string{
					"* main",
					"  feature/foo",
					"  remotes/origin/main",
					"  remotes/origin/feature/foo",
				},
			},
			want: []string{"  remotes/origin/main", "  remotes/origin/feature/foo"},
		},
		{
			name: "prefers precomputed RemoteBranches when set",
			d: BranchWorktreeDetails{
				Branches:       []string{"  remotes/origin/main"},
				RemoteBranches: []string{"precomputed"},
			},
			want: []string{"precomputed"},
		},
		{
			name: "no remotes yields nil",
			d: BranchWorktreeDetails{
				Branches: []string{"* main", "  feature/foo"},
			},
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.d.GetRemoteBranches()
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("GetRemoteBranches() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestGetDefaultBranch(t *testing.T) {
	const path = "/repo"

	// The gh repo view tier runs as a direct exec with cmd.Dir set (CommandRunner
	// cannot set Dir), so it never reaches the scripted runner. Each subtest fakes
	// it via withFakeGHDefaultBranch instead of relying on gh being absent.
	symbolicRefKey := cmdKey("git", []string{"-C", path, "symbolic-ref", "refs/remotes/origin/HEAD"})
	showRefMainKey := cmdKey("git", []string{"-C", path, "show-ref", "--verify", "--quiet", "refs/heads/main"})
	showRefMasterKey := cmdKey("git", []string{"-C", path, "show-ref", "--verify", "--quiet", "refs/heads/master"})
	symbolicHeadKey := cmdKey("git", []string{"-C", path, "symbolic-ref", "--short", "HEAD"})
	revParseKey := cmdKey("git", []string{"-C", path, "rev-parse", "--short", "HEAD"})

	t.Run("symbolic-ref to origin/HEAD succeeds", func(t *testing.T) {
		withScriptedRunner(t, map[string]cmdResponse{
			symbolicRefKey: {out: []byte("refs/remotes/origin/develop\n")},
		})

		if got := GetDefaultBranch(context.Background(), path); got != "develop" {
			t.Errorf("GetDefaultBranch() = %q, want %q", got, "develop")
		}
	})

	t.Run("falls back to gh repo view", func(t *testing.T) {
		withScriptedRunner(t, map[string]cmdResponse{
			symbolicRefKey: {err: errors.New("fatal: not a symbolic ref")},
		})
		var gotPath string
		withFakeGHDefaultBranch(t, func(p string) (string, error) {
			gotPath = p
			return "trunk\n", nil
		})

		if got := GetDefaultBranch(context.Background(), path); got != "trunk" {
			t.Errorf("GetDefaultBranch() = %q, want %q", got, "trunk")
		}
		if gotPath != path {
			t.Errorf("gh tier ran in %q, want %q", gotPath, path)
		}
	})

	t.Run("falls back to show-ref main", func(t *testing.T) {
		withScriptedRunner(t, map[string]cmdResponse{
			symbolicRefKey:   {err: errors.New("fatal: not a symbolic ref")},
			showRefMainKey:   {},
			showRefMasterKey: {err: errors.New("no such ref")},
		})
		withFakeGHDefaultBranch(t, func(string) (string, error) { return "", errors.New("gh unavailable") })

		if got := GetDefaultBranch(context.Background(), path); got != "main" {
			t.Errorf("GetDefaultBranch() = %q, want %q", got, "main")
		}
	})

	t.Run("falls back to show-ref master", func(t *testing.T) {
		withScriptedRunner(t, map[string]cmdResponse{
			symbolicRefKey:   {err: errors.New("fatal: not a symbolic ref")},
			showRefMainKey:   {err: errors.New("no such ref")},
			showRefMasterKey: {},
		})
		withFakeGHDefaultBranch(t, func(string) (string, error) { return "", errors.New("gh unavailable") })

		if got := GetDefaultBranch(context.Background(), path); got != "master" {
			t.Errorf("GetDefaultBranch() = %q, want %q", got, "master")
		}
	})

	t.Run("falls back to GetOriginalBranch when everything else fails", func(t *testing.T) {
		withScriptedRunner(t, map[string]cmdResponse{
			symbolicRefKey:   {err: errors.New("fatal: not a symbolic ref")},
			showRefMainKey:   {err: errors.New("no such ref")},
			showRefMasterKey: {err: errors.New("no such ref")},
			symbolicHeadKey:  {err: errors.New("fatal: not on a branch")},
			revParseKey:      {out: []byte("abc1234\n")},
		})
		withFakeGHDefaultBranch(t, func(string) (string, error) { return "", errors.New("gh unavailable") })

		if got := GetDefaultBranch(context.Background(), path); got != "abc1234" {
			t.Errorf("GetDefaultBranch() = %q, want %q", got, "abc1234")
		}
	})

	t.Run("returns HEAD when every lookup fails", func(t *testing.T) {
		withScriptedRunner(t, map[string]cmdResponse{
			symbolicRefKey:   {err: errors.New("fatal: not a symbolic ref")},
			showRefMainKey:   {err: errors.New("no such ref")},
			showRefMasterKey: {err: errors.New("no such ref")},
			symbolicHeadKey:  {err: errors.New("fatal: not on a branch")},
			revParseKey:      {err: errors.New("fatal: bad revision 'HEAD'")},
		})
		withFakeGHDefaultBranch(t, func(string) (string, error) { return "", errors.New("gh unavailable") })

		if got := GetDefaultBranch(context.Background(), path); got != "HEAD" {
			t.Errorf("GetDefaultBranch() = %q, want %q", got, "HEAD")
		}
	})
}

func TestGetOriginalBranch(t *testing.T) {
	const path = "/repo"

	symbolicHeadKey := cmdKey("git", []string{"-C", path, "symbolic-ref", "--short", "HEAD"})
	revParseKey := cmdKey("git", []string{"-C", path, "rev-parse", "--short", "HEAD"})

	t.Run("symbolic-ref succeeds", func(t *testing.T) {
		withScriptedRunner(t, map[string]cmdResponse{
			symbolicHeadKey: {out: []byte("feature/thing\n")},
		})

		if got := GetOriginalBranch(context.Background(), path); got != "feature/thing" {
			t.Errorf("GetOriginalBranch() = %q, want %q", got, "feature/thing")
		}
	})

	t.Run("falls back to rev-parse short hash on detached HEAD", func(t *testing.T) {
		withScriptedRunner(t, map[string]cmdResponse{
			symbolicHeadKey: {err: errors.New("fatal: not on a branch")},
			revParseKey:     {out: []byte("deadbee\n")},
		})

		if got := GetOriginalBranch(context.Background(), path); got != "deadbee" {
			t.Errorf("GetOriginalBranch() = %q, want %q", got, "deadbee")
		}
	})

	t.Run("returns HEAD when both lookups fail", func(t *testing.T) {
		withScriptedRunner(t, map[string]cmdResponse{
			symbolicHeadKey: {err: errors.New("fatal: not on a branch")},
			revParseKey:     {err: errors.New("fatal: bad revision 'HEAD'")},
		})

		if got := GetOriginalBranch(context.Background(), path); got != "HEAD" {
			t.Errorf("GetOriginalBranch() = %q, want %q", got, "HEAD")
		}
	})
}

func TestDeleteLocalRepo(t *testing.T) {
	t.Run("removes an existing directory tree", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "repo")
		if err := os.MkdirAll(filepath.Join(target, "sub"), 0o755); err != nil {
			t.Fatalf("setup MkdirAll: %v", err)
		}
		if err := os.WriteFile(filepath.Join(target, "sub", "file.txt"), []byte("x"), 0o644); err != nil {
			t.Fatalf("setup WriteFile: %v", err)
		}

		if err := DeleteLocalRepo(target); err != nil {
			t.Fatalf("DeleteLocalRepo() error = %v", err)
		}
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed, stat err = %v", target, err)
		}
	})

	t.Run("nonexistent path is not an error", func(t *testing.T) {
		// os.RemoveAll (and thus DeleteLocalRepo) treats a missing path as
		// success rather than surfacing ENOENT.
		dir := t.TempDir()
		missing := filepath.Join(dir, "does-not-exist")

		if err := DeleteLocalRepo(missing); err != nil {
			t.Errorf("DeleteLocalRepo(%s) error = %v, want nil", missing, err)
		}
	})

	t.Run("permission denied propagates as an error", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root: permission checks are bypassed")
		}

		dir := t.TempDir()
		target := filepath.Join(dir, "locked")
		if err := os.MkdirAll(filepath.Join(target, "sub"), 0o755); err != nil {
			t.Fatalf("setup MkdirAll: %v", err)
		}
		if err := os.WriteFile(filepath.Join(target, "sub", "file.txt"), []byte("x"), 0o644); err != nil {
			t.Fatalf("setup WriteFile: %v", err)
		}
		// Strip write permission on the parent so entries under it can't be
		// unlinked, forcing os.RemoveAll to fail with EACCES.
		if err := os.Chmod(target, 0o555); err != nil {
			t.Fatalf("setup Chmod: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(target, 0o755) })

		err := DeleteLocalRepo(target)
		if err == nil {
			t.Fatal("DeleteLocalRepo() error = nil, want a permission error")
		}
		if !errors.Is(err, os.ErrPermission) {
			t.Errorf("DeleteLocalRepo() error = %v, want an os.ErrPermission-wrapped error", err)
		}
	})
}

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

// withAliases installs user-supplied alias pairs for the duration of the test
// and restores the package alias maps on cleanup.
func withAliases(t *testing.T, pairs map[string]string) {
	t.Helper()
	origRemote, origLocal := aliasToRemote, aliasToLocal
	aliasToRemote = make(map[string]string)
	aliasToLocal = make(map[string]string)
	t.Cleanup(func() { aliasToRemote, aliasToLocal = origRemote, origLocal })
	for local, remote := range pairs {
		if err := AddAlias(local, remote); err != nil {
			t.Fatalf("AddAlias(%q, %q): %v", local, remote, err)
		}
	}
}

func TestGetLocalDirName(t *testing.T) {
	t.Run("built-in and pass-through names are returned unchanged", func(t *testing.T) {
		withAliases(t, nil)
		cases := map[string]string{
			".github":       "github",
			"careynas.net":  "wiki.robot.house",
			"freshen":       "freshen",
			"foo..bar":      "foo..bar", // dots are legal in repo names, not traversal
			"..evil":        "..evil",   // must NOT collide with "evil"
			".dotfiles":     ".dotfiles",
			"repo.with.dot": "repo.with.dot",
		}
		for in, want := range cases {
			got, ok := GetLocalDirName(in)
			if !ok {
				t.Errorf("GetLocalDirName(%q) rejected a safe name", in)
				continue
			}
			if got != want {
				t.Errorf("GetLocalDirName(%q) = %q, want %q", in, got, want)
			}
		}
	})

	t.Run("rejects names that are not a safe single segment", func(t *testing.T) {
		withAliases(t, nil)
		for _, in := range []string{"", ".", "..", "../..", "foo/..", "a/b", `a\b`, "/", "./foo", "foo/"} {
			got, ok := GetLocalDirName(in)
			if ok {
				t.Errorf("GetLocalDirName(%q) = %q, ok=true; want rejected", in, got)
			}
			if got != "" {
				t.Errorf("GetLocalDirName(%q) returned %q on rejection, want empty", in, got)
			}
		}
	})
}

func TestAddAlias(t *testing.T) {
	t.Run("valid pair round-trips through both directions", func(t *testing.T) {
		withAliases(t, map[string]string{"wiki": "careynas.net"})

		local, ok := GetLocalDirName("careynas.net")
		if !ok || local != "wiki" {
			t.Fatalf("GetLocalDirName(careynas.net) = %q, %v; want wiki, true", local, ok)
		}
		if remote := GetGHRepoName(local); remote != "careynas.net" {
			t.Errorf("GetGHRepoName(%q) = %q, want careynas.net", local, remote)
		}
	})

	t.Run("rejects unsafe halves instead of registering them", func(t *testing.T) {
		withAliases(t, nil)
		for _, pair := range [][2]string{
			{"..", "some-repo"},
			{".", "some-repo"},
			{"", "some-repo"},
			{"../etc", "some-repo"},
			{"a/b", "some-repo"},
			{"ok", ".."},
			{"ok", "a/b"},
			{"ok", ""},
		} {
			if err := AddAlias(pair[0], pair[1]); err == nil {
				t.Errorf("AddAlias(%q, %q) = nil, want error", pair[0], pair[1])
			}
		}
		if len(aliasToRemote) != 0 || len(aliasToLocal) != 0 {
			t.Errorf("rejected aliases were registered: %v / %v", aliasToRemote, aliasToLocal)
		}
		// A rejected alias must leave the built-in mapping intact, not shadow it.
		if got, ok := GetLocalDirName("some-repo"); !ok || got != "some-repo" {
			t.Errorf("GetLocalDirName(some-repo) = %q, %v; want some-repo, true", got, ok)
		}
	})
}
