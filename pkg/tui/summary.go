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
				detail:   "waiting for GitHub, approval, concurrency or a runner",
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

	title := lipgloss.NewStyle().Bold(true).Foreground(colorText).Render(owner)
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

	repositories := summarySection("Repositories", append([]string{
		summaryMetric(colorGreen, iconSuccess, fmt.Sprintf("%d local", s.localRepos), fmt.Sprintf("%d not cloned", s.newRepos)),
		summaryMetric(summaryProblemColor(s.repoProblems), summaryProblemIcon(s.repoProblems), fmt.Sprintf("%d need attention", s.repoProblems), fmt.Sprintf("%d with local changes", s.dirty)),
	}, m.repositoryConcernRows(s)...), width)
	actions := summarySection("Actions", m.actionSummaryRows(s), width)
	runners := summarySection("Runners", m.runnerSummaryRows(s), width)

	var body string
	if width >= 96 {
		gap := 2
		columnWidth := (width - gap*2) / 3
		lastWidth := width - columnWidth*2 - gap*2
		body = lipgloss.JoinHorizontal(lipgloss.Top,
			summaryFit(repositories, columnWidth, 4), strings.Repeat(" ", gap),
			summaryFit(actions, columnWidth, 4), strings.Repeat(" ", gap),
			summaryFit(runners, lastWidth, 4),
		)
	} else {
		body = lipgloss.JoinVertical(lipgloss.Left, repositories, "", actions, "", runners)
	}

	content := lipgloss.JoinVertical(lipgloss.Left, title, headline, "", body)
	remaining := max(0, height-lipgloss.Height(content))
	if remaining > 0 && remaining < 3 {
		content = lipgloss.JoinVertical(lipgloss.Left, content, m.firstAttentionLine(s, width))
	} else if remaining >= 3 {
		attention := m.summaryAttentionRows(s, width, min(remaining-2, 5))
		content = lipgloss.JoinVertical(lipgloss.Left, content, "", summarySectionHeader("Needs attention", width), attention)
	}
	return summaryFit(content, width, height)
}

func (m Model) repositoryConcernRows(s orgSummary) []string {
	if m.TargetOrg == "" {
		return []string{summaryMetric(colorMuted, iconPending, "Review counts unavailable", "set a GitHub owner")}
	}
	if s.countsLoaded == 0 && s.repositories > 0 {
		if m.IsOrgSyncing {
			return []string{summaryMetric(colorSecondary, m.Spinner.View(), "Loading review counts", "")}
		}
		return []string{summaryMetric(colorMuted, iconPending, "Review counts unavailable", "press r to retry")}
	}
	concerns := s.openPRs + s.openIssues
	colour, icon := colorGreen, iconSuccess
	if concerns > 0 {
		colour, icon = colorYellow, "!"
	}
	prs := countPhrase(s.openPRs, "open PR", "open PRs")
	issues := countPhrase(s.openIssues, "open issue", "open issues")
	detail := issues
	if s.countsLoaded < s.repositories {
		detail += fmt.Sprintf(" · counts from %d/%d repositories", s.countsLoaded, s.repositories)
	}
	if s.countsStale > 0 {
		detail += " · last successful refresh for " + countPhrase(s.countsStale, "repository", "repositories")
	}
	if m.RepoCountsFetchFailed {
		detail += " · refresh incomplete"
	}
	if s.countsLoaded < s.repositories || s.countsStale > 0 || m.RepoCountsFetchFailed {
		return []string{summaryMetric(colour, icon, "Known: "+prs, detail)}
	}
	return []string{summaryMetric(colour, icon, prs, issues)}
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
	return lipgloss.NewStyle().Foreground(colorMuted).Render(headline)
}

func (m Model) orgRefreshFailureDetail() string {
	if m.LastOrgRefresh.IsZero() {
		return "no successful refresh"
	}
	return "last successful refresh " + jobs.FormatDuration(time.Since(m.LastOrgRefresh)) + " ago"
}

func (m Model) actionSummaryRows(s orgSummary) []string {
	if m.TargetOrg == "" {
		return []string{summaryMetric(colorMuted, iconPending, "Not configured", "set a GitHub owner"), ""}
	}
	if m.IsJobQueueLoading && len(m.JobQueue) == 0 {
		return []string{summaryMetric(colorSecondary, m.Spinner.View(), "Loading runs", ""), ""}
	}
	if m.JobQueueFetchFailed {
		if len(m.JobQueue) == 0 {
			return []string{summaryMetric(colorMuted, iconPending, "Actions data unavailable", "press r to retry"), ""}
		}
		return []string{
			summaryMetric(colorSecondary, "●", fmt.Sprintf("Known: %d running", s.runningRuns), fmt.Sprintf("%d queued · %d waiting", s.queuedRuns, s.waitingRuns)),
			summaryMetric(colorYellow, "!", "Actions data incomplete", "press r to retry"),
		}
	}
	return []string{
		summaryMetric(colorSecondary, "●", fmt.Sprintf("%d running", s.runningRuns), fmt.Sprintf("%d queued · %d waiting", s.queuedRuns, s.waitingRuns)),
		summaryMetric(summaryProblemColor(s.failedRuns), summaryProblemIcon(s.failedRuns), fmt.Sprintf("%d recent failures", s.failedRuns), "current snapshot"),
	}
}

func (m Model) runnerSummaryRows(s orgSummary) []string {
	if m.TargetOrg == "" {
		return []string{summaryMetric(colorMuted, iconPending, "Not configured", "set a GitHub owner"), ""}
	}
	if m.RunnerPermissionDenied {
		return []string{
			summaryMetric(colorSecondary, "●", fmt.Sprintf("%d observed busy", s.runnerBusy), "active assignments only"),
			summaryMetric(colorMuted, iconPending, "Fleet totals unavailable", "runner permission required"),
		}
	}
	if m.IsRunnersLoading && len(m.Runners) == 0 {
		return []string{summaryMetric(colorSecondary, m.Spinner.View(), "Loading runners", ""), ""}
	}
	if m.RunnerFetchFailed {
		if len(m.Runners) == 0 {
			return []string{summaryMetric(colorMuted, iconPending, "Runner data unavailable", "press r to retry"), ""}
		}
		return []string{
			summaryMetric(colorSecondary, "●", fmt.Sprintf("Known: %d busy", s.runnerBusy), fmt.Sprintf("%d idle", s.runnerIdle)),
			summaryMetric(colorYellow, "!", "Runner data unavailable", "showing last observed state"),
		}
	}
	return []string{
		summaryMetric(colorSecondary, "●", fmt.Sprintf("%d busy", s.runnerBusy), fmt.Sprintf("%d idle", s.runnerIdle)),
		summaryMetric(summaryProblemColor(s.runnerOffline), summaryProblemIcon(s.runnerOffline), fmt.Sprintf("%d offline", s.runnerOffline), fmt.Sprintf("%d maintenance · %d unknown", s.runnerMaintenance, s.runnerUnknown)),
	}
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
			return summaryMetric(colorSecondary, m.Spinner.View(), "Snapshot still loading", "")
		}
		return summaryMetric(colorGreen, iconSuccess, "Nothing needs attention", "in this snapshot")
	}
	renderCount := min(limit, len(s.attention))
	if len(s.attention) > limit {
		renderCount = max(0, limit-1)
	}
	rows := make([]string, 0, limit)
	for _, a := range s.attention[:renderCount] {
		colour := colorMuted
		icon := iconPending
		switch a.priority {
		case 0:
			colour = colorRed
			icon = iconError
		case 1:
			colour = colorYellow
			icon = "!"
		}
		rows = append(rows, summaryFit(summaryMetric(colour, icon, a.title, a.detail), width, 1))
	}
	if remaining := len(s.attention) - renderCount; remaining > 0 {
		rows = append(rows, summaryFit(summaryMetric(colorMuted, "+", fmt.Sprintf("%d more", remaining), "use 1–4 for details"), width, 1))
	}
	return strings.Join(rows, "\n")
}

func summarySection(title string, rows []string, width int) string {
	lines := append([]string{summarySectionHeader(title, width)}, rows...)
	return summaryFit(strings.Join(lines, "\n"), width, len(lines))
}

func summarySectionHeader(title string, width int) string {
	styledTitle := lipgloss.NewStyle().Bold(true).Foreground(colorText).Render(title)
	remaining := max(0, width-ansi.StringWidth(title)-1)
	return styledTitle + " " + lipgloss.NewStyle().Foreground(colorSurface).Render(strings.Repeat("─", remaining))
}

func summaryMetric(colour lipgloss.Color, icon, title, detail string) string {
	line := lipgloss.NewStyle().Foreground(colour).Render(icon) + " " + lipgloss.NewStyle().Bold(true).Foreground(colorText).Render(title)
	if detail != "" {
		line += "  " + lipgloss.NewStyle().Foreground(colorMuted).Render(detail)
	}
	return line
}

func summaryProblemColor(count int) lipgloss.Color {
	if count > 0 {
		return colorRed
	}
	return colorGreen
}

func summaryProblemIcon(count int) string {
	if count > 0 {
		return iconError
	}
	return iconSuccess
}

func compactSummaryLine(label, value string, width int) string {
	line := lipgloss.NewStyle().Bold(true).Foreground(colorText).Render(label) + "  " + lipgloss.NewStyle().Foreground(colorMuted).Render(value)
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
