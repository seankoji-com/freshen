package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/x/ansi"
	"github.com/seankoji-com/freshen/pkg/git"
	"github.com/seankoji-com/freshen/pkg/jobs"
)

// Frame layout, top to bottom: header, screen tabs, breadcrumb, body,
// status row and key hints. Every row is clipped to the terminal width.
const (
	headerRows = 3
	footerRows = 2
)

// minFrameWidth and minFrameHeight are the smallest terminal that renders the
// full chrome; anything smaller shows a resize notice instead.
const (
	minFrameWidth  = 30
	minFrameHeight = 10
)

type screenTab struct {
	focus      FocusType
	key, label string
	start, end int // terminal columns [start, end) on the tab row
}

var tabNames = map[FocusType][3]string{
	FocusOverview: {"Overview", "Org", "O"},
	FocusRepos:    {"Repositories", "Repos", "R"},
	FocusJobs:     {"Actions", "CI", "A"},
	FocusRunners:  {"Runners", "Run", "N"},
	FocusConcerns: {"Concerns", "Concerns", "C"},
}

// tabBadge is a short count for a tab, or "" when there is nothing to flag.
// It is plain text so the tab row's width does not depend on styling.
func (m Model) tabBadge(f FocusType, s orgSummary) string {
	switch f {
	case FocusRepos:
		if s.repoProblems > 0 {
			return fmt.Sprintf("%s%d", statusGlyphs[kindFailure], s.repoProblems)
		}
	case FocusJobs:
		if s.failedRuns > 0 {
			return fmt.Sprintf("%s%d", statusGlyphs[kindFailure], s.failedRuns)
		}
		if s.runningRuns > 0 {
			return fmt.Sprintf("%s%d", statusGlyphs[kindRunning], s.runningRuns)
		}
	case FocusRunners:
		if s.runnerOffline > 0 && !m.RunnerPermissionDenied {
			return fmt.Sprintf("%s%d", statusGlyphs[kindFailure], s.runnerOffline)
		}
		if s.runnerBusy > 0 {
			return fmt.Sprintf("%s%d", statusGlyphs[kindRunning], s.runnerBusy)
		}
	case FocusConcerns:
		if s.concernRepos > 0 {
			return fmt.Sprint(s.concernRepos)
		}
	}
	return ""
}

// screenTabs lays out the tab row. It tries full names with count badges,
// then progressively shorter labels, and uses the first that fits. Rendering
// and mouse hit testing both use these spans.
func (m Model) screenTabs() []screenTab {
	summary := m.buildOrgSummary()
	for _, variant := range []struct {
		name   int
		badges bool
	}{{0, true}, {0, false}, {1, false}, {2, false}} {
		tabs := make([]screenTab, 0, len(screenOrder))
		x := 1
		for i, f := range screenOrder {
			label := fmt.Sprintf("%d %s", i, tabNames[f][variant.name])
			if variant.badges {
				if badge := m.tabBadge(f, summary); badge != "" {
					label += " " + badge
				}
			}
			w := ansi.StringWidth(label) + 2 // tab style pads one cell each side
			tabs = append(tabs, screenTab{focus: f, key: fmt.Sprint(i), label: label, start: x, end: x + w})
			x += w + 1
		}
		if x-1 <= m.Width || variant.name == 2 {
			return tabs
		}
	}
	return nil
}

func (m Model) tabRow() string {
	var sb strings.Builder
	sb.WriteString(" ")
	for i, tab := range m.screenTabs() {
		if i > 0 {
			sb.WriteString(" ")
		}
		style := th.tabInactive
		if tab.focus == m.ActiveFocus {
			style = th.tabActive
		}
		sb.WriteString(style.Render(tab.label))
	}
	return sb.String()
}

// compactAgo renders an age at a glance: 8s, 4m, 2h 5m, 3d.
func compactAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(0, int(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func agoOr(t time.Time, loading bool) string {
	switch {
	case !t.IsZero():
		return compactAgo(time.Since(t)) + " ago"
	case loading:
		return "loading"
	default:
		return "not loaded yet"
	}
}

func updated(t time.Time, loading bool) string {
	if t.IsZero() {
		return agoOr(t, loading)
	}
	return "updated " + agoOr(t, loading)
}

// freshness says how old the data on this screen is and how often it renders
// anew, so stale numbers are never mistaken for live ones.
func (m Model) freshness() string {
	refreshing := ""
	switch m.ActiveFocus {
	case FocusOverview:
		parts := []string{"repos " + agoOr(m.LastOrgRefresh, m.IsOrgSyncing)}
		if m.TargetOrg != "" {
			parts = append(parts, "CI "+agoOr(m.LastActionsRefresh, m.IsJobQueueLoading), "runners "+agoOr(m.LastRunnersRefresh, m.IsRunnersLoading))
		}
		if m.IsOrgSyncing || m.IsJobQueueLoading || m.IsRunnersLoading {
			refreshing = strings.TrimSpace(m.Spinner.View())
		}
		return strings.TrimSpace(refreshing + " " + strings.Join(parts, " · "))
	case FocusRepos, FocusConcerns:
		if m.IsOrgSyncing {
			refreshing = strings.TrimSpace(m.Spinner.View())
		}
		return strings.TrimSpace(refreshing + " " + updated(m.LastOrgRefresh, m.IsOrgSyncing) + " · every " + compactAgo(repoTickInterval))
	case FocusJobs:
		if m.TargetOrg == "" {
			return ""
		}
		if m.IsJobQueueLoading || m.RunLoading || m.LogLoading != "" {
			refreshing = strings.TrimSpace(m.Spinner.View())
		}
		return strings.TrimSpace(refreshing + " " + updated(m.LastActionsRefresh, m.IsJobQueueLoading) + " · every " + compactAgo(m.actionsPollInterval()))
	case FocusRunners:
		if m.TargetOrg == "" {
			return ""
		}
		if m.IsRunnersLoading {
			refreshing = strings.TrimSpace(m.Spinner.View())
		}
		every := " · every " + compactAgo(runnerJobTickInterval)
		if m.RunnerPermissionDenied {
			every = " · observed via Actions"
		}
		return strings.TrimSpace(refreshing + " " + updated(m.LastRunnersRefresh, m.IsRunnersLoading) + every)
	}
	return ""
}

// joinEnds places right after left, flush to width, dropping right if both
// cannot fit.
func joinEnds(left, right string, width int) string {
	lw, rw := ansi.StringWidth(left), ansi.StringWidth(right)
	if right == "" || lw+rw+2 > width {
		return left
	}
	return left + strings.Repeat(" ", width-lw-rw) + right
}

func (m Model) headerRow() string {
	left := " " + th.brand.Render("freshen")
	owner := m.TargetOrg
	if owner == "" {
		owner = git.ShortenHomePath(m.TargetDir)
	}
	if owner != "" {
		left += "  " + th.owner.Render(owner)
	}
	if m.JobQueueFetchFailed {
		left += "  " + th.toastError.Render(statusGlyphs[kindFailure]+" Actions incomplete")
	}
	if full := th.dim.Render(m.freshness()) + " "; ansi.StringWidth(left)+ansi.StringWidth(full)+2 <= m.Width || !m.screenRefreshing() {
		return joinEnds(left, full, m.Width)
	}
	// Too narrow for the freshness text: keep a compact refresh indicator so an
	// in-flight refresh over cached rows is never invisible.
	spin := strings.TrimSpace(m.Spinner.View())
	for _, right := range []string{spin + " refreshing ", spin + " "} {
		if ansi.StringWidth(left)+ansi.StringWidth(right)+2 <= m.Width {
			return joinEnds(left, th.dim.Render(right), m.Width)
		}
	}
	right := th.dim.Render(spin + " ")
	return ansi.Truncate(left, max(0, m.Width-ansi.StringWidth(right)-1), "…") + " " + right
}

// screenRefreshing reports whether a refresh feeding the current screen is in
// flight; it matches the spinner conditions in freshness.
func (m Model) screenRefreshing() bool {
	switch m.ActiveFocus {
	case FocusOverview:
		return m.IsOrgSyncing || m.IsJobQueueLoading || m.IsRunnersLoading
	case FocusRepos, FocusConcerns:
		return m.IsOrgSyncing
	case FocusJobs:
		return m.TargetOrg != "" && (m.IsJobQueueLoading || m.RunLoading || m.LogLoading != "")
	case FocusRunners:
		return m.TargetOrg != "" && m.IsRunnersLoading
	}
	return false
}

func (m Model) screenName() string {
	return tabNames[m.ActiveFocus][0]
}

// breadcrumb is the drill-down path from the top-level screen to the current view.
func (m Model) breadcrumb() []string {
	crumbs := []string{m.screenName()}
	switch m.ActiveFocus {
	case FocusJobs:
		if m.OpenRun == nil {
			if m.QueueView {
				return append(crumbs, "Job queue")
			}
			return append(crumbs, []string{"Active runs", "Needs attention", "Recent runs"}[m.ActionsFilter])
		}
		crumbs = append(crumbs, runSummaryName(m.OpenRun))
		if m.OpenJobID != "" {
			name := "job " + m.OpenJobID
			if j := m.openJob(); j != nil {
				name = strings.TrimPrefix(j.Name, j.Repo+" / ")
			}
			crumbs = append(crumbs, name)
		}
	case FocusRepos, FocusConcerns:
		if m.Detail && m.SelectedIndex >= 0 && m.SelectedIndex < len(m.Repos) {
			crumbs = append(crumbs, m.Repos[m.SelectedIndex].Name)
		}
	case FocusRunners:
		if m.Detail {
			if rs := m.getMatchingRunners(); m.SelectedRunnerIndex < len(rs) {
				crumbs = append(crumbs, rs[m.SelectedRunnerIndex].Name)
			} else {
				crumbs = append(crumbs, "runner")
			}
		}
	}
	return crumbs
}

const crumbSep = " › "

// fitCrumbs shortens a breadcrumb to at most budget cells. The current
// (last) crumb is kept longest: ancestors are shortened first, then collapsed
// to "…", and only then is the current crumb itself truncated.
func fitCrumbs(crumbs []string, budget int) []string {
	width := func(cs []string) int { return ansi.StringWidth(strings.Join(cs, crumbSep)) }
	if len(crumbs) == 0 || width(crumbs) <= budget {
		return crumbs
	}
	out := append([]string(nil), crumbs...)
	for i := range out[:len(out)-1] {
		out[i] = ansi.Truncate(out[i], 14, "…")
	}
	if width(out) <= budget {
		return out
	}
	if len(out) > 2 {
		out = []string{out[0], "…", out[len(out)-1]}
	}
	if width(out) <= budget {
		return out
	}
	last := len(out) - 1
	prefix := width(out[:last]) + ansi.StringWidth(crumbSep)
	if last == 0 || budget-prefix < 8 {
		return []string{ansi.Truncate(out[last], max(1, budget), "…")}
	}
	out[last] = ansi.Truncate(out[last], budget-prefix, "…")
	return out
}

func (m Model) breadcrumbView(budget int) string {
	crumbs := fitCrumbs(m.breadcrumb(), budget)
	parts := make([]string, len(crumbs))
	for i, c := range crumbs {
		if i == len(crumbs)-1 {
			parts[i] = th.crumbCurrent.Render(c)
		} else {
			parts[i] = th.crumb.Render(c)
		}
	}
	return strings.Join(parts, th.crumb.Render(crumbSep))
}

// Context row geometry: one leading cell, the breadcrumb, a two-cell gap and
// a control. The text input renders its prompt, Width cells of text and one
// cursor cell.
const (
	contextGap       = 2
	searchChrome     = 3 // "/ " prompt plus the cursor cell
	minSearchWidth   = 8
	maxFilterCrumbsW = 24
)

// filterCrumbBudget is the breadcrumb's share of the row while the filter
// input is open; the input takes the rest.
func (m Model) filterCrumbBudget() int {
	return min(maxFilterCrumbsW, max(4, (m.Width-1-contextGap)/3))
}

// searchWidth is how many query cells the filter input can show beside the
// breadcrumb without the row overflowing the terminal.
func (m Model) searchWidth() int {
	crumbs := ansi.StringWidth(strings.Join(fitCrumbs(m.breadcrumb(), m.filterCrumbBudget()), crumbSep))
	return max(1, m.Width-1-crumbs-contextGap-searchChrome)
}

// sizeSearch fits the filter input to the context row. Re-seating the cursor
// recomputes the input's horizontal scroll so the cursor stays visible after
// the width shrinks.
func (m *Model) sizeSearch() {
	w := m.searchWidth()
	if m.Search.Width == w {
		return
	}
	m.Search.Width = w
	pos := m.Search.Position()
	m.Search.SetCursor(0)
	m.Search.SetCursor(pos)
}

// contextRow is the breadcrumb plus what the user needs to read the screen:
// the filter input while filtering, detail tabs, or summary counts. Controls
// are sized first and the breadcrumb shortened to the cells that remain, so
// neither the input's cursor nor the active tab is ever cut off.
func (m Model) contextRow() string {
	avail := max(1, m.Width-1)
	if m.Filtering {
		// The input was sized by sizeSearch; the breadcrumb gets what is left.
		budget := min(m.filterCrumbBudget(), avail-contextGap-m.Search.Width-searchChrome)
		return " " + m.breadcrumbView(max(1, budget)) + strings.Repeat(" ", contextGap) + m.Search.View()
	}
	if m.Detail && (m.ActiveFocus == FocusRepos || m.ActiveFocus == FocusConcerns) {
		tabs := m.renderTabBar(max(1, avail-contextGap-8))
		return " " + m.breadcrumbView(avail-contextGap-ansi.StringWidth(tabs)) + strings.Repeat(" ", contextGap) + tabs
	}
	row := " " + m.breadcrumbView(avail)
	if detail := m.contextSummary(); detail != "" {
		row += th.dim.Render("  ·  " + detail)
	}
	return row
}

func (m Model) contextSummary() string {
	var s string
	switch m.ActiveFocus {
	case FocusOverview:
		switch {
		case m.TargetOrg == "" && m.OrgRefreshFailed:
			s = "degraded local snapshot"
		case m.TargetOrg == "" && m.LocalScanFailed:
			s = "local workspace · metadata incomplete"
		case m.TargetOrg == "":
			s = "local workspace"
		case m.OrgRefreshFailed:
			s = "degraded organization snapshot"
		case m.LocalScanFailed:
			s = "organization snapshot · local metadata incomplete"
		default:
			s = "live organization snapshot"
		}
		return s
	case FocusRepos:
		summary := m.buildOrgSummary()
		s = countPhrase(summary.repositories, "repository", "repositories")
		if summary.repoProblems > 0 {
			s += fmt.Sprintf(" · %d need attention", summary.repoProblems)
		}
		if summary.newRepos > 0 {
			s += fmt.Sprintf(" · %d not cloned", summary.newRepos)
		}
		if summary.dirty > 0 {
			s += fmt.Sprintf(" · %d with local changes", summary.dirty)
		}
	case FocusConcerns:
		summary := m.buildRepoCountSummary()
		known := summary.concernRepos
		switch {
		case m.TargetOrg == "":
			s = "GitHub owner not configured"
		case summary.countsLoaded < summary.repositories:
			s = fmt.Sprintf("%s · counts loaded for %d/%d repositories", countPhrase(known, "known repository", "known repositories"), summary.countsLoaded, summary.repositories)
			if summary.countsStale > 0 {
				s += fmt.Sprintf(" · %d from last successful refresh", summary.countsStale)
			}
		case summary.countsStale > 0:
			s = fmt.Sprintf("%d repositories · %d open PRs · %d open issues · last successful counts for %d", known, summary.openPRs, summary.openIssues, summary.countsStale)
		default:
			s = fmt.Sprintf("%d repositories · %d open PRs · %d open issues", known, summary.openPRs, summary.openIssues)
		}
	case FocusJobs:
		switch {
		case m.OpenJobID != "":
			if j := m.openJob(); j != nil {
				s = string(j.Status)
				if j.RunnerName != "" {
					s += " · on " + j.RunnerName
				}
			}
		case m.OpenRun != nil:
			s = fmt.Sprintf("%s · attempt %d", string(m.OpenRun.Status), m.OpenRun.Attempt)
			if t := runTiming(m.OpenRun, time.Now()); t != "" {
				s += " · " + t
			}
		default:
			running, queued, waiting := 0, 0, 0
			for _, r := range m.actionRuns() {
				switch r.run.Status {
				case jobs.JobRunning:
					running++
				case jobs.JobQueued:
					queued++
				case jobs.JobWaiting:
					waiting++
				}
			}
			queuedJobs := 0
			for _, j := range m.JobQueue {
				if !j.IsRunHeader && j.Status == jobs.JobQueued {
					queuedJobs++
				}
			}
			if m.QueueView {
				s = fmt.Sprintf("%d jobs not started", queuedJobs)
			} else {
				s = fmt.Sprintf("%d running · %d queued · %d waiting · %d queued jobs", running, queued, waiting, queuedJobs)
			}
		}
	case FocusRunners:
		summary := m.buildOrgSummary()
		if m.RunnerPermissionDenied {
			s = fmt.Sprintf("%d observed busy · fleet access unavailable", summary.runnerBusy)
		} else {
			s = fmt.Sprintf("%d busy · %d idle · %d offline", summary.runnerBusy, summary.runnerIdle, summary.runnerOffline)
			if m.RunnerFetchFailed {
				s += " · last refresh failed"
			}
		}
	}
	if m.Search.Value() != "" {
		s += " · filter: " + m.Search.Value()
	}
	return s
}

// runTiming is the elapsed, waiting or total time GitHub reported for a run.
func runTiming(r *jobs.RunItem, now time.Time) string {
	switch {
	case terminalStatus(r.Status) && !r.StartedAt.IsZero() && r.CompletedAt.After(r.StartedAt):
		return "took " + compactAgo(r.CompletedAt.Sub(r.StartedAt)) + " · finished " + compactAgo(now.Sub(r.CompletedAt)) + " ago"
	case r.Status == jobs.JobRunning && !r.StartedAt.IsZero():
		return "running " + compactAgo(now.Sub(r.StartedAt))
	case (r.Status == jobs.JobQueued || r.Status == jobs.JobWaiting) && !r.CreatedAt.IsZero():
		return "waiting " + compactAgo(now.Sub(r.CreatedAt))
	}
	return ""
}

func (m Model) screenView() string {
	if m.Width == 0 {
		return "Starting freshen…"
	}
	if m.Width < minFrameWidth || m.Height < minFrameHeight {
		return fitFrame(fmt.Sprintf("freshen\nResize to at least %d × %d\n%s", minFrameWidth, minFrameHeight, m.quitHint()), m.Width, m.Height)
	}
	body := m.listContent()
	if m.ActiveFocus == FocusOverview {
		body = m.summaryContent(max(1, m.Width-2), m.bodyHeight())
	}
	if m.detailVisible() {
		body = m.Viewport.View()
	}
	if m.ShowHelp {
		lines := strings.Split(m.helpContent(), "\n")
		body = strings.Join(lines[min(m.HelpOffset, m.helpMaxOffset()):], "\n")
	}
	if m.MenuOpen || m.PendingAction != "" {
		body = ansi.Wrap(m.menuContent(), m.Width-2, "")
	}
	body = insetFrame(body, m.Width, m.bodyHeight())
	return fitFrame(strings.Join([]string{m.headerRow(), m.tabRow(), m.contextRow(), body, " " + m.statusLine(), " " + m.footerView()}, "\n"), m.Width, m.Height)
}

// quitHint names a quit key that works right now: q is suspended by the
// filter, menu and confirmation layers, ctrl+c never is.
func (m Model) quitHint() string {
	k := m.keys()
	if k.Quit.Enabled() {
		return k.Quit.Help().Key + " quit"
	}
	return k.ForceQuit.Help().Key + " quit"
}

// helpMaxOffset is the furthest the help overlay can scroll at the current
// size: its wrapped rows less one screen of body.
func (m Model) helpMaxOffset() int {
	return max(0, len(strings.Split(m.helpContent(), "\n"))-m.bodyHeight())
}

// footerView renders the key hints that fit, always keeping the last one.
func (m Model) footerView() string {
	bs := m.footerBindings()
	if len(bs) == 0 {
		return ""
	}
	render := func(b key.Binding) string {
		return th.key.Render(b.Help().Key) + " " + th.keyDesc.Render(b.Help().Desc)
	}
	sep := th.keySep.Render(" • ")
	last := render(bs[len(bs)-1])
	out := ""
	for _, b := range bs[:len(bs)-1] {
		candidate := out + render(b) + sep
		if ansi.StringWidth(candidate+last) > m.Width-2 {
			break
		}
		out = candidate
	}
	return out + last
}

func (m Model) statusLine() string {
	if m.BusyAction != "" {
		return m.Spinner.View() + " " + m.BusyAction + th.dim.Render(" · navigation remains available")
	}
	if m.toastVisible(time.Now()) {
		style := th.toastInfo
		if m.ToastPriority >= 2 {
			style = th.toastError
		}
		toast := style.Render(strings.TrimSpace(m.ToastMsg))
		if m.OpenJobID == "" && !m.Detail && m.Search.Value() == "" && m.OpenRun == nil {
			toast += th.dim.Render("  esc dismiss") // Esc reaches the message only at the top of a screen
		}
		return toast
	}
	repoScreen := m.ActiveFocus == FocusOverview || m.ActiveFocus == FocusRepos || m.ActiveFocus == FocusConcerns
	warn := func(s string) string { return th.warning.Render(statusGlyphs[kindWarning] + " " + s) }
	switch {
	case m.OrgRefreshFailed && repoScreen:
		return warn("Repository snapshot unavailable · " + m.orgRefreshFailureDetail())
	case m.LocalScanUnresponsive && repoScreen:
		return warn("Workspace directory unresponsive · restore it and press r to retry")
	case m.LocalScanStuck && repoScreen:
		return warn("Previous local repository scan abandoned · press r to retry")
	case m.LocalScanInProgress && repoScreen:
		return warn("Local repository scan still running · automatic refresh will retry after it finishes")
	case m.LocalScanFailed && repoScreen:
		return warn("Local repository metadata incomplete · press r to retry")
	case m.JobQueueFetchFailed:
		return warn("Actions incomplete · " + m.ActionsCoverage)
	case m.RunnerFetchFailed && !m.RunnerPermissionDenied && m.runnersVisible():
		// Outlives the error toast: cached rows must not pass for live ones.
		shown := "no runner data"
		if !m.LastRunnersRefresh.IsZero() {
			shown = "showing runners from " + agoOr(m.LastRunnersRefresh, false)
		}
		return warn("Runner refresh failed · " + shown + " · press r to retry")
	case m.IsSyncing:
		return m.Spinner.View() + " Synchronising repositories"
	}
	if m.ShowHelp {
		total := len(strings.Split(m.helpContent(), "\n"))
		offset := min(m.HelpOffset, m.helpMaxOffset())
		shown := min(total, offset+m.bodyHeight())
		return th.dim.Render(fmt.Sprintf("Keys valid on %s · lines %d–%d of %d", m.screenName(), offset+1, shown, total))
	}
	if m.ActiveFocus == FocusOverview {
		if m.TargetOrg == "" {
			return th.dim.Render("Local workspace · set an owner to load Actions and runners")
		}
		return th.dim.Render("Press 1–4 or Tab to open a screen · ? lists every key")
	}
	if m.detailVisible() {
		return th.dim.Render(fmt.Sprintf("%d%% scrolled", int(m.Viewport.ScrollPercent()*100)))
	}
	es := m.entries()
	if len(es) == 0 {
		return th.dim.Render("0 items")
	}
	position := fmt.Sprintf("%d / %d", m.entryIndex(es)+1, len(es))
	if m.ActiveFocus == FocusJobs && m.OpenRun == nil && !m.QueueView {
		position += " · recent history: 30 runs/repository"
	}
	return th.dim.Render(position)
}

// hangingWrap renders text after a lead already indent cells wide, wrapping it
// to width cells and aligning continuation lines under its first character.
// When fewer than minText cells remain beside the lead, the text starts on the
// next line instead, indented by two cells.
func hangingWrap(lead string, indent int, text string, width int, style func(...string) string) []string {
	const minText = 10
	textWidth := width - indent
	pad := strings.Repeat(" ", indent)
	var out []string
	if textWidth < minText {
		out = append(out, lead)
		lead, pad, textWidth = "  ", "  ", max(1, width-2)
	}
	for i, line := range strings.Split(ansi.Wrap(text, textWidth, ""), "\n") {
		if i == 0 {
			out = append(out, lead+style(line))
		} else {
			out = append(out, pad+style(line))
		}
	}
	return out
}

// helpContent is the ? overlay. Its key list is generated from keys(), the
// same bindings the handlers match, for the screen beneath the overlay. Every
// row is wrapped to the body width, so scrolling reaches all of it.
func (m Model) helpContent() string {
	width := max(1, m.Width-2)
	plain := func(s ...string) string { return strings.Join(s, " ") }
	groups := m.helpGroups()
	keyWidth := 0
	for _, g := range groups {
		for _, b := range g.bindings {
			keyWidth = max(keyWidth, ansi.StringWidth(b.Help().Key))
		}
	}
	lines := hangingWrap("", 0, "Keys for "+strings.Join(m.breadcrumb(), crumbSep), width, th.strong.Render)
	lines = append(lines, "")
	for _, g := range groups {
		lines = append(lines, th.section.Render(g.title))
		for _, b := range g.bindings {
			k := b.Help().Key
			lead := "  " + th.key.Render(k) + strings.Repeat(" ", keyWidth-ansi.StringWidth(k)+2)
			lines = append(lines, hangingWrap(lead, keyWidth+4, b.Help().Desc, width, plain)...)
		}
		lines = append(lines, "")
	}
	lines = append(lines, th.section.Render("Status"))
	for _, legend := range []struct {
		kind statusKind
		text string
	}{
		{kindSuccess, "success · passed, up to date, idle runner"},
		{kindFailure, "failure · failed, sync error, offline runner"},
		{kindRunning, "running · in progress, syncing, busy runner"},
		{kindQueued, "queued · not started, waiting for approval or a runner"},
		{kindSkipped, "skipped · skipped, cancelled, archived, maintenance"},
		{kindUnknown, "unknown · not synced or not reported"},
		{kindWarning, "attention · incomplete or stale data"},
	} {
		glyph := th.glyph(legend.kind)
		lines = append(lines, hangingWrap("  "+glyph+"  ", 4+ansi.StringWidth(glyph), legend.text, width, plain)...)
	}
	lines = append(lines, "")
	lines = append(lines, hangingWrap("", 0, "The filter, action menu and confirmations take every key until Enter or Esc; Ctrl+C quits from anywhere. Lists never change screen at their ends.", width, th.dim.Render)...)
	return strings.Join(lines, "\n")
}
