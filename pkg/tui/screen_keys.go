package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/seankoji-com/freshen/pkg/git"
	"github.com/seankoji-com/freshen/pkg/jobs"
)

func (m *Model) screenKey(msg tea.KeyMsg) tea.Cmd {
	k := m.keys()
	if key.Matches(msg, k.ForceQuit) {
		cmd, _ := m.handleKeyQuit()
		return cmd
	}
	switch {
	case m.Filtering:
		switch {
		case key.Matches(msg, k.FilterClear):
			m.Search.SetValue("")
			m.Filtering = false
			m.Search.Blur()
		case key.Matches(msg, k.FilterApply):
			m.Filtering = false
			m.Search.Blur()
			m.moveSelection(0)
		default:
			var cmd tea.Cmd
			m.sizeSearch()
			m.Search, cmd = m.Search.Update(msg)
			return cmd
		}
		return nil
	case m.PendingAction != "":
		switch {
		case key.Matches(msg, k.Cancel):
			m.PendingAction = ""
			m.ActionTarget = nil
		case key.Matches(msg, k.Confirm):
			if !m.confirmationFits() {
				m.setToast("Enlarge the terminal to read the full confirmation, or Esc to cancel.", 2)
				return nil
			}
			action := m.PendingAction
			m.PendingAction = ""
			return m.executeAction(action)
		}
		return nil
	case m.MenuOpen:
		switch {
		case key.Matches(msg, k.MenuClose):
			m.MenuOpen = false
		case key.Matches(msg, k.Up):
			m.MenuIndex = max(0, m.MenuIndex-1)
		case key.Matches(msg, k.Down):
			m.MenuIndex = min(len(m.menuActions())-1, m.MenuIndex+1)
		case key.Matches(msg, k.MenuSelect):
			actions := m.menuActions()
			if len(actions) > 0 {
				a := actions[m.MenuIndex]
				m.MenuOpen = false
				return m.requestAction(a.id)
			}
		}
		return nil
	case m.ShowHelp:
		maxOffset := m.helpMaxOffset()
		switch {
		case key.Matches(msg, k.Help), key.Matches(msg, k.Back):
			m.ShowHelp = false
		case key.Matches(msg, k.Down):
			m.HelpOffset = min(maxOffset, m.HelpOffset+1)
		case key.Matches(msg, k.Up):
			m.HelpOffset = max(0, m.HelpOffset-1)
		case key.Matches(msg, k.PageDown):
			m.HelpOffset = min(maxOffset, m.HelpOffset+m.bodyHeight())
		case key.Matches(msg, k.PageUp):
			m.HelpOffset = max(0, m.HelpOffset-m.bodyHeight())
		case key.Matches(msg, k.Home):
			m.HelpOffset = 0
		case key.Matches(msg, k.End):
			m.HelpOffset = maxOffset
		case key.Matches(msg, k.Quit, k.Jump, k.NextScreen, k.PrevScreen, k.Refresh, k.Filter):
			// Global keys keep their meaning over the overlay: close it and act.
			m.ShowHelp = false
			return m.globalKey(msg, m.keys())
		}
		return nil
	}
	if cmd, handled := m.globalKeyHandled(msg, k); handled {
		return cmd
	}
	switch {
	case key.Matches(msg, k.Up, k.Down, k.PageUp, k.PageDown, k.Home, k.End):
		m.moveOrScroll(msg, k)
	case key.Matches(msg, k.Open):
		return m.openEntry()
	case key.Matches(msg, k.PrevTab, k.NextTab):
		if key.Matches(msg, k.NextTab) {
			m.ActiveTab = (m.ActiveTab + 1) % 4
		} else {
			m.ActiveTab = (m.ActiveTab + 3) % 4
		}
		m.Viewport.GotoTop()
		return m.loadRepoDetail()
	case key.Matches(msg, k.ToggleQueue):
		m.QueueView = !m.QueueView
		m.Search.SetValue("")
	case key.Matches(msg, k.CycleRuns):
		m.ActionsFilter = (m.ActionsFilter + 1) % 3
		m.moveSelection(0)
	case key.Matches(msg, k.Menu):
		m.captureActionTarget()
		m.MenuIndex = 0
		m.MenuOpen = true
	case key.Matches(msg, k.Browser, k.Copy, k.Sync, k.SyncAll, k.Switch, k.Push, k.Prune, k.Delete):
		ids := []struct {
			binding key.Binding
			id      string
		}{{k.Browser, "open"}, {k.Copy, "copy"}, {k.Sync, "sync"}, {k.SyncAll, "sync-all"}, {k.Switch, "switch"}, {k.Push, "push"}, {k.Prune, "prune"}, {k.Delete, "delete"}}
		for _, a := range ids {
			if key.Matches(msg, a.binding) {
				m.captureActionTarget()
				return m.requestAction(a.id)
			}
		}
	}
	return nil
}

// globalKeyHandled applies a global binding if msg matches one.
func (m *Model) globalKeyHandled(msg tea.KeyMsg, k keyMap) (tea.Cmd, bool) {
	if !key.Matches(msg, k.Quit, k.Help, k.Back, k.Refresh, k.Filter, k.Jump, k.NextScreen, k.PrevScreen) {
		return nil, false
	}
	return m.globalKey(msg, k), true
}

func (m *Model) globalKey(msg tea.KeyMsg, k keyMap) tea.Cmd {
	switch {
	case key.Matches(msg, k.Quit):
		cmd, _ := m.handleKeyQuit()
		return cmd
	case key.Matches(msg, k.Help):
		m.ShowHelp = true
		m.HelpOffset = 0
	case key.Matches(msg, k.Jump):
		i := int(msg.String()[0] - '0')
		return m.changeScreen(screenOrder[i])
	case key.Matches(msg, k.NextScreen, k.PrevScreen):
		i := 0
		for n, f := range screenOrder {
			if f == m.ActiveFocus {
				i = n
			}
		}
		if key.Matches(msg, k.NextScreen) {
			i = (i + 1) % len(screenOrder)
		} else {
			i = (i + len(screenOrder) - 1) % len(screenOrder)
		}
		return m.changeScreen(screenOrder[i])
	case key.Matches(msg, k.Back):
		m.back()
	case key.Matches(msg, k.Filter):
		return m.startFilter()
	case key.Matches(msg, k.Refresh):
		return m.refreshScreen()
	}
	return nil
}

// back leaves the innermost layer: job detail, repository/runner detail, an
// active filter, an open run, and finally a status message.
func (m *Model) back() {
	switch {
	case m.OpenJobID != "":
		m.OpenJobID = ""
		m.Viewport.GotoTop()
	case m.Detail:
		m.Detail = false
		if m.ActiveFocus == FocusConcerns {
			entries := m.entries()
			if len(entries) == 0 {
				m.ScreenCursor[FocusConcerns] = ""
				m.SelectedIndex = -1
			} else {
				m.selectEntry(entries[m.entryIndex(entries)])
			}
		}
	case m.Search.Value() != "":
		m.Search.SetValue("")
	case m.OpenRun != nil:
		m.OpenRun = nil
	default:
		m.clearToast()
	}
}

// startFilter focuses the filter for the nearest list: a detail view returns
// to the list it came from, and Overview, which has no list, opens Repositories.
func (m *Model) startFilter() tea.Cmd {
	var cmd tea.Cmd
	if m.ActiveFocus == FocusOverview {
		cmd = m.changeScreen(FocusRepos)
	}
	if m.OpenJobID != "" {
		m.OpenJobID = ""
		m.Viewport.GotoTop()
	}
	if m.Detail {
		m.back()
	}
	m.Filtering = true
	m.sizeSearch()
	return tea.Batch(cmd, m.Search.Focus())
}

func (m *Model) moveOrScroll(msg tea.KeyMsg, k keyMap) {
	delta := 1
	switch {
	case key.Matches(msg, k.Up):
		delta = -1
	case key.Matches(msg, k.PageUp):
		delta = -max(1, m.bodyHeight()/2)
	case key.Matches(msg, k.PageDown):
		delta = max(1, m.bodyHeight()/2)
	}
	if m.detailVisible() {
		switch {
		case key.Matches(msg, k.Home):
			m.Viewport.GotoTop()
		case key.Matches(msg, k.End):
			m.Viewport.GotoBottom()
		case delta < 0:
			m.Viewport.ScrollUp(-delta)
		default:
			m.Viewport.ScrollDown(delta)
		}
		return
	}
	switch {
	case key.Matches(msg, k.Home):
		delta = -len(m.entries())
	case key.Matches(msg, k.End):
		delta = len(m.entries())
	}
	m.moveSelection(delta)
}

// changeScreen opens a top-level screen from anywhere, discarding drill-downs.
// Entering a screen that shows runner data refreshes it if polling was paused.
func (m *Model) changeScreen(f FocusType) tea.Cmd {
	m.ActiveFocus = f
	m.Detail = false
	m.OpenRun = nil
	m.OpenJobID = ""
	m.Search.SetValue("")
	m.Filtering = false
	m.Viewport.GotoTop()
	m.moveSelection(0)
	m.updateViewport()
	return m.resumeRunnerPolling()
}
func (m *Model) openEntry() tea.Cmd {
	if m.detailVisible() {
		return nil
	}
	es := m.entries()
	if len(es) == 0 {
		return nil
	}
	if m.selectionKey() == "" {
		m.selectEntry(es[0])
		return nil
	}
	e := es[m.entryIndex(es)]
	m.selectEntry(e)
	m.Viewport.GotoTop()
	switch m.ActiveFocus {
	case FocusJobs:
		if m.OpenRun == nil {
			if m.QueueView {
				j := m.JobQueue[e.index]
				m.OpenRun = j.Run
				if m.OpenRun == nil {
					return nil
				}
				m.OpenJobID = j.ID
				m.JobCursor = j.ID
				m.Search.SetValue("")
				m.updateViewport()
				return m.fetchOpenJobLogs()
			}
			rs := m.actionRuns()
			m.OpenRun = rs[e.index].run
			m.JobCursor = ""
			m.Search.SetValue("")
			if !m.OpenRun.JobsKnown {
				return m.loadOpenRun()
			}
			m.moveSelection(0)
		} else {
			m.OpenJobID = e.key
			m.updateViewport()
			return m.fetchOpenJobLogs()
		}
	default:
		m.Detail = true
		if m.ActiveFocus == FocusRepos || m.ActiveFocus == FocusConcerns {
			return m.loadRepoDetail()
		}
		m.updateViewport()
	}
	return nil
}
func (m *Model) refreshScreen() tea.Cmd {
	switch m.ActiveFocus {
	case FocusOverview:
		if m.LocalScanUnresponsive {
			git.ResetLocalDirectoryScanFailures(m.TargetDir)
			m.LocalScanUnresponsive = false
		}
		var cmds []tea.Cmd
		if refresh := m.startOrgRefresh(false, true); refresh != nil {
			cmds = append(cmds, refresh)
		}
		if m.TargetOrg != "" && !m.IsJobQueueLoading {
			m.IsJobQueueLoading = true
			cmds = append(cmds, m.loadJobQueueCmd())
		}
		if m.TargetOrg != "" && !m.IsRunnersLoading {
			cmds = append(cmds, m.startRunnerFetch())
		}
		if len(cmds) > 0 {
			return tea.Batch(cmds...)
		}
	case FocusJobs:
		if m.OpenJobID != "" {
			return tea.Batch(m.loadOpenRun(), m.fetchOpenJobLogs())
		}
		if m.OpenRun != nil {
			return m.loadOpenRun()
		}
		if m.TargetOrg != "" && !m.IsJobQueueLoading {
			m.IsJobQueueLoading = true
			return m.loadJobQueueCmd()
		}
	case FocusRunners:
		if m.TargetOrg != "" && !m.IsRunnersLoading {
			return m.startRunnerFetch()
		}
	case FocusRepos, FocusConcerns:
		if m.Detail {
			return m.loadRepoDetail()
		}
		if m.LocalScanUnresponsive {
			git.ResetLocalDirectoryScanFailures(m.TargetDir)
			m.LocalScanUnresponsive = false
		}
		return m.startOrgRefresh(false, true)
	}
	return nil
}
func (m *Model) fetchOpenJobLogs() tea.Cmd {
	j := m.openJob()
	if j == nil || j.GHJobID == 0 {
		return nil
	}
	cmd := m.startLogFetch(j)
	m.updateViewport()
	return cmd
}

type runJobsLoadedMsg struct {
	run   *jobs.RunItem
	infos []jobs.GHJobInfo
	err   error
}

func (m *Model) loadOpenRun() tea.Cmd {
	if m.OpenRun == nil || m.RunLoading {
		return nil
	}
	m.RunLoading = true
	run := *m.OpenRun
	org := m.TargetOrg
	return func() tea.Msg {
		infos, err := jobs.FetchRunJobs(org, run.Repo, run.ID)
		return runJobsLoadedMsg{run: &run, infos: infos, err: err}
	}
}
func (m *Model) receiveRunJobs(msg runJobsLoadedMsg) {
	m.RunLoading = false
	// Navigating away makes an in-flight response irrelevant to the current detail.
	if m.OpenRun == nil || runKey(m.OpenRun) != runKey(msg.run) || m.OpenRun.Attempt != msg.run.Attempt {
		return
	}
	if msg.err != nil {
		copyRun := *m.OpenRun
		copyRun.JobsError = jobs.SanitizeTerminal(msg.err.Error())
		m.OpenRun = &copyRun
		m.setToast("Job details unavailable: "+copyRun.JobsError, 2)
		return
	}
	run := *m.OpenRun
	run.JobsKnown = true
	run.JobsStale = false
	run.JobsError = ""
	m.OpenRun = &run
	logs := map[string][]string{}
	var queue []*jobs.JobItem
	for _, j := range m.JobQueue {
		if j.Repo == run.Repo && j.RunID == run.ID {
			logs[j.ID] = j.Logs
			if j.IsRunHeader {
				j.Run = m.OpenRun
				queue = append(queue, j)
			}
		} else {
			queue = append(queue, j)
		}
	}
	for _, j := range jobs.JobsForRun(m.OpenRun, msg.infos) {
		j.Logs = logs[j.ID]
		queue = append(queue, j)
	}
	m.JobQueue = queue
	if m.JobCursor == "" {
		m.moveSelection(0)
	}
	m.updateViewport()
}
func (m *Model) screenMouse(msg tea.MouseMsg) tea.Cmd {
	if m.MenuOpen || m.PendingAction != "" || m.Filtering || m.ShowHelp {
		return nil
	}
	if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
		delta := 3
		if msg.Button == tea.MouseButtonWheelUp {
			delta = -3
		}
		if m.detailVisible() {
			if delta < 0 {
				m.Viewport.ScrollUp(-delta)
			} else {
				m.Viewport.ScrollDown(delta)
			}
		} else {
			m.moveSelection(delta)
		}
		return nil
	}
	if msg.Button != tea.MouseButtonLeft || msg.Action != tea.MouseActionPress {
		return nil
	}
	if msg.Y == 1 {
		for _, tab := range m.screenTabs() {
			if msg.X >= tab.start && msg.X < tab.end {
				return m.changeScreen(tab.focus)
			}
		}
	}
	if m.ActiveFocus != FocusOverview && !m.detailVisible() && msg.Y >= headerRows && msg.Y < headerRows+m.bodyHeight() && msg.X >= 1 && msg.X < m.Width-1 {
		entries, _, _ := m.visibleEntries()
		bodyRow := msg.Y - headerRows
		headerRows := m.listHeaderRows()
		if bodyRow < headerRows {
			return nil
		}
		i := (bodyRow - headerRows) / 2
		if i < len(entries) {
			m.selectEntry(entries[i])
		}
	}
	return nil
}
func (m Model) selectedURL() string {
	if m.OpenJobID != "" {
		if j := m.openJob(); j != nil {
			return m.jobURL(j)
		}
		return ""
	}
	if m.OpenRun != nil {
		return fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d", m.TargetOrg, m.OpenRun.Repo, m.OpenRun.ID)
	}
	es := m.entries()
	if len(es) == 0 {
		return ""
	}
	e := es[m.entryIndex(es)]
	switch m.ActiveFocus {
	case FocusRepos, FocusConcerns:
		r := m.Repos[e.index]
		repo := r.GHRepoName
		if repo == "" {
			repo = r.Name
		}
		if m.TargetOrg != "" {
			return "https://github.com/" + m.TargetOrg + "/" + repo
		}
		return r.Path
	case FocusJobs:
		if m.QueueView {
			return m.jobURL(m.JobQueue[e.index])
		}
		r := m.actionRuns()[e.index].run
		return fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d", m.TargetOrg, r.Repo, r.ID)
	case FocusRunners:
		return m.getMatchingRunners()[e.index].ID
	}
	return ""
}
func isWebURL(s string) bool { return strings.HasPrefix(s, "https://github.com/") }
