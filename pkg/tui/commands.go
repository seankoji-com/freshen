package tui

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/seankoji-com/freshen/pkg/git"
	"github.com/seankoji-com/freshen/pkg/jobs"
)

func (m Model) loadRunnersCmd() tea.Cmd {
	return func() tea.Msg {
		runners, err := jobs.FetchOrgRunners(m.TargetOrg)
		return loadedRunnersMsg{runners: runners, err: err}
	}
}

type loadedJobQueueMsg struct {
	queue []*jobs.JobItem
	err   error
}

func (m Model) loadJobQueueCmd() tea.Cmd {
	// Collect repo names from loaded repos
	repos := make([]string, 0, len(m.Repos))
	for _, r := range m.Repos {
		repoName := r.GHRepoName
		if repoName == "" {
			repoName = r.Name
		}
		if !r.IsArchived && repoName != "" {
			repos = append(repos, repoName)
		}
	}
	return func() tea.Msg {
		// If repos not loaded yet, fetch from org API inside the closure
		// to avoid blocking the main goroutine (was a main-thread-blocking bug).
		repoList := repos
		if len(repoList) == 0 {
			orgRepos, err := git.FetchOrgRepos(m.TargetOrg)
			if err == nil {
				for _, r := range orgRepos {
					if !r.IsArchived {
						repoList = append(repoList, r.Name)
					}
				}
			}
		}
		queue, err := jobs.FetchOrgJobQueue(m.TargetOrg, repoList)
		return loadedJobQueueMsg{queue: queue, err: err}
	}
}

type loadedJobLogsMsg struct {
	jobID   string // JobItem.ID to match
	ghJobID int64
	logs    []string
	err     error
}

// loadJobLogsCmd fetches real log lines for the given running job.
func (m Model) loadJobLogsCmd(job *jobs.JobItem) tea.Cmd {
	if job == nil || job.RunID == 0 {
		return nil
	}
	org := m.TargetOrg
	repo := job.Repo
	runID := job.RunID
	ghJobID := job.GHJobID
	jobName := job.Name
	jobID := job.ID
	return func() tea.Msg {
		lines, resolvedGHJobID, err := jobs.FetchJobLogs(org, repo, runID, ghJobID, jobName, 200)
		return loadedJobLogsMsg{jobID: jobID, ghJobID: resolvedGHJobID, logs: lines, err: err}
	}
}

func (m Model) loadOrgReposCmd(autoSync bool) tea.Cmd {
	return func() tea.Msg {
		if m.TargetOrg == "" {
			entries, err := git.ScanLocalDirectory(m.TargetDir)
			if err != nil {
				return orgSyncedMsg{err: err, autoSync: autoSync}
			}
			repos := make([]*git.RepoItem, 0, len(entries))
			for _, name := range entries {
				path := filepath.Join(m.TargetDir, name)
				if git.IsGitRepo(path) {
					repos = append(repos, &git.RepoItem{Name: name, Path: path, Status: git.StatusPending, Logs: []string{}})
				}
			}
			return orgSyncedMsg{repos: repos, autoSync: false}
		}
		orgRepos, err := git.FetchOrgRepos(m.TargetOrg)
		if err != nil {
			return orgSyncedMsg{repos: nil, err: err, autoSync: autoSync}
		}

		orgCounts, countsErr := git.FetchOrgRepoCounts(m.TargetOrg)
		if countsErr != nil {
			slog.Debug("org repo counts fetch failed", "org", m.TargetOrg, "error", countsErr)
		}

		entries, err := git.ScanLocalDirectory(m.TargetDir)
		if err != nil {
			return orgSyncedMsg{repos: nil, err: err, autoSync: autoSync}
		}

		repoMap := make(map[string]*git.RepoItem)

		for _, ghRepo := range orgRepos {
			localDir, ok := git.GetLocalDirName(ghRepo.Name)
			if !ok {
				// An unusable name must never be joined onto TargetDir: the join would
				// resolve to TargetDir itself, so the item's Path — which delete and
				// sync act on — would point at the whole repos root.
				slog.Warn("skipping repo with no safe local directory name", "repo", ghRepo.Name)
				continue
			}
			localPath := filepath.Join(m.TargetDir, localDir)

			localExists := false
			if stat, err := os.Stat(localPath); err == nil && stat.IsDir() {
				localExists = true
			}

			if ghRepo.IsArchived && !localExists {
				continue
			}

			item := &git.RepoItem{
				Name:       localDir,
				GHRepoName: ghRepo.Name,
				Path:       localPath,
				URL:        ghRepo.URL,
				IsArchived: ghRepo.IsArchived,
				IsNew:      !localExists,
				Status:     git.StatusPending,
				Logs:       make([]string, 0),
			}

			if git.IsGitRepo(localPath) {
				item.CurrentBranch = git.GetOriginalBranch(m.ctx, localPath)
				item.DefaultBranch = git.GetDefaultBranch(m.ctx, localPath)
			}

			if counts, found := orgCounts[ghRepo.Name]; found {
				item.OpenIssuesCount = counts.Issues
				item.OpenPRsCount = counts.PRs
				item.HasLoadedCounts = true
			}

			if ghRepo.IsArchived {
				item.Status = git.StatusArchived
				item.StatusMsg = "Archived"
			}

			repoMap[localDir] = item
		}

		for _, name := range entries {
			if _, exists := repoMap[name]; !exists {
				path := filepath.Join(m.TargetDir, name)
				if git.IsGitRepo(path) {
					item := &git.RepoItem{
						Name:          name,
						GHRepoName:    git.GetGHRepoName(name),
						Path:          path,
						URL:           fmt.Sprintf("https://github.com/%s/%s", m.TargetOrg, git.GetGHRepoName(name)),
						CurrentBranch: git.GetOriginalBranch(m.ctx, path),
						DefaultBranch: git.GetDefaultBranch(m.ctx, path),
						Status:        git.StatusPending,
						Logs:          make([]string, 0),
					}

					if counts, found := orgCounts[item.GHRepoName]; found {
						item.OpenIssuesCount = counts.Issues
						item.OpenPRsCount = counts.PRs
						item.HasLoadedCounts = true
					}

					repoMap[name] = item
				}
			}
		}

		result := make([]*git.RepoItem, 0, len(repoMap))
		for _, item := range repoMap {
			result = append(result, item)
		}

		sort.Slice(result, func(i, j int) bool {
			return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
		})

		return orgSyncedMsg{repos: result, err: nil, autoSync: autoSync}
	}
}

func (m Model) fetchIssuesCmd(repoName, ghRepoName string) tea.Cmd {
	return func() tea.Msg {
		issues, err := git.FetchOpenIssuesList(m.TargetOrg, ghRepoName)
		return loadedIssuesMsg{repoName: repoName, issues: issues, err: err}
	}
}

func (m Model) fetchPRsCmd(repoName, ghRepoName string) tea.Cmd {
	return func() tea.Msg {
		prs, err := git.FetchOpenPRsList(m.TargetOrg, ghRepoName)
		return loadedPRsMsg{repoName: repoName, prs: prs, err: err}
	}
}

// bgGuard wraps task with bgWG tracking: Add(1) runs immediately (on the
// caller's goroutine, before any `go` statement), and Done() is deferred
// inside the returned function so it always fires once task completes,
// whether that function is invoked inline or launched in a new goroutine.
// This is the single choke point background git tasks must go through so
// the bgWG guard can't be skipped at a launch site.
func (m Model) bgGuard(task func()) func() {
	if m.bgWG != nil {
		m.bgWG.Add(1)
	}
	return func() {
		if m.bgWG != nil {
			defer m.bgWG.Done()
		}
		task()
	}
}

// syncConcurrency bounds how many repositories are synced at once.
const syncConcurrency = 4

// syncRepositoryFn abstracts git.SyncRepository behind a package-level func
// var so tests can control per-repo sync timing and observe the worker
// pool's concurrency limit and cancellation behavior in startSyncCmd without
// touching real git repos on disk.
var syncRepositoryFn = git.SyncRepository

// startSyncCmd syncs the given repositories in the background and streams their
// state back into the update loop, one message per state change.
//
// The workers never touch the model's RepoItems: each gets a private clone and
// publishes owned snapshots. Only Update — which Bubble Tea runs on a single
// goroutine — writes to m.Repos, so the render path never reads a struct while
// a sync is writing to it.
func (m Model) startSyncCmd(items []*git.RepoItem, bulk bool) tea.Cmd {
	// Clone here, on the update goroutine, before any worker exists.
	work := make([]*git.RepoItem, 0, len(items))
	for _, item := range items {
		if item.IsArchived {
			continue
		}
		work = append(work, item.Clone())
	}
	if len(work) == 0 {
		return func() tea.Msg { return syncFinishedMsg{bulk: bulk} }
	}

	ctx := m.ctx
	snapshots := make(chan *git.RepoItem, len(work))

	// bgGuard keeps the shutdown accounting at a single choke point: Add(1)
	// happens here on the update goroutine, Done() once the stream closes.
	run := m.bgGuard(func() {
		defer close(snapshots)

		var wg sync.WaitGroup
		concurrency := m.Concurrency
		if concurrency <= 0 {
			concurrency = syncConcurrency
		}
		sem := make(chan struct{}, concurrency)

	producer:
		for _, r := range work {
			if ctx.Err() != nil {
				break producer
			}

			// Acquiring must stay cancellable: a wedged worker would
			// otherwise block this loop forever, so the ctx check above is
			// never reached again and bgGuard's Done() never fires.
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				break producer
			}
			wg.Add(1)

			go func(r *git.RepoItem) {
				defer wg.Done()
				defer func() { <-sem }()

				syncRepositoryFn(ctx, r, func(snapshot *git.RepoItem) {
					// Give up on quit rather than block on an undrained channel.
					select {
					case snapshots <- snapshot:
					case <-ctx.Done():
					}
				})
			}(r)
		}

		wg.Wait()
	})
	go run()

	return waitForSyncSnapshot(snapshots, bulk)
}

// waitForSyncSnapshot turns the next snapshot on the stream into a message,
// or reports the sync finished once the stream closes.
func waitForSyncSnapshot(snapshots <-chan *git.RepoItem, bulk bool) tea.Cmd {
	return func() tea.Msg {
		snapshot, ok := <-snapshots
		if !ok {
			return syncFinishedMsg{bulk: bulk}
		}
		return repoSyncMsg{repo: snapshot, snapshots: snapshots, bulk: bulk}
	}
}

// applyRepoSnapshot copies the sync-owned fields of a snapshot onto the live
// model item. It runs on the update goroutine — the only writer of m.Repos —
// and leaves issue/PR state alone, since other commands own that.
func (m *Model) applyRepoSnapshot(snapshot *git.RepoItem) {
	if snapshot == nil {
		return
	}
	for _, item := range m.Repos {
		if item.Name != snapshot.Name {
			continue
		}
		item.Status = snapshot.Status
		item.StatusMsg = snapshot.StatusMsg
		item.OriginalBranch = snapshot.OriginalBranch
		item.CurrentBranch = snapshot.CurrentBranch
		item.DefaultBranch = snapshot.DefaultBranch
		item.HasUnstagedChanges = snapshot.HasUnstagedChanges
		item.ExistingPRURL = snapshot.ExistingPRURL
		item.BranchDetails = snapshot.BranchDetails
		item.Stashed = snapshot.Stashed
		item.DraftPRURL = snapshot.DraftPRURL
		item.ErrorErr = snapshot.ErrorErr
		item.Logs = snapshot.Logs
		return
	}
}

func copyToClipboard(text string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbcopy")
	case "windows":
		cmd = exec.Command("clip")
	default:
		if _, err := exec.LookPath("wl-copy"); err == nil {
			cmd = exec.Command("wl-copy")
		} else if _, err := exec.LookPath("xclip"); err == nil {
			cmd = exec.Command("xclip", "-selection", "clipboard")
		} else if _, err := exec.LookPath("xsel"); err == nil {
			cmd = exec.Command("xsel", "--clipboard", "--input")
		} else {
			return fmt.Errorf("no clipboard command found (install wl-clipboard, xclip, or xsel)")
		}
	}
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

// noteFetchFailure records a consecutive runner/job-queue fetch failure and
// logs the moment the poll cadence first widens past its baseline.
func (m *Model) noteFetchFailure(source string, base time.Duration) {
	if m.ConsecutiveErrors == nil {
		m.ConsecutiveErrors = make(map[string]int)
	}
	m.ConsecutiveErrors[source]++
	streak := m.ConsecutiveErrors[source]
	if next := backoffInterval(base, streak); next > base {
		slog.Warn("backing off polls after consecutive fetch failures",
			"source", source, "consecutiveErrors", streak, "interval", next)
	}
}

// noteFetchSuccess clears this source's failure streak, logging the return to
// the baseline poll cadence when a backoff was in effect. Streaks for other
// sources are left alone so one healthy endpoint cannot cancel the backoff a
// different, still-failing endpoint has earned.
func (m *Model) noteFetchSuccess(source string) {
	if streak := m.ConsecutiveErrors[source]; streak > 0 {
		slog.Warn("resuming baseline poll interval after successful fetch",
			"source", source, "consecutiveErrors", streak)
	}
	// delete rather than assign: it is a no-op on a nil map.
	delete(m.ConsecutiveErrors, source)
}

// setToast sets a toast message with the given priority. Higher priority toasts
// (error=2) override lower ones (info=1), and equal priorities replace.
// Priority 0 clears on next keypress.
func (m *Model) setToast(msg string, priority int) {
	if priority >= m.ToastPriority {
		m.ToastMsg = msg
		m.ToastPriority = priority
	}
}
