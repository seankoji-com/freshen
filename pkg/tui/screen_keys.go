package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/seankoji-com/freshen/pkg/jobs"
)

func (m *Model) screenKey(msg tea.KeyMsg) tea.Cmd {
	k := msg.String()
	if k == "ctrl+c" {
		cmd, _ := m.handleKeyQuit()
		return cmd
	}
	if m.Filtering {
		switch k {
		case "esc":
			m.Search.SetValue("")
			m.Filtering = false
			m.Search.Blur()
		case "enter":
			m.Filtering = false
			m.Search.Blur()
			m.moveSelection(0)
		default:
			var cmd tea.Cmd
			m.Search, cmd = m.Search.Update(msg)
			return cmd
		}
		return nil
	}
	if m.ShowHelp {
		maxOffset := max(0, len(strings.Split(m.helpContent(), "\n"))-m.bodyHeight())
		switch k {
		case "down", "j":
			m.HelpOffset = min(maxOffset, m.HelpOffset+1)
		case "up", "k":
			m.HelpOffset = max(0, m.HelpOffset-1)
		case "pgdown":
			m.HelpOffset = min(maxOffset, m.HelpOffset+m.bodyHeight())
		case "pgup":
			m.HelpOffset = max(0, m.HelpOffset-m.bodyHeight())
		}
		if k == "?" || k == "esc" {
			m.ShowHelp = false
		}
		return nil
	}
	if m.PendingAction != "" {
		if k == "esc" {
			m.PendingAction = ""
			m.ActionTarget = nil
			return nil
		}
		if k == "enter" {
			if !m.confirmationFits() {
				m.setToast("Enlarge the terminal to read the full confirmation, or Esc to cancel.", 2)
				return nil
			}
			action := m.PendingAction
			m.PendingAction = ""
			return m.executeAction(action)
		}
		return nil
	}
	if m.MenuOpen {
		switch k {
		case "esc", " ":
			m.MenuOpen = false
		case "up", "k":
			m.MenuIndex = max(0, m.MenuIndex-1)
		case "down", "j":
			m.MenuIndex = min(len(m.menuActions())-1, m.MenuIndex+1)
		case "enter":
			actions := m.menuActions()
			if len(actions) > 0 {
				a := actions[m.MenuIndex]
				m.MenuOpen = false
				return m.requestAction(a.id)
			}
		}
		return nil
	}
	switch k {
	case "q":
		cmd, _ := m.handleKeyQuit()
		return cmd
	case "?":
		m.ShowHelp = true
	case "1":
		m.changeScreen(FocusRepos)
	case "2":
		m.changeScreen(FocusJobs)
	case "3":
		m.changeScreen(FocusRunners)
	case "tab", "shift+tab":
		screens := []FocusType{FocusRepos, FocusJobs, FocusRunners}
		i := 0
		for n, f := range screens {
			if f == m.ActiveFocus {
				i = n
			}
		}
		if k == "tab" {
			i = (i + 1) % 3
		} else {
			i = (i + 2) % 3
		}
		m.changeScreen(screens[i])
	case "esc", "left", "h":
		switch {
		case m.OpenJobID != "":
			m.OpenJobID = ""
			m.Viewport.GotoTop()
		case m.OpenRun != nil:
			m.OpenRun = nil
			m.Search.SetValue("")
		case m.Detail:
			m.Detail = false
		case m.Search.Value() != "":
			m.Search.SetValue("")
		default:
			m.ToastMsg = ""
			m.ToastPriority = 0
		}
	case "/":
		if !m.detailVisible() {
			m.Filtering = true
			return m.Search.Focus()
		}
	case "up", "k", "down", "j", "pgup", "pgdown", "home", "end":
		delta := 1
		if k == "up" || k == "k" {
			delta = -1
		}
		if k == "pgup" {
			delta = -max(1, m.bodyHeight()/2)
		}
		if k == "pgdown" {
			delta = max(1, m.bodyHeight()/2)
		}
		if m.detailVisible() {
			switch k {
			case "home":
				m.Viewport.GotoTop()
			case "end":
				m.Viewport.GotoBottom()
			default:
				if delta < 0 {
					m.Viewport.ScrollUp(-delta)
				} else {
					m.Viewport.ScrollDown(delta)
				}
			}
		} else {
			if k == "home" {
				delta = -len(m.entries())
			}
			if k == "end" {
				delta = len(m.entries())
			}
			m.moveSelection(delta)
		}
	case "enter", "right", "l":
		return m.openEntry()
	case "[", "]":
		if m.Detail && m.ActiveFocus == FocusRepos {
			if k == "]" {
				m.ActiveTab = (m.ActiveTab + 1) % 4
			} else {
				m.ActiveTab = (m.ActiveTab + 3) % 4
			}
			m.Viewport.GotoTop()
			m.updateViewport()
			return tea.Batch(m.loadRepoDetails(), m.triggerTabFetch())
		}
	case "v":
		if m.ActiveFocus == FocusJobs && m.OpenRun == nil {
			m.QueueView = !m.QueueView
			m.Search.SetValue("")
		}
	case "f":
		if m.ActiveFocus == FocusJobs && m.OpenRun == nil {
			m.ActionsFilter = (m.ActionsFilter + 1) % 3
			m.moveSelection(0)
		}
	case "r":
		return m.refreshScreen()
	case " ":
		m.captureActionTarget()
		m.MenuIndex = 0
		m.MenuOpen = true
	case "o", "y", "c", "s", "a", "b", "p", "X", "d":
		ids := map[string]string{"o": "open", "y": "copy", "c": "copy", "s": "sync", "a": "sync-all", "b": "switch", "p": "push", "X": "prune", "d": "delete"}
		m.captureActionTarget()
		return m.requestAction(ids[k])
	}
	return nil
}
func (m *Model) changeScreen(f FocusType) {
	m.ActiveFocus = f
	m.Detail = false
	m.OpenRun = nil
	m.OpenJobID = ""
	m.Search.SetValue("")
	m.Filtering = false
	m.Viewport.GotoTop()
	m.moveSelection(0)
	m.updateViewport()
}
func (m *Model) openEntry() tea.Cmd {
	if m.detailVisible() {
		return nil
	}
	es := m.entries()
	if len(es) == 0 {
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
		} else {
			m.OpenJobID = e.key
			m.updateViewport()
			return m.fetchOpenJobLogs()
		}
	default:
		m.Detail = true
		m.updateViewport()
		if m.ActiveFocus == FocusRepos {
			return tea.Batch(m.loadRepoDetails(), m.triggerTabFetch())
		}
	}
	return nil
}
func (m *Model) refreshScreen() tea.Cmd {
	switch m.ActiveFocus {
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
			m.IsRunnersLoading = true
			return m.loadRunnersCmd()
		}
	case FocusRepos:
		if m.Detail {
			return tea.Batch(m.loadRepoDetails(), m.triggerTabFetch())
		}
		if !m.IsOrgSyncing {
			m.IsOrgSyncing = true
			return m.loadOrgReposCmd(false)
		}
	}
	return nil
}
func (m *Model) fetchOpenJobLogs() tea.Cmd {
	j := m.openJob()
	if j == nil || j.GHJobID == 0 || m.LogLoading != "" {
		return nil
	}
	m.LogLoading = j.ID
	m.updateViewport()
	return m.loadJobLogsCmd(j)
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
		x := 0
		for i, tab := range []struct {
			name  string
			focus FocusType
		}{{"Repositories", FocusRepos}, {"Actions", FocusJobs}, {"Runners", FocusRunners}} {
			w := len(fmt.Sprintf("%d %s", i+1, tab.name)) + 3
			if msg.X >= x && msg.X < x+w {
				m.changeScreen(tab.focus)
				return nil
			}
			x += w
		}
	}
	if !m.detailVisible() && msg.Y >= 4 && msg.Y < 4+m.bodyHeight() && msg.X >= 2 && msg.X < m.Width-2 {
		entries, _, _ := m.visibleEntries()
		i := (msg.Y - 4) / 2
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
	case FocusRepos:
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
