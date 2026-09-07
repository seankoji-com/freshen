package tui

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/seankoji-com/freshen/pkg/git"
)

type uiAction struct {
	id, label, detail string
	confirm           bool
}

func (m Model) menuActions() []uiAction {
	actions := []uiAction{{"refresh", "Refresh", "Fetch the current view again", false}, {"copy", "Copy link or ID", "Copy this item's GitHub URL or runner ID", false}}
	if isWebURL(m.ActionURL) {
		actions = append(actions, uiAction{"open", "Open in GitHub", "Open this item in your browser", false})
	}
	if (m.ActiveFocus != FocusRepos && m.ActiveFocus != FocusConcerns) || m.ActionTarget == nil {
		return actions
	}
	if m.ActionTarget.IsNew {
		return append(actions, uiAction{"sync", "Clone repository", "Clone this repository into the workspace", false})
	}
	if m.ActionTarget.IsArchived {
		return append(actions, uiAction{"delete", "Delete archived clone", "Delete this local repository from disk. This cannot be undone.", true})
	}
	return append(actions,
		uiAction{"sync", "Sync repository", "Run the repository sync workflow, including stash/pull/reapply", false},
		uiAction{"sync-all", "Sync all repositories", "Run sync across every active repository", true},
		uiAction{"switch", "Switch branch", "Toggle between the original and default branch", false},
		uiAction{"push", "Commit, push and create PR", "Commit all changes, push, create a PR and switch to the default branch", true},
		uiAction{"prune", "Prune branches and worktrees", "Remove clean secondary worktrees, prune expired registrations, and delete non-default branches already reachable from a remote. Changed or unavailable worktrees and branches with local-only commits are kept.", true})
}
func (m *Model) captureActionTarget() {
	m.ActionTarget = nil
	if !m.detailVisible() && m.OpenRun == nil && m.selectionKey() == "" {
		m.ActionURL = ""
		return
	}
	if m.ActiveFocus == FocusRepos || m.ActiveFocus == FocusConcerns {
		index := m.SelectedIndex
		if !m.detailVisible() {
			entries := m.entries()
			if len(entries) == 0 {
				m.ActionURL = ""
				return
			}
			entry := entries[m.entryIndex(entries)]
			m.selectEntry(entry)
			index = entry.index
		}
		if index >= 0 && index < len(m.Repos) {
			m.ActionTarget = m.Repos[index].Clone()
		}
	}
	m.ActionURL = m.selectedURL()
}
func (m Model) menuContent() string {
	if m.PendingAction != "" {
		for _, a := range m.menuActions() {
			if a.id == m.PendingAction {
				target := "all active repositories"
				detail := a.detail
				if m.ActionTarget != nil && a.id != "sync-all" {
					target = m.ActionTarget.Name + "\n" + m.ActionTarget.Path
				}
				if m.ActionTarget != nil && a.id == "prune" {
					detail += "\nFreshen aborts on default-branch drift. Changed or unavailable worktrees and branches with commits absent from every remote are kept."
				}
				return "Confirm: " + a.label + "\n\n" + target + "\n\n" + detail + "\n\nEnter confirms · Esc cancels"
			}
		}
	}
	title := "Actions"
	if m.ActionTarget != nil {
		title += " / " + m.ActionTarget.Name
	}
	lines := []string{title, ""}
	actions := m.menuActions()
	count := max(1, m.bodyHeight()-5)
	start := max(0, m.MenuIndex-count+1)
	for i := start; i < min(len(actions), start+count); i++ {
		a := actions[i]
		prefix := "  "
		if i == m.MenuIndex {
			prefix = "› "
		}
		lines = append(lines, prefix+a.label)
	}
	if m.MenuIndex < len(actions) {
		lines = append(lines, "", actions[m.MenuIndex].detail)
	}
	if m.BusyAction != "" {
		lines = append(lines, "", "A repository action is already running.")
	}
	return strings.Join(lines, "\n")
}
func (m *Model) requestAction(id string) tea.Cmd {
	for _, a := range m.menuActions() {
		if a.id != id {
			continue
		}
		if id != "open" && id != "copy" && id != "refresh" && (m.BusyAction != "" || m.IsSyncing) {
			m.setToast("Wait for the current repository operation to finish.", 1)
			return nil
		}
		if a.confirm {
			m.PendingAction = id
			return nil
		}
		return m.executeAction(id)
	}
	m.setToast("That action is unavailable for this selection. Space shows available actions.", 1)
	return nil
}

type actionResultMsg struct {
	id, label, path string
	repo            *git.RepoItem
	err             error
}

func (m *Model) executeAction(id string) tea.Cmd {
	if id == "refresh" {
		return m.refreshScreen()
	}
	if id == "sync-all" {
		return m.handleKeySyncAll()
	}
	if id == "sync" {
		if m.ActionTarget == nil {
			return nil
		}
		m.BusyAction = "Syncing " + m.ActionTarget.Name
		return m.startSyncCmd([]*git.RepoItem{m.ActionTarget}, false, false)
	}
	target := m.ActionTarget
	url := m.ActionURL
	dir := m.TargetDir
	ctx := m.ctx
	label := id
	for _, a := range m.menuActions() {
		if a.id == id {
			label = a.label
		}
	}
	if id != "copy" && id != "open" {
		m.BusyAction = label + " · " + target.Name
	}
	m.ToastMsg = ""
	m.ToastPriority = 0
	work := func() actionResultMsg {
		result := actionResultMsg{id: id, label: label, repo: target}
		if target != nil {
			result.path = target.Path
		}
		switch id {
		case "copy":
			if url == "" {
				result.err = fmt.Errorf("no item selected")
			} else {
				result.err = copyToClipboard(url)
			}
		case "open":
			if !isWebURL(url) {
				result.err = fmt.Errorf("no GitHub URL selected")
				break
			}
			openCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			command := "xdg-open"
			args := []string{url}
			if runtime.GOOS == "darwin" {
				command = "open"
			}
			if runtime.GOOS == "windows" {
				command = "rundll32"
				args = []string{"url.dll,FileProtocolHandler", url}
			}
			result.err = exec.CommandContext(openCtx, command, args...).Run()
		case "switch":
			branch := target.OriginalBranch
			if target.CurrentBranch == branch {
				branch = target.DefaultBranch
			}
			result.err = git.SwitchBranch(target, branch)
		case "push":
			result.err = git.CommitPushPRAndSwitchDefault(ctx, target)
		case "prune":
			var pruned git.PruneResult
			pruned, result.err = git.PruneBranchesAndWorktrees(ctx, target)
			for _, path := range pruned.RemovedWorktrees {
				target.Logs = append(target.Logs, "Removed worktree: "+path)
			}
			for _, path := range pruned.PrunedWorktrees {
				target.Logs = append(target.Logs, "Pruned expired worktree registration: "+path)
			}
			for _, path := range pruned.DirtyWorktrees {
				target.Logs = append(target.Logs, "Kept worktree with local changes: "+path)
			}
			for _, path := range pruned.UnavailableWorktrees {
				target.Logs = append(target.Logs, "Kept registered worktree whose path is unavailable: "+path)
			}
			for _, branch := range pruned.ProtectedBranches {
				target.Logs = append(target.Logs, "Kept branch registered to a worktree: "+branch)
			}
			for _, branch := range pruned.UnpushedBranches {
				target.Logs = append(target.Logs, "Kept branch with local-only commits: "+branch)
			}
			for _, branch := range pruned.DeletedBranches {
				target.Logs = append(target.Logs, "Deleted local branch: "+branch)
			}
			for _, failure := range pruned.Failures {
				target.Logs = append(target.Logs, "Prune step failed: "+failure)
			}
			target.Logs = append(target.Logs, fmt.Sprintf("Prune removed %d worktrees, pruned %d stale registrations, and deleted %d branches; error: %v", len(pruned.RemovedWorktrees), len(pruned.PrunedWorktrees), len(pruned.DeletedBranches), result.err))
		case "delete":
			result.err = git.DeleteLocalRepo(dir, target.Path)
		}
		if target != nil && id != "delete" && id != "copy" && id != "open" {
			target.CurrentBranch = git.GetOriginalBranch(ctx, target.Path)
			target.BranchDetails = git.GetRepoBranchDetails(ctx, target.Path, target.DefaultBranch)
		}
		return result
	}
	return m.actionCmd(work)
}
func (m *Model) receiveAction(msg actionResultMsg) {
	if msg.id != "copy" && msg.id != "open" {
		m.BusyAction = ""
	}
	if msg.err != nil {
		m.setToast(fmt.Sprintf("%s failed: %v", msg.label, msg.err), 2)
	} else {
		m.setToast(msg.label+" complete", 1)
	}
	if msg.id == "delete" && msg.err == nil {
		if m.RemovedRepoPaths == nil {
			m.RemovedRepoPaths = make(map[string]uint64)
		}
		m.RemovedRepoPaths[msg.path] = m.OrgRefreshGeneration
		for i, r := range m.Repos {
			if r.Path == msg.path {
				m.Repos = append(m.Repos[:i], m.Repos[i+1:]...)
				break
			}
		}
		m.TotalCount = len(m.Repos)
		if len(m.Repos) == 0 {
			m.SelectedIndex = -1
		} else {
			m.SelectedIndex = max(0, min(m.SelectedIndex, len(m.Repos)-1))
		}
		m.Detail = false
	} else if msg.id != "copy" && msg.id != "open" {
		m.applyRepoSnapshot(msg.repo)
	}
	m.updateViewport()
}

func (m Model) confirmationFits() bool {
	return m.Width >= 30 && len(strings.Split(ansi.Wrap(m.menuContent(), max(1, m.Width-4), ""), "\n")) <= m.bodyHeight()
}

// Start the guarded worker immediately. A discarded Bubble Tea command cannot
// strand the WaitGroup; the buffered result never needs a surviving UI consumer.
func (m *Model) actionCmd(work func() actionResultMsg) tea.Cmd {
	results := make(chan actionResultMsg, 1)
	run := m.bgGuard(func() { results <- work() })
	go run()
	return func() tea.Msg { return <-results }
}
