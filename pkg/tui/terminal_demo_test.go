package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

type terminalDemo struct{ Model }

func (m terminalDemo) Init() tea.Cmd { return m.Spinner.Tick }
func (m terminalDemo) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Sync commands start workers while being constructed, so intercept actions
	// before Update, not merely by dropping its returned command.
	if k, ok := msg.(tea.KeyMsg); ok && !m.Filtering {
		block := strings.Contains("sboyc", k.String()) && len(k.String()) == 1
		if k.String() == "enter" && m.PendingAction != "" {
			block = true
			m.PendingAction = ""
		}
		if k.String() == "enter" && m.MenuOpen {
			actions := m.menuActions()
			if len(actions) > 0 && !actions[m.MenuIndex].confirm {
				block = true
				m.MenuOpen = false
			}
		}
		if block {
			m.setToast("Demo: external operation disabled", 1)
			return m, nil
		}
	}
	// Quit only through bindings the production handler honours right now, so
	// the demo cannot mask a hint that advertises a suspended key.
	keys := m.keys()
	next, cmd := m.Model.Update(msg)
	updated := next.(Model)
	settleDemoRequests(&updated)
	next = updated
	if k, ok := msg.(tea.KeyMsg); ok && (key.Matches(k, keys.ForceQuit) || key.Matches(k, keys.Quit)) {
		return next, tea.Quit
	}
	if _, ok := msg.(spinner.TickMsg); ok {
		return terminalDemo{next.(Model)}, cmd
	}
	return terminalDemo{next.(Model)}, nil
}

// settleDemoRequests completes, with the seeded data, every request the demo
// dropped: it runs no commands, so nothing else would clear their loading flags.
func settleDemoRequests(m *Model) {
	now := time.Now()
	m.LogLoading = ""
	m.RunLoading = false
	m.RepoDetailLoading = ""
	m.RepoDetailQueued = ""
	if m.IsRunnersLoading {
		m.IsRunnersLoading = false
		m.RunnerFetchFailed = false
		m.LastRunnersRefresh = now
	}
	if m.IsJobQueueLoading {
		m.IsJobQueueLoading = false
		m.LastActionsRefresh = now
	}
	if m.IsOrgSyncing {
		m.IsOrgSyncing = false
		m.LastOrgRefresh = now
	}
	for _, r := range m.Repos {
		if r.IsLoadingIssues {
			r.IsLoadingIssues, r.HasLoadedIssues = false, true
		}
		if r.IsLoadingPRs {
			r.IsLoadingPRs, r.HasLoadedPRs = false, true
		}
	}
	m.updateViewport()
}

func TestTerminalDemo(t *testing.T) {
	if os.Getenv("FRESHEN_DEMO") != "1" {
		t.Skip("manual terminal fixture")
	}
	m := smokeFixtureModel()
	if _, err := tea.NewProgram(terminalDemo{m}, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run(); err != nil {
		t.Fatal(err)
	}
}
