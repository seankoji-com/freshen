package tui

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/seankoji-com/freshen/pkg/git"
	"github.com/seankoji-com/freshen/pkg/jobs"
)

// --- Active Right-Pane Detail View Tab ---
type TabType int

const (
	TabLogs TabType = iota
	TabBranches
	TabIssues
	TabPRs
)

// --- Left-Pane Focus Panel ---
type FocusType int

const (
	FocusRepos FocusType = iota
	FocusRunners
	FocusJobs
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
	iconPending  = "•"
	iconCopy     = "󰅍"
	iconWorktree = "󰉓"
	iconRunner   = "🏃"
	iconQueue    = "📋"
	iconCpu      = "⚡"

	colorPrimary   = lipgloss.Color("#7D56F4") // Electric Purple
	colorSecondary = lipgloss.Color("#00F5D4") // Bright Mint / Cyan
	colorGreen     = lipgloss.Color("#10B981") // Emerald Green
	colorYellow    = lipgloss.Color("#F59E0B") // Warm Amber
	colorRed       = lipgloss.Color("#EF4444") // Coral Red
	colorBlue      = lipgloss.Color("#3B82F6") // Bright Blue
	colorMuted     = lipgloss.Color("#6C7086") // Muted Slate Grey

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

	borderFocusedStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorSecondary).
				Padding(0, 1)

	logBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorSecondary).
			Padding(0, 1)

	// Explicit Column Width Styles for Table Alignment
	// Total: 2 + 20 + 16 + 4 + 6 + spaces = ~51 chars, fits in a ~55 char inner pane
	cellStatusIconStyle = lipgloss.NewStyle().Width(2)
	cellNameStyle       = lipgloss.NewStyle().Width(20)
	cellBranchStyle     = lipgloss.NewStyle().Width(16)
	cellPRsStyle        = lipgloss.NewStyle().Width(4).Align(lipgloss.Right)
	cellIssuesStyle     = lipgloss.NewStyle().Width(6).Align(lipgloss.Right)
)

// Hyperlink formats text as an OSC 8 terminal hyperlink.
func Hyperlink(text, url string) string {
	if url == "" {
		return text
	}
	return fmt.Sprintf("\x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\", url, text)
}

// --- Messages for Bubble Tea Update Loop ---

type syncFinishedMsg struct{}

type repoTickMsg time.Time

type runnerJobTickMsg time.Time

func repoTickCmd() tea.Cmd {
	return tea.Every(5*time.Minute, func(t time.Time) tea.Msg {
		return repoTickMsg(t)
	})
}

func runnerJobTickCmd() tea.Cmd {
	return tea.Every(10*time.Second, func(t time.Time) tea.Msg {
		return runnerJobTickMsg(t)
	})
}

type loadedRunnersMsg struct {
	runners []*jobs.RunnerItem
	err     error
}

type orgSyncedMsg struct {
	repos []*git.RepoItem
	err   error
}

type loadedIssuesMsg struct {
	repoName string
	issues   []git.IssueItem
	err      error
}

type loadedPRsMsg struct {
	repoName string
	prs      []git.PRItem
	err      error
}

// --- Bubble Tea Model ---

type Model struct {
	TargetDir           string
	TargetOrg           string
	Repos               []*git.RepoItem
	Runners             []*jobs.RunnerItem
	JobQueue            []*jobs.JobItem
	SelectedIndex       int
	SelectedRunnerIndex int
	SelectedJobIndex    int
	ActiveFocus         FocusType
	ActiveTab           TabType
	IsSyncing           bool
	IsOrgSyncing        bool
	TotalCount          int
	ToastMsg            string

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
		TargetDir:           targetDir,
		TargetOrg:           targetOrg,
		Repos:               make([]*git.RepoItem, 0),
		Runners:             make([]*jobs.RunnerItem, 0),
		JobQueue:            make([]*jobs.JobItem, 0),
		SelectedIndex:       0,
		SelectedRunnerIndex: 0,
		SelectedJobIndex:    0,
		ActiveFocus:         FocusRepos,
		ActiveTab:           TabLogs,
		IsOrgSyncing:        true,
		Spinner:             s,
		ProgressBar:         p,
		Viewport:            vp,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.Spinner.Tick,
		m.loadOrgReposCmd(),
		m.loadRunnersCmd(),
		m.loadJobQueueCmd(),
		repoTickCmd(),
		runnerJobTickCmd(),
	)
}

func (m Model) loadRunnersCmd() tea.Cmd {
	return func() tea.Msg {
		runners, err := jobs.FetchOrgRunners(m.TargetOrg, m.Runners, m.JobQueue)
		return loadedRunnersMsg{runners: runners, err: err}
	}
}

type loadedJobQueueMsg struct {
	queue []*jobs.JobItem
	err   error
}

func (m Model) loadJobQueueCmd() tea.Cmd {
	// Collect repo names from loaded repos
	repos := make([]string, 0, len(m.Repos))
	for _, r := range m.Repos {
		repoName := r.GHRepoName
		if repoName == "" {
			repoName = r.Name
		}
		if !r.IsArchived && repoName != "" {
			repos = append(repos, repoName)
		}
	}
	// If repos not loaded yet, try fetching from org API
	if len(repos) == 0 {
		orgRepos, err := git.FetchOrgRepos(m.TargetOrg)
		if err == nil {
			for _, r := range orgRepos {
				if !r.IsArchived {
					repos = append(repos, r.Name)
				}
			}
		}
	}
	return func() tea.Msg {
		queue, err := jobs.FetchOrgJobQueue(m.TargetOrg, repos)
		return loadedJobQueueMsg{queue: queue, err: err}
	}
}

type loadedJobLogsMsg struct {
	jobID   string // JobItem.ID to match
	ghJobID int64
	logs    []string
	err     error
}

// loadJobLogsCmd fetches real log lines for the given running job.
func (m Model) loadJobLogsCmd(job *jobs.JobItem) tea.Cmd {
	if job == nil || job.RunID == 0 {
		return nil
	}
	org := m.TargetOrg
	repo := job.Repo
	runID := job.RunID
	jobID := job.ID
	return func() tea.Msg {
		lines, ghJobID, err := jobs.FetchJobLogs(org, repo, runID, 200)
		return loadedJobLogsMsg{jobID: jobID, ghJobID: ghJobID, logs: lines, err: err}
	}
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

			localExists := false
			if stat, err := os.Stat(localPath); err == nil && stat.IsDir() {
				localExists = true
			}

			if ghRepo.IsArchived && !localExists {
				continue
			}

			item := &git.RepoItem{
				Name:       localDir,
				GHRepoName: ghRepo.Name,
				Path:       localPath,
				URL:        ghRepo.URL,
				IsArchived: ghRepo.IsArchived,
				Status:     git.StatusPending,
				Logs:       make([]string, 0),
			}

			if git.IsGitRepo(localPath) {
				item.CurrentBranch = git.GetOriginalBranch(localPath)
				item.DefaultBranch = git.GetDefaultBranch(localPath)
			}

			if counts, found := orgCounts[ghRepo.Name]; found {
				item.OpenIssuesCount = counts.Issues
				item.OpenPRsCount = counts.PRs
			}

			if ghRepo.IsArchived {
				item.Status = git.StatusArchived
				item.StatusMsg = "Archived"
			}

			repoMap[localDir] = item
		}

		for _, name := range entries {
			if _, exists := repoMap[name]; !exists {
				path := fmt.Sprintf("%s/%s", m.TargetDir, name)
				if git.IsGitRepo(path) {
					item := &git.RepoItem{
						Name:          name,
						GHRepoName:    git.GetGHRepoName(name),
						Path:          path,
						URL:           fmt.Sprintf("https://github.com/%s/%s", m.TargetOrg, git.GetGHRepoName(name)),
						CurrentBranch: git.GetOriginalBranch(path),
						DefaultBranch: git.GetDefaultBranch(path),
						Status:        git.StatusPending,
						Logs:          make([]string, 0),
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

		sort.Slice(result, func(i, j int) bool {
			return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
		})

		return orgSyncedMsg{repos: result, err: nil}
	}
}

func (m Model) fetchIssuesCmd(repoName, ghRepoName string) tea.Cmd {
	return func() tea.Msg {
		issues, err := git.FetchOpenIssuesList(m.TargetOrg, ghRepoName)
		return loadedIssuesMsg{repoName: repoName, issues: issues, err: err}
	}
}

func (m Model) fetchPRsCmd(repoName, ghRepoName string) tea.Cmd {
	return func() tea.Msg {
		prs, err := git.FetchOpenPRsList(m.TargetOrg, ghRepoName)
		return loadedPRsMsg{repoName: repoName, prs: prs, err: err}
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

		case "w", "W":
			// Cycle active panel focus: Repos -> Runners -> Jobs
			m.ActiveFocus = (m.ActiveFocus + 1) % 3
			m.updateViewport()

		case "up":
			switch m.ActiveFocus {
			case FocusRepos:
				if m.SelectedIndex > 0 {
					m.SelectedIndex--
					m.updateViewport()
				}
			case FocusRunners:
				if m.SelectedRunnerIndex > 0 {
					m.SelectedRunnerIndex--
					m.updateViewport()
				} else if len(m.Repos) > 0 {
					m.ActiveFocus = FocusRepos
					m.SelectedIndex = len(m.Repos) - 1
					m.updateViewport()
				}
			case FocusJobs:
				if m.SelectedJobIndex > 0 {
					m.SelectedJobIndex--
					m.updateViewport()
					if m.SelectedJobIndex < len(m.JobQueue) {
						j := m.JobQueue[m.SelectedJobIndex]
						if j.Status == jobs.JobRunning {
							cmds = append(cmds, m.loadJobLogsCmd(j))
						}
					}
				} else if len(m.Runners) > 0 {
					m.ActiveFocus = FocusRunners
					m.SelectedRunnerIndex = len(m.Runners) - 1
					m.updateViewport()
				}
			}

		case "down":
			switch m.ActiveFocus {
			case FocusRepos:
				if m.SelectedIndex < len(m.Repos)-1 {
					m.SelectedIndex++
					m.updateViewport()
				} else if len(m.Runners) > 0 {
					m.ActiveFocus = FocusRunners
					m.SelectedRunnerIndex = 0
					m.updateViewport()
				}
			case FocusRunners:
				if m.SelectedRunnerIndex < len(m.Runners)-1 {
					m.SelectedRunnerIndex++
					m.updateViewport()
				} else {
					// Always fall through to jobs panel from last runner
					m.ActiveFocus = FocusJobs
					m.SelectedJobIndex = 0
					m.updateViewport()
					// Immediately kick off log fetch for the newly focused job
					if len(m.JobQueue) > 0 {
						j := m.JobQueue[0]
						if j.Status == jobs.JobRunning {
							cmds = append(cmds, m.loadJobLogsCmd(j))
						}
					}
				}
			case FocusJobs:
				if m.SelectedJobIndex < len(m.JobQueue)-1 {
					m.SelectedJobIndex++
					m.updateViewport()
					if m.SelectedJobIndex < len(m.JobQueue) {
						j := m.JobQueue[m.SelectedJobIndex]
						if j.Status == jobs.JobRunning {
							cmds = append(cmds, m.loadJobLogsCmd(j))
						}
					}
				}
			}

		case "j", "ctrl+d", "pgdown":
			m.Viewport.LineDown(3)

		case "k", "ctrl+u", "pgup":
			m.Viewport.LineUp(3)

		case "right", "l", "tab":
			if m.ActiveFocus == FocusRepos {
				m.ActiveTab = (m.ActiveTab + 1) % 4
				m.updateViewport()
				return m, m.triggerTabFetch()
			} else {
				m.ActiveFocus = (m.ActiveFocus + 1) % 3
				m.updateViewport()
			}

		case "left", "h", "shift+tab":
			if m.ActiveFocus == FocusRepos {
				m.ActiveTab = (m.ActiveTab + 3) % 4
				m.updateViewport()
				return m, m.triggerTabFetch()
			} else {
				m.ActiveFocus = (m.ActiveFocus + 2) % 3
				m.updateViewport()
			}

		case "1":
			m.ActiveFocus = FocusRepos
			m.ActiveTab = TabLogs
			m.updateViewport()

		case "2":
			m.ActiveFocus = FocusRunners
			m.updateViewport()

		case "3":
			m.ActiveFocus = FocusJobs
			m.updateViewport()

		case "4":
			if m.ActiveFocus == FocusRepos {
				m.ActiveTab = TabPRs
				m.updateViewport()
				return m, m.triggerTabFetch()
			}

		case "X":
			if m.ActiveFocus == FocusRepos && len(m.Repos) > 0 && m.SelectedIndex < len(m.Repos) {
				item := m.Repos[m.SelectedIndex]
				count, err := git.PruneBranchesAndWorktrees(item.Path, item.DefaultBranch)
				if err == nil {
					item.CurrentBranch = git.GetOriginalBranch(item.Path)
					item.BranchDetails = git.GetRepoBranchDetails(item.Path, item.DefaultBranch)
					item.Logs = append(item.Logs, fmt.Sprintf("󰄬 Pruned remote tracking branches (git fetch --prune), force removed worktrees, and deleted %d non-default local branches.", count))
					m.ToastMsg = fmt.Sprintf(" 󰄬 Fetched & pruned remote refs, removed worktrees & deleted %d branches!", count)
					m.updateViewport()
				}
			}

		case "r":
			if m.ActiveFocus == FocusRepos && len(m.Repos) > 0 && m.SelectedIndex < len(m.Repos) {
				item := m.Repos[m.SelectedIndex]
				if !item.IsArchived {
					go git.SyncRepository(item)
					m.updateViewport()
				}
			}

		case "c", "y":
			if m.ActiveFocus == FocusRepos && len(m.Repos) > 0 && m.SelectedIndex < len(m.Repos) {
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
			} else if m.ActiveFocus == FocusRunners && len(m.Runners) > 0 && m.SelectedRunnerIndex < len(m.Runners) {
				r := m.Runners[m.SelectedRunnerIndex]
				if err := copyToClipboard(r.ID); err == nil {
					m.ToastMsg = fmt.Sprintf(" %s Copied Runner ID to clipboard: %s", iconCopy, r.ID)
				}
			} else if m.ActiveFocus == FocusJobs && len(m.JobQueue) > 0 && m.SelectedJobIndex < len(m.JobQueue) {
				j := m.JobQueue[m.SelectedJobIndex]
				if err := copyToClipboard(j.ID); err == nil {
					m.ToastMsg = fmt.Sprintf(" %s Copied Job ID to clipboard: %s", iconCopy, j.ID)
				}
			}

		case "b":
			if m.ActiveFocus == FocusRepos && len(m.Repos) > 0 && m.SelectedIndex < len(m.Repos) {
				item := m.Repos[m.SelectedIndex]
				target := item.OriginalBranch
				if item.CurrentBranch == item.OriginalBranch {
					target = item.DefaultBranch
				}
				if err := git.SwitchBranch(item, target); err == nil {
					item.CurrentBranch = git.GetOriginalBranch(item.Path)
					item.BranchDetails = git.GetRepoBranchDetails(item.Path, item.DefaultBranch)
					m.updateViewport()
				}
			}

		case "p":
			if m.ActiveFocus == FocusRepos && len(m.Repos) > 0 && m.SelectedIndex < len(m.Repos) {
				item := m.Repos[m.SelectedIndex]
				if !item.IsArchived {
					go func(r *git.RepoItem) {
						if err := git.CommitPushPRAndSwitchDefault(r); err == nil {
							r.CurrentBranch = git.GetOriginalBranch(r.Path)
							r.BranchDetails = git.GetRepoBranchDetails(r.Path, r.DefaultBranch)
						}
					}(item)
					m.updateViewport()
				}
			}

		case "d":
			if m.ActiveFocus == FocusRepos && len(m.Repos) > 0 && m.SelectedIndex < len(m.Repos) {
				item := m.Repos[m.SelectedIndex]
				if item.IsArchived {
					_ = git.DeleteLocalRepo(item.Path)
					deletedName := item.Name

					m.Repos = append(m.Repos[:m.SelectedIndex], m.Repos[m.SelectedIndex+1:]...)
					m.TotalCount = len(m.Repos)

					if m.SelectedIndex >= len(m.Repos) && len(m.Repos) > 0 {
						m.SelectedIndex = len(m.Repos) - 1
					}

					m.ToastMsg = fmt.Sprintf(" 🗑️ Deleted archived repo '%s' from disk.", deletedName)
					m.updateViewport()
				}
			}
		}

	case loadedIssuesMsg:
		for _, item := range m.Repos {
			if item.Name == msg.repoName {
				item.IsLoadingIssues = false
				item.HasLoadedIssues = true
				if msg.err == nil && msg.issues != nil {
					item.IssuesList = msg.issues
				}
				break
			}
		}
		m.updateViewport()

	case loadedPRsMsg:
		for _, item := range m.Repos {
			if item.Name == msg.repoName {
				item.IsLoadingPRs = false
				item.HasLoadedPRs = true
				if msg.err == nil && msg.prs != nil {
					item.PRsList = msg.prs
				}
				break
			}
		}
		m.updateViewport()

	case tea.MouseMsg:
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
			// Click in Left Column Panes
			if msg.X < m.Width/2 {
				repoPaneEnd := 4 + len(m.Repos) + 2
				runnersPaneEnd := repoPaneEnd + 3 + len(m.Runners)

				if msg.Y >= 2 && msg.Y <= repoPaneEnd {
					m.ActiveFocus = FocusRepos
					clickedIdx := msg.Y - 4
					if clickedIdx >= 0 && clickedIdx < len(m.Repos) {
						m.SelectedIndex = clickedIdx
					}
					m.updateViewport()
				} else if msg.Y > repoPaneEnd && msg.Y <= runnersPaneEnd {
					m.ActiveFocus = FocusRunners
					clickedIdx := msg.Y - repoPaneEnd - 2
					if clickedIdx >= 0 && clickedIdx < len(m.Runners) {
						m.SelectedRunnerIndex = clickedIdx
					}
					m.updateViewport()
				} else if msg.Y > runnersPaneEnd {
					m.ActiveFocus = FocusJobs
					clickedIdx := msg.Y - runnersPaneEnd - 2
					if clickedIdx >= 0 && clickedIdx < len(m.JobQueue) {
						m.SelectedJobIndex = clickedIdx
					}
					m.updateViewport()
				}
			}
			// Click in Right Detail View Pane
			if msg.X >= m.Width/2 && (msg.Y == 4 || msg.Y == 5) && m.ActiveFocus == FocusRepos {
				relX := msg.X - (m.Width / 2)
				if relX >= 0 && relX < 10 {
					m.ActiveTab = TabLogs
					m.updateViewport()
				} else if relX >= 10 && relX < 35 {
					m.ActiveTab = TabBranches
					m.updateViewport()
				} else if relX >= 35 && relX < 47 {
					m.ActiveTab = TabIssues
					m.updateViewport()
					cmds = append(cmds, m.triggerTabFetch())
				} else if relX >= 47 {
					m.ActiveTab = TabPRs
					m.updateViewport()
					cmds = append(cmds, m.triggerTabFetch())
				}
			}
		}

	case repoTickMsg:
		return m, tea.Batch(
			m.loadOrgReposCmd(),
			repoTickCmd(),
		)

	case runnerJobTickMsg:
		jobs.PollStep(m.Runners, m.JobQueue)
		m.updateViewport()
		var cmdsToAdd []tea.Cmd
		cmdsToAdd = append(cmdsToAdd, m.loadRunnersCmd(), m.loadJobQueueCmd(), runnerJobTickCmd())
		// Refresh logs for selected running job
		if m.ActiveFocus == FocusJobs && len(m.JobQueue) > 0 && m.SelectedJobIndex < len(m.JobQueue) {
			selJob := m.JobQueue[m.SelectedJobIndex]
			if selJob.Status == jobs.JobRunning {
				cmdsToAdd = append(cmdsToAdd, m.loadJobLogsCmd(selJob))
			}
		}
		return m, tea.Batch(cmdsToAdd...)

	case loadedRunnersMsg:
		if msg.err == nil && len(msg.runners) > 0 {
			m.Runners = msg.runners
			m.updateViewport()
		}

	case loadedJobQueueMsg:
		if msg.err == nil {
			// Compare with previous queue to trigger toast notifications
			if len(m.JobQueue) > 0 {
				oldJobs := make(map[string]*jobs.JobItem)
				for _, j := range m.JobQueue {
					oldJobs[j.ID] = j
				}

				newJobsMap := make(map[string]*jobs.JobItem)
				for _, j := range msg.queue {
					newJobsMap[j.ID] = j
				}

				// Check for status changes or new jobs
				for _, newJ := range msg.queue {
					if oldJ, ok := oldJobs[newJ.ID]; ok {
						if oldJ.Status != newJ.Status {
							if newJ.Status == jobs.JobRunning {
								runnerStr := newJ.RunnerName
								if runnerStr == "" {
									runnerStr = "worker"
								}
								m.ToastMsg = fmt.Sprintf(" ⚡ Job %s started running on %s", newJ.ID, runnerStr)
							} else if newJ.Status == jobs.JobPassed {
								m.ToastMsg = fmt.Sprintf(" 󰄬 Job %s passed (%s)", newJ.ID, newJ.Name)
							} else if newJ.Status == jobs.JobFailed {
								m.ToastMsg = fmt.Sprintf(" 󰅙 Job %s failed (%s)", newJ.ID, newJ.Name)
							}
						}
					} else {
						// Brand new job detected during polling
						if newJ.Status == jobs.JobRunning {
							runnerStr := newJ.RunnerName
							if runnerStr == "" {
								runnerStr = "worker"
							}
							m.ToastMsg = fmt.Sprintf(" ⚡ Job %s started running on %s", newJ.ID, runnerStr)
						} else if newJ.Status == jobs.JobQueued {
							m.ToastMsg = fmt.Sprintf(" ⏳ Job %s queued (%s)", newJ.ID, newJ.Name)
						}
					}
				}

				// Check for jobs that finished and left the queue
				for _, oldJ := range m.JobQueue {
					if _, stillThere := newJobsMap[oldJ.ID]; !stillThere {
						if oldJ.Status == jobs.JobRunning {
							m.ToastMsg = fmt.Sprintf(" 󰄬 Job %s completed (%s)", oldJ.ID, oldJ.Name)
						}
					}
				}
			}

			// Preserve existing logs on jobs we already have
			existingLogs := make(map[string][]string)
			existingGHJobID := make(map[string]int64)
			for _, j := range m.JobQueue {
				if len(j.Logs) > 0 {
					existingLogs[j.ID] = j.Logs
					existingGHJobID[j.ID] = j.GHJobID
				}
			}
			for _, j := range msg.queue {
				if logs, ok := existingLogs[j.ID]; ok {
					j.Logs = logs
					j.GHJobID = existingGHJobID[j.ID]
				}
			}
			m.JobQueue = msg.queue
			// Kick off log fetch for selected running job
			if m.ActiveFocus == FocusJobs && len(m.JobQueue) > 0 && m.SelectedJobIndex < len(m.JobQueue) {
				selJob := m.JobQueue[m.SelectedJobIndex]
				if selJob.Status == jobs.JobRunning {
					cmds = append(cmds, m.loadJobLogsCmd(selJob))
				}
			}
			m.updateViewport()
		}

	case loadedJobLogsMsg:
		if msg.err == nil && len(msg.logs) > 0 {
			for _, j := range m.JobQueue {
				if j.ID == msg.jobID {
					j.Logs = msg.logs
					j.GHJobID = msg.ghJobID
					break
				}
			}
			m.updateViewport()
		}

	case orgSyncedMsg:
		m.IsOrgSyncing = false
		if len(m.Repos) > 0 && len(msg.repos) > 0 {
			oldRepoBranches := make(map[string]string)
			for _, r := range m.Repos {
				oldRepoBranches[r.Name] = r.CurrentBranch
			}
			for _, newR := range msg.repos {
				if oldBranch, ok := oldRepoBranches[newR.Name]; ok && oldBranch != "" && newR.CurrentBranch != "" && oldBranch != newR.CurrentBranch {
					m.ToastMsg = fmt.Sprintf("  Branch changed for %s: %s → %s", newR.Name, oldBranch, newR.CurrentBranch)
				}
			}
		}
		m.Repos = msg.repos
		sort.Slice(m.Repos, func(i, j int) bool {
			return strings.ToLower(m.Repos[i].Name) < strings.ToLower(m.Repos[j].Name)
		})
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

		// Mirror the same height budget as View()
		rightBoxH := msg.Height - 3
		if rightBoxH < 13 {
			rightBoxH = 13
		}
		halfWidth := msg.Width / 2
		leftWidth := halfWidth - 1
		rightWidth := msg.Width - leftWidth - 2
		m.Viewport.Width = rightWidth - 4
		m.Viewport.Height = rightBoxH - 2 // inner content = outer - 2 (borders)
	}

	var spinnerCmd tea.Cmd
	m.Spinner, spinnerCmd = m.Spinner.Update(msg)
	cmds = append(cmds, spinnerCmd)

	var vpCmd tea.Cmd
	m.Viewport, vpCmd = m.Viewport.Update(msg)
	cmds = append(cmds, vpCmd)

	return m, tea.Batch(cmds...)
}

func (m *Model) triggerTabFetch() tea.Cmd {
	if len(m.Repos) == 0 || m.SelectedIndex >= len(m.Repos) {
		return nil
	}
	item := m.Repos[m.SelectedIndex]
	if m.ActiveTab == TabIssues {
		item.IsLoadingIssues = true
		return m.fetchIssuesCmd(item.Name, item.GHRepoName)
	}
	if m.ActiveTab == TabPRs {
		item.IsLoadingPRs = true
		return m.fetchPRsCmd(item.Name, item.GHRepoName)
	}
	return nil
}

func (m *Model) updateViewport() {
	var sb strings.Builder

	switch m.ActiveFocus {

	case FocusRunners:
		if len(m.Runners) == 0 {
			m.Viewport.SetContent(lipgloss.NewStyle().Foreground(colorMuted).Render(" No registered runners found for org."))
			return
		}
		if m.SelectedRunnerIndex >= len(m.Runners) {
			m.SelectedRunnerIndex = 0
		}
		runner := m.Runners[m.SelectedRunnerIndex]

		// --- STATUS BADGE ---
		var statusColor lipgloss.Color
		var statusGlyph string
		switch runner.Status {
		case jobs.RunnerRunning:
			statusColor, statusGlyph = colorSecondary, "⚡"
		case jobs.RunnerOffline:
			statusColor, statusGlyph = colorRed, "✖"
		case jobs.RunnerMaintenance:
			statusColor, statusGlyph = colorYellow, "⚠"
		default:
			statusColor, statusGlyph = colorGreen, "●"
		}
		statusBadge := lipgloss.NewStyle().Foreground(statusColor).Bold(true).
			Render(fmt.Sprintf("%s %s", statusGlyph, runner.Status))

		// --- HEADER ---
		sb.WriteString(fmt.Sprintf(" %s %s   %s\n",
			iconRunner,
			lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Render(runner.Name),
			statusBadge,
		))
		sb.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(
			fmt.Sprintf(" %s", strings.Repeat("─", m.Viewport.Width-2)),
		) + "\n")

		// --- DETAILS TABLE ---
		label := func(k, v string, vc lipgloss.Color) string {
			return fmt.Sprintf(" %s  %s\n",
				lipgloss.NewStyle().Foreground(colorMuted).Width(14).Render(k),
				lipgloss.NewStyle().Foreground(vc).Render(v),
			)
		}

		sb.WriteString(label("ID", runner.ID, colorPrimary))
		sb.WriteString(label("Platform", runner.Platform, colorSecondary))

		tagsStr := strings.Join(runner.Tags, "  ")
		if tagsStr == "" {
			tagsStr = "—"
		}
		sb.WriteString(label("Labels", tagsStr, colorYellow))

		heartbeat := "—"
		if !runner.LastHeartbeat.IsZero() {
			ago := int(time.Since(runner.LastHeartbeat).Seconds())
			if ago < 60 {
				heartbeat = fmt.Sprintf("%ds ago", ago)
			} else {
				heartbeat = fmt.Sprintf("%dm ago", ago/60)
			}
		}
		sb.WriteString(label("Last Seen", heartbeat, colorMuted))

		// --- CURRENT JOB ---
		sb.WriteString("\n")
		activeJob := findJobForRunner(runner, m.JobQueue)

		curJobID := runner.CurrentJobID
		curJobName := runner.CurrentJob
		if activeJob != nil {
			curJobID = activeJob.ID
			curJobName = activeJob.Name
		}

		if activeJob != nil || (curJobName != "-" && curJobName != "") {
			jobText := curJobName
			if curJobID != "-" && curJobID != "" && curJobID != activeJob.Name {
				jobText = fmt.Sprintf("%s  %s", curJobID, curJobName)
			}

			maxCurJobLen := m.Viewport.Width - 17
			if maxCurJobLen < 15 {
				maxCurJobLen = 15
			}
			jobTextTrunc := truncateString(jobText, maxCurJobLen)

			var jobLink string
			if activeJob != nil && activeJob.RunID != 0 {
				jobURL := fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d", m.TargetOrg, activeJob.Repo, activeJob.RunID)
				if activeJob.GHJobID != 0 {
					jobURL = fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d/job/%d", m.TargetOrg, activeJob.Repo, activeJob.RunID, activeJob.GHJobID)
				}
				jobLink = Hyperlink(lipgloss.NewStyle().Foreground(colorSecondary).Bold(true).Render(jobTextTrunc), jobURL)
			} else {
				jobLink = lipgloss.NewStyle().Foreground(colorSecondary).Bold(true).Render(jobTextTrunc)
			}
			sb.WriteString(fmt.Sprintf(" %s  %s\n",
				lipgloss.NewStyle().Foreground(colorMuted).Width(14).Render("Current Job"),
				jobLink,
			))
		} else if runner.Status == jobs.RunnerRunning {
			sb.WriteString(fmt.Sprintf(" %s  %s\n",
				lipgloss.NewStyle().Foreground(colorMuted).Width(14).Render("Current Job"),
				lipgloss.NewStyle().Foreground(colorSecondary).Bold(true).Render(fmt.Sprintf("⚡ %s active workflow", m.TargetOrg)),
			))
		} else {
			sb.WriteString(fmt.Sprintf(" %s  %s\n",
				lipgloss.NewStyle().Foreground(colorMuted).Width(14).Render("Current Job"),
				lipgloss.NewStyle().Foreground(colorMuted).Render("Idle — no active job"),
			))
		}

		// --- ALL RUNNERS OVERVIEW TABLE ---
		sb.WriteString("\n")
		sb.WriteString(lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(" ALL RUNNERS") + "\n")
		sb.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(
			fmt.Sprintf(" %s", strings.Repeat("─", m.Viewport.Width-2)),
		) + "\n")

		maxJobLen := m.Viewport.Width - 29
		if maxJobLen < 15 {
			maxJobLen = 15
		}

		for i, r := range m.Runners {
			var rColor lipgloss.Color
			var rGlyph string
			switch r.Status {
			case jobs.RunnerRunning:
				rColor, rGlyph = colorSecondary, "⚡"
			case jobs.RunnerOffline:
				rColor, rGlyph = colorRed, "✖"
			case jobs.RunnerMaintenance:
				rColor, rGlyph = colorYellow, "⚠"
			default:
				rColor, rGlyph = colorGreen, "●"
			}
			marker := "  "
			if i == m.SelectedRunnerIndex {
				marker = lipgloss.NewStyle().Foreground(colorSecondary).Bold(true).Render("> ")
			}
			// Fixed-width glyph column (⚡ is 2 cells wide, others are 1 — pad to equalise)
			glyphCell := lipgloss.NewStyle().Foreground(rColor).Width(2).Render(rGlyph)
			var currentJobStr string
			switch {
			case r.Status == jobs.RunnerRunning:
				matchedJ := findJobForRunner(r, m.JobQueue)
				if matchedJ != nil {
					dispName := matchedJ.Name
					repoPrefix := matchedJ.Repo + " / "
					if strings.HasPrefix(dispName, repoPrefix) {
						dispName = strings.TrimPrefix(dispName, repoPrefix)
					}
					currentJobStr = lipgloss.NewStyle().Foreground(colorYellow).Render(truncateString(dispName, maxJobLen))
				} else if r.CurrentJob != "-" && r.CurrentJob != "" {
					currentJobStr = lipgloss.NewStyle().Foreground(colorYellow).Render(truncateString(r.CurrentJob, maxJobLen))
				} else {
					currentJobStr = lipgloss.NewStyle().Foreground(colorYellow).Render(truncateString("active job in queue", maxJobLen))
				}
			case r.CurrentJob != "-" && r.CurrentJob != "":
				currentJobStr = lipgloss.NewStyle().Foreground(colorYellow).Render(truncateString(r.CurrentJob, maxJobLen))
			default:
				currentJobStr = lipgloss.NewStyle().Foreground(colorMuted).Render("idle")
			}
			sb.WriteString(fmt.Sprintf("%s%s %s  %s\n",
				marker,
				glyphCell,
				lipgloss.NewStyle().Foreground(colorSecondary).Width(20).Render(r.Name),
				currentJobStr,
			))
		}

		// --- QUEUED / RUNNING JOBS ON THIS RUNNER ---
		var assignedJobs []*jobs.JobItem
		for _, j := range m.JobQueue {
			if j.RunnerName == runner.Name || j.RunnerID == runner.ID {
				assignedJobs = append(assignedJobs, j)
			}
		}
		if len(assignedJobs) > 0 {
			sb.WriteString("\n")
			sb.WriteString(lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(" QUEUED / RUNNING JOBS ON THIS RUNNER") + "\n")
			sb.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(
				fmt.Sprintf(" %s", strings.Repeat("─", m.Viewport.Width-2)),
			) + "\n")

			nameMaxLen := m.Viewport.Width - 32
			if nameMaxLen < 12 {
				nameMaxLen = 12
			}

			for _, j := range assignedJobs {
				var jColor lipgloss.Color
				var jGlyph string
				if j.Status == jobs.JobRunning {
					jColor, jGlyph = colorSecondary, "⚡"
				} else {
					jColor, jGlyph = colorYellow, "⏳"
				}
				jLink := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(j.ID)
				if j.RunID != 0 {
					jobURL := fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d", m.TargetOrg, j.Repo, j.RunID)
					if j.GHJobID != 0 {
						jobURL = fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d/job/%d", m.TargetOrg, j.Repo, j.RunID, j.GHJobID)
					}
					jLink = Hyperlink(jLink, jobURL)
				}
				jNameTrunc := truncateString(j.Name, nameMaxLen)
				sb.WriteString(fmt.Sprintf(" %s  %s  %s  %s\n",
					lipgloss.NewStyle().Foreground(jColor).Render(jGlyph+" "+string(j.Status)),
					jLink,
					lipgloss.NewStyle().Foreground(colorSecondary).Render(jNameTrunc),
					lipgloss.NewStyle().Foreground(colorMuted).Render(j.Duration),
				))
			}
		}

	case FocusJobs:
		if len(m.JobQueue) == 0 || m.SelectedJobIndex >= len(m.JobQueue) {
			m.Viewport.SetContent("No jobs in queue.")
			return
		}
		job := m.JobQueue[m.SelectedJobIndex]

		statusBadge := lipgloss.NewStyle().Foreground(colorYellow).Bold(true).Render("⏳ " + string(job.Status))
		if job.Status == jobs.JobRunning {
			statusBadge = lipgloss.NewStyle().Foreground(colorSecondary).Bold(true).Render("⚡ " + string(job.Status))
		} else if job.Status == jobs.JobPassed {
			statusBadge = lipgloss.NewStyle().Foreground(colorGreen).Bold(true).Render("󰄬 " + string(job.Status))
		} else if job.Status == jobs.JobFailed {
			statusBadge = lipgloss.NewStyle().Foreground(colorRed).Bold(true).Render("󰅙 " + string(job.Status))
		}

		jobURL := fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d", m.TargetOrg, job.Repo, job.RunID)
		if job.GHJobID != 0 {
			jobURL = fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d/job/%d", m.TargetOrg, job.Repo, job.RunID, job.GHJobID)
		}
		jobIDLink := Hyperlink(lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Render(job.ID), jobURL)

		runToken, jobToken := parseJobHierarchy(job.Name, job.Repo)
		runnerDisplay := job.RunnerName
		if runnerDisplay == "" {
			runnerDisplay = "Awaiting available runner node..."
		}

		sb.WriteString(fmt.Sprintf(" %s %s  |  %s  |  %s %s  |  %s %s  |  Duration: %s\n\n",
			iconQueue,
			jobIDLink,
			statusBadge,
			iconFolder,
			lipgloss.NewStyle().Foreground(colorPrimary).Render(job.Repo),
			iconBranch,
			lipgloss.NewStyle().Foreground(colorMuted).Render(job.Branch),
			job.Duration,
		))

		sb.WriteString(fmt.Sprintf(" %s  %s\n",
			lipgloss.NewStyle().Bold(true).Foreground(colorMuted).Width(10).Render("Run:"),
			lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(runToken),
		))
		sb.WriteString(fmt.Sprintf(" %s  %s\n",
			lipgloss.NewStyle().Bold(true).Foreground(colorMuted).Width(10).Render("Job:"),
			lipgloss.NewStyle().Foreground(colorYellow).Bold(true).Render(jobToken),
		))
		sb.WriteString(fmt.Sprintf(" %s  %s %s\n",
			lipgloss.NewStyle().Bold(true).Foreground(colorMuted).Width(10).Render("Runner:"),
			iconRunner,
			lipgloss.NewStyle().Foreground(colorSecondary).Bold(true).Render(runnerDisplay),
		))

		if job.Status == jobs.JobQueued {
			sb.WriteString("\n" + lipgloss.NewStyle().Foreground(colorYellow).Bold(true).Render("⏳ Job is currently queued. Awaiting available runner worker node...") + "\n")
		} else {
			dashCount := (m.Viewport.Width - 26) / 2
			if dashCount < 2 {
				dashCount = 2
			}
			divider := strings.Repeat("─", dashCount) + " ACTIVE RUNNER LOGS " + strings.Repeat("─", dashCount)
			sb.WriteString("\n" + lipgloss.NewStyle().Foreground(colorSecondary).Render(divider) + "\n\n")

			wrapWidth := m.Viewport.Width - 2
			if wrapWidth < 20 {
				wrapWidth = 40
			}
			logWrapper := lipgloss.NewStyle().Width(wrapWidth)

			if len(job.Logs) > 0 {
				for _, logLine := range job.Logs {
					styled := highlightLogLine(logLine)
					sb.WriteString(logWrapper.Render(styled) + "\n")
				}
			} else {
				var assignedRunner *jobs.RunnerItem
				for _, r := range m.Runners {
					if r.ID == job.RunnerID || r.Name == job.RunnerName {
						assignedRunner = r
						break
					}
				}

				if assignedRunner != nil && len(assignedRunner.OutputLogs) > 0 {
					for _, logLine := range assignedRunner.OutputLogs {
						styled := highlightLogLine(logLine)
						sb.WriteString(logWrapper.Render(styled) + "\n")
					}
				} else {
					sb.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render("  Fetching workflow execution logs...\n"))
				}
			}
		}

	case FocusRepos:
		if len(m.Repos) == 0 || m.SelectedIndex >= len(m.Repos) {
			return
		}

		item := m.Repos[m.SelectedIndex]

		repoURL := item.URL
		if repoURL == "" {
			repoURL = fmt.Sprintf("https://github.com/%s/%s", m.TargetOrg, item.GHRepoName)
		}
		repoLink := Hyperlink(fmt.Sprintf("%s %s", iconGithub, subtitleStyle.Render(item.GHRepoName)), repoURL)

		branchDetail := fmt.Sprintf("%s (default)", item.CurrentBranch)
		if item.CurrentBranch != item.DefaultBranch {
			branchDetail = fmt.Sprintf("%s (default: %s)", item.CurrentBranch, item.DefaultBranch)
		}
		branchURL := fmt.Sprintf("%s/tree/%s", repoURL, item.CurrentBranch)
		branchLink := Hyperlink(fmt.Sprintf("%s %s", iconBranch, branchDetail), branchURL)

		prCountStr := "0 open"
		if item.OpenPRsCount > 0 {
			prCountStr = fmt.Sprintf("%d open", item.OpenPRsCount)
		}
		prURL := fmt.Sprintf("%s/pulls", repoURL)
		prLink := Hyperlink(fmt.Sprintf("%s %s", iconPR, prCountStr), prURL)

		issueCountStr := "0 open"
		if item.OpenIssuesCount > 0 {
			issueCountStr = fmt.Sprintf("%d open", item.OpenIssuesCount)
		}
		issueURL := fmt.Sprintf("%s/issues", repoURL)
		issueLink := Hyperlink(fmt.Sprintf("%s %s", iconIssue, issueCountStr), issueURL)

		sb.WriteString(fmt.Sprintf(" %s  |  %s  |  %s  |  %s  |  %s\n\n",
			repoLink, branchLink, lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render(item.StatusMsg), prLink, issueLink,
		))

		sb.WriteString(m.renderTabBar() + "\n\n")

		switch m.ActiveTab {

		case TabLogs:
			if item.ExistingPRURL != "" {
				sb.WriteString(fmt.Sprintf("%s %s %s\n", lipgloss.NewStyle().Bold(true).Foreground(colorYellow).Render("OPEN PR: "), iconPR, badgePR.Render(item.ExistingPRURL)))
			}
			if item.DraftPRURL != "" && item.DraftPRURL != item.ExistingPRURL {
				sb.WriteString(fmt.Sprintf("%s %s %s\n", lipgloss.NewStyle().Bold(true).Foreground(colorYellow).Render("DRAFT PR:"), iconPR, badgePR.Render(item.DraftPRURL)))
			}

			dashCount := (m.Viewport.Width - 18) / 2
			if dashCount < 2 {
				dashCount = 2
			}
			divider := strings.Repeat("─", dashCount) + " EXECUTION LOGS " + strings.Repeat("─", dashCount)
			sb.WriteString("\n" + lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Render(divider) + "\n")

			wrapWidth := m.Viewport.Width - 2
			if wrapWidth < 20 {
				wrapWidth = 40
			}
			logWrapper := lipgloss.NewStyle().Width(wrapWidth)

			for _, logLine := range item.Logs {
				styled := highlightLogLine(logLine)
				sb.WriteString(logWrapper.Render(styled) + "\n")
			}

		case TabBranches:
			sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Render("󰓦 BRANCHES & WORKTREES") + "\n")
			sb.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render("(Press 'X' to git fetch --prune, delete non-default local branches & prune worktrees)") + "\n\n")

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
			spinnerStr := ""
			if item.IsLoadingIssues {
				spinnerStr = fmt.Sprintf("  %s %s", m.Spinner.View(), lipgloss.NewStyle().Foreground(colorMuted).Render("Updating..."))
			}
			sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Render(fmt.Sprintf("⊙ OPEN ISSUES (%d)", item.OpenIssuesCount)) + spinnerStr + "\n\n")

			if len(item.IssuesList) == 0 && item.IsLoadingIssues {
				sb.WriteString(fmt.Sprintf("  %s Loading open issues from GitHub...\n", m.Spinner.View()))
			} else if len(item.IssuesList) == 0 {
				sb.WriteString("  󰄬 No open issues found for this repository.\n")
			} else {
				titleWrapper := lipgloss.NewStyle().Width(m.Viewport.Width - 6)
				for _, issue := range item.IssuesList {
					header := fmt.Sprintf("#%-4d %s", issue.Number, issue.Title)
					sb.WriteString(fmt.Sprintf("  %s %s\n     %s\n\n",
						badgeIssue.Render("⊙"), titleWrapper.Render(header),
						lipgloss.NewStyle().Foreground(colorBlue).Underline(true).Render(issue.URL),
					))
				}
			}

		case TabPRs:
			spinnerStr := ""
			if item.IsLoadingPRs {
				spinnerStr = fmt.Sprintf("  %s %s", m.Spinner.View(), lipgloss.NewStyle().Foreground(colorMuted).Render("Updating..."))
			}
			sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Render(fmt.Sprintf("󰏫 OPEN PULL REQUESTS (%d)", item.OpenPRsCount)) + spinnerStr + "\n\n")

			if len(item.PRsList) == 0 && item.IsLoadingPRs {
				sb.WriteString(fmt.Sprintf("  %s Loading open pull requests from GitHub...\n", m.Spinner.View()))
			} else if len(item.PRsList) == 0 {
				sb.WriteString("  󰄬 No open pull requests found for this repository.\n")
			} else {
				titleWrapper := lipgloss.NewStyle().Width(m.Viewport.Width - 6)
				for _, pr := range item.PRsList {
					header := fmt.Sprintf("#%-4d %s (%s %s)", pr.Number, pr.Title, iconBranch, pr.HeadRefName)
					sb.WriteString(fmt.Sprintf("  %s %s\n     %s\n\n",
						badgePR.Render("󰏫"), titleWrapper.Render(header),
						lipgloss.NewStyle().Foreground(colorBlue).Underline(true).Render(pr.URL),
					))
				}
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

func highlightLogLine(line string) string {
	if line == "" {
		return line
	}

	line = regexp.MustCompile(`\[\d{2}:\d{2}:\d{2}\]`).ReplaceAllStringFunc(line, func(m string) string {
		return lipgloss.NewStyle().Foreground(colorMuted).Render(m)
	})

	cmdRegex := regexp.MustCompile(`\b(git pull|git push|git fetch|git rebase|git checkout|git stash|git add|gh pr create|gh pr list|gh repo list|go test|go build|shellcheck)\b`)
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

	if strings.Contains(line, "󰄬") || strings.Contains(line, "PASS") || strings.Contains(line, "successfully") || strings.Contains(line, "Up to date") {
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

	// 1. Header Banner
	shortTargetDir := git.ShortenHomePath(m.TargetDir)
	leftTitle := titleStyle.Render(fmt.Sprintf(" %s FRESHEN ", iconLeaf)) + " " +
		subtitleStyle.Render("GitHub Repository & CI Runner Workflow Manager")

	orgURL := fmt.Sprintf("https://github.com/%s", m.TargetOrg)
	clickableOrg := Hyperlink(fmt.Sprintf("%s %s", iconGithub, m.TargetOrg), orgURL)
	rightSubtitle := lipgloss.NewStyle().Foreground(colorMuted).Render(fmt.Sprintf("%s |  %s", shortTargetDir, clickableOrg))

	leftWidthVis := lipgloss.Width(leftTitle)
	rightWidthVis := lipgloss.Width(shortTargetDir+" | "+m.TargetOrg) + 3

	spacingLen := m.Width - leftWidthVis - rightWidthVis
	if spacingLen < 1 {
		spacingLen = 1
	}

	headerText := leftTitle + strings.Repeat(" ", spacingLen) + rightSubtitle
	header := lipgloss.NewStyle().MaxWidth(m.Width).Render(headerText) + "\n"

	// 2. Split Layout Dimensions
	//
	// Lipgloss .Height(N) sets the INNER content area. The outer rendered block is N+2 lines
	// (top border + N content lines + bottom border).
	//
	// Left column: 3 boxes stacked, each rendered as (innerHeight + 2) lines.
	//   Total rendered left = (repoH + 2) + (runnersH + 2) + (jobsH + 2)
	//                       = totalInner + 6
	//
	// Right pane: 1 box, rendered as (rightBoxHeight + 2) lines.
	//
	// JoinHorizontal height = max(left, right).
	// We size rightBoxHeight so right = left: rightBoxHeight + 2 = totalInner + 6
	//   => rightBoxHeight = totalInner + 4
	//
	// Total output lines = 1 (header\n) + mainView lines = 1 + (rightBoxHeight + 2)
	// We need: 1 + rightBoxHeight + 2 = m.Height
	//   => rightBoxHeight = m.Height - 3
	//   => totalInner = rightBoxHeight - 4 = m.Height - 7
	//
	halfWidth := m.Width / 2
	leftWidth := halfWidth - 1
	rightWidth := m.Width - leftWidth - 2

	// Inner content width: border (1) + padding (1) on each side = 4 chars overhead
	paneInnerWidth := leftWidth - 4
	if paneInnerWidth < 30 {
		paneInnerWidth = 30
	}

	rightBoxHeight := m.Height - 3
	if rightBoxHeight < 13 {
		rightBoxHeight = 13
	}

	// totalInner is the sum of content heights for the 3 left boxes
	totalInner := rightBoxHeight - 4
	if totalInner < 9 {
		totalInner = 9
	}

	// runnersBoxHeight=4 means inner content=4 lines, outer rendered=6 (top+4+bottom)
	runnersBoxHeight := 4 // inner content lines (renders as 6 outer lines)
	repoBoxHeight := (totalInner - runnersBoxHeight) * 60 / 100
	if repoBoxHeight < 4 {
		repoBoxHeight = 4
	}

	jobsBoxHeight := totalInner - repoBoxHeight - runnersBoxHeight
	if jobsBoxHeight < 3 {
		jobsBoxHeight = 3
	}

	// clipLine clips a rendered line to the inner pane width, preventing terminal wrapping
	clipLine := func(line string) string {
		return lipgloss.NewStyle().MaxWidth(paneInnerWidth).Render(line)
	}
	// sliceLines clips a slice to at most n lines (prevents .Height(N) from overflowing)
	sliceLines := func(lines []string, n int) []string {
		if len(lines) > n {
			return lines[:n]
		}
		return lines
	}

	// ------------------ PANEL 1: REPOSITORIES ------------------
	var repoLines []string
	availRepoW := paneInnerWidth - 18
	if availRepoW < 24 {
		availRepoW = 24
	}
	repoNameW := (availRepoW * 55) / 100
	if repoNameW < 18 {
		repoNameW = 18
	}
	branchW := availRepoW - repoNameW
	if branchW < 12 {
		branchW = 12
	}

	dynCellNameStyle := lipgloss.NewStyle().Width(repoNameW)
	dynCellBranchStyle := lipgloss.NewStyle().Width(branchW)

	repoHeader := fmt.Sprintf("%s%s %s %s %s",
		cellStatusIconStyle.Render(""),
		dynCellNameStyle.Bold(true).Foreground(colorPrimary).Render("REPOSITORY"),
		dynCellBranchStyle.Bold(true).Foreground(colorPrimary).Render("BRANCH"),
		cellPRsStyle.Bold(true).Foreground(colorPrimary).Render("PRs"),
		cellIssuesStyle.Bold(true).Foreground(colorPrimary).Render("ISSUES"),
	)
	repoLines = append(repoLines, clipLine(repoHeader))
	repoLines = append(repoLines, clipLine(lipgloss.NewStyle().Foreground(colorMuted).Render(strings.Repeat("─", paneInnerWidth))))

	if m.IsOrgSyncing {
		repoLines = append(repoLines, clipLine(m.Spinner.View()+" Fetching GitHub repositories..."))
	} else if len(m.Repos) == 0 {
		repoLines = append(repoLines, clipLine("No repositories found in target directory."))
	} else {
		maxRepoRows := repoBoxHeight - 4
		if maxRepoRows < 1 {
			maxRepoRows = 1
		}

		startIdx := 0
		if m.SelectedIndex >= maxRepoRows {
			startIdx = m.SelectedIndex - maxRepoRows + 1
		}
		endIdx := startIdx + maxRepoRows
		if endIdx > len(m.Repos) {
			endIdx = len(m.Repos)
		}

		for i := startIdx; i < endIdx; i++ {
			item := m.Repos[i]
			statusIconStr := cellStatusIconStyle.Render(m.renderStatusBadge(item))
			nameCell := dynCellNameStyle.Render(truncateString(item.Name, repoNameW-1))

			branchStr := item.CurrentBranch
			if branchStr == "" {
				branchStr = "-"
			}
			branchStr = truncateString(branchStr, branchW-1)
			displayText := fmt.Sprintf(" %s", branchStr)

			var branchStyle lipgloss.Style
			isDefaultBranch := branchStr == "main" || branchStr == "master"

			if item.IsArchived {
				displayText = " Archived"
				branchStyle = lipgloss.NewStyle().Foreground(colorMuted).Strikethrough(true)
			} else if item.Status == git.StatusError || item.Status == git.StatusRebaseConflict {
				branchStyle = lipgloss.NewStyle().Foreground(colorRed).Bold(true)
			} else if item.HasUnstagedChanges {
				branchStyle = lipgloss.NewStyle().Foreground(colorYellow).Bold(true)
			} else if isDefaultBranch || branchStr == "-" {
				branchStyle = lipgloss.NewStyle().Foreground(colorMuted)
			} else {
				branchStyle = lipgloss.NewStyle().Foreground(colorGreen).Bold(true)
			}

			branchCell := dynCellBranchStyle.Render(branchStyle.Render(displayText))

			var prsCell string
			if item.OpenPRsCount > 0 {
				prsCell = cellPRsStyle.Foreground(colorYellow).Bold(true).Render(fmt.Sprintf("%d", item.OpenPRsCount))
			} else {
				prsCell = cellPRsStyle.Foreground(colorMuted).Render("-")
			}

			var issuesCell string
			if item.OpenIssuesCount > 0 {
				issuesCell = cellIssuesStyle.Foreground(colorBlue).Bold(true).Render(fmt.Sprintf("%d", item.OpenIssuesCount))
			} else {
				issuesCell = cellIssuesStyle.Foreground(colorMuted).Render("-")
			}

			line := fmt.Sprintf("%s%s %s %s %s", statusIconStr, nameCell, branchCell, prsCell, issuesCell)

			if m.ActiveFocus == FocusRepos && i == m.SelectedIndex {
				repoLines = append(repoLines, clipLine(selectedRowStyle.Width(paneInnerWidth).Render("> "+line)))
			} else {
				repoLines = append(repoLines, clipLine(normalRowStyle.Render("  "+line)))
			}
		}
	}

	repoStyle := borderBoxStyle
	if m.ActiveFocus == FocusRepos {
		repoStyle = borderFocusedStyle
	}
	repoPane := repoStyle.
		Width(leftWidth).
		Height(repoBoxHeight).
		Render(strings.Join(sliceLines(repoLines, repoBoxHeight), "\n"))

	// ------------------ PANEL 2: REGISTERED RUNNERS (ULTRA-COMPACT GROUPED) ------------------
	var runnerLines []string
	runnerHeader := fmt.Sprintf(" %s %s", iconRunner, lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Render("REGISTERED RUNNERS"))
	runnerLines = append(runnerLines, clipLine(runnerHeader))
	runnerLines = append(runnerLines, clipLine(lipgloss.NewStyle().Foreground(colorMuted).Render(strings.Repeat("─", paneInnerWidth))))

	var runningNames, idleNames, offlineNames []string
	for _, r := range m.Runners {
		switch r.Status {
		case jobs.RunnerRunning:
			runningNames = append(runningNames, r.Name)
		case jobs.RunnerOffline:
			offlineNames = append(offlineNames, r.Name)
		default:
			idleNames = append(idleNames, r.Name)
		}
	}

	if len(runningNames) > 0 {
		glyph := lipgloss.NewStyle().Foreground(colorSecondary).Bold(true).Width(2).Render("⚡")
		label := lipgloss.NewStyle().Foreground(colorSecondary).Bold(true).Render(fmt.Sprintf(" RUNNING (%d): ", len(runningNames)))
		names := lipgloss.NewStyle().Foreground(colorGreen).Render(strings.Join(runningNames, ", "))
		runnerLines = append(runnerLines, clipLine(" "+glyph+label+names))
	}
	if len(idleNames) > 0 {
		glyph := lipgloss.NewStyle().Foreground(colorGreen).Bold(true).Width(2).Render("●")
		label := lipgloss.NewStyle().Foreground(colorGreen).Bold(true).Render(fmt.Sprintf(" IDLE (%d): ", len(idleNames)))
		names := lipgloss.NewStyle().Foreground(colorMuted).Render(strings.Join(idleNames, ", "))
		runnerLines = append(runnerLines, clipLine(" "+glyph+label+names))
	}
	if len(offlineNames) > 0 {
		glyph := lipgloss.NewStyle().Foreground(colorRed).Bold(true).Width(2).Render("✖")
		label := lipgloss.NewStyle().Foreground(colorRed).Bold(true).Render(fmt.Sprintf(" OFFLINE (%d): ", len(offlineNames)))
		names := lipgloss.NewStyle().Foreground(colorRed).Render(strings.Join(offlineNames, ", "))
		runnerLines = append(runnerLines, clipLine(" "+glyph+label+names))
	}

	runnerStyle := borderBoxStyle
	if m.ActiveFocus == FocusRunners {
		runnerStyle = borderFocusedStyle
	}
	runnersPane := runnerStyle.
		Width(leftWidth).
		Height(runnersBoxHeight).
		Render(strings.Join(sliceLines(runnerLines, runnersBoxHeight), "\n"))

	// ------------------ PANEL 3: OVERALL JOB QUEUE ------------------
	var jobsLines []string
	jobHeader := fmt.Sprintf(" %s %s", iconQueue, lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render("OVERALL JOB QUEUE"))
	jobsLines = append(jobsLines, jobHeader)
	jobsLines = append(jobsLines, lipgloss.NewStyle().Foreground(colorMuted).Render(strings.Repeat("─", paneInnerWidth)))

	// maxJobRows: inner content area of jobs box minus header and divider lines
	maxJobRows := jobsBoxHeight - 2
	if maxJobRows < 1 {
		maxJobRows = 1
	}
	jobStartIdx := 0
	if m.SelectedJobIndex >= maxJobRows {
		jobStartIdx = m.SelectedJobIndex - maxJobRows + 1
	}
	jobEndIdx := jobStartIdx + maxJobRows
	if jobEndIdx > len(m.JobQueue) {
		jobEndIdx = len(m.JobQueue)
	}

	// Detect grouped runs to add tree branch connectors for matrix jobs
	runCounts := make(map[int64]int)
	runIndices := make(map[int64][]int)
	for idx, j := range m.JobQueue {
		if j.RunID != 0 {
			runCounts[j.RunID]++
			runIndices[j.RunID] = append(runIndices[j.RunID], idx)
		}
	}

	for i := jobStartIdx; i < jobEndIdx; i++ {
		j := m.JobQueue[i]
		stSymbol := "⏳"
		stColor := colorYellow
		if j.Status == jobs.JobRunning {
			stSymbol = "⚡"
			stColor = colorSecondary
		} else if j.Status == jobs.JobPassed {
			stSymbol = "󰄬"
			stColor = colorGreen
		} else if j.Status == jobs.JobFailed {
			stSymbol = "󰅙"
			stColor = colorRed
		}

		stBadge := lipgloss.NewStyle().Foreground(stColor).Bold(true).Render(fmt.Sprintf("%s %-7s", stSymbol, j.Status))

		// Tree branch connector if part of a multi-job workflow run
		treePrefix := ""
		if count := runCounts[j.RunID]; count > 1 {
			indices := runIndices[j.RunID]
			if indices[len(indices)-1] == i {
				treePrefix = "└─ "
			} else {
				treePrefix = "├─ "
			}
		}

		idW := len(j.ID)
		if idW < 8 {
			idW = 8
		}
		if idW > 14 {
			idW = 14
		}

		idStrText := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Width(idW).Render(truncateString(j.ID, idW))
		if j.RunID != 0 {
			jobURL := fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d", m.TargetOrg, j.Repo, j.RunID)
			if j.GHJobID != 0 {
				jobURL = fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d/job/%d", m.TargetOrg, j.Repo, j.RunID, j.GHJobID)
			}
			idStrText = Hyperlink(idStrText, jobURL)
		}

		runnerStr := j.RunnerName
		if runnerStr == "" {
			runnerStr = "awaiting"
		}
		runnerW := len(runnerStr) + 2 // include "→ " prefix
		if runnerW < 10 {
			runnerW = 10
		}
		if runnerW > 16 {
			runnerW = 16
		}
		runnerAssigned := lipgloss.NewStyle().Foreground(colorMuted).Width(runnerW).Render("→ " + truncateString(runnerStr, runnerW-2))

		// Give all remaining inner pane space to job name
		prefixLen := lipgloss.Width(treePrefix)
		nameW := paneInnerWidth - 4 - prefixLen - 9 - 1 - idW - 1 - runnerW - 1
		if nameW < 10 {
			nameW = 10
		}

		// Smart display name: strip redundant repo prefix if job name starts with "repo / "
		displayName := j.Name
		repoPrefix := j.Repo + " / "
		if strings.HasPrefix(displayName, repoPrefix) {
			displayName = strings.TrimPrefix(displayName, repoPrefix)
		}

		nameStr := lipgloss.NewStyle().Width(nameW).Render(truncateString(displayName, nameW))

		line := fmt.Sprintf("%s%s %s %s %s", treePrefix, stBadge, idStrText, nameStr, runnerAssigned)

		if m.ActiveFocus == FocusJobs && i == m.SelectedJobIndex {
			jobsLines = append(jobsLines, clipLine(selectedRowStyle.MaxWidth(paneInnerWidth).Render("> "+line)))
		} else {
			jobsLines = append(jobsLines, clipLine(normalRowStyle.Render("  "+line)))
		}
	}

	jobsStyle := borderBoxStyle
	if m.ActiveFocus == FocusJobs {
		jobsStyle = borderFocusedStyle
	}
	jobsPane := jobsStyle.
		Width(leftWidth).
		Height(jobsBoxHeight).
		Render(strings.Join(sliceLines(jobsLines, jobsBoxHeight), "\n"))

	leftColumn := lipgloss.JoinVertical(lipgloss.Left, repoPane, runnersPane, jobsPane)

	// Render Details Viewport (Right)
	// logBoxStyle has Border (1+1) + Padding (1+1) = 4 chars horizontal overhead.
	// Passing rightWidth - 4 makes the outer rendered width equal to rightWidth.
	rightInnerWidth := rightWidth - 4
	if rightInnerWidth < 20 {
		rightInnerWidth = 20
	}

	rightPane := logBoxStyle.
		Width(rightInnerWidth).
		Height(rightBoxHeight).
		Render(m.Viewport.View())

	mainView := lipgloss.JoinHorizontal(lipgloss.Top, leftColumn, " ", rightPane)

	// 3. Footer Keybindings Help (on its own line below mainView)
	footerText := "[w/1/2/3] Focus  [↑/↓] Select  [j/k] Scroll  [c] Copy  [q] Quit"
	if m.ToastMsg != "" {
		footerText = lipgloss.NewStyle().Foreground(colorGreen).Bold(true).Render(m.ToastMsg)
	}

	footer := lipgloss.NewStyle().Foreground(colorMuted).MaxWidth(m.Width).Render(footerText)

	// header has trailing \n (1 newline)
	// mainView has (availableHeight-1) internal newlines = availableHeight lines
	// footer is concatenated to last line of mainView (no extra newline)
	// strings.Split gives: availableHeight+1 lines total = m.Height ✓
	return header + mainView + footer
}

func (m Model) renderStatusBadge(item *git.RepoItem) string {
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
		return m.Spinner.View()
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

func parseJobHierarchy(fullName, repo string) (runName, jobName string) {
	cleanName := fullName
	repoPrefix := repo + " / "
	if strings.HasPrefix(cleanName, repoPrefix) {
		cleanName = strings.TrimPrefix(cleanName, repoPrefix)
	}

	parts := strings.Split(cleanName, " / ")
	if len(parts) >= 2 {
		runName = parts[0]
		jobName = strings.Join(parts[1:], " / ")
	} else {
		runName = "-"
		jobName = cleanName
	}
	return runName, jobName
}

func findJobForRunner(r *jobs.RunnerItem, queue []*jobs.JobItem) *jobs.JobItem {
	if r == nil {
		return nil
	}
	// 1. Try exact runner name match (running)
	for _, j := range queue {
		if j.Status == jobs.JobRunning && (j.RunnerName == r.Name || j.RunnerID == r.ID) {
			return j
		}
	}
	// 2. Try case-insensitive runner name match (running)
	for _, j := range queue {
		if j.Status == jobs.JobRunning && j.RunnerName != "" && strings.EqualFold(j.RunnerName, r.Name) {
			return j
		}
	}
	// 3. Try any queued/running job assigned to this runner
	for _, j := range queue {
		if (j.RunnerName == r.Name || j.RunnerID == r.ID) && j.Name != "" {
			return j
		}
	}
	// 4. Try any running job in queue
	for _, j := range queue {
		if j.Status == jobs.JobRunning {
			return j
		}
	}
	return nil
}
