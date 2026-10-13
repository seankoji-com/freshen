package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func sized(m Model, w, h int) Model {
	m.handleWindowSizeMsg(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

// A query longer than the input keeps its end and cursor on screen: the input
// is sized against the breadcrumb, so the context row never overflows.
func TestLongFilterQueryKeepsCursorVisible(t *testing.T) {
	query := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcdefghijkl" // 74 cells
	for _, size := range [][2]int{{80, 24}, {200, 50}, {30, 10}} {
		for name, open := range map[string][]string{"repos": {"1"}, "run": {"2", "enter"}} {
			m := sized(smokeFixtureModel(), size[0], size[1])
			for _, k := range open {
				press(&m, k)
			}
			press(&m, "/")
			for _, r := range query {
				press(&m, string(r))
			}
			if m.Search.Value() != query {
				t.Fatalf("%s %v: query = %q", name, size, m.Search.Value())
			}
			row := m.contextRow()
			if w := ansi.StringWidth(row); w > size[0] {
				t.Fatalf("%s %v: context row is %d cells, terminal %d: %q", name, size, w, size[0], stripped(row))
			}
			tail := query[len(query)-min(len(query), m.Search.Width-1):]
			if !strings.Contains(stripped(row), tail) {
				t.Fatalf("%s %v: query end %q missing from %q", name, size, tail, stripped(row))
			}
			frame := strings.Split(stripped(m.View()), "\n")
			if !strings.Contains(frame[2], tail) {
				t.Fatalf("%s %v: rendered row lost the query end: %q", name, size, frame[2])
			}
		}
	}
}

// Long repository and job names shorten the breadcrumb, never the active
// detail tab or the current crumb.
func TestLongNamesKeepActiveTabAndCurrentCrumb(t *testing.T) {
	long := strings.Repeat("very-long-repository-name-", 6)
	for _, size := range [][2]int{{80, 24}, {200, 50}, {30, 10}} {
		m := sized(smokeFixtureModel(), size[0], size[1])
		m.Repos[0].Name = long
		press(&m, "1")
		press(&m, "enter")
		for tab := 0; tab < 4; tab++ {
			row := m.contextRow()
			if w := ansi.StringWidth(row); w > size[0] {
				t.Fatalf("%v tab %d: context row %d cells wide: %q", size, tab, w, stripped(row))
			}
			full, short := detailTabNames[0][m.ActiveTab], detailTabNames[1][m.ActiveTab]
			if s := stripped(row); !strings.Contains(s, full) && !strings.Contains(s, short) {
				t.Fatalf("%v: active tab %q hidden: %q", size, full, s)
			}
			if !strings.Contains(stripped(row), "very") {
				t.Fatalf("%v: current crumb hidden: %q", size, stripped(row))
			}
			press(&m, "]")
		}

		m = sized(smokeFixtureModel(), size[0], size[1])
		m.JobQueue[2].Name = "alpha / " + long
		press(&m, "2")
		press(&m, "enter")
		press(&m, "down")
		press(&m, "enter")
		row := stripped(strings.Split(m.View(), "\n")[2])
		if !strings.Contains(row, "very-long") {
			t.Fatalf("%v: job crumb hidden: %q", size, row)
		}
	}
}

func TestFitCrumbsKeepsCurrentCrumbLongest(t *testing.T) {
	crumbs := []string{"Actions", "very-long-repository / Continuous Integration #4242", "integration-tests"}
	for budget := 1; budget <= 80; budget++ {
		got := strings.Join(fitCrumbs(crumbs, budget), crumbSep)
		if w := ansi.StringWidth(got); w > max(1, budget) {
			t.Fatalf("budget %d: %q is %d cells", budget, got, w)
		}
	}
	if got := fitCrumbs(crumbs, 40); got[len(got)-1] != "integration-tests" {
		t.Fatalf("current crumb shortened before its ancestors: %q", got)
	}
}

// A runner refresh failure stays visible after its toast expires while cached
// runners remain on screen.
func TestRunnerFailureOutlivesToastWithCachedRunners(t *testing.T) {
	m := sized(smokeFixtureModel(), 80, 24)
	press(&m, "3")
	m.handleLoadedRunnersMsg(loadedRunnersMsg{err: errors.New("HTTP 502")})
	next, _ := m.Update(toastExpiredMsg{seq: m.ToastSeq})
	m = next.(Model)
	if m.ToastMsg != "" {
		t.Fatal("toast did not expire")
	}
	frame := stripped(m.View())
	for _, want := range []string{"mac-builder", "Runner refresh failed", "showing runners from 4s ago", "press r to retry", "last refresh failed"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("frame missing %q after toast expiry:\n%s", want, frame)
		}
	}
	press(&m, "0")
	if !strings.Contains(stripped(m.View()), "Runner refresh failed") {
		t.Fatal("Overview hides the runner failure")
	}
	m.handleLoadedRunnersMsg(loadedRunnersMsg{runners: m.Runners})
	if strings.Contains(stripped(m.View()), "Runner refresh failed") {
		t.Fatal("warning survived a successful refresh")
	}
}

// The frame drawn right after a key that starts a repository detail request
// already shows that request loading, never "none found" or "not loaded".
func TestRepositoryDetailShowsLoadingInFirstFrame(t *testing.T) {
	m := sized(smokeFixtureModel(), 80, 24)
	press(&m, "1")
	press(&m, "enter")
	next, _ := m.Update(repoDetailsLoadedMsg{repo: m.Repos[0].Clone()})
	m = next.(Model)
	press(&m, "]") // Branches: a fresh read starts
	if frame := stripped(m.View()); !strings.Contains(frame, "Loading branches and worktrees") || strings.Contains(frame, "none found") {
		t.Fatalf("Branches frame does not show its read loading:\n%s", frame)
	}
	next, _ = m.Update(repoDetailsLoadedMsg{repo: m.Repos[0].Clone()})
	m = next.(Model)
	press(&m, "]") // Issues
	if frame := stripped(m.View()); !strings.Contains(frame, "Loading open issues") || strings.Contains(frame, "not loaded") {
		t.Fatalf("Issues frame does not show its request loading:\n%s", frame)
	}
	press(&m, "]") // PRs
	if frame := stripped(m.View()); !strings.Contains(frame, "Loading open pull requests") || strings.Contains(frame, "not loaded") {
		t.Fatalf("PRs frame does not show its request loading:\n%s", frame)
	}
}

// Opening repository B while A's branch read is in flight queues B; A's
// completion starts B instead of leaving it falsely empty.
func TestRepositoryDetailHandsOffToQueuedRepository(t *testing.T) {
	m := sized(smokeFixtureModel(), 80, 24)
	press(&m, "1")
	press(&m, "enter")
	alpha, beta := m.Repos[0], m.Repos[1]
	if m.RepoDetailLoading != alpha.Path {
		t.Fatalf("alpha read not started: %q", m.RepoDetailLoading)
	}
	press(&m, "esc")
	press(&m, "down")
	press(&m, "enter")
	press(&m, "]")
	if m.RepoDetailQueued != beta.Path {
		t.Fatalf("beta not queued behind alpha: %q", m.RepoDetailQueued)
	}
	if frame := stripped(m.View()); !strings.Contains(frame, "Loading branches") {
		t.Fatalf("queued beta shows no loading state:\n%s", frame)
	}
	next, cmd := m.Update(repoDetailsLoadedMsg{repo: alpha.Clone()})
	m = next.(Model)
	if m.RepoDetailLoading != beta.Path || m.RepoDetailQueued != "" || cmd == nil {
		t.Fatalf("alpha completion did not start beta: loading=%q queued=%q", m.RepoDetailLoading, m.RepoDetailQueued)
	}

	// A queued read is dropped when its detail closes first.
	m = sized(smokeFixtureModel(), 80, 24)
	press(&m, "1")
	press(&m, "enter")
	press(&m, "esc")
	press(&m, "down")
	press(&m, "enter")
	press(&m, "esc")
	next, _ = m.Update(repoDetailsLoadedMsg{repo: m.Repos[0].Clone()})
	m = next.(Model)
	if m.RepoDetailLoading != "" || m.RepoDetailQueued != "" {
		t.Fatalf("closed detail still loading: loading=%q queued=%q", m.RepoDetailLoading, m.RepoDetailQueued)
	}
}

func openCompletedJob(t *testing.T) Model {
	t.Helper()
	m := sized(smokeFixtureModel(), 80, 24)
	press(&m, "2")
	press(&m, "enter")
	press(&m, "enter") // #1, passed
	if m.OpenJobID != "#1" || m.LogLoading != "#1" {
		t.Fatalf("job #1 log request not started: open=%q loading=%q", m.OpenJobID, m.LogLoading)
	}
	return m
}

// A successful but empty log response replaces "Loading logs…" with a
// statement of what GitHub returned.
func TestEmptyLogResponseEndsLoadingState(t *testing.T) {
	m := openCompletedJob(t)
	if !strings.Contains(stripped(m.Viewport.View()), "Loading logs") {
		t.Fatal("log request shows no loading state")
	}
	next, _ := m.Update(loadedJobLogsMsg{jobID: "#1", seq: m.LogSeq})
	m = next.(Model)
	view := stripped(m.Viewport.View() + m.jobDetailContent())
	if strings.Contains(view, "Loading logs") {
		t.Fatalf("empty log left the loading state:\n%s", view)
	}
	if !strings.Contains(view, "GitHub returned an empty log for this job. Press r to fetch it again.") {
		t.Fatalf("empty log not explained:\n%s", view)
	}
}

// Automatic log refreshes for a running job never overlap, and a response
// overtaken by a newer request cannot replace the newer lines.
func TestRunningJobLogRefreshIsSingleFlightAndOrdered(t *testing.T) {
	var calls atomic.Int32
	original := fetchJobLogs
	fetchJobLogs = func(_ context.Context, _, _ string, _, ghJobID int64, _ string, _ int) ([]string, int64, error) {
		n := calls.Add(1)
		return []string{map[int32]string{1: "old tail", 2: "new tail"}[n]}, ghJobID, nil
	}
	t.Cleanup(func() { fetchJobLogs = original })

	m := sized(smokeFixtureModel(), 80, 24)
	press(&m, "2")
	press(&m, "enter")
	press(&m, "down")
	first := press(&m, "enter") // #2, running
	if m.OpenJobID != "#2" || m.LogLoading != "#2" {
		t.Fatalf("running job log request not started: open=%q loading=%q", m.OpenJobID, m.LogLoading)
	}
	var stale []tea.Msg
	for _, msg := range drain(first) {
		if l, ok := msg.(loadedJobLogsMsg); ok {
			stale = append(stale, l)
		}
	}
	if len(stale) != 1 {
		t.Fatalf("opening the job made %d log requests", len(stale))
	}

	// Runner ticks and queue completions while the request is in flight.
	for i := 0; i < 3; i++ {
		drain(m.handleRunnerJobTickMsg())
		drain(m.handleLoadedJobQueueMsg(loadedJobQueueMsg{queue: m.JobQueue}))
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("polls stacked %d log requests behind an unanswered one, want 1", got)
	}

	// The first response never arrives in time; a later tick supersedes it.
	m.LogLoadingSince = time.Now().Add(-logFetchStuckAfter - time.Second)
	var fresh []tea.Msg
	for _, msg := range drain(m.handleRunnerJobTickMsg()) {
		if l, ok := msg.(loadedJobLogsMsg); ok {
			fresh = append(fresh, l)
		}
	}
	if len(fresh) != 1 || calls.Load() != 2 {
		t.Fatalf("stuck request was not superseded: %d responses, %d calls", len(fresh), calls.Load())
	}
	next, _ := m.Update(fresh[0])
	m = next.(Model)
	next, _ = m.Update(stale[0]) // the overtaken response arrives last
	m = next.(Model)
	if logs := m.openJob().Logs; len(logs) != 1 || logs[0] != "new tail" {
		t.Fatalf("overtaken response replaced newer logs: %v", logs)
	}
	if m.LogLoading != "" {
		t.Fatalf("log loading stuck on %q", m.LogLoading)
	}
}

// Help rows wrap to the terminal, so every description is reachable by
// scrolling, and Home/End jump to either end as the README says.
func TestHelpWrapsAndScrollsToEveryDescription(t *testing.T) {
	collapse := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	for _, size := range [][2]int{{30, 10}, {48, 14}, {80, 24}} {
		m := sized(smokeFixtureModel(), size[0], size[1])
		press(&m, "2")
		press(&m, "?")
		help := m.helpContent()
		for _, line := range strings.Split(help, "\n") {
			if w := ansi.StringWidth(line); w > size[0]-2 {
				t.Fatalf("%v: help row %d cells wide: %q", size, w, stripped(line))
			}
		}
		var seen []string
		for i := 0; i < 200 && (i == 0 || m.HelpOffset < len(strings.Split(help, "\n"))-m.bodyHeight()); i++ {
			body := strings.Split(stripped(m.View()), "\n")
			seen = append(seen, body[headerRows:len(body)-footerRows]...)
			press(&m, "down")
		}
		all := collapse(strings.Join(seen, " "))
		for _, g := range m.helpGroups() {
			for _, b := range g.bindings {
				if !strings.Contains(all, collapse(b.Help().Desc)) {
					t.Fatalf("%v: %q unreachable in help", size, b.Help().Desc)
				}
			}
		}
		lines := strings.Split(help, "\n")
		press(&m, "home")
		if m.HelpOffset != 0 {
			t.Fatalf("%v: home left offset %d", size, m.HelpOffset)
		}
		press(&m, "end")
		if want := max(0, len(lines)-m.bodyHeight()); m.HelpOffset != want {
			t.Fatalf("%v: end offset %d, want %d", size, m.HelpOffset, want)
		}
		if !m.ShowHelp {
			t.Fatal("home/end closed help")
		}
	}
}

// The offline demo completes the requests it drops with seeded data, so no
// screen is left loading.
func TestTerminalDemoSettlesDroppedRequests(t *testing.T) {
	d := terminalDemo{smokeFixtureModel()}
	step := func(msg tea.Msg) {
		next, _ := d.Update(msg)
		d = next.(terminalDemo)
	}
	step(tea.WindowSizeMsg{Width: 200, Height: 50})
	for _, k := range []string{"1", "2", "0"} {
		step(keyMsg(k))
	}
	d.LastRunnersAttempt = time.Now().Add(-time.Minute) // outside the poll interval
	step(keyMsg("3"))
	if d.IsRunnersLoading || strings.Contains(stripped(d.View()), "Loading runners") {
		t.Fatal("returning to Runners left the demo loading")
	}
	step(keyMsg("r"))
	step(keyMsg("1"))
	step(keyMsg("enter"))
	step(keyMsg("]"))
	step(keyMsg("]"))
	r := d.Repos[0]
	if d.IsRunnersLoading || d.IsJobQueueLoading || d.IsOrgSyncing || d.RepoDetailLoading != "" || r.IsLoadingIssues {
		t.Fatalf("demo left a request loading: runners=%v queue=%v org=%v detail=%q issues=%v",
			d.IsRunnersLoading, d.IsJobQueueLoading, d.IsOrgSyncing, d.RepoDetailLoading, r.IsLoadingIssues)
	}
	if frame := stripped(d.View()); strings.Contains(frame, "Loading") {
		t.Fatalf("demo frame still loading:\n%s", frame)
	}
}

// Detail text wraps at narrow widths instead of being clipped by the frame,
// and wrapping keeps OSC 8 links intact.
func TestNarrowDetailWrapsInsteadOfClipping(t *testing.T) {
	m := sized(smokeFixtureModel(), 30, 10)
	press(&m, "1")
	press(&m, "enter")
	press(&m, "]")
	content := m.Viewport.View()
	for _, line := range strings.Split(content, "\n") {
		if w := ansi.StringWidth(line); w > m.Viewport.Width {
			t.Fatalf("detail row %d cells wide in a %d-cell viewport: %q", w, m.Viewport.Width, stripped(line))
		}
	}
	if top := stripped(content); !strings.Contains(top, "feat/screens") {
		t.Fatalf("narrow detail lost the branch:\n%s", top)
	}
	m.Viewport.GotoBottom()
	bottom := stripped(m.Viewport.View())
	for _, want := range []string{"Loading branches and", "worktrees"} {
		if !strings.Contains(bottom, want) {
			t.Fatalf("narrow detail lost %q:\n%s", want, bottom)
		}
	}
	if !strings.Contains(content, "\x1b]8;;https://github.com/fixture-org/alpha\x1b\\") {
		t.Fatalf("wrapping broke the repository link: %q", content)
	}
}

// A log request in flight carries one deadline for all its gh calls, and a
// request that outlives it is cancelled, not abandoned, before a replacement
// starts: the slow call returns and its late answer is discarded.
func TestStuckLogRequestIsCancelledBeforeReplacement(t *testing.T) {
	var calls atomic.Int32
	inFlight := make(chan context.Context, 1)
	original := fetchJobLogs
	fetchJobLogs = func(ctx context.Context, _, _ string, _, ghJobID int64, _ string, _ int) ([]string, int64, error) {
		if calls.Add(1) == 1 { // a slow multi-call fallback still running
			inFlight <- ctx
			<-ctx.Done()
			return nil, 0, ctx.Err()
		}
		return []string{"new tail"}, ghJobID, nil
	}
	t.Cleanup(func() { fetchJobLogs = original })

	m := sized(smokeFixtureModel(), 80, 24)
	press(&m, "2")
	press(&m, "enter")
	press(&m, "down")
	open := press(&m, "enter") // #2, running
	if m.LogLoading != "#2" {
		t.Fatalf("running job log request not started: %q", m.LogLoading)
	}
	oldDone := make(chan tea.Msg, 1)
	sendLogResponses(open, oldDone)
	ctx := <-inFlight
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > logFetchTimeout {
		t.Fatalf("log request has no request-wide deadline (ok=%v, %s left)", ok, time.Until(deadline))
	}

	for i := 0; i < 3; i++ {
		drain(m.handleRunnerJobTickMsg())
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("ticks stacked %d requests behind one in flight", got)
	}

	m.LogLoadingSince = time.Now().Add(-logFetchStuckAfter - time.Second)
	replacement := m.handleRunnerJobTickMsg()
	var old tea.Msg
	select {
	case old = <-oldDone:
	case <-time.After(2 * time.Second):
		t.Fatal("the stuck request kept running after its replacement started")
	}
	for _, msg := range drain(replacement) {
		next, _ := m.Update(msg)
		m = next.(Model)
	}
	next, _ := m.Update(old)
	m = next.(Model)
	if logs := m.openJob().Logs; len(logs) != 1 || logs[0] != "new tail" || m.LogLoading != "" || m.LogErrors["#2"] != "" {
		t.Fatalf("late cancelled answer changed state: logs=%v loading=%q err=%q", logs, m.LogLoading, m.LogErrors["#2"])
	}
}

// sendLogResponses runs cmd and every command it batches without a deadline,
// delivering each log response to out, so a request can stay in flight while
// the test drives the model.
func sendLogResponses(cmd tea.Cmd, out chan<- tea.Msg) {
	if cmd == nil {
		return
	}
	go func() {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				sendLogResponses(c, out)
			}
			return
		}
		if l, ok := msg.(loadedJobLogsMsg); ok {
			out <- l
		}
	}()
}

// Only the latest log outcome is shown: a successful empty answer clears an
// earlier error, a kept tail is labelled when a refresh fails or returns
// nothing, and new lines clear both notes.
func TestLogOutcomeTransitions(t *testing.T) {
	m := openCompletedJob(t)
	reply := func(logs []string, err error) string {
		t.Helper()
		next, _ := m.Update(loadedJobLogsMsg{jobID: "#1", seq: m.LogSeq, ghJobID: 11, logs: logs, err: err})
		m = next.(Model)
		return stripped(m.jobDetailContent())
	}
	refetch := func() {
		t.Helper()
		m.startLogFetch(m.openJob())
		if m.LogLoading != "#1" {
			t.Fatal("refetch did not start")
		}
	}
	steps := []struct {
		name      string
		logs      []string
		err       error
		want, not []string
	}{
		{"error", nil, errors.New("HTTP 502"), []string{"Log fetch failed: HTTP 502. Press r to retry."}, []string{"Loading logs", "previous tail"}},
		{"error then empty", nil, nil, []string{"GitHub returned an empty log for this job."}, []string{"HTTP 502", "Loading logs"}},
		{"lines", []string{"line one"}, nil, []string{"line one"}, []string{"empty log", "previous tail"}},
		{"lines then empty", nil, nil, []string{"no lines on the latest refresh. Showing the previous tail.", "line one"}, []string{"empty log for this job"}},
		{"lines then error", nil, errors.New("timeout"), []string{"Log fetch failed: timeout. Press r to retry. Showing the previous tail.", "line one"}, []string{"latest refresh"}},
		{"recovered", []string{"line two"}, nil, []string{"line two"}, []string{"failed", "previous tail", "line one"}},
	}
	for i, step := range steps {
		if i > 0 {
			refetch()
		}
		view := reply(step.logs, step.err)
		for _, w := range step.want {
			if !strings.Contains(view, w) {
				t.Fatalf("%s: missing %q in:\n%s", step.name, w, view)
			}
		}
		for _, n := range step.not {
			if strings.Contains(view, n) {
				t.Fatalf("%s: stale %q in:\n%s", step.name, n, view)
			}
		}
	}
}

// Resizing with help open keeps the stored scroll offset within the rewrapped
// help, so the overlay still fills the body and its line count stays ordered.
func TestHelpOffsetFollowsResize(t *testing.T) {
	m := sized(smokeFixtureModel(), 30, 10)
	press(&m, "1")
	press(&m, "?")
	press(&m, "end")
	if m.HelpOffset == 0 {
		t.Fatal("help did not scroll at 30x10")
	}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(Model)
	lines := strings.Split(m.helpContent(), "\n")
	if want := max(0, len(lines)-m.bodyHeight()); m.HelpOffset != want {
		t.Fatalf("offset %d after resize, want %d", m.HelpOffset, want)
	}
	frame := strings.Split(stripped(m.View()), "\n")
	body := frame[headerRows : len(frame)-footerRows]
	shown := 0
	for _, row := range body {
		if strings.TrimSpace(row) != "" {
			shown++
		}
	}
	if shown < min(len(lines), m.bodyHeight())/2 {
		t.Fatalf("help shows %d of %d body rows after resize:\n%s", shown, m.bodyHeight(), strings.Join(frame, "\n"))
	}
	status := stripped(m.statusLine())
	want := fmt.Sprintf("lines %d–%d of %d", m.HelpOffset+1, len(lines), len(lines))
	if !strings.Contains(status, want) {
		t.Fatalf("status %q, want %q", status, want)
	}
}

// A long repository name cannot push the selected action off the menu: the
// heading is cut to one row and the detail gives way first.
func TestActionMenuKeepsSelectionWithLongName(t *testing.T) {
	long := strings.Repeat("very-long-repository-name-", 4) // 104 cells
	for _, size := range [][2]int{{30, 10}, {31, 11}, {48, 14}, {80, 24}, {200, 50}} {
		m := smokeFixtureModel()
		m.Repos[0].Name = long
		m.Repos[0].GHRepoName = long
		m = sized(m, size[0], size[1])
		press(&m, "1")
		press(&m, " ")
		if !m.MenuOpen {
			t.Fatalf("%v: menu did not open", size)
		}
		actions := m.menuActions()
		for i := range actions {
			frame := stripped(m.View())
			rows := strings.Split(frame, "\n")
			if len(rows) != size[1] {
				t.Fatalf("%v: frame height %d", size, len(rows))
			}
			for _, row := range rows {
				if w := ansi.StringWidth(row); w > size[0] {
					t.Fatalf("%v: row %d cells wide: %q", size, w, row)
				}
			}
			label := ansi.Truncate(actions[i].label, size[0]-5, "")
			if !strings.Contains(frame, "› "+label) {
				t.Fatalf("%v: selected action %q not visible:\n%s", size, actions[i].label, frame)
			}
			press(&m, "down")
		}
	}
}

// An in-flight refresh over cached rows stays visible at every width: when the
// header cannot fit the freshness text it keeps a compact spinner instead.
func TestCompactHeaderKeepsRefreshIndicator(t *testing.T) {
	cases := []struct {
		name  string
		focus string
		set   func(m *Model)
	}{
		{"runners", "3", func(m *Model) { m.IsRunnersLoading = true }},
		{"repos", "1", func(m *Model) { m.IsOrgSyncing = true }},
		{"actions", "2", func(m *Model) { m.IsJobQueueLoading = true }},
		{"overview", "0", func(m *Model) { m.IsRunnersLoading = true }},
		{"long-owner", "3", func(m *Model) { m.TargetOrg = strings.Repeat("an-organisation-", 4); m.IsRunnersLoading = true }},
	}
	for _, size := range [][2]int{{30, 10}, {31, 11}, {48, 14}, {80, 24}, {200, 50}} {
		for _, c := range cases {
			m := sized(smokeFixtureModel(), size[0], size[1])
			press(&m, c.focus)
			c.set(&m)
			lines := strings.Split(m.View(), "\n")
			if len(lines) != size[1] {
				t.Fatalf("%s %v: %d rows", c.name, size, len(lines))
			}
			for i, l := range lines {
				if w := ansi.StringWidth(l); w > size[0] {
					t.Fatalf("%s %v: row %d is %d cells", c.name, size, i, w)
				}
			}
			header := stripped(lines[0])
			if spin := strings.TrimSpace(stripped(m.Spinner.View())); !strings.Contains(header, spin) {
				t.Fatalf("%s %v: header hides the refresh in flight: %q", c.name, size, header)
			}
			if !strings.Contains(header, "freshen") {
				t.Fatalf("%s %v: header lost the app name: %q", c.name, size, header)
			}
			if len(m.Runners) == 0 || len(m.Repos) == 0 {
				t.Fatal("fixture must keep cached rows")
			}
		}
	}
	// Idle screens show no indicator, so the spinner always means work.
	m := sized(smokeFixtureModel(), 30, 10)
	press(&m, "3")
	if header := stripped(strings.Split(m.View(), "\n")[0]); strings.Contains(header, "refreshing") || strings.Contains(header, strings.TrimSpace(stripped(m.Spinner.View()))) {
		t.Fatalf("idle header shows a refresh: %q", header)
	}
}

// The too-small notice names a quit key that works in the current layer:
// pressing exactly what it shows quits, including during a confirmation.
func TestResizeNoticeNamesWorkingQuitKey(t *testing.T) {
	contexts := []struct {
		name, want string
		open       func(m *Model)
	}{
		{"screen", "q quit", func(m *Model) { press(m, "1") }},
		{"help", "q quit", func(m *Model) { press(m, "?") }},
		{"filter", "ctrl+c quit", func(m *Model) { press(m, "1"); press(m, "/") }},
		{"menu", "ctrl+c quit", func(m *Model) { press(m, "1"); press(m, " ") }},
		{"confirm", "ctrl+c quit", func(m *Model) { press(m, "1"); press(m, "X") }},
	}
	for _, c := range contexts {
		m := sized(smokeFixtureModel(), 80, 24)
		c.open(&m)
		m = sized(m, 29, 9)
		frame := stripped(m.View())
		if !strings.Contains(frame, c.want) {
			t.Fatalf("%s: notice does not offer %q:\n%s", c.name, c.want, frame)
		}
		hint := strings.Fields(c.want)[0]
		if !isQuit(pressHandler(&m, hint)) {
			t.Fatalf("%s: the advertised %q did not quit (pending=%q)", c.name, hint, m.PendingAction)
		}
	}
	// The demo quits only through the same bindings, so it cannot hide a mismatch.
	d := terminalDemo{sized(smokeFixtureModel(), 80, 24)}
	next, _ := d.Update(keyMsg("1"))
	next, _ = next.Update(keyMsg("X"))
	if next.(terminalDemo).PendingAction == "" {
		t.Fatal("fixture did not open the prune confirmation")
	}
	if _, cmd := next.Update(keyMsg("q")); isQuit(cmd) {
		t.Fatal("demo quit on q during a confirmation, which production ignores")
	}
	if _, cmd := next.Update(keyMsg("ctrl+c")); !isQuit(cmd) {
		t.Fatal("demo did not quit on ctrl+c")
	}
}
