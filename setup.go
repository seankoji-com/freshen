package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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
	return setupModel{inputs: []textinput.Model{workspace}}
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
	return lipgloss.NewStyle().Padding(1, 2).Render("Freshen first-run setup\n\nWorkspace (sibling repositories):\n" + m.inputs[0].View() + "\n\nEnter to save • Esc to cancel")
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
	c := config.Config{Workspace: workspace, Concurrency: 4}
	return c, config.Save(c)
}

// ownerPromptModel asks for the GitHub owner on any boot where none is
// configured. Unlike the workspace, an owner is never assumed or defaulted:
// declining (Esc or blank Enter) just runs this session in local-only mode
// without persisting anything, so the prompt reappears next launch.
type ownerPromptModel struct {
	input  textinput.Model
	errMsg string
	done   bool
	quit   bool
}

func newOwnerPromptModel(errMsg string) ownerPromptModel {
	owner := textinput.New()
	owner.Placeholder = "e.g. your-username or your-org"
	owner.Focus()
	return ownerPromptModel{input: owner, errMsg: errMsg}
}

func (m ownerPromptModel) Init() tea.Cmd { return textinput.Blink }
func (m ownerPromptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "ctrl+c", "esc":
			m.quit = true
			return m, tea.Quit
		case "enter":
			m.done = true
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}
func (m ownerPromptModel) View() string {
	body := "No GitHub owner configured\n\nGitHub user or organization (enables sync, PRs/issues, Actions):\n" + m.input.View()
	if m.errMsg != "" {
		body = "No GitHub owner configured\n\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render(m.errMsg) + "\n\nGitHub user or organization (enables sync, PRs/issues, Actions):\n" + m.input.View()
	}
	return lipgloss.NewStyle().Padding(1, 2).Render(body + "\n\nEnter to continue • Esc to skip for this run")
}

// runOwnerPrompt asks for a GitHub owner and returns the trimmed value the
// user entered, or "" if they skipped (Esc, Ctrl+C, or a blank Enter). errMsg,
// if non-empty, is shown above the input (e.g. after a prior attempt wasn't found).
func runOwnerPrompt(errMsg string) (string, error) {
	p, err := tea.NewProgram(newOwnerPromptModel(errMsg)).Run()
	if err != nil {
		return "", err
	}
	m := p.(ownerPromptModel)
	if m.quit || !m.done {
		return "", nil
	}
	return strings.TrimSpace(m.input.Value()), nil
}

// ownerExists reports whether owner is a real GitHub user or organization
// login, so a mistyped owner from the prompt is never persisted to config.
func ownerExists(owner string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "api", fmt.Sprintf("users/%s", owner), "--silent")
	return cmd.Run() == nil
}
