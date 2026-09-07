package git

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	defaultRepoLimit     = 1000
	graphQLRepoLimit     = 100
	countsMaxPages       = (defaultRepoLimit + graphQLRepoLimit - 1) / graphQLRepoLimit
	defaultIssueLimit    = 50
	defaultPRLimit       = 50
	fetchTimeout         = 6 * time.Second
	countsFetchTimeout   = 15 * time.Second
	countsSweepTimeout   = countsMaxPages*countsFetchTimeout + 5*time.Second
	orgFetchTimeout      = 30 * time.Second
	branchResolveTimeout = 10 * time.Second
	rebaseAbortTimeout   = 10 * time.Second
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
	// StatusSkipped marks a repo a safe-only sync left untouched because it
	// wasn't on its default branch — see SyncRepository's safeOnly param.
	StatusSkipped RepoStatus = "SKIPPED"
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

type GraphQLOwnerResponse struct {
	Data struct {
		RepositoryOwner *struct {
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
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
			} `json:"repositories"`
		} `json:"repositoryOwner"`
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
	CountsStale        bool
	CountsUpdatedAt    time.Time
	LocalMetadataStale bool
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

// aliasToRemote and aliasToLocal hold user-supplied repo alias pairs
// registered via the repeatable --alias local=remote flag (see AddAlias).
// They are consulted before the built-in defaults below, so a user-supplied
// pair overrides a built-in one for the same key.
var (
	aliasToRemote = make(map[string]string) // local -> remote
	aliasToLocal  = make(map[string]string) // remote -> local
)

// AddAlias registers a local/remote repo alias pair, overriding the built-in
// defaults in GetLocalDirName/GetGHRepoName for that pair. Intended to be
// called during flag parsing, before any concurrent use of this package.
// The local half is validated here (it becomes a directory name joined onto the
// target directory), so a bad --alias is a startup error rather than a runtime
// path collapse. The remote half is validated the same way because it is used
// as a clone target name.
func AddAlias(local, remote string) error {
	if _, ok := sanitizeDirName(local); !ok {
		return fmt.Errorf("invalid local alias name %q: must be a single path segment, not empty, %q or %q", local, ".", "..")
	}
	if _, ok := sanitizeDirName(remote); !ok {
		return fmt.Errorf("invalid remote alias name %q: must be a single path segment, not empty, %q or %q", remote, ".", "..")
	}
	aliasToRemote[local] = remote
	aliasToLocal[remote] = local
	return nil
}

// GetLocalDirName maps GitHub repository name to local folder alias.
// The case statements contain user-specific aliases (e.g., .github -> github, careynas.net -> wiki.robot.house).
// User-supplied aliases from --alias take precedence; see AddAlias.
// The second return value reports whether the result is a safe single path
// segment; callers MUST check it and skip the repo when false rather than
// joining an unusable name onto the target directory.
func GetLocalDirName(ghRepo string) (string, bool) {
	if local, ok := aliasToLocal[ghRepo]; ok {
		return sanitizeDirName(local)
	}
	var name string
	switch ghRepo {
	case ".github":
		name = "github"
	case "careynas.net":
		name = "wiki.robot.house"
	default:
		name = ghRepo
	}
	return sanitizeDirName(name)
}

// sanitizeDirName reports whether name is safe to join onto a parent directory
// as a single path segment, returning it unchanged when it is.
//
// It deliberately never rewrites the name. Stripping the offending characters
// instead would map distinct repos onto one directory (GitHub allows dots, so
// "evil" and "..evil" are different repos) and — worse — collapse "..", "." and
// "foo/.." to "", which filepath.Join resolves back to the parent directory
// itself, handing the whole repos root to os.RemoveAll. Rejection is the only
// safe failure mode, so ok=false means "refuse", never "use the default".
func sanitizeDirName(name string) (string, bool) {
	if name == "" || name == "." || name == ".." {
		return "", false
	}
	if strings.ContainsAny(name, `/\`) || strings.ContainsRune(name, 0) {
		return "", false
	}
	// Clean leaves a valid single segment untouched ("foo..bar" stays as-is) but
	// rewrites anything with traversal or redundant separators still in it.
	if filepath.Clean(name) != name || filepath.Base(name) != name {
		return "", false
	}
	return name, true
}

// GetGHRepoName maps local folder alias to GitHub repository name.
// The case statements contain user-specific aliases (e.g., github -> .github, wiki.robot.house -> careynas.net).
// User-supplied aliases from --alias take precedence; see AddAlias, which
// validates both halves of every pair so an alias always round-trips with
// GetLocalDirName. Non-alias inputs are local directory entries, which are
// single path segments by construction.
func GetGHRepoName(localDir string) string {
	if remote, ok := aliasToRemote[localDir]; ok {
		return remote
	}
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
func FetchOrgRepos(parent context.Context, org string) ([]GHRepoInfo, error) {
	ctx, cancel := context.WithTimeout(parent, orgFetchTimeout)
	defer cancel()
	out, err := runner.Run(ctx, "gh", "repo", "list", org, "--limit", fmt.Sprintf("%d", defaultRepoLimit), "--json", "name,isArchived,url,sshUrl")
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
func FetchOrgRepoCounts(parent context.Context, org string) (map[string]RepoCounts, error) {
	if err := parent.Err(); err != nil {
		return map[string]RepoCounts{}, err
	}
	sweepCtx, sweepCancel := context.WithTimeout(parent, countsSweepTimeout)
	defer sweepCancel()

	query := fmt.Sprintf(`query($login: String!, $endCursor: String) { repositoryOwner(login: $login) { repositories(first: %d, after: $endCursor) { nodes { name issues(states: OPEN) { totalCount } pullRequests(states: OPEN) { totalCount } } pageInfo { hasNextPage endCursor } } } }`, graphQLRepoLimit)
	result := make(map[string]RepoCounts)
	cursor := ""
	fetched := 0
	for page := 0; page < countsMaxPages; page++ {
		args := []string{"api", "graphql", "-f", fmt.Sprintf("query=%s", query), "-F", fmt.Sprintf("login=%s", org)}
		if cursor != "" {
			args = append(args, "-F", "endCursor="+cursor)
		}
		pageCtx, cancel := context.WithTimeout(sweepCtx, countsFetchTimeout)
		out, err := runner.Run(pageCtx, "gh", args...)
		cancel()
		if err != nil {
			return result, fmt.Errorf("gh api graphql failed for org %s: %w", org, err)
		}

		var resp GraphQLOwnerResponse
		if err := json.Unmarshal(out, &resp); err != nil {
			return result, err
		}
		if resp.Data.RepositoryOwner == nil {
			return result, fmt.Errorf("gh api graphql returned no repository owner for org %s", org)
		}
		repositories := resp.Data.RepositoryOwner.Repositories
		if repositories.PageInfo.HasNextPage && len(repositories.Nodes) == 0 {
			return result, fmt.Errorf("gh api graphql returned an empty repository page with another page pending for org %s", org)
		}
		for _, node := range repositories.Nodes {
			result[node.Name] = RepoCounts{
				Issues: node.Issues.TotalCount,
				PRs:    node.PullRequests.TotalCount,
			}
		}
		fetched += len(repositories.Nodes)
		if !repositories.PageInfo.HasNextPage {
			return result, nil
		}
		if repositories.PageInfo.EndCursor == "" || repositories.PageInfo.EndCursor == cursor {
			return result, fmt.Errorf("gh api graphql returned an invalid repository cursor for org %s", org)
		}
		if page == countsMaxPages-1 || fetched >= defaultRepoLimit {
			return result, fmt.Errorf("gh api graphql repository counts truncated at %d repositories for org %s", defaultRepoLimit, org)
		}
		cursor = repositories.PageInfo.EndCursor
	}
	return result, fmt.Errorf("gh api graphql repository counts exceeded the page limit for org %s", org)
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

type PruneResult struct {
	RemovedWorktrees     []string
	PrunedWorktrees      []string
	DirtyWorktrees       []string
	UnavailableWorktrees []string
	ProtectedBranches    []string
	UnpushedBranches     []string
	DeletedBranches      []string
	Failures             []string
}

// worktreePruneExpire gives temporarily unavailable mounts a recovery window
// before Git may classify their registrations as stale.
var worktreePruneExpire = "3.months.ago"

// PruneBranchesAndWorktrees fetches and prunes remote tracking branches,
// removes clean secondary worktrees, and deletes non-default local branches.
// It verifies every destructive target before removing anything.
func PruneBranchesAndWorktrees(ctx context.Context, item *RepoItem) (PruneResult, error) {
	var result PruneResult
	if item == nil {
		return result, fmt.Errorf("repository is required")
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	defaultBranch, err := ResolveDefaultBranch(ctx, item.Path)
	if err != nil {
		return result, err
	}
	knownDefault := strings.TrimSpace(item.DefaultBranch)
	if knownDefault == "" || strings.EqualFold(knownDefault, "HEAD") {
		return result, fmt.Errorf("displayed default branch is unresolved; sync the repository and review it before pruning")
	}
	if knownDefault != defaultBranch {
		return result, fmt.Errorf("default branch changed from %q to %q; sync the repository and confirm pruning again", knownDefault, defaultBranch)
	}
	if !localBranchExists(ctx, item.Path, defaultBranch) {
		return result, fmt.Errorf("verified default branch %q has no local branch; fetch or sync it before pruning", defaultBranch)
	}
	currentBranch, err := resolveCurrentBranch(ctx, item.Path)
	if err != nil {
		return result, err
	}
	item.DefaultBranch = defaultBranch
	item.CurrentBranch = currentBranch
	path := item.Path

	// 1. Fetch & prune deleted remote-tracking references from origin
	// Best-effort: a failure here shouldn't block local branch/worktree cleanup.
	if err := exec.CommandContext(ctx, "git", "-C", path, "fetch", "--prune", "origin").Run(); err != nil {
		slog.Warn("git fetch --prune failed", "path", path, "error", err)
	}

	if ctx.Err() != nil {
		return result, ctx.Err()
	}

	// 2. Inventory every registered secondary worktree before removing any of
	// them. Git's prunable marker observes its normal expiry grace period, so a
	// newly unavailable mount stays protected while an expired stale
	// registration can be cleaned up.
	cmdWorktree := exec.CommandContext(ctx, "git", "-C", path, "worktree", "list", "--porcelain", "--expire="+worktreePruneExpire)
	var wtOut bytes.Buffer
	cmdWorktree.Stdout = &wtOut
	if err := cmdWorktree.Run(); err != nil {
		return result, fmt.Errorf("list worktrees: %w", err)
	}
	type worktreeRef struct {
		path     string
		branch   string
		prunable bool
	}
	var worktrees []worktreeRef
	var current worktreeRef
	for _, line := range strings.Split(wtOut.String(), "\n") {
		if strings.HasPrefix(line, "worktree ") {
			if current.path != "" && !sameExistingPath(current.path, path) {
				worktrees = append(worktrees, current)
			}
			current = worktreeRef{path: strings.TrimSpace(strings.TrimPrefix(line, "worktree "))}
		} else if strings.HasPrefix(line, "branch refs/heads/") {
			current.branch = strings.TrimSpace(strings.TrimPrefix(line, "branch refs/heads/"))
		} else if strings.HasPrefix(line, "prunable") {
			current.prunable = true
		}
	}
	if current.path != "" && !sameExistingPath(current.path, path) {
		worktrees = append(worktrees, current)
	}
	occupiedBranches := make(map[string]string)
	var staleWorktrees []worktreeRef
	for _, worktree := range worktrees {
		if worktree.branch != "" {
			occupiedBranches[worktree.branch] = worktree.path
		}
		if _, err := os.Stat(worktree.path); errors.Is(err, os.ErrNotExist) {
			if worktree.prunable {
				staleWorktrees = append(staleWorktrees, worktree)
				continue
			}
			result.UnavailableWorktrees = append(result.UnavailableWorktrees, worktree.path)
			continue
		} else if err != nil {
			result.UnavailableWorktrees = append(result.UnavailableWorktrees, worktree.path)
			result.Failures = append(result.Failures, fmt.Sprintf("inspect worktree path %q before removal: %v", worktree.path, err))
			continue
		}
		statusCmd := exec.CommandContext(ctx, "git", "-C", worktree.path, "status", "--porcelain", "--untracked-files=all")
		var statusOut bytes.Buffer
		statusCmd.Stdout = &statusOut
		if err := statusCmd.Run(); err != nil {
			result.UnavailableWorktrees = append(result.UnavailableWorktrees, worktree.path)
			result.Failures = append(result.Failures, fmt.Sprintf("inspect worktree %q before removal: %v", worktree.path, err))
			continue
		}
		if strings.TrimSpace(statusOut.String()) != "" {
			result.DirtyWorktrees = append(result.DirtyWorktrees, worktree.path)
		}
	}
	if len(staleWorktrees) > 0 {
		if err := exec.CommandContext(ctx, "git", "-C", path, "worktree", "prune", "--expire="+worktreePruneExpire).Run(); err != nil {
			result.Failures = append(result.Failures, fmt.Sprintf("prune expired worktree registrations: %v", err))
		} else {
			verifyCmd := exec.CommandContext(ctx, "git", "-C", path, "worktree", "list", "--porcelain", "--expire="+worktreePruneExpire)
			verifyOut, err := verifyCmd.Output()
			if err != nil {
				result.Failures = append(result.Failures, fmt.Sprintf("verify pruned worktree registrations: %v", err))
			} else {
				registered := make(map[string]bool)
				for _, line := range strings.Split(string(verifyOut), "\n") {
					if strings.HasPrefix(line, "worktree ") {
						registered[strings.TrimSpace(strings.TrimPrefix(line, "worktree "))] = true
					}
				}
				for _, worktree := range staleWorktrees {
					if registered[worktree.path] {
						result.UnavailableWorktrees = append(result.UnavailableWorktrees, worktree.path)
						continue
					}
					result.PrunedWorktrees = append(result.PrunedWorktrees, worktree.path)
					delete(occupiedBranches, worktree.branch)
				}
			}
		}
	}
	for _, worktree := range worktrees {
		if worktree.prunable || slices.Contains(result.UnavailableWorktrees, worktree.path) || slices.Contains(result.DirtyWorktrees, worktree.path) {
			continue
		}
		if err := exec.CommandContext(ctx, "git", "-C", path, "worktree", "remove", worktree.path).Run(); err != nil {
			result.Failures = append(result.Failures, fmt.Sprintf("remove clean worktree %q: %v", worktree.path, err))
			continue
		}
		result.RemovedWorktrees = append(result.RemovedWorktrees, worktree.path)
		delete(occupiedBranches, worktree.branch)
	}

	if ctx.Err() != nil {
		return result, ctx.Err()
	}

	// 3. Delete local non-default branches
	cmd := exec.CommandContext(ctx, "git", "-C", path, "branch", "--format=%(refname:short)")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return result, err
	}

	for _, b := range strings.Split(out.String(), "\n") {
		b = cleanBranchName(b)
		if b != "" && b != defaultBranch && b != currentBranch {
			if _, occupied := occupiedBranches[b]; occupied {
				result.ProtectedBranches = append(result.ProtectedBranches, b)
				continue
			}
			countCmd := exec.CommandContext(ctx, "git", "-C", path, "rev-list", "--count", b, "--not", "--remotes")
			countOut, err := countCmd.Output()
			if err != nil {
				result.Failures = append(result.Failures, fmt.Sprintf("inspect local-only commits on branch %q: %v", b, err))
				continue
			}
			unpushed, err := strconv.Atoi(strings.TrimSpace(string(countOut)))
			if err != nil {
				result.Failures = append(result.Failures, fmt.Sprintf("parse local-only commit count for branch %q: %v", b, err))
				continue
			}
			if unpushed > 0 {
				result.UnpushedBranches = append(result.UnpushedBranches, b)
				continue
			}
			delCmd := exec.CommandContext(ctx, "git", "-C", path, "branch", "-D", b)
			if err := delCmd.Run(); err != nil {
				result.Failures = append(result.Failures, fmt.Sprintf("delete local branch %q: %v", b, err))
				continue
			}
			result.DeletedBranches = append(result.DeletedBranches, b)
		}
	}
	if len(result.Failures) > 0 {
		return result, fmt.Errorf("prune completed partially: %s", strings.Join(result.Failures, "; "))
	}
	return result, nil
}

func sameExistingPath(left, right string) bool {
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	if leftErr == nil && rightErr == nil {
		return os.SameFile(leftInfo, rightInfo)
	}
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

// FetchOpenIssuesList retrieves open GitHub issues with a 6-second timeout context.
func FetchOpenIssuesList(org, ghRepo string) ([]IssueItem, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()

	target := fmt.Sprintf("%s/%s", org, ghRepo)
	cmd := exec.CommandContext(ctx, "gh", "issue", "list", "--repo", target, "--state", "open", "--limit", fmt.Sprintf("%d", defaultIssueLimit), "--json", "number,title,url")
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		if stderr := strings.TrimSpace(errOut.String()); stderr != "" {
			return nil, fmt.Errorf("%w: %s", err, stderr)
		}
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
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		if stderr := strings.TrimSpace(errOut.String()); stderr != "" {
			return nil, fmt.Errorf("%w: %s", err, stderr)
		}
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
// An empty result means "no open PR found"; a lookup failure is logged so it is
// not silently mistaken for that, since callers act on the distinction.
func FetchExistingPRURL(ctx context.Context, repoPath, branch string) string {
	if branch == "" || branch == "HEAD" {
		return ""
	}
	cmd := exec.CommandContext(ctx, "gh", "pr", "list", "--head", branch, "--state", "open", "--json", "url", "--jq", ".[0].url")
	cmd.Dir = repoPath
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		slog.Warn("gh pr list failed", "path", repoPath, "branch", branch, "error", err, "stderr", strings.TrimSpace(errOut.String()))
		return ""
	}
	return strings.TrimSpace(out.String())
}

// IsGitRepo checks if a directory is a valid git working tree.
func IsGitRepo(path string) bool {
	cmd := exec.Command("git", "-C", path, "rev-parse", "--is-inside-work-tree")
	return cmd.Run() == nil
}

func IsGitRepoContext(ctx context.Context, path string) bool {
	_, err := runner.Run(ctx, "git", "-C", path, "rev-parse", "--is-inside-work-tree")
	return err == nil
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

// ghDefaultBranch resolves a repo's default branch through each configured
// remote before falling back to gh's working-directory resolution. It is a
// package-level var so tests can fake this tier without spawning gh.
var ghDefaultBranch = func(ctx context.Context, path string) (string, error) {
	remoteOut, remoteErr := runner.Run(ctx, "git", "-C", path, "remote")
	if remoteErr == nil {
		names := strings.Fields(string(remoteOut))
		if originIndex := slices.Index(names, "origin"); originIndex > 0 {
			names[0], names[originIndex] = names[originIndex], names[0]
		}
		for _, name := range names {
			remoteURL, err := runner.Run(ctx, "git", "-C", path, "remote", "get-url", name)
			if err != nil {
				continue
			}
			target, err := githubRepoTarget(string(remoteURL))
			if err != nil {
				continue
			}
			out, err := runner.Run(ctx, "gh", "repo", "view", target, "--json", "defaultBranchRef", "--jq", ".defaultBranchRef.name")
			if err == nil && strings.TrimSpace(string(out)) != "" {
				return string(out), nil
			}
		}
	}

	cmd := exec.CommandContext(ctx, "gh", "repo", "view", "--json", "defaultBranchRef", "--jq", ".defaultBranchRef.name")
	cmd.Dir = path
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		if stderr := strings.TrimSpace(errOut.String()); stderr != "" {
			return "", fmt.Errorf("%w: %s", err, stderr)
		}
		return "", err
	}
	return out.String(), nil
}

func githubRepoTarget(remote string) (string, error) {
	remote = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(remote), ".git"))
	if strings.HasPrefix(remote, "git@") {
		parts := strings.SplitN(strings.TrimPrefix(remote, "git@"), ":", 2)
		if len(parts) == 2 && parts[0] != "" && strings.Count(strings.Trim(parts[1], "/"), "/") == 1 {
			path := strings.Trim(parts[1], "/")
			if parts[0] == "github.com" {
				return path, nil
			}
			return parts[0] + "/" + path, nil
		}
	}
	if parsed, err := url.Parse(remote); err == nil && parsed.Host != "" {
		path := strings.Trim(parsed.Path, "/")
		if strings.Count(path, "/") == 1 {
			if parsed.Host == "github.com" {
				return path, nil
			}
			return parsed.Host + "/" + path, nil
		}
	}
	return "", fmt.Errorf("remote %q is not a GitHub repository", remote)
}

// GetDefaultBranch determines default branch (main/master) for a git repository.
// ctx allows the caller to abort the lookup, including the networked gh tier.
func GetDefaultBranch(ctx context.Context, path string) string {
	return getDefaultBranch(ctx, path, true)
}

// GetDefaultBranchLocal avoids the networked GitHub fallback during bulk workspace scans.
func GetDefaultBranchLocal(ctx context.Context, path string) string {
	return getDefaultBranch(ctx, path, false)
}

// ResolveDefaultBranch obtains GitHub's current default branch. Destructive
// and publishing actions use this strict path instead of scan-time guesses.
func ResolveDefaultBranch(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	resolveCtx, cancel := context.WithTimeout(ctx, branchResolveTimeout)
	defer cancel()
	branch, err := ghDefaultBranch(resolveCtx, path)
	if err != nil {
		return "", fmt.Errorf("could not query GitHub's default branch; check 'gh auth status' and network access, then retry: %w", err)
	}
	branch = strings.TrimSpace(branch)
	if branch == "" || strings.EqualFold(branch, "HEAD") {
		return "", fmt.Errorf("GitHub returned an invalid default branch %q", branch)
	}
	return branch, nil
}

func localBranchExists(ctx context.Context, path, branch string) bool {
	ref := "refs/heads/" + branch
	_, err := runner.Run(ctx, "git", "-C", path, "show-ref", "--verify", "--quiet", ref)
	return err == nil
}

func ensureLocalDefaultBranch(ctx context.Context, path, branch string) error {
	if localBranchExists(ctx, path, branch) {
		return nil
	}
	fetchCtx, cancel := context.WithTimeout(ctx, orgFetchTimeout)
	defer cancel()
	refspec := branch + ":refs/heads/" + branch
	if _, err := runner.Run(fetchCtx, "git", "-C", path, "fetch", "origin", refspec); err != nil {
		return fmt.Errorf("GitHub default branch %q is not local and could not be fetched from origin: %w", branch, err)
	}
	if !localBranchExists(fetchCtx, path, branch) {
		return fmt.Errorf("GitHub default branch %q is still unavailable locally after fetch", branch)
	}
	return nil
}

func resolveCurrentBranch(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	out, err := runner.Run(ctx, "git", "-C", path, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve current branch: %w", err)
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" || strings.EqualFold(branch, "HEAD") {
		return "", fmt.Errorf("invalid current branch %q", branch)
	}
	ref := "refs/heads/" + branch
	if _, err := runner.Run(ctx, "git", "-C", path, "show-ref", "--verify", "--quiet", ref); err != nil {
		return "", fmt.Errorf("current branch %q has no local branch: %w", branch, err)
	}
	return branch, nil
}

func getDefaultBranch(ctx context.Context, path string, allowGitHub bool) string {
	if ctx.Err() != nil {
		return "HEAD"
	}

	out, err := runner.Run(ctx, "git", "-C", path, "symbolic-ref", "refs/remotes/origin/HEAD")
	if err == nil {
		ref := strings.TrimSpace(string(out))
		ref = strings.TrimPrefix(ref, "refs/remotes/origin/")
		ref = strings.TrimPrefix(ref, "origin/")
		if ref != "" {
			return ref
		}
	}

	if allowGitHub {
		if branch, err := ghDefaultBranch(ctx, path); err == nil {
			if branch = strings.TrimSpace(branch); branch != "" {
				return branch
			}
		} else {
			slog.Warn("gh repo view failed", "path", path, "error", err)
		}
	}

	if _, err := runner.Run(ctx, "git", "-C", path, "show-ref", "--verify", "--quiet", "refs/heads/main"); err == nil {
		return "main"
	}
	if _, err := runner.Run(ctx, "git", "-C", path, "show-ref", "--verify", "--quiet", "refs/heads/master"); err == nil {
		return "master"
	}

	return GetOriginalBranch(ctx, path)
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
//
// safeOnly restricts the sync to actions that never touch what branch is
// checked out: a plain pull on the default branch, or (if dirty) add+stash+
// pull+stash-apply. If the repo is on a feature branch, safeOnly skips it
// entirely rather than auto-switching to default or auto-rebasing — those
// stay available via an explicit, single-repo sync (safeOnly=false).
func SyncRepository(ctx context.Context, item *RepoItem, emit SyncProgress, safeOnly bool) {
	if ctx.Err() != nil {
		return
	}
	s := &syncSession{item: item, emit: emit}

	item.Status = StatusSyncing
	s.log("[%s] 󰓦 Starting sync for %s", time.Now().Format("15:04:05"), item.Name)

	origBranch := GetOriginalBranch(ctx, item.Path)
	item.OriginalBranch = origBranch
	item.CurrentBranch = origBranch
	defaultBranch := GetDefaultBranch(ctx, item.Path)
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
		defaultBranch = GetDefaultBranch(ctx, item.Path)
		item.OriginalBranch = origBranch
		item.CurrentBranch = origBranch
		item.DefaultBranch = defaultBranch
		item.BranchDetails = GetRepoBranchDetails(ctx, item.Path, defaultBranch)
		s.finish(StatusCloned, "Cloned", "󰄬 Successfully cloned into '%s' (%s).", item.Path, defaultBranch)
		return
	}

	item.BranchDetails = GetRepoBranchDetails(ctx, item.Path, defaultBranch)

	if origBranch != defaultBranch {
		item.ExistingPRURL = FetchExistingPRURL(ctx, item.Path, origBranch)
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

		// Best-effort: the stash below is what actually has to succeed.
		if err := exec.CommandContext(ctx, "git", "-C", item.Path, "add", ".").Run(); err != nil {
			s.log("󰀪 git add . failed (continuing): %v", err)
		}
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

	if safeOnly {
		s.finish(StatusSkipped, "Skipped", "󰒲 On feature branch '%s'; run [r] Sync to update manually.", origBranch)
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

	// Best-effort: the rebase below still runs against whatever refs are local.
	if err := exec.CommandContext(ctx, "git", "-C", item.Path, "fetch", "origin").Run(); err != nil {
		s.log("󰀪 git fetch origin failed (continuing): %v", err)
	}
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
		if abortErr := exec.CommandContext(abortCtx, "git", "-C", item.Path, "rebase", "--abort").Run(); abortErr != nil {
			s.log("󰀪 git rebase --abort failed — repository may be left mid-rebase: %v", abortErr)
		}
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
	if item == nil {
		return fmt.Errorf("repository is required")
	}
	defaultBranch, err := ResolveDefaultBranch(ctx, item.Path)
	if err != nil {
		return err
	}
	knownDefault := strings.TrimSpace(item.DefaultBranch)
	if knownDefault == "" || strings.EqualFold(knownDefault, "HEAD") {
		return fmt.Errorf("displayed default branch is unresolved; sync the repository and review it before publishing")
	}
	if knownDefault != defaultBranch {
		return fmt.Errorf("default branch changed from %q to %q; sync the repository and confirm publishing again", knownDefault, defaultBranch)
	}
	if err := ensureLocalDefaultBranch(ctx, item.Path, defaultBranch); err != nil {
		return err
	}
	branch, err := resolveCurrentBranch(ctx, item.Path)
	if err != nil {
		return err
	}
	if item.OriginalBranch != branch {
		item.ExistingPRURL = ""
	}
	item.DefaultBranch = defaultBranch
	item.CurrentBranch = branch
	item.OriginalBranch = branch
	if branch == "" || branch == item.DefaultBranch {
		return fmt.Errorf("cannot raise PR from default branch")
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	item.Logs = append(item.Logs, fmt.Sprintf("󰏫 Committing and pushing branch '%s' to raise/update PR...", branch))

	// add/commit are best-effort: if there's nothing to commit (or the commit
	// otherwise no-ops), we still want to push whatever's already committed.
	// Failures are logged rather than swallowed so they aren't invisible.
	if err := exec.CommandContext(ctx, "git", "-C", item.Path, "add", "-A").Run(); err != nil {
		item.Logs = append(item.Logs, fmt.Sprintf("󰀪 git add -A failed (continuing): %v", err))
	}
	commitMsg := fmt.Sprintf("WIP: Updates on branch '%s'", branch)
	if err := exec.CommandContext(ctx, "git", "-C", item.Path, "commit", "-m", commitMsg).Run(); err != nil {
		item.Logs = append(item.Logs, fmt.Sprintf("󰀪 git commit failed or had nothing to commit (continuing): %v", err))
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	pushCmd := exec.CommandContext(ctx, "git", "-C", item.Path, "push", "-u", "origin", branch)
	var pushOut bytes.Buffer
	pushCmd.Stdout = &pushOut
	pushCmd.Stderr = &pushOut
	if err := pushCmd.Run(); err != nil {
		// git reports rejections, auth failures and hook output on stderr; without
		// capturing it the caller only ever sees "exit status 1".
		detail := strings.TrimSpace(pushOut.String())
		item.Logs = append(item.Logs, fmt.Sprintf("󰅙 git push error: %v — %s", err, detail))
		if detail != "" {
			return fmt.Errorf("git push failed: %w: %s", err, detail)
		}
		return fmt.Errorf("git push failed: %w", err)
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
		if err := prCmd.Run(); err != nil {
			item.Logs = append(item.Logs, fmt.Sprintf("󰅙 gh pr create failed: %v — %s", err, strings.TrimSpace(prOut.String())))
			return fmt.Errorf("gh pr create failed: %w: %s", err, strings.TrimSpace(prOut.String()))
		}
		prURL := strings.TrimSpace(prOut.String())
		item.DraftPRURL = prURL
		item.ExistingPRURL = prURL
		item.Logs = append(item.Logs, fmt.Sprintf("󰄬 PR created: %s", prURL))
	} else {
		item.Logs = append(item.Logs, fmt.Sprintf("󰄬 Pushed commits to existing PR: %s", item.ExistingPRURL))
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	coCmd := exec.CommandContext(ctx, "git", "-C", item.Path, "checkout", item.DefaultBranch)
	if err := coCmd.Run(); err != nil {
		item.Logs = append(item.Logs, fmt.Sprintf("󰅙 Failed to switch back to default branch '%s': %v", item.DefaultBranch, err))
		return fmt.Errorf("failed to checkout '%s': %w", item.DefaultBranch, err)
	}
	item.CurrentBranch = item.DefaultBranch
	item.Status = StatusPRCreated
	item.StatusMsg = "PR Raised"
	item.Logs = append(item.Logs, fmt.Sprintf("󰄬 Switched back to default branch '%s'.", item.DefaultBranch))

	return nil
}

// CloneRepo clones a repository from GitHub organization into local path.
func CloneRepo(org, ghRepoName, targetPath string) error {
	cmd := exec.Command("gh", "repo", "clone", fmt.Sprintf("%s/%s", org, ghRepoName), targetPath)
	return cmd.Run()
}

// DeleteLocalRepo removes the local directory for an archived repository.
func DeleteLocalRepo(workspace, path string) error {
	if err := ValidateWorkspacePath(workspace, path); err != nil {
		return err
	}
	return os.RemoveAll(path)
}

// ValidateWorkspacePath rejects paths outside workspace, including existing symlinks.
func ValidateWorkspacePath(workspace, path string) error {
	root, err := filepath.Abs(workspace)
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve workspace: %w", err)
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(target); err == nil {
		target = resolved
	} else if os.IsNotExist(err) {
		parent, parentErr := filepath.EvalSymlinks(filepath.Dir(target))
		if parentErr != nil {
			return fmt.Errorf("resolve target parent: %w", parentErr)
		}
		target = filepath.Join(parent, filepath.Base(target))
	} else {
		return fmt.Errorf("resolve target: %w", err)
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("refusing path outside workspace: %s", path)
	}
	return nil
}

// ScanLocalDirectory returns names of all direct subdirectories in target path.
func ScanLocalDirectory(targetDir string) ([]string, error) {
	return scanLocalDirectory(context.Background(), targetDir)
}

func scanLocalDirectory(ctx context.Context, targetDir string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := os.Open(targetDir)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := dir.Close(); closeErr != nil {
			slog.Debug("close local directory scan", "path", targetDir, "error", closeErr)
		}
	}()

	var dirs []string
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries, err := dir.ReadDir(64)
		for _, entry := range entries {
			if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
				dirs = append(dirs, entry.Name())
			}
		}
		if errors.Is(err, io.EOF) {
			sort.Strings(dirs)
			return dirs, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

type directoryScan struct {
	done    chan struct{}
	entries []string
	err     error
	started time.Time
}

var directoryScans = struct {
	sync.Mutex
	active    map[string]*directoryScan
	abandoned map[string]int
}{active: make(map[string]*directoryScan), abandoned: make(map[string]int)}

// ErrDirectoryScanInProgress means an earlier filesystem scan is still
// blocked. Joining it would make an explicit retry wait for the same work.
var ErrDirectoryScanInProgress = errors.New("local directory scan already in progress")

// ErrDirectoryScanStuck means a filesystem scan exceeded the recovery window
// and was abandoned so a later refresh can start a new attempt.
var ErrDirectoryScanStuck = errors.New("local directory scan appears stuck")

// ErrDirectoryScanUnresponsive means repeated filesystem scans have wedged;
// no more are started for that path during this process.
var ErrDirectoryScanUnresponsive = errors.New("workspace directory is unresponsive")

const directoryScanStuckAfter = 5 * time.Minute
const maxAbandonedDirectoryScans = 2

// ResetLocalDirectoryScanFailures allows an explicit user retry after the
// abandoned-scan cap has stopped automatic retries. It does not disturb a scan
// that is still registered as active.
func ResetLocalDirectoryScanFailures(targetDir string) {
	directoryScans.Lock()
	defer directoryScans.Unlock()
	if directoryScans.active[targetDir] == nil {
		delete(directoryScans.abandoned, targetDir)
	}
}

func finishDirectoryScan(targetDir string, scan *directoryScan, entries []string, err error) {
	directoryScans.Lock()
	defer directoryScans.Unlock()
	scan.entries = entries
	scan.err = err
	if directoryScans.active[targetDir] == scan {
		delete(directoryScans.active, targetDir)
	}
	// A successful completion proves the path recovered, even if this was an
	// older scan that had already been abandoned in favour of a new attempt.
	if err == nil {
		delete(directoryScans.abandoned, targetDir)
	}
	close(scan.done)
}

func ScanLocalDirectoryContext(ctx context.Context, targetDir string) ([]string, error) {
	directoryScans.Lock()
	scan := directoryScans.active[targetDir]
	if scan == nil && directoryScans.abandoned[targetDir] >= maxAbandonedDirectoryScans {
		directoryScans.Unlock()
		return nil, fmt.Errorf("%w: %s", ErrDirectoryScanUnresponsive, targetDir)
	}
	if scan != nil {
		stuck := time.Since(scan.started) >= directoryScanStuckAfter
		if stuck {
			delete(directoryScans.active, targetDir)
			directoryScans.abandoned[targetDir]++
		}
		abandoned := directoryScans.abandoned[targetDir]
		directoryScans.Unlock()
		if stuck {
			if abandoned >= maxAbandonedDirectoryScans {
				return nil, fmt.Errorf("%w after %d abandoned scans: %s", ErrDirectoryScanUnresponsive, abandoned, targetDir)
			}
			return nil, fmt.Errorf("%w: %s", ErrDirectoryScanStuck, targetDir)
		}
		return nil, fmt.Errorf("%w: %s", ErrDirectoryScanInProgress, targetDir)
	}
	scan = &directoryScan{done: make(chan struct{}), started: time.Now()}
	directoryScans.active[targetDir] = scan
	go func() {
		entries, err := scanLocalDirectory(ctx, targetDir)
		finishDirectoryScan(targetDir, scan, entries, err)
	}()
	directoryScans.Unlock()
	select {
	case <-ctx.Done():
		slog.Warn("local directory scan still in progress", "path", targetDir, "error", ctx.Err())
		return nil, ctx.Err()
	case <-scan.done:
		return scan.entries, scan.err
	}
}
