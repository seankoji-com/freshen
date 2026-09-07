package tui

import (
	"os"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

type terminalDemo struct{ Model }

func (m terminalDemo) Init() tea.Cmd { return m.Spinner.Tick }
func (m terminalDemo) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.Model.Update(msg)
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
