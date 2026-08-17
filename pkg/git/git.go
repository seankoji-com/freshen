package git

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/seankoji-com/freshen/pkg/jobs"
)

// CommandRunner abstracts external command execution so it can be faked in tests.
// Implementations return stdout on success; on failure the returned error should
// include any stderr output so callers get diagnostic detail without managing buffers.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// execRunner is the production CommandRunner backed by exec.CommandContext.
type execRunner struct{}

func (e *execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		if stderr := strings.TrimSpace(errOut.String()); stderr != "" {
			return out.Bytes(), fmt.Errorf("%w: %s", err, stderr)
		}
		return out.Bytes(), err
	}
	return out.Bytes(), nil
}

// runner is the package-level CommandRunner; tests swap it for a fake.
var runner CommandRunner = &execRunner{}

// API pagination limits and timeouts
const (
	defaultRepoLimit   = 1000
	graphQLRepoLimit   = 100
	defaultIssueLimit  = 50
	defaultPRLimit     = 50
	fetchTimeout       = 6 * time.Second
	rebaseAbortTimeout = 10 * time.Second
)

type RepoStatus string

const (
	StatusPending         RepoStatus = "PENDING"
	StatusSyncing         RepoStatus = "SYNCING"
	StatusUpToDate        RepoStatus = "UP_TO_DATE"
	StatusUpdated         RepoStatus = "UPDATED"
	StatusStashedApplied  RepoStatus = "STASHED_APPLIED"
	StatusSwitchedDefault RepoStatus = "SWITCHED_DEFAULT"
	StatusRebased         RepoStatus = "REBASED"
	StatusRebaseConflict  RepoStatus = "REBASE_CONFLICT"
	StatusPRCreated       RepoStatus = "PR_CREATED"
	StatusCloned          RepoStatus = "CLONED"
	StatusArchived        RepoStatus = "ARCHIVED"
	StatusError           RepoStatus = "ERROR"
)

type GHRepoInfo struct {
	Name       string `json:"name"`
	IsArchived bool   `json:"isArchived"`
	URL        string `json:"url"`
	SSHURL     string `json:"sshUrl"`
}

type RepoCounts struct {
	Issues int
	PRs    int
}

type IssueItem struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
}

type PRItem struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	HeadRefName string `json:"headRefName"`
	URL         string `json:"url"`
}

type BranchWorktreeDetails struct {
	Branches       []string
	LocalBranches  []string
	RemoteBranches []string
	Worktrees      []string
	ChangedFiles   []string
}

// cleanBranchName trims whitespace and strips one leading * or + marker with or without a following space.
func cleanBranchName(s string) string {
	clean := strings.TrimSpace(s)
	// Strip one leading * or + with or without a following space
	if strings.HasPrefix(clean, "* ") {
		clean = strings.TrimPrefix(clean, "* ")
	} else if strings.HasPrefix(clean, "+ ") {
		clean = strings.TrimPrefix(clean, "+ ")
	} else if strings.HasPrefix(clean, "*") {
		clean = strings.TrimPrefix(clean, "*")
	} else if strings.HasPrefix(clean, "+") {
		clean = strings.TrimPrefix(clean, "+")
	}
	clean = strings.TrimSpace(clean)
	return clean
}

// GetLocalBranches returns local branch entries.
func (d BranchWorktreeDetails) GetLocalBranches() []string {
	if len(d.LocalBranches) > 0 {
		return d.LocalBranches
	}
	var local []string
	for _, b := range d.Branches {
		clean := cleanBranchName(b)
		if !strings.HasPrefix(clean, "remotes/") {
			local = append(local, b)
		}
	}
	return local
}

// GetRemoteBranches returns remote tracking branch entries.
func (d BranchWorktreeDetails) GetRemoteBranches() []string {
	if len(d.RemoteBranches) > 0 {
		return d.RemoteBranches
	}
	var remote []string
	for _, b := range d.Branches {
		clean := cleanBranchName(b)
		if strings.HasPrefix(clean, "remotes/") {
			remote = append(remote, b)
		}
	}
	return remote
}

type GraphQLOrgResponse struct {
	Data struct {
		Organization struct {
			Repositories struct {
				Nodes []struct {
					Name   string `json:"name"`
					Issues struct {
						TotalCount int `json:"totalCount"`
					} `json:"issues"`
					PullRequests struct {
						TotalCount int `json:"totalCount"`
					} `json:"pullRequests"`
				} `json:"nodes"`
			} `json:"repositories"`
		} `json:"organization"`
	} `json:"data"`
}

type RepoItem struct {
	Name               string
	GHRepoName         string
	Path               string
	URL                string
	IsArchived         bool
	IsNew              bool
	CurrentBranch      string
	OriginalBranch     string
	DefaultBranch      string
	HasUnstagedChanges bool
	ExistingPRURL      string
	OpenIssuesCount    int
	OpenPRsCount       int
	IssuesList         []IssueItem
	PRsList            []PRItem
	HasLoadedIssues    bool
	HasLoadedPRs       bool
	IsLoadingIssues    bool
	IsLoadingPRs       bool
	HasLoadedCounts    bool
	BranchDetails      BranchWorktreeDetails
	Status             RepoStatus
	StatusMsg          string
	Stashed            bool
	DraftPRURL         string
	Logs               []string
	ErrorErr           error
}

// ShortenHomePath replaces user home directory prefix with ~.
func ShortenHomePath(path string) string {
	home, err := os.UserHomeDir()
	if err == nil && strings.HasPrefix(path, home) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

// GetLocalDirName maps GitHub repository name to local folder alias.
// The case statements contain user-specific aliases (e.g., .github -> github, careynas.net -> wiki.robot.house).
func GetLocalDirName(ghRepo string) string {
	switch ghRepo {
	case ".github":
		return "github"
	case "careynas.net":
		return "wiki.robot.house"
	default:
		return ghRepo
	}
}

// GetGHRepoName maps local folder alias to GitHub repository name.
// The case statements contain user-specific aliases (e.g., github -> .github, wiki.robot.house -> careynas.net).
func GetGHRepoName(localDir string) string {
	switch localDir {
	case "github":
		return ".github"
	case "wiki.robot.house":
		return "careynas.net"
	default:
		return localDir
	}
}

// FetchOrgRepos queries GitHub CLI for all repositories in the specified organization.
func FetchOrgRepos(org string) ([]GHRepoInfo, error) {
	out, err := runner.Run(context.Background(), "gh", "repo", "list", org, "--limit", fmt.Sprintf("%d", defaultRepoLimit), "--json", "name,isArchived,url,sshUrl")
	if err != nil {
		return nil, fmt.Errorf("failed to fetch gh repos for org %s: %w", org, err)
	}

	var repos []GHRepoInfo
	if err := json.Unmarshal(out, &repos); err != nil {
		return nil, fmt.Errorf("failed to parse gh JSON output: %w", err)
	}

	return repos, nil
}

// FetchOrgRepoCounts queries GraphQL API for open issue and PR counts per repo.
func FetchOrgRepoCounts(org string) (map[string]RepoCounts, error) {
	query := fmt.Sprintf(`query { organization(login: "%s") { repositories(first: %d) { nodes { name issues(states: OPEN) { totalCount } pullRequests(states: OPEN) { totalCount } } } } }`, org, graphQLRepoLimit)
	out, err := runner.Run(context.Background(), "gh", "api", "graphql", "-f", fmt.Sprintf("query=%s", query))
	if err != nil {
		return nil, fmt.Errorf("gh api graphql failed for org %s: %w", org, err)
	}

	var resp GraphQLOrgResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, err
	}

	result := make(map[string]RepoCounts)
	for _, node := range resp.Data.Organization.Repositories.Nodes {
		result[node.Name] = RepoCounts{
			Issues: node.Issues.TotalCount,
			PRs:    node.PullRequests.TotalCount,
		}
	}
	return result, nil
}

// GetRepoBranchDetails fetches branches, worktrees, and changed file details.
func GetRepoBranchDetails(ctx context.Context, path, defaultBranch string) BranchWorktreeDetails {
	var details BranchWorktreeDetails

	if ctx.Err() != nil {
		return details
	}

	// Branches
	cmd := exec.CommandContext(ctx, "git", "-C", path, "branch", "-a")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err == nil {
		for _, line := range strings.Split(out.String(), "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				details.Branches = append(details.Branches, trimmed)

				clean := cleanBranchName(trimmed)

				if strings.HasPrefix(clean, "remotes/") {
					details.RemoteBranches = append(details.RemoteBranches, trimmed)
				} else {
					details.LocalBranches = append(details.LocalBranches, trimmed)
				}
			}
		}
	}

	if ctx.Err() != nil {
		return details
	}

	// Worktrees
	cmd = exec.CommandContext(ctx, "git", "-C", path, "worktree", "list")
	out.Reset()
	cmd.Stdout = &out
	if err := cmd.Run(); err == nil {
		for _, line := range strings.Split(out.String(), "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				details.Worktrees = append(details.Worktrees, trimmed)
			}
		}
	}

	if ctx.Err() != nil {
		return details
	}

	// Changed files & status
	cmd = exec.CommandContext(ctx, "git", "-C", path, "status", "--short")
	out.Reset()
	cmd.Stdout = &out
	if err := cmd.Run(); err == nil {
		for _, line := range strings.Split(out.String(), "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				details.ChangedFiles = append(details.ChangedFiles, trimmed)
			}
		}
	}

	return details
}

// PruneBranchesAndWorktrees fetches & prunes remote tracking branches, removes secondary worktrees, and deletes non-default local branches.
func PruneBranchesAndWorktrees(ctx context.Context, path, defaultBranch string) (int, error) {
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}

	// 1. Fetch & prune deleted remote-tracking references from origin
	// Best-effort: a failure here shouldn't block local branch/worktree cleanup.
	if err := exec.CommandContext(ctx, "git", "-C", path, "fetch", "--prune", "origin").Run(); err != nil {
		slog.Warn("git fetch --prune failed", "path", path, "error", err)
	}

	if ctx.Err() != nil {
		return 0, ctx.Err()
	}

	// 2. Force remove secondary git worktrees
	cmdWorktree := exec.CommandContext(ctx, "git", "-C", path, "worktree", "list", "--porcelain")
	var wtOut bytes.Buffer
	cmdWorktree.Stdout = &wtOut
	if err := cmdWorktree.Run(); err == nil {
		lines := strings.Split(wtOut.String(), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "worktree ") {
				wtPath := strings.TrimPrefix(line, "worktree ")
				wtPath = strings.TrimSpace(wtPath)
				if wtPath != "" && wtPath != path {
					if err := exec.CommandContext(ctx, "git", "-C", path, "worktree", "remove", "--force", wtPath).Run(); err != nil {
						slog.Warn("git worktree remove failed", "worktreePath", wtPath, "error", err)
					}
				}
			}
		}
	}
	if err := exec.CommandContext(ctx, "git", "-C", path, "worktree", "prune").Run(); err != nil {
		slog.Warn("git worktree prune failed", "path", path, "error", err)
	}

	if ctx.Err() != nil {
		return 0, ctx.Err()
	}

	// 3. Delete local non-default branches
	cmd := exec.CommandContext(ctx, "git", "-C", path, "branch", "--format=%(refname:short)")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return 0, err
	}

	currentBranch := GetOriginalBranch(ctx, path)
	deletedCount := 0
	for _, b := range strings.Split(out.String(), "\n") {
		b = cleanBranchName(b)
		if b != "" && b != defaultBranch && b != currentBranch {
			delCmd := exec.CommandContext(ctx, "git", "-C", path, "branch", "-D", b)
			if delCmd.Run() == nil {
				deletedCount++
			}
		}
	}
	return deletedCount, nil
}

// FetchOpenIssuesList retrieves open GitHub issues with a 6-second timeout context.
func FetchOpenIssuesList(org, ghRepo string) ([]IssueItem, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()

	target := fmt.Sprintf("%s/%s", org, ghRepo)
	cmd := exec.CommandContext(ctx, "gh", "issue", "list", "--repo", target, "--state", "open", "--limit", fmt.Sprintf("%d", defaultIssueLimit), "--json", "number,title,url")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}

	var issues []IssueItem
	if err := json.Unmarshal(out.Bytes(), &issues); err != nil {
		return nil, err
	}
	if issues == nil {
		issues = []IssueItem{}
	}
	for i := range issues {
		issues[i].Title = jobs.SanitizeTerminal(issues[i].Title)
	}
	return issues, nil
}

// FetchOpenPRsList retrieves open GitHub pull requests with a 6-second timeout context.
func FetchOpenPRsList(org, ghRepo string) ([]PRItem, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()

	target := fmt.Sprintf("%s/%s", org, ghRepo)
	cmd := exec.CommandContext(ctx, "gh", "pr", "list", "--repo", target, "--state", "open", "--limit", fmt.Sprintf("%d", defaultPRLimit), "--json", "number,title,headRefName,url")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}

	var prs []PRItem
	if err := json.Unmarshal(out.Bytes(), &prs); err != nil {
		return nil, err
	}
	if prs == nil {
		prs = []PRItem{}
	}
	for i := range prs {
		prs[i].Title = jobs.SanitizeTerminal(prs[i].Title)
		prs[i].HeadRefName = jobs.SanitizeTerminal(prs[i].HeadRefName)
	}
	return prs, nil
}

// FetchExistingPRURL checks if an open PR exists on GitHub for the given branch.
func FetchExistingPRURL(repoPath, branch string) string {
	if branch == "" || branch == "HEAD" {
		return ""
	}
	cmd := exec.Command("gh", "pr", "list", "--head", branch, "--state", "open", "--json", "url", "--jq", ".[0].url")
	cmd.Dir = repoPath
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err == nil {
		return strings.TrimSpace(out.String())
	}
	return ""
}

// IsGitRepo checks if a directory is a valid git working tree.
func IsGitRepo(path string) bool {
	cmd := exec.Command("git", "-C", path, "rev-parse", "--is-inside-work-tree")
	return cmd.Run() == nil
}

// GetOriginalBranch gets current checked out branch name or HEAD commit short hash.
func GetOriginalBranch(ctx context.Context, path string) string {
	if ctx.Err() != nil {
		return "HEAD"
	}

	out, err := runner.Run(ctx, "git", "-C", path, "symbolic-ref", "--short", "HEAD")
	if err == nil && strings.TrimSpace(string(out)) != "" {
		return strings.TrimSpace(string(out))
	}

	if ctx.Err() != nil {
		return "HEAD"
	}

	out, err = runner.Run(ctx, "git", "-C", path, "rev-parse", "--short", "HEAD")
	if err == nil && strings.TrimSpace(string(out)) != "" {
		return strings.TrimSpace(string(out))
	}

	return "HEAD"
}

// GetDefaultBranch determines default branch (main/master) for a git repository.
func GetDefaultBranch(path string) string {
	out, err := runner.Run(context.Background(), "git", "-C", path, "symbolic-ref", "refs/remotes/origin/HEAD")
	if err == nil {
		ref := strings.TrimSpace(string(out))
		ref = strings.TrimPrefix(ref, "refs/remotes/origin/")
		ref = strings.TrimPrefix(ref, "origin/")
		if ref != "" {
			return ref
		}
	}

	out, err = runner.Run(context.Background(), "gh", "repo", "view", path, "--json", "defaultBranchRef", "--jq", ".defaultBranchRef.name")
	if err == nil && strings.TrimSpace(string(out)) != "" {
		return strings.TrimSpace(string(out))
	}

	if _, err := runner.Run(context.Background(), "git", "-C", path, "show-ref", "--verify", "--quiet", "refs/heads/main"); err == nil {
		return "main"
	}
	if _, err := runner.Run(context.Background(), "git", "-C", path, "show-ref", "--verify", "--quiet", "refs/heads/master"); err == nil {
		return "master"
	}

	return GetOriginalBranch(context.Background(), path)
}

// SyncProgress receives a snapshot of a repository after each state change
// during a sync. Each snapshot is owned by the receiver — the sync never writes
// to it again — so it can be read from another goroutine safely.
type SyncProgress func(*RepoItem)

// Clone returns a deep copy of the item, safe to hand to another goroutine.
// Slice fields are copied rather than shared, so appends on either side stay
// invisible to the other.
func (r *RepoItem) Clone() *RepoItem {
	if r == nil {
		return nil
	}
	c := *r
	c.Logs = append([]string(nil), r.Logs...)
	c.IssuesList = append([]IssueItem(nil), r.IssuesList...)
	c.PRsList = append([]PRItem(nil), r.PRsList...)
	c.BranchDetails = r.BranchDetails.clone()
	return &c
}

func (d BranchWorktreeDetails) clone() BranchWorktreeDetails {
	return BranchWorktreeDetails{
		Branches:       append([]string(nil), d.Branches...),
		LocalBranches:  append([]string(nil), d.LocalBranches...),
		RemoteBranches: append([]string(nil), d.RemoteBranches...),
		Worktrees:      append([]string(nil), d.Worktrees...),
		ChangedFiles:   append([]string(nil), d.ChangedFiles...),
	}
}

// syncSession pairs the repository being synced with the progress emitter, so
// every state change is published as an owned snapshot rather than left for a
// concurrent reader to catch mid-write.
type syncSession struct {
	item *RepoItem
	emit SyncProgress
}

// log appends a line to the repository log and publishes a snapshot.
func (s *syncSession) log(format string, args ...any) {
	s.item.Logs = append(s.item.Logs, fmt.Sprintf(format, args...))
	s.publish()
}

// finish records a terminal status alongside its log line, in one snapshot.
func (s *syncSession) finish(status RepoStatus, statusMsg, format string, args ...any) {
	s.item.Status = status
	s.item.StatusMsg = statusMsg
	s.log(format, args...)
}

func (s *syncSession) publish() {
	if s.emit != nil {
		s.emit(s.item.Clone())
	}
}

// SyncRepository performs the exact branch workflow with brief status messages.
// ctx allows the caller to abort in-flight git operations (e.g. on app quit).
//
// item is mutated in place. When emit is non-nil it receives an owned snapshot
// after every state change, which is how the TUI follows progress without
// reading the struct this function is writing to.
func SyncRepository(ctx context.Context, item *RepoItem, emit SyncProgress) {
	if ctx.Err() != nil {
		return
	}
	s := &syncSession{item: item, emit: emit}

	item.Status = StatusSyncing
	s.log("[%s] 󰓦 Starting sync for %s", time.Now().Format("15:04:05"), item.Name)

	origBranch := GetOriginalBranch(ctx, item.Path)
	item.OriginalBranch = origBranch
	item.CurrentBranch = origBranch
	defaultBranch := GetDefaultBranch(item.Path)
	item.DefaultBranch = defaultBranch

	if !IsGitRepo(item.Path) {
		target := item.URL
		if target == "" && item.GHRepoName != "" {
			target = item.GHRepoName
		}
		if target == "" {
			s.finish(StatusError, "Not Found", "󰅙 '%s' is not a local git repository and has no remote repository target.", item.Path)
			return
		}

		s.log("↳ Repository not found locally. Cloning %s into %s...", target, item.Path)
		if err := os.MkdirAll(filepath.Dir(item.Path), 0o755); err != nil {
			s.finish(StatusError, "Clone Err", "󰅙 Failed to create directory: %v", err)
			return
		}

		cloneCmd := exec.CommandContext(ctx, "gh", "repo", "clone", target, item.Path)
		var cloneOut bytes.Buffer
		cloneCmd.Stdout = &cloneOut
		cloneCmd.Stderr = &cloneOut
		if err := cloneCmd.Run(); err != nil {
			if item.URL != "" {
				gitCloneCmd := exec.CommandContext(ctx, "git", "clone", item.URL, item.Path)
				var gitCloneOut bytes.Buffer
				gitCloneCmd.Stdout = &gitCloneOut
				gitCloneCmd.Stderr = &gitCloneOut
				if gitErr := gitCloneCmd.Run(); gitErr != nil {
					s.finish(StatusError, "Clone Err", "󰅙 Failed to clone '%s': %s", target, strings.TrimSpace(cloneOut.String()+"\n"+gitCloneOut.String()))
					return
				}
			} else {
				s.finish(StatusError, "Clone Err", "󰅙 Failed to clone '%s': %s", target, strings.TrimSpace(cloneOut.String()))
				return
			}
		}

		item.IsNew = false
		origBranch = GetOriginalBranch(ctx, item.Path)
		defaultBranch = GetDefaultBranch(item.Path)
		item.OriginalBranch = origBranch
		item.CurrentBranch = origBranch
		item.DefaultBranch = defaultBranch
		item.BranchDetails = GetRepoBranchDetails(ctx, item.Path, defaultBranch)
		s.finish(StatusCloned, "Cloned", "󰄬 Successfully cloned into '%s' (%s).", item.Path, defaultBranch)
		return
	}

	item.BranchDetails = GetRepoBranchDetails(ctx, item.Path, defaultBranch)

	if origBranch != defaultBranch {
		item.ExistingPRURL = FetchExistingPRURL(item.Path, origBranch)
		if item.ExistingPRURL != "" {
			s.log("󰏫 Existing Open PR found: %s", item.ExistingPRURL)
		}
	}

	isDirtyCmd := exec.CommandContext(ctx, "git", "-C", item.Path, "status", "--porcelain")
	var dirtyOut bytes.Buffer
	isDirtyCmd.Stdout = &dirtyOut
	_ = isDirtyCmd.Run()
	hasUnstagedChanges := strings.TrimSpace(dirtyOut.String()) != ""
	item.HasUnstagedChanges = hasUnstagedChanges

	s.log(" Branch: %s | Default: %s | Unstaged: %v", origBranch, defaultBranch, hasUnstagedChanges)

	if origBranch == defaultBranch {
		if !hasUnstagedChanges {
			s.log(" On default branch '%s' (clean). Running git pull --no-rebase origin %s...", defaultBranch, defaultBranch)
			pullCmd := exec.CommandContext(ctx, "git", "-C", item.Path, "pull", "--no-rebase", "origin", defaultBranch)
			var pullOut bytes.Buffer
			pullCmd.Stdout = &pullOut
			pullCmd.Stderr = &pullOut
			if err := pullCmd.Run(); err == nil {
				if strings.Contains(pullOut.String(), "Already up to date.") {
					s.finish(StatusUpToDate, "OK", "󰄬 Successfully pulled '%s'.", defaultBranch)
				} else {
					s.finish(StatusUpdated, "Updated", "󰄬 Successfully pulled '%s'.", defaultBranch)
				}
			} else {
				s.finish(StatusError, "Pull Error", "󰅙 git pull error: %s", pullOut.String())
			}
			return
		}

		s.log(" On default branch '%s' (dirty). Executing git add . && git stash && git pull --no-rebase origin %s && git stash apply...", defaultBranch, defaultBranch)

		_ = exec.CommandContext(ctx, "git", "-C", item.Path, "add", ".").Run()
		stashMsg := fmt.Sprintf("freshen auto-stash %s", time.Now().Format("2006-01-02 15:04:05"))
		stashCmd := exec.CommandContext(ctx, "git", "-C", item.Path, "stash", "push", "-m", stashMsg)
		if err := stashCmd.Run(); err != nil {
			s.finish(StatusError, "Stash Err", "󰅙 Failed to stash local changes: %v", err)
			return
		}
		item.Stashed = true

		if ctx.Err() != nil {
			s.log("󰅙 Sync cancelled after stashing — changes remain stashed, re-run sync to restore them.")
			return
		}

		pullCmd := exec.CommandContext(ctx, "git", "-C", item.Path, "pull", "--no-rebase", "origin", defaultBranch)
		var dirtyPullOut bytes.Buffer
		pullCmd.Stdout = &dirtyPullOut
		pullCmd.Stderr = &dirtyPullOut
		if err := pullCmd.Run(); err != nil {
			s.log("󰅙 git pull error (continuing with stash apply): %s — %s", err.Error(), dirtyPullOut.String())
		}

		applyCmd := exec.CommandContext(ctx, "git", "-C", item.Path, "stash", "apply")
		if err := applyCmd.Run(); err == nil {
			s.finish(StatusStashedApplied, "Stashed", "󰄬 Successfully pulled '%s' and re-applied stashed changes.", defaultBranch)
		} else {
			s.finish(StatusError, "Conflict", "󰅙 Conflict occurred while applying stash!")
		}
		return
	}

	if !hasUnstagedChanges {
		s.log(" Feature branch '%s' is clean. Checking out '%s' and running git pull --no-rebase origin %s...", origBranch, defaultBranch, defaultBranch)

		coCmd := exec.CommandContext(ctx, "git", "-C", item.Path, "checkout", defaultBranch)
		if err := coCmd.Run(); err != nil {
			s.finish(StatusError, "Checkout Err", "󰅙 Failed to checkout '%s': %v", defaultBranch, err)
			return
		}
		item.CurrentBranch = defaultBranch

		pullCmd := exec.CommandContext(ctx, "git", "-C", item.Path, "pull", "--no-rebase", "origin", defaultBranch)
		var pullOut bytes.Buffer
		pullCmd.Stdout = &pullOut
		pullCmd.Stderr = &pullOut
		if err := pullCmd.Run(); err == nil {
			s.finish(StatusSwitchedDefault, "Switched", "󰄬 Switched from '%s' to '%s' and pulled.", origBranch, defaultBranch)
		} else {
			s.finish(StatusError, "Pull Error", "󰅙 git pull error: %s", pullOut.String())
		}
		return
	}

	s.log(" Feature branch '%s' has unstaged changes. Executing git fetch and git rebase origin/%s...", origBranch, defaultBranch)

	_ = exec.CommandContext(ctx, "git", "-C", item.Path, "fetch", "origin").Run()
	rebaseTarget := fmt.Sprintf("origin/%s", defaultBranch)
	rebaseCmd := exec.CommandContext(ctx, "git", "-C", item.Path, "rebase", rebaseTarget)
	var rebaseOut bytes.Buffer
	rebaseCmd.Stdout = &rebaseOut
	rebaseCmd.Stderr = &rebaseOut

	if err := rebaseCmd.Run(); err == nil {
		s.finish(StatusRebased, "Rebased", "󰄬 Rebased '%s' onto '%s'.", origBranch, rebaseTarget)
	} else {
		// Use a fresh context for the abort so a mid-rebase state isn't left behind
		// even if the sync itself was cancelled.
		abortCtx, abortCancel := context.WithTimeout(context.Background(), rebaseAbortTimeout)
		_ = exec.CommandContext(abortCtx, "git", "-C", item.Path, "rebase", "--abort").Run()
		abortCancel()
		s.finish(StatusRebaseConflict, "Conflict", "󰅙 Rebase conflict: %s", rebaseOut.String())
	}
}

// SwitchBranch switches checkout to target branch.
func SwitchBranch(item *RepoItem, targetBranch string) error {
	cmd := exec.Command("git", "-C", item.Path, "checkout", targetBranch)
	if err := cmd.Run(); err != nil {
		item.Logs = append(item.Logs, fmt.Sprintf("󰅙 Failed to switch to branch '%s': %v", targetBranch, err))
		return err
	}
	item.CurrentBranch = targetBranch
	item.StatusMsg = "Switched"
	item.Logs = append(item.Logs, fmt.Sprintf("󰄬 Switched branch to '%s'.", targetBranch))
	return nil
}

// CommitPushPRAndSwitchDefault commits unstaged changes, pushes to origin, creates/updates PR, and switches back to default branch.
func CommitPushPRAndSwitchDefault(ctx context.Context, item *RepoItem) error {
	branch := item.OriginalBranch
	if branch == "" || branch == item.DefaultBranch {
		return fmt.Errorf("cannot raise PR from default branch")
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	item.Logs = append(item.Logs, fmt.Sprintf("󰏫 Committing and pushing branch '%s' to raise/update PR...", branch))

	// add/commit are best-effort: if there's nothing to commit (or the commit
	// otherwise no-ops), we still want to push whatever's already committed.
	_ = exec.CommandContext(ctx, "git", "-C", item.Path, "add", "-A").Run()
	commitMsg := fmt.Sprintf("WIP: Updates on branch '%s'", branch)
	_ = exec.CommandContext(ctx, "git", "-C", item.Path, "commit", "-m", commitMsg).Run()

	if ctx.Err() != nil {
		return ctx.Err()
	}

	pushCmd := exec.CommandContext(ctx, "git", "-C", item.Path, "push", "-u", "origin", branch)
	if err := pushCmd.Run(); err != nil {
		item.Logs = append(item.Logs, fmt.Sprintf("󰅙 git push error: %v", err))
		return err
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	if item.ExistingPRURL == "" {
		prCmd := exec.CommandContext(ctx, "gh", "pr", "create", "--fill", "--base", item.DefaultBranch, "--head", branch)
		prCmd.Dir = item.Path
		var prOut bytes.Buffer
		prCmd.Stdout = &prOut
		prCmd.Stderr = &prOut
		if err := prCmd.Run(); err == nil {
			prURL := strings.TrimSpace(prOut.String())
			item.DraftPRURL = prURL
			item.ExistingPRURL = prURL
			item.Logs = append(item.Logs, fmt.Sprintf("󰄬 PR created: %s", prURL))
		} else {
			item.Logs = append(item.Logs, fmt.Sprintf("󰅙 gh pr create notice: %s", prOut.String()))
		}
	} else {
		item.Logs = append(item.Logs, fmt.Sprintf("󰄬 Pushed commits to existing PR: %s", item.ExistingPRURL))
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	coCmd := exec.CommandContext(ctx, "git", "-C", item.Path, "checkout", item.DefaultBranch)
	if err := coCmd.Run(); err == nil {
		item.CurrentBranch = item.DefaultBranch
		item.Status = StatusPRCreated
		item.StatusMsg = "PR Raised"
		item.Logs = append(item.Logs, fmt.Sprintf("󰄬 Switched back to default branch '%s'.", item.DefaultBranch))
	}

	return nil
}

// CloneRepo clones a repository from GitHub organization into local path.
func CloneRepo(org, ghRepoName, targetPath string) error {
	cmd := exec.Command("gh", "repo", "clone", fmt.Sprintf("%s/%s", org, ghRepoName), targetPath)
	return cmd.Run()
}

// DeleteLocalRepo removes the local directory for an archived repository.
func DeleteLocalRepo(path string) error {
	return os.RemoveAll(path)
}

// ScanLocalDirectory returns names of all direct subdirectories in target path.
func ScanLocalDirectory(targetDir string) ([]string, error) {
	entries, err := os.ReadDir(targetDir)
	if err != nil {
		return nil, err
	}

	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			dirs = append(dirs, entry.Name())
		}
	}
	return dirs, nil
}
