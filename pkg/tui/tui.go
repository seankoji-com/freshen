package tui

import (
	"context"
	"fmt"
	"log/slog"
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
	cellBranchStyle     = lipgloss.NewStyle().Width(20)
	cellPRsStyle        = lipgloss.NewStyle().Width(4).Align(lipgloss.Right)
	cellIssuesStyle     = lipgloss.NewStyle().Width(6).Align(lipgloss.Right)

	// Pre-compiled regex for highlightLogLine (package level to avoid re-compiling on every call)
	reTimestamp = regexp.MustCompile(`\[\d{2}:\d{2}:\d{2}\]`)
	reCmd       = regexp.MustCompile(`\b(git pull|git push|git fetch|git rebase|git checkout|git stash|git add|gh pr create|gh pr list|gh repo list|go test|go build|shellcheck)\b`)
	reURL       = regexp.MustCompile(`https?://[^\s]+`)
	reQuoted    = regexp.MustCompile(`'[^']+'`)
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
	// autoSync is true only for the initial load; periodic refreshes only
	// update displayed repo metadata and must not trigger a full
	// stash/pull/apply sync of every dirty repo.
	autoSync bool
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
	SelectedTagIndex    int
	ActiveFocus         FocusType
	ActiveTab           TabType
	IsSyncing           bool
	IsOrgSyncing        bool
	IsJobQueueLoading   bool
	IsRunnersLoading    bool
	TotalCount          int
	ToastMsg            string
	FocusedRunID        int64 // When non-zero, a specific workflow run is focused
	ToastPriority       int   // higher priority overrides lower; 0 = none, 1 = info, 2 = error
	ConsecutiveErrors   int
	RunnerFetchFailed   bool
	JobQueueFetchFailed bool

	// pendingDeleteIndex holds the Repos index awaiting a second 'd' press
	// to confirm deletion of an archived repo; -1 means no pending delete.
	pendingDeleteIndex int

	Spinner     spinner.Model
	ProgressBar progress.Model
	Viewport    viewport.Model
	Width       int
	Height      int

	mu sync.Mutex

	// ctx is cancelled on quit (via key or OS signal) to abort in-flight git
	// operations instead of letting them keep running after the UI exits.
	// bgWG tracks those operations so the caller can wait for them to
	// actually stop before the process exits.
	ctx    context.Context
	cancel context.CancelFunc
	bgWG   *sync.WaitGroup
}

// NewModel constructs the TUI model. ctx should be cancelled by the caller
// (e.g. on quit or an OS signal) to abort in-flight background git
// operations; bgWG tracks those operations for a bounded shutdown wait.
func NewModel(targetDir, targetOrg string, ctx context.Context, cancel context.CancelFunc, bgWG *sync.WaitGroup) Model {
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
		pendingDeleteIndex:  -1,
		ActiveFocus:         FocusRepos,
		ActiveTab:           TabLogs,
		IsOrgSyncing:        true,
		IsJobQueueLoading:   true,
		IsRunnersLoading:    true,
		Spinner:             s,
		ProgressBar:         p,
		Viewport:            vp,
		ctx:                 ctx,
		cancel:              cancel,
		bgWG:                bgWG,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.Spinner.Tick,
		m.loadOrgReposCmd(true),
		m.loadRunnersCmd(),
		m.loadJobQueueCmd(),
		repoTickCmd(),
		runnerJobTickCmd(),
	)
}

func (m Model) loadRunnersCmd() tea.Cmd {
	return func() tea.Msg {
		runners, err := jobs.FetchOrgRunners(m.TargetOrg)
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
	return func() tea.Msg {
		// If repos not loaded yet, fetch from org API inside the closure
		// to avoid blocking the main goroutine (was a main-thread-blocking bug).
		repoList := repos
		if len(repoList) == 0 {
			orgRepos, err := git.FetchOrgRepos(m.TargetOrg)
			if err == nil {
				for _, r := range orgRepos {
					if !r.IsArchived {
						repoList = append(repoList, r.Name)
					}
				}
			}
		}
		queue, err := jobs.FetchOrgJobQueue(m.TargetOrg, repoList)
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
	ghJobID := job.GHJobID
	jobName := job.Name
	jobID := job.ID
	return func() tea.Msg {
		lines, resolvedGHJobID, err := jobs.FetchJobLogs(org, repo, runID, ghJobID, jobName, 200)
		return loadedJobLogsMsg{jobID: jobID, ghJobID: resolvedGHJobID, logs: lines, err: err}
	}
}

func (m Model) loadOrgReposCmd(autoSync bool) tea.Cmd {
	return func() tea.Msg {
		orgRepos, err := git.FetchOrgRepos(m.TargetOrg)
		if err != nil {
			return orgSyncedMsg{repos: nil, err: err, autoSync: autoSync}
		}

		orgCounts, countsErr := git.FetchOrgRepoCounts(m.TargetOrg)
		if countsErr != nil {
			slog.Debug("org repo counts fetch failed", "org", m.TargetOrg, "error", countsErr)
		}

		entries, err := git.ScanLocalDirectory(m.TargetDir)
		if err != nil {
			return orgSyncedMsg{repos: nil, err: err, autoSync: autoSync}
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
				item.HasLoadedCounts = true
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
						item.HasLoadedCounts = true
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

		return orgSyncedMsg{repos: result, err: nil, autoSync: autoSync}
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

// bgGuard wraps task with bgWG tracking: Add(1) runs immediately (on the
// caller's goroutine, before any `go` statement), and Done() is deferred
// inside the returned function so it always fires once task completes,
// whether that function is invoked inline or launched in a new goroutine.
// This is the single choke point background git tasks must go through so
// the bgWG guard can't be skipped at a launch site.
func (m Model) bgGuard(task func()) func() {
	if m.bgWG != nil {
		m.bgWG.Add(1)
	}
	return func() {
		if m.bgWG != nil {
			defer m.bgWG.Done()
		}
		task()
	}
}

func (m Model) startParallelSyncCmd() tea.Cmd {
	run := m.bgGuard(func() {
		var wg sync.WaitGroup
		concurrency := 4
		sem := make(chan struct{}, concurrency)

		for _, item := range m.Repos {
			if item.IsArchived {
				continue
			}
			if m.ctx.Err() != nil {
				break
			}

			wg.Add(1)
			sem <- struct{}{}

			go func(r *git.RepoItem) {
				defer wg.Done()
				defer func() { <-sem }()

				git.SyncRepository(m.ctx, r)
			}(item)
		}

		wg.Wait()
	})
	return func() tea.Msg {
		run()
		return syncFinishedMsg{}
	}
}

func copyToClipboard(text string) error {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

// setToast sets a toast message with the given priority. Higher priority toasts
// (error=2) override lower ones (info=1), and equal priorities replace.
// Priority 0 clears on next keypress.
func (m *Model) setToast(msg string, priority int) {
	if priority >= m.ToastPriority {
		m.ToastMsg = msg
		m.ToastPriority = priority
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case tea.KeyMsg:
		m.setToast("", 0)
		if msg.String() != "d" {
			m.pendingDeleteIndex = -1
		}
		switch msg.String() {

		case "q", "ctrl+c":
			if m.cancel != nil {
				m.cancel()
			}
			return m, tea.Quit

		case "enter":
			if m.ActiveFocus == FocusJobs && len(m.JobQueue) > 0 && m.SelectedJobIndex < len(m.JobQueue) {
				job := m.JobQueue[m.SelectedJobIndex]
				// If already focused on this run, unfocus it
				if m.FocusedRunID == job.RunID {
					m.FocusedRunID = 0
				} else {
					// Focus this run
					m.FocusedRunID = job.RunID
				}
				m.updateViewport()
			}

		case "esc":
			if m.FocusedRunID != 0 {
				m.FocusedRunID = 0
				m.updateViewport()
			}

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
					m.setToast(" Focused Repositories Panel", 1)
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
				} else {
					m.ActiveFocus = FocusRunners
					matching := m.getMatchingRunners()
					if len(matching) > 0 {
						m.SelectedRunnerIndex = len(matching) - 1
					} else {
						m.SelectedRunnerIndex = 0
					}
					m.setToast(" Focused Runners Panel", 1)
					m.updateViewport()
				}
			}

		case "down":
			switch m.ActiveFocus {
			case FocusRepos:
				if m.SelectedIndex < len(m.Repos)-1 {
					m.SelectedIndex++
					m.updateViewport()
				} else {
					m.ActiveFocus = FocusRunners
					m.SelectedRunnerIndex = 0
					m.setToast(" Focused Runners Panel", 1)
					m.updateViewport()
				}
			case FocusRunners:
				matching := m.getMatchingRunners()
				if m.SelectedRunnerIndex < len(matching)-1 {
					m.SelectedRunnerIndex++
					m.updateViewport()
				} else {
					m.ActiveFocus = FocusJobs
					m.SelectedJobIndex = 0
					m.setToast(" Focused Jobs Panel", 1)
					m.updateViewport()
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

		case "right", "l", "tab":
			if m.ActiveFocus == FocusRunners {
				tags := m.getAvailableTags()
				if len(tags) > 0 {
					m.SelectedTagIndex = (m.SelectedTagIndex + 1) % len(tags)
					m.updateViewport()
				}
			} else if m.ActiveFocus == FocusRepos {
				m.ActiveTab = (m.ActiveTab + 1) % 4
				m.updateViewport()
				return m, m.triggerTabFetch()
			} else {
				m.ActiveFocus = (m.ActiveFocus + 1) % 3
				m.updateViewport()
			}

		case "left", "h", "shift+tab":
			if m.ActiveFocus == FocusRunners {
				tags := m.getAvailableTags()
				if len(tags) > 0 {
					m.SelectedTagIndex = (m.SelectedTagIndex - 1 + len(tags)) % len(tags)
					m.updateViewport()
				}
			} else if m.ActiveFocus == FocusRepos {
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
					m.setToast(fmt.Sprintf(" 󰄬 Fetched & pruned remote refs, removed worktrees & deleted %d branches!", count), 1)
					m.updateViewport()
				}
			}

		case "r":
			if m.ActiveFocus == FocusRepos && len(m.Repos) > 0 && m.SelectedIndex < len(m.Repos) {
				item := m.Repos[m.SelectedIndex]
				if !item.IsArchived {
					go m.bgGuard(func() {
						git.SyncRepository(m.ctx, item)
					})()
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
					m.setToast(fmt.Sprintf(" %s Copied to clipboard: %s", iconCopy, targetCopy), 1)
				}
			} else if m.ActiveFocus == FocusRunners && len(m.Runners) > 0 && m.SelectedRunnerIndex < len(m.Runners) {
				r := m.Runners[m.SelectedRunnerIndex]
				if err := copyToClipboard(r.ID); err == nil {
					m.setToast(fmt.Sprintf(" %s Copied Runner ID to clipboard: %s", iconCopy, r.ID), 1)
				}
			} else if m.ActiveFocus == FocusJobs && len(m.JobQueue) > 0 && m.SelectedJobIndex < len(m.JobQueue) {
				j := m.JobQueue[m.SelectedJobIndex]
				if err := copyToClipboard(j.ID); err == nil {
					m.setToast(fmt.Sprintf(" %s Copied Job ID to clipboard: %s", iconCopy, j.ID), 1)
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
					go m.bgGuard(func() {
						if err := git.CommitPushPRAndSwitchDefault(item); err == nil {
							item.CurrentBranch = git.GetOriginalBranch(item.Path)
							item.BranchDetails = git.GetRepoBranchDetails(item.Path, item.DefaultBranch)
						}
					})()
					m.updateViewport()
				}
			}

		case "d":
			if m.ActiveFocus == FocusRepos && len(m.Repos) > 0 && m.SelectedIndex < len(m.Repos) {
				item := m.Repos[m.SelectedIndex]
				if item.IsArchived {
					if m.pendingDeleteIndex == m.SelectedIndex {
						m.pendingDeleteIndex = -1
						if err := git.DeleteLocalRepo(item.Path); err != nil {
							m.setToast(fmt.Sprintf(" ⚠ Failed to delete '%s': %v", item.Name, err), 2)
						} else {
							deletedName := item.Name

							m.Repos = append(m.Repos[:m.SelectedIndex], m.Repos[m.SelectedIndex+1:]...)
							m.TotalCount = len(m.Repos)

							if m.SelectedIndex >= len(m.Repos) && len(m.Repos) > 0 {
								m.SelectedIndex = len(m.Repos) - 1
							}

							m.setToast(fmt.Sprintf(" 🗑️ Deleted archived repo '%s' from disk.", deletedName), 1)
							m.updateViewport()
						}
					} else {
						m.pendingDeleteIndex = m.SelectedIndex
						m.setToast(fmt.Sprintf(" ⚠ Press 'd' again to delete archived repo '%s'.", item.Name), 2)
					}
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
				// Use panel heights from View() layout instead of content lengths
				rightBoxHeight := m.Height - 4
				if rightBoxHeight < 12 {
					rightBoxHeight = 12
				}
				totalInner := rightBoxHeight - 4
				if totalInner < 8 {
					totalInner = 8
				}
				runnersBoxHeight := 4
				repoBoxHeight := (totalInner - runnersBoxHeight) * 60 / 100
				if repoBoxHeight < 4 {
					repoBoxHeight = 4
				}

				// Y=0 is header, Y=1 starts the first panel
				// Each bordered panel: 1 top border + innerHeight + 1 bottom border = innerHeight + 2
				repoPaneStart := 1 + 1 // header + top border
				repoPaneEnd := repoPaneStart + repoBoxHeight
				runnersPaneStart := repoPaneEnd + 1 // bottom border of repo + top border of runners
				runnersPaneEnd := runnersPaneStart + runnersBoxHeight

				if msg.Y >= repoPaneStart && msg.Y <= repoPaneEnd {
					m.ActiveFocus = FocusRepos
					clickedIdx := msg.Y - repoPaneStart - 1 // -1 for header row
					if clickedIdx >= 0 && clickedIdx < len(m.Repos) {
						m.SelectedIndex = clickedIdx
					}
					m.updateViewport()
				} else if msg.Y > repoPaneEnd && msg.Y <= runnersPaneEnd {
					m.ActiveFocus = FocusRunners
					clickedIdx := msg.Y - runnersPaneStart
					if clickedIdx >= 0 && clickedIdx < len(m.Runners) {
						m.SelectedRunnerIndex = clickedIdx
					}
					m.updateViewport()
				} else if msg.Y > runnersPaneEnd {
					m.ActiveFocus = FocusJobs
					jobsPaneStart := runnersPaneEnd + 1
					clickedIdx := msg.Y - jobsPaneStart - 2
					if clickedIdx >= 0 && clickedIdx < len(m.JobQueue) {
						m.SelectedJobIndex = clickedIdx
					}
					m.updateViewport()
				}
			}
			// Click in Right Detail View Pane
			if msg.X >= m.Width/2 && (msg.Y == 4 || msg.Y == 5) {
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
			m.loadOrgReposCmd(false),
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
		m.IsRunnersLoading = false
		if msg.err != nil {
			m.RunnerFetchFailed = true
			m.ConsecutiveErrors++
			slog.Error("runner fetch failed", "org", m.TargetOrg, "error", msg.err)
			m.setToast(fmt.Sprintf(" ⚠ Runner fetch failed: %v", msg.err), 2)
		} else {
			m.RunnerFetchFailed = false
			m.ConsecutiveErrors = 0
			// Always update runners, even if empty
			merged := jobs.MergeRunners(msg.runners, m.Runners, m.JobQueue)
			m.Runners = merged

			if len(m.Runners) == 0 {
				m.setToast(" No registered runners found for org.", 1)
			}

			m.JobQueue = reconcileRunnerJobs(m.Runners, m.JobQueue, m.TargetOrg)
			m.updateViewport()
		}

	case loadedJobQueueMsg:
		m.IsJobQueueLoading = false
		if msg.err != nil {
			m.JobQueueFetchFailed = true
			m.ConsecutiveErrors++
			slog.Error("job queue fetch failed", "org", m.TargetOrg, "error", msg.err)
			m.setToast(fmt.Sprintf(" ⚠ Job queue may be incomplete: %v", msg.err), 2)
			if len(msg.queue) > 0 {
				m.processJobQueueUpdate(msg.queue)
				cmds = append(cmds, m.triggerLogFetchForSelectedJob())
			}
			m.updateViewport()
		} else {
			m.JobQueueFetchFailed = false
			m.ConsecutiveErrors = 0
			m.processJobQueueUpdate(msg.queue)
			cmds = append(cmds, m.triggerLogFetchForSelectedJob())
			m.updateViewport()
		}

	case loadedJobLogsMsg:
		if msg.err != nil {
			slog.Debug("log fetch failed", "jobID", msg.jobID, "error", msg.err)
		}
		if len(msg.logs) > 0 {
			for _, j := range m.JobQueue {
				if j.ID == msg.jobID {
					j.Logs = msg.logs
					j.GHJobID = msg.ghJobID
					break
				}
			}
			m.updateViewport()
		} else if msg.err != nil {
			for _, j := range m.JobQueue {
				if j.ID == msg.jobID {
					j.Logs = []string{"[log fetch failed: " + msg.err.Error() + "]"}
					break
				}
			}
			m.updateViewport()
		}

	case orgSyncedMsg:
		m.IsOrgSyncing = false
		if msg.err != nil {
			slog.Error("org repos fetch failed", "org", m.TargetOrg, "error", msg.err)
			m.setToast(fmt.Sprintf(" %s Fetch failed: %v. Check 'gh auth status'.", iconError, msg.err), 2)
			m.updateViewport()
			return m, tea.Batch(cmds...)
		}
		if len(m.Repos) > 0 && len(msg.repos) > 0 {
			oldRepoBranches := make(map[string]string)
			for _, r := range m.Repos {
				oldRepoBranches[r.Name] = r.CurrentBranch
			}
			for _, newR := range msg.repos {
				if oldBranch, ok := oldRepoBranches[newR.Name]; ok && oldBranch != "" && newR.CurrentBranch != "" && oldBranch != newR.CurrentBranch {
					m.setToast(fmt.Sprintf("  Branch changed for %s: %s → %s", newR.Name, oldBranch, newR.CurrentBranch), 1)
				}
			}
		}
		m.Repos = msg.repos
		sort.Slice(m.Repos, func(i, j int) bool {
			return strings.ToLower(m.Repos[i].Name) < strings.ToLower(m.Repos[j].Name)
		})
		m.TotalCount = len(m.Repos)
		if msg.autoSync && len(m.Repos) > 0 {
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
		rightBoxH := msg.Height - 4
		if rightBoxH < 12 {
			rightBoxH = 12
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

// processJobQueueUpdate processes a freshly loaded job queue, comparing to the
// old queue for status-change notifications with proper priority handling.
func (m *Model) processJobQueueUpdate(queue []*jobs.JobItem) {
	if len(m.JobQueue) > 0 {
		oldJobs := make(map[string]*jobs.JobItem)
		for _, j := range m.JobQueue {
			oldJobs[j.ID] = j
		}

		newJobsMap := make(map[string]*jobs.JobItem)
		for _, j := range queue {
			newJobsMap[j.ID] = j
		}

		// Status changes — failure toasts (priority 2) survive info toasts (priority 1)
		for _, newJ := range queue {
			if oldJ, ok := oldJobs[newJ.ID]; ok {
				if oldJ.Status != newJ.Status {
					if newJ.Status == jobs.JobFailed {
						runnerStr := newJ.RunnerName
						if runnerStr == "" {
							runnerStr = "worker"
						}
						m.setToast(fmt.Sprintf(" ❌ Job %s failed (%s) on %s", newJ.ID, newJ.Name, runnerStr), 2)
					} else if newJ.Status == jobs.JobRunning {
						runnerStr := newJ.RunnerName
						if runnerStr == "" {
							runnerStr = "worker"
						}
						m.setToast(fmt.Sprintf(" ⚡ Job %s started running on %s", newJ.ID, runnerStr), 1)
					} else if newJ.Status == jobs.JobPassed {
						m.setToast(fmt.Sprintf(" ✅ Job %s passed (%s)", newJ.ID, newJ.Name), 1)
					}
				}
			} else {
				if newJ.Status == jobs.JobRunning {
					runnerStr := newJ.RunnerName
					if runnerStr == "" {
						runnerStr = "worker"
					}
					m.setToast(fmt.Sprintf(" ⚡ Job %s started running on %s", newJ.ID, runnerStr), 1)
				} else if newJ.Status == jobs.JobQueued {
					m.setToast(fmt.Sprintf(" ⏳ Job %s queued (%s)", newJ.ID, newJ.Name), 1)
				}
			}
		}

		// Jobs that finished and left the queue
		for _, oldJ := range m.JobQueue {
			if _, stillThere := newJobsMap[oldJ.ID]; !stillThere {
				if oldJ.Status == jobs.JobRunning {
					m.setToast(fmt.Sprintf(" ✅ Job %s completed (%s)", oldJ.ID, oldJ.Name), 1)
				}
			}
		}
	}

	// Preserve existing logs
	existingLogs := make(map[string][]string)
	existingGHJobID := make(map[string]int64)
	for _, j := range m.JobQueue {
		if len(j.Logs) > 0 {
			existingLogs[j.ID] = j.Logs
			existingGHJobID[j.ID] = j.GHJobID
		}
	}
	for _, j := range queue {
		if logs, ok := existingLogs[j.ID]; ok {
			j.Logs = logs
			j.GHJobID = existingGHJobID[j.ID]
		}
	}
	m.JobQueue = reconcileRunnerJobs(m.Runners, queue, m.TargetOrg)

	// Bounds validation after queue update
	if len(m.JobQueue) > 0 && m.SelectedJobIndex >= len(m.JobQueue) {
		m.SelectedJobIndex = len(m.JobQueue) - 1
	}
	if len(m.Runners) > 0 && m.SelectedRunnerIndex >= len(m.Runners) {
		m.SelectedRunnerIndex = len(m.Runners) - 1
	}
}

// triggerLogFetchForSelectedJob returns a log-fetch command if a running job is selected.
func (m Model) triggerLogFetchForSelectedJob() tea.Cmd {
	if m.ActiveFocus == FocusJobs && len(m.JobQueue) > 0 && m.SelectedJobIndex < len(m.JobQueue) {
		selJob := m.JobQueue[m.SelectedJobIndex]
		if selJob.Status == jobs.JobRunning {
			return m.loadJobLogsCmd(selJob)
		}
	}
	return nil
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

		tags := m.getAvailableTags()
		if m.SelectedTagIndex >= len(tags) {
			m.SelectedTagIndex = 0
		}
		activeTag := tags[m.SelectedTagIndex]

		// Filter runners by activeTag
		var matchingRunners []*jobs.RunnerItem
		for _, r := range m.Runners {
			if activeTag == "ALL" {
				matchingRunners = append(matchingRunners, r)
			} else {
				for _, tag := range r.Tags {
					if tag == activeTag {
						matchingRunners = append(matchingRunners, r)
						break
					}
				}
			}
		}

		busyCount := 0
		for _, r := range matchingRunners {
			if r.Status == jobs.RunnerRunning {
				busyCount++
			}
		}
		loadPct := 0
		if len(matchingRunners) > 0 {
			loadPct = (busyCount * 100) / len(matchingRunners)
		}

		// --- TAG TABS BAR (Right Column Header) ---
		var tagPills []string
		for i, tag := range tags {
			if i == m.SelectedTagIndex {
				tagPills = append(tagPills, lipgloss.NewStyle().Foreground(colorSecondary).Bold(true).Render("["+tag+"]"))
			} else {
				tagPills = append(tagPills, lipgloss.NewStyle().Foreground(colorMuted).Render(tag))
			}
		}
		sb.WriteString(" " + iconRunner + "  TAGS:  " + strings.Join(tagPills, "  ") + "\n")
		sb.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(
			fmt.Sprintf(" %s", strings.Repeat("─", m.Viewport.Width-2)),
		) + "\n\n")

		// --- DETAILS TABLE ---
		label := func(k, v string, vc lipgloss.Color) string {
			return fmt.Sprintf(" %s  %s\n",
				lipgloss.NewStyle().Foreground(colorMuted).Width(14).Render(k),
				lipgloss.NewStyle().Foreground(vc).Render(v),
			)
		}

		sb.WriteString(label("Active Tag", activeTag, colorSecondary))
		sb.WriteString(label("Runner Count", fmt.Sprintf("%d matching runners", len(matchingRunners)), colorPrimary))
		sb.WriteString(label("Cluster Load", fmt.Sprintf("%d%% load (%d busy / %d total)", loadPct, busyCount, len(matchingRunners)), colorYellow))
		if len(tags) > 1 {
			sb.WriteString(label("Tag Navigation", "[← / →] or [h / l] to cycle through fleet tags", colorMuted))
		}

		// --- MATCHING RUNNERS TABLE ---
		sb.WriteString("\n")
		sb.WriteString(lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(fmt.Sprintf(" RUNNERS MATCHING [%s]", activeTag)) + "\n")
		sb.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(
			fmt.Sprintf(" %s", strings.Repeat("─", m.Viewport.Width-2)),
		) + "\n")

		maxJobLen := m.Viewport.Width - 29
		if maxJobLen < 15 {
			maxJobLen = 15
		}

		for idx, r := range matchingRunners {
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

			isSelected := m.ActiveFocus == FocusRunners && idx == m.SelectedRunnerIndex
			rowContent := fmt.Sprintf("%s %s  %s",
				glyphCell,
				lipgloss.NewStyle().Foreground(colorSecondary).Width(20).Render(r.Name),
				currentJobStr,
			)
			if isSelected {
				sb.WriteString(selectedRowStyle.Render("> "+rowContent) + "\n")
			} else {
				sb.WriteString(normalRowStyle.Render("  "+rowContent) + "\n")
			}
		}

		// --- QUEUED / RUNNING JOBS ON MATCHING RUNNERS ---
		var assignedJobs []*jobs.JobItem
		matchingNames := make(map[string]bool)
		for _, r := range matchingRunners {
			matchingNames[r.Name] = true
			matchingNames[r.ID] = true
		}
		for _, j := range m.JobQueue {
			if matchingNames[j.RunnerName] || matchingNames[j.RunnerID] {
				assignedJobs = append(assignedJobs, j)
			}
		}
		if len(assignedJobs) > 0 {
			sb.WriteString("\n")
			sb.WriteString(lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(fmt.Sprintf(" QUEUED / RUNNING JOBS ON [%s] RUNNERS", activeTag)) + "\n")
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

		// If a run is focused, show summary of all jobs in that run
		if m.FocusedRunID != 0 {
			// Find all jobs belonging to the focused run
			var runJobs []*jobs.JobItem
			var runHeaderJob *jobs.JobItem
			for _, j := range m.JobQueue {
				if j.RunID == m.FocusedRunID {
					if j.IsRunHeader {
						runHeaderJob = j
					} else {
						runJobs = append(runJobs, j)
					}
				}
			}

			// Use run header for metadata if available, otherwise use first job
			metaJob := job
			if runHeaderJob != nil {
				metaJob = runHeaderJob
			} else if len(runJobs) > 0 {
				metaJob = runJobs[0]
			}

			// Header with run info
			runToken, _ := parseJobHierarchy(metaJob.Name, metaJob.Repo)
			runURL := fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d", m.TargetOrg, metaJob.Repo, metaJob.RunID)
			runLink := Hyperlink(lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(runToken), runURL)

			sb.WriteString(fmt.Sprintf(" %s FOCUSED RUN: %s\n\n", iconQueue, runLink))

			// Trigger info
			triggerStr := "push"
			if metaJob.Event != "" {
				triggerStr = metaJob.Event
			}
			if metaJob.PRNumber != 0 {
				prLabel := fmt.Sprintf("PR #%d", metaJob.PRNumber)
				prURL := metaJob.PRURL
				if prURL == "" {
					prURL = fmt.Sprintf("https://github.com/%s/%s/pull/%d", m.TargetOrg, metaJob.Repo, metaJob.PRNumber)
				}
				triggerStr = fmt.Sprintf("%s (%s)", Hyperlink(prLabel, prURL), metaJob.Event)
			} else if metaJob.Branch != "" {
				branchURL := fmt.Sprintf("https://github.com/%s/%s/tree/%s", m.TargetOrg, metaJob.Repo, metaJob.Branch)
				triggerStr = fmt.Sprintf("%s on %s", triggerStr, Hyperlink(metaJob.Branch, branchURL))
			}

			sb.WriteString(fmt.Sprintf(" %s  %s\n",
				lipgloss.NewStyle().Bold(true).Foreground(colorMuted).Width(10).Render("Repo:"),
				lipgloss.NewStyle().Foreground(colorPrimary).Render(metaJob.Repo),
			))
			sb.WriteString(fmt.Sprintf(" %s  %s\n",
				lipgloss.NewStyle().Bold(true).Foreground(colorMuted).Width(10).Render("Trigger:"),
				lipgloss.NewStyle().Foreground(colorYellow).Bold(true).Render(triggerStr),
			))
			sb.WriteString(fmt.Sprintf(" %s  %d jobs\n\n",
				lipgloss.NewStyle().Bold(true).Foreground(colorMuted).Width(10).Render("Total:"),
				len(runJobs),
			))

			// Summary table of all jobs in this run
			sb.WriteString(lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(" RUN SUMMARY") + "\n")
			sb.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(
				fmt.Sprintf(" %s", strings.Repeat("─", m.Viewport.Width-2)),
			) + "\n")

			if len(runJobs) == 0 {
				sb.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render("  No jobs found for this run.") + "\n")
			} else {
				// Calculate status counts
				statusCounts := make(map[jobs.JobStatus]int)
				for _, j := range runJobs {
					statusCounts[j.Status]++
				}

				// Status summary line
				var statusParts []string
				if statusCounts[jobs.JobRunning] > 0 {
					statusParts = append(statusParts, lipgloss.NewStyle().Foreground(colorSecondary).Bold(true).Render(fmt.Sprintf("%d running", statusCounts[jobs.JobRunning])))
				}
				if statusCounts[jobs.JobQueued] > 0 {
					statusParts = append(statusParts, lipgloss.NewStyle().Foreground(colorYellow).Bold(true).Render(fmt.Sprintf("%d queued", statusCounts[jobs.JobQueued])))
				}
				if statusCounts[jobs.JobPassed] > 0 {
					statusParts = append(statusParts, lipgloss.NewStyle().Foreground(colorGreen).Bold(true).Render(fmt.Sprintf("%d passed", statusCounts[jobs.JobPassed])))
				}
				if statusCounts[jobs.JobFailed] > 0 {
					statusParts = append(statusParts, lipgloss.NewStyle().Foreground(colorRed).Bold(true).Render(fmt.Sprintf("%d failed", statusCounts[jobs.JobFailed])))
				}
				if statusCounts[jobs.JobCancelled] > 0 {
					statusParts = append(statusParts, lipgloss.NewStyle().Foreground(colorMuted).Bold(true).Render(fmt.Sprintf("%d cancelled", statusCounts[jobs.JobCancelled])))
				}
				sb.WriteString(" " + strings.Join(statusParts, "  |  ") + "\n\n")

				// Individual job rows
				nameMaxLen := m.Viewport.Width - 40
				if nameMaxLen < 15 {
					nameMaxLen = 15
				}

				for idx, j := range runJobs {
					var jColor lipgloss.Color
					var jGlyph string
					switch j.Status {
					case jobs.JobRunning:
						jColor, jGlyph = colorSecondary, "⚡"
					case jobs.JobPassed:
						jColor, jGlyph = colorGreen, "󰄬"
					case jobs.JobFailed:
						jColor, jGlyph = colorRed, "󰅙"
					case jobs.JobCancelled:
						jColor, jGlyph = colorMuted, "⊘"
					default:
						jColor, jGlyph = colorYellow, "⏳"
					}

					jLink := Hyperlink(lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(j.ID), fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d", m.TargetOrg, j.Repo, j.RunID))
					_, jobToken := parseJobHierarchy(j.Name, j.Repo)
					jNameTrunc := truncateString(jobToken, nameMaxLen)

					runnerStr := j.RunnerName
					if runnerStr == "" {
						runnerStr = "awaiting"
					}
					runnerStr = truncateString(runnerStr, 20)

					treeConnector := "├─"
					if idx == len(runJobs)-1 {
						treeConnector = "└─"
					}

					sb.WriteString(fmt.Sprintf("  %s %s  %s  %s  %s%s  %s\n",
						treeConnector,
						lipgloss.NewStyle().Foreground(jColor).Render(jGlyph+" "+string(j.Status)),
						jLink,
						lipgloss.NewStyle().Foreground(colorSecondary).Render(jNameTrunc),
						lipgloss.NewStyle().Foreground(colorMuted).Render(iconRunner+" "),
						lipgloss.NewStyle().Foreground(colorMuted).Render(runnerStr),
						lipgloss.NewStyle().Foreground(colorMuted).Render(j.Duration),
					))
				}
			}

			sb.WriteString("\n" + lipgloss.NewStyle().Foreground(colorMuted).Render(" Press Enter or Esc to unfocus") + "\n")
		} else {
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

			triggerStr := "push"
			if job.Event != "" {
				triggerStr = job.Event
			}
			if job.PRNumber != 0 {
				prLabel := fmt.Sprintf("PR #%d", job.PRNumber)
				prURL := job.PRURL
				if prURL == "" {
					prURL = fmt.Sprintf("https://github.com/%s/%s/pull/%d", m.TargetOrg, job.Repo, job.PRNumber)
				}
				triggerStr = fmt.Sprintf("%s (%s)", Hyperlink(prLabel, prURL), job.Event)
			} else if job.Branch != "" {
				branchURL := fmt.Sprintf("https://github.com/%s/%s/tree/%s", m.TargetOrg, job.Repo, job.Branch)
				triggerStr = fmt.Sprintf("%s on %s", triggerStr, Hyperlink(job.Branch, branchURL))
			}

			sb.WriteString(fmt.Sprintf(" %s %s  |  %s  |  %s %s  |  Duration: %s\n\n",
				iconQueue,
				jobIDLink,
				statusBadge,
				iconFolder,
				lipgloss.NewStyle().Foreground(colorPrimary).Render(job.Repo),
				job.Duration,
			))

			sb.WriteString(fmt.Sprintf(" %s  %s\n",
				lipgloss.NewStyle().Bold(true).Foreground(colorMuted).Width(10).Render("Run:"),
				lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(runToken),
			))
			sb.WriteString(fmt.Sprintf(" %s  %s\n",
				lipgloss.NewStyle().Bold(true).Foreground(colorMuted).Width(10).Render("Trigger:"),
				lipgloss.NewStyle().Foreground(colorYellow).Bold(true).Render(triggerStr),
			))
			sb.WriteString(fmt.Sprintf(" %s  %s\n",
				lipgloss.NewStyle().Bold(true).Foreground(colorMuted).Width(10).Render("Job:"),
				lipgloss.NewStyle().Foreground(colorSecondary).Bold(true).Render(jobToken),
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
		}

	case FocusRepos:
		if len(m.Repos) == 0 || m.SelectedIndex >= len(m.Repos) {
			if m.IsOrgSyncing {
				m.Viewport.SetContent(fmt.Sprintf(" %s Fetching GitHub repositories...", m.Spinner.View()))
			} else {
				m.Viewport.SetContent(lipgloss.NewStyle().Foreground(colorMuted).Render(" No repositories found. Select a repo from the left pane."))
			}
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

			localBranches := item.BranchDetails.GetLocalBranches()
			sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorBlue).Render(" Local Branches:") + "\n")
			if len(localBranches) == 0 {
				sb.WriteString("  (None found)\n")
			} else {
				for _, b := range localBranches {
					sb.WriteString(fmt.Sprintf("  %s %s\n", iconBranch, b))
				}
			}

			remoteBranches := item.BranchDetails.GetRemoteBranches()
			sb.WriteString("\n" + lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Render(" Remote Branches:") + "\n")
			if len(remoteBranches) == 0 {
				sb.WriteString("  (None found)\n")
			} else {
				for _, b := range remoteBranches {
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
				spinnerStr = fmt.Sprintf("  %s %s", cellStatusIconStyle.Render(m.Spinner.View()), lipgloss.NewStyle().Foreground(colorMuted).Render("Updating..."))
			}
			sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Render(fmt.Sprintf("⊙ OPEN ISSUES (%d)", item.OpenIssuesCount)) + spinnerStr + "\n\n")

			if len(item.IssuesList) == 0 && item.IsLoadingIssues {
				sb.WriteString(fmt.Sprintf("  %s Loading open issues from GitHub...\n", cellStatusIconStyle.Render(m.Spinner.View())))
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
				spinnerStr = fmt.Sprintf("  %s %s", cellStatusIconStyle.Render(m.Spinner.View()), lipgloss.NewStyle().Foreground(colorMuted).Render("Updating..."))
			}
			sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Render(fmt.Sprintf("󰏫 OPEN PULL REQUESTS (%d)", item.OpenPRsCount)) + spinnerStr + "\n\n")

			if len(item.PRsList) == 0 && item.IsLoadingPRs {
				sb.WriteString(fmt.Sprintf("  %s Loading open pull requests from GitHub...\n", cellStatusIconStyle.Render(m.Spinner.View())))
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
	t1 := "[1 Logs]"
	t2 := "[2 Branches & Worktrees]"
	t3 := "[3 Issues]"
	t4 := "[4 PRs]"

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

	line = reTimestamp.ReplaceAllStringFunc(line, func(m string) string {
		return lipgloss.NewStyle().Foreground(colorMuted).Render(m)
	})

	line = reCmd.ReplaceAllStringFunc(line, func(m string) string {
		return lipgloss.NewStyle().Foreground(colorSecondary).Bold(true).Render(m)
	})

	line = reURL.ReplaceAllStringFunc(line, func(m string) string {
		return lipgloss.NewStyle().Foreground(colorBlue).Underline(true).Render(m)
	})

	line = reQuoted.ReplaceAllStringFunc(line, func(m string) string {
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
	headerStr := lipgloss.NewStyle().MaxWidth(m.Width).Render(headerText)
	headerLines := strings.Split(headerStr, "\n")
	header := headerLines[0] + "\n"

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
	// Total output lines = 1 (header\n) + mainView lines + 1 (footer) = 1 + (rightBoxHeight + 2) + 1
	// We need: 1 + rightBoxHeight + 2 + 1 = m.Height
	//   => rightBoxHeight = m.Height - 4
	//   => totalInner = rightBoxHeight - 4 = m.Height - 8
	//
	halfWidth := m.Width / 2
	leftWidth := halfWidth - 1
	rightWidth := m.Width - leftWidth - 2

	// Inner content width: border (1) + padding (1) on each side = 4 chars overhead
	paneInnerWidth := leftWidth - 4
	if paneInnerWidth < 30 {
		paneInnerWidth = 30
	}

	rightBoxHeight := m.Height - 4
	if rightBoxHeight < 12 {
		rightBoxHeight = 12
	}

	// totalInner is the sum of content heights for the 3 left boxes
	totalInner := rightBoxHeight - 4
	if totalInner < 8 {
		totalInner = 8
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
	availRepoW := paneInnerWidth - 14
	if availRepoW < 38 {
		availRepoW = 38
	}
	repoNameW := (availRepoW * 45) / 100
	if repoNameW < 18 {
		repoNameW = 18
	}
	branchW := availRepoW - repoNameW
	if branchW < 20 {
		branchW = 20
	} else if branchW > 35 {
		branchW = 35
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
		repoLines = append(repoLines, clipLine(cellStatusIconStyle.Render(m.Spinner.View())+" Fetching GitHub repositories..."))
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
			if !item.HasLoadedCounts && !item.HasLoadedPRs {
				prsCell = cellPRsStyle.Foreground(colorMuted).Render("?")
			} else if item.OpenPRsCount > 0 {
				prsCell = cellPRsStyle.Foreground(colorYellow).Bold(true).Render(fmt.Sprintf("%d", item.OpenPRsCount))
			} else {
				prsCell = cellPRsStyle.Foreground(colorMuted).Render("—")
			}

			var issuesCell string
			if !item.HasLoadedCounts && !item.HasLoadedIssues {
				issuesCell = cellIssuesStyle.Foreground(colorMuted).Render("?")
			} else if item.OpenIssuesCount > 0 {
				issuesCell = cellIssuesStyle.Foreground(colorBlue).Bold(true).Render(fmt.Sprintf("%d", item.OpenIssuesCount))
			} else {
				issuesCell = cellIssuesStyle.Foreground(colorMuted).Render("—")
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

	// ------------------ PANEL 2: REGISTERED RUNNERS (TAG BROWSER) ------------------
	var runnerLines []string
	tags := m.getAvailableTags()
	if m.SelectedTagIndex >= len(tags) {
		m.SelectedTagIndex = 0
	}
	activeTag := tags[m.SelectedTagIndex]

	tagTitle := "REGISTERED RUNNERS"
	if activeTag != "ALL" {
		tagTitle = fmt.Sprintf("REGISTERED RUNNERS (%s)", activeTag)
	}

	runnerHeader := fmt.Sprintf(" %s %s", iconRunner, lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Render(tagTitle))
	runnerLines = append(runnerLines, clipLine(runnerHeader))
	runnerLines = append(runnerLines, clipLine(lipgloss.NewStyle().Foreground(colorMuted).Render(strings.Repeat("─", paneInnerWidth))))

	if m.IsRunnersLoading && len(m.Runners) == 0 {
		runnerLines = append(runnerLines, clipLine(fmt.Sprintf(" %s Fetching registered runners...", cellStatusIconStyle.Render(m.Spinner.View()))))
	}

	var runningNames, idleNames, offlineNames []string
	for _, r := range m.Runners {
		match := false
		if activeTag == "ALL" {
			match = true
		} else {
			for _, tag := range r.Tags {
				if tag == activeTag {
					match = true
					break
				}
			}
		}
		if !match {
			continue
		}

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

	if !m.IsRunnersLoading && len(runningNames) == 0 && len(idleNames) == 0 && len(offlineNames) == 0 {
		runnerLines = append(runnerLines, clipLine(lipgloss.NewStyle().Foreground(colorMuted).Render(" No registered runners found for org.")))
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
	runningCount := 0
	queuedCount := 0
	for _, j := range m.JobQueue {
		if j.Status == jobs.JobRunning {
			runningCount++
		} else if j.Status == jobs.JobQueued {
			queuedCount++
		}
	}
	countsStr := ""
	if runningCount > 0 || queuedCount > 0 {
		countsStr = fmt.Sprintf(" (%d running, %d queued)", runningCount, queuedCount)
	}
	jobHeaderSpinner := ""
	if m.IsJobQueueLoading || m.IsOrgSyncing {
		jobHeaderSpinner = " " + m.Spinner.View()
	}
	jobHeader := fmt.Sprintf(" %s %s%s%s", iconQueue, lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render("OVERALL JOB QUEUE"), jobHeaderSpinner, lipgloss.NewStyle().Foreground(colorSecondary).Render(countsStr))
	jobsLines = append(jobsLines, jobHeader)
	jobsLines = append(jobsLines, lipgloss.NewStyle().Foreground(colorMuted).Render(strings.Repeat("─", paneInnerWidth)))

	if len(m.JobQueue) == 0 {
		if m.IsJobQueueLoading || m.IsOrgSyncing {
			jobsLines = append(jobsLines, clipLine(fmt.Sprintf(" %s Fetching active workflow jobs...", cellStatusIconStyle.Render(m.Spinner.View()))))
		} else {
			jobsLines = append(jobsLines, clipLine(lipgloss.NewStyle().Foreground(colorMuted).Render(" No queued or running workflow jobs.")))
		}
	}

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

	renderedRuns := make(map[int64]bool)

	for i := jobStartIdx; i < jobEndIdx; i++ {
		j := m.JobQueue[i]
		runToken, jobToken := parseJobHierarchy(j.Name, j.Repo)

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

		// If this job is part of a multi-job run and we haven't rendered the parent run header yet:
		if runCounts[j.RunID] > 1 && !renderedRuns[j.RunID] {
			renderedRuns[j.RunID] = true
			runTitleText := runToken
			if j.RunID != 0 {
				runURL := fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d", m.TargetOrg, j.Repo, j.RunID)
				runTitleText = Hyperlink(lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(runToken), runURL)
			} else {
				runTitleText = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(runToken)
			}

			// Format Trigger badge with hyperlink
			triggerBadge := ""
			if j.PRNumber != 0 {
				prLabel := fmt.Sprintf("PR #%d", j.PRNumber)
				if j.PRTitle != "" {
					prLabel = fmt.Sprintf("PR #%d: %s", j.PRNumber, j.PRTitle)
				}
				prURL := j.PRURL
				if prURL == "" {
					prURL = fmt.Sprintf("https://github.com/%s/%s/pull/%d", m.TargetOrg, j.Repo, j.PRNumber)
				}
				triggerBadge = lipgloss.NewStyle().Foreground(colorYellow).Bold(true).Render(" (" + Hyperlink(prLabel, prURL) + ")")
			} else if j.Event != "" {
				eventText := fmt.Sprintf("(%s)", j.Event)
				if j.Branch != "" {
					branchURL := fmt.Sprintf("https://github.com/%s/%s/tree/%s", m.TargetOrg, j.Repo, j.Branch)
					eventText = fmt.Sprintf("(%s on %s)", j.Event, Hyperlink(j.Branch, branchURL))
				}
				triggerBadge = " " + lipgloss.NewStyle().Foreground(colorMuted).Render(eventText)
			}

			runHeaderLine := fmt.Sprintf("  %s %s%s", stBadge, runTitleText, triggerBadge)
			jobsLines = append(jobsLines, clipLine(runHeaderLine))
		}

		runnerStr := j.RunnerName
		if runnerStr == "" {
			runnerStr = "awaiting"
		}
		runnerW := len(runnerStr) + 2 // include "→ " prefix
		if runnerW < 10 {
			runnerW = 10
		}
		if runnerW > 28 {
			runnerW = 28
		}
		runnerAssigned := lipgloss.NewStyle().Foreground(colorMuted).Width(runnerW).Render("→ " + truncateString(runnerStr, runnerW-2))

		var line string
		if runCounts[j.RunID] > 1 {
			// Child matrix job row
			indices := runIndices[j.RunID]
			treeConnector := "├─ "
			if indices[len(indices)-1] == i {
				treeConnector = "└─ "
			}

			nameW := paneInnerWidth - 4 - 5 - runnerW - 1
			if nameW < 10 {
				nameW = 10
			}
			jobStrText := lipgloss.NewStyle().Foreground(colorSecondary).Width(nameW).Render(truncateString(jobToken, nameW))
			if j.RunID != 0 {
				jobURL := fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d", m.TargetOrg, j.Repo, j.RunID)
				if j.GHJobID != 0 {
					jobURL = fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d/job/%d", m.TargetOrg, j.Repo, j.RunID, j.GHJobID)
				}
				jobStrText = Hyperlink(jobStrText, jobURL)
			}
			line = fmt.Sprintf("  %s%s %s", treeConnector, jobStrText, runnerAssigned)
		} else {
			// Single job run
			nameW := paneInnerWidth - 4 - 9 - 1 - runnerW - 1
			if nameW < 10 {
				nameW = 10
			}
			jobStrText := lipgloss.NewStyle().Width(nameW).Render(truncateString(jobToken, nameW))
			if j.RunID != 0 {
				jobURL := fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d", m.TargetOrg, j.Repo, j.RunID)
				if j.GHJobID != 0 {
					jobURL = fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d/job/%d", m.TargetOrg, j.Repo, j.RunID, j.GHJobID)
				}
				jobStrText = Hyperlink(jobStrText, jobURL)
			}
			line = fmt.Sprintf("%s %s %s", stBadge, jobStrText, runnerAssigned)
		}

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
	footerText := "[w/1/2/3] Focus  [↑/↓] Select  [←/→/h/l] Tabs  [j/k] Scroll  [r] Sync  [b] Branch  [p] Push/PR  [dd] Del Archived  [X] Prune  [c] Copy  [q] Quit"
	if m.ToastMsg != "" {
		msgText := m.ToastMsg
		if lipgloss.Width(msgText) > m.Width {
			msgText = truncateString(msgText, m.Width)
		}
		footerText = lipgloss.NewStyle().Foreground(colorGreen).Bold(true).Render(msgText)
	} else if lipgloss.Width(footerText) > m.Width {
		footerText = truncateString(footerText, m.Width)
	}

	footer := lipgloss.NewStyle().Foreground(colorMuted).MaxWidth(m.Width).Render(footerText)
	footerLines := strings.Split(footer, "\n")
	if len(footerLines) > 0 {
		footer = footerLines[0]
	}

	// header has trailing \n (1 newline)
	// mainView has (rightBoxHeight + 2) lines
	// footer is placed on its own line preceded by \n
	// strings.Split gives: 1 + (rightBoxHeight + 2) + 1 = rightBoxHeight + 4 = m.Height lines total ✓
	return header + mainView + "\n" + footer
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
	// No more fallback — returning a random running job would be misleading.
	return nil
}

func reconcileRunnerJobs(runners []*jobs.RunnerItem, queue []*jobs.JobItem, targetOrg string) []*jobs.JobItem {
	var realJobs []*jobs.JobItem
	for _, j := range queue {
		if j.RunID != 0 {
			realJobs = append(realJobs, j)
		}
	}

	result := make([]*jobs.JobItem, 0, len(queue))
	if len(realJobs) > 0 {
		result = append(result, realJobs...)
	} else {
		result = append(result, queue...)
	}

	for _, r := range runners {
		if r.Status == jobs.RunnerRunning {
			found := false
			for _, j := range result {
				if j.RunnerName == r.Name || j.RunnerID == r.ID || strings.EqualFold(j.RunnerName, r.Name) {
					found = true
					break
				}
				if j.Name != "" && r.CurrentJob != "" && r.CurrentJob != "-" && (strings.Contains(j.Name, r.CurrentJob) || strings.Contains(r.CurrentJob, j.Name)) {
					found = true
					if j.RunnerName == "" {
						j.RunnerName = r.Name
						j.RunnerID = r.ID
					}
					break
				}
			}
			if !found && len(realJobs) == 0 {
				jobTitle := r.CurrentJob
				if jobTitle == "" || jobTitle == "-" {
					jobTitle = fmt.Sprintf("%s active workflow job", r.Name)
				}
				jobID := r.CurrentJobID
				if jobID == "" || jobID == "-" {
					jobID = fmt.Sprintf("#%s", strings.TrimPrefix(r.ID, "runner-"))
				}
				result = append(result, &jobs.JobItem{
					ID:         jobID,
					Name:       jobTitle,
					Repo:       "", // unknown — synthetic entry; URL construction guarded by RunID==0
					Status:     jobs.JobRunning,
					RunnerName: r.Name,
					RunnerID:   r.ID,
					Duration:   "active",
				})
			}
		}
	}
	return result
}

func (m Model) getAvailableTags() []string {
	tagsSet := make(map[string]bool)
	for _, r := range m.Runners {
		for _, tag := range r.Tags {
			if tag != "" {
				tagsSet[tag] = true
			}
		}
	}
	tags := []string{"ALL"}
	var keys []string
	for k := range tagsSet {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return append(tags, keys...)
}

func (m Model) getMatchingRunners() []*jobs.RunnerItem {
	tags := m.getAvailableTags()
	tagIdx := m.SelectedTagIndex
	if tagIdx >= len(tags) {
		tagIdx = 0
	}
	activeTag := tags[tagIdx]

	var matching []*jobs.RunnerItem
	for _, r := range m.Runners {
		if activeTag == "ALL" {
			matching = append(matching, r)
		} else {
			for _, tag := range r.Tags {
				if tag == activeTag {
					matching = append(matching, r)
					break
				}
			}
		}
	}
	return matching
}
