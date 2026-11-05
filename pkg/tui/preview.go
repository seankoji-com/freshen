package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/seankoji-com/freshen/pkg/jobs"
)

// showPreview adds a read-only detail column on wide terminals, the way
// gh-dash pairs its list with the selected item. Enter still opens the full
// screen; the preview never takes the keyboard.
func (m Model) showPreview() bool {
	if m.Width < 140 || m.detailVisible() || m.ShowHelp || m.MenuOpen || m.PendingAction != "" {
		return false
	}
	return m.ActiveFocus != FocusOverview
}

// previewSplit is the one width calculation shared by rendering and mouse
// hit-testing.
func previewSplit(width int) (listW, previewW, gap int) {
	inner := max(20, width-2)
	gap = 3
	listW = inner * 3 / 5
	previewW = inner - listW - gap
	return listW, previewW, gap
}

func (m Model) listWithPreview() string {
	inner := max(20, m.Width-2)
	listW, previewW, gap := previewSplit(m.Width)
	if previewW < 28 {
		return m.listContentWidth(inner)
	}
	left := padBlock(m.listContentWidth(listW), listW)
	right := padBlock(m.previewPanel(previewW, m.bodyHeight()), previewW)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Repeat(" ", gap), right)
}

func padBlock(s string, width int) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = padTo(lines[i], width)
	}
	return strings.Join(lines, "\n")
}

func (m Model) previewPanel(width, height int) string {
	es := m.entries()
	if len(es) == 0 {
		return m.emptyPreview(width, height)
	}
	e := es[m.entryIndex(es)]
	var lines []string
	switch m.ActiveFocus {
	case FocusRepos, FocusConcerns:
		lines = m.repoPreview(e)
	case FocusJobs:
		lines = m.jobPreview(e)
	case FocusRunners:
		lines = m.runnerPreview(e)
	}
	lines = append([]string{rule("PREVIEW", width)}, lines...)
	if len(lines) > height {
		lines = lines[:max(1, height-1)]
		lines = append(lines, th.dim.Render("…"))
	}
	for i := range lines {
		lines[i] = fitLine(lines[i], width)
	}
	return strings.Join(lines, "\n")
}

func (m Model) emptyPreview(width, height int) string {
	lines := []string{rule("PREVIEW", width), th.dim.Render("Select an item to preview it.")}
	switch {
	case m.ActiveFocus == FocusRunners && m.RunnerPermissionDenied:
		lines[1] = th.dim.Render("Fleet access unavailable. Runners appear here when observed in job assignments.")
	case m.ActiveFocus == FocusJobs && m.TargetOrg == "":
		lines[1] = th.dim.Render("Actions needs a GitHub owner.")
	case m.ActiveFocus == FocusRunners && m.TargetOrg == "":
		lines[1] = th.dim.Render("Runners need a GitHub owner.")
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	for i := range lines {
		lines[i] = fitLine(lines[i], width)
	}
	return padBlock(strings.Join(lines, "\n"), width)
}

func (m Model) repoPreview(e screenEntry) []string {
	if e.index < 0 || e.index >= len(m.Repos) {
		return nil
	}
	r := m.Repos[e.index]
	kind, label := repoKind(r.Status)
	if r.IsNew {
		kind, label = kindUnknown, "not cloned"
	}
	lines := []string{
		th.strong.Render(r.Name) + "  " + th.status(kind, label),
	}
	if r.IsNew {
		lines = append(lines, th.dim.Render("not cloned · press s to clone"))
	} else {
		lines = append(lines, th.dim.Render("branch "+r.CurrentBranch))
		if r.DefaultBranch != "" && r.CurrentBranch != r.DefaultBranch {
			lines = append(lines, th.dim.Render("default "+r.DefaultBranch))
		}
	}
	if r.HasLoadedCounts {
		lines = append(lines, th.pr.Render(countPhrase(r.OpenPRsCount, "open PR", "open PRs"))+" · "+th.issue.Render(countPhrase(r.OpenIssuesCount, "open issue", "open issues")))
	} else {
		lines = append(lines, th.dim.Render("review counts not loaded"))
	}
	ghName := r.GHRepoName
	if ghName == "" {
		ghName = r.Name
	}
	if ci := m.repoCI(ghName); ci != "" {
		lines = append(lines, th.dim.Render(ci))
	}
	if div := repoDivergence(r); div != "" {
		lines = append(lines, th.dim.Render("upstream "+div))
	}
	if r.HasUnstagedChanges {
		lines = append(lines, th.warning.Render(statusGlyphs[kindWarning]+" local changes"))
	}
	if _, label := repoKind(r.Status); r.StatusMsg != "" && !strings.EqualFold(r.StatusMsg, label) {
		lines = append(lines, th.warning.Render(statusGlyphs[kindWarning]+" "+r.StatusMsg))
	}
	if r.CountsStale {
		lines = append(lines, th.warning.Render("review counts from an earlier refresh"))
	}
	if r.LocalMetadataStale {
		lines = append(lines, th.warning.Render("local metadata from an earlier snapshot"))
	}
	if len(r.PRsList) > 0 {
		lines = append(lines, "", th.tileLabel.Render(fmt.Sprintf("Open PRs (%d)", len(r.PRsList))))
		for i, pr := range r.PRsList {
			if i >= 3 {
				break
			}
			lines = append(lines, "  "+th.pr.Render(fmt.Sprintf("#%d", pr.Number))+" "+pr.Title)
		}
	}
	if len(r.IssuesList) > 0 {
		lines = append(lines, "", th.tileLabel.Render(fmt.Sprintf("Open issues (%d)", len(r.IssuesList))))
		for i, issue := range r.IssuesList {
			if i >= 3 {
				break
			}
			lines = append(lines, "  "+th.issue.Render(fmt.Sprintf("#%d", issue.Number))+" "+issue.Title)
		}
	}

	d := r.BranchDetails
	switch {
	case d.Unread:
		lines = append(lines, "", th.warning.Render("Branches not read since the checkout moved"))
	case d.BranchesErr != "" || d.WorktreesErr != "" || d.StatusErr != "":
		lines = append(lines, "", th.warning.Render("Some branch reads failed · enter to retry"))
	case r.IsNew:
		lines = append(lines, "", th.dim.Render("No checkout to read branches."))
	case len(d.LocalBranches)+len(d.Worktrees)+len(d.ChangedFiles) == 0:
		lines = append(lines, "", th.dim.Render("Branches load when the detail opens."))
	default:
		lines = append(lines, "", th.tileLabel.Render("Branches"))
		for i, b := range d.GetLocalBranches() {
			if i >= 4 {
				break
			}
			lines = append(lines, "  "+b)
		}
		if len(d.Worktrees) > 0 {
			lines = append(lines, th.tileLabel.Render("Worktrees"))
			for i, w := range d.Worktrees {
				if i >= 2 {
					break
				}
				lines = append(lines, "  "+w)
			}
		}
		if len(d.ChangedFiles) > 0 {
			lines = append(lines, th.warning.Render(fmt.Sprintf("%d changed files", len(d.ChangedFiles))))
		}
	}
	if len(r.Logs) > 0 {
		lines = append(lines, "", th.tileLabel.Render("Last sync"))
		start := max(0, len(r.Logs)-3)
		for i := start; i < len(r.Logs); i++ {
			lines = append(lines, th.dim.Render(fitLine(r.Logs[i], 60)))
		}
	}
	return lines
}

func (m Model) jobPreview(e screenEntry) []string {
	if m.OpenRun != nil && !m.QueueView {
		jobsForRun := m.runJobs()
		if e.index < 0 || e.index >= len(jobsForRun) {
			return nil
		}
		j := jobsForRun[e.index]
		lines := []string{th.strong.Render(strings.TrimPrefix(j.Name, j.Repo+" / ")) + "  " + stateBadge(j.Status)}
		if j.Run != nil && j.Run.JobsError != "" {
			lines = append(lines, th.warning.Render("job details unavailable: "+j.Run.JobsError))
		} else if j.Run != nil && j.Run.JobsStale {
			lines = append(lines, th.warning.Render("cached jobs from the previous poll; refreshing"))
		}
		if j.RunnerName != "" {
			lines = append(lines, th.dim.Render("on "+j.RunnerName))
		} else {
			lines = append(lines, th.dim.Render("runner not assigned"))
		}
		if j.Duration != "" {
			lines = append(lines, th.dim.Render("elapsed "+j.Duration))
		}
		done, _ := stepSummary(j)
		lines = append(lines, "", th.dim.Render(fmt.Sprintf("%d/%d steps complete", done, len(j.Steps))), th.tileLabel.Render("Steps"))
		for i, s := range j.Steps {
			if i >= 6 {
				lines = append(lines, th.dim.Render("…"))
				break
			}
			lines = append(lines, "  "+th.glyph(stepKind(s))+" "+s.Name)
		}
		if len(j.Logs) > 0 {
			lines = append(lines, "", th.tileLabel.Render("Log tail"))
			for i := max(0, len(j.Logs)-3); i < len(j.Logs); i++ {
				lines = append(lines, th.dim.Render(fitLine(j.Logs[i], 60)))
			}
		}
		return lines
	}
	if m.QueueView {
		if e.index < 0 || e.index >= len(m.JobQueue) {
			return nil
		}
		j := m.JobQueue[e.index]
		lines := []string{th.strong.Render(j.Name) + "  " + stateBadge(j.Status)}
		if j.Run != nil {
			lines = append(lines, th.dim.Render(j.Run.Repo+" / "+j.Run.Workflow+" · "+j.Run.Branch))
		}
		if len(j.Labels) > 0 {
			lines = append(lines, th.dim.Render("labels "+strings.Join(j.Labels, ", ")))
		}
		if j.Status == jobs.JobQueued && !j.CreatedAt.IsZero() {
			lines = append(lines, th.warning.Render("waiting "+jobs.FormatDuration(time.Since(j.CreatedAt))))
		}
		return lines
	}
	runs := m.actionRuns()
	if e.index < 0 || e.index >= len(runs) {
		return nil
	}
	ar := runs[e.index]
	lines := []string{th.strong.Render(runSummaryName(ar.run)) + "  " + stateBadge(ar.run.Status)}
	if t := runTiming(ar.run, time.Now()); t != "" {
		lines = append(lines, th.dim.Render(t))
	}
	lines = append(lines, th.dim.Render(ar.run.Branch+" · "+ar.run.Event))
	if ar.run.Title != "" {
		lines = append(lines, th.dim.Render(ar.run.Title))
	}
	if ar.run.JobsError != "" {
		lines = append(lines, th.warning.Render("job details unavailable: "+ar.run.JobsError))
	} else if ar.run.JobsStale {
		lines = append(lines, th.warning.Render("cached jobs from the previous poll; refreshing"))
	}
	lines = append(lines, "", th.tileLabel.Render("Jobs"))
	if len(ar.jobs) == 0 {
		lines = append(lines, th.dim.Render("Enter loads this run's jobs."))
	}
	for i, j := range ar.jobs {
		if i >= 6 {
			lines = append(lines, th.dim.Render("…"))
			break
		}
		lines = append(lines, "  "+th.glyph(jobKind(j.Status))+" "+strings.TrimPrefix(j.Name, j.Repo+" / "))
	}
	return lines
}

func (m Model) runnerPreview(e screenEntry) []string {
	rs := m.getMatchingRunners()
	if e.index < 0 || e.index >= len(rs) {
		return nil
	}
	r := rs[e.index]
	kind, label := runnerKind(r.Status)
	lines := []string{
		th.strong.Render(r.Name) + "  " + th.status(kind, label),
		th.dim.Render(r.Platform),
	}
	if len(r.Tags) > 0 {
		lines = append(lines, th.dim.Render("labels "+strings.Join(r.Tags, ", ")))
	}
	if j := findJobForRunner(r, m.JobQueue); j != nil {
		lines = append(lines, "", th.tileLabel.Render("Assigned job"), "  "+th.glyph(jobKind(j.Status))+" "+j.Name)
	} else {
		lines = append(lines, "", th.dim.Render("No active job assignment."))
	}
	return lines
}
