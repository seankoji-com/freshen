package tui

import tea "github.com/charmbracelet/bubbletea"

func (m *Model) handleKeyMsg(msg tea.KeyMsg) (tea.Cmd, bool) {
	return m.screenKey(msg), true
}

func (m *Model) handleKeyQuit() (tea.Cmd, bool) {
	if m.cancel != nil {
		m.cancel()
	}
	return tea.Quit, true
}

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
