package git

import (
	"context"
	"encoding/json"
	"errors"
	"os"
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

	symbolicRefKey := cmdKey("git", []string{"-C", path, "symbolic-ref", "refs/remotes/origin/HEAD"})
	ghViewKey := cmdKey("gh", []string{"repo", "view", path, "--json", "defaultBranchRef", "--jq", ".defaultBranchRef.name"})
	showRefMainKey := cmdKey("git", []string{"-C", path, "show-ref", "--verify", "--quiet", "refs/heads/main"})
	showRefMasterKey := cmdKey("git", []string{"-C", path, "show-ref", "--verify", "--quiet", "refs/heads/master"})
	symbolicHeadKey := cmdKey("git", []string{"-C", path, "symbolic-ref", "--short", "HEAD"})
	revParseKey := cmdKey("git", []string{"-C", path, "rev-parse", "--short", "HEAD"})

	t.Run("symbolic-ref to origin/HEAD succeeds", func(t *testing.T) {
		withScriptedRunner(t, map[string]cmdResponse{
			symbolicRefKey: {out: []byte("refs/remotes/origin/develop\n")},
		})

		if got := GetDefaultBranch(path); got != "develop" {
			t.Errorf("GetDefaultBranch() = %q, want %q", got, "develop")
		}
	})

	t.Run("falls back to gh repo view", func(t *testing.T) {
		withScriptedRunner(t, map[string]cmdResponse{
			symbolicRefKey: {err: errors.New("fatal: not a symbolic ref")},
			ghViewKey:      {out: []byte("trunk\n")},
		})

		if got := GetDefaultBranch(path); got != "trunk" {
			t.Errorf("GetDefaultBranch() = %q, want %q", got, "trunk")
		}
	})

	t.Run("falls back to show-ref main", func(t *testing.T) {
		withScriptedRunner(t, map[string]cmdResponse{
			symbolicRefKey:   {err: errors.New("fatal: not a symbolic ref")},
			ghViewKey:        {err: errors.New("gh: not found")},
			showRefMainKey:   {},
			showRefMasterKey: {err: errors.New("no such ref")},
		})

		if got := GetDefaultBranch(path); got != "main" {
			t.Errorf("GetDefaultBranch() = %q, want %q", got, "main")
		}
	})

	t.Run("falls back to show-ref master", func(t *testing.T) {
		withScriptedRunner(t, map[string]cmdResponse{
			symbolicRefKey:   {err: errors.New("fatal: not a symbolic ref")},
			ghViewKey:        {err: errors.New("gh: not found")},
			showRefMainKey:   {err: errors.New("no such ref")},
			showRefMasterKey: {},
		})

		if got := GetDefaultBranch(path); got != "master" {
			t.Errorf("GetDefaultBranch() = %q, want %q", got, "master")
		}
	})

	t.Run("falls back to GetOriginalBranch when everything else fails", func(t *testing.T) {
		withScriptedRunner(t, map[string]cmdResponse{
			symbolicRefKey:   {err: errors.New("fatal: not a symbolic ref")},
			ghViewKey:        {err: errors.New("gh: not found")},
			showRefMainKey:   {err: errors.New("no such ref")},
			showRefMasterKey: {err: errors.New("no such ref")},
			symbolicHeadKey:  {err: errors.New("fatal: not on a branch")},
			revParseKey:      {out: []byte("abc1234\n")},
		})

		if got := GetDefaultBranch(path); got != "abc1234" {
			t.Errorf("GetDefaultBranch() = %q, want %q", got, "abc1234")
		}
	})

	t.Run("returns HEAD when every lookup fails", func(t *testing.T) {
		withScriptedRunner(t, map[string]cmdResponse{
			symbolicRefKey:   {err: errors.New("fatal: not a symbolic ref")},
			ghViewKey:        {err: errors.New("gh: not found")},
			showRefMainKey:   {err: errors.New("no such ref")},
			showRefMasterKey: {err: errors.New("no such ref")},
			symbolicHeadKey:  {err: errors.New("fatal: not on a branch")},
			revParseKey:      {err: errors.New("fatal: bad revision 'HEAD'")},
		})

		if got := GetDefaultBranch(path); got != "HEAD" {
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

	t.Run("returns HEAD immediately when context is already cancelled", func(t *testing.T) {
		withFakeRunner(t, func(name string, args []string) ([]byte, error) {
			t.Fatalf("runner should not be called with a cancelled context, got: %s", cmdKey(name, args))
			return nil, nil
		})

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if got := GetOriginalBranch(ctx, path); got != "HEAD" {
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
