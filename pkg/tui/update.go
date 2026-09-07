package tui

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/seankoji-com/freshen/pkg/config"
	"github.com/seankoji-com/freshen/pkg/git"
	"github.com/seankoji-com/freshen/pkg/jobs"
)

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case repoDetailsLoadedMsg:
		m.RepoDetailLoading = ""
		for _, r := range m.Repos {
			if r.Path == msg.repo.Path {
				r.BranchDetails = msg.repo.BranchDetails
				break
			}
		}
		m.updateViewport()
	case runJobsLoadedMsg:
		m.receiveRunJobs(msg)
		if m.OpenRun != nil && !m.OpenRun.JobsKnown && m.OpenRun.JobsError == "" {
			cmds = append(cmds, m.loadOpenRun())
		}
	case actionResultMsg:
		m.receiveAction(msg)
	case tea.KeyMsg:
		cmd, early := m.handleKeyMsg(msg)
		if early {
			return m, cmd
		}
		cmds = append(cmds, cmd)

	case loadedIssuesMsg:
		m.handleLoadedIssuesMsg(msg)

	case loadedPRsMsg:
		m.handleLoadedPRsMsg(msg)

	case tea.MouseMsg:
		cmds = append(cmds, m.handleMouseMsg(msg))

	case repoTickMsg:
		return m, m.handleRepoTickMsg()

	case runnerJobTickMsg:
		return m, m.handleRunnerJobTickMsg()

	case jobQueueTickMsg:
		return m, m.handleJobQueueTickMsg()

	case loadedRunnersMsg:
		m.handleLoadedRunnersMsg(msg)

	case loadedJobQueueMsg:
		cmds = append(cmds, m.handleLoadedJobQueueMsg(msg))

	case loadedJobLogsMsg:
		m.handleLoadedJobLogsMsg(msg)
		if m.OpenJobID != "" && m.OpenJobID != msg.jobID {
			cmds = append(cmds, m.fetchOpenJobLogs())
		}

	case orgSyncedMsg:
		cmd, early := m.handleOrgSyncedMsg(msg)
		if early {
			return m, cmd
		}
		cmds = append(cmds, cmd)

	case repoSyncMsg:
		cmds = append(cmds, m.handleRepoSyncMsg(msg))

	case syncFinishedMsg:
		m.handleSyncFinishedMsg(msg)

	case tea.WindowSizeMsg:
		m.handleWindowSizeMsg(msg)
	}

	var spinnerCmd tea.Cmd
	m.Spinner, spinnerCmd = m.Spinner.Update(msg)
	cmds = append(cmds, spinnerCmd)

	return m, tea.Batch(cmds...)
}

func (m *Model) handleLoadedIssuesMsg(msg loadedIssuesMsg) {
	if msg.err != nil {
		m.setToast(fmt.Sprintf("Issues unavailable: %v. Press r to retry.", msg.err), 2)
	}
	for _, item := range m.Repos {
		if item.Name == msg.repoName {
			item.IsLoadingIssues = false
			item.HasLoadedIssues = msg.err == nil
			if msg.err == nil && msg.issues != nil {
				item.IssuesList = msg.issues
			}
			break
		}
	}
	m.updateViewport()
}

func (m *Model) handleLoadedPRsMsg(msg loadedPRsMsg) {
	if msg.err != nil {
		m.setToast(fmt.Sprintf("Pull requests unavailable: %v. Press r to retry.", msg.err), 2)
	}
	for _, item := range m.Repos {
		if item.Name == msg.repoName {
			item.IsLoadingPRs = false
			item.HasLoadedPRs = msg.err == nil
			if msg.err == nil && msg.prs != nil {
				item.PRsList = msg.prs
			}
			break
		}
	}
	m.updateViewport()
}

func (m *Model) handleMouseMsg(msg tea.MouseMsg) tea.Cmd { return m.screenMouse(msg) }

func (m *Model) handleRepoTickMsg() tea.Cmd {
	cmds := []tea.Cmd{repoTickCmd()}
	if refresh := m.startOrgRefresh(false, false); refresh != nil {
		cmds = append([]tea.Cmd{refresh}, cmds...)
	}
	return tea.Batch(cmds...)
}

func (m *Model) handleRunnerJobTickMsg() tea.Cmd {
	jobs.PollStep(m.Runners, m.JobQueue)
	m.updateViewport()
	if m.TargetOrg == "" {
		// Owner was cleared mid-session (e.g. an unrecognized org); nothing
		// left to poll, so stop the tick chain rather than retry forever.
		return nil
	}
	var cmdsToAdd []tea.Cmd
	if !m.RunnerPermissionDenied {
		if !m.IsRunnersLoading {
			m.IsRunnersLoading = true
			cmdsToAdd = append(cmdsToAdd, m.loadRunnersCmd())
		}
	}
	cmdsToAdd = append(cmdsToAdd, runnerJobTickCmd(backoffInterval(runnerJobTickInterval, m.ConsecutiveErrors[fetchSourceRunners])))
	// Refresh logs for selected running job
	if m.ActiveFocus == FocusJobs {
		if logsCmd := m.loadLogsIfSelectedJobRunning(); logsCmd != nil {
			cmdsToAdd = append(cmdsToAdd, logsCmd)
		}
	}
	return tea.Batch(cmdsToAdd...)
}

// handleJobQueueTickMsg refreshes the job queue on its own slower cadence,
// keeping the per-repo API cost off the 10s runner tick.
func (m *Model) handleJobQueueTickMsg() tea.Cmd {
	if m.TargetOrg == "" {
		return nil
	}
	if m.IsJobQueueLoading {
		return jobQueueTickCmd(m.actionsPollInterval())
	}
	m.IsJobQueueLoading = true
	return tea.Batch(m.loadJobQueueCmd(), jobQueueTickCmd(backoffInterval(m.actionsPollInterval(), m.ConsecutiveErrors[fetchSourceJobQueue])))
}

func (m *Model) handleLoadedRunnersMsg(msg loadedRunnersMsg) {
	m.IsRunnersLoading = false
	if msg.err != nil {
		m.RunnerFetchFailed = true
		if isRunnerPermissionError(msg.err) {
			// Runner listing needs org-admin scope; fall back to inferring
			// runners from the job queue instead of nagging with toasts.
			m.RunnerPermissionDenied = true
			slog.Debug("runner fetch forbidden (org admin permissions required)", "org", m.TargetOrg, "error", msg.err)
			if len(m.Runners) == 0 && len(m.JobQueue) > 0 {
				m.Runners = extractRunnersFromJobQueue(m.JobQueue, m.Runners)
			}
		} else {
			m.noteFetchFailure(fetchSourceRunners, runnerJobTickInterval)
			slog.Error("runner fetch failed", "org", m.TargetOrg, "error", msg.err)
			m.setToast(fmt.Sprintf(" ⚠ Runner fetch failed: %v", msg.err), 2)
		}
	} else {
		m.RunnerFetchFailed = false
		m.RunnerPermissionDenied = false
		m.noteFetchSuccess(fetchSourceRunners)
		// Always update runners, even if empty
		merged := jobs.MergeRunners(msg.runners, m.Runners, m.JobQueue)
		m.Runners = merged
		if key := m.ScreenCursor[FocusRunners]; key != "" {
			found := false
			for i, r := range m.getMatchingRunners() {
				if r.ID == key {
					m.SelectedRunnerIndex = i
					found = true
					break
				}
			}
			if !found && m.ActiveFocus == FocusRunners {
				m.Detail = false
				m.SelectedRunnerIndex = 0
			}
		}

		// Runner capacity never invents jobs or run identities.
		m.updateViewport()
	}
}

func (m *Model) handleLoadedJobQueueMsg(msg loadedJobQueueMsg) tea.Cmd {
	m.IsJobQueueLoading = false
	if msg.err != nil {
		m.JobQueueFetchFailed = true
		var partial *jobs.QueueFetchError
		m.ActionsCoverage = msg.err.Error()
		if errors.As(msg.err, &partial) {
			slog.Warn("Actions coverage incomplete", "unavailable", partial.Failed, "total", partial.Total, "error", partial.Cause)
		}
		if partial != nil && partial.Partial && partial.Total > 0 && partial.Failed*4 < partial.Total {
			m.noteFetchSuccess(fetchSourceJobQueue)
		} else {
			m.noteFetchFailure(fetchSourceJobQueue, m.actionsPollInterval())
			slog.Error("job queue fetch failed", "org", m.TargetOrg, "error", msg.err)
			m.setToast(fmt.Sprintf("Actions fetch failed: %v", msg.err), 2)
		}
		var cmd tea.Cmd
		if len(msg.queue) > 0 {
			m.processJobQueueUpdate(msg.queue, msg.history)
			if len(m.Runners) == 0 || m.RunnerPermissionDenied {
				m.Runners = extractRunnersFromJobQueue(msg.queue, m.Runners)
			}
			cmd = m.triggerLogFetchForSelectedJob()
		}
		m.updateViewport()
		return cmd
	}

	m.JobQueueFetchFailed = false
	m.ActionsCoverage = ""
	m.LastActionsRefresh = time.Now()
	m.noteFetchSuccess(fetchSourceJobQueue)
	m.processJobQueueUpdate(msg.queue, msg.history)
	if len(m.Runners) == 0 || m.RunnerPermissionDenied {
		m.Runners = extractRunnersFromJobQueue(msg.queue, m.Runners)
	}
	cmd := m.triggerLogFetchForSelectedJob()
	if m.OpenRun != nil && !m.OpenRun.JobsKnown {
		cmd = tea.Batch(cmd, m.loadOpenRun())
	}
	m.updateViewport()
	return cmd
}

func (m *Model) handleLoadedJobLogsMsg(msg loadedJobLogsMsg) {
	if m.LogLoading == msg.jobID {
		m.LogLoading = ""
	}
	if msg.err != nil {
		slog.Debug("log fetch failed", "jobID", msg.jobID, "error", msg.err)
	}
	if len(msg.logs) > 0 {
		for _, j := range m.JobQueue {
			if j.ID == msg.jobID {
				j.Logs = msg.logs
				j.GHJobID = msg.ghJobID
				break
			}
		}
		m.updateViewport()
	} else if msg.err != nil {
		for _, j := range m.JobQueue {
			if j.ID == msg.jobID {
				j.Logs = []string{"[log fetch failed: " + msg.err.Error() + "]"}
				break
			}
		}
		m.updateViewport()
	}
}

func (m *Model) handleOrgSyncedMsg(msg orgSyncedMsg) (tea.Cmd, bool) {
	if msg.generation != 0 && msg.generation != m.OrgRefreshGeneration {
		return nil, true
	}
	m.IsOrgSyncing = false
	m.OrgSyncStartedAt = time.Time{}
	pendingRefresh := m.PendingOrgRefresh
	m.PendingOrgRefresh = false
	notifyCountsErr := m.NotifyOrgCountsError && !pendingRefresh
	if !pendingRefresh {
		m.NotifyOrgCountsError = false
	}
	if msg.err != nil {
		m.OrgRefreshFailed = true
		slog.Error("org repos fetch failed", "org", m.TargetOrg, "error", msg.err)
		if isUnknownOwnerError(msg.err) {
			pendingRefresh = false
			m.NotifyOrgCountsError = false
			badOwner := m.TargetOrg
			m.TargetOrg = ""
			m.IsRunnersLoading = false
			m.IsJobQueueLoading = false
			if clearErr := clearConfiguredOwner(badOwner); clearErr != nil {
				slog.Error("failed to clear invalid owner from config", "owner", badOwner, "error", clearErr)
			}
			m.setToast(fmt.Sprintf(" %s GitHub owner %q not found — cleared. Restart freshen to set a new one.", iconError, badOwner), 3)
		} else {
			m.setToast(fmt.Sprintf(" %s Fetch failed: %v. Check 'gh auth status'.", iconError, msg.err), 2)
		}
		m.updateViewport()
		if pendingRefresh {
			return m.startOrgRefresh(false, true), true
		}
		m.NotifyOrgCountsError = false
		return tea.Batch(), true
	}
	m.OrgRefreshFailed = false
	m.LastOrgRefresh = time.Now()
	m.RepoCountsFetchFailed = msg.countsErr != nil
	m.LocalScanFailed = msg.localErr != nil
	m.LocalScanInProgress = errors.Is(msg.localErr, git.ErrDirectoryScanInProgress)
	m.LocalScanStuck = errors.Is(msg.localErr, git.ErrDirectoryScanStuck)
	m.LocalScanUnresponsive = errors.Is(msg.localErr, git.ErrDirectoryScanUnresponsive)
	if notifyCountsErr {
		switch {
		case m.LocalScanUnresponsive && msg.countsErr != nil:
			m.setToast("Review counts are incomplete and the workspace directory is unresponsive; keeping last-known data. Restore the directory and press r to retry.", 2)
		case m.LocalScanUnresponsive:
			m.setToast("The workspace directory is unresponsive; keeping last-known data. Restore the directory and press r to retry.", 2)
		case m.LocalScanStuck && msg.countsErr != nil:
			m.setToast("Review counts are incomplete and the previous local repository scan was abandoned; keeping last-known data. Press r to retry.", 2)
		case m.LocalScanStuck:
			m.setToast("The previous local repository scan was abandoned after it stopped responding; keeping last-known data. Press r to retry.", 2)
		case m.LocalScanInProgress && msg.countsErr != nil:
			m.setToast("Review counts are incomplete and the local repository scan is still running; keeping last-known data. Try again after it finishes.", 1)
		case m.LocalScanInProgress:
			m.setToast("Local repository scan is still running; keeping last-known data. Try again after it finishes.", 1)
		case msg.countsErr != nil && msg.localErr != nil:
			m.setToast("Review counts and local metadata are incomplete; keeping last known data where available. Press r to retry.", 1)
		case msg.countsErr != nil:
			m.setToast("Review counts refresh failed; keeping last known counts where available. Press r to retry.", 1)
		case msg.localErr != nil:
			m.setToast("Local repository metadata is incomplete; keeping last known data where available. Press r to retry.", 1)
		}
	}
	if len(m.Repos) > 0 && len(msg.repos) > 0 {
		oldRepoBranches := make(map[string]string)
		for _, r := range m.Repos {
			oldRepoBranches[r.Name] = r.CurrentBranch
		}
		for _, newR := range msg.repos {
			if oldBranch, ok := oldRepoBranches[newR.Name]; ok && oldBranch != "" && newR.CurrentBranch != "" && oldBranch != newR.CurrentBranch {
				m.setToast(fmt.Sprintf("  Branch changed for %s: %s → %s", newR.Name, oldBranch, newR.CurrentBranch), 1)
			}
		}
	}
	countsNow := time.Now()
	filteredRepos := msg.repos[:0]
	for _, fresh := range msg.repos {
		if removedAt, removed := m.RemovedRepoPaths[fresh.Path]; removed {
			if msg.generation == 0 || msg.generation <= removedAt || msg.localErr != nil {
				continue
			}
			delete(m.RemovedRepoPaths, fresh.Path)
		}
		if fresh.LocalMetadataStale && fresh.HasLoadedCounts && (fresh.CountsUpdatedAt.IsZero() || countsNow.Sub(fresh.CountsUpdatedAt) > repoCountsStaleTTL) {
			fresh.OpenIssuesCount = 0
			fresh.OpenPRsCount = 0
			fresh.HasLoadedCounts = false
			fresh.CountsStale = false
			fresh.CountsUpdatedAt = time.Time{}
		}
		filteredRepos = append(filteredRepos, fresh)
	}
	msg.repos = filteredRepos
	for _, fresh := range msg.repos {
		for _, old := range m.Repos {
			if fresh.Path == old.Path {
				fresh.Logs = old.Logs
				fresh.OriginalBranch = old.OriginalBranch
				if msg.localErr != nil && !fresh.IsNew {
					if fresh.CurrentBranch == "" {
						fresh.CurrentBranch = old.CurrentBranch
					}
					if fresh.DefaultBranch == "" {
						fresh.DefaultBranch = old.DefaultBranch
					}
					fresh.HasUnstagedChanges = old.HasUnstagedChanges
					if fresh.Status == git.StatusPending && old.Status != git.StatusArchived {
						fresh.Status = old.Status
						fresh.StatusMsg = old.StatusMsg
						fresh.ErrorErr = old.ErrorErr
					}
				}
				if !fresh.HasLoadedCounts && old.HasLoadedCounts && !fresh.LocalMetadataStale {
					updatedAt := old.CountsUpdatedAt
					if updatedAt.IsZero() {
						updatedAt = countsNow
					}
					if countsNow.Sub(updatedAt) <= repoCountsStaleTTL {
						fresh.OpenIssuesCount = old.OpenIssuesCount
						fresh.OpenPRsCount = old.OpenPRsCount
						fresh.HasLoadedCounts = true
						fresh.CountsStale = true
						fresh.CountsUpdatedAt = updatedAt
					}
				}
				if fresh.CurrentBranch == old.CurrentBranch {
					fresh.BranchDetails = old.BranchDetails
				}
				break
			}
		}
	}
	m.Repos = msg.repos
	sort.Slice(m.Repos, func(i, j int) bool {
		return strings.ToLower(m.Repos[i].Name) < strings.ToLower(m.Repos[j].Name)
	})
	m.TotalCount = len(m.Repos)
	repoKeys := make(map[string]int, len(m.Repos))
	concernKeys := make(map[string]int)
	for i, repo := range m.Repos {
		repoKeys[repo.Path+"/"+repo.Name] = i
	}
	for _, i := range m.concernScreenRepoIndices() {
		repo := m.Repos[i]
		concernKeys[repo.Path+"/"+repo.Name] = i
	}
	for _, focus := range []FocusType{FocusRepos, FocusConcerns} {
		selected := m.ScreenCursor[focus]
		if selected == "" {
			continue
		}
		keys := repoKeys
		if focus == FocusConcerns {
			keys = concernKeys
			if m.ActiveFocus == FocusConcerns && m.Detail {
				if index, found := repoKeys[selected]; found {
					m.SelectedIndex = index
					continue
				}
			}
		}
		if index, found := keys[selected]; found {
			if m.ActiveFocus == focus {
				m.SelectedIndex = index
			}
		} else {
			m.ScreenCursor[focus] = ""
			if m.ActiveFocus == focus {
				m.SelectedIndex = -1
				m.Detail = false
			}
		}
	}

	// The list the user confirmed against is gone; make them re-arm rather
	// than let a stale token delete whatever now sits under the selection.
	var cmd tea.Cmd
	if msg.autoSync && len(m.Repos) > 0 {
		m.IsSyncing = true
		// safeOnly=true: this is the passive startup sync, not something the
		// user directly asked for — never auto-switch a feature branch or
		// auto-rebase it. [a] Sync All and [r] Sync are explicit commands
		// and keep the full behavior (see startSyncCmd call sites above).
		cmd = m.startSyncCmd(m.Repos, true, true)
	}
	if pendingRefresh {
		cmd = tea.Batch(cmd, m.startOrgRefresh(false, true))
	}
	m.updateViewport()
	return cmd, false
}

// handleRepoSyncMsg folds one streamed snapshot into the model and waits for
// the next one on the same stream.
func (m *Model) handleRepoSyncMsg(msg repoSyncMsg) tea.Cmd {
	m.applyRepoSnapshot(msg.repo)
	m.updateViewport()
	return waitForSyncSnapshot(msg.snapshots, msg.bulk)
}

// handleSyncFinishedMsg clears the syncing banner only for the bulk sync; a
// single-repo re-sync must leave it alone.
func (m *Model) handleSyncFinishedMsg(msg syncFinishedMsg) {
	if !msg.bulk {
		m.BusyAction = ""
		m.setToast("Repository sync finished. See Logs for the result.", 1)
	}
	if msg.bulk {
		m.IsSyncing = false
	}
	m.updateViewport()
}

func (m *Model) handleWindowSizeMsg(msg tea.WindowSizeMsg) {
	m.Width = msg.Width
	m.Height = msg.Height

	m.Viewport.Width = max(2, msg.Width-2)
	m.Viewport.Height = max(1, msg.Height-5)
	m.Search.Width = max(1, msg.Width-6)
	m.Help.Width = max(1, msg.Width)

	m.updateViewport()
}

// processJobQueueUpdate processes a freshly loaded job queue, comparing to the
// old queue for status-change notifications with proper priority handling.
// maxJobDurationSamples caps how many historical samples processJobQueueUpdate
// keeps per workflow name — each fetch only turns up a handful (the runs
// listing is 30 total, active and completed combined), so history
// accumulates across polls rather than resetting every refresh; this just
// bounds how far back that accumulation reaches.
const maxJobDurationSamples = 5

func (m *Model) processJobQueueUpdate(queue []*jobs.JobItem, newHistory map[string][]time.Duration) {
	if m.JobDurationHistory == nil {
		m.JobDurationHistory = make(map[string][]time.Duration)
	}
	for name, samples := range newHistory {
		combined := append(m.JobDurationHistory[name], samples...)
		if len(combined) > maxJobDurationSamples {
			combined = combined[len(combined)-maxJobDurationSamples:]
		}
		m.JobDurationHistory[name] = combined
	}

	if len(m.JobQueue) > 0 {
		oldJobs := make(map[string]*jobs.JobItem)
		for _, j := range m.JobQueue {
			oldJobs[j.ID] = j
		}

		// Status changes — failure toasts (priority 2) survive info toasts (priority 1)
		for _, newJ := range queue {
			if newJ.IsRunHeader {
				if old, ok := oldJobs[newJ.ID]; ok && old.Status != newJ.Status && newJ.Run != nil {
					label := fmt.Sprintf("Run %s / %s #%d", newJ.Repo, newJ.Run.Workflow, newJ.Run.Number)
					switch newJ.Status {
					case jobs.JobFailed:
						m.setToast(label+" failed", 2)
					case jobs.JobPassed:
						m.setToast(label+" passed", 1)
					case jobs.JobCancelled:
						m.setToast(label+" cancelled", 1)
					}
				}
				continue
			}
			if oldJ, ok := oldJobs[newJ.ID]; ok {
				if oldJ.Status != newJ.Status {
					switch newJ.Status {
					case jobs.JobFailed:
						runnerStr := newJ.RunnerName
						if runnerStr == "" {
							runnerStr = "worker"
						}
						m.setToast(fmt.Sprintf(" ❌ Job %s failed (%s) on %s", newJ.ID, newJ.Name, runnerStr), 2)
					case jobs.JobRunning:
						runnerStr := newJ.RunnerName
						if runnerStr == "" {
							runnerStr = "worker"
						}
						m.setToast(fmt.Sprintf(" ⚡ Job %s started running on %s", newJ.ID, runnerStr), 1)
					case jobs.JobPassed:
						m.setToast(fmt.Sprintf(" ✅ Job %s passed (%s)", newJ.ID, newJ.Name), 1)
					}
				}
			} else {
				switch newJ.Status {
				case jobs.JobRunning:
					runnerStr := newJ.RunnerName
					if runnerStr == "" {
						runnerStr = "worker"
					}
					m.setToast(fmt.Sprintf(" ⚡ Job %s started running on %s", newJ.ID, runnerStr), 1)
				case jobs.JobQueued:
					m.setToast(fmt.Sprintf(" ⏳ Job %s queued (%s)", newJ.ID, newJ.Name), 1)
				}
			}
		}

	}

	// Preserve existing logs
	existingLogs := make(map[string][]string)
	existingGHJobID := make(map[string]int64)
	for _, j := range m.JobQueue {
		if len(j.Logs) > 0 {
			existingLogs[j.ID] = j.Logs
			existingGHJobID[j.ID] = j.GHJobID
		}
	}
	for _, j := range queue {
		if logs, ok := existingLogs[j.ID]; ok {
			j.Logs = logs
			j.GHJobID = existingGHJobID[j.ID]
		}
	}
	// Preserve fetched details only for the same completed attempt. Active polls
	// are authoritative, including a new attempt with different job IDs.
	for _, header := range queue {
		if !header.IsRunHeader || header.Run == nil || header.Run.JobsKnown || !terminalStatus(header.Run.Status) {
			continue
		}
		for _, old := range m.JobQueue {
			if old.Run == nil || old.IsRunHeader || old.RunID != header.RunID || old.Repo != header.Repo || old.Run.Attempt != header.Run.Attempt {
				continue
			}
			copyJob := *old
			copyJob.Run = header.Run
			if header.Run.JobsError != "" || old.Run.JobsStale {
				header.Run.JobsStale = true
			}
			queue = append(queue, &copyJob)
			if header.Run.JobsError == "" {
				if terminalStatus(old.Run.Status) {
					header.Run.JobsKnown = old.Run.JobsKnown
				} else {
					header.Run.JobsStale = true
				}
			}
		}
	}
	m.JobQueue = queue
	m.refreshOpenRun()

}

// triggerLogFetchForSelectedJob returns a log-fetch command if a running job is selected.
func (m Model) triggerLogFetchForSelectedJob() tea.Cmd {
	if m.ActiveFocus != FocusJobs {
		return nil
	}
	return m.loadLogsIfSelectedJobRunning()
}

func (m *Model) triggerTabFetch() tea.Cmd {
	if len(m.Repos) == 0 || m.SelectedIndex < 0 || m.SelectedIndex >= len(m.Repos) {
		return nil
	}
	item := m.Repos[m.SelectedIndex]
	if m.ActiveTab == TabIssues && !item.IsLoadingIssues && m.TargetOrg != "" {
		item.IsLoadingIssues = true
		return m.fetchIssuesCmd(item.Name, item.GHRepoName)
	}
	if m.ActiveTab == TabPRs && !item.IsLoadingPRs && m.TargetOrg != "" {
		item.IsLoadingPRs = true
		return m.fetchPRsCmd(item.Name, item.GHRepoName)
	}
	return nil
}

func isRunnerPermissionError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "403") ||
		strings.Contains(errStr, "permission") ||
		strings.Contains(errStr, "org admin") ||
		strings.Contains(errStr, "must be an org admin") ||
		strings.Contains(errStr, "fine-grained permission")
}

// isUnknownOwnerError reports whether err is gh's response to a repo-list
// call against a user/org login that doesn't exist, as opposed to a
// transient network or auth failure.
func isUnknownOwnerError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "was not recognized as either a github user or an organization")
}

// clearConfiguredOwner removes owner from the persisted config, but only if
// it's still the currently saved value — so a bad owner from an earlier run
// doesn't keep failing on every subsequent launch, without racing a config
// change made since this session started.
func clearConfiguredOwner(owner string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Owner != owner {
		return nil
	}
	cfg.Owner = ""
	return config.Save(cfg)
}

func extractRunnersFromJobQueue(queue []*jobs.JobItem, _ []*jobs.RunnerItem) []*jobs.RunnerItem {
	runnerMap := make(map[string]*jobs.RunnerItem)

	for _, j := range queue {
		if j.IsRunHeader || terminalStatus(j.Status) {
			continue
		}
		if j.RunnerName == "" || j.RunnerName == "worker" {
			continue
		}
		if r, ok := runnerMap[j.RunnerName]; ok {
			if j.Status == jobs.JobRunning {
				r.Status = jobs.RunnerRunning
				r.CurrentJobID = j.ID
				r.CurrentJob = j.Name
				r.LastHeartbeat = time.Now()
			}
		} else {
			st := jobs.RunnerUnknown
			currJob := "-"
			currJobID := "-"
			if j.Status == jobs.JobRunning {
				st = jobs.RunnerRunning
				currJob = j.Name
				currJobID = j.ID
			}
			runnerMap[j.RunnerName] = &jobs.RunnerItem{
				ID:            j.RunnerID,
				Name:          j.RunnerName,
				Platform:      "GitHub Actions",
				Status:        st,
				CurrentJobID:  currJobID,
				CurrentJob:    currJob,
				Tags:          []string{"actions"},
				LastHeartbeat: time.Now(),
				OutputLogs:    []string{fmt.Sprintf("[%s] Runner active for job %s", time.Now().Format("15:04:05"), j.Name)},
			}
		}
	}
	var res []*jobs.RunnerItem
	for _, r := range runnerMap {
		res = append(res, r)
	}
	sort.Slice(res, func(i, j int) bool {
		return res[i].Name < res[j].Name
	})
	return res
}
