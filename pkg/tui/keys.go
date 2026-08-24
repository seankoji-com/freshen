package tui

import (
	"fmt"
	"log/slog"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/seankoji-com/freshen/pkg/git"
)

// handleKeyMsg dispatches a tea.KeyMsg to the per-key handler for msg.String(),
// mirroring the original inline switch. It returns the resulting command and
// whether Update() should return immediately with it, bypassing the shared
// spinner/viewport postlude (true only for the keys that used to `return m, ...`
// mid-switch: quit, and the repo-tab-cycling right/left/"4" keys).
func (m *Model) handleKeyMsg(msg tea.KeyMsg) (tea.Cmd, bool) {
	// Unconditional: setToast("", 0) would no-op here once any priority-1+
	// toast has ever fired, since its own priority gate blocks a lower
	// priority from clearing a higher one.
	m.ToastMsg = ""
	m.ToastPriority = 0
	if msg.String() != "d" {
		m.pendingDeletePath = ""
	}

	switch msg.String() {

	case "q", "ctrl+c":
		return m.handleKeyQuit()

	case "enter":
		m.handleKeyEnter()

	case "esc":
		m.handleKeyEsc()

	case "w", "W":
		m.handleKeyCyclePanel()

	case "up", "k":
		return m.handleKeyUp(), false

	case "down", "j":
		return m.handleKeyDown(), false

	case "tab":
		return m.handleKeyNextPanel(), false

	case "shift+tab":
		return m.handleKeyPrevPanel(), false

	case "right", "l":
		return m.handleKeyRight()

	case "left", "h":
		return m.handleKeyLeft()

	case "1":
		m.handleKeyTab1()

	case "2":
		m.handleKeyTab2()

	case "3":
		return m.handleKeyTab3()

	case "4":
		return m.handleKeyTab4()

	case "a", "s":
		return m.handleKeySyncAll(), false

	case "X":
		m.handleKeyPrune()

	case "r":
		return m.handleKeySync(), false

	case "c", "y":
		m.handleKeyCopy()

	case "b":
		m.handleKeySwitchBranch()

	case "p":
		return m.handleKeyPush(), false

	case "d":
		m.handleKeyDelete()
	}

	return nil, false
}

func (m *Model) handleKeyQuit() (tea.Cmd, bool) {
	if m.cancel != nil {
		m.cancel()
	}
	return tea.Quit, true
}

// handleKeyEnter drills into whatever's currently selected in the OVERALL
// JOB QUEUE panel: an initiator header focuses that PR/branch (all its runs
// and jobs), a run header or job row focuses that run — pressing Enter again
// on the same target unfocuses it.
func (m *Model) handleKeyEnter() {
	if m.ActiveFocus != FocusJobs {
		return
	}
	_, row, ok := m.selectedJobRow()
	if !ok {
		return
	}
	job := m.JobQueue[row.itemIndex]
	if row.kind == rowKindInitiatorHeader {
		if m.FocusedInitiatorKey == row.initiatorKey {
			m.FocusedInitiatorKey = ""
		} else {
			m.FocusedInitiatorKey = row.initiatorKey
			m.FocusedRunID = 0
		}
	} else {
		if m.FocusedRunID == job.RunID {
			m.FocusedRunID = 0
		} else {
			m.FocusedRunID = job.RunID
			m.FocusedInitiatorKey = ""
		}
	}
	m.updateViewport()
}

func (m *Model) handleKeyEsc() {
	if m.FocusedRunID != 0 || m.FocusedInitiatorKey != "" {
		m.FocusedRunID = 0
		m.FocusedInitiatorKey = ""
		m.updateViewport()
	}
}

func (m *Model) handleKeyCyclePanel() {
	// Cycle active panel focus: Repos -> Runners -> Jobs
	m.ActiveFocus = (m.ActiveFocus + 1) % 3
	m.updateViewport()
}

func (m *Model) handleKeyUp() tea.Cmd {
	var cmd tea.Cmd
	switch m.ActiveFocus {
	case FocusRepos:
		if m.SelectedIndex > 0 {
			m.SelectedIndex--
			m.updateViewport()
		}
	case FocusRunners:
		if m.SelectedRunnerIndex > 0 {
			m.SelectedRunnerIndex--
			m.updateViewport()
		} else if len(m.Repos) > 0 {
			m.ActiveFocus = FocusRepos
			m.SelectedIndex = len(m.Repos) - 1
			m.setToast(" Focused Repositories Panel", 1)
			m.updateViewport()
		}
	case FocusJobs:
		if m.SelectedJobIndex > 0 {
			m.SelectedJobIndex--
			m.updateViewport()
			cmd = m.loadLogsIfSelectedJobRunning()
		} else {
			m.ActiveFocus = FocusRunners
			matching := m.getMatchingRunners()
			if len(matching) > 0 {
				m.SelectedRunnerIndex = len(matching) - 1
			} else {
				m.SelectedRunnerIndex = 0
			}
			m.setToast(" Focused Runners Panel", 1)
			m.updateViewport()
		}
	}
	return cmd
}

func (m *Model) handleKeyDown() tea.Cmd {
	var cmd tea.Cmd
	switch m.ActiveFocus {
	case FocusRepos:
		if m.SelectedIndex < len(m.Repos)-1 {
			m.SelectedIndex++
			m.updateViewport()
		} else {
			m.ActiveFocus = FocusRunners
			m.SelectedRunnerIndex = 0
			m.setToast(" Focused Runners Panel", 1)
			m.updateViewport()
		}
	case FocusRunners:
		matching := m.getMatchingRunners()
		if m.SelectedRunnerIndex < len(matching)-1 {
			m.SelectedRunnerIndex++
			m.updateViewport()
		} else {
			m.ActiveFocus = FocusJobs
			m.SelectedJobIndex = 0
			m.setToast(" Focused Jobs Panel", 1)
			m.updateViewport()
			cmd = m.loadLogsIfSelectedJobRunning()
		}
	case FocusJobs:
		if m.SelectedJobIndex < len(buildJobQueueRows(m.JobQueue))-1 {
			m.SelectedJobIndex++
			m.updateViewport()
			cmd = m.loadLogsIfSelectedJobRunning()
		}
	}
	return cmd
}

func (m *Model) handleKeyRight() (tea.Cmd, bool) {
	if m.ActiveFocus == FocusRunners {
		tags := m.getAvailableTags()
		if len(tags) > 0 {
			m.SelectedTagIndex = (m.SelectedTagIndex + 1) % len(tags)
			m.updateViewport()
		}
	} else {
		m.ActiveFocus = FocusRepos
		m.ActiveTab = (m.ActiveTab + 1) % 4
		m.updateViewport()
		return m.triggerTabFetch(), true
	}
	return nil, false
}

func (m *Model) handleKeyLeft() (tea.Cmd, bool) {
	if m.ActiveFocus == FocusRunners {
		tags := m.getAvailableTags()
		if len(tags) > 0 {
			m.SelectedTagIndex = (m.SelectedTagIndex - 1 + len(tags)) % len(tags)
			m.updateViewport()
		}
	} else {
		m.ActiveFocus = FocusRepos
		m.ActiveTab = (m.ActiveTab + 3) % 4
		m.updateViewport()
		return m.triggerTabFetch(), true
	}
	return nil, false
}

func (m *Model) handleKeyTab1() {
	m.ActiveFocus = FocusRepos
	m.ActiveTab = TabLogs
	m.updateViewport()
}

func (m *Model) handleKeyTab2() {
	m.ActiveFocus = FocusRepos
	m.ActiveTab = TabBranches
	m.updateViewport()
}

func (m *Model) handleKeyTab3() (tea.Cmd, bool) {
	m.ActiveFocus = FocusRepos
	m.ActiveTab = TabIssues
	m.updateViewport()
	return m.triggerTabFetch(), true
}

func (m *Model) handleKeyTab4() (tea.Cmd, bool) {
	m.ActiveFocus = FocusRepos
	m.ActiveTab = TabPRs
	m.updateViewport()
	return m.triggerTabFetch(), true
}

// handleKeyNextPanel cycles panel focus forward (Tab), loading logs when the
// jobs panel gains focus on a running job.
func (m *Model) handleKeyNextPanel() tea.Cmd {
	m.ActiveFocus = (m.ActiveFocus + 1) % 3
	m.updateViewport()
	return m.triggerLogFetchForFocusedJob()
}

// handleKeyPrevPanel cycles panel focus backward (Shift+Tab).
func (m *Model) handleKeyPrevPanel() tea.Cmd {
	m.ActiveFocus = (m.ActiveFocus + 2) % 3
	m.updateViewport()
	return m.triggerLogFetchForFocusedJob()
}

// triggerLogFetchForFocusedJob returns a log-fetch command when the jobs panel
// is focused on a running job, and nil otherwise.
func (m *Model) triggerLogFetchForFocusedJob() tea.Cmd {
	if m.ActiveFocus != FocusJobs {
		return nil
	}
	return m.loadLogsIfSelectedJobRunning()
}

// handleKeySyncAll starts a parallel sync across every loaded repository.
func (m *Model) handleKeySyncAll() tea.Cmd {
	if !m.IsSyncing && len(m.Repos) > 0 {
		m.IsSyncing = true
		m.setToast(" 󰓦 Starting parallel sync for all active repositories...", 1)
		cmd := m.startSyncCmd(m.Repos, true, false)
		m.updateViewport()
		return cmd
	}
	return nil
}

func (m *Model) handleKeyPrune() {
	if m.ActiveFocus == FocusRepos && len(m.Repos) > 0 && m.SelectedIndex < len(m.Repos) {
		item := m.Repos[m.SelectedIndex]
		count, err := git.PruneBranchesAndWorktrees(m.ctx, item.Path, item.DefaultBranch)
		if err != nil {
			// Earlier steps (worktree remove --force) may already have run
			// destructively before this error surfaced, so say so rather than
			// leaving the operator unable to tell "did nothing" from "half done".
			slog.Error("prune failed", "repo", item.Name, "path", item.Path, "error", err)
			item.Logs = append(item.Logs, fmt.Sprintf("⚠ Prune failed (earlier steps may already have removed worktrees): %v", err))
			m.setToast(fmt.Sprintf(" ⚠ Prune failed for '%s': %v", item.Name, err), 2)
			m.updateViewport()
			return
		}
		item.CurrentBranch = git.GetOriginalBranch(m.ctx, item.Path)
		item.BranchDetails = git.GetRepoBranchDetails(m.ctx, item.Path, item.DefaultBranch)
		item.Logs = append(item.Logs, fmt.Sprintf("󰄬 Pruned remote tracking branches (git fetch --prune), force removed worktrees, and deleted %d non-default local branches.", count))
		m.setToast(fmt.Sprintf(" 󰄬 Fetched & pruned remote refs, removed worktrees & deleted %d branches!", count), 1)
		m.updateViewport()
	}
}

// handleKeySync re-syncs just the selected repository. It returns a command so
// the sync streams its snapshots back through the update loop rather than
// mutating the model item from a background goroutine.
func (m *Model) handleKeySync() tea.Cmd {
	if m.ActiveFocus == FocusRepos && len(m.Repos) > 0 && m.SelectedIndex < len(m.Repos) {
		item := m.Repos[m.SelectedIndex]
		if !item.IsArchived {
			cmd := m.startSyncCmd([]*git.RepoItem{item}, false, false)
			m.updateViewport()
			return cmd
		}
	}
	return nil
}

func (m *Model) handleKeyCopy() {
	if m.ActiveFocus == FocusRepos && len(m.Repos) > 0 && m.SelectedIndex < len(m.Repos) {
		item := m.Repos[m.SelectedIndex]
		targetCopy := item.DraftPRURL
		if targetCopy == "" {
			targetCopy = item.ExistingPRURL
		}
		if targetCopy == "" {
			targetCopy = item.Path
		}

		if err := copyToClipboard(targetCopy); err != nil {
			m.setToast(fmt.Sprintf(" %s Failed to copy: %v", iconCopy, err), 2)
		} else {
			m.setToast(fmt.Sprintf(" %s Copied to clipboard: %s", iconCopy, targetCopy), 1)
		}
	} else if m.ActiveFocus == FocusRunners && len(m.Runners) > 0 && m.SelectedRunnerIndex < len(m.Runners) {
		r := m.Runners[m.SelectedRunnerIndex]
		if err := copyToClipboard(r.ID); err != nil {
			m.setToast(fmt.Sprintf(" %s Failed to copy: %v", iconCopy, err), 2)
		} else {
			m.setToast(fmt.Sprintf(" %s Copied Runner ID to clipboard: %s", iconCopy, r.ID), 1)
		}
	} else if m.ActiveFocus == FocusJobs {
		if j := m.selectedJob(); j != nil {
			if err := copyToClipboard(j.ID); err != nil {
				m.setToast(fmt.Sprintf(" %s Failed to copy: %v", iconCopy, err), 2)
			} else {
				m.setToast(fmt.Sprintf(" %s Copied Job ID to clipboard: %s", iconCopy, j.ID), 1)
			}
		}
	}
}

func (m *Model) handleKeySwitchBranch() {
	if m.ActiveFocus == FocusRepos && len(m.Repos) > 0 && m.SelectedIndex < len(m.Repos) {
		item := m.Repos[m.SelectedIndex]
		target := item.OriginalBranch
		if item.CurrentBranch == item.OriginalBranch {
			target = item.DefaultBranch
		}
		if err := git.SwitchBranch(item, target); err == nil {
			item.CurrentBranch = git.GetOriginalBranch(m.ctx, item.Path)
			item.BranchDetails = git.GetRepoBranchDetails(m.ctx, item.Path, item.DefaultBranch)
			m.updateViewport()
		}
	}
}

// handleKeyPush commits, pushes, and opens a PR for the selected repository in
// the background. Like handleKeySync it hands the worker a private clone and
// routes the result back as a tea.Msg, so only the update goroutine ever
// writes to an item in m.Repos.
func (m *Model) handleKeyPush() tea.Cmd {
	if m.ActiveFocus != FocusRepos || len(m.Repos) == 0 || m.SelectedIndex >= len(m.Repos) {
		return nil
	}
	item := m.Repos[m.SelectedIndex]
	if item.IsArchived {
		return nil
	}

	// Clone here, on the update goroutine, before the worker exists.
	work := item.Clone()
	ctx := m.ctx
	// Buffered so the worker never blocks (and so bgGuard's Done() always
	// fires) even if the receiving command is dropped at quit.
	result := make(chan pushFinishedMsg, 1)

	run := m.bgGuard(func() {
		out := pushFinishedMsg{repoName: work.Name, repo: work}
		if err := git.CommitPushPRAndSwitchDefault(ctx, work); err != nil {
			out.err = err
		} else {
			work.CurrentBranch = git.GetOriginalBranch(ctx, work.Path)
			work.BranchDetails = git.GetRepoBranchDetails(ctx, work.Path, work.DefaultBranch)
		}
		result <- out
	})
	go run()

	m.updateViewport()
	return func() tea.Msg { return <-result }
}

// handlePushFinishedMsg folds a background push result into the model on the
// update goroutine, surfacing failures the way handleKeyDelete does for its
// own destructive action instead of discarding the error.
func (m *Model) handlePushFinishedMsg(msg pushFinishedMsg) {
	// Log and toast unconditionally: a background refresh may have rebuilt
	// m.Repos while the push was in flight, and a failure that no longer
	// matches a live repo must still leave a trace.
	if msg.err != nil {
		slog.Error("push/PR failed", "repo", msg.repoName, "error", msg.err)
		if msg.repo != nil {
			msg.repo.Logs = append(msg.repo.Logs, fmt.Sprintf("⚠ Commit/push/PR failed: %v", msg.err))
		}
		m.setToast(fmt.Sprintf(" ⚠ Push/PR failed for '%s': %v", msg.repoName, msg.err), 2)
	} else {
		m.setToast(fmt.Sprintf(" 󰄬 Pushed '%s' and switched to the default branch.", msg.repoName), 1)
	}
	m.applyRepoSnapshot(msg.repo)
	m.updateViewport()
}

func (m *Model) handleKeyDelete() {
	if m.ActiveFocus == FocusRepos && len(m.Repos) > 0 && m.SelectedIndex < len(m.Repos) {
		item := m.Repos[m.SelectedIndex]
		if item.IsArchived {
			if m.pendingDeletePath != "" && m.pendingDeletePath == item.Path {
				m.pendingDeletePath = ""
				if err := git.DeleteLocalRepo(m.TargetDir, item.Path); err != nil {
					m.setToast(fmt.Sprintf(" ⚠ Failed to delete '%s': %v", item.Name, err), 2)
				} else {
					deletedName := item.Name

					m.Repos = append(m.Repos[:m.SelectedIndex], m.Repos[m.SelectedIndex+1:]...)
					m.TotalCount = len(m.Repos)

					if m.SelectedIndex >= len(m.Repos) && len(m.Repos) > 0 {
						m.SelectedIndex = len(m.Repos) - 1
					}

					m.setToast(fmt.Sprintf(" 🗑️ Deleted archived repo '%s' from disk.", deletedName), 1)
					m.updateViewport()
				}
			} else {
				m.pendingDeletePath = item.Path
				m.setToast(fmt.Sprintf(" ⚠ Press 'd' again to delete archived repo '%s'.", item.Name), 2)
			}
		}
	}
}
