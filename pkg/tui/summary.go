package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/seankoji-com/freshen/pkg/git"
	"github.com/seankoji-com/freshen/pkg/jobs"
)

type summaryAttention struct {
	priority int
	title    string
	detail   string
}

type orgSummary struct {
	repositories int
	localRepos   int
	newRepos     int
	archived     int
	dirty        int
	repoProblems int
	openPRs      int
	openIssues   int
	countsLoaded int
	countsStale  int
	concernRepos int

	runningRuns int
	queuedRuns  int
	waitingRuns int
	failedRuns  int

	runnerBusy        int
	runnerIdle        int
	runnerOffline     int
	runnerMaintenance int
	runnerUnknown     int

	attention []summaryAttention
}

func (m Model) buildRepoCountSummary() orgSummary {
	s := orgSummary{repositories: len(m.Repos)}
	for _, repo := range m.Repos {
		if !repo.HasLoadedCounts {
			continue
		}
		s.countsLoaded++
		if repo.CountsStale {
			s.countsStale++
		}
		s.openPRs += repo.OpenPRsCount
		s.openIssues += repo.OpenIssuesCount
		if repo.OpenPRsCount > 0 || repo.OpenIssuesCount > 0 {
			s.concernRepos++
		}
	}
	return s
}

func (m Model) buildOrgSummary() orgSummary {
	s := m.buildRepoCountSummary()
	for _, r := range m.Repos {
		if r.IsNew {
			s.newRepos++
		} else {
			s.localRepos++
		}
		if r.IsArchived {
			s.archived++
		}
		if r.HasUnstagedChanges {
			s.dirty++
		}
		if r.HasLoadedCounts {
			if r.OpenPRsCount > 0 || r.OpenIssuesCount > 0 {
				parts := make([]string, 0, 2)
				if r.OpenPRsCount > 0 {
					parts = append(parts, countPhrase(r.OpenPRsCount, "open PR", "open PRs"))
				}
				if r.OpenIssuesCount > 0 {
					parts = append(parts, countPhrase(r.OpenIssuesCount, "open issue", "open issues"))
				}
				priority := 2
				if r.OpenPRsCount > 0 {
					priority = 1
				}
				s.attention = append(s.attention, summaryAttention{priority: priority, title: r.Name, detail: strings.Join(parts, " · ")})
			}
		}
		if r.Status == git.StatusError || r.Status == git.StatusRebaseConflict {
			s.repoProblems++
			detail := r.StatusMsg
			if detail == "" {
				detail = "repository state needs attention"
			}
			s.attention = append(s.attention, summaryAttention{priority: 0, title: r.Name, detail: detail})
		}
	}
	if m.RepoCountsFetchFailed && s.countsStale == 0 {
		s.attention = append(s.attention, summaryAttention{priority: 1, title: "Review count snapshot incomplete", detail: "press r to retry"})
	}
	if s.countsStale > 0 {
		s.attention = append(s.attention, summaryAttention{
			priority: 1,
			title:    "Review counts stale",
			detail:   fmt.Sprintf("last successful refresh for %s", countPhrase(s.countsStale, "repository", "repositories")),
		})
	}
	if m.OrgRefreshFailed {
		s.attention = append(s.attention, summaryAttention{priority: 0, title: "Repository snapshot unavailable", detail: m.orgRefreshFailureDetail()})
	}
	if m.LocalScanFailed {
		title := "Local repository metadata incomplete"
		detail := "showing last-known data where available · press r to retry"
		if m.LocalScanUnresponsive {
			title = "Workspace directory unresponsive"
			detail = "showing last-known data · restore the directory and press r to retry"
		} else if m.LocalScanStuck {
			title = "Previous local repository scan abandoned"
			detail = "showing last-known data · press r to retry"
		} else if m.LocalScanInProgress {
			title = "Local repository scan still running"
			detail = "showing last-known data · automatic refresh will retry after it finishes"
		}
		s.attention = append(s.attention, summaryAttention{priority: 1, title: title, detail: detail})
	}

	for _, run := range m.actionRuns() {
		r := run.run
		switch r.Status {
		case jobs.JobRunning:
			s.runningRuns++
		case jobs.JobQueued:
			s.queuedRuns++
		case jobs.JobWaiting:
			s.waitingRuns++
			s.attention = append(s.attention, summaryAttention{
				priority: 1,
				title:    runSummaryName(r),
				detail:   "waiting for GitHub, approval or a runner",
			})
		case jobs.JobFailed:
			s.failedRuns++
			s.attention = append(s.attention, summaryAttention{
				priority: 0,
				title:    runSummaryName(r),
				detail:   "failed in the current snapshot",
			})
		}
		if r.JobsError != "" {
			s.attention = append(s.attention, summaryAttention{
				priority: 1,
				title:    runSummaryName(r),
				detail:   "job details unavailable",
			})
		}
	}

	for _, r := range m.Runners {
		switch r.Status {
		case jobs.RunnerRunning:
			s.runnerBusy++
		case jobs.RunnerIdle:
			s.runnerIdle++
		case jobs.RunnerOffline:
			s.runnerOffline++
		case jobs.RunnerMaintenance:
			s.runnerMaintenance++
		default:
			s.runnerUnknown++
		}
	}

	if m.JobQueueFetchFailed {
		detail := m.ActionsCoverage
		if detail == "" {
			detail = "some Actions data could not be loaded"
		}
		s.attention = append(s.attention, summaryAttention{priority: 0, title: "Actions snapshot incomplete", detail: detail})
	}
	if m.RunnerFetchFailed && !m.RunnerPermissionDenied {
		s.attention = append(s.attention, summaryAttention{priority: 1, title: "Runner snapshot unavailable", detail: "press r to retry"})
	}
	if m.RunnerPermissionDenied {
		s.attention = append(s.attention, summaryAttention{priority: 2, title: "Runner fleet totals unavailable", detail: "showing observed assignments only"})
	}

	sort.SliceStable(s.attention, func(i, j int) bool {
		if s.attention[i].priority != s.attention[j].priority {
			return s.attention[i].priority < s.attention[j].priority
		}
		return s.attention[i].title < s.attention[j].title
	})
	return s
}

func runSummaryName(r *jobs.RunItem) string {
	name := strings.TrimSpace(r.Repo + " / " + r.Workflow)
	if r.Number > 0 {
		return fmt.Sprintf("%s #%d", name, r.Number)
	}
	return name
}

func (m Model) summaryContent(width, height int) string {
	s := m.buildOrgSummary()
	owner := m.TargetOrg
	if owner == "" {
		owner = git.ShortenHomePath(m.TargetDir)
	}
	if owner == "" {
		owner = "Local workspace"
	}

	title := th.strong.Render(owner)
	headline := m.summaryHeadline(s)
	if width < 52 || height < 18 {
		repositoriesSummary := fmt.Sprintf("%d local, %d not cloned, %d need attention", s.localRepos, s.newRepos, s.repoProblems)
		if m.TargetOrg != "" && s.countsStale > 0 {
			repositoriesSummary += ", review counts stale"
		} else if m.TargetOrg != "" && (s.countsLoaded < s.repositories || m.RepoCountsFetchFailed) {
			repositoriesSummary += ", review counts partial"
		}
		if m.OrgRefreshFailed {
			repositoriesSummary += ", snapshot unavailable"
		}
		if m.LocalScanFailed {
			repositoriesSummary += ", local metadata partial"
		}
		return summaryFit(strings.Join([]string{
			title,
			headline,
			compactSummaryLine("Repositories", repositoriesSummary, width),
			compactSummaryLine("Actions", m.compactActionSummary(s), width),
			compactSummaryLine("Runners", m.compactRunnerSummary(s), width),
			m.firstAttentionLine(s, width),
		}, "\n"), width, height)
	}

	titleRow := " " + title + "  " + th.dim.Render("workspace overview")
	if s.repositories == 0 && !m.IsOrgSyncing && !m.IsJobQueueLoading && !m.IsRunnersLoading {
		lines := []string{titleRow, headline, "", rule("GET STARTED", width), th.strong.Render("No repositories in this workspace")}
		for _, text := range []string{
			"freshen reads sibling checkouts in " + git.ShortenHomePath(m.TargetDir) + ".",
			"Press r to refresh, or start freshen with -dir <path> and an optional -owner.",
			"? lists every key.",
		} {
			lines = append(lines, strings.Split(ansi.Wrap(th.dim.Render(text), width, ""), "\n")...)
		}
		return summaryFit(strings.Join(lines, "\n"), width, height)
	}
	tiles := m.renderSummaryTiles(s, width, height)

	content := []string{titleRow, headline, ""}
	content = append(content, strings.Split(tiles, "\n")...)
	content = append(content, "")
	used := len(content)

	// Two desks: what needs a decision first, then the work already moving.
	// Both columns render only real snapshot rows; empty states say so.
	gap := 3
	leftW := max(24, (width-gap)*3/5)
	rightW := max(18, width-gap-leftW)
	attention := m.attentionPanel(s, leftW, max(3, height-used-2))
	inFlight := m.inFlightPanel(s, rightW, max(3, height-used-2))
	block := lipgloss.JoinHorizontal(lipgloss.Top, attention, strings.Repeat(" ", gap), inFlight)
	content = append(content, strings.Split(block, "\n")...)
	used = len(content)

	remaining := height - used
	if remaining >= 6 {
		content = append(content, "")
		health := m.repoHealthPanel(s, leftW, max(2, remaining/2))
		recent := m.recentPanel(s, rightW, max(2, remaining/2))
		bottom := lipgloss.JoinHorizontal(lipgloss.Top, health, strings.Repeat(" ", gap), recent)
		content = append(content, strings.Split(bottom, "\n")...)
	}

	if used = len(content); height-used >= 11 {
		content = append(content, "")
		avail := height - used - 1
		snapshot := m.snapshotPanel(leftW, avail)
		keys := keysPanel(rightW, avail)
		footer := lipgloss.JoinHorizontal(lipgloss.Top, snapshot, strings.Repeat(" ", gap), keys)
		content = append(content, strings.Split(footer, "\n")...)
	}

	return summaryFit(strings.Join(content, "\n"), width, height)
}

// healthLabel names the header dot in words, so it is never colour alone.
func healthLabel(k statusKind) string {
	switch k {
	case kindFailure:
		return "attention"
	case kindWarning:
		return "partial"
	case kindRunning:
		return "working"
	case kindSuccess:
		return "healthy"
	}
	return "unknown"
}

type summaryTile struct {
	label, figure, detail, tallDetail string
	kind                              statusKind
}

func (m Model) summaryTiles(s orgSummary) []summaryTile {
	loading := m.IsOrgSyncing || m.IsJobQueueLoading || m.IsRunnersLoading
	repos := summaryTile{label: "Repositories", kind: summaryProblemKind(s.repoProblems)}
	switch {
	case loading && len(m.Repos) == 0:
		repos.figure, repos.detail, repos.kind = "Loading", "reading workspace and GitHub", kindRunning
	case m.OrgRefreshFailed && len(m.Repos) == 0:
		repos.figure, repos.detail, repos.kind = "Unavailable", "press r to retry", kindWarning
	default:
		repos.figure = fmt.Sprintf("%d local · %d not cloned", s.localRepos, s.newRepos)
		repos.detail = attentionPhrase(s.repoProblems)
		repos.tallDetail = fmt.Sprintf("%d with local changes · %d archived", s.dirty, s.archived)
		if m.OrgRefreshFailed {
			repos.kind = kindWarning
			repos.tallDetail = "repository snapshot unavailable · press r to retry"
		}
	}

	actions := summaryTile{label: "Actions", kind: summaryProblemKind(s.failedRuns)}
	switch {
	case m.TargetOrg == "":
		actions.figure, actions.detail, actions.kind = "Not configured", "set a GitHub owner", kindUnknown
	case loading && len(m.JobQueue) == 0:
		actions.figure, actions.detail, actions.kind = "Loading", "reading runs and jobs", kindRunning
	case m.JobQueueFetchFailed && len(m.JobQueue) == 0:
		actions.figure, actions.detail, actions.kind = "Actions data unavailable", "press r to retry", kindWarning
	case m.JobQueueFetchFailed:
		actions.figure = fmt.Sprintf("Known: %d running · %d queued", s.runningRuns, s.queuedRuns)
		actions.detail, actions.kind = "Actions data incomplete", kindWarning
	default:
		actions.figure = fmt.Sprintf("%d running · %d queued", s.runningRuns, s.queuedRuns)
		actions.detail = countPhrase(s.failedRuns, "recent failure", "recent failures")
		actions.tallDetail = fmt.Sprintf("%d waiting for a runner, approval or concurrency", s.waitingRuns)
	}

	runners := summaryTile{label: "Runners", kind: summaryProblemKind(s.runnerOffline)}
	switch {
	case m.TargetOrg == "":
		runners.figure, runners.detail, runners.kind = "Not configured", "set a GitHub owner", kindUnknown
	case m.RunnerPermissionDenied:
		runners.figure = fmt.Sprintf("%d observed busy", s.runnerBusy)
		runners.detail, runners.kind = "fleet totals unavailable", kindUnknown
		runners.tallDetail = "showing observed assignments only"
	case loading && len(m.Runners) == 0:
		runners.figure, runners.detail, runners.kind = "Loading", "reading runner fleet", kindRunning
	case m.RunnerFetchFailed && len(m.Runners) == 0:
		runners.figure, runners.detail, runners.kind = "Runner data unavailable", "press r to retry", kindWarning
	case m.RunnerFetchFailed:
		runners.figure = fmt.Sprintf("Known: %d busy · %d idle", s.runnerBusy, s.runnerIdle)
		runners.detail, runners.kind = "showing last observed state", kindWarning
	default:
		runners.figure = fmt.Sprintf("%d busy · %d idle", s.runnerBusy, s.runnerIdle)
		runners.detail = fmt.Sprintf("%d offline · %d maintenance", s.runnerOffline, s.runnerMaintenance)
		runners.tallDetail = fmt.Sprintf("%d unknown availability", s.runnerUnknown)
	}

	concerns := summaryTile{label: "Concerns", kind: kindSuccess}
	switch {
	case m.TargetOrg == "":
		concerns.figure, concerns.detail, concerns.kind = "Not configured", "set a GitHub owner", kindUnknown
	case s.repositories == 0 && loading:
		concerns.figure, concerns.detail, concerns.kind = "Loading", "reading review counts", kindRunning
	case s.countsLoaded == 0 && s.repositories > 0 && loading:
		concerns.figure, concerns.detail, concerns.kind = "Loading", "reading review counts", kindRunning
	case s.countsLoaded == 0 && s.repositories > 0:
		concerns.figure, concerns.detail, concerns.kind = "Counts unavailable", "press r to retry", kindWarning
	default:
		concerns.figure = fmt.Sprintf("%d open PRs · %d open issues", s.openPRs, s.openIssues)
		concerns.detail = fmt.Sprintf("%d ranked", s.concernRepos)
		concerns.tallDetail = fmt.Sprintf("review counts for %d/%d repositories", s.countsLoaded, s.repositories)
		if s.concernRepos > 0 {
			concerns.kind = kindWarning
		}
	}
	return []summaryTile{repos, actions, runners, concerns}
}

// renderSummaryTiles lays out four operational tiles: four across on wide
// terminals, two by two in the middle, and one per line on narrow ones.
func (m Model) renderSummaryTiles(s orgSummary, width, height int) string {
	tiles := m.summaryTiles(s)
	tall := height >= 34
	render := func(t summaryTile, w int) string {
		lines := []string{tileHeader(t.label, t.kind, w), figureLine(t.figure, w), statusLine(t.kind, t.detail, w)}
		if tall && t.tallDetail != "" {
			lines = append(lines, fitLine(th.dim.Render(t.tallDetail), w))
		}
		return strings.Join(lines, "\n")
	}
	if width >= 112 {
		gap := 3
		w := (width - gap*3) / 4
		last := width - w*3 - gap*3
		return lipgloss.JoinHorizontal(lipgloss.Top,
			render(tiles[0], w), strings.Repeat(" ", gap),
			render(tiles[1], w), strings.Repeat(" ", gap),
			render(tiles[2], w), strings.Repeat(" ", gap),
			render(tiles[3], last))
	}
	gap := 3
	w := (width - gap) / 2
	last := width - w - gap
	return lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.JoinHorizontal(lipgloss.Top, render(tiles[0], w), strings.Repeat(" ", gap), render(tiles[1], last)),
		"",
		lipgloss.JoinHorizontal(lipgloss.Top, render(tiles[2], w), strings.Repeat(" ", gap), render(tiles[3], last)))
}

// tileHeader is the tile label, rule and status glyph on one line.
func tileHeader(label string, kind statusKind, width int) string {
	g := th.glyph(kind)
	label = th.tileLabel.Render(label)
	used := ansi.StringWidth(label) + 2 + ansi.StringWidth(g)
	if used >= width {
		return fitLine(label, width)
	}
	return label + " " + th.divider.Render(strings.Repeat("─", width-used)) + " " + g
}

// figureLine gives the first number the strongest weight and keeps its unit
// and companions muted, so the eye lands on the count first.
func figureLine(figure string, width int) string {
	parts := strings.SplitN(figure, " ", 2)
	line := th.strong.Render(parts[0])
	if len(parts) == 2 {
		line += " " + th.dim.Render(parts[1])
	}
	return fitLine(line, width)
}

// statusLine is a coloured glyph plus a muted phrase, never colour alone.
func statusLine(kind statusKind, detail string, width int) string {
	return fitLine(th.glyph(kind)+" "+th.dim.Render(detail), width)
}

func (m Model) attentionPanel(s orgSummary, width, limit int) string {
	lines := []string{panelHeader("Needs attention", len(s.attention), width)}
	rows := strings.Split(m.summaryAttentionRows(s, width, max(1, limit-1)), "\n")
	lines = append(lines, rows...)
	return strings.Join(lines, "\n")
}

func (m Model) inFlightPanel(_ orgSummary, width, limit int) string {
	var rows []string
	for _, run := range m.actionRuns() {
		if run.run.Status != jobs.JobRunning && run.run.Status != jobs.JobWaiting && run.run.Status != jobs.JobQueued {
			continue
		}
		title := runSummaryName(run.run)
		var detail string
		switch {
		case width < 55 && (run.run.Status == jobs.JobRunning || run.run.Status == jobs.JobQueued || run.run.Status == jobs.JobWaiting):
			if run.run.Status == jobs.JobRunning {
				if len(run.jobs) == 0 {
					detail = "jobs not reported"
				} else {
					detail = fmt.Sprintf("%d/%d jobs", completion(run.jobs), len(run.jobs))
				}
			} else if !run.run.CreatedAt.IsZero() {
				detail = "waiting " + compactAgo(time.Since(run.run.CreatedAt))
			} else {
				detail = string(run.run.Status)
			}
		default:
			detail = m.countBar(completion(run.jobs), len(run.jobs), "jobs")
			if width >= 70 {
				if timing := runTiming(run.run, time.Now()); timing != "" {
					detail += " · " + timing
				}
			}
		}
		rows = append(rows, joinEnds(th.status(jobKind(run.run.Status), title), th.dim.Render(detail), width))
	}
	for _, r := range m.Runners {
		if r.Status != jobs.RunnerRunning {
			continue
		}
		detail := r.Platform
		if j := findJobForRunner(r, m.JobQueue); j != nil {
			detail = "running " + j.Name
		}
		rows = append(rows, joinEnds(th.status(kindRunning, r.Name), th.dim.Render(detail), width))
	}
	total := len(rows)
	header := panelHeader("In flight", total, width)
	if total == 0 {
		if m.IsJobQueueLoading || m.IsRunnersLoading || m.IsOrgSyncing {
			rows = append(rows, m.Spinner.View()+th.strong.Render("Loading runs and runners"))
		} else {
			rows = append(rows, th.status(kindSuccess, "No runs in flight"), th.dim.Render("The snapshot shows no running or waiting work."))
		}
		return strings.Join(append([]string{header}, rows...), "\n")
	}
	maxRows := max(1, limit-1)
	if total > maxRows {
		keep := max(0, maxRows-1)
		rows = rows[:keep]
		rows = append(rows, th.dim.Render(fmt.Sprintf("+ %d more", total-keep)))
	}
	return strings.Join(append([]string{header}, rows...), "\n")
}
func (m Model) repoHealthPanel(s orgSummary, width, limit int) string {
	lines := []string{panelHeader("Repository health", s.repositories, width)}
	for i, r := range m.Repos {
		if i >= max(1, limit-1) {
			if len(m.Repos) > i {
				lines = append(lines, th.dim.Render(fmt.Sprintf("+ %d more repositories", len(m.Repos)-i)))
			}
			break
		}
		kind, label := repoKind(r.Status)
		if r.IsNew {
			kind, label = kindUnknown, "not cloned"
		}
		detail := r.CurrentBranch
		if r.IsNew || detail == "" {
			detail = "not cloned"
		}
		if r.HasLoadedCounts {
			detail += fmt.Sprintf(" · %s · %s", countPhrase(r.OpenPRsCount, "PR", "PRs"), countPhrase(r.OpenIssuesCount, "issue", "issues"))
		} else {
			detail += " · counts not loaded"
		}
		lines = append(lines, joinLeft(th.status(kind, r.Name), th.dim.Render(label+"  "+detail), width, width*2/5))
	}
	if len(m.Repos) == 0 {
		if m.IsOrgSyncing {
			lines = append(lines, m.Spinner.View()+th.dim.Render("Loading repositories"))
		} else {
			lines = append(lines, th.dim.Render("No repositories in this snapshot."))
		}
	}
	return strings.Join(lines, "\n")
}

// snapshotPanel states where the data came from and how often it refreshes,
// so a quiet dashboard still explains itself on a tall terminal.
func (m Model) snapshotPanel(width, limit int) string {
	lines := []string{panelHeader("Snapshot", 0, width)}
	row := func(label, value string) string {
		return joinLeft(th.dim.Render(label), th.body.Render(fitLine(value, width)), width, min(14, width/3))
	}
	owner := m.TargetOrg
	if owner == "" {
		owner = "local workspace"
	}
	lines = append(lines,
		row("workspace", git.ShortenHomePath(m.TargetDir)),
		row("owner", owner),
		row("refreshed", m.freshness()),
		row("intervals", fmt.Sprintf("repos %s · CI %s · runners %s", compactAgo(repoTickInterval), compactAgo(m.actionsPollInterval()), compactAgo(runnerJobTickInterval))),
	)
	if limit > 0 && len(lines) > limit {
		lines = lines[:limit]
	}
	return strings.Join(lines, "\n")
}

// keysPanel is the dashboard's short key reference; ? still lists every
// binding for the current screen.
func keysPanel(width, limit int) string {
	lines := []string{panelHeader("Keys", 0, width)}
	rows := [][2]string{
		{"0–4", "switch screen"},
		{"enter", "open the selection"},
		{"space", "actions for the selection"},
		{"s", "sync or clone"},
		{"v / f", "job queue / run filter"},
		{"[ / ]", "detail tabs"},
		{"r", "refresh this screen"},
		{"?", "every key and the status legend"},
	}
	for _, r := range rows {
		if limit > 0 && len(lines) >= limit {
			break
		}
		lines = append(lines, joinLeft(th.keyPrimary.Render(r[0]), th.dim.Render(r[1]), width, 6))
	}
	return strings.Join(lines, "\n")
}

func (m Model) recentPanel(_ orgSummary, width, limit int) string {
	var runs []actionRun
	for _, run := range m.actionRuns() {
		if terminalStatus(run.run.Status) {
			runs = append(runs, run)
		}
	}
	sort.SliceStable(runs, func(i, j int) bool {
		ti, tj := runFinished(runs[i].run), runFinished(runs[j].run)
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return runs[i].run.ID > runs[j].run.ID
	})
	lines := []string{panelHeader("Recent runs", len(runs), width)}
	maxRows := max(1, limit-1)
	shown := 0
	for _, run := range runs {
		if shown >= maxRows {
			break
		}
		detail := runTiming(run.run, time.Now())
		if detail == "" {
			detail = "finished"
		}
		lines = append(lines, joinEnds(th.status(jobKind(run.run.Status), runSummaryName(run.run)), th.dim.Render(detail), width))
		shown++
	}
	if shown == 0 {
		if m.IsJobQueueLoading {
			lines = append(lines, m.Spinner.View()+th.dim.Render("Loading recent runs"))
		} else {
			lines = append(lines, th.dim.Render("No finished runs in this snapshot."))
		}
		return strings.Join(lines, "\n")
	}
	if shown < len(runs) {
		if shown == maxRows {
			// The final row is the overflow notice, so the count matches the
			// visible rows above it.
			lines = lines[:len(lines)-1]
			shown--
		}
		lines = append(lines, th.dim.Render(fmt.Sprintf("+ %d more finished runs", len(runs)-shown)))
	}
	return strings.Join(lines, "\n")
}

// runFinished is the time a terminal run finished, falling back to its start
// or creation time when GitHub omits a completion timestamp.
func runFinished(r *jobs.RunItem) time.Time {
	switch {
	case !r.CompletedAt.IsZero():
		return r.CompletedAt
	case !r.StartedAt.IsZero():
		return r.StartedAt
	}
	return r.CreatedAt
}

// attentionPhrase keeps the tile copy grammatical for one and many.
func attentionPhrase(n int) string {
	if n == 1 {
		return "1 needs attention"
	}
	return fmt.Sprintf("%d need attention", n)
}

// panelHeader is a section title with its count right-aligned on the rule.
func panelHeader(title string, count, width int) string {
	countText := ""
	if count > 0 {
		countText = fmt.Sprintf(" %d ", count)
	}
	label := th.tileLabel.Render(strings.ToUpper(title))
	used := ansi.StringWidth(label) + ansi.StringWidth(countText)
	if used >= width {
		return fitLine(label, width)
	}
	return label + " " + th.divider.Render(strings.Repeat("─", max(0, width-used-1))) + th.dim.Render(countText)
}
func (m Model) summaryHeadline(s orgSummary) string {
	base := fmt.Sprintf("%d repositories", s.repositories)
	if s.archived > 0 {
		base += fmt.Sprintf(" · %d archived", s.archived)
	}
	var headline string
	if m.OrgRefreshFailed {
		headline = base + " · known snapshot · " + m.orgRefreshFailureDetail()
	} else if m.TargetOrg == "" {
		headline = base + " · GitHub owner not configured"
	} else if s.repositories == 0 {
		if m.IsOrgSyncing {
			headline = base + " · loading repository snapshot"
		} else {
			headline = base
		}
	} else if s.countsStale > 0 {
		headline = fmt.Sprintf("%s · known: %d open PRs · %d open issues · last successful refresh for %d", base, s.openPRs, s.openIssues, s.countsStale)
	} else if m.RepoCountsFetchFailed {
		headline = fmt.Sprintf("%s · known: %d open PRs · %d open issues · count refresh incomplete", base, s.openPRs, s.openIssues)
	} else if s.countsLoaded == s.repositories {
		headline = fmt.Sprintf("%s · %d open PRs · %d open issues", base, s.openPRs, s.openIssues)
	} else if s.countsLoaded > 0 {
		headline = fmt.Sprintf("%s · PR and issue counts loaded for %d/%d", base, s.countsLoaded, s.repositories)
	} else {
		headline = base + " · PR and issue counts loading"
	}
	if m.LocalScanFailed {
		headline += " · local metadata incomplete"
	}
	return th.dim.Render(headline)
}

func (m Model) orgRefreshFailureDetail() string {
	if m.LastOrgRefresh.IsZero() {
		return "no successful refresh"
	}
	return "last successful refresh " + jobs.FormatDuration(time.Since(m.LastOrgRefresh)) + " ago"
}
func (m Model) compactRunnerSummary(s orgSummary) string {
	if m.TargetOrg == "" {
		return "not configured"
	}
	if m.RunnerPermissionDenied {
		return fmt.Sprintf("%d observed busy, fleet totals unavailable", s.runnerBusy)
	}
	if m.IsRunnersLoading && len(m.Runners) == 0 {
		return "loading"
	}
	if m.RunnerFetchFailed {
		if len(m.Runners) == 0 {
			return "data unavailable — press r"
		}
		return fmt.Sprintf("known: %d busy, %d idle — data unavailable", s.runnerBusy, s.runnerIdle)
	}
	return fmt.Sprintf("%d busy, %d idle, %d offline", s.runnerBusy, s.runnerIdle, s.runnerOffline)
}

func (m Model) compactActionSummary(s orgSummary) string {
	if m.TargetOrg == "" {
		return "not configured"
	}
	if m.IsJobQueueLoading && len(m.JobQueue) == 0 {
		return "loading"
	}
	if m.JobQueueFetchFailed {
		if len(m.JobQueue) == 0 {
			return "data unavailable — press r"
		}
		return fmt.Sprintf("known: %d running, %d queued — data incomplete", s.runningRuns, s.queuedRuns)
	}
	return fmt.Sprintf("%d running, %d queued, %d failed", s.runningRuns, s.queuedRuns, s.failedRuns)
}

func (m Model) firstAttentionLine(s orgSummary, width int) string {
	if len(s.attention) == 0 {
		if m.IsOrgSyncing || m.IsJobQueueLoading || m.IsRunnersLoading {
			return compactSummaryLine("Snapshot", "still loading", width)
		}
		return compactSummaryLine("Attention", "nothing in this snapshot", width)
	}
	a := s.attention[0]
	return compactSummaryLine(fmt.Sprintf("Attention (1 of %d)", len(s.attention)), a.title+" · "+a.detail, width)
}

func (m Model) summaryAttentionRows(s orgSummary, width, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s.attention) == 0 {
		if m.IsOrgSyncing || m.IsJobQueueLoading || m.IsRunnersLoading {
			return summaryLoading(m, "Snapshot still loading")
		}
		return summaryMetric(kindSuccess, "Nothing needs attention", "in this snapshot")
	}
	var fails, review []summaryAttention
	for _, a := range s.attention {
		if a.priority == 0 {
			fails = append(fails, a)
		} else {
			review = append(review, a)
		}
	}
	maxReview := 6
	grouped := width >= 60 && limit >= 5 && len(fails) > 0 && len(review) > 0
	var rows []string
	add := func(a summaryAttention) {
		kind := kindWarning
		if a.priority == 0 {
			kind = kindFailure
		}
		rows = append(rows, summaryFit(summaryMetric(kind, a.title, a.detail), width, 1))
	}
	if grouped {
		rows = append(rows, summaryFit(th.tileLabel.Render(fmt.Sprintf("FAILURES (%d)", len(fails))), width, 1))
		for i, a := range fails {
			if len(rows) >= limit-1 {
				rows = append(rows, summaryFit(th.dim.Render(fmt.Sprintf("+ %d more", len(fails)-i)), width, 1))
				return strings.Join(rows, "\n")
			}
			add(a)
		}
		if len(review) > 0 && len(rows) < limit-1 {
			rows = append(rows, summaryFit(th.tileLabel.Render(fmt.Sprintf("REVIEW (%d)", len(review))), width, 1))
			shown := 0
			for _, a := range review {
				if shown >= maxReview || len(rows) >= limit-1 {
					break
				}
				add(a)
				shown++
			}
			if remaining := len(review) - shown; remaining > 0 {
				rows = append(rows, summaryFit(th.dim.Render(fmt.Sprintf("+ %d more", remaining))+"  "+th.dim.Render("use 1–4 for details"), width, 1))
			}
		}
		return strings.Join(rows, "\n")
	}

	renderCount := min(limit, len(s.attention))
	if len(s.attention) > limit {
		renderCount = max(0, limit-1)
	}
	for _, a := range s.attention[:renderCount] {
		add(a)
	}
	if remaining := len(s.attention) - renderCount; remaining > 0 {
		rows = append(rows, summaryFit(th.dim.Render("+ ")+th.strong.Render(fmt.Sprintf("%d more", remaining))+"  "+th.dim.Render("use 1–4 for details"), width, 1))
	}
	return strings.Join(rows, "\n")
}
func summaryMetric(kind statusKind, title, detail string) string {
	line := th.glyph(kind) + " " + th.strong.Render(title)
	if detail != "" {
		line += "  " + th.dim.Render(detail)
	}
	return line
}

func summaryLoading(m Model, title string) string {
	return m.Spinner.View() + th.strong.Render(title)
}

func summaryProblemKind(count int) statusKind {
	if count > 0 {
		return kindFailure
	}
	return kindSuccess
}

func compactSummaryLine(label, value string, width int) string {
	line := th.strong.Render(label) + "  " + th.dim.Render(value)
	return summaryFit(line, width, 1)
}

func summaryFit(content string, width, height int) string {
	lines := strings.Split(content, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], max(0, width), "…")
	}
	return strings.Join(lines, "\n")
}
