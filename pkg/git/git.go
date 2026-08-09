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
	StatusPending        RepoStatus = "PENDING"
	StatusSyncing        RepoStatus = "SYNCING"
	StatusUpToDate       RepoStatus = "UP_TO_DATE"
	StatusUpdated        RepoStatus = "UPDATED"
	StatusRebased        RepoStatus = "REBASED"
	StatusRebaseConflict RepoStatus = "REBASE_CONFLICT"
	StatusStashedPR      RepoStatus = "STASHED_PR"
	StatusCloned         RepoStatus = "CLONED"
	StatusArchived       RepoStatus = "ARCHIVED"
	StatusError          RepoStatus = "ERROR"
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

// SyncRepository performs branch-selective workflow:
// 1. Repos on default branch -> Full sync (stash -> pull default -> stash apply -> draft PR if stashed)
// 2. Repos on feature branch with NO unstaged changes -> Switch back to default branch & pull
// 3. Repos on feature branch WITH unstaged changes -> Fetch origin & rebase onto default branch
func SyncRepository(item *RepoItem, forceFullSync bool) {
	item.Status = StatusSyncing
	item.Logs = append(item.Logs, fmt.Sprintf("[%s] Starting sync for %s", time.Now().Format("15:04:05"), item.Name))

	origBranch := GetOriginalBranch(item.Path)
	item.CurrentBranch = origBranch
	defaultBranch := GetDefaultBranch(item.Path)
	item.DefaultBranch = defaultBranch

	item.Logs = append(item.Logs, fmt.Sprintf("Current branch: %s | Default branch: %s", origBranch, defaultBranch))

	// Check if working tree has unstaged/uncommitted changes
	isDirtyCmd := exec.Command("git", "-C", item.Path, "status", "--porcelain")
	var dirtyOut bytes.Buffer
	isDirtyCmd.Stdout = &dirtyOut
	_ = isDirtyCmd.Run()
	hasDirtyChanges := strings.TrimSpace(dirtyOut.String()) != ""

	// Branch Logic Condition: Not on default branch & not forcing full sync
	if origBranch != defaultBranch && !forceFullSync {
		if hasDirtyChanges {
			// Sub-case: Feature branch HAS unstaged changes -> fetch origin & rebase to default
			item.Logs = append(item.Logs, fmt.Sprintf("Branch '%s' has unstaged changes. Executing git fetch origin and git rebase origin/%s...", origBranch, defaultBranch))

			fetchCmd := exec.Command("git", "-C", item.Path, "fetch", "origin")
			if err := fetchCmd.Run(); err != nil {
				item.Logs = append(item.Logs, fmt.Sprintf("git fetch error: %v", err))
			}

			rebaseTarget := fmt.Sprintf("origin/%s", defaultBranch)
			rebaseCmd := exec.Command("git", "-C", item.Path, "rebase", rebaseTarget)
			var rebaseOut bytes.Buffer
			rebaseCmd.Stdout = &rebaseOut
			rebaseCmd.Stderr = &rebaseOut

			if err := rebaseCmd.Run(); err == nil {
				item.Status = StatusRebased
				item.StatusMsg = fmt.Sprintf("Rebased (%s)", origBranch)
				item.Logs = append(item.Logs, fmt.Sprintf("✓ Rebased '%s' onto '%s'. Hit 'f' for full PR sync.", origBranch, rebaseTarget))
			} else {
				_ = exec.Command("git", "-C", item.Path, "rebase", "--abort").Run()
				item.Status = StatusRebaseConflict
				item.StatusMsg = fmt.Sprintf("Rebase Conflict (%s)", origBranch)
				item.Logs = append(item.Logs, fmt.Sprintf("✗ Rebase conflict on %s: %s", origBranch, rebaseOut.String()))
			}
			return
		} else {
			// Sub-case: Feature branch HAS NO unstaged changes -> switch back to default and pull
			item.Logs = append(item.Logs, fmt.Sprintf("Branch '%s' is clean with no unstaged changes. Switching back to '%s' and pulling...", origBranch, defaultBranch))

			coCmd := exec.Command("git", "-C", item.Path, "checkout", defaultBranch)
			if err := coCmd.Run(); err != nil {
				item.Status = StatusError
				item.StatusMsg = fmt.Sprintf("Checkout failed (%s)", defaultBranch)
				item.Logs = append(item.Logs, fmt.Sprintf("✗ Error checking out default branch '%s': %v", defaultBranch, err))
				return
			}
			item.CurrentBranch = defaultBranch

			pullCmd := exec.Command("git", "-C", item.Path, "pull")
			var pullOut bytes.Buffer
			pullCmd.Stdout = &pullOut
			pullCmd.Stderr = &pullOut
			if err := pullCmd.Run(); err == nil {
				isUpToDate := strings.Contains(pullOut.String(), "Already up to date.")
				if isUpToDate {
					item.Status = StatusUpToDate
					item.StatusMsg = fmt.Sprintf("Up to date (%s)", defaultBranch)
				} else {
					item.Status = StatusUpdated
					item.StatusMsg = fmt.Sprintf("Updated (%s)", defaultBranch)
				}
				item.Logs = append(item.Logs, fmt.Sprintf("✓ Successfully switched to '%s' and pulled latest changes.", defaultBranch))
			} else {
				item.Status = StatusError
				item.StatusMsg = "Pull Failed"
				item.Logs = append(item.Logs, fmt.Sprintf("✗ git pull error: %s", pullOut.String()))
			}
			return
		}
	}

	// Full Sync Workflow (For repos on default branch OR when forceFullSync is true)
	item.Logs = append(item.Logs, "Executing full sync workflow (stash -> checkout default -> pull -> stash apply -> draft PR)...")

	// Step 1: Check for uncommitted changes & Stash
	stashed := false
	if hasDirtyChanges {
		item.Logs = append(item.Logs, "Uncommitted changes detected. Stashing local working tree...")
		stashMsg := fmt.Sprintf("freshen auto-stash %s", time.Now().Format("2006-01-02 15:04:05"))
		stashCmd := exec.Command("git", "-C", item.Path, "stash", "push", "--include-untracked", "-m", stashMsg)
		if err := stashCmd.Run(); err == nil {
			stashed = true
			item.Stashed = true
			item.Logs = append(item.Logs, "✓ Local changes stashed successfully.")
		} else {
			item.Logs = append(item.Logs, "✗ Failed to stash local changes!")
		}
	}

	// Step 2: Switch to default branch
	if origBranch != defaultBranch {
		item.Logs = append(item.Logs, fmt.Sprintf("Switching branch from '%s' to '%s'...", origBranch, defaultBranch))
		coCmd := exec.Command("git", "-C", item.Path, "checkout", defaultBranch)
		if err := coCmd.Run(); err != nil {
			item.Status = StatusError
			item.StatusMsg = fmt.Sprintf("Checkout failed (%s)", defaultBranch)
			item.Logs = append(item.Logs, fmt.Sprintf("✗ Error checking out default branch '%s': %v", defaultBranch, err))
			return
		}
		item.CurrentBranch = defaultBranch
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
		item.Logs = append(item.Logs, fmt.Sprintf("✗ git pull error: %v", pullErr))
		return
	}

	isUpToDate := strings.Contains(pullOutputStr, "Already up to date.")

	// Step 4: Stash Apply & Draft PR Creation
	if stashed {
		item.Logs = append(item.Logs, "Applying stashed changes onto pulled default branch...")
		applyCmd := exec.Command("git", "-C", item.Path, "stash", "apply")
		if err := applyCmd.Run(); err != nil {
			item.Status = StatusError
			item.StatusMsg = "Stash Conflict"
			item.Logs = append(item.Logs, "✗ Conflict occurred while applying stash!")
			return
		}
		item.Logs = append(item.Logs, "✓ Stashed changes applied successfully.")

		// Step 5: Draft PR Creation
		// How Draft PR works on default branch:
		// Since we cannot push directly to main to create a PR, we create a feature branch `draft/<branch>-<timestamp>`,
		// commit the stashed changes to that feature branch, push to origin, and run `gh pr create --draft --base defaultBranch --head draft/...`.
		restoredDirty := exec.Command("git", "-C", item.Path, "status", "--porcelain")
		var restoredOut bytes.Buffer
		restoredDirty.Stdout = &restoredOut
		_ = restoredDirty.Run()

		if strings.TrimSpace(restoredOut.String()) != "" {
			item.Logs = append(item.Logs, "Creating draft pull request for stashed changes...")
			timestamp := time.Now().Format("20060102-150405")
			cleanBranch := sanitizeBranchName(origBranch)
			prBranch := fmt.Sprintf("draft/%s-%s", cleanBranch, timestamp)

			item.Logs = append(item.Logs, fmt.Sprintf("Creating feature branch '%s' for PR...", prBranch))
			coBranchCmd := exec.Command("git", "-C", item.Path, "checkout", "-b", prBranch)
			if err := coBranchCmd.Run(); err == nil {
				_ = exec.Command("git", "-C", item.Path, "add", "-A").Run()
				commitMsg := fmt.Sprintf("WIP: Restored stashed changes from branch '%s'", origBranch)
				_ = exec.Command("git", "-C", item.Path, "commit", "-m", commitMsg).Run()

				item.Logs = append(item.Logs, fmt.Sprintf("Pushing branch '%s' to origin...", prBranch))
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
						item.Logs = append(item.Logs, fmt.Sprintf("✓ Draft PR created successfully: %s", prURL))
						return
					} else {
						item.Logs = append(item.Logs, fmt.Sprintf("✗ gh pr create error: %s", prOut.String()))
					}
				} else {
					item.Logs = append(item.Logs, fmt.Sprintf("✗ Failed to push branch '%s' to origin", prBranch))
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
