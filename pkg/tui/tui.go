package tui

import (
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/seankoji-com/freshen/pkg/git"
)

// --- Color Palette & Lip Gloss Styles ---
var (
	colorPrimary   = lipgloss.Color("#7D56F4") // Electric Purple
	colorSecondary = lipgloss.Color("#00F5D4") // Bright Mint / Cyan
	colorGreen     = lipgloss.Color("#10B981") // Emerald Green
	colorYellow    = lipgloss.Color("#F59E0B") // Warm Amber
	colorRed       = lipgloss.Color("#EF4444") // Coral Red
	colorBlue      = lipgloss.Color("#3B82F6") // Bright Blue
	colorMuted     = lipgloss.Color("#6C7086") // Muted Slate

	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(colorPrimary).
			Padding(0, 1)

	subtitleStyle = lipgloss.NewStyle().
			Foreground(colorSecondary).
			Bold(true)

	badgeUpToDate = lipgloss.NewStyle().
			Foreground(colorGreen).
			Bold(true)

	badgeUpdated = lipgloss.NewStyle().
			Foreground(colorSecondary).
			Bold(true)

	badgeRebased = lipgloss.NewStyle().
			Foreground(colorBlue).
			Bold(true)

	badgeStashedPR = lipgloss.NewStyle().
			Foreground(colorYellow).
			Bold(true)

	badgeError = lipgloss.NewStyle().
			Foreground(colorRed).
			Bold(true)

	badgeArchived = lipgloss.NewStyle().
			Foreground(colorMuted).
			Strikethrough(true)

	badgeSyncing = lipgloss.NewStyle().
			Foreground(colorBlue).
			Bold(true)

	selectedRowStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#313244")).
				Foreground(lipgloss.Color("#F5E0DC")).
				Bold(true)

	normalRowStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#CDD6F4"))

	borderBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorPrimary).
			Padding(0, 1)

	logBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorSecondary).
			Padding(0, 1)
)

// --- Messages for Bubble Tea Update Loop ---

type syncFinishedMsg struct{}

type orgSyncedMsg struct {
	repos []*git.RepoItem
	err   error
}

// --- Bubble Tea Model ---

type Model struct {
	TargetDir     string
	TargetOrg     string
	Repos         []*git.RepoItem
	SelectedIndex int
	IsSyncing     bool
	IsOrgSyncing  bool
	TotalCount    int

	Spinner     spinner.Model
	ProgressBar progress.Model
	Viewport    viewport.Model
	Width       int
	Height      int

	mu sync.Mutex
}

func NewModel(targetDir, targetOrg string) Model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(colorSecondary)

	p := progress.New(
		progress.WithDefaultGradient(),
		progress.WithoutPercentage(),
	)

	vp := viewport.New(60, 15)

	return Model{
		TargetDir:     targetDir,
		TargetOrg:     targetOrg,
		Repos:         make([]*git.RepoItem, 0),
		SelectedIndex: 0,
		IsOrgSyncing:  true,
		Spinner:       s,
		ProgressBar:   p,
		Viewport:      vp,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.Spinner.Tick,
		m.loadOrgReposCmd(),
	)
}

func (m Model) loadOrgReposCmd() tea.Cmd {
	return func() tea.Msg {
		orgRepos, _ := git.FetchOrgRepos(m.TargetOrg)

		// Discover existing local repos in target directory
		entries, err := git.ScanLocalDirectory(m.TargetDir)
		if err != nil {
			return orgSyncedMsg{repos: nil, err: err}
		}

		repoMap := make(map[string]*git.RepoItem)

		// Process org repos first
		for _, ghRepo := range orgRepos {
			localDir := git.GetLocalDirName(ghRepo.Name)
			localPath := fmt.Sprintf("%s/%s", m.TargetDir, localDir)

			item := &git.RepoItem{
				Name:       localDir,
				GHRepoName: ghRepo.Name,
				Path:       localPath,
				IsArchived: ghRepo.IsArchived,
				Status:     git.StatusPending,
				Logs:       make([]string, 0),
			}

			if ghRepo.IsArchived {
				item.Status = git.StatusArchived
				item.StatusMsg = "Archived (Press 'd' to delete)"
			}

			repoMap[localDir] = item
		}

		// Process local entries that might not be in org list
		for _, name := range entries {
			if _, exists := repoMap[name]; !exists {
				path := fmt.Sprintf("%s/%s", m.TargetDir, name)
				if git.IsGitRepo(path) {
					item := &git.RepoItem{
						Name:       name,
						GHRepoName: git.GetGHRepoName(name),
						Path:       path,
						Status:     git.StatusPending,
						Logs:       make([]string, 0),
					}
					repoMap[name] = item
				}
			}
		}

		result := make([]*git.RepoItem, 0, len(repoMap))
		for _, item := range repoMap {
			result = append(result, item)
		}

		return orgSyncedMsg{repos: result, err: nil}
	}
}

func (m Model) startParallelSyncCmd(forceFullSync bool) tea.Cmd {
	return func() tea.Msg {
		var wg sync.WaitGroup
		concurrency := 4
		sem := make(chan struct{}, concurrency)

		for _, item := range m.Repos {
			if item.IsArchived {
				continue
			}

			wg.Add(1)
			sem <- struct{}{}

			go func(r *git.RepoItem) {
				defer wg.Done()
				defer func() { <-sem }()

				// Branch-selective sync
				git.SyncRepository(r, forceFullSync)
			}(item)
		}

		wg.Wait()
		return syncFinishedMsg{}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case tea.KeyMsg:
		switch msg.String() {

		case "q", "ctrl+c":
			return m, tea.Quit

		case "up", "k":
			if m.SelectedIndex > 0 {
				m.SelectedIndex--
				m.updateViewport()
			}

		case "down", "j":
			if m.SelectedIndex < len(m.Repos)-1 {
				m.SelectedIndex++
				m.updateViewport()
			}

		case "r":
			// Re-sync selected repo
			if len(m.Repos) > 0 && m.SelectedIndex < len(m.Repos) {
				item := m.Repos[m.SelectedIndex]
				if !item.IsArchived {
					go git.SyncRepository(item, false)
					m.updateViewport()
				}
			}

		case "f":
			// Run Full Sync ("do the rest": stash, checkout default, pull, stash apply, draft PR) on selected repo
			if len(m.Repos) > 0 && m.SelectedIndex < len(m.Repos) {
				item := m.Repos[m.SelectedIndex]
				if !item.IsArchived {
					go git.SyncRepository(item, true)
					m.updateViewport()
				}
			}

		case "F":
			// Run Full Sync on ALL repositories
			if !m.IsSyncing && len(m.Repos) > 0 {
				m.IsSyncing = true
				return m, m.startParallelSyncCmd(true)
			}

		case "d":
			// Delete archived repo if selected
			if len(m.Repos) > 0 && m.SelectedIndex < len(m.Repos) {
				item := m.Repos[m.SelectedIndex]
				if item.IsArchived {
					_ = git.DeleteLocalRepo(item.Path)
					item.StatusMsg = "Deleted from disk"
					item.Logs = append(item.Logs, "Local folder deleted successfully.")
					m.updateViewport()
				}
			}
		}

	case orgSyncedMsg:
		m.IsOrgSyncing = false
		m.Repos = msg.repos
		m.TotalCount = len(m.Repos)
		if len(m.Repos) > 0 {
			m.IsSyncing = true
			m.updateViewport()
			cmds = append(cmds, m.startParallelSyncCmd(false))
		}

	case syncFinishedMsg:
		m.IsSyncing = false
		m.updateViewport()

	case tea.WindowSizeMsg:
		m.Width = msg.Width
		m.Height = msg.Height
		m.ProgressBar.Width = msg.Width - 20
		m.Viewport.Width = (msg.Width / 2) - 4
		m.Viewport.Height = msg.Height - 12
	}

	var spinnerCmd tea.Cmd
	m.Spinner, spinnerCmd = m.Spinner.Update(msg)
	cmds = append(cmds, spinnerCmd)

	return m, tea.Batch(cmds...)
}

func (m *Model) updateViewport() {
	if len(m.Repos) == 0 || m.SelectedIndex >= len(m.Repos) {
		return
	}

	item := m.Repos[m.SelectedIndex]
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("%s %s\n", lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Render("REPOSITORY:"), subtitleStyle.Render(item.Name)))
	sb.WriteString(fmt.Sprintf("%s %s\n", lipgloss.NewStyle().Bold(true).Foreground(colorMuted).Render("GITHUB NAME:"), item.GHRepoName))
	sb.WriteString(fmt.Sprintf("%s %s\n", lipgloss.NewStyle().Bold(true).Foreground(colorMuted).Render("LOCAL PATH: "), item.Path))
	sb.WriteString(fmt.Sprintf("%s %s (Default: %s)\n", lipgloss.NewStyle().Bold(true).Foreground(colorBlue).Render("BRANCH:     "), item.CurrentBranch, item.DefaultBranch))
	sb.WriteString(fmt.Sprintf("%s %s\n", lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render("STATUS:     "), item.StatusMsg))
	if item.DraftPRURL != "" {
		sb.WriteString(fmt.Sprintf("%s %s\n", lipgloss.NewStyle().Bold(true).Foreground(colorYellow).Render("DRAFT PR:   "), badgeStashedPR.Render(item.DraftPRURL)))
	}
	sb.WriteString("\n" + lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Render("------------------ EXECUTION LOGS ------------------") + "\n")

	for _, logLine := range item.Logs {
		sb.WriteString(highlightLogLine(logLine) + "\n")
	}

	m.Viewport.SetContent(sb.String())
}

// highlightLogLine applies syntax highlighting to git commands, timestamps, URLs, and status markers.
func highlightLogLine(line string) string {
	if line == "" {
		return line
	}

	// 1. Highlight timestamps [15:04:05]
	line = regexp.MustCompile(`\[\d{2}:\d{2}:\d{2}\]`).ReplaceAllStringFunc(line, func(m string) string {
		return lipgloss.NewStyle().Foreground(colorMuted).Render(m)
	})

	// 2. Highlight Git / GH commands
	cmdRegex := regexp.MustCompile(`\b(git pull|git push|git fetch|git rebase|git checkout|git stash|gh pr create|gh repo list)\b`)
	line = cmdRegex.ReplaceAllStringFunc(line, func(m string) string {
		return lipgloss.NewStyle().Foreground(colorSecondary).Bold(true).Render(m)
	})

	// 3. Highlight URLs (https://...)
	urlRegex := regexp.MustCompile(`https?://[^\s]+`)
	line = urlRegex.ReplaceAllStringFunc(line, func(m string) string {
		return lipgloss.NewStyle().Foreground(colorBlue).Underline(true).Render(m)
	})

	// 4. Highlight Quoted Strings / Branches ('main', 'master', etc.)
	quoteRegex := regexp.MustCompile(`'[^']+'`)
	line = quoteRegex.ReplaceAllStringFunc(line, func(m string) string {
		return lipgloss.NewStyle().Foreground(colorYellow).Bold(true).Render(m)
	})

	// 5. Highlight Success / Error prefix markers
	if strings.HasPrefix(line, "✓") || strings.Contains(line, "successfully") || strings.Contains(line, "Up to date") {
		line = lipgloss.NewStyle().Foreground(colorGreen).Render(line)
	} else if strings.HasPrefix(line, "✗") || strings.Contains(line, "error") || strings.Contains(line, "Failed") || strings.Contains(line, "conflict") {
		line = lipgloss.NewStyle().Foreground(colorRed).Render(line)
	}

	return line
}

func (m Model) View() string {
	if m.Width == 0 {
		return "Initializing freshen TUI..."
	}

	// 1. Header Banner
	header := titleStyle.Render(" FRESHEN ") + " " +
		subtitleStyle.Render("GitHub Repository Workflow & Sync Manager") + "\n" +
		lipgloss.NewStyle().Foreground(colorMuted).Render(fmt.Sprintf("Target Directory: %s | Org: %s", m.TargetDir, m.TargetOrg)) + "\n"

	// 2. Main Content Split View
	leftWidth := (m.Width / 2) - 2
	rightWidth := (m.Width / 2) - 2

	// Render Repo List (Left)
	var leftSb strings.Builder
	leftSb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render("REPOSITORIES") + "\n\n")

	if m.IsOrgSyncing {
		leftSb.WriteString(m.Spinner.View() + " Fetching GitHub organization repositories...\n")
	} else if len(m.Repos) == 0 {
		leftSb.WriteString("No repositories found in target directory.\n")
	} else {
		for i, item := range m.Repos {
			statusIcon := renderStatusBadge(item)
			line := fmt.Sprintf("%s %-22s %s", statusIcon, item.Name, item.StatusMsg)

			if i == m.SelectedIndex {
				leftSb.WriteString(selectedRowStyle.Width(leftWidth - 4).Render("> "+line) + "\n")
			} else {
				leftSb.WriteString(normalRowStyle.Render("  "+line) + "\n")
			}
		}
	}

	leftPane := borderBoxStyle.
		Width(leftWidth).
		Height(m.Height - 8).
		Render(leftSb.String())

	// Render Details Viewport (Right)
	rightPane := logBoxStyle.
		Width(rightWidth).
		Height(m.Height - 8).
		Render(m.Viewport.View())

	mainView := lipgloss.JoinHorizontal(lipgloss.Top, leftPane, " ", rightPane)

	// 3. Footer Keybindings Help
	footer := lipgloss.NewStyle().Foreground(colorMuted).Render(
		"[↑/↓ or j/k] Navigate  |  [r] Re-sync  |  [f] Full PR Sync  |  [d] Delete Archived  |  [q] Quit",
	)

	return header + "\n" + mainView + "\n" + footer
}

func renderStatusBadge(item *git.RepoItem) string {
	switch item.Status {
	case git.StatusUpToDate:
		return badgeUpToDate.Render("✓")
	case git.StatusUpdated:
		return badgeUpdated.Render("✓")
	case git.StatusRebased:
		return badgeRebased.Render("🔄")
	case git.StatusStashedPR:
		return badgeStashedPR.Render("🔗")
	case git.StatusError, git.StatusRebaseConflict:
		return badgeError.Render("✗")
	case git.StatusArchived:
		return badgeArchived.Render("🗑️")
	case git.StatusSyncing:
		return badgeSyncing.Render("⏳")
	default:
		return lipgloss.NewStyle().Foreground(colorMuted).Render("•")
	}
}
