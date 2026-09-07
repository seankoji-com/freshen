package tui

import (
	"os"
	"strings"
	"testing"

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
	next, cmd := m.Model.Update(msg)
	updated := next.(Model)
	updated.LogLoading = ""
	updated.RunLoading = false
	updated.RepoDetailLoading = ""
	updated.updateViewport()
	next = updated
	if k, ok := msg.(tea.KeyMsg); ok && (k.String() == "q" || k.String() == "ctrl+c") {
		return next, tea.Quit
	}
	if _, ok := msg.(spinner.TickMsg); ok {
		return terminalDemo{next.(Model)}, cmd
	}
	return terminalDemo{next.(Model)}, nil
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
