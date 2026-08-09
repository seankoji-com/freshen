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
	Status             RepoStatus
	StatusMsg          string
	Stashed            bool
	DraftPRURL         string
	Logs               []string
	ErrorErr           error
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

// SyncRepository performs the exact branch workflow:
// A. If on DEFAULT branch:
//   - No unstaged changes: git pull, done.
//   - Unstaged changes: git add . && git stash && git pull && git stash apply, done.
// B. If on DIFFERENT branch:
//   - Fetch link to any existing open PR.
//   - No unstaged changes: checkout default branch, git pull, done. (Option in TUI to switch back with 'b')
//   - Unstaged changes: git fetch && git rebase to default branch. (Option in TUI to commit/push PR with 'p')
func SyncRepository(item *RepoItem) {
	item.Status = StatusSyncing
	item.Logs = append(item.Logs, fmt.Sprintf("[%s] 󰓦 Starting sync for %s", time.Now().Format("15:04:05"), item.Name))

	origBranch := GetOriginalBranch(item.Path)
	item.OriginalBranch = origBranch
	item.CurrentBranch = origBranch
	defaultBranch := GetDefaultBranch(item.Path)
	item.DefaultBranch = defaultBranch

	// Check if an open PR exists for this feature branch
	if origBranch != defaultBranch {
		item.ExistingPRURL = FetchExistingPRURL(item.Path, origBranch)
		if item.ExistingPRURL != "" {
			item.Logs = append(item.Logs, fmt.Sprintf("󰏫 Existing Open PR found: %s", item.ExistingPRURL))
		}
	}

	// Check for unstaged / uncommitted changes
	isDirtyCmd := exec.Command("git", "-C", item.Path, "status", "--porcelain")
	var dirtyOut bytes.Buffer
	isDirtyCmd.Stdout = &dirtyOut
	_ = isDirtyCmd.Run()
	hasUnstagedChanges := strings.TrimSpace(dirtyOut.String()) != ""
	item.HasUnstagedChanges = hasUnstagedChanges

	item.Logs = append(item.Logs, fmt.Sprintf(" Branch: %s | Default: %s | Unstaged: %v", origBranch, defaultBranch, hasUnstagedChanges))

	// =========================================================================
	// CASE A: Repository is on DEFAULT branch
	// =========================================================================
	if origBranch == defaultBranch {
		if !hasUnstagedChanges {
			// A1: No unstaged changes -> git pull, done.
			item.Logs = append(item.Logs, fmt.Sprintf(" On default branch '%s' (clean). Running git pull...", defaultBranch))
			pullCmd := exec.Command("git", "-C", item.Path, "pull")
			var pullOut bytes.Buffer
			pullCmd.Stdout = &pullOut
			pullCmd.Stderr = &pullOut
			if err := pullCmd.Run(); err == nil {
				if strings.Contains(pullOut.String(), "Already up to date.") {
					item.Status = StatusUpToDate
					item.StatusMsg = fmt.Sprintf("󰄬 Up to date (%s)", defaultBranch)
				} else {
					item.Status = StatusUpdated
					item.StatusMsg = fmt.Sprintf("󰄬 Updated (%s)", defaultBranch)
				}
				item.Logs = append(item.Logs, fmt.Sprintf("󰄬 Successfully pulled '%s'.", defaultBranch))
			} else {
				item.Status = StatusError
				item.StatusMsg = "󰅙 Pull Failed"
				item.Logs = append(item.Logs, fmt.Sprintf("󰅙 git pull error: %s", pullOut.String()))
			}
			return
		} else {
			// A2: Unstaged changes -> git add . && git stash && git pull && git stash apply, done.
			item.Logs = append(item.Logs, fmt.Sprintf(" On default branch '%s' (dirty). Executing git add . && git stash && git pull && git stash apply...", defaultBranch))

			_ = exec.Command("git", "-C", item.Path, "add", ".").Run()
			stashMsg := fmt.Sprintf("freshen auto-stash %s", time.Now().Format("2006-01-02 15:04:05"))
			stashCmd := exec.Command("git", "-C", item.Path, "stash", "push", "-m", stashMsg)
			if err := stashCmd.Run(); err != nil {
				item.Status = StatusError
				item.StatusMsg = "󰅙 Stash Error"
				item.Logs = append(item.Logs, fmt.Sprintf("󰅙 Failed to stash local changes: %v", err))
				return
			}
			item.Stashed = true

			pullCmd := exec.Command("git", "-C", item.Path, "pull")
			_ = pullCmd.Run()

			applyCmd := exec.Command("git", "-C", item.Path, "stash", "apply")
			if err := applyCmd.Run(); err == nil {
				item.Status = StatusStashedApplied
				item.StatusMsg = fmt.Sprintf("󰏖 Stash Applied (%s)", defaultBranch)
				item.Logs = append(item.Logs, fmt.Sprintf("󰄬 Successfully pulled '%s' and re-applied stashed changes.", defaultBranch))
			} else {
				item.Status = StatusError
				item.StatusMsg = "󰅙 Stash Apply Conflict"
				item.Logs = append(item.Logs, "󰅙 Conflict occurred while applying stash!")
			}
			return
		}
	}

	// =========================================================================
	// CASE B: Repository is on DIFFERENT branch (origBranch != defaultBranch)
	// =========================================================================
	if !hasUnstagedChanges {
		// B1: No unstaged changes -> checkout default branch, git pull, done.
		// (Option in TUI: Press 'b' to switch back to origBranch)
		item.Logs = append(item.Logs, fmt.Sprintf(" Feature branch '%s' is clean. Checking out '%s' and running git pull...", origBranch, defaultBranch))

		coCmd := exec.Command("git", "-C", item.Path, "checkout", defaultBranch)
		if err := coCmd.Run(); err != nil {
			item.Status = StatusError
			item.StatusMsg = fmt.Sprintf("󰅙 Checkout Failed (%s)", defaultBranch)
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
			item.StatusMsg = fmt.Sprintf("󰄬 Switched to %s & Pulled", defaultBranch)
			item.Logs = append(item.Logs, fmt.Sprintf("󰄬 Switched from '%s' to '%s' and pulled. (Press 'b' to switch back to '%s')", origBranch, defaultBranch, origBranch))
		} else {
			item.Status = StatusError
			item.StatusMsg = "󰅙 Pull Failed"
			item.Logs = append(item.Logs, fmt.Sprintf("󰅙 git pull error: %s", pullOut.String()))
		}
		return
	} else {
		// B2: Unstaged changes -> git fetch and rebase to default branch.
		// (Option in TUI: Press 'p' to commit & push to raise new/existing PR and switch back to default)
		item.Logs = append(item.Logs, fmt.Sprintf(" Feature branch '%s' has unstaged changes. Executing git fetch and git rebase origin/%s...", origBranch, defaultBranch))

		_ = exec.Command("git", "-C", item.Path, "fetch", "origin").Run()
		rebaseTarget := fmt.Sprintf("origin/%s", defaultBranch)
		rebaseCmd := exec.Command("git", "-C", item.Path, "rebase", rebaseTarget)
		var rebaseOut bytes.Buffer
		rebaseCmd.Stdout = &rebaseOut
		rebaseCmd.Stderr = &rebaseOut

		if err := rebaseCmd.Run(); err == nil {
			item.Status = StatusRebased
			item.StatusMsg = fmt.Sprintf("󰚰 Rebased (%s)", origBranch)
			item.Logs = append(item.Logs, fmt.Sprintf("󰄬 Rebased '%s' onto '%s'. (Press 'p' to commit, push/PR & switch to %s)", origBranch, rebaseTarget, defaultBranch))
		} else {
			_ = exec.Command("git", "-C", item.Path, "rebase", "--abort").Run()
			item.Status = StatusRebaseConflict
			item.StatusMsg = fmt.Sprintf("󰅙 Rebase Conflict (%s)", origBranch)
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
	item.StatusMsg = fmt.Sprintf("󰁨 Switched to %s", targetBranch)
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

	// Stage and commit changes
	_ = exec.Command("git", "-C", item.Path, "add", "-A").Run()
	commitMsg := fmt.Sprintf("WIP: Updates on branch '%s'", branch)
	_ = exec.Command("git", "-C", item.Path, "commit", "-m", commitMsg).Run()

	// Push branch to origin
	pushCmd := exec.Command("git", "-C", item.Path, "push", "-u", "origin", branch)
	if err := pushCmd.Run(); err != nil {
		item.Logs = append(item.Logs, fmt.Sprintf("󰅙 git push error: %v", err))
		return err
	}

	// Create PR using gh CLI if no existing PR
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

	// Switch back to default branch
	coCmd := exec.Command("git", "-C", item.Path, "checkout", item.DefaultBranch)
	if err := coCmd.Run(); err == nil {
		item.CurrentBranch = item.DefaultBranch
		item.Status = StatusPRCreated
		item.StatusMsg = fmt.Sprintf("󰏫 PR Raised & Switched to %s", item.DefaultBranch)
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
