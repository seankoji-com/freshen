package tui

import (
	"fmt"
	"os/exec"
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

// --- Active Right-Pane Detail View Tab ---
type TabType int

const (
	TabLogs TabType = iota
	TabBranches
	TabIssues
	TabPRs
)

// --- NerdFont Glyphs & Color Palette ---
var (
	iconLeaf     = "🍃"
	iconGithub   = "\uea84" //  GitHub Octocat NerdFont Icon
	iconFolder   = "󰉋"
	iconBranch   = ""
	iconPR       = "󰏫"
	iconIssue    = "⊙"
	iconSuccess  = "󰄬"
	iconError    = "󰅙"
	iconRebase   = "󰚰"
	iconStash    = "󰏖"
	iconSwitch   = "󰁨"
	iconTrash    = "🗑️"
	iconSyncing  = "⏳"
	iconPending  = "•"
	iconCopy     = "󰅍"
	iconWorktree = "󰉓"

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

	tabActiveStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(colorPrimary).
			Padding(0, 1)

	tabInactiveStyle = lipgloss.NewStyle().
				Foreground(colorMuted).
				Background(lipgloss.Color("#313244")).
				Padding(0, 1)

	badgeUpToDate = lipgloss.NewStyle().
			Foreground(colorGreen).
			Bold(true)

	badgeUpdated = lipgloss.NewStyle().
			Foreground(colorSecondary).
			Bold(true)

	badgeRebased = lipgloss.NewStyle().
			Foreground(colorBlue).
			Bold(true)

	badgeStash = lipgloss.NewStyle().
			Foreground(colorYellow).
			Bold(true)

	badgePR = lipgloss.NewStyle().
			Foreground(colorYellow).
			Bold(true)

	badgeIssue = lipgloss.NewStyle().
			Foreground(colorBlue).
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

type loadedIssuesMsg struct {
	repoName string
	issues   []git.IssueItem
}

type loadedPRsMsg struct {
	repoName string
	prs      []git.PRItem
}

// --- Bubble Tea Model ---

type Model struct {
	TargetDir     string
	TargetOrg     string
	Repos         []*git.RepoItem
	SelectedIndex int
	ActiveTab     TabType
	IsSyncing     bool
	IsOrgSyncing  bool
	TotalCount    int
	ToastMsg      string

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
		ActiveTab:     TabLogs,
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
		orgCounts, _ := git.FetchOrgRepoCounts(m.TargetOrg)

		entries, err := git.ScanLocalDirectory(m.TargetDir)
		if err != nil {
			return orgSyncedMsg{repos: nil, err: err}
		}

		repoMap := make(map[string]*git.RepoItem)

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

			if counts, found := orgCounts[ghRepo.Name]; found {
				item.OpenIssuesCount = counts.Issues
				item.OpenPRsCount = counts.PRs
			}

			if ghRepo.IsArchived {
				item.Status = git.StatusArchived
				item.StatusMsg = "Archived (Press 'd' to delete)"
			}

			repoMap[localDir] = item
		}

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

					if counts, found := orgCounts[item.GHRepoName]; found {
						item.OpenIssuesCount = counts.Issues
						item.OpenPRsCount = counts.PRs
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

func (m Model) fetchIssuesCmd(repoName, ghRepoName string) tea.Cmd {
	return func() tea.Msg {
		issues, _ := git.FetchOpenIssuesList(m.TargetOrg, ghRepoName)
		return loadedIssuesMsg{repoName: repoName, issues: issues}
	}
}

func (m Model) fetchPRsCmd(repoName, ghRepoName string) tea.Cmd {
	return func() tea.Msg {
		prs, _ := git.FetchOpenPRsList(m.TargetOrg, ghRepoName)
		return loadedPRsMsg{repoName: repoName, prs: prs}
	}
}

func (m Model) startParallelSyncCmd() tea.Cmd {
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

				git.SyncRepository(r)
			}(item)
		}

		wg.Wait()
		return syncFinishedMsg{}
	}
}

func copyToClipboard(text string) error {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case tea.KeyMsg:
		m.ToastMsg = ""
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

		case "right", "l", "tab":
			m.ActiveTab = (m.ActiveTab + 1) % 4
			m.updateViewport()
			return m, m.triggerTabFetch()

		case "left", "h", "shift+tab":
			m.ActiveTab = (m.ActiveTab + 3) % 4
			m.updateViewport()
			return m, m.triggerTabFetch()

		case "1":
			m.ActiveTab = TabLogs
			m.updateViewport()

		case "2":
			m.ActiveTab = TabBranches
			m.updateViewport()

		case "3":
			m.ActiveTab = TabIssues
			m.updateViewport()
			return m, m.triggerTabFetch()

		case "4":
			m.ActiveTab = TabPRs
			m.updateViewport()
			return m, m.triggerTabFetch()

		case "X":
			// Shortcut: Delete all local non-default branches and prune worktrees
			if len(m.Repos) > 0 && m.SelectedIndex < len(m.Repos) {
				item := m.Repos[m.SelectedIndex]
				count, err := git.PruneBranchesAndWorktrees(item.Path, item.DefaultBranch)
				if err == nil {
					item.Logs = append(item.Logs, fmt.Sprintf("󰄬 Pruned stale worktrees and deleted %d non-default local branches.", count))
					item.BranchDetails = git.GetRepoBranchDetails(item.Path, item.DefaultBranch)
					m.ToastMsg = fmt.Sprintf(" 󰄬 Deleted %d stale branches & pruned worktrees!", count)
					m.updateViewport()
				}
			}

		case "r":
			if len(m.Repos) > 0 && m.SelectedIndex < len(m.Repos) {
				item := m.Repos[m.SelectedIndex]
				if !item.IsArchived {
					go git.SyncRepository(item)
					m.updateViewport()
				}
			}

		case "c", "y":
			if len(m.Repos) > 0 && m.SelectedIndex < len(m.Repos) {
				item := m.Repos[m.SelectedIndex]
				targetCopy := item.DraftPRURL
				if targetCopy == "" {
					targetCopy = item.ExistingPRURL
				}
				if targetCopy == "" {
					targetCopy = item.Path
				}

				if err := copyToClipboard(targetCopy); err == nil {
					m.ToastMsg = fmt.Sprintf(" %s Copied to clipboard: %s", iconCopy, targetCopy)
				}
			}

		case "b":
			if len(m.Repos) > 0 && m.SelectedIndex < len(m.Repos) {
				item := m.Repos[m.SelectedIndex]
				target := item.OriginalBranch
				if item.CurrentBranch == item.OriginalBranch {
					target = item.DefaultBranch
				}
				_ = git.SwitchBranch(item, target)
				m.updateViewport()
			}

		case "p":
			if len(m.Repos) > 0 && m.SelectedIndex < len(m.Repos) {
				item := m.Repos[m.SelectedIndex]
				if !item.IsArchived {
					go func(r *git.RepoItem) {
						_ = git.CommitPushPRAndSwitchDefault(r)
					}(item)
					m.updateViewport()
				}
			}

		case "d":
			if len(m.Repos) > 0 && m.SelectedIndex < len(m.Repos) {
				item := m.Repos[m.SelectedIndex]
				if item.IsArchived {
					_ = git.DeleteLocalRepo(item.Path)
					item.StatusMsg = "Deleted from disk"
					item.Logs = append(item.Logs, "󰄬 Local folder deleted successfully.")
					m.updateViewport()
				}
			}
		}

	case loadedIssuesMsg:
		for _, item := range m.Repos {
			if item.Name == msg.repoName {
				item.IssuesList = msg.issues
				break
			}
		}
		m.updateViewport()

	case loadedPRsMsg:
		for _, item := range m.Repos {
			if item.Name == msg.repoName {
				item.PRsList = msg.prs
				break
			}
		}
		m.updateViewport()

	case tea.MouseMsg:
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
			if msg.X < m.Width/2 && msg.Y >= 4 {
				clickedIdx := msg.Y - 4
				if clickedIdx >= 0 && clickedIdx < len(m.Repos) {
					m.SelectedIndex = clickedIdx
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
			cmds = append(cmds, m.startParallelSyncCmd())
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

	var vpCmd tea.Cmd
	m.Viewport, vpCmd = m.Viewport.Update(msg)
	cmds = append(cmds, vpCmd)

	return m, tea.Batch(cmds...)
}

func (m Model) triggerTabFetch() tea.Cmd {
	if len(m.Repos) == 0 || m.SelectedIndex >= len(m.Repos) {
		return nil
	}
	item := m.Repos[m.SelectedIndex]
	if m.ActiveTab == TabIssues && len(item.IssuesList) == 0 {
		return m.fetchIssuesCmd(item.Name, item.GHRepoName)
	}
	if m.ActiveTab == TabPRs && len(item.PRsList) == 0 {
		return m.fetchPRsCmd(item.Name, item.GHRepoName)
	}
	return nil
}

func (m *Model) updateViewport() {
	if len(m.Repos) == 0 || m.SelectedIndex >= len(m.Repos) {
		return
	}

	item := m.Repos[m.SelectedIndex]
	var sb strings.Builder

	// Header metadata
	sb.WriteString(fmt.Sprintf("%s %s\n", lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Render("REPOSITORY: "), subtitleStyle.Render(item.Name)))
	sb.WriteString(fmt.Sprintf("%s %s %s | %s %s (%s)\n",
		lipgloss.NewStyle().Bold(true).Foreground(colorMuted).Render("LOCAL:"), iconFolder, item.Path,
		iconBranch, item.CurrentBranch, item.DefaultBranch,
	))
	sb.WriteString(fmt.Sprintf("%s %s  |  %s %d open  |  %s %d open\n\n",
		lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render("STATUS:"), item.StatusMsg,
		iconPR, item.OpenPRsCount, iconIssue, item.OpenIssuesCount,
	))

	// Tab Bar Header
	sb.WriteString(m.renderTabBar() + "\n\n")

	// Render Content Based on Selected Tab
	switch m.ActiveTab {

	case TabLogs:
		if item.ExistingPRURL != "" {
			sb.WriteString(fmt.Sprintf("%s %s %s\n", lipgloss.NewStyle().Bold(true).Foreground(colorYellow).Render("OPEN PR: "), iconPR, badgePR.Render(item.ExistingPRURL)))
		}
		if item.DraftPRURL != "" && item.DraftPRURL != item.ExistingPRURL {
			sb.WriteString(fmt.Sprintf("%s %s %s\n", lipgloss.NewStyle().Bold(true).Foreground(colorYellow).Render("DRAFT PR:"), iconPR, badgePR.Render(item.DraftPRURL)))
		}
		sb.WriteString("\n" + lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Render("------------------ EXECUTION LOGS ------------------") + "\n")
		for _, logLine := range item.Logs {
			sb.WriteString(highlightLogLine(logLine) + "\n")
		}

	case TabBranches:
		sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Render("󰓦 BRANCHES & WORKTREES") + "\n")
		sb.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render("(Press 'X' to delete all non-default branches & prune worktrees)") + "\n\n")

		sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorBlue).Render(" Local & Remote Branches:") + "\n")
		if len(item.BranchDetails.Branches) == 0 {
			sb.WriteString("  (None found)\n")
		} else {
			for _, b := range item.BranchDetails.Branches {
				sb.WriteString(fmt.Sprintf("  %s %s\n", iconBranch, b))
			}
		}

		sb.WriteString("\n" + lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render("󰉓 Git Worktrees:") + "\n")
		if len(item.BranchDetails.Worktrees) == 0 {
			sb.WriteString("  (No worktrees found)\n")
		} else {
			for _, w := range item.BranchDetails.Worktrees {
				sb.WriteString(fmt.Sprintf("  %s %s\n", iconWorktree, w))
			}
		}

		sb.WriteString("\n" + lipgloss.NewStyle().Bold(true).Foreground(colorYellow).Render("󰈔 Changed Files (Branch Diff Status):") + "\n")
		if len(item.BranchDetails.ChangedFiles) == 0 {
			sb.WriteString("  󰄬 Working tree is clean.\n")
		} else {
			for _, f := range item.BranchDetails.ChangedFiles {
				sb.WriteString(fmt.Sprintf("  %s\n", f))
			}
		}

	case TabIssues:
		sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Render(fmt.Sprintf("⊙ OPEN ISSUES (%d)", item.OpenIssuesCount)) + "\n\n")
		if len(item.IssuesList) == 0 {
			if item.OpenIssuesCount == 0 {
				sb.WriteString("  󰄬 No open issues found for this repository.\n")
			} else {
				sb.WriteString("  ⏳ Loading open issues from GitHub...\n")
			}
		} else {
			for _, issue := range item.IssuesList {
				sb.WriteString(fmt.Sprintf("  %s #%-4d %s\n     %s\n\n",
					badgeIssue.Render("⊙"), issue.Number, issue.Title,
					lipgloss.NewStyle().Foreground(colorBlue).Underline(true).Render(issue.URL),
				))
			}
		}

	case TabPRs:
		sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Render(fmt.Sprintf("󰏫 OPEN PULL REQUESTS (%d)", item.OpenPRsCount)) + "\n\n")
		if len(item.PRsList) == 0 {
			if item.OpenPRsCount == 0 {
				sb.WriteString("  󰄬 No open pull requests found for this repository.\n")
			} else {
				sb.WriteString("  ⏳ Loading open pull requests from GitHub...\n")
			}
		} else {
			for _, pr := range item.PRsList {
				sb.WriteString(fmt.Sprintf("  %s #%-4d %s (%s %s)\n     %s\n\n",
					badgePR.Render("󰏫"), pr.Number, pr.Title, iconBranch, pr.HeadRefName,
					lipgloss.NewStyle().Foreground(colorBlue).Underline(true).Render(pr.URL),
				))
			}
		}
	}

	m.Viewport.SetContent(sb.String())
}

func (m Model) renderTabBar() string {
	t1 := " [1 Logs] "
	t2 := " [2 Branches & Worktrees] "
	t3 := " [3 Issues] "
	t4 := " [4 PRs] "

	switch m.ActiveTab {
	case TabLogs:
		return tabActiveStyle.Render(t1) + tabInactiveStyle.Render(t2) + tabInactiveStyle.Render(t3) + tabInactiveStyle.Render(t4)
	case TabBranches:
		return tabInactiveStyle.Render(t1) + tabActiveStyle.Render(t2) + tabInactiveStyle.Render(t3) + tabInactiveStyle.Render(t4)
	case TabIssues:
		return tabInactiveStyle.Render(t1) + tabInactiveStyle.Render(t2) + tabActiveStyle.Render(t3) + tabInactiveStyle.Render(t4)
	case TabPRs:
		return tabInactiveStyle.Render(t1) + tabInactiveStyle.Render(t2) + tabInactiveStyle.Render(t3) + tabActiveStyle.Render(t4)
	}
	return ""
}

// highlightLogLine applies NerdFont styling & syntax highlighting to log text.
func highlightLogLine(line string) string {
	if line == "" {
		return line
	}

	line = regexp.MustCompile(`\[\d{2}:\d{2}:\d{2}\]`).ReplaceAllStringFunc(line, func(m string) string {
		return lipgloss.NewStyle().Foreground(colorMuted).Render(m)
	})

	cmdRegex := regexp.MustCompile(`\b(git pull|git push|git fetch|git rebase|git checkout|git stash|git add|gh pr create|gh pr list|gh repo list)\b`)
	line = cmdRegex.ReplaceAllStringFunc(line, func(m string) string {
		return lipgloss.NewStyle().Foreground(colorSecondary).Bold(true).Render(m)
	})

	urlRegex := regexp.MustCompile(`https?://[^\s]+`)
	line = urlRegex.ReplaceAllStringFunc(line, func(m string) string {
		return lipgloss.NewStyle().Foreground(colorBlue).Underline(true).Render(m)
	})

	quoteRegex := regexp.MustCompile(`'[^']+'`)
	line = quoteRegex.ReplaceAllStringFunc(line, func(m string) string {
		return lipgloss.NewStyle().Foreground(colorYellow).Bold(true).Render(m)
	})

	if strings.Contains(line, "󰄬") || strings.Contains(line, "successfully") || strings.Contains(line, "Up to date") {
		line = lipgloss.NewStyle().Foreground(colorGreen).Render(line)
	} else if strings.Contains(line, "󰅙") || strings.Contains(line, "error") || strings.Contains(line, "Failed") || strings.Contains(line, "conflict") {
		line = lipgloss.NewStyle().Foreground(colorRed).Render(line)
	}

	return line
}

func (m Model) View() string {
	if m.Width == 0 {
		return "Initializing freshen TUI..."
	}

	// 1. Header Banner with NerdFont icons
	header := titleStyle.Render(fmt.Sprintf(" %s FRESHEN ", iconLeaf)) + " " +
		subtitleStyle.Render("GitHub Repository Workflow & Sync Manager") + "\n" +
		lipgloss.NewStyle().Foreground(colorMuted).Render(fmt.Sprintf("%s Target Directory: %s | %s Org: %s", iconFolder, m.TargetDir, iconGithub, m.TargetOrg)) + "\n"

	// 2. Main Content Split View
	leftWidth := (m.Width / 2) - 2
	rightWidth := (m.Width / 2) - 2

	// Render Repo Grid Table (Left)
	var leftSb strings.Builder
	
	// Table Column Headers
	tableHeader := fmt.Sprintf("%-18s %-16s %-12s %-5s %-5s", "REPOSITORY", "STATUS", "BRANCH", "PRs", "ISSUES")
	leftSb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render(tableHeader) + "\n")
	leftSb.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render("------------------------------------------------------------------") + "\n")

	if m.IsOrgSyncing {
		leftSb.WriteString(m.Spinner.View() + " Fetching GitHub organization repositories...\n")
	} else if len(m.Repos) == 0 {
		leftSb.WriteString("No repositories found in target directory.\n")
	} else {
		for i, item := range m.Repos {
			statusIcon := renderStatusBadge(item)

			nameCell := truncateString(item.Name, 18)
			statusCell := truncateString(item.StatusMsg, 16)
			branchCell := truncateString(item.CurrentBranch, 12)
			
			prsCell := "-"
			if item.OpenPRsCount > 0 {
				prsCell = fmt.Sprintf("%d", item.OpenPRsCount)
			}

			issuesCell := "-"
			if item.OpenIssuesCount > 0 {
				issuesCell = fmt.Sprintf("%d", item.OpenIssuesCount)
			}

			line := fmt.Sprintf("%s %-16s %-16s %-12s %-5s %-5s",
				statusIcon, nameCell, statusCell, branchCell, prsCell, issuesCell,
			)

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

	// 3. Footer Keybindings Help with NerdFont Glyphs
	footerText := "[↑/↓/j/k] Move  |  [←/→/h/l] Switch Tab  |  [X] Prune Branches  |  [b] Switch Branch  |  [p] Push/PR  |  [q] Quit"
	if m.ToastMsg != "" {
		footerText = lipgloss.NewStyle().Foreground(colorGreen).Bold(true).Render(m.ToastMsg)
	}

	footer := lipgloss.NewStyle().Foreground(colorMuted).Render(footerText)

	return header + "\n" + mainView + "\n" + footer
}

func renderStatusBadge(item *git.RepoItem) string {
	switch item.Status {
	case git.StatusUpToDate:
		return badgeUpToDate.Render(iconSuccess)
	case git.StatusUpdated:
		return badgeUpdated.Render(iconSuccess)
	case git.StatusStashedApplied:
		return badgeStash.Render(iconStash)
	case git.StatusSwitchedDefault:
		return badgeUpToDate.Render(iconSwitch)
	case git.StatusRebased:
		return badgeRebased.Render(iconRebase)
	case git.StatusPRCreated:
		return badgePR.Render(iconPR)
	case git.StatusError, git.StatusRebaseConflict:
		return badgeError.Render(iconError)
	case git.StatusArchived:
		return badgeArchived.Render(iconTrash)
	case git.StatusSyncing:
		return badgeSyncing.Render(iconSyncing)
	default:
		return lipgloss.NewStyle().Foreground(colorMuted).Render(iconPending)
	}
}

func truncateString(str string, maxLen int) string {
	if len(str) <= maxLen {
		return str
	}
	if maxLen <= 3 {
		return str[:maxLen]
	}
	return str[:maxLen-3] + "..."
}
