package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/seankoji-com/freshen/pkg/config"
)

type setupModel struct {
	inputs []textinput.Model
	focus  int
	done   bool
	quit   bool
}

func newSetupModel(defaultDir string) setupModel {
	workspace := textinput.New()
	workspace.Placeholder = defaultDir
	workspace.SetValue(defaultDir)
	workspace.Focus()
	owner := textinput.New()
	owner.Placeholder = "Optional GitHub user or organization"
	return setupModel{inputs: []textinput.Model{workspace, owner}}
}

func (m setupModel) Init() tea.Cmd { return textinput.Blink }
func (m setupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "ctrl+c", "esc":
			m.quit = true
			return m, tea.Quit
		case "tab", "shift+tab", "up", "down":
			if key.String() == "shift+tab" || key.String() == "up" {
				m.focus = (m.focus + len(m.inputs) - 1) % len(m.inputs)
			} else {
				m.focus = (m.focus + 1) % len(m.inputs)
			}
			for i := range m.inputs {
				if i == m.focus {
					m.inputs[i].Focus()
				} else {
					m.inputs[i].Blur()
				}
			}
			return m, nil
		case "enter":
			if strings.TrimSpace(m.inputs[0].Value()) != "" {
				m.done = true
				return m, tea.Quit
			}
		}
	}
	var cmd tea.Cmd
	m.inputs[m.focus], cmd = m.inputs[m.focus].Update(msg)
	return m, cmd
}
func (m setupModel) View() string {
	return lipgloss.NewStyle().Padding(1, 2).Render("Freshen first-run setup\n\nWorkspace (sibling repositories):\n" + m.inputs[0].View() + "\n\nGitHub owner (optional; enables sync and Actions):\n" + m.inputs[1].View() + "\n\nEnter to save • Tab to switch • Esc to cancel")
}

func runFirstSetup(defaultDir string) (config.Config, error) {
	p, err := tea.NewProgram(newSetupModel(defaultDir)).Run()
	if err != nil {
		return config.Config{}, err
	}
	m := p.(setupModel)
	if m.quit || !m.done {
		return config.Config{}, fmt.Errorf("setup cancelled")
	}
	workspace, err := filepath.Abs(strings.TrimSpace(m.inputs[0].Value()))
	if err != nil {
		return config.Config{}, err
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return config.Config{}, err
	}
	c := config.Config{Workspace: workspace, Owner: strings.TrimSpace(m.inputs[1].Value()), Concurrency: 4}
	return c, config.Save(c)
}
