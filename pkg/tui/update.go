package tui

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/seankoji-com/freshen/pkg/jobs"
)

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

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

	case pushFinishedMsg:
		m.handlePushFinishedMsg(msg)

	case tea.WindowSizeMsg:
		m.handleWindowSizeMsg(msg)
	}

	var spinnerCmd tea.Cmd
	m.Spinner, spinnerCmd = m.Spinner.Update(msg)
	cmds = append(cmds, spinnerCmd)

	var vpCmd tea.Cmd
	m.Viewport, vpCmd = m.Viewport.Update(msg)
	cmds = append(cmds, vpCmd)

	return m, tea.Batch(cmds...)
}

func (m *Model) handleLoadedIssuesMsg(msg loadedIssuesMsg) {
	for _, item := range m.Repos {
		if item.Name == msg.repoName {
			item.IsLoadingIssues = false
			item.HasLoadedIssues = true
			if msg.err == nil && msg.issues != nil {
				item.IssuesList = msg.issues
			}
			break
		}
	}
	m.updateViewport()
}

func (m *Model) handleLoadedPRsMsg(msg loadedPRsMsg) {
	for _, item := range m.Repos {
		if item.Name == msg.repoName {
			item.IsLoadingPRs = false
			item.HasLoadedPRs = true
			if msg.err == nil && msg.prs != nil {
				item.PRsList = msg.prs
			}
			break
		}
	}
	m.updateViewport()
}

func (m *Model) handleMouseMsg(msg tea.MouseMsg) tea.Cmd {
	var cmd tea.Cmd
	if msg.Button == tea.MouseButtonWheelUp {
		m.Viewport.LineUp(3)
	} else if msg.Button == tea.MouseButtonWheelDown {
		m.Viewport.LineDown(3)
	} else if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
		// Click in Left Column Panes
		if msg.X < m.Width/2 {
			// Use panel heights from View() layout instead of content lengths
			rightBoxHeight := m.Height - 4
			if rightBoxHeight < 12 {
				rightBoxHeight = 12
			}
			totalInner := rightBoxHeight - 4
			if totalInner < 8 {
				totalInner = 8
			}
			runnersBoxHeight := 4
			repoBoxHeight := (totalInner - runnersBoxHeight) * 60 / 100
			if repoBoxHeight < 4 {
				repoBoxHeight = 4
			}

			// Y=0 is header, Y=1 starts the first panel
			// Each bordered panel: 1 top border + innerHeight + 1 bottom border = innerHeight + 2
			repoPaneStart := 1 + 1 // header + top border
			repoPaneEnd := repoPaneStart + repoBoxHeight
			runnersPaneStart := repoPaneEnd + 1 // bottom border of repo + top border of runners
			runnersPaneEnd := runnersPaneStart + runnersBoxHeight

			if msg.Y >= repoPaneStart && msg.Y <= repoPaneEnd {
				m.ActiveFocus = FocusRepos
				clickedIdx := msg.Y - repoPaneStart - 1 // -1 for header row
				if clickedIdx >= 0 && clickedIdx < len(m.Repos) {
					m.SelectedIndex = clickedIdx
				}
				m.updateViewport()
			} else if msg.Y > repoPaneEnd && msg.Y <= runnersPaneEnd {
				m.ActiveFocus = FocusRunners
				clickedIdx := msg.Y - runnersPaneStart
				if clickedIdx >= 0 && clickedIdx < len(m.Runners) {
					m.SelectedRunnerIndex = clickedIdx
				}
				m.updateViewport()
			} else if msg.Y > runnersPaneEnd {
				m.ActiveFocus = FocusJobs
				jobsPaneStart := runnersPaneEnd + 1
				clickedIdx := msg.Y - jobsPaneStart - 2
				if clickedIdx >= 0 && clickedIdx < len(m.JobQueue) {
					m.SelectedJobIndex = clickedIdx
				}
				m.updateViewport()
			}
		}
		// Click in Right Detail View Pane
		if msg.X >= m.Width/2 && (msg.Y == 4 || msg.Y == 5) {
			relX := msg.X - (m.Width / 2)
			if relX >= 0 && relX < 10 {
				m.ActiveTab = TabLogs
				m.updateViewport()
			} else if relX >= 10 && relX < 35 {
				m.ActiveTab = TabBranches
				m.updateViewport()
			} else if relX >= 35 && relX < 47 {
				m.ActiveTab = TabIssues
				m.updateViewport()
				cmd = m.triggerTabFetch()
			} else if relX >= 47 {
				m.ActiveTab = TabPRs
				m.updateViewport()
				cmd = m.triggerTabFetch()
			}
		}
	}
	return cmd
}

func (m Model) handleRepoTickMsg() tea.Cmd {
	return tea.Batch(
		m.loadOrgReposCmd(false),
		repoTickCmd(),
	)
}

func (m *Model) handleRunnerJobTickMsg() tea.Cmd {
	jobs.PollStep(m.Runners, m.JobQueue)
	m.updateViewport()
	var cmdsToAdd []tea.Cmd
	if !m.RunnerPermissionDenied {
		cmdsToAdd = append(cmdsToAdd, m.loadRunnersCmd())
	}
	cmdsToAdd = append(cmdsToAdd, runnerJobTickCmd(backoffInterval(runnerJobTickInterval, m.ConsecutiveErrors[fetchSourceRunners])))
	// Refresh logs for selected running job
	if m.ActiveFocus == FocusJobs && len(m.JobQueue) > 0 && m.SelectedJobIndex < len(m.JobQueue) {
		selJob := m.JobQueue[m.SelectedJobIndex]
		if selJob.Status == jobs.JobRunning {
			cmdsToAdd = append(cmdsToAdd, m.loadJobLogsCmd(selJob))
		}
	}
	return tea.Batch(cmdsToAdd...)
}

// handleJobQueueTickMsg refreshes the job queue on its own slower cadence,
// keeping the per-repo API cost off the 10s runner tick.
func (m *Model) handleJobQueueTickMsg() tea.Cmd {
	return tea.Batch(m.loadJobQueueCmd(), jobQueueTickCmd(backoffInterval(jobQueueTickInterval, m.ConsecutiveErrors[fetchSourceJobQueue])))
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

		m.JobQueue = reconcileRunnerJobs(m.Runners, m.JobQueue, m.TargetOrg)
		m.updateViewport()
	}
}

func (m *Model) handleLoadedJobQueueMsg(msg loadedJobQueueMsg) tea.Cmd {
	m.IsJobQueueLoading = false
	if msg.err != nil {
		m.JobQueueFetchFailed = true
		m.noteFetchFailure(fetchSourceJobQueue, jobQueueTickInterval)
		slog.Error("job queue fetch failed", "org", m.TargetOrg, "error", msg.err)
		m.setToast(fmt.Sprintf(" ⚠ Job queue may be incomplete: %v", msg.err), 2)
		var cmd tea.Cmd
		if len(msg.queue) > 0 {
			m.processJobQueueUpdate(msg.queue)
			if len(m.Runners) == 0 || m.RunnerPermissionDenied {
				m.Runners = extractRunnersFromJobQueue(msg.queue, m.Runners)
			}
			cmd = m.triggerLogFetchForSelectedJob()
		}
		m.updateViewport()
		return cmd
	}

	m.JobQueueFetchFailed = false
	m.noteFetchSuccess(fetchSourceJobQueue)
	m.processJobQueueUpdate(msg.queue)
	if len(m.Runners) == 0 || m.RunnerPermissionDenied {
		m.Runners = extractRunnersFromJobQueue(msg.queue, m.Runners)
	}
	cmd := m.triggerLogFetchForSelectedJob()
	m.updateViewport()
	return cmd
}

func (m *Model) handleLoadedJobLogsMsg(msg loadedJobLogsMsg) {
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
	m.IsOrgSyncing = false
	if msg.err != nil {
		slog.Error("org repos fetch failed", "org", m.TargetOrg, "error", msg.err)
		m.setToast(fmt.Sprintf(" %s Fetch failed: %v. Check 'gh auth status'.", iconError, msg.err), 2)
		m.updateViewport()
		return tea.Batch(), true
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
	m.Repos = msg.repos
	sort.Slice(m.Repos, func(i, j int) bool {
		return strings.ToLower(m.Repos[i].Name) < strings.ToLower(m.Repos[j].Name)
	})
	m.TotalCount = len(m.Repos)
	// The list the user confirmed against is gone; make them re-arm rather
	// than let a stale token delete whatever now sits under the selection.
	m.pendingDeletePath = ""
	var cmd tea.Cmd
	if msg.autoSync && len(m.Repos) > 0 {
		m.IsSyncing = true
		m.updateViewport()
		cmd = m.startSyncCmd(m.Repos, true)
	}
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
	if msg.bulk {
		m.IsSyncing = false
	}
	m.updateViewport()
}

func (m *Model) handleWindowSizeMsg(msg tea.WindowSizeMsg) {
	m.Width = msg.Width
	m.Height = msg.Height
	m.ProgressBar.Width = msg.Width - 20

	// Mirror the same height budget as View()
	rightBoxH := msg.Height - 4
	if rightBoxH < 12 {
		rightBoxH = 12
	}
	halfWidth := msg.Width / 2
	leftWidth := halfWidth - 1
	rightWidth := msg.Width - leftWidth - 2
	m.Viewport.Width = rightWidth - 4
	m.Viewport.Height = rightBoxH - 2 // inner content = outer - 2 (borders)
	m.updateViewport()
}

// processJobQueueUpdate processes a freshly loaded job queue, comparing to the
// old queue for status-change notifications with proper priority handling.
func (m *Model) processJobQueueUpdate(queue []*jobs.JobItem) {
	if len(m.JobQueue) > 0 {
		oldJobs := make(map[string]*jobs.JobItem)
		for _, j := range m.JobQueue {
			oldJobs[j.ID] = j
		}

		newJobsMap := make(map[string]*jobs.JobItem)
		for _, j := range queue {
			newJobsMap[j.ID] = j
		}

		// Status changes — failure toasts (priority 2) survive info toasts (priority 1)
		for _, newJ := range queue {
			if oldJ, ok := oldJobs[newJ.ID]; ok {
				if oldJ.Status != newJ.Status {
					if newJ.Status == jobs.JobFailed {
						runnerStr := newJ.RunnerName
						if runnerStr == "" {
							runnerStr = "worker"
						}
						m.setToast(fmt.Sprintf(" ❌ Job %s failed (%s) on %s", newJ.ID, newJ.Name, runnerStr), 2)
					} else if newJ.Status == jobs.JobRunning {
						runnerStr := newJ.RunnerName
						if runnerStr == "" {
							runnerStr = "worker"
						}
						m.setToast(fmt.Sprintf(" ⚡ Job %s started running on %s", newJ.ID, runnerStr), 1)
					} else if newJ.Status == jobs.JobPassed {
						m.setToast(fmt.Sprintf(" ✅ Job %s passed (%s)", newJ.ID, newJ.Name), 1)
					}
				}
			} else {
				if newJ.Status == jobs.JobRunning {
					runnerStr := newJ.RunnerName
					if runnerStr == "" {
						runnerStr = "worker"
					}
					m.setToast(fmt.Sprintf(" ⚡ Job %s started running on %s", newJ.ID, runnerStr), 1)
				} else if newJ.Status == jobs.JobQueued {
					m.setToast(fmt.Sprintf(" ⏳ Job %s queued (%s)", newJ.ID, newJ.Name), 1)
				}
			}
		}

		// Jobs that finished and left the queue
		for _, oldJ := range m.JobQueue {
			if _, stillThere := newJobsMap[oldJ.ID]; !stillThere {
				if oldJ.Status == jobs.JobRunning {
					m.setToast(fmt.Sprintf(" ✅ Job %s completed (%s)", oldJ.ID, oldJ.Name), 1)
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
	m.JobQueue = reconcileRunnerJobs(m.Runners, queue, m.TargetOrg)

	// Bounds validation after queue update
	if len(m.JobQueue) > 0 && m.SelectedJobIndex >= len(m.JobQueue) {
		m.SelectedJobIndex = len(m.JobQueue) - 1
	}
	if len(m.Runners) > 0 && m.SelectedRunnerIndex >= len(m.Runners) {
		m.SelectedRunnerIndex = len(m.Runners) - 1
	}
}

// triggerLogFetchForSelectedJob returns a log-fetch command if a running job is selected.
func (m Model) triggerLogFetchForSelectedJob() tea.Cmd {
	if m.ActiveFocus == FocusJobs && len(m.JobQueue) > 0 && m.SelectedJobIndex < len(m.JobQueue) {
		selJob := m.JobQueue[m.SelectedJobIndex]
		if selJob.Status == jobs.JobRunning {
			return m.loadJobLogsCmd(selJob)
		}
	}
	return nil
}

func (m *Model) triggerTabFetch() tea.Cmd {
	if len(m.Repos) == 0 || m.SelectedIndex >= len(m.Repos) {
		return nil
	}
	item := m.Repos[m.SelectedIndex]
	if m.ActiveTab == TabIssues {
		item.IsLoadingIssues = true
		return m.fetchIssuesCmd(item.Name, item.GHRepoName)
	}
	if m.ActiveTab == TabPRs {
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

func reconcileRunnerJobs(runners []*jobs.RunnerItem, queue []*jobs.JobItem, targetOrg string) []*jobs.JobItem {
	var realJobs []*jobs.JobItem
	for _, j := range queue {
		if j.RunID != 0 {
			realJobs = append(realJobs, j)
		}
	}

	result := make([]*jobs.JobItem, 0, len(queue))
	if len(realJobs) > 0 {
		result = append(result, realJobs...)
	} else {
		result = append(result, queue...)
	}

	for _, r := range runners {
		if r.Status == jobs.RunnerRunning {
			found := false
			for _, j := range result {
				if j.RunnerName == r.Name || j.RunnerID == r.ID || strings.EqualFold(j.RunnerName, r.Name) {
					found = true
					break
				}
				if j.Name != "" && r.CurrentJob != "" && r.CurrentJob != "-" && (strings.Contains(j.Name, r.CurrentJob) || strings.Contains(r.CurrentJob, j.Name)) {
					found = true
					if j.RunnerName == "" {
						j.RunnerName = r.Name
						j.RunnerID = r.ID
					}
					break
				}
			}
			if !found && len(realJobs) == 0 {
				jobTitle := r.CurrentJob
				if jobTitle == "" || jobTitle == "-" {
					jobTitle = fmt.Sprintf("%s active workflow job", r.Name)
				}
				jobID := r.CurrentJobID
				if jobID == "" || jobID == "-" {
					jobID = fmt.Sprintf("#%s", strings.TrimPrefix(r.ID, "runner-"))
				}
				result = append(result, &jobs.JobItem{
					ID:         jobID,
					Name:       jobTitle,
					Repo:       "", // unknown — synthetic entry; URL construction guarded by RunID==0
					Status:     jobs.JobRunning,
					RunnerName: r.Name,
					RunnerID:   r.ID,
					Duration:   "active",
				})
			}
		}
	}
	return result
}

func extractRunnersFromJobQueue(queue []*jobs.JobItem, existing []*jobs.RunnerItem) []*jobs.RunnerItem {
	runnerMap := make(map[string]*jobs.RunnerItem)
	for _, r := range existing {
		runnerMap[r.Name] = r
	}
	for _, j := range queue {
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
			st := jobs.RunnerIdle
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
