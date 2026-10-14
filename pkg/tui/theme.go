package tui

import (
	"os"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/lipgloss"
	"github.com/seankoji-com/freshen/pkg/git"
	"github.com/seankoji-com/freshen/pkg/jobs"
)

// statusKind is the single vocabulary for result semantics. Every job, run,
// runner and repository state maps to one kind, and each kind renders as a
// colour plus a glyph plus a text label so meaning never depends on colour.
type statusKind int

const (
	kindUnknown statusKind = iota
	kindSuccess
	kindFailure
	kindRunning
	kindQueued
	kindSkipped
	kindWarning
)

// Glyphs are plain Unicode with neutral East Asian width, so they occupy one
// cell in every terminal and need no Nerd Font. A text label always follows.
var statusGlyphs = map[statusKind]string{
	kindUnknown: "?",
	kindSuccess: "✓",
	kindFailure: "✗",
	kindRunning: "↻",
	kindQueued:  "◌",
	kindSkipped: "⊘",
	kindWarning: "!",
}

// theme owns every colour and style the TUI renders. Render code reads styles
// from th rather than building ad-hoc lipgloss styles.
type theme struct {
	noColor bool

	primary, secondary, green, yellow, red, blue, muted, text, surface lipgloss.TerminalColor

	brand, owner, dim, body, strong, section, divider    lipgloss.Style
	tabActive, tabInactive, subTabActive, subTabInactive lipgloss.Style
	crumb, crumbCurrent, selected, cursor                lipgloss.Style
	key, keyDesc, keySep                                 lipgloss.Style
	toastInfo, toastError, warning, link                 lipgloss.Style
	pr, issue, branchHeading, worktreeHeading            lipgloss.Style
	logTimestamp, logCommand, logQuoted                  lipgloss.Style
}

// noColorRequested follows https://no-color.org: any non-empty NO_COLOR value
// disables colour while keeping bold, underline and reverse attributes.
func noColorRequested() bool { return os.Getenv("NO_COLOR") != "" }

var th = newTheme(noColorRequested())

func newTheme(noColor bool) theme {
	c := func(hex string) lipgloss.TerminalColor {
		if noColor {
			return lipgloss.NoColor{}
		}
		return lipgloss.Color(hex)
	}
	t := theme{
		noColor:   noColor,
		primary:   c("#78B159"), // Leaf green
		secondary: c("#56BFA8"), // Dew mint
		green:     c("#72C98B"), // Fresh green
		yellow:    c("#F59E0B"), // Warm amber
		red:       c("#F07178"), // Coral red
		blue:      c("#5B9BF8"), // Clear blue
		muted:     c("#7F9087"), // Muted sage
		text:      c("#DCE7E0"), // Frosted white
		surface:   c("#2A3A31"), // Deep leaf surface
	}
	s := lipgloss.NewStyle
	t.brand = s().Bold(true).Foreground(t.primary)
	t.owner = s().Foreground(t.text)
	t.dim = s().Foreground(t.muted)
	t.body = s().Foreground(t.text)
	t.strong = s().Bold(true).Foreground(t.text)
	t.section = s().Bold(true).Foreground(t.secondary)
	t.divider = s().Foreground(t.surface)
	t.tabActive = s().Bold(true).Foreground(t.text).Background(t.surface).Padding(0, 1)
	t.tabInactive = s().Foreground(t.muted).Padding(0, 1)
	t.subTabActive = s().Bold(true).Foreground(t.text).Underline(true).Padding(0, 1)
	t.subTabInactive = s().Foreground(t.muted).Padding(0, 1)
	t.crumb = s().Foreground(t.muted)
	t.crumbCurrent = s().Bold(true).Foreground(t.text)
	t.selected = s().Bold(true).Foreground(t.text).Background(t.surface)
	t.cursor = s().Bold(true).Foreground(t.primary)
	if noColor {
		// Without colour a background highlight disappears; reverse video keeps
		// the selected row visible alongside the › cursor.
		t.tabActive = t.tabActive.Reverse(true)
		t.selected = t.selected.Reverse(true)
	}
	t.key = s().Bold(true).Foreground(t.secondary)
	t.keyDesc = s().Foreground(t.muted)
	t.keySep = s().Foreground(t.surface)
	t.toastInfo = s().Foreground(t.text)
	t.toastError = s().Bold(true).Foreground(t.red)
	t.warning = s().Bold(true).Foreground(t.yellow)
	t.link = s().Foreground(t.blue).Underline(true)
	t.pr = s().Bold(true).Foreground(t.yellow)
	t.issue = s().Bold(true).Foreground(t.blue)
	t.branchHeading = s().Bold(true).Foreground(t.blue)
	t.worktreeHeading = s().Bold(true).Foreground(t.primary)
	t.logTimestamp = s().Foreground(t.muted)
	t.logCommand = s().Bold(true).Foreground(t.secondary)
	t.logQuoted = s().Bold(true).Foreground(t.yellow)
	return t
}

func (t theme) fg(c lipgloss.TerminalColor) lipgloss.Style { return lipgloss.NewStyle().Foreground(c) }

func (t theme) kindColor(k statusKind) lipgloss.TerminalColor {
	switch k {
	case kindSuccess:
		return t.green
	case kindFailure:
		return t.red
	case kindRunning:
		return t.blue
	case kindQueued, kindWarning:
		return t.yellow
	default:
		return t.muted
	}
}

// glyph renders only the coloured glyph; callers must place a text label next to it.
func (t theme) glyph(k statusKind) string {
	return t.fg(t.kindColor(k)).Bold(true).Render(statusGlyphs[k])
}

// status renders the glyph and label together in the kind's colour.
func (t theme) status(k statusKind, label string) string {
	return t.fg(t.kindColor(k)).Bold(true).Render(statusGlyphs[k] + " " + label)
}

// newProgress builds the shared progress bar. Bubbles' progress bar queries the
// terminal profile itself and ignores NO_COLOR, so it is pinned to the same
// environment-aware profile Lip Gloss uses, or to no colour at all.
func (t theme) newProgress() progress.Model {
	if t.noColor {
		p := progress.New(progress.WithSolidFill(""), progress.WithoutPercentage())
		p.EmptyColor = ""
		return p
	}
	return progress.New(progress.WithDefaultGradient(), progress.WithoutPercentage(), progress.WithColorProfile(lipgloss.ColorProfile()))
}

func jobKind(s jobs.JobStatus) statusKind {
	switch s {
	case jobs.JobPassed:
		return kindSuccess
	case jobs.JobFailed:
		return kindFailure
	case jobs.JobRunning:
		return kindRunning
	case jobs.JobQueued, jobs.JobWaiting:
		return kindQueued
	case jobs.JobCancelled, jobs.JobSkipped:
		return kindSkipped
	default:
		return kindUnknown
	}
}

func runnerKind(s jobs.RunnerStatus) (statusKind, string) {
	switch s {
	case jobs.RunnerRunning:
		return kindRunning, "BUSY"
	case jobs.RunnerIdle:
		return kindSuccess, "IDLE"
	case jobs.RunnerOffline:
		return kindFailure, "OFFLINE"
	case jobs.RunnerMaintenance:
		return kindSkipped, "MAINTENANCE"
	default:
		return kindUnknown, "UNKNOWN"
	}
}

func repoKind(s git.RepoStatus) (statusKind, string) {
	switch s {
	case git.StatusUpToDate:
		return kindSuccess, "up to date"
	case git.StatusUpdated:
		return kindSuccess, "updated"
	case git.StatusCloned:
		return kindSuccess, "cloned"
	case git.StatusStashedApplied:
		return kindSuccess, "stash reapplied"
	case git.StatusSwitchedDefault:
		return kindSuccess, "on default branch"
	case git.StatusRebased:
		return kindSuccess, "rebased"
	case git.StatusPRCreated:
		return kindSuccess, "PR created"
	case git.StatusError:
		return kindFailure, "error"
	case git.StatusRebaseConflict:
		return kindFailure, "rebase conflict"
	case git.StatusArchived:
		return kindSkipped, "archived"
	case git.StatusSkipped:
		return kindSkipped, "skipped"
	case git.StatusSyncing:
		return kindRunning, "syncing"
	default:
		return kindUnknown, "not synced"
	}
}
