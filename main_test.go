package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/seankoji-com/freshen/pkg/config"
	"github.com/seankoji-com/freshen/pkg/git"
)

func TestConfigConcurrency(t *testing.T) {
	if got := configConcurrency(7); got != 7 {
		t.Fatalf("configConcurrency(7) = %d, want 7", got)
	}
	if got := configConcurrency(0); got != 4 {
		t.Fatalf("configConcurrency(0) = %d, want 4", got)
	}
}

func TestApplyConfigAliases(t *testing.T) {
	if err := applyConfigAliases([]string{" config-local = config-remote "}); err != nil {
		t.Fatal(err)
	}
	if got, ok := git.GetLocalDirName("config-remote"); !ok || got != "config-local" {
		t.Fatalf("GetLocalDirName(config-remote) = %q, %v", got, ok)
	}
	for _, alias := range []string{"bad alias", "=remote", "local="} {
		if err := applyConfigAliases([]string{alias}); err == nil {
			t.Fatalf("applyConfigAliases accepted invalid alias %q", alias)
		}
	}
}

func TestDisplayVersion(t *testing.T) {
	cases := map[string]string{
		"0.2.0":        "v0.2.0", // GoReleaser {{.Version}}
		"v0.2.0":       "v0.2.0", // git describe
		"v0.2.0-3-gab": "v0.2.0-3-gab",
		"dev":          "dev",
		"ab12cd3":      "ab12cd3", // git describe --always with no tags
	}
	for in, want := range cases {
		if got := displayVersion(in); got != want {
			t.Errorf("displayVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

// batchFakes swaps runNonInteractive's git and GitHub calls for fakes that
// record what batch mode asked for, and returns those records.
type batchFakes struct {
	cloned, deleted, synced []string
}

func stubBatch(t *testing.T, repos []git.GHRepoInfo, clone func(ctx context.Context, name string) error) *batchFakes {
	t.Helper()
	f := &batchFakes{}
	origFetch, origClone, origDelete, origIsRepo, origSync := fetchOrgRepos, cloneRepo, deleteLocalRepo, isGitRepo, syncRepository
	t.Cleanup(func() {
		fetchOrgRepos, cloneRepo, deleteLocalRepo, isGitRepo, syncRepository = origFetch, origClone, origDelete, origIsRepo, origSync
	})
	fetchOrgRepos = func(context.Context, string) ([]git.GHRepoInfo, error) { return repos, nil }
	cloneRepo = func(ctx context.Context, _, name, _ string) error {
		f.cloned = append(f.cloned, name)
		if clone != nil {
			return clone(ctx, name)
		}
		return nil
	}
	deleteLocalRepo = func(_ context.Context, _, path string) error {
		f.deleted = append(f.deleted, filepath.Base(path))
		return nil
	}
	isGitRepo = func(string) bool { return true }
	syncRepository = func(_ context.Context, item *git.RepoItem, _ git.SyncProgress, _ bool) {
		f.synced = append(f.synced, item.Name)
		item.StatusMsg = "Up to date"
	}
	return f
}

// captureStdout returns what fn prints to stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	read := make(chan string)
	go func() {
		out, _ := io.ReadAll(r)
		read <- string(out)
	}()
	defer func() { os.Stdout = orig }()
	fn()
	_ = w.Close()
	return <-read
}

func mkdirs(t *testing.T, root string, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// Batch mode, like the TUI, never clones an archived repository that is
// missing locally, and never syncs one that is present. It deletes a present
// one only with --delete-archived.
func TestRunNonInteractiveSkipsArchivedRepos(t *testing.T) {
	repos := []git.GHRepoInfo{
		{Name: "active"},
		{Name: "archived-here", IsArchived: true},
		{Name: "archived-gone", IsArchived: true},
		{Name: "new-active"},
	}
	for _, deleteArchived := range []bool{false, true} {
		t.Run(fmt.Sprintf("delete-archived=%v", deleteArchived), func(t *testing.T) {
			dir := t.TempDir()
			mkdirs(t, dir, "active", "archived-here")
			f := stubBatch(t, repos, nil)

			captureStdout(t, func() { runNonInteractive(context.Background(), dir, "org", deleteArchived) })

			if !slices.Equal(f.cloned, []string{"new-active"}) {
				t.Errorf("cloned %v, want only [new-active]", f.cloned)
			}
			if !slices.Equal(f.synced, []string{"active"}) {
				t.Errorf("synced %v, want only [active]", f.synced)
			}
			wantDeleted := []string(nil)
			if deleteArchived {
				wantDeleted = []string{"archived-here"}
			}
			if !slices.Equal(f.deleted, wantDeleted) {
				t.Errorf("deleted %v, want %v", f.deleted, wantDeleted)
			}
		})
	}
}

// A failed clone is reported, and once the run is interrupted the clone in
// progress receives the cancellation and nothing further is cloned or synced.
func TestRunNonInteractiveReportsFailuresAndStopsWhenInterrupted(t *testing.T) {
	t.Run("clone failure", func(t *testing.T) {
		dir := t.TempDir()
		stubBatch(t, []git.GHRepoInfo{{Name: "broken"}}, func(context.Context, string) error {
			return errors.New("gh: repository not found")
		})
		out := captureStdout(t, func() { runNonInteractive(context.Background(), dir, "org", false) })
		if !strings.Contains(out, "failed to clone 'broken': gh: repository not found") {
			t.Fatalf("clone failure not reported; output:\n%s", out)
		}
	})

	t.Run("interrupted", func(t *testing.T) {
		dir := t.TempDir()
		mkdirs(t, dir, "local")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f := stubBatch(t, []git.GHRepoInfo{{Name: "first"}, {Name: "second"}, {Name: "local"}}, func(cloneCtx context.Context, name string) error {
			cancel() // Ctrl+C arrives while the first clone runs.
			if cloneCtx.Err() == nil {
				t.Errorf("clone of %s did not receive the run's cancellation", name)
			}
			return cloneCtx.Err()
		})
		out := captureStdout(t, func() { runNonInteractive(ctx, dir, "org", false) })
		if !slices.Equal(f.cloned, []string{"first"}) || len(f.synced) != 0 {
			t.Fatalf("after the interrupt: cloned %v, synced %v; want only [first] cloned", f.cloned, f.synced)
		}
		if !strings.Contains(out, "interrupted") {
			t.Fatalf("interrupted run not reported; output:\n%s", out)
		}
	})
}

// The first interactive run asks for a workspace, and that same session works
// in the workspace chosen, not in the default it offered.
func TestResolveWorkspaceUsesFirstRunChoice(t *testing.T) {
	orig := firstSetup
	t.Cleanup(func() { firstSetup = orig })
	chosen := filepath.Join(t.TempDir(), "code")
	prompted := 0
	firstSetup = func(defaultDir string, existing config.Config) (config.Config, error) {
		prompted++
		existing.Workspace = chosen
		return existing, nil
	}
	const def = "/home/someone/repos"

	dir, cfg, err := resolveWorkspace(config.Config{Owner: "octocat"}, def, def, true)
	if err != nil || prompted != 1 {
		t.Fatalf("first run: err %v, prompted %d times", err, prompted)
	}
	if dir != chosen || cfg.Workspace != chosen || cfg.Owner != "octocat" {
		t.Fatalf("first run works in %q with config %+v; want %q", dir, cfg, chosen)
	}

	// A configured workspace, an explicit -dir, or batch mode never prompts.
	for name, tc := range map[string]struct {
		cfg         config.Config
		dirFlag     string
		interactive bool
	}{
		"configured":   {config.Config{Workspace: "/w"}, def, true},
		"explicit dir": {config.Config{}, "/elsewhere", true},
		"batch mode":   {config.Config{}, def, false},
	} {
		dir, _, err := resolveWorkspace(tc.cfg, tc.dirFlag, def, tc.interactive)
		if err != nil || dir != tc.dirFlag || prompted != 1 {
			t.Errorf("%s: dir %q, err %v, prompted %d; want %q without a prompt", name, dir, err, prompted, tc.dirFlag)
		}
	}
}
