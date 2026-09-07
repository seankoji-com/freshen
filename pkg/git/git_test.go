package git

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeRunner is a test double for CommandRunner. fn receives the command
// name and args and returns the fake stdout/error for that invocation.
type fakeRunner struct {
	fn func(name string, args []string) ([]byte, error)
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	return f.fn(name, args)
}

type contextFakeRunner struct {
	fn func(ctx context.Context, name string, args []string) ([]byte, error)
}

func (f *contextFakeRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f.fn(ctx, name, args)
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

		got, err := FetchOrgRepos(context.Background(), "myorg")
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

		_, err := FetchOrgRepos(context.Background(), "myorg")
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

		_, err := FetchOrgRepos(context.Background(), "myorg")
		if err == nil {
			t.Fatal("FetchOrgRepos() error = nil, want a JSON parse error")
		}
		if !strings.Contains(err.Error(), "failed to parse gh JSON output") {
			t.Errorf("FetchOrgRepos() error = %q, want a JSON parse error", err.Error())
		}
	})

	t.Run("honors caller cancellation", func(t *testing.T) {
		orig := runner
		runner = &contextFakeRunner{fn: func(ctx context.Context, _ string, _ []string) ([]byte, error) {
			return nil, ctx.Err()
		}}
		t.Cleanup(func() { runner = orig })
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := FetchOrgRepos(ctx, "myorg")
		if err == nil || !strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("FetchOrgRepos() error = %v, want cancellation", err)
		}
	})
}

func TestFetchOrgRepoCounts(t *testing.T) {
	const fixture = `{
		"data": {
			"repositoryOwner": {
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
			for _, arg := range args {
				if strings.HasPrefix(arg, "query=") && strings.Contains(arg, "myorg") {
					t.Fatalf("owner was interpolated into GraphQL query: %s", arg)
				}
			}
			return []byte(fixture), nil
		})

		got, err := FetchOrgRepoCounts(context.Background(), "myorg")
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

	t.Run("paginates", func(t *testing.T) {
		calls := 0
		withFakeRunner(t, func(name string, args []string) ([]byte, error) {
			calls++
			if name != "gh" || len(args) < 2 || args[0] != "api" || args[1] != "graphql" {
				t.Fatalf("unexpected command: %s %v", name, args)
			}
			cursor := ""
			for _, arg := range args {
				if strings.HasPrefix(arg, "endCursor=") {
					cursor = strings.TrimPrefix(arg, "endCursor=")
				}
			}
			switch calls {
			case 1:
				if cursor != "" {
					t.Fatalf("first page sent cursor %q", cursor)
				}
				return []byte(`{"data":{"repositoryOwner":{"repositories":{"nodes":[{"name":"alpha","issues":{"totalCount":1},"pullRequests":{"totalCount":2}}],"pageInfo":{"hasNextPage":true,"endCursor":"cursor-1"}}}}}`), nil
			case 2:
				if cursor != "cursor-1" {
					t.Fatalf("second page sent cursor %q", cursor)
				}
				return []byte(`{"data":{"repositoryOwner":{"repositories":{"nodes":[{"name":"beta","issues":{"totalCount":3},"pullRequests":{"totalCount":4}}],"pageInfo":{"hasNextPage":false,"endCursor":"cursor-2"}}}}}`), nil
			default:
				t.Fatalf("unexpected page %d", calls)
				return nil, nil
			}
		})

		got, err := FetchOrgRepoCounts(context.Background(), "myorg")
		if err != nil {
			t.Fatalf("FetchOrgRepoCounts() error = %v", err)
		}
		want := map[string]RepoCounts{
			"alpha": {Issues: 1, PRs: 2},
			"beta":  {Issues: 3, PRs: 4},
		}
		if calls != 2 || !reflect.DeepEqual(got, want) {
			t.Fatalf("FetchOrgRepoCounts() calls = %d, result = %+v, want %+v", calls, got, want)
		}
	})

	t.Run("returns completed pages when a later page fails", func(t *testing.T) {
		calls := 0
		withFakeRunner(t, func(_ string, _ []string) ([]byte, error) {
			calls++
			if calls == 1 {
				return []byte(`{"data":{"repositoryOwner":{"repositories":{"nodes":[{"name":"alpha","issues":{"totalCount":1},"pullRequests":{"totalCount":2}}],"pageInfo":{"hasNextPage":true,"endCursor":"cursor-1"}}}}}`), nil
			}
			return nil, errors.New("context deadline exceeded")
		})

		got, err := FetchOrgRepoCounts(context.Background(), "myorg")
		if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
			t.Fatalf("FetchOrgRepoCounts() error = %v, want page failure", err)
		}
		want := map[string]RepoCounts{"alpha": {Issues: 1, PRs: 2}}
		if calls != 2 || !reflect.DeepEqual(got, want) {
			t.Fatalf("FetchOrgRepoCounts() calls = %d, partial result = %+v, want %+v", calls, got, want)
		}
	})

	t.Run("rejects null owner", func(t *testing.T) {
		withFakeRunner(t, func(_ string, _ []string) ([]byte, error) {
			return []byte(`{"data":{"repositoryOwner":null}}`), nil
		})

		got, err := FetchOrgRepoCounts(context.Background(), "myorg")
		if err == nil || !strings.Contains(err.Error(), "no repository owner") {
			t.Fatalf("FetchOrgRepoCounts() = %+v, %v, want owner error", got, err)
		}
	})

	t.Run("rejects empty intermediate page", func(t *testing.T) {
		withFakeRunner(t, func(_ string, _ []string) ([]byte, error) {
			return []byte(`{"data":{"repositoryOwner":{"repositories":{"nodes":[],"pageInfo":{"hasNextPage":true,"endCursor":"cursor-1"}}}}}`), nil
		})

		_, err := FetchOrgRepoCounts(context.Background(), "myorg")
		if err == nil || !strings.Contains(err.Error(), "empty repository page") {
			t.Fatalf("FetchOrgRepoCounts() error = %v, want empty-page error", err)
		}
	})

	t.Run("honors caller cancellation", func(t *testing.T) {
		orig := runner
		runner = &contextFakeRunner{fn: func(ctx context.Context, _ string, _ []string) ([]byte, error) {
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatalf("runner context error = %v, want canceled", ctx.Err())
			}
			return nil, ctx.Err()
		}}
		t.Cleanup(func() { runner = orig })
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := FetchOrgRepoCounts(ctx, "myorg")
		if err == nil || !strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("FetchOrgRepoCounts() error = %v, want cancellation", err)
		}
	})

	t.Run("runner error", func(t *testing.T) {
		withFakeRunner(t, func(name string, args []string) ([]byte, error) {
			return nil, errors.New("exit status 1: bad credentials")
		})

		_, err := FetchOrgRepoCounts(context.Background(), "myorg")
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

		_, err := FetchOrgRepoCounts(context.Background(), "myorg")
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

	t.Run("local lookup skips gh repo view", func(t *testing.T) {
		withScriptedRunner(t, map[string]cmdResponse{
			symbolicRefKey: {err: errors.New("fatal: not a symbolic ref")},
			showRefMainKey: {},
		})
		withFakeGHDefaultBranch(t, func(string) (string, error) {
			t.Fatal("GetDefaultBranchLocal called the networked gh fallback")
			return "", nil
		})

		if got := GetDefaultBranchLocal(context.Background(), path); got != "main" {
			t.Errorf("GetDefaultBranchLocal() = %q, want %q", got, "main")
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

func TestResolveDefaultBranchValidatesGitHubValue(t *testing.T) {
	const path = "/repo"

	t.Run("verified", func(t *testing.T) {
		withFakeGHDefaultBranch(t, func(string) (string, error) { return "develop\n", nil })

		branch, err := ResolveDefaultBranch(context.Background(), path)
		if err != nil || branch != "develop" {
			t.Fatalf("ResolveDefaultBranch() = %q, %v", branch, err)
		}
	})

	t.Run("rejects HEAD sentinel", func(t *testing.T) {
		withFakeGHDefaultBranch(t, func(string) (string, error) { return "HEAD", nil })
		withFakeRunner(t, func(name string, args []string) ([]byte, error) {
			t.Fatalf("invalid branch reached local validation: %s", cmdKey(name, args))
			return nil, nil
		})

		if _, err := ResolveDefaultBranch(context.Background(), path); err == nil {
			t.Fatal("ResolveDefaultBranch accepted HEAD")
		}
	})

	t.Run("does not confuse a missing local branch with GitHub resolution", func(t *testing.T) {
		withFakeGHDefaultBranch(t, func(string) (string, error) { return "develop", nil })

		if branch, err := ResolveDefaultBranch(context.Background(), path); err != nil || branch != "develop" {
			t.Fatalf("ResolveDefaultBranch() = %q, %v", branch, err)
		}
	})
}

func TestGHDefaultBranchUsesUpstreamWhenOriginIsAbsent(t *testing.T) {
	const path = "/repo"
	withScriptedRunner(t, map[string]cmdResponse{
		cmdKey("git", []string{"-C", path, "remote"}):                                                                        {out: []byte("upstream\n")},
		cmdKey("git", []string{"-C", path, "remote", "get-url", "upstream"}):                                                 {out: []byte("git@github.com:owner/repo.git\n")},
		cmdKey("gh", []string{"repo", "view", "owner/repo", "--json", "defaultBranchRef", "--jq", ".defaultBranchRef.name"}): {out: []byte("develop\n")},
	})

	branch, err := ghDefaultBranch(context.Background(), path)
	if err != nil || strings.TrimSpace(branch) != "develop" {
		t.Fatalf("ghDefaultBranch() = %q, %v", branch, err)
	}
}

func TestEnsureLocalDefaultBranchFetchesMissingRef(t *testing.T) {
	const path = "/repo"
	calls := 0
	withFakeRunner(t, func(name string, args []string) ([]byte, error) {
		calls++
		switch calls {
		case 1:
			return nil, errors.New("missing ref")
		case 2:
			if got := cmdKey(name, args); got != "git -C /repo fetch origin develop:refs/heads/develop" {
				t.Fatalf("unexpected fetch: %s", got)
			}
			return nil, nil
		case 3:
			return nil, nil
		default:
			t.Fatalf("unexpected command %d: %s", calls, cmdKey(name, args))
			return nil, nil
		}
	})

	if err := ensureLocalDefaultBranch(context.Background(), path, "develop"); err != nil || calls != 3 {
		t.Fatalf("ensureLocalDefaultBranch() = %v, calls=%d", err, calls)
	}
}

func TestGitHubRepoTargetAcceptsGitHubRemoteURLs(t *testing.T) {
	tests := map[string]string{
		"git@github.com:owner/repo.git":        "owner/repo",
		"https://github.com/owner/repo.git":    "owner/repo",
		"ssh://git@ghe.example/owner/repo.git": "ghe.example/owner/repo",
		"https://ghe.example/owner/repo.git":   "ghe.example/owner/repo",
	}
	for remote, want := range tests {
		got, err := githubRepoTarget(remote)
		if err != nil || got != want {
			t.Errorf("githubRepoTarget(%q) = %q, %v; want %q", remote, got, err, want)
		}
	}
	if _, err := githubRepoTarget("/local/repo"); err == nil {
		t.Fatal("githubRepoTarget accepted a local path")
	}
	if _, err := githubRepoTarget("owner/repo"); err == nil {
		t.Fatal("githubRepoTarget accepted a relative filesystem remote as a GitHub slug")
	}
}

func TestResolveDefaultBranchHonoursCallerDeadline(t *testing.T) {
	original := ghDefaultBranch
	ghDefaultBranch = func(ctx context.Context, _ string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}
	t.Cleanup(func() { ghDefaultBranch = original })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	started := time.Now()
	_, err := ResolveDefaultBranch(ctx, "/repo")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ResolveDefaultBranch() error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("ResolveDefaultBranch ignored its deadline for %v", elapsed)
	}
}

func TestPruneRejectsDefaultBranchMismatchBeforeMutation(t *testing.T) {
	const path = "/repo"
	withFakeGHDefaultBranch(t, func(string) (string, error) { return "main", nil })
	withScriptedRunner(t, map[string]cmdResponse{
		cmdKey("git", []string{"-C", path, "show-ref", "--verify", "--quiet", "refs/heads/main"}): {},
	})
	item := &RepoItem{Name: "repo", Path: path, DefaultBranch: "master", CurrentBranch: "feature"}

	_, err := PruneBranchesAndWorktrees(context.Background(), item)
	if err == nil || !strings.Contains(err.Error(), `changed from "master" to "main"`) {
		t.Fatalf("prune mismatch error = %v", err)
	}
}

func registeredWorktreePath(t *testing.T, repo, branch string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "worktree", "list", "--porcelain").CombinedOutput()
	if err != nil {
		t.Fatalf("list worktrees: %v\n%s", err, out)
	}
	var path string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "worktree ") {
			path = strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		}
		if strings.TrimSpace(line) == "branch refs/heads/"+branch {
			return path
		}
	}
	t.Fatalf("worktree for branch %q not found in:\n%s", branch, out)
	return ""
}

func TestPruneRefusesDirtySecondaryWorktreeBeforeRemoval(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-b", "main", repo)
	run("-C", repo, "config", "user.name", "Freshen Test")
	run("-C", repo, "config", "user.email", "freshen@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("-C", repo, "add", "README.md")
	run("-C", repo, "commit", "-m", "fixture")
	run("-C", repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	run("-C", repo, "branch", "feature")
	worktree := filepath.Join(t.TempDir(), "feature")
	run("-C", repo, "worktree", "add", worktree, "feature")
	dirtyFile := filepath.Join(worktree, "dirty.txt")
	if err := os.WriteFile(dirtyFile, []byte("do not delete\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	canonicalWorktree := registeredWorktreePath(t, repo, "feature")
	withFakeGHDefaultBranch(t, func(string) (string, error) { return "main", nil })

	result, err := PruneBranchesAndWorktrees(context.Background(), &RepoItem{Name: "repo", Path: repo, DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("dirty-worktree prune failed instead of skipping changed worktree: %v", err)
	}
	if !reflect.DeepEqual(result.DirtyWorktrees, []string{canonicalWorktree}) || !reflect.DeepEqual(result.ProtectedBranches, []string{"feature"}) || len(result.RemovedWorktrees) != 0 || len(result.DeletedBranches) != 0 {
		t.Fatalf("dirty-worktree prune did not skip and protect its branch: %+v", result)
	}
	if _, err := os.Stat(dirtyFile); err != nil {
		t.Fatalf("dirty worktree was removed: %v", err)
	}
}

func TestPruneRemovesWorktreeContainingOnlyIgnoredFiles(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-b", "main", repo)
	run("-C", repo, "config", "user.name", "Freshen Test")
	run("-C", repo, "config", "user.email", "freshen@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("dist/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("-C", repo, "add", ".gitignore")
	run("-C", repo, "commit", "-m", "fixture")
	run("-C", repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	run("-C", repo, "branch", "feature")
	worktree := filepath.Join(t.TempDir(), "feature")
	run("-C", repo, "worktree", "add", worktree, "feature")
	if err := os.MkdirAll(filepath.Join(worktree, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "dist", "bundle.js"), []byte("ignored\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	canonicalWorktree := registeredWorktreePath(t, repo, "feature")
	withFakeGHDefaultBranch(t, func(string) (string, error) { return "main", nil })

	result, err := PruneBranchesAndWorktrees(context.Background(), &RepoItem{Name: "repo", Path: repo, DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("ignored-only worktree prune failed: %v", err)
	}
	if !reflect.DeepEqual(result.RemovedWorktrees, []string{canonicalWorktree}) || !reflect.DeepEqual(result.DeletedBranches, []string{"feature"}) || len(result.DirtyWorktrees) != 0 {
		t.Fatalf("ignored-only worktree was not removed: %+v", result)
	}
}

func TestPruneSkipsDirtyWorktreeAndContinuesWithCleanWorktree(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-b", "main", repo)
	run("-C", repo, "config", "user.name", "Freshen Test")
	run("-C", repo, "config", "user.email", "freshen@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("-C", repo, "add", "README.md")
	run("-C", repo, "commit", "-m", "fixture")
	run("-C", repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	run("-C", repo, "branch", "clean")
	run("-C", repo, "branch", "dirty")
	worktreeRoot := t.TempDir()
	cleanWorktree := filepath.Join(worktreeRoot, "clean")
	dirtyWorktree := filepath.Join(worktreeRoot, "dirty")
	run("-C", repo, "worktree", "add", cleanWorktree, "clean")
	run("-C", repo, "worktree", "add", dirtyWorktree, "dirty")
	if err := os.WriteFile(filepath.Join(dirtyWorktree, "keep.txt"), []byte("do not delete\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	canonicalClean := registeredWorktreePath(t, repo, "clean")
	canonicalDirty := registeredWorktreePath(t, repo, "dirty")
	withFakeGHDefaultBranch(t, func(string) (string, error) { return "main", nil })

	result, err := PruneBranchesAndWorktrees(context.Background(), &RepoItem{Name: "repo", Path: repo, DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("mixed-worktree prune failed: %v", err)
	}
	if !reflect.DeepEqual(result.DirtyWorktrees, []string{canonicalDirty}) || !reflect.DeepEqual(result.RemovedWorktrees, []string{canonicalClean}) || !reflect.DeepEqual(result.ProtectedBranches, []string{"dirty"}) || !reflect.DeepEqual(result.DeletedBranches, []string{"clean"}) {
		t.Fatalf("mixed-worktree prune audit is incomplete: %+v", result)
	}
	if _, err := os.Stat(cleanWorktree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clean worktree still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dirtyWorktree, "keep.txt")); err != nil {
		t.Fatalf("dirty worktree was damaged: %v", err)
	}
}

func TestPruneReportsRemovedCleanWorktreeAndBranch(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-b", "main", repo)
	run("-C", repo, "config", "user.name", "Freshen Test")
	run("-C", repo, "config", "user.email", "freshen@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("-C", repo, "add", "README.md")
	run("-C", repo, "commit", "-m", "fixture")
	run("-C", repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	run("-C", repo, "branch", "feature")
	worktree := filepath.Join(t.TempDir(), "feature")
	run("-C", repo, "worktree", "add", worktree, "feature")
	canonicalWorktree := registeredWorktreePath(t, repo, "feature")
	withFakeGHDefaultBranch(t, func(string) (string, error) { return "main", nil })

	result, err := PruneBranchesAndWorktrees(context.Background(), &RepoItem{Name: "repo", Path: repo, DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("clean prune failed: %v", err)
	}
	if !reflect.DeepEqual(result.RemovedWorktrees, []string{canonicalWorktree}) || len(result.DirtyWorktrees) != 0 || !reflect.DeepEqual(result.DeletedBranches, []string{"feature"}) {
		t.Fatalf("clean prune audit is incomplete: %+v", result)
	}
	if _, err := os.Stat(worktree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clean worktree still exists: %v", err)
	}
}

func TestPruneProtectsTemporarilyUnavailableWorktreeBranch(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-b", "main", repo)
	run("-C", repo, "config", "user.name", "Freshen Test")
	run("-C", repo, "config", "user.email", "freshen@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("-C", repo, "add", "README.md")
	run("-C", repo, "commit", "-m", "fixture")
	run("-C", repo, "branch", "feature")
	worktree := filepath.Join(t.TempDir(), "feature")
	run("-C", repo, "worktree", "add", worktree, "feature")
	canonicalWorktree := registeredWorktreePath(t, repo, "feature")
	movedWorktree := worktree + "-temporarily-unavailable"
	if err := os.Rename(worktree, movedWorktree); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Rename(movedWorktree, worktree) })
	withFakeGHDefaultBranch(t, func(string) (string, error) { return "main", nil })

	result, err := PruneBranchesAndWorktrees(context.Background(), &RepoItem{Name: "repo", Path: repo, DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("unavailable-worktree prune failed: %v", err)
	}
	if !reflect.DeepEqual(result.UnavailableWorktrees, []string{canonicalWorktree}) || !reflect.DeepEqual(result.ProtectedBranches, []string{"feature"}) || len(result.DeletedBranches) != 0 {
		t.Fatalf("unavailable worktree branch was not protected: %+v", result)
	}
	if _, err := os.Stat(movedWorktree); err != nil {
		t.Fatalf("temporarily unavailable worktree was damaged: %v", err)
	}
}

func TestPruneRemovesExpiredWorktreeRegistration(t *testing.T) {
	originalPruneExpire := worktreePruneExpire
	worktreePruneExpire = "now"
	t.Cleanup(func() { worktreePruneExpire = originalPruneExpire })
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-b", "main", repo)
	run("-C", repo, "config", "user.name", "Freshen Test")
	run("-C", repo, "config", "user.email", "freshen@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("-C", repo, "add", "README.md")
	run("-C", repo, "commit", "-m", "fixture")
	run("-C", repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	run("-C", repo, "branch", "feature")
	worktree := filepath.Join(t.TempDir(), "feature")
	run("-C", repo, "worktree", "add", worktree, "feature")
	canonicalWorktree := registeredWorktreePath(t, repo, "feature")
	if err := os.RemoveAll(worktree); err != nil {
		t.Fatal(err)
	}
	withFakeGHDefaultBranch(t, func(string) (string, error) { return "main", nil })

	result, err := PruneBranchesAndWorktrees(context.Background(), &RepoItem{Name: "repo", Path: repo, DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("expired-worktree prune failed: %v", err)
	}
	if !reflect.DeepEqual(result.PrunedWorktrees, []string{canonicalWorktree}) || !reflect.DeepEqual(result.DeletedBranches, []string{"feature"}) || len(result.UnavailableWorktrees) != 0 {
		t.Fatalf("expired registration was not pruned: %+v", result)
	}
}

func TestPruneKeepsCleanBranchWithLocalOnlyCommits(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-b", "main", repo)
	run("-C", repo, "config", "user.name", "Freshen Test")
	run("-C", repo, "config", "user.email", "freshen@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("-C", repo, "add", "README.md")
	run("-C", repo, "commit", "-m", "fixture")
	run("-C", repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	run("-C", repo, "branch", "feature")
	worktree := filepath.Join(t.TempDir(), "feature")
	run("-C", repo, "worktree", "add", worktree, "feature")
	if err := os.WriteFile(filepath.Join(worktree, "feature.txt"), []byte("local-only commit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("-C", worktree, "add", "feature.txt")
	run("-C", worktree, "commit", "-m", "local-only")
	withFakeGHDefaultBranch(t, func(string) (string, error) { return "main", nil })

	result, err := PruneBranchesAndWorktrees(context.Background(), &RepoItem{Name: "repo", Path: repo, DefaultBranch: "main"})
	if err != nil {
		t.Fatalf("unpushed-branch prune failed: %v", err)
	}
	if !reflect.DeepEqual(result.UnpushedBranches, []string{"feature"}) || len(result.DeletedBranches) != 0 || len(result.RemovedWorktrees) != 1 {
		t.Fatalf("local-only branch was not retained: %+v", result)
	}
	if out, err := exec.Command("git", "-C", repo, "show-ref", "--verify", "refs/heads/feature").CombinedOutput(); err != nil {
		t.Fatalf("local-only branch was deleted: %v\n%s", err, out)
	}
}

func TestDestructiveActionsRejectUnverifiedDefaultBeforeMutation(t *testing.T) {
	withFakeGHDefaultBranch(t, func(string) (string, error) { return "HEAD", nil })
	withFakeRunner(t, func(name string, args []string) ([]byte, error) {
		t.Fatalf("unverified default branch reached git mutation: %s", cmdKey(name, args))
		return nil, nil
	})

	item := &RepoItem{Name: "repo", Path: "/repo", OriginalBranch: "feature", DefaultBranch: "HEAD"}
	if _, err := PruneBranchesAndWorktrees(context.Background(), item); err == nil {
		t.Fatal("prune accepted an unverified default branch")
	}
	if err := CommitPushPRAndSwitchDefault(context.Background(), item); err == nil {
		t.Fatal("push accepted an unverified default branch")
	}
	if len(item.Logs) != 0 {
		t.Fatalf("rejected operations mutated logs: %v", item.Logs)
	}
}

func TestCommitPushRejectsDefaultBranchDriftBeforeMutation(t *testing.T) {
	withFakeGHDefaultBranch(t, func(string) (string, error) { return "main", nil })
	withFakeRunner(t, func(name string, args []string) ([]byte, error) {
		t.Fatalf("default-branch drift reached mutation: %s", cmdKey(name, args))
		return nil, nil
	})

	item := &RepoItem{Name: "repo", Path: "/repo", OriginalBranch: "feature", DefaultBranch: "master"}
	err := CommitPushPRAndSwitchDefault(context.Background(), item)
	if err == nil || !strings.Contains(err.Error(), `changed from "master" to "main"`) {
		t.Fatalf("push drift error = %v", err)
	}
	if len(item.Logs) != 0 {
		t.Fatalf("rejected publishing mutated logs: %v", item.Logs)
	}
}

func TestScanLocalDirectoryContextDoesNotJoinBlockedScan(t *testing.T) {
	const targetDir = "/blocked/local-scan"
	directoryScans.Lock()
	directoryScans.active[targetDir] = &directoryScan{done: make(chan struct{}), started: time.Now()}
	directoryScans.Unlock()
	t.Cleanup(func() {
		directoryScans.Lock()
		delete(directoryScans.active, targetDir)
		delete(directoryScans.abandoned, targetDir)
		directoryScans.Unlock()
	})

	started := time.Now()
	_, err := ScanLocalDirectoryContext(context.Background(), targetDir)
	if !errors.Is(err, ErrDirectoryScanInProgress) {
		t.Fatalf("ScanLocalDirectoryContext() error = %v, want ErrDirectoryScanInProgress", err)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("blocked scan retry waited %v instead of returning immediately", elapsed)
	}
}

func TestScanLocalDirectoryContextEscalatesStuckScan(t *testing.T) {
	const targetDir = "/stuck/local-scan"
	directoryScans.Lock()
	directoryScans.active[targetDir] = &directoryScan{done: make(chan struct{}), started: time.Now().Add(-directoryScanStuckAfter - time.Second)}
	directoryScans.Unlock()
	t.Cleanup(func() {
		directoryScans.Lock()
		delete(directoryScans.active, targetDir)
		delete(directoryScans.abandoned, targetDir)
		directoryScans.Unlock()
	})

	_, err := ScanLocalDirectoryContext(context.Background(), targetDir)
	if !errors.Is(err, ErrDirectoryScanStuck) {
		t.Fatalf("ScanLocalDirectoryContext() error = %v, want ErrDirectoryScanStuck", err)
	}
	directoryScans.Lock()
	_, stillActive := directoryScans.active[targetDir]
	directoryScans.Unlock()
	if stillActive {
		t.Fatal("stuck scan still blocked future refreshes")
	}
}

func TestScanLocalDirectoryContextCapsAbandonedScans(t *testing.T) {
	const targetDir = "/repeatedly-stuck/local-scan"
	directoryScans.Lock()
	directoryScans.abandoned[targetDir] = maxAbandonedDirectoryScans - 1
	directoryScans.active[targetDir] = &directoryScan{done: make(chan struct{}), started: time.Now().Add(-directoryScanStuckAfter - time.Second)}
	directoryScans.Unlock()
	t.Cleanup(func() {
		directoryScans.Lock()
		delete(directoryScans.active, targetDir)
		delete(directoryScans.abandoned, targetDir)
		directoryScans.Unlock()
	})

	_, err := ScanLocalDirectoryContext(context.Background(), targetDir)
	if !errors.Is(err, ErrDirectoryScanUnresponsive) {
		t.Fatalf("repeated stuck scan error = %v, want ErrDirectoryScanUnresponsive", err)
	}
	_, err = ScanLocalDirectoryContext(context.Background(), targetDir)
	if !errors.Is(err, ErrDirectoryScanUnresponsive) {
		t.Fatalf("capped scan started new work: %v", err)
	}
}

func TestSuccessfulAbandonedScanClearsFailureCap(t *testing.T) {
	const targetDir = "/recovered/local-scan"
	scan := &directoryScan{done: make(chan struct{}), started: time.Now()}
	directoryScans.Lock()
	directoryScans.abandoned[targetDir] = maxAbandonedDirectoryScans
	directoryScans.Unlock()
	t.Cleanup(func() {
		directoryScans.Lock()
		delete(directoryScans.active, targetDir)
		delete(directoryScans.abandoned, targetDir)
		directoryScans.Unlock()
	})

	finishDirectoryScan(targetDir, scan, []string{"repo"}, nil)
	directoryScans.Lock()
	_, capped := directoryScans.abandoned[targetDir]
	directoryScans.Unlock()
	if capped {
		t.Fatal("successful abandoned scan did not clear failure cap")
	}
}

func TestResetLocalDirectoryScanFailuresAllowsExplicitRetry(t *testing.T) {
	const targetDir = "/manually-recovered/local-scan"
	directoryScans.Lock()
	directoryScans.abandoned[targetDir] = maxAbandonedDirectoryScans
	directoryScans.Unlock()
	t.Cleanup(func() {
		directoryScans.Lock()
		delete(directoryScans.active, targetDir)
		delete(directoryScans.abandoned, targetDir)
		directoryScans.Unlock()
	})

	ResetLocalDirectoryScanFailures(targetDir)
	directoryScans.Lock()
	_, capped := directoryScans.abandoned[targetDir]
	directoryScans.Unlock()
	if capped {
		t.Fatal("explicit retry did not clear abandoned-scan cap")
	}
}

func TestScanLocalDirectoryContextDoesNotReuseCompletedScan(t *testing.T) {
	targetDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(targetDir, "first"), 0o755); err != nil {
		t.Fatal(err)
	}
	first, err := ScanLocalDirectoryContext(context.Background(), targetDir)
	if err != nil || !reflect.DeepEqual(first, []string{"first"}) {
		t.Fatalf("first scan = %v, %v", first, err)
	}
	if err := os.Mkdir(filepath.Join(targetDir, "second"), 0o755); err != nil {
		t.Fatal(err)
	}
	second, err := ScanLocalDirectoryContext(context.Background(), targetDir)
	if err != nil || !reflect.DeepEqual(second, []string{"first", "second"}) {
		t.Fatalf("second scan reused stale results: %v, %v", second, err)
	}
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
	t.Run("rejects an outside path", func(t *testing.T) {
		workspace := t.TempDir()
		outside := t.TempDir()
		if err := DeleteLocalRepo(workspace, outside); err == nil {
			t.Fatal("DeleteLocalRepo() error = nil, want containment error")
		}
	})

	t.Run("rejects a symlink escape", func(t *testing.T) {
		workspace := t.TempDir()
		outside := t.TempDir()
		link := filepath.Join(workspace, "linked-repo")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := DeleteLocalRepo(workspace, link); err == nil {
			t.Fatal("DeleteLocalRepo() accepted a symlink outside the workspace")
		}
	})

	t.Run("removes an existing directory tree", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "repo")
		if err := os.MkdirAll(filepath.Join(target, "sub"), 0o755); err != nil {
			t.Fatalf("setup MkdirAll: %v", err)
		}
		if err := os.WriteFile(filepath.Join(target, "sub", "file.txt"), []byte("x"), 0o644); err != nil {
			t.Fatalf("setup WriteFile: %v", err)
		}

		if err := DeleteLocalRepo(dir, target); err != nil {
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

		if err := DeleteLocalRepo(dir, missing); err != nil {
			t.Errorf("DeleteLocalRepo(%s) error = %v, want nil", missing, err)
		}
	})

	t.Run("permission denied propagates as an error", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root: permission checks are bypassed")
		}
		if runtime.GOOS == "windows" {
			t.Skip("Windows does not enforce directory-unlink permissions via Chmod")
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

		err := DeleteLocalRepo(dir, target)
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
	}, false)
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

	SyncRepository(context.Background(), item, nil, false)

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
	SyncRepository(ctx, item, func(*RepoItem) { published++ }, false)

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

	SyncRepository(context.Background(), item, nil, false)

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

	SyncRepository(context.Background(), item, nil, false)

	if item.Status != StatusError {
		t.Fatalf("expected status ERROR when no target URL/name, got %s", item.Status)
	}
	if item.StatusMsg != "Not Found" {
		t.Errorf("expected StatusMsg 'Not Found', got %q", item.StatusMsg)
	}
}

// safeOnly must never touch what's checked out: a feature-branch repo is
// left exactly as it found it — no checkout, no stash, no rebase — whether
// it's clean or dirty.
func TestSyncRepositorySafeOnlySkipsFeatureBranch(t *testing.T) {
	dir := newTestRepo(t)
	checkout := exec.Command("git", "-C", dir, "checkout", "-b", "feature/x")
	if out, err := checkout.CombinedOutput(); err != nil {
		t.Skipf("git checkout unavailable in this environment: %v (%s)", err, out)
	}
	dirtyFile := filepath.Join(dir, "dirty.txt")
	if err := os.WriteFile(dirtyFile, []byte("uncommitted"), 0o644); err != nil {
		t.Fatalf("failed to write dirty file: %v", err)
	}

	item := &RepoItem{Name: "probe", Path: dir, Logs: []string{}}
	SyncRepository(context.Background(), item, nil, true)

	if item.Status != StatusSkipped {
		t.Fatalf("expected StatusSkipped for a feature-branch repo under safeOnly, got %s (msg: %s)", item.Status, item.StatusMsg)
	}
	if item.CurrentBranch != "feature/x" {
		t.Errorf("safeOnly must not switch branches; expected 'feature/x', got %q", item.CurrentBranch)
	}
	if _, err := os.Stat(dirtyFile); err != nil {
		t.Errorf("expected the dirty file to remain in the working tree untouched, stat failed: %v", err)
	}
}

// safeOnly's restriction only applies to feature branches — a repo already
// on its default branch must still sync exactly as before, stash included.
func TestSyncRepositorySafeOnlyStillSyncsDefaultBranch(t *testing.T) {
	dir := newTestRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("uncommitted"), 0o644); err != nil {
		t.Fatalf("failed to write dirty file: %v", err)
	}

	item := &RepoItem{Name: "probe", Path: dir, Logs: []string{}}
	SyncRepository(context.Background(), item, nil, true)

	if item.Status == StatusSkipped {
		t.Fatalf("safeOnly must still sync a repo already on its default branch, got StatusSkipped (msg: %s)", item.StatusMsg)
	}
	if item.CurrentBranch != item.DefaultBranch {
		t.Errorf("expected to remain on the default branch, got %q (default %q)", item.CurrentBranch, item.DefaultBranch)
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
