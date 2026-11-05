package tui

import (
	"os"
	"strings"

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
	light   bool

	primary, secondary, green, yellow, red, blue, muted, text, surface, line lipgloss.TerminalColor

	brand, owner, dim, body, strong, section, divider    lipgloss.Style
	tabActive, tabInactive, subTabActive, subTabInactive lipgloss.Style
	crumb, crumbCurrent, cursor                          lipgloss.Style
	rowSelected, rowDetail                               lipgloss.Style
	tableHeader, tileLabel                               lipgloss.Style
	key, keyDesc, keySep, keyPrimary                     lipgloss.Style
	toastInfo, toastError, warning, link                 lipgloss.Style
	pr, issue, branchHeading, worktreeHeading            lipgloss.Style
	logTimestamp, logCommand, logQuoted                  lipgloss.Style
}

// noColorRequested follows https://no-color.org: any non-empty NO_COLOR value
// disables colour while keeping bold, underline and reverse attributes.
func noColorRequested() bool { return os.Getenv("NO_COLOR") != "" }

// lightRequested chooses a light-terminal palette. FRESHEN_LIGHT wins; then a
// COLORFGBG background of 7 or 15. The default is dark because the app does not
// paint its own background and most terminals in this workflow are dark.
func lightRequested() bool {
	switch os.Getenv("FRESHEN_LIGHT") {
	case "1":
		return true
	case "0":
		return false
	}
	parts := strings.Split(os.Getenv("COLORFGBG"), ";")
	if len(parts) >= 2 {
		bg := parts[len(parts)-1]
		return bg == "15" || bg == "7"
	}
	return false
}

var th = newTheme(noColorRequested())

// termenv.ANSI: attributes (bold, reverse, underline) without colour. Lip
// Gloss otherwise selects the Ascii profile when NO_COLOR is set, which drops
// the reverse-video selection too; NO_COLOR asks for no colour, not no styling.
const ansiProfile = 1

func init() {
	if noColorRequested() {
		lipgloss.SetColorProfile(ansiProfile)
		th = newTheme(true)
	}
}

func newTheme(noColor bool) theme {
	return newThemeMode(noColor, lightRequested())
}

func newThemeMode(noColor, light bool) theme {
	c := func(hex string) lipgloss.TerminalColor {
		if noColor {
			return lipgloss.NoColor{}
		}
		return lipgloss.Color(hex)
	}
	primary, secondary, green := "#78B159", "#56BFA8", "#72C98B"
	yellow, red, blue := "#F59E0B", "#F07178", "#5B9BF8"
	muted, text, surface, line := "#8CA096", "#DCE7E0", "#223028", "#2E4038"
	if light {
		primary, secondary, green = "#276B2B", "#126B5C", "#1E7A3A"
		yellow, red, blue = "#8A5A00", "#B3261E", "#1F5FBF"
		muted, text, surface, line = "#4E6258", "#1B2A22", "#E4EEE7", "#C9D8CF"
	}
	t := theme{
		noColor:   noColor,
		light:     light,
		primary:   c(primary),
		secondary: c(secondary),
		green:     c(green),
		yellow:    c(yellow),
		red:       c(red),
		blue:      c(blue),
		muted:     c(muted),
		text:      c(text),
		surface:   c(surface),
		line:      c(line),
	}
	s := lipgloss.NewStyle
	t.brand = s().Bold(true).Foreground(t.primary)
	t.owner = s().Foreground(t.text)
	t.dim = s().Foreground(t.muted)
	t.body = s().Foreground(t.text)
	t.strong = s().Bold(true).Foreground(t.text)
	t.section = s().Bold(true).Foreground(t.secondary)
	t.divider = s().Foreground(t.line)
	t.tabActive = s().Bold(true).Foreground(t.text).Background(t.surface).Padding(0, 1)
	t.tabInactive = s().Foreground(t.muted).Padding(0, 1)
	t.subTabActive = s().Bold(true).Foreground(t.text).Underline(true).Padding(0, 1)
	t.subTabInactive = s().Foreground(t.muted).Padding(0, 1)
	t.crumb = s().Foreground(t.muted)
	t.crumbCurrent = s().Bold(true).Foreground(t.text)
	t.cursor = s().Bold(true).Foreground(t.primary)
	t.rowSelected = s().Foreground(t.text).Background(t.surface)
	t.rowDetail = s().Foreground(t.text).Background(t.surface)
	t.tableHeader = s().Bold(true).Foreground(t.muted)
	t.tileLabel = s().Bold(true).Foreground(t.secondary)
	if noColor {
		// Without colour a background highlight disappears; reverse video keeps
		// the selected row visible alongside the › cursor.
		t.tabActive = t.tabActive.Reverse(true)
		t.rowSelected = t.rowSelected.Reverse(true)
		t.rowDetail = t.rowDetail.Reverse(true)
	}
	t.key = s().Bold(true).Foreground(t.secondary)
	t.keyPrimary = s().Bold(true).Foreground(t.primary)
	t.keyDesc = s().Foreground(t.muted)
	t.keySep = s().Foreground(t.line)
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

// statusChip is the compact state capsule used in the context row. Colour is
// carried by the glyph and label, never by the background alone.
func (t theme) statusChip(k statusKind, label string) string {
	s := lipgloss.NewStyle().Bold(true).Foreground(t.kindColor(k)).Background(t.surface).Padding(0, 1)
	if t.noColor {
		s = s.Reverse(true)
	}
	return s.Render(statusGlyphs[k] + " " + label)
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
	// The bar stays inside the leaf/mint identity instead of Charm's default
	// purple gradient, which was the only off-palette colour in the frame.
	return progress.New(progress.WithGradient("#78B159", "#56BFA8"), progress.WithoutPercentage(), progress.WithColorProfile(lipgloss.ColorProfile()))
}

// progressFor colours a bar by the state it describes: a failed run must not
// show a reassuring green bar just because its jobs finished.
func (t theme) progressFor(k statusKind) progress.Model {
	if t.noColor {
		p := progress.New(progress.WithSolidFill(""), progress.WithoutPercentage())
		p.EmptyColor = ""
		return p
	}
	return progress.New(progress.WithSolidFill(t.progressFill(k)), progress.WithoutPercentage(), progress.WithColorProfile(lipgloss.ColorProfile()))
}

// progressFill is the bar colour for a state, chosen from the active palette.
func (t theme) progressFill(k statusKind) string {
	fill := "#8CA096"
	if t.light {
		fill = "#4E6258"
	}
	switch k {
	case kindSuccess:
		fill = "#78B159"
		if t.light {
			fill = "#1E7A3A"
		}
	case kindFailure:
		fill = "#F07178"
		if t.light {
			fill = "#B3261E"
		}
	case kindRunning:
		fill = "#5B9BF8"
		if t.light {
			fill = "#1F5FBF"
		}
	case kindQueued, kindWarning:
		fill = "#F59E0B"
		if t.light {
			fill = "#8A5A00"
		}
	}
	return fill
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
		return kindRunning, "busy"
	case jobs.RunnerIdle:
		return kindSuccess, "idle"
	case jobs.RunnerOffline:
		return kindFailure, "offline"
	case jobs.RunnerMaintenance:
		return kindSkipped, "maintenance"
	default:
		return kindUnknown, "unknown"
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
