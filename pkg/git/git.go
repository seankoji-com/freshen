package git

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/seankoji-com/freshen/pkg/jobs"
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
func GetLocalDirName(ghRepo string) string {
	switch ghRepo {
	case ".github":
		return "github"
	case "careynas.net":
		return "wiki.robot.house"
	default:
		// Sanitize: reject paths containing /, \, or .. to prevent directory traversal
		if strings.ContainsAny(ghRepo, "/\\") || strings.Contains(ghRepo, "..") || filepath.Base(ghRepo) != ghRepo {
			// Return the base name with .. stripped
			base := filepath.Base(ghRepo)
			if base == "." || base == ".." {
				base = ""
			}
			return base
		}
		return ghRepo
	}
}

// GetGHRepoName maps local folder alias to GitHub repository name.
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
	cmd := exec.Command("gh", "repo", "list", org, "--limit", "1000", "--json", "name,isArchived,url,sshUrl")
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to fetch gh repos for org %s: %w: %s", org, err, strings.TrimSpace(errOut.String()))
	}

	var repos []GHRepoInfo
	if err := json.Unmarshal(out.Bytes(), &repos); err != nil {
		return nil, fmt.Errorf("failed to parse gh JSON output: %w", err)
	}

	return repos, nil
}

// FetchOrgRepoCounts queries GraphQL API for open issue and PR counts per repo.
func FetchOrgRepoCounts(org string) (map[string]RepoCounts, error) {
	query := fmt.Sprintf(`query { organization(login: "%s") { repositories(first: 100) { nodes { name issues(states: OPEN) { totalCount } pullRequests(states: OPEN) { totalCount } } } } }`, org)
	cmd := exec.Command("gh", "api", "graphql", "-f", fmt.Sprintf("query=%s", query))
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("gh api graphql failed for org %s: %w: %s", org, err, strings.TrimSpace(errOut.String()))
	}

	var resp GraphQLOrgResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
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
func GetRepoBranchDetails(path, defaultBranch string) BranchWorktreeDetails {
	var details BranchWorktreeDetails

	// Branches
	cmd := exec.Command("git", "-C", path, "branch", "-a")
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

	// Worktrees
	cmd = exec.Command("git", "-C", path, "worktree", "list")
	out.Reset()
	cmd.Stdout = &out
	if err := cmd.Run(); err == nil {
		for _, line := range strings.Split(out.String(), "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				details.Worktrees = append(details.Worktrees, trimmed)
			}
		}
	}

	// Changed files & status
	cmd = exec.Command("git", "-C", path, "status", "--short")
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
func PruneBranchesAndWorktrees(path, defaultBranch string) (int, error) {
	// 1. Fetch & prune deleted remote-tracking references from origin
	// Best-effort: a failure here shouldn't block local branch/worktree cleanup.
	if err := exec.Command("git", "-C", path, "fetch", "--prune", "origin").Run(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: git fetch --prune failed for %s: %v\n", path, err)
	}

	// 2. Force remove secondary git worktrees
	cmdWorktree := exec.Command("git", "-C", path, "worktree", "list", "--porcelain")
	var wtOut bytes.Buffer
	cmdWorktree.Stdout = &wtOut
	if err := cmdWorktree.Run(); err == nil {
		lines := strings.Split(wtOut.String(), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "worktree ") {
				wtPath := strings.TrimPrefix(line, "worktree ")
				wtPath = strings.TrimSpace(wtPath)
				if wtPath != "" && wtPath != path {
					if err := exec.Command("git", "-C", path, "worktree", "remove", "--force", wtPath).Run(); err != nil {
						fmt.Fprintf(os.Stderr, "warning: git worktree remove %s failed: %v\n", wtPath, err)
					}
				}
			}
		}
	}
	if err := exec.Command("git", "-C", path, "worktree", "prune").Run(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: git worktree prune failed for %s: %v\n", path, err)
	}

	// 3. Delete local non-default branches
	cmd := exec.Command("git", "-C", path, "branch", "--format=%(refname:short)")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return 0, err
	}

	currentBranch := GetOriginalBranch(path)
	deletedCount := 0
	for _, b := range strings.Split(out.String(), "\n") {
		b = cleanBranchName(b)
		if b != "" && b != defaultBranch && b != currentBranch {
			delCmd := exec.Command("git", "-C", path, "branch", "-D", b)
			if delCmd.Run() == nil {
				deletedCount++
			}
		}
	}
	return deletedCount, nil
}

// FetchOpenIssuesList retrieves open GitHub issues with a 6-second timeout context.
func FetchOpenIssuesList(org, ghRepo string) ([]IssueItem, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	target := fmt.Sprintf("%s/%s", org, ghRepo)
	cmd := exec.CommandContext(ctx, "gh", "issue", "list", "--repo", target, "--state", "open", "--limit", "50", "--json", "number,title,url")
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
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	target := fmt.Sprintf("%s/%s", org, ghRepo)
	cmd := exec.CommandContext(ctx, "gh", "pr", "list", "--repo", target, "--state", "open", "--limit", "50", "--json", "number,title,headRefName,url")
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
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err == nil {
		return strings.TrimSpace(out.String())
	} else {
		fmt.Fprintf(os.Stderr, "warning: gh pr list failed for %s: %v: %s\n", branch, err, strings.TrimSpace(errOut.String()))
	}
	return ""
}

// IsGitRepo checks if a directory is a valid git working tree.
func IsGitRepo(path string) bool {
	cmd := exec.Command("git", "-C", path, "rev-parse", "--is-inside-work-tree")
	return cmd.Run() == nil
}

// GetOriginalBranch gets current checked out branch name or HEAD commit short hash.
func GetOriginalBranch(path string) string {
	cmd := exec.Command("git", "-C", path, "symbolic-ref", "--short", "HEAD")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err == nil && strings.TrimSpace(out.String()) != "" {
		return strings.TrimSpace(out.String())
	}

	cmd = exec.Command("git", "-C", path, "rev-parse", "--short", "HEAD")
	out.Reset()
	cmd.Stdout = &out
	if err := cmd.Run(); err == nil && strings.TrimSpace(out.String()) != "" {
		return strings.TrimSpace(out.String())
	}

	return "HEAD"
}

// GetDefaultBranch determines default branch (main/master) for a git repository.
func GetDefaultBranch(path string) string {
	cmd := exec.Command("git", "-C", path, "symbolic-ref", "refs/remotes/origin/HEAD")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err == nil {
		ref := strings.TrimSpace(out.String())
		ref = strings.TrimPrefix(ref, "refs/remotes/origin/")
		ref = strings.TrimPrefix(ref, "origin/")
		if ref != "" {
			return ref
		}
	}

	cmd = exec.Command("gh", "repo", "view", path, "--json", "defaultBranchRef", "--jq", ".defaultBranchRef.name")
	out.Reset()
	var ghErrOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &ghErrOut
	if err := cmd.Run(); err == nil && strings.TrimSpace(out.String()) != "" {
		return strings.TrimSpace(out.String())
	} else if err != nil {
		fmt.Fprintf(os.Stderr, "warning: gh repo view failed for %s: %v: %s\n", path, err, strings.TrimSpace(ghErrOut.String()))
	}

	cmd = exec.Command("git", "-C", path, "show-ref", "--verify", "--quiet", "refs/heads/main")
	if cmd.Run() == nil {
		return "main"
	}
	cmd = exec.Command("git", "-C", path, "show-ref", "--verify", "--quiet", "refs/heads/master")
	if cmd.Run() == nil {
		return "master"
	}

	return GetOriginalBranch(path)
}

// SyncRepository performs the exact branch workflow with brief status messages.
// ctx allows the caller to abort in-flight git operations (e.g. on app quit).
func SyncRepository(ctx context.Context, item *RepoItem) {
	if ctx.Err() != nil {
		return
	}
	item.Status = StatusSyncing
	item.Logs = append(item.Logs, fmt.Sprintf("[%s] 󰓦 Starting sync for %s", time.Now().Format("15:04:05"), item.Name))

	origBranch := GetOriginalBranch(item.Path)
	item.OriginalBranch = origBranch
	item.CurrentBranch = origBranch
	defaultBranch := GetDefaultBranch(item.Path)
	item.DefaultBranch = defaultBranch

	item.BranchDetails = GetRepoBranchDetails(item.Path, defaultBranch)

	if origBranch != defaultBranch {
		item.ExistingPRURL = FetchExistingPRURL(item.Path, origBranch)
		if item.ExistingPRURL != "" {
			item.Logs = append(item.Logs, fmt.Sprintf("󰏫 Existing Open PR found: %s", item.ExistingPRURL))
		}
	}

	isDirtyCmd := exec.CommandContext(ctx, "git", "-C", item.Path, "status", "--porcelain")
	var dirtyOut bytes.Buffer
	isDirtyCmd.Stdout = &dirtyOut
	if err := isDirtyCmd.Run(); err != nil {
		item.Logs = append(item.Logs, fmt.Sprintf("󰅙 git status --porcelain failed: %v", err))
	}
	hasUnstagedChanges := strings.TrimSpace(dirtyOut.String()) != ""
	item.HasUnstagedChanges = hasUnstagedChanges

	item.Logs = append(item.Logs, fmt.Sprintf(" Branch: %s | Default: %s | Unstaged: %v", origBranch, defaultBranch, hasUnstagedChanges))

	if origBranch == defaultBranch {
		if !hasUnstagedChanges {
			item.Logs = append(item.Logs, fmt.Sprintf(" On default branch '%s' (clean). Running git pull --no-rebase origin %s...", defaultBranch, defaultBranch))
			pullCmd := exec.CommandContext(ctx, "git", "-C", item.Path, "pull", "--no-rebase", "origin", defaultBranch)
			var pullOut bytes.Buffer
			pullCmd.Stdout = &pullOut
			pullCmd.Stderr = &pullOut
			if err := pullCmd.Run(); err == nil {
				if strings.Contains(pullOut.String(), "Already up to date.") {
					item.Status = StatusUpToDate
					item.StatusMsg = "OK"
				} else {
					item.Status = StatusUpdated
					item.StatusMsg = "Updated"
				}
				item.Logs = append(item.Logs, fmt.Sprintf("󰄬 Successfully pulled '%s'.", defaultBranch))
			} else {
				item.Status = StatusError
				item.StatusMsg = "Pull Error"
				item.Logs = append(item.Logs, fmt.Sprintf("󰅙 git pull error: %s", pullOut.String()))
			}
			return
		} else {
			item.Logs = append(item.Logs, fmt.Sprintf(" On default branch '%s' (dirty). Executing git add . && git stash && git pull --no-rebase origin %s && git stash apply...", defaultBranch, defaultBranch))

			if err := exec.CommandContext(ctx, "git", "-C", item.Path, "add", ".").Run(); err != nil {
				item.Logs = append(item.Logs, fmt.Sprintf("󰅙 git add . failed (continuing with stash): %v", err))
			}
			stashMsg := fmt.Sprintf("freshen auto-stash %s", time.Now().Format("2006-01-02 15:04:05"))
			stashCmd := exec.CommandContext(ctx, "git", "-C", item.Path, "stash", "push", "-m", stashMsg)
			if err := stashCmd.Run(); err != nil {
				item.Status = StatusError
				item.StatusMsg = "Stash Err"
				item.Logs = append(item.Logs, fmt.Sprintf("󰅙 Failed to stash local changes: %v", err))
				return
			}
			item.Stashed = true

			if ctx.Err() != nil {
				item.Logs = append(item.Logs, "󰅙 Sync cancelled after stashing — changes remain stashed, re-run sync to restore them.")
				return
			}

			pullCmd := exec.CommandContext(ctx, "git", "-C", item.Path, "pull", "--no-rebase", "origin", defaultBranch)
			var dirtyPullOut bytes.Buffer
			pullCmd.Stdout = &dirtyPullOut
			pullCmd.Stderr = &dirtyPullOut
			if err := pullCmd.Run(); err != nil {
				item.Logs = append(item.Logs, fmt.Sprintf("󰅙 git pull error (continuing with stash apply): %s — %s", err.Error(), dirtyPullOut.String()))
			}

			applyCmd := exec.CommandContext(ctx, "git", "-C", item.Path, "stash", "apply")
			if err := applyCmd.Run(); err == nil {
				item.Status = StatusStashedApplied
				item.StatusMsg = "Stashed"
				item.Logs = append(item.Logs, fmt.Sprintf("󰄬 Successfully pulled '%s' and re-applied stashed changes.", defaultBranch))
			} else {
				item.Status = StatusError
				item.StatusMsg = "Conflict"
				item.Logs = append(item.Logs, "󰅙 Conflict occurred while applying stash!")
			}
			return
		}
	}

	if !hasUnstagedChanges {
		item.Logs = append(item.Logs, fmt.Sprintf(" Feature branch '%s' is clean. Checking out '%s' and running git pull --no-rebase origin %s...", origBranch, defaultBranch, defaultBranch))

		coCmd := exec.CommandContext(ctx, "git", "-C", item.Path, "checkout", defaultBranch)
		if err := coCmd.Run(); err != nil {
			item.Status = StatusError
			item.StatusMsg = "Checkout Err"
			item.Logs = append(item.Logs, fmt.Sprintf("󰅙 Failed to checkout '%s': %v", defaultBranch, err))
			return
		}
		item.CurrentBranch = defaultBranch

		pullCmd := exec.CommandContext(ctx, "git", "-C", item.Path, "pull", "--no-rebase", "origin", defaultBranch)
		var pullOut bytes.Buffer
		pullCmd.Stdout = &pullOut
		pullCmd.Stderr = &pullOut
		if err := pullCmd.Run(); err == nil {
			item.Status = StatusSwitchedDefault
			item.StatusMsg = "Switched"
			item.Logs = append(item.Logs, fmt.Sprintf("󰄬 Switched from '%s' to '%s' and pulled.", origBranch, defaultBranch))
		} else {
			item.Status = StatusError
			item.StatusMsg = "Pull Error"
			item.Logs = append(item.Logs, fmt.Sprintf("󰅙 git pull error: %s", pullOut.String()))
		}
		return
	} else {
		item.Logs = append(item.Logs, fmt.Sprintf(" Feature branch '%s' has unstaged changes. Executing git fetch and git rebase origin/%s...", origBranch, defaultBranch))

		if err := exec.CommandContext(ctx, "git", "-C", item.Path, "fetch", "origin").Run(); err != nil {
			item.Logs = append(item.Logs, fmt.Sprintf("󰅙 git fetch origin failed (continuing with rebase attempt): %v", err))
		}
		rebaseTarget := fmt.Sprintf("origin/%s", defaultBranch)
		rebaseCmd := exec.CommandContext(ctx, "git", "-C", item.Path, "rebase", rebaseTarget)
		var rebaseOut bytes.Buffer
		rebaseCmd.Stdout = &rebaseOut
		rebaseCmd.Stderr = &rebaseOut

		if err := rebaseCmd.Run(); err == nil {
			item.Status = StatusRebased
			item.StatusMsg = "Rebased"
			item.Logs = append(item.Logs, fmt.Sprintf("󰄬 Rebased '%s' onto '%s'.", origBranch, rebaseTarget))
		} else {
			// Use a fresh context for the abort so a mid-rebase state isn't left behind
			// even if the sync itself was cancelled.
			abortCtx, abortCancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := exec.CommandContext(abortCtx, "git", "-C", item.Path, "rebase", "--abort").Run(); err != nil {
				item.Logs = append(item.Logs, fmt.Sprintf("󰅙 git rebase --abort failed: %v", err))
			}
			abortCancel()
			item.Status = StatusRebaseConflict
			item.StatusMsg = "Conflict"
			item.Logs = append(item.Logs, fmt.Sprintf("󰅙 Rebase conflict: %s", rebaseOut.String()))
		}
		return
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
func CommitPushPRAndSwitchDefault(item *RepoItem) error {
	branch := item.OriginalBranch
	if branch == "" || branch == item.DefaultBranch {
		return fmt.Errorf("cannot raise PR from default branch")
	}

	item.Logs = append(item.Logs, fmt.Sprintf("󰏫 Committing and pushing branch '%s' to raise/update PR...", branch))

	if err := exec.Command("git", "-C", item.Path, "add", "-A").Run(); err != nil {
		item.Logs = append(item.Logs, fmt.Sprintf("󰅙 git add -A failed: %v", err))
		return err
	}

	commitMsg := fmt.Sprintf("WIP: Updates on branch '%s'", branch)
	commitCmd := exec.Command("git", "-C", item.Path, "commit", "-m", commitMsg)
	var commitOut bytes.Buffer
	commitCmd.Stdout = &commitOut
	commitCmd.Stderr = &commitOut
	if err := commitCmd.Run(); err != nil {
		if strings.Contains(commitOut.String(), "nothing to commit") {
			item.Logs = append(item.Logs, "Nothing to commit; continuing to push existing commits.")
		} else {
			item.Logs = append(item.Logs, fmt.Sprintf("󰅙 git commit failed: %v: %s", err, strings.TrimSpace(commitOut.String())))
			return err
		}
	}

	pushCmd := exec.Command("git", "-C", item.Path, "push", "-u", "origin", branch)
	if err := pushCmd.Run(); err != nil {
		item.Logs = append(item.Logs, fmt.Sprintf("󰅙 git push error: %v", err))
		return err
	}

	if item.ExistingPRURL == "" {
		prCmd := exec.Command("gh", "pr", "create", "--fill", "--base", item.DefaultBranch, "--head", branch)
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

	coCmd := exec.Command("git", "-C", item.Path, "checkout", item.DefaultBranch)
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
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gh repo clone %s/%s failed: %w: %s", org, ghRepoName, err, strings.TrimSpace(errOut.String()))
	}
	return nil
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
