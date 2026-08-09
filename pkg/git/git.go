package git

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
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
	Branches     []string
	Worktrees    []string
	ChangedFiles []string
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
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to fetch gh repos for org %s: %w", org, err)
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
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
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

// PruneBranchesAndWorktrees deletes non-default local branches and prunes worktrees.
func PruneBranchesAndWorktrees(path, defaultBranch string) (int, error) {
	_ = exec.Command("git", "-C", path, "worktree", "prune").Run()

	cmd := exec.Command("git", "-C", path, "branch", "--format=%(refname:short)")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return 0, err
	}

	currentBranch := GetOriginalBranch(path)
	deletedCount := 0
	for _, b := range strings.Split(out.String(), "\n") {
		b = strings.TrimSpace(b)
		if b != "" && b != defaultBranch && b != currentBranch {
			delCmd := exec.Command("git", "-C", path, "branch", "-D", b)
			if delCmd.Run() == nil {
				deletedCount++
			}
		}
	}
	return deletedCount, nil
}

// FetchOpenIssuesList retrieves open GitHub issues for a repository.
func FetchOpenIssuesList(org, ghRepo string) ([]IssueItem, error) {
	target := fmt.Sprintf("%s/%s", org, ghRepo)
	cmd := exec.Command("gh", "issue", "list", "--repo", target, "--state", "open", "--limit", "50", "--json", "number,title,url")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return []IssueItem{}, err
	}

	var issues []IssueItem
	if err := json.Unmarshal(out.Bytes(), &issues); err != nil {
		return []IssueItem{}, err
	}
	if issues == nil {
		issues = []IssueItem{}
	}
	return issues, nil
}

// FetchOpenPRsList retrieves open GitHub pull requests for a repository.
func FetchOpenPRsList(org, ghRepo string) ([]PRItem, error) {
	target := fmt.Sprintf("%s/%s", org, ghRepo)
	cmd := exec.Command("gh", "pr", "list", "--repo", target, "--state", "open", "--limit", "50", "--json", "number,title,headRefName,url")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return []PRItem{}, err
	}

	var prs []PRItem
	if err := json.Unmarshal(out.Bytes(), &prs); err != nil {
		return []PRItem{}, err
	}
	if prs == nil {
		prs = []PRItem{}
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
	cmd.Stdout = &out
	if err := cmd.Run(); err == nil && strings.TrimSpace(out.String()) != "" {
		return strings.TrimSpace(out.String())
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
func SyncRepository(item *RepoItem) {
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

	isDirtyCmd := exec.Command("git", "-C", item.Path, "status", "--porcelain")
	var dirtyOut bytes.Buffer
	isDirtyCmd.Stdout = &dirtyOut
	_ = isDirtyCmd.Run()
	hasUnstagedChanges := strings.TrimSpace(dirtyOut.String()) != ""
	item.HasUnstagedChanges = hasUnstagedChanges

	item.Logs = append(item.Logs, fmt.Sprintf(" Branch: %s | Default: %s | Unstaged: %v", origBranch, defaultBranch, hasUnstagedChanges))

	if origBranch == defaultBranch {
		if !hasUnstagedChanges {
			item.Logs = append(item.Logs, fmt.Sprintf(" On default branch '%s' (clean). Running git pull...", defaultBranch))
			pullCmd := exec.Command("git", "-C", item.Path, "pull")
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
			item.Logs = append(item.Logs, fmt.Sprintf(" On default branch '%s' (dirty). Executing git add . && git stash && git pull && git stash apply...", defaultBranch))

			_ = exec.Command("git", "-C", item.Path, "add", ".").Run()
			stashMsg := fmt.Sprintf("freshen auto-stash %s", time.Now().Format("2006-01-02 15:04:05"))
			stashCmd := exec.Command("git", "-C", item.Path, "stash", "push", "-m", stashMsg)
			if err := stashCmd.Run(); err != nil {
				item.Status = StatusError
				item.StatusMsg = "Stash Err"
				item.Logs = append(item.Logs, fmt.Sprintf("󰅙 Failed to stash local changes: %v", err))
				return
			}
			item.Stashed = true

			pullCmd := exec.Command("git", "-C", item.Path, "pull")
			_ = pullCmd.Run()

			applyCmd := exec.Command("git", "-C", item.Path, "stash", "apply")
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
		item.Logs = append(item.Logs, fmt.Sprintf(" Feature branch '%s' is clean. Checking out '%s' and running git pull...", origBranch, defaultBranch))

		coCmd := exec.Command("git", "-C", item.Path, "checkout", defaultBranch)
		if err := coCmd.Run(); err != nil {
			item.Status = StatusError
			item.StatusMsg = "Checkout Err"
			item.Logs = append(item.Logs, fmt.Sprintf("󰅙 Failed to checkout '%s': %v", defaultBranch, err))
			return
		}
		item.CurrentBranch = defaultBranch

		pullCmd := exec.Command("git", "-C", item.Path, "pull")
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

		_ = exec.Command("git", "-C", item.Path, "fetch", "origin").Run()
		rebaseTarget := fmt.Sprintf("origin/%s", defaultBranch)
		rebaseCmd := exec.Command("git", "-C", item.Path, "rebase", rebaseTarget)
		var rebaseOut bytes.Buffer
		rebaseCmd.Stdout = &rebaseOut
		rebaseCmd.Stderr = &rebaseOut

		if err := rebaseCmd.Run(); err == nil {
			item.Status = StatusRebased
			item.StatusMsg = "Rebased"
			item.Logs = append(item.Logs, fmt.Sprintf("󰄬 Rebased '%s' onto '%s'.", origBranch, rebaseTarget))
		} else {
			_ = exec.Command("git", "-C", item.Path, "rebase", "--abort").Run()
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

	_ = exec.Command("git", "-C", item.Path, "add", "-A").Run()
	commitMsg := fmt.Sprintf("WIP: Updates on branch '%s'", branch)
	_ = exec.Command("git", "-C", item.Path, "commit", "-m", commitMsg).Run()

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
