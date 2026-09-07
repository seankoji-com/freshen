package tui

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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
	if m.ActiveFocus != FocusRepos || m.ActionTarget == nil {
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
		uiAction{"prune", "Prune branches and worktrees", "Force-remove worktrees and delete non-default local branches. Uncommitted work may be lost.", true})
}
func (m *Model) captureActionTarget() {
	if !m.detailVisible() {
		m.moveSelection(0)
	}
	m.ActionTarget = nil
	if m.ActiveFocus == FocusRepos && m.SelectedIndex >= 0 && m.SelectedIndex < len(m.Repos) {
		m.ActionTarget = m.Repos[m.SelectedIndex].Clone()
	}
	m.ActionURL = m.selectedURL()
}
func (m Model) menuContent() string {
	if m.PendingAction != "" {
		for _, a := range m.menuActions() {
			if a.id == m.PendingAction {
				target := "all active repositories"
				if m.ActionTarget != nil && a.id != "sync-all" {
					target = m.ActionTarget.Name + "\n" + m.ActionTarget.Path
				}
				return "Confirm: " + a.label + "\n\n" + target + "\n\n" + a.detail + "\n\nEnter confirms · Esc cancels"
			}
		}
	}
	title := "Actions"
	if m.ActionTarget != nil {
		title += " / " + m.ActionTarget.Name
	}
	lines := []string{title, ""}
	for i, a := range m.menuActions() {
		prefix := "  "
		if i == m.MenuIndex {
			prefix = "› "
		}
		lines = append(lines, prefix+a.label)
	}
	actions := m.menuActions()
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
			var n int
			n, result.err = git.PruneBranchesAndWorktrees(ctx, target.Path, target.DefaultBranch)
			target.Logs = append(target.Logs, fmt.Sprintf("Prune removed %d branches; error: %v", n, result.err))
		case "delete":
			result.err = git.DeleteLocalRepo(dir, target.Path)
		}
		if target != nil && id != "delete" && id != "copy" && id != "open" {
			target.CurrentBranch = git.GetOriginalBranch(ctx, target.Path)
			target.BranchDetails = git.GetRepoBranchDetails(ctx, target.Path, target.DefaultBranch)
		}
		return result
	}
	return func() tea.Msg {
		var result actionResultMsg
		m.bgGuard(func() { result = work() })()
		return result
	}
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
		for i, r := range m.Repos {
			if r.Path == msg.path {
				m.Repos = append(m.Repos[:i], m.Repos[i+1:]...)
				break
			}
		}
		m.TotalCount = len(m.Repos)
		m.SelectedIndex = max(0, min(m.SelectedIndex, len(m.Repos)-1))
		m.Detail = false
	} else if msg.id != "copy" && msg.id != "open" {
		m.applyRepoSnapshot(msg.repo)
	}
	m.updateViewport()
}
