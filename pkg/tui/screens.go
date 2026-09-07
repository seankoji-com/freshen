package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/seankoji-com/freshen/pkg/jobs"
)

// screenEntry is shared by rendering, navigation and mouse hit testing. Keys
// survive polling and sorting; index resolves the current backing slice only.
type screenEntry struct {
	key, title, subtitle string
	index                int
}
type actionRun struct {
	run  *jobs.RunItem
	jobs []*jobs.JobItem
}

type screenTab struct {
	key   string
	name  string
	focus FocusType
}

func screenTabs(width int) []screenTab {
	if width < 48 {
		return []screenTab{{"0", "O", FocusOverview}, {"1", "R", FocusRepos}, {"2", "A", FocusJobs}, {"3", "N", FocusRunners}, {"4", "C", FocusConcerns}}
	}
	if width < 72 {
		return []screenTab{{"0", "Org", FocusOverview}, {"1", "Repos", FocusRepos}, {"2", "CI", FocusJobs}, {"3", "Run", FocusRunners}, {"4", "Concerns", FocusConcerns}}
	}
	return []screenTab{{"0", "Overview", FocusOverview}, {"1", "Repositories", FocusRepos}, {"2", "Actions", FocusJobs}, {"3", "Runners", FocusRunners}, {"4", "Concerns", FocusConcerns}}
}

func countPhrase(count int, singular, plural string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s", singular)
	}
	return fmt.Sprintf("%d %s", count, plural)
}

func runKey(r *jobs.RunItem) string { return fmt.Sprintf("%s/%d", r.Repo, r.ID) }
func terminalStatus(s jobs.JobStatus) bool {
	return s == jobs.JobPassed || s == jobs.JobFailed || s == jobs.JobCancelled || s == jobs.JobSkipped
}
func (m Model) actionRuns() []actionRun {
	var runs []actionRun
	indices := map[string]int{}
	for _, j := range m.JobQueue {
		if j.RunID == 0 {
			continue
		}
		r := j.Run
		if r == nil {
			r = &jobs.RunItem{ID: j.RunID, Repo: j.Repo, Workflow: j.WorkflowName, Branch: j.Branch, Event: j.Event, Status: j.Status}
		}
		k := runKey(r)
		i, ok := indices[k]
		if !ok {
			i = len(runs)
			indices[k] = i
			runs = append(runs, actionRun{run: r})
		}
		if j.IsRunHeader {
			runs[i].run = r
		} else {
			runs[i].jobs = append(runs[i].jobs, j)
		}
	}
	sort.SliceStable(runs, func(i, j int) bool {
		ai, aj := !terminalStatus(runs[i].run.Status), !terminalStatus(runs[j].run.Status)
		if ai != aj {
			return ai
		}
		if ai {
			return runs[i].run.ID < runs[j].run.ID
		}
		return runs[i].run.ID > runs[j].run.ID
	})
	return runs
}
func (m Model) runJobs() []*jobs.JobItem {
	var result []*jobs.JobItem
	if m.OpenRun == nil {
		return result
	}
	for _, j := range m.JobQueue {
		if !j.IsRunHeader && j.Repo == m.OpenRun.Repo && j.RunID == m.OpenRun.ID {
			result = append(result, j)
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].GHJobID < result[j].GHJobID })
	return result
}
func (m Model) openJob() *jobs.JobItem {
	for _, j := range m.runJobs() {
		if j.ID == m.OpenJobID {
			return j
		}
	}
	return nil
}
func (m *Model) refreshOpenRun() {
	if m.OpenRun == nil {
		return
	}
	for _, r := range m.actionRuns() {
		if runKey(r.run) == runKey(m.OpenRun) {
			m.OpenRun = r.run
			return
		}
	}
	// Keep the navigation target so a missing poll cannot silently open another run.
	copyRun := *m.OpenRun
	copyRun.JobsError = "This run is outside the latest snapshot. Press r to reload its jobs or o to open GitHub."
	m.OpenRun = &copyRun
}
func completion(items []*jobs.JobItem) (done int) {
	for _, j := range items {
		if terminalStatus(j.Status) {
			done++
		}
	}
	return done
}
func stepSummary(j *jobs.JobItem) (done int, current string) {
	for _, step := range j.Steps {
		if step.Status == "completed" {
			done++
		}
		if step.Status == "in_progress" {
			current = step.Name
		}
	}
	return done, current
}
func stateBadge(s jobs.JobStatus) string {
	c := colorMuted
	switch s {
	case jobs.JobRunning:
		c = colorSecondary
	case jobs.JobQueued, jobs.JobWaiting:
		c = colorYellow
	case jobs.JobPassed:
		c = colorGreen
	case jobs.JobFailed:
		c = colorRed
	}
	return lipgloss.NewStyle().Foreground(c).Bold(true).Render(string(s))
}
func (m Model) countBar(done, total int, noun string) string {
	if total == 0 {
		return noun + " not reported"
	}
	p := m.ProgressBar
	p.Width = min(14, max(4, m.Width/8))
	return fmt.Sprintf("%s %d/%d %s complete", p.ViewAs(float64(done)/float64(total)), done, total, noun)
}
func (m Model) entries() []screenEntry {
	var entries []screenEntry
	switch m.ActiveFocus {
	case FocusConcerns:
		for _, i := range m.concernScreenRepoIndices() {
			r := m.Repos[i]
			if !r.HasLoadedCounts {
				detail := r.CurrentBranch
				if detail == "" {
					detail = "not cloned"
				}
				entries = append(entries, screenEntry{
					key: r.Path + "/" + r.Name, index: i,
					title: r.Name + "  " + badgeSkipped.Render("counts unknown"), subtitle: detail + " · not ranked",
				})
				continue
			}
			badges := make([]string, 0, 2)
			if r.OpenPRsCount > 0 {
				badges = append(badges, badgePR.Render(countPhrase(r.OpenPRsCount, "open PR", "open PRs")))
			}
			if r.OpenIssuesCount > 0 {
				badges = append(badges, badgeIssue.Render(countPhrase(r.OpenIssuesCount, "open issue", "open issues")))
			}
			detail := r.CurrentBranch
			if detail == "" {
				detail = "not cloned"
			}
			if r.StatusMsg != "" {
				detail += " · " + r.StatusMsg
			}
			if r.LocalMetadataStale {
				detail += " · local metadata from last snapshot"
			}
			if r.CountsStale {
				detail += " · counts from last successful refresh"
			}
			entries = append(entries, screenEntry{
				key: r.Path + "/" + r.Name, index: i,
				title: r.Name + "  " + strings.Join(badges, "  "), subtitle: detail,
			})
		}
	case FocusRepos:
		for i, r := range m.Repos {
			counts := "counts not loaded"
			if r.HasLoadedCounts {
				counts = fmt.Sprintf("%d PRs · %d issues", r.OpenPRsCount, r.OpenIssuesCount)
				if r.CountsStale {
					counts += " · last successful refresh"
				}
			}
			if r.LocalMetadataStale {
				counts += " · local metadata from last snapshot"
			}
			entries = append(entries, screenEntry{key: r.Path + "/" + r.Name, index: i,
				title: r.Name + "  " + m.renderStatusBadge(r), subtitle: r.CurrentBranch + " · " + counts + " · " + r.StatusMsg})
		}
	case FocusRunners:
		for i, r := range m.getMatchingRunners() {
			current := "No assigned job reported"
			if j := findJobForRunner(r, m.JobQueue); j != nil && j.Status == jobs.JobRunning {
				current = j.Name
			}
			entries = append(entries, screenEntry{key: r.ID, index: i, title: r.Name + "  " + string(r.Status), subtitle: r.Platform + " · " + current})
		}
	case FocusJobs:
		if m.OpenRun != nil {
			for i, j := range m.runJobs() {
				done, current := stepSummary(j)
				detail := m.countBar(done, len(j.Steps), "steps")
				if current != "" {
					detail += " · " + current
				}
				if j.Status == jobs.JobQueued || j.Status == jobs.JobWaiting {
					detail = "Not started · labels: " + strings.Join(j.Labels, ", ")
				}
				if j.Duration != "" && j.Duration != "-" {
					detail += " · " + j.Duration
				}
				entries = append(entries, screenEntry{key: j.ID, index: i, title: stateBadge(j.Status) + "  " + strings.TrimPrefix(j.Name, j.Repo+" / "), subtitle: detail})
			}
		} else if m.QueueView {
			for i, j := range m.JobQueue {
				if j.IsRunHeader || terminalStatus(j.Status) || j.RunID == 0 {
					continue
				}
				subtitle := j.Repo + " / " + j.WorkflowName + " · " + j.Branch
				if j.Run != nil {
					subtitle = j.Run.Repo + " / " + j.Run.Workflow + " · " + j.Run.Branch
				}
				if j.Status == jobs.JobRunning {
					done, current := stepSummary(j)
					subtitle = m.countBar(done, len(j.Steps), "steps") + " · " + current + " · " + subtitle
				}
				if !j.CreatedAt.IsZero() && j.Status == jobs.JobQueued {
					subtitle = "Waiting " + jobs.FormatDuration(time.Since(j.CreatedAt)) + " · " + subtitle
				}
				entries = append(entries, screenEntry{key: j.ID, index: i, title: stateBadge(j.Status) + "  " + j.Name, subtitle: subtitle})
			}
		} else {
			for i, r := range m.actionRuns() {
				if m.ActionsFilter == 0 && terminalStatus(r.run.Status) {
					continue
				}
				if m.ActionsFilter == 1 && r.run.Status != jobs.JobFailed && r.run.Status != jobs.JobWaiting {
					continue
				}
				title := fmt.Sprintf("%s  %s / %s  #%d", stateBadge(r.run.Status), r.run.Repo, r.run.Workflow, r.run.Number)
				if r.run.Number == 0 {
					title = fmt.Sprintf("%s  %s / %s  run %d", stateBadge(r.run.Status), r.run.Repo, r.run.Workflow, r.run.ID)
				}
				progress := m.countBar(completion(r.jobs), len(r.jobs), "jobs")
				if !r.run.JobsKnown {
					progress = "Enter to load jobs"
				}
				if r.run.JobsStale {
					progress = "Cached results · refreshing"
				}
				if r.run.JobsError != "" {
					progress = "Job details unavailable · r retries"
				}
				sub := progress + " · " + r.run.Branch + " · " + r.run.Event
				if r.run.Title != "" {
					sub += " · " + r.run.Title
				}
				entries = append(entries, screenEntry{key: runKey(r.run), index: i, title: title, subtitle: sub})
			}
		}
	}
	query := strings.ToLower(m.Search.Value())
	if query == "" {
		return entries
	}
	filtered := entries[:0]
	for _, e := range entries {
		if strings.Contains(strings.ToLower(ansi.Strip(e.title+" "+e.subtitle)), query) {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

func (m Model) concernRepoIndices() []int {
	indices := make([]int, 0)
	for i, r := range m.Repos {
		if r.HasLoadedCounts && (r.OpenPRsCount > 0 || r.OpenIssuesCount > 0) {
			indices = append(indices, i)
		}
	}
	sort.SliceStable(indices, func(i, j int) bool {
		a, b := m.Repos[indices[i]], m.Repos[indices[j]]
		if (a.OpenPRsCount > 0) != (b.OpenPRsCount > 0) {
			return a.OpenPRsCount > 0
		}
		if a.OpenPRsCount+a.OpenIssuesCount != b.OpenPRsCount+b.OpenIssuesCount {
			return a.OpenPRsCount+a.OpenIssuesCount > b.OpenPRsCount+b.OpenIssuesCount
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return indices
}
func (m Model) concernScreenRepoIndices() []int {
	indices := m.concernRepoIndices()
	var unknown []int
	for i, repo := range m.Repos {
		if !repo.HasLoadedCounts {
			unknown = append(unknown, i)
		}
	}
	sort.SliceStable(unknown, func(i, j int) bool {
		return strings.ToLower(m.Repos[unknown[i]].Name) < strings.ToLower(m.Repos[unknown[j]].Name)
	})
	return append(indices, unknown...)
}
func (m Model) concernsCoverageWarning() string {
	if m.ActiveFocus != FocusConcerns {
		return ""
	}
	summary := m.buildRepoCountSummary()
	unknown := summary.repositories - summary.countsLoaded
	if unknown <= 0 {
		return ""
	}
	if unknown == 1 {
		return "! 1 repository has unknown counts and is not ranked"
	}
	return fmt.Sprintf("! %d repositories have unknown counts and are not ranked", unknown)
}
func (m Model) listHeaderRows() int {
	if m.concernsCoverageWarning() != "" {
		return 2
	}
	return 0
}
func (m Model) selectionKey() string {
	if m.ActiveFocus == FocusJobs && m.OpenRun != nil {
		return m.JobCursor
	}
	return m.ScreenCursor[m.ActiveFocus]
}
func (m Model) entryIndex(entries []screenEntry) int {
	for i, e := range entries {
		if e.key == m.selectionKey() {
			return i
		}
	}
	return 0
}
func (m *Model) selectEntry(e screenEntry) {
	if m.ActiveFocus == FocusJobs && m.OpenRun != nil {
		m.JobCursor = e.key
	} else {
		m.ScreenCursor[m.ActiveFocus] = e.key
	}
	if m.ActiveFocus == FocusRepos || m.ActiveFocus == FocusConcerns {
		m.SelectedIndex = e.index
	}
	if m.ActiveFocus == FocusRunners {
		m.SelectedRunnerIndex = e.index
	}
}
func (m *Model) moveSelection(delta int) {
	es := m.entries()
	if len(es) == 0 {
		return
	}
	index := m.entryIndex(es)
	if m.selectionKey() == "" {
		index = 0
		delta = 0
	}
	m.selectEntry(es[max(0, min(len(es)-1, index+delta))])
}
func (m Model) bodyHeight() int { return max(1, m.Height-5) }
func (m Model) visibleEntries() (entries []screenEntry, start, selected int) {
	es := m.entries()
	selected = -1
	if m.selectionKey() != "" {
		selected = m.entryIndex(es)
	}
	reservedRows := m.listHeaderRows()
	count := max(1, (m.bodyHeight()-reservedRows)/2)
	start = max(0, max(selected, 0)-count+1)
	return es[start:min(len(es), start+count)], start, selected
}
func (m Model) detailVisible() bool { return m.Detail || m.OpenJobID != "" }
func (m Model) screenName() string {
	switch m.ActiveFocus {
	case FocusOverview:
		return "Overview"
	case FocusConcerns:
		return "Concerns"
	case FocusJobs:
		return "Actions"
	case FocusRunners:
		return "Runners"
	default:
		return "Repositories"
	}
}
func (m Model) contextLine() string {
	if m.Filtering {
		return m.Search.View()
	}
	if m.OpenJobID != "" {
		return "Actions / " + m.OpenRun.Workflow + " / job " + m.OpenJobID + " · steps and log tail"
	}
	if m.OpenRun != nil {
		return fmt.Sprintf("Actions / %s / %s · run #%d · attempt %d · %s", m.OpenRun.Repo, m.OpenRun.Workflow, m.OpenRun.Number, m.OpenRun.Attempt, stateBadge(m.OpenRun.Status))
	}
	if m.Detail && (m.ActiveFocus == FocusRepos || m.ActiveFocus == FocusConcerns) && m.SelectedIndex >= 0 && m.SelectedIndex < len(m.Repos) {
		return m.screenName() + " / " + m.Repos[m.SelectedIndex].Name + "  " + m.renderTabBar() + "  [ ] change tab"
	}
	if m.Detail {
		return "Runners / details"
	}
	s := m.screenName()
	if m.ActiveFocus == FocusOverview {
		if m.TargetOrg == "" {
			if m.OrgRefreshFailed {
				return "Overview · degraded local snapshot"
			}
			if m.LocalScanFailed {
				return "Overview · local workspace · metadata incomplete"
			}
			return "Overview · local workspace"
		}
		if m.OrgRefreshFailed {
			return "Overview · degraded organization snapshot"
		}
		if m.LocalScanFailed {
			return "Overview · organization snapshot · local metadata incomplete"
		}
		return "Overview · live organization snapshot"
	}
	if m.ActiveFocus == FocusConcerns {
		summary := m.buildRepoCountSummary()
		known := summary.concernRepos
		if m.TargetOrg == "" {
			return "Concerns · GitHub owner not configured"
		}
		if summary.countsLoaded < summary.repositories {
			context := fmt.Sprintf("Concerns · %s · counts loaded for %d/%d repositories", countPhrase(known, "known repository", "known repositories"), summary.countsLoaded, summary.repositories)
			if summary.countsStale > 0 {
				context += fmt.Sprintf(" · %d from last successful refresh", summary.countsStale)
			}
			return context
		}
		if summary.countsStale > 0 {
			return fmt.Sprintf("Concerns · %d repositories · %d open PRs · %d open issues · last successful counts for %d", known, summary.openPRs, summary.openIssues, summary.countsStale)
		}
		return fmt.Sprintf("Concerns · %d repositories · %d open PRs · %d open issues", known, summary.openPRs, summary.openIssues)
	}
	if m.ActiveFocus == FocusJobs {
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
		s = fmt.Sprintf("%s runs · %d running · %d queued · %d waiting", []string{"Active", "Needs attention", "Recent"}[m.ActionsFilter], running, queued, waiting)
	}
	if m.ActiveFocus == FocusJobs && m.OpenRun == nil {
		queued := 0
		for _, j := range m.JobQueue {
			if !j.IsRunHeader && j.Status == jobs.JobQueued {
				queued++
			}
		}
		if m.QueueView {
			s = fmt.Sprintf("Job queue · %d jobs not started · v shows runs", queued)
		} else {
			s += fmt.Sprintf(" · %d queued jobs · v queue", queued)
		}
	}
	if m.ActiveFocus == FocusRunners && m.RunnerPermissionDenied {
		s += " · observed assignments only; fleet access unavailable"
	}
	if m.Search.Value() != "" {
		s += " · filter: " + m.Search.Value()
	}
	return s
}
func (m Model) listContent() string {
	es, start, selected := m.visibleEntries()
	coverageWarning := m.concernsCoverageWarning()
	if len(es) == 0 {
		switch {
		case m.Search.Value() != "":
			if coverageWarning != "" {
				return coverageWarning + "\n\nNo matches. Esc clears the filter."
			}
			return "No matches. Esc clears the filter."
		case m.ActiveFocus == FocusRepos && m.IsOrgSyncing:
			return m.Spinner.View() + " Loading repositories…"
		case m.ActiveFocus == FocusConcerns && m.IsOrgSyncing:
			return m.Spinner.View() + " Loading concern counts…"
		case m.ActiveFocus == FocusConcerns && m.TargetOrg == "":
			return "Set a GitHub owner to load open PR and issue counts."
		case (m.ActiveFocus == FocusRepos || m.ActiveFocus == FocusConcerns) && m.LocalScanUnresponsive:
			return "Workspace directory is unresponsive. Restore it and press r to retry."
		case (m.ActiveFocus == FocusRepos || m.ActiveFocus == FocusConcerns) && m.LocalScanStuck:
			return "Previous local repository scan was abandoned. Press r to retry."
		case (m.ActiveFocus == FocusRepos || m.ActiveFocus == FocusConcerns) && m.LocalScanInProgress:
			return "Local repository scan is still running. Automatic refresh will retry after it finishes."
		case (m.ActiveFocus == FocusRepos || m.ActiveFocus == FocusConcerns) && m.LocalScanFailed:
			return "Local repository metadata is incomplete. Press r to retry."
		case m.ActiveFocus == FocusConcerns && coverageWarning != "":
			return coverageWarning + "\n\nNo known concerns. Press r to retry unavailable counts."
		case m.ActiveFocus == FocusConcerns:
			return "No open PRs or issues."
		case m.ActiveFocus == FocusJobs && (m.IsJobQueueLoading || m.RunLoading):
			return m.Spinner.View() + " Loading Actions…"
		case m.ActiveFocus == FocusJobs && m.OpenRun != nil && m.OpenRun.JobsError != "":
			return m.OpenRun.JobsError
		case m.ActiveFocus == FocusJobs && m.JobQueueFetchFailed:
			return "Actions unavailable or incomplete. Press r to retry."
		case m.ActiveFocus == FocusJobs && m.OpenRun != nil:
			return "GitHub has not reported any jobs for this run. Press r to refresh."
		case m.ActiveFocus == FocusJobs:
			return "No runs in this view. Press f to change the filter, or r to refresh."
		case m.ActiveFocus == FocusRunners && m.IsRunnersLoading:
			return m.Spinner.View() + " Loading runners…"
		case m.ActiveFocus == FocusRunners && m.RunnerPermissionDenied:
			return "Fleet access unavailable. Runners appear here when observed in job assignments."
		case m.ActiveFocus == FocusRunners && m.RunnerFetchFailed:
			return "Runner fetch failed. Press r to retry."
		default:
			return "Nothing to show. Press r to refresh."
		}
	}
	var lines []string
	if coverageWarning != "" {
		lines = append(lines, lipgloss.NewStyle().Foreground(colorYellow).Bold(true).Render(coverageWarning), "")
	}
	for i, e := range es {
		prefix := "  "
		title := e.title
		if i+start == selected {
			prefix = "› "
			title = selectedRowStyle.Render(title)
		}
		lines = append(lines, prefix+title, "  "+lipgloss.NewStyle().Foreground(colorMuted).Render(e.subtitle))
	}
	return strings.Join(lines, "\n")
}
func fitFrame(content string, width, height int) string {
	lines := strings.Split(content, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], max(0, width), "")
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func insetFrame(content string, width, height int) string {
	innerWidth := max(0, width-2)
	lines := strings.Split(fitFrame(content, innerWidth, height), "\n")
	for i := range lines {
		lines[i] = " " + lines[i]
	}
	return strings.Join(lines, "\n")
}

func (m Model) screenView() string {
	if m.Width == 0 {
		return "Starting freshen…"
	}
	if m.Width < 30 || m.Height < 10 {
		return fitFrame("freshen\nResize to at least 30 × 10\nq quit", m.Width, m.Height)
	}
	header := " " + titleStyle.Render("freshen")
	if m.TargetOrg != "" {
		header += "  " + lipgloss.NewStyle().Foreground(colorMuted).Render(m.TargetOrg)
	}
	if m.JobQueueFetchFailed {
		header += badgeError.Render(" · Actions incomplete")
	}
	tabs := ""
	for _, tab := range screenTabs(m.Width) {
		style := tabInactiveStyle
		if tab.focus == m.ActiveFocus {
			style = tabActiveStyle
		}
		tabs += style.Render(tab.key+" "+tab.name) + " "
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
		body = strings.Join(lines[min(m.HelpOffset, len(lines)-1):], "\n")
	}
	if m.MenuOpen || m.PendingAction != "" {
		body = ansi.Wrap(m.menuContent(), m.Width-2, "")
	}
	body = insetFrame(body, m.Width, m.bodyHeight())
	status := m.statusLine()
	return fitFrame(header+"\n"+tabs+"\n "+m.contextLine()+"\n"+body+"\n "+status+"\n "+m.shortHelp(), m.Width, m.Height)
}
func (m Model) statusLine() string {
	if m.BusyAction != "" {
		return m.Spinner.View() + " " + m.BusyAction + " · navigation remains available"
	}
	if m.ToastMsg != "" {
		return m.ToastMsg + "  (Esc dismiss)"
	}
	if m.OrgRefreshFailed && (m.ActiveFocus == FocusOverview || m.ActiveFocus == FocusRepos || m.ActiveFocus == FocusConcerns) {
		return "Repository snapshot unavailable · " + m.orgRefreshFailureDetail()
	}
	if m.LocalScanUnresponsive && (m.ActiveFocus == FocusOverview || m.ActiveFocus == FocusRepos || m.ActiveFocus == FocusConcerns) {
		return "Workspace directory unresponsive · restore it and press r to retry"
	}
	if m.LocalScanStuck && (m.ActiveFocus == FocusOverview || m.ActiveFocus == FocusRepos || m.ActiveFocus == FocusConcerns) {
		return "Previous local repository scan abandoned · press r to retry"
	}
	if m.LocalScanInProgress && (m.ActiveFocus == FocusOverview || m.ActiveFocus == FocusRepos || m.ActiveFocus == FocusConcerns) {
		return "Local repository scan still running · automatic refresh will retry after it finishes"
	}
	if m.LocalScanFailed && (m.ActiveFocus == FocusOverview || m.ActiveFocus == FocusRepos || m.ActiveFocus == FocusConcerns) {
		return "Local repository metadata incomplete · press r to retry"
	}
	if m.JobQueueFetchFailed {
		return "Actions incomplete · " + m.ActionsCoverage
	}
	if m.ActiveFocus == FocusJobs && !m.LastActionsRefresh.IsZero() {
		return fmt.Sprintf("Updated %s ago · auto-refresh %s · recent history: 30 runs/repository", jobs.FormatDuration(time.Since(m.LastActionsRefresh)), jobs.FormatDuration(m.actionsPollInterval()))
	}
	if m.IsSyncing {
		return m.Spinner.View() + " Synchronising repositories"
	}
	if m.ActiveFocus == FocusOverview {
		if m.TargetOrg == "" {
			return "Local workspace · set an owner to load Actions and runners"
		}
		if m.IsOrgSyncing || m.IsJobQueueLoading || m.IsRunnersLoading {
			return m.Spinner.View() + " Refreshing organization snapshot"
		}
		if !m.LastActionsRefresh.IsZero() {
			return fmt.Sprintf("Actions updated %s ago · auto-refresh %s", jobs.FormatDuration(time.Since(m.LastActionsRefresh)), jobs.FormatDuration(m.actionsPollInterval()))
		}
		return "Live snapshot · repositories 5m · Actions 20s · runners 10s"
	}
	if m.detailVisible() {
		return fmt.Sprintf("%d%% scrolled", int(m.Viewport.ScrollPercent()*100))
	}
	es := m.entries()
	if len(es) == 0 {
		return "0 items"
	}
	return fmt.Sprintf("%d / %d · %s", m.entryIndex(es)+1, len(es), m.screenName())
}
func (m Model) shortHelp() string {
	bindings := []key.Binding{}
	add := func(k, d string) { bindings = append(bindings, key.NewBinding(key.WithKeys(k), key.WithHelp(k, d))) }
	if m.PendingAction != "" {
		add("enter", "confirm")
		add("esc", "cancel")
	} else if m.MenuOpen {
		add("↑↓", "choose")
		add("enter", "select")
		add("esc", "close")
	} else if m.Filtering {
		add("enter", "apply filter")
		add("esc", "clear")
	} else if m.ShowHelp {
		add("↑↓ / pgdn", "scroll")
		add("? / esc", "close help")
	} else if m.ActiveFocus == FocusOverview {
		add("tab", "screen")
		add("1/2/3/4", "jump")
		add("r", "refresh")
		add("?", "help")
	} else {
		add("tab", "screen")
		add("↑↓", "move")
		if m.detailVisible() {
			add("esc", "back")
		} else {
			add("enter", "open")
			add("/", "filter")
		}
		add("space", "actions")
		add("?", "help")
	}
	return m.Help.ShortHelpView(bindings)
}
func (m Model) helpContent() string {
	return "Navigation\n\n0 Overview   1 Repositories   2 Actions   3 Runners   4 Concerns\nTab / Shift+Tab change screen\n↑↓ or j/k move within a list; never change screens\nEnter / → open    Esc / ← back\n/ filter list    Home/End first/last    PgUp/PgDn page\n\nOverview\nr refreshes repository, Actions and runner snapshots\nCounts marked unavailable or incomplete are never treated as zero\n\nConcerns\nRepositories with known open PRs or issues, ordered for review\nEnter opens the existing repository detail; r refreshes counts\n\nActions\nSpace opens actions for the selected item\nr refreshes the current screen or detail\nv toggles workflow runs / job queue\nf cycles Active / Needs attention / Recent runs\nEnter on a run opens jobs; Enter on a job opens steps and logs\no opens the selected run, job or repository in GitHub\ny copies the selected URL or runner ID\n\nRepository detail\n[ / ] switch Logs / Branches / Issues / PRs\ns sync selected repository    a sync all (confirmation)\nb switch branch    p commit, push and create PR (confirmation)\nX prune branches/worktrees (confirmation)    d delete archived clone\n\n↑↓ scroll details; PgUp/PgDn scroll a page\n? closes help    q quits    Ctrl+C quits everywhere"
}
func (m Model) jobDetailContent() string {
	j := m.openJob()
	if j == nil {
		return "This job is no longer in the snapshot. Esc returns to the run."
	}
	var sb strings.Builder
	if j.Run != nil && j.Run.JobsError != "" {
		sb.WriteString("Cached job state; refresh failed: " + j.Run.JobsError + "\n\n")
	} else if j.Run != nil && j.Run.JobsStale {
		sb.WriteString("Cached job state from the previous poll; refreshing.\n\n")
	}
	fmt.Fprintf(&sb, "%s  %s\n%s\n\n", stateBadge(j.Status), j.Name, Hyperlink("Open job in GitHub", m.jobURL(j)))
	if j.RunnerName != "" {
		fmt.Fprintf(&sb, "Runner: %s\n", j.RunnerName)
	} else {
		sb.WriteString("Runner: not assigned\n")
	}
	if len(j.Labels) > 0 {
		fmt.Fprintf(&sb, "Requested labels: %s\n", strings.Join(j.Labels, ", "))
	}
	if j.Status == jobs.JobQueued || j.Status == jobs.JobWaiting {
		sb.WriteString("Not started. Dependencies, approvals, concurrency or runner availability may be involved.\n")
	}
	if j.Duration != "" {
		fmt.Fprintf(&sb, "Elapsed: %s\n", j.Duration)
	}
	done, _ := stepSummary(j)
	fmt.Fprintf(&sb, "\n%s\n\n", m.countBar(done, len(j.Steps), "steps"))
	for _, s := range j.Steps {
		state := s.Status
		if s.Conclusion != "" {
			state = s.Conclusion
		}
		fmt.Fprintf(&sb, "%2d  %-12s %s\n", s.Number, state, s.Name)
	}
	sb.WriteString("\nLog tail (last 200 lines)\n\n")
	if m.LogLoading == j.ID {
		sb.WriteString("Loading logs…\n")
	}
	if len(j.Logs) == 0 && m.LogLoading == "" {
		sb.WriteString("Logs not loaded. Press r to retry; GitHub may withhold logs while a job is running.\n")
	}
	for _, line := range j.Logs {
		sb.WriteString(jobs.SanitizeTerminal(line) + "\n")
	}
	return sb.String()
}
func (m Model) runnerDetailContent() string {
	rs := m.getMatchingRunners()
	if m.SelectedRunnerIndex >= len(rs) {
		return "Runner no longer available. Esc returns to runners."
	}
	r := rs[m.SelectedRunnerIndex]
	content := fmt.Sprintf("%s  %s\n\nPlatform: %s\nLabels: %s\nID: %s\n", r.Name, r.Status, r.Platform, strings.Join(r.Tags, ", "), r.ID)
	if m.RunnerPermissionDenied {
		content += "\nObserved job assignment only. Fleet availability is unknown.\n"
	}
	if j := findJobForRunner(r, m.JobQueue); j != nil && j.Status == jobs.JobRunning {
		content += "\nAssigned job: " + j.Name + "\n" + m.jobURL(j)
	} else {
		content += "\nNo active job assignment reported."
	}
	return content
}
func (m Model) jobURL(j *jobs.JobItem) string {
	if j.RunID == 0 || j.Repo == "" {
		return ""
	}
	url := fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d", m.TargetOrg, j.Repo, j.RunID)
	if !j.IsRunHeader && j.GHJobID != 0 {
		url += fmt.Sprintf("/job/%d", j.GHJobID)
	}
	return url
}
