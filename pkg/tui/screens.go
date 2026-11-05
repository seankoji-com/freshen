package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/seankoji-com/freshen/pkg/git"
	"github.com/seankoji-com/freshen/pkg/jobs"
)

// screenEntry is shared by rendering, navigation and mouse hit testing. Keys
// survive polling and sorting; index resolves the current backing slice only.
type screenEntry struct {
	key, title, subtitle string
	status, item, detail string
	prs, issues          string
	index                int
}
type actionRun struct {
	run  *jobs.RunItem
	jobs []*jobs.JobItem
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
		ti, tj := runFinished(runs[i].run), runFinished(runs[j].run)
		if !ti.Equal(tj) {
			return ti.After(tj)
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
func stepKind(s jobs.GHJobStep) statusKind {
	switch {
	case s.Status == "in_progress":
		return kindRunning
	case s.Status == "queued" || s.Status == "pending" || s.Status == "waiting":
		return kindQueued
	case s.Conclusion == "success":
		return kindSuccess
	case s.Conclusion == "failure" || s.Conclusion == "timed_out":
		return kindFailure
	case s.Conclusion == "skipped" || s.Conclusion == "cancelled":
		return kindSkipped
	}
	return kindUnknown
}

// stepLabel turns GitHub's API values into the same vocabulary as job states.
func stepLabel(s jobs.GHJobStep) string {
	switch s.Status {
	case "in_progress":
		return "running"
	case "completed":
		switch s.Conclusion {
		case "success":
			return "passed"
		case "failure", "timed_out":
			return "failed"
		case "skipped":
			return "skipped"
		case "cancelled":
			return "cancelled"
		}
		return "completed"
	case "queued", "pending":
		return "queued"
	case "waiting":
		return "waiting"
	}
	return s.Status
}

// failedStep names the first failing step GitHub reported for a job, the
// quickest pointer to why a run failed.
func failedStep(j *jobs.JobItem) string {
	for _, s := range j.Steps {
		if stepKind(s) == kindFailure {
			return fmt.Sprintf("%s failed at step %d: %s", statusGlyphs[kindFailure], s.Number, s.Name)
		}
	}
	return ""
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

// stateBadge renders a job or run status as glyph, label and colour. Labels
// stay lowercase so they read like repository states.
func stateBadge(s jobs.JobStatus) string {
	return th.status(jobKind(s), jobLabel(s))
}

func jobLabel(s jobs.JobStatus) string {
	switch s {
	case jobs.JobPassed:
		return "passed"
	case jobs.JobFailed:
		return "failed"
	case jobs.JobRunning:
		return "running"
	case jobs.JobQueued:
		return "queued"
	case jobs.JobWaiting:
		return "waiting"
	case jobs.JobCancelled:
		return "cancelled"
	case jobs.JobSkipped:
		return "skipped"
	}
	return "unknown"
}
func (m Model) countBar(done, total int, noun string, kind statusKind) string {
	if total == 0 {
		return noun + " not reported"
	}
	p := th.progressFor(kind)
	p.Width = min(14, max(4, m.Width/8))
	if m.singleLineList() {
		p.Width = 4
	}
	return fmt.Sprintf("%s %d/%d %s complete", p.ViewAs(float64(done)/float64(total)), done, total, noun)
}

// countCell is one fixed-width PR or issue count. An asterisk marks a count
// kept from an earlier refresh.
func countCell(n int, loaded, stale bool) string {
	if !loaded {
		return "—"
	}
	out := strconv.Itoa(n)
	if stale {
		out += "*"
	}
	return out
}

// repoDivergence is the ahead/behind marker for a repository with an upstream.
func repoDivergence(r *git.RepoItem) string {
	if !r.HasUpstream {
		return ""
	}
	switch {
	case r.Ahead > 0 && r.Behind > 0:
		return fmt.Sprintf("↑%d ↓%d", r.Ahead, r.Behind)
	case r.Ahead > 0:
		return fmt.Sprintf("↑%d ahead", r.Ahead)
	case r.Behind > 0:
		return fmt.Sprintf("↓%d behind", r.Behind)
	}
	return "up to date"
}

// ciRunsByRepo indexes the newest observed run per repository once for a
// render, so listing many repositories does not rebuild the whole run list per
// row.
func (m Model) ciRunsByRepo() map[string]*jobs.RunItem {
	latest := make(map[string]*jobs.RunItem)
	for _, ar := range m.actionRuns() {
		r := ar.run
		if r.Repo == "" {
			continue
		}
		prev := latest[r.Repo]
		if prev == nil || r.CreatedAt.After(prev.CreatedAt) || (r.CreatedAt.Equal(prev.CreatedAt) && r.ID > prev.ID) {
			latest[r.Repo] = r
		}
	}
	return latest
}

// ciLabel names one run for the repository table.
func ciLabel(r *jobs.RunItem) string {
	label := fmt.Sprintf("CI %s #%d", statusGlyphs[jobKind(r.Status)], r.Number)
	if r.Number == 0 {
		label = fmt.Sprintf("CI %s run %d", statusGlyphs[jobKind(r.Status)], r.ID)
	}
	if terminalStatus(r.Status) && !r.CompletedAt.IsZero() {
		label += " " + compactAgo(time.Since(r.CompletedAt)) + " ago"
	}
	return label
}

// repoCI names the newest workflow run observed for a repository.
func (m Model) repoCI(repo string) string {
	if repo == "" {
		return ""
	}
	if r := m.ciRunsByRepo()[repo]; r != nil {
		return ciLabel(r)
	}
	return ""
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
					status: th.status(kindUnknown, "unknown"),
					item:   r.Name, prs: "—", issues: "—",
					detail: detail + " · not ranked",
					title:  r.Name + "  " + th.status(kindUnknown, "counts unknown"), subtitle: detail + " · not ranked",
				})
				continue
			}
			badges := make([]string, 0, 2)
			if r.OpenPRsCount > 0 {
				badges = append(badges, th.pr.Render(countPhrase(r.OpenPRsCount, "open PR", "open PRs")))
			}
			if r.OpenIssuesCount > 0 {
				badges = append(badges, th.issue.Render(countPhrase(r.OpenIssuesCount, "open issue", "open issues")))
			}
			detail := r.CurrentBranch
			if detail == "" {
				detail = "not cloned"
			}
			if r.StatusMsg != "" {
				msg := r.StatusMsg
				if _, label := repoKind(r.Status); strings.EqualFold(msg, label) {
					msg = label
				}
				detail += " · " + msg
			}
			if r.LocalMetadataStale {
				detail += " · local metadata from last snapshot"
			}
			if r.CountsStale {
				detail += " · counts from last successful refresh"
			}
			reviewLabel := "review"
			if r.OpenPRsCount > 0 && r.OpenIssuesCount == 0 {
				reviewLabel = "PRs"
			} else if r.OpenIssuesCount > 0 && r.OpenPRsCount == 0 {
				reviewLabel = "issues"
			}
			entries = append(entries, screenEntry{
				key: r.Path + "/" + r.Name, index: i,
				status: th.status(kindWarning, reviewLabel),
				item:   r.Name,
				prs:    countCell(r.OpenPRsCount, r.HasLoadedCounts, r.CountsStale),
				issues: countCell(r.OpenIssuesCount, r.HasLoadedCounts, r.CountsStale),
				detail: detail,
				title:  r.Name + "  " + strings.Join(badges, "  "), subtitle: detail,
			})
		}
	case FocusRepos:
		ciRuns := m.ciRunsByRepo()
		for i, r := range m.Repos {
			base := make([]string, 0, 4)
			switch {
			case r.IsNew:
				base = append(base, "not cloned")
			case r.CurrentBranch != "":
				branch := r.CurrentBranch
				if r.DefaultBranch != "" && r.CurrentBranch != r.DefaultBranch {
					branch += " → " + r.DefaultBranch
				}
				base = append(base, branch)
			}
			if r.HasUnstagedChanges {
				base = append(base, "local changes")
			}
			if r.LocalMetadataStale {
				base = append(base, "local metadata from last snapshot")
			}
			head := ""
			if r.CountsStale {
				head = "counts stale · "
			}
			if r.LocalMetadataStale {
				head = "local metadata stale · " + head
			}
			if div := repoDivergence(r); div != "" {
				head = div + " · " + head
			}
			if _, label := repoKind(r.Status); r.StatusMsg != "" && !strings.EqualFold(r.StatusMsg, label) {
				head += r.StatusMsg + " · "
			}
			ghName := r.GHRepoName
			if ghName == "" {
				ghName = r.Name
			}
			ci := ""
			if run := ciRuns[ghName]; run != nil {
				ci = ciLabel(run)
			}
			if ci != "" {
				head += ci + " · "
			}
			detail := head + strings.Join(base, " · ")
			cardParts := append([]string{}, base...)
			if div := repoDivergence(r); div != "" {
				cardParts = append([]string{div}, cardParts...)
			}
			if ci != "" {
				cardParts = append([]string{ci}, cardParts...)
			}
			if _, label := repoKind(r.Status); r.StatusMsg != "" && !strings.EqualFold(r.StatusMsg, label) {
				cardParts = append([]string{r.StatusMsg}, cardParts...)
			}
			counts := "counts not loaded"
			if r.HasLoadedCounts {
				counts = countPhrase(r.OpenPRsCount, "PR", "PRs") + " · " + countPhrase(r.OpenIssuesCount, "issue", "issues")
				if r.CountsStale {
					counts += " · last successful refresh"
				}
			}
			cardParts = append(cardParts, counts)
			entries = append(entries, screenEntry{key: r.Path + "/" + r.Name, index: i,
				status: m.renderStatusBadge(r), item: r.Name, detail: detail,
				prs:    countCell(r.OpenPRsCount, r.HasLoadedCounts, r.CountsStale),
				issues: countCell(r.OpenIssuesCount, r.HasLoadedCounts, r.CountsStale),
				title:  r.Name + "  " + m.renderStatusBadge(r), subtitle: strings.Join(cardParts, " · ")})
		}
	case FocusRunners:
		for i, r := range m.getMatchingRunners() {
			current := "no assigned job reported"
			if j := findJobForRunner(r, m.JobQueue); j != nil && j.Status == jobs.JobRunning {
				current = "running " + j.Name
				if j.Duration != "" && j.Duration != "-" {
					current += " for " + j.Duration
				}
			}
			kind, label := runnerKind(r.Status)
			subtitle := r.Platform + " · " + current
			if len(r.Tags) > 0 {
				subtitle += " · labels: " + strings.Join(r.Tags, ", ")
			}
			entries = append(entries, screenEntry{key: r.ID, index: i,
				status: th.status(kind, label), item: r.Name, detail: subtitle,
				title: r.Name + "  " + th.status(kind, label), subtitle: subtitle})
		}
	case FocusJobs:
		if m.OpenRun != nil {
			for i, j := range m.runJobs() {
				done, current := stepSummary(j)
				detail := m.countBar(done, len(j.Steps), "steps", jobKind(j.Status))
				if current != "" {
					detail += " · " + current
				}
				if j.Status == jobs.JobQueued || j.Status == jobs.JobWaiting {
					detail = "Not started · labels: " + strings.Join(j.Labels, ", ")
				}
				if failed := failedStep(j); failed != "" {
					detail = strings.TrimPrefix(failed, statusGlyphs[kindFailure]+" ")
				}
				if j.Duration != "" && j.Duration != "-" {
					detail += " · " + j.Duration
				}
				entries = append(entries, screenEntry{key: j.ID, index: i,
					status: stateBadge(j.Status), item: strings.TrimPrefix(j.Name, j.Repo+" / "), detail: detail,
					title: stateBadge(j.Status) + "  " + strings.TrimPrefix(j.Name, j.Repo+" / "), subtitle: detail})
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
					subtitle = m.countBar(done, len(j.Steps), "steps", jobKind(j.Status)) + " · " + current + " · " + subtitle
				}
				if !j.CreatedAt.IsZero() && j.Status == jobs.JobQueued {
					subtitle = "Waiting " + jobs.FormatDuration(time.Since(j.CreatedAt)) + " · " + subtitle
				}
				entries = append(entries, screenEntry{key: j.ID, index: i,
					status: stateBadge(j.Status), item: j.Name, detail: subtitle,
					title: stateBadge(j.Status) + "  " + j.Name, subtitle: subtitle})
			}
		} else {
			for i, r := range m.actionRuns() {
				if m.ActionsFilter == 0 && terminalStatus(r.run.Status) {
					continue
				}
				if m.ActionsFilter == 1 && r.run.Status != jobs.JobFailed && r.run.Status != jobs.JobWaiting {
					continue
				}
				item := fmt.Sprintf("%s / %s  #%d", r.run.Repo, r.run.Workflow, r.run.Number)
				if r.run.Number == 0 {
					item = fmt.Sprintf("%s / %s  run %d", r.run.Repo, r.run.Workflow, r.run.ID)
				}
				title := stateBadge(r.run.Status) + "  " + item
				progress := m.countBar(completion(r.jobs), len(r.jobs), "jobs", jobKind(r.run.Status))
				if !r.run.JobsKnown {
					progress = "Enter to load jobs"
				}
				if r.run.JobsStale {
					progress = "Cached results · refreshing"
				}
				if r.run.JobsError != "" {
					progress = "Job details unavailable · r retries"
				}
				sub := progress
				if timing := runTiming(r.run, time.Now()); timing != "" {
					sub += " · " + timing
				}
				sub += " · " + r.run.Branch + " · " + r.run.Event
				if r.run.Title != "" {
					sub += " · " + r.run.Title
				}
				entries = append(entries, screenEntry{key: runKey(r.run), index: i,
					status: stateBadge(r.run.Status), item: item, detail: sub,
					title: title, subtitle: sub})
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
	n := 0
	if m.concernsCoverageWarning() != "" {
		n += 2
	}
	if m.singleLineList() {
		n++
	}
	return n
}

// singleLineList uses a dense one-row-per-item table on wide terminals. The
// two-line card layout remains the fallback where titles and details would be
// squeezed.
func (m Model) singleLineList() bool {
	if m.Width < 100 || m.detailVisible() || m.ActiveFocus == FocusOverview {
		return false
	}
	return true
}

type tableLayout struct {
	labels []string
	widths []int
}

func (m Model) tableLayout(width int) tableLayout {
	inner := max(20, width-2)
	status := 11
	itemLabel := "REPOSITORY"
	detailLabel := "SIGNALS"
	switch m.ActiveFocus {
	case FocusRepos:
		status = 17
	case FocusRunners:
		status, itemLabel, detailLabel = 13, "RUNNER", "PLATFORM · ASSIGNMENT"
	case FocusConcerns:
		status = 9
		detailLabel = "SIGNAL"
	case FocusJobs:
		itemLabel, detailLabel = "RUN", "PROGRESS · TIMING"
		if m.OpenRun != nil {
			itemLabel, detailLabel = "JOB", "PROGRESS · STATE"
		} else if m.QueueView {
			itemLabel, detailLabel = "JOB", "RUN · WAITING"
		}
	}
	item := min(44, max(26, width/4))
	labels := []string{"STATUS", itemLabel}
	widths := []int{status, item}
	if m.ActiveFocus == FocusRepos || m.ActiveFocus == FocusConcerns {
		labels = append(labels, "PRs", "ISSUES")
		widths = append(widths, 4, 6)
	}
	detail := inner - len(widths)*2
	for _, w := range widths {
		detail -= w
	}
	detail = max(8, detail)
	labels = append(labels, detailLabel)
	widths = append(widths, detail)
	return tableLayout{labels: labels, widths: widths}
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
func (m Model) bodyHeight() int { return max(1, m.Height-headerRows-footerRows) }
func (m Model) visibleEntries() (entries []screenEntry, start, selected int) {
	es := m.entries()
	selected = -1
	if m.selectionKey() != "" {
		selected = m.entryIndex(es)
	}
	reservedRows := m.listHeaderRows()
	var count int
	if m.singleLineList() {
		count = max(1, m.bodyHeight()-reservedRows)
	} else {
		count = max(1, (m.bodyHeight()-reservedRows)/2)
	}
	start = max(0, max(selected, 0)-count+1)
	return es[start:min(len(es), start+count)], start, selected
}
func (m Model) detailVisible() bool { return m.Detail || m.OpenJobID != "" }

// wrapState wraps an empty-state message to the list width instead of letting
// the frame clip it.
func wrapState(s string, width int) string {
	return strings.Join(strings.Split(ansi.Wrap(s, max(1, width), ""), "\n"), "\n")
}

func (m Model) listContent() string {
	return m.listContentWidth(max(10, m.Width-2))
}

func (m Model) listContentWidth(width int) string {
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
		case (m.ActiveFocus == FocusJobs || m.ActiveFocus == FocusRunners) && m.TargetOrg == "":
			return "Actions and runners need a GitHub owner. Restart with -owner NAME, or set \"owner\" in config.json."
		case m.ActiveFocus == FocusJobs && (m.IsJobQueueLoading || m.RunLoading):
			return m.Spinner.View() + " Loading Actions…"
		case m.ActiveFocus == FocusJobs && m.OpenRun != nil && m.OpenRun.JobsError != "":
			return m.OpenRun.JobsError
		case m.ActiveFocus == FocusJobs && m.JobQueueFetchFailed:
			return "Actions unavailable or incomplete: " + m.ActionsCoverage + "\nPress r to retry."
		case m.ActiveFocus == FocusJobs && m.OpenRun != nil:
			return "GitHub has not reported any jobs for this run. Press r to refresh."
		case m.ActiveFocus == FocusJobs && m.QueueView:
			return "No queued or running jobs. Press v to return to runs, or r to refresh."
		case m.ActiveFocus == FocusJobs:
			return fmt.Sprintf("No %s. Press f to show %s, or r to refresh.", strings.ToLower(m.breadcrumb()[1]), []string{"runs needing attention", "recent runs", "active runs"}[m.ActionsFilter])
		case m.ActiveFocus == FocusRunners && m.IsRunnersLoading:
			return m.Spinner.View() + " Loading runners…"
		case m.ActiveFocus == FocusRunners && m.RunnerPermissionDenied:
			return wrapState("Fleet access unavailable. Runners appear here when observed in job assignments.\nRe-authenticate gh with runner read permission, or ask an organization admin.", width)
		case m.ActiveFocus == FocusRunners && m.RunnerFetchFailed:
			return "Runner fetch failed. Press r to retry."
		case m.ActiveFocus == FocusRunners:
			return "No self-hosted runners are registered for " + m.TargetOrg + ". GitHub-hosted runners are not listed.\nPress r to refresh."
		case m.ActiveFocus == FocusRepos:
			return "No repositories found in " + git.ShortenHomePath(m.TargetDir) + ". Check the workspace path, or press r to refresh."
		default:
			return "Nothing to show. Press r to refresh."
		}
	}
	var lines []string
	if coverageWarning != "" {
		lines = append(lines, th.warning.Render(coverageWarning), "")
	}
	if m.singleLineList() {
		layout := m.tableLayout(width)
		lines = append(lines, "  "+columnHeader(layout.labels, layout.widths, 2))
		for i, e := range es {
			cells := []string{e.status, e.item}
			if len(layout.widths) == 5 {
				cells = append(cells, e.prs, e.issues)
			}
			cells = append(cells, e.detail)
			lines = append(lines, m.tableRow(e, i+start == selected, width, layout, cells))
		}
		return strings.Join(lines, "\n")
	}
	for i, e := range es {
		if i+start == selected {
			w := max(10, width)
			lines = append(lines,
				th.cursor.Render("▌")+tint(th.rowSelected, padTo(" "+ansi.Strip(e.title), w-1), w-1),
				" "+tint(th.rowDetail, " "+padTo(ansi.Strip(e.subtitle), max(0, w-3)), max(0, w-1)))
			continue
		}
		lines = append(lines, "  "+e.title, "  "+th.dim.Render(e.subtitle))
	}
	return strings.Join(lines, "\n")
}

// tableRow is one dense list row. The selected row drops nested colours so the
// surface highlight and text contrast survive the whole row.
func (m Model) tableRow(e screenEntry, selected bool, width int, layout tableLayout, cells []string) string {
	var body strings.Builder
	for i, cell := range cells {
		if i > 0 {
			body.WriteString("  ")
		}
		if selected {
			cell = ansi.Strip(cell)
		} else if i == len(cells)-1 {
			cell = th.dim.Render(cell)
		}
		body.WriteString(padTo(cell, layout.widths[i]))
	}
	if selected {
		return th.cursor.Render("› ") + th.rowSelected.Render(padTo(body.String(), max(0, width-2)))
	}
	return "  " + body.String()
}

func fitFrame(content string, width, height int) string {
	lines := strings.Split(content, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], max(0, width), "…")
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
	fmt.Fprintf(&sb, "%s  %s\n%s\n\n", stateBadge(j.Status), j.Name, Hyperlink("Open job in GitHub", m.jobURL(j))+th.dim.Render("  (o)"))
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
	if failed := failedStep(j); failed != "" {
		sb.WriteString(th.toastError.Render(failed) + "\n")
	}
	done, _ := stepSummary(j)
	fmt.Fprintf(&sb, "\n%s\n\n", m.countBar(done, len(j.Steps), "steps", jobKind(j.Status)))
	for _, s := range j.Steps {
		fmt.Fprintf(&sb, "%2d  %s %s\n", s.Number, th.status(stepKind(s), fmt.Sprintf("%-9s", stepLabel(s))), s.Name)
	}
	sb.WriteString("\n" + th.section.Render("Log tail (last 200 lines)") + "\n\n")
	sb.WriteString(m.logStatusLine(j))
	for _, line := range j.Logs {
		sb.WriteString(highlightLogLine(jobs.SanitizeTerminal(line)) + "\n")
	}
	return sb.String()
}

// logStatusLine states the outcome of the open job's latest log request above
// its tail. A tail kept from an earlier fetch is labelled as such, so an error
// or empty answer is never hidden behind older lines.
func (m Model) logStatusLine(j *jobs.JobItem) string {
	kept := len(j.Logs) > 0
	switch {
	case m.LogLoading == j.ID && !kept:
		return m.Spinner.View() + " Loading logs…\n"
	case j.Status == jobs.JobQueued || j.Status == jobs.JobWaiting:
		return "Logs are not available until this job starts.\n"
	case m.LogErrors[j.ID] != "":
		msg := "Log fetch failed: " + jobs.SanitizeTerminal(m.LogErrors[j.ID]) + ". Press r to retry."
		if kept {
			msg += " Showing the previous tail."
		}
		return th.toastError.Render(msg) + "\n"
	case m.EmptyLogs[j.ID] && kept:
		return "GitHub returned no lines on the latest refresh. Showing the previous tail.\n"
	case m.EmptyLogs[j.ID] && j.Status == jobs.JobRunning:
		return "GitHub returned no log lines yet. The tail refreshes while the job runs.\n"
	case m.EmptyLogs[j.ID]:
		return "GitHub returned an empty log for this job. Press r to fetch it again.\n"
	case kept: // a refresh keeps the current tail; the header spinner shows it
		return ""
	default:
		return "Logs not loaded. Press r to retry; GitHub may withhold logs while a job is running.\n"
	}
}

func (m Model) runnerDetailContent() string {
	rs := m.getMatchingRunners()
	if m.SelectedRunnerIndex >= len(rs) {
		return "Runner no longer available. Esc returns to runners."
	}
	r := rs[m.SelectedRunnerIndex]
	kind, label := runnerKind(r.Status)
	content := fmt.Sprintf("%s  %s\n\nPlatform: %s\nLabels: %s\nID: %s\n", th.strong.Render(r.Name), th.status(kind, label), r.Platform, strings.Join(r.Tags, ", "), r.ID)
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
