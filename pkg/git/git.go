package git

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

type RepoStatus string

const (
	StatusPending   RepoStatus = "PENDING"
	StatusSyncing   RepoStatus = "SYNCING"
	StatusUpToDate  RepoStatus = "UP_TO_DATE"
	StatusUpdated   RepoStatus = "UPDATED"
	StatusStashedPR RepoStatus = "STASHED_PR"
	StatusCloned    RepoStatus = "CLONED"
	StatusArchived  RepoStatus = "ARCHIVED"
	StatusError     RepoStatus = "ERROR"
)

type GHRepoInfo struct {
	Name       string `json:"name"`
	IsArchived bool   `json:"isArchived"`
	URL        string `json:"url"`
	SSHURL     string `json:"sshUrl"`
}

type RepoItem struct {
	Name          string
	GHRepoName    string
	Path          string
	IsArchived    bool
	IsNew         bool
	CurrentBranch string
	DefaultBranch string
	Status        RepoStatus
	StatusMsg     string
	Stashed       bool
	DraftPRURL    string
	Logs          []string
	ErrorErr      error
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
	// Attempt 1: symbolic-ref for origin/HEAD
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

	// Attempt 2: gh repo view
	cmd = exec.Command("gh", "repo", "view", path, "--json", "defaultBranchRef", "--jq", ".defaultBranchRef.name")
	out.Reset()
	cmd.Stdout = &out
	if err := cmd.Run(); err == nil && strings.TrimSpace(out.String()) != "" {
		return strings.TrimSpace(out.String())
	}

	// Attempt 3: local refs check
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

// SyncRepository performs the full workflow on an active local repository:
// 1. Stash changes if dirty
// 2. Switch branch to default
// 3. Pull latest changes
// 4. Stash apply
// 5. Create draft PR if stashed changes restored
func SyncRepository(item *RepoItem) {
	item.Status = StatusSyncing
	item.Logs = append(item.Logs, fmt.Sprintf("[%s] Starting sync for %s", time.Now().Format("15:04:05"), item.Name))

	origBranch := GetOriginalBranch(item.Path)
	item.CurrentBranch = origBranch
	defaultBranch := GetDefaultBranch(item.Path)
	item.DefaultBranch = defaultBranch

	item.Logs = append(item.Logs, fmt.Sprintf("Current branch: %s | Default branch: %s", origBranch, defaultBranch))

	// Step 1: Check for uncommitted changes & Stash
	isDirtyCmd := exec.Command("git", "-C", item.Path, "status", "--porcelain")
	var dirtyOut bytes.Buffer
	isDirtyCmd.Stdout = &dirtyOut
	_ = isDirtyCmd.Run()

	stashed := false
	if strings.TrimSpace(dirtyOut.String()) != "" {
		item.Logs = append(item.Logs, "Uncommitted changes detected. Stashing local changes...")
		stashMsg := fmt.Sprintf("freshen auto-stash %s", time.Now().Format("2006-01-02 15:04:05"))
		stashCmd := exec.Command("git", "-C", item.Path, "stash", "push", "--include-untracked", "-m", stashMsg)
		if err := stashCmd.Run(); err == nil {
			stashed = true
			item.Stashed = true
			item.Logs = append(item.Logs, "Local changes stashed successfully.")
		} else {
			item.Logs = append(item.Logs, "Failed to stash local changes!")
		}
	}

	// Step 2: Switch to default branch
	if origBranch != defaultBranch {
		item.Logs = append(item.Logs, fmt.Sprintf("Switching branch from '%s' to '%s'...", origBranch, defaultBranch))
		coCmd := exec.Command("git", "-C", item.Path, "checkout", defaultBranch)
		if err := coCmd.Run(); err != nil {
			item.Status = StatusError
			item.StatusMsg = fmt.Sprintf("Checkout failed (%s)", defaultBranch)
			item.Logs = append(item.Logs, fmt.Sprintf("Error checking out default branch '%s': %v", defaultBranch, err))
			return
		}
	}

	// Step 3: Pull latest changes
	item.Logs = append(item.Logs, fmt.Sprintf("Pulling latest changes on '%s'...", defaultBranch))
	pullCmd := exec.Command("git", "-C", item.Path, "pull")
	var pullOut bytes.Buffer
	pullCmd.Stdout = &pullOut
	pullCmd.Stderr = &pullOut
	pullErr := pullCmd.Run()

	pullOutputStr := strings.TrimSpace(pullOut.String())
	if pullOutputStr != "" {
		item.Logs = append(item.Logs, fmt.Sprintf("git pull output: %s", pullOutputStr))
	}

	if pullErr != nil {
		item.Status = StatusError
		item.StatusMsg = "Pull Failed"
		item.Logs = append(item.Logs, fmt.Sprintf("git pull error: %v", pullErr))
		return
	}

	isUpToDate := strings.Contains(pullOutputStr, "Already up to date.")

	// Step 4: Stash Apply & Draft PR Creation
	if stashed {
		item.Logs = append(item.Logs, "Applying stashed changes...")
		applyCmd := exec.Command("git", "-C", item.Path, "stash", "apply")
		if err := applyCmd.Run(); err != nil {
			item.Status = StatusError
			item.StatusMsg = "Stash Conflict"
			item.Logs = append(item.Logs, "Conflict occurred while applying stash!")
			return
		}
		item.Logs = append(item.Logs, "Stashed changes applied successfully.")

		// Step 5: Draft PR Creation
		restoredDirty := exec.Command("git", "-C", item.Path, "status", "--porcelain")
		var restoredOut bytes.Buffer
		restoredDirty.Stdout = &restoredOut
		_ = restoredDirty.Run()

		if strings.TrimSpace(restoredOut.String()) != "" {
			item.Logs = append(item.Logs, "Creating draft pull request for stashed changes...")
			timestamp := time.Now().Format("20060102-150405")
			cleanBranch := sanitizeBranchName(origBranch)
			prBranch := fmt.Sprintf("draft/%s-%s", cleanBranch, timestamp)

			// Checkout new branch
			coBranchCmd := exec.Command("git", "-C", item.Path, "checkout", "-b", prBranch)
			if err := coBranchCmd.Run(); err == nil {
				_ = exec.Command("git", "-C", item.Path, "add", "-A").Run()
				commitMsg := fmt.Sprintf("WIP: Restored stashed changes from branch '%s'", origBranch)
				_ = exec.Command("git", "-C", item.Path, "commit", "-m", commitMsg).Run()

				// Push to origin
				pushCmd := exec.Command("git", "-C", item.Path, "push", "-u", "origin", prBranch)
				if err := pushCmd.Run(); err == nil {
					prTitle := fmt.Sprintf("WIP: Stashed changes (%s)", origBranch)
					prBody := fmt.Sprintf("Draft pull request automatically generated by freshen after pulling %s.", defaultBranch)
					prCmd := exec.Command("gh", "pr", "create", "--draft", "--base", defaultBranch, "--head", prBranch, "--title", prTitle, "--body", prBody)
					prCmd.Dir = item.Path
					var prOut bytes.Buffer
					prCmd.Stdout = &prOut
					prCmd.Stderr = &prOut

					if err := prCmd.Run(); err == nil {
						prURL := strings.TrimSpace(prOut.String())
						item.DraftPRURL = prURL
						item.Status = StatusStashedPR
						item.StatusMsg = "Draft PR Created"
						item.Logs = append(item.Logs, fmt.Sprintf("Draft PR created: %s", prURL))
						return
					} else {
						item.Logs = append(item.Logs, fmt.Sprintf("gh pr create error: %s", prOut.String()))
					}
				} else {
					item.Logs = append(item.Logs, fmt.Sprintf("Failed to push branch '%s' to origin", prBranch))
				}
			}
		}
	}

	if isUpToDate {
		item.Status = StatusUpToDate
		item.StatusMsg = "Up to date"
	} else {
		item.Status = StatusUpdated
		item.StatusMsg = "Updated"
	}
	item.Logs = append(item.Logs, fmt.Sprintf("[%s] Sync finished for %s", time.Now().Format("15:04:05"), item.Name))
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

func sanitizeBranchName(branch string) string {
	reg := regexp.MustCompile(`[^a-zA-Z0-9\-_]+`)
	return reg.ReplaceAllString(branch, "-")
}
