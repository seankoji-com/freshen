package tui

import (
	"context"
	"regexp"
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

// --- Sync and Refresh Intervals ---
const (
	repoTickInterval      = 5 * time.Minute
	runnerJobTickInterval = 10 * time.Second
	jobQueueTickInterval  = 20 * time.Second

	// pollBackoffCap bounds the exponential backoff applied to the runner and
	// job-queue polls after consecutive fetch failures, so a tripped API quota
	// isn't pinned at zero overnight by a fixed-interval poll.
	pollBackoffCap = 5 * time.Minute
)

// Fetch-source keys for the per-poller backoff counters. The runner poll and
// the job-queue poll fail independently, so each tracks its own streak — a
// healthy fetch on one must not reset the backoff the other has earned.
const (
	fetchSourceRunners  = "runners"
	fetchSourceJobQueue = "jobQueue"
)

// backoffInterval doubles base once per consecutive failure, capped at
// pollBackoffCap. With no failures recorded it returns base unchanged.
func backoffInterval(base time.Duration, consecutiveErrors int) time.Duration {
	d := base
	for i := 0; i < consecutiveErrors && d < pollBackoffCap; i++ {
		d *= 2
	}
	if d > pollBackoffCap {
		d = pollBackoffCap
	}
	return d
}

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

// --- Messages for Bubble Tea Update Loop ---

// syncFinishedMsg reports that a sync stream has closed. bulk distinguishes the
// all-repositories sync, which drives the IsSyncing banner, from a single-repo
// re-sync that must not clear it.
type syncFinishedMsg struct{ bulk bool }

// repoSyncMsg carries one snapshot from a background sync, along with the stream
// it came from so the update loop can wait for the next one.
type repoSyncMsg struct {
	repo      *git.RepoItem
	snapshots <-chan *git.RepoItem
	bulk      bool
}

type repoTickMsg time.Time

type runnerJobTickMsg time.Time

type jobQueueTickMsg time.Time

func repoTickCmd() tea.Cmd {
	return tea.Every(repoTickInterval, func(t time.Time) tea.Msg {
		return repoTickMsg(t)
	})
}

// runnerJobTickCmd schedules the next runner poll after d, which callers widen
// via backoffInterval once fetches start failing.
func runnerJobTickCmd(d time.Duration) tea.Cmd {
	return tea.Every(d, func(t time.Time) tea.Msg {
		return runnerJobTickMsg(t)
	})
}

// jobQueueTickCmd drives the job-queue refresh on its own, slower cadence.
// GitHub has no org-wide workflow-runs endpoint, so each poll costs one API
// call per tracked repository; at 20s a ~20-repo org stays well inside the
// 5,000 requests/hour limit, while the runner panel keeps its 10s refresh.
// d is widened via backoffInterval once fetches start failing.
func jobQueueTickCmd(d time.Duration) tea.Cmd {
	return tea.Every(d, func(t time.Time) tea.Msg {
		return jobQueueTickMsg(t)
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

// pushFinishedMsg carries the result of a background commit/push/PR run back
// to the update goroutine, which is the only writer of m.Repos. The worker
// mutates a private clone and never touches the live item; repo is that clone,
// folded onto the live item via applyRepoSnapshot so every field the push
// wrote (Status, StatusMsg, Logs, DraftPRURL, ExistingPRURL, branch state)
// survives the handoff.
type pushFinishedMsg struct {
	repoName string
	repo     *git.RepoItem
	err      error
}

// --- Bubble Tea Model ---

type Model struct {
	TargetDir              string
	TargetOrg              string
	Concurrency            int
	Repos                  []*git.RepoItem
	Runners                []*jobs.RunnerItem
	JobQueue               []*jobs.JobItem
	SelectedIndex          int
	SelectedRunnerIndex    int
	SelectedJobIndex       int
	SelectedTagIndex       int
	ActiveFocus            FocusType
	ActiveTab              TabType
	IsSyncing              bool
	IsOrgSyncing           bool
	IsJobQueueLoading      bool
	IsRunnersLoading       bool
	TotalCount             int
	ToastMsg               string
	FocusedRunID           int64 // When non-zero, a specific workflow run is focused
	ToastPriority          int   // higher priority overrides lower; 0 = none, 1 = info, 2 = error
	ConsecutiveErrors      map[string]int
	RunnerFetchFailed      bool
	RunnerPermissionDenied bool
	JobQueueFetchFailed    bool

	// pendingDeletePath holds the Path of the archived repo awaiting a second
	// 'd' press to confirm deletion; "" means no pending delete. It is keyed
	// on Path rather than a Repos index because a background refresh can
	// rebuild and re-sort m.Repos between the two presses, which would leave
	// an index pointing at a different repo than the one just confirmed.
	pendingDeletePath string

	Spinner     spinner.Model
	ProgressBar progress.Model
	Viewport    viewport.Model
	Width       int
	Height      int

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
func NewModel(targetDir, targetOrg string, concurrency int, ctx context.Context, cancel context.CancelFunc, bgWG *sync.WaitGroup) Model {
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
		Concurrency:         concurrency,
		Repos:               make([]*git.RepoItem, 0),
		Runners:             make([]*jobs.RunnerItem, 0),
		JobQueue:            make([]*jobs.JobItem, 0),
		SelectedIndex:       0,
		SelectedRunnerIndex: 0,
		SelectedJobIndex:    0,
		pendingDeletePath:   "",
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
	if m.TargetOrg == "" {
		return tea.Batch(m.Spinner.Tick, m.loadOrgReposCmd(true), repoTickCmd())
	}
	return tea.Batch(
		m.Spinner.Tick,
		m.loadOrgReposCmd(true),
		m.loadRunnersCmd(),
		m.loadJobQueueCmd(),
		repoTickCmd(),
		runnerJobTickCmd(runnerJobTickInterval),
		jobQueueTickCmd(jobQueueTickInterval),
	)
}
