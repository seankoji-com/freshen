package tui

import (
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/seankoji-com/freshen/pkg/git"
	"github.com/seankoji-com/freshen/pkg/jobs"
)

// uxStates opens every screen, drill-down and layer the frame can show.
var uxStates = []struct {
	name string
	open func(m *Model)
}{
	{"overview", func(m *Model) { press(m, "0") }},
	{"repos", func(m *Model) { press(m, "1") }},
	{"repo-detail", func(m *Model) { press(m, "1"); press(m, "enter") }},
	{"repo-branches", func(m *Model) { press(m, "1"); press(m, "enter"); press(m, "]") }},
	{"actions", func(m *Model) { press(m, "2") }},
	{"queue", func(m *Model) { press(m, "2"); press(m, "v") }},
	{"run", func(m *Model) { press(m, "2"); press(m, "enter") }},
	{"job", func(m *Model) { press(m, "2"); press(m, "enter"); press(m, "down"); press(m, "enter") }},
	{"runners", func(m *Model) { press(m, "3") }},
	{"runner-detail", func(m *Model) { press(m, "3"); press(m, "enter") }},
	{"concerns", func(m *Model) { press(m, "4") }},
	{"help", func(m *Model) { press(m, "2"); press(m, "?") }},
	{"filter", func(m *Model) { press(m, "/"); press(m, "al") }},
	{"menu", func(m *Model) { press(m, " ") }},
	{"confirm", func(m *Model) { press(m, "X") }},
	{"toast", func(m *Model) {
		m.setToast(strings.Repeat("A very long error message that must never push the footer away ", 6), 2)
	}},
}

// Every rendered frame has exactly the terminal's height, no row wider than
// the terminal, the header on the first row and key hints on the last.
func TestFramesFitTerminalExactly(t *testing.T) {
	sizes := [][2]int{{80, 24}, {200, 50}, {30, 10}, {31, 11}, {48, 14}, {72, 20}, {120, 40}, {29, 9}, {12, 4}}
	for _, size := range sizes {
		for _, st := range uxStates {
			m := smokeFixtureModel()
			m.handleWindowSizeMsg(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			st.open(&m)
			frame := m.View()
			lines := strings.Split(frame, "\n")
			if len(lines) != size[1] {
				t.Fatalf("%s %v: %d rows, want %d", st.name, size, len(lines), size[1])
			}
			for i, line := range lines {
				if w := ansi.StringWidth(line); w > size[0] {
					t.Fatalf("%s %v: row %d is %d cells wide: %q", st.name, size, i, w, stripped(line))
				}
			}
			if size[0] < minFrameWidth || size[1] < minFrameHeight {
				continue
			}
			if !strings.Contains(stripped(lines[0]), "freshen") {
				t.Fatalf("%s %v: header missing: %q", st.name, size, stripped(lines[0]))
			}
			last := strings.TrimSpace(stripped(lines[len(lines)-1]))
			hints := m.footerBindings()
			want := hints[len(hints)-1].Help().Key
			if !strings.Contains(last, want) {
				t.Fatalf("%s %v: footer lost its final hint %q: %q", st.name, size, want, last)
			}
		}
	}
}

// Frames at the brief's reference sizes, excerpted for review.
func TestReferenceFramesShowChrome(t *testing.T) {
	m := smokeFixtureModel()
	frame := stripped(renderAt(m, 80, 24))
	lines := strings.Split(frame, "\n")
	for i, want := range map[int]string{0: "freshen  fixture-org", 1: "1 Repositories", 2: "Repositories  ·  2 repositories", 3: "› alpha  ✓ updated", 22: "1 / 2", 23: "? keys"} {
		if !strings.Contains(lines[i], want) {
			t.Fatalf("80x24 row %d = %q, want %q\n%s", i, lines[i], want, frame)
		}
	}
	wide := strings.Split(stripped(renderAt(m, 200, 50)), "\n")
	if !strings.Contains(wide[1], "2 Actions ↻1") || !strings.Contains(wide[0], "updated 2m ago · every 5m") {
		t.Fatalf("200x50 header/tabs missing badges or freshness:\n%s\n%s", wide[0], wide[1])
	}
}

func globalBindings(k keyMap) []key.Binding {
	return []key.Binding{k.ForceQuit, k.Quit, k.Help, k.Back, k.Refresh, k.Filter, k.Jump, k.NextScreen, k.PrevScreen}
}

// Screen-specific keys can never shadow a global key.
func TestScreenKeysDoNotShadowGlobalKeys(t *testing.T) {
	k := baseKeys
	global := map[string]bool{}
	for _, b := range globalBindings(k) {
		for _, s := range b.Keys() {
			global[s] = true
		}
	}
	for _, b := range []key.Binding{k.Up, k.Down, k.PageUp, k.PageDown, k.Home, k.End, k.Open, k.Menu, k.Browser, k.Copy, k.ToggleQueue, k.CycleRuns, k.PrevTab, k.NextTab, k.Sync, k.SyncAll, k.Switch, k.Push, k.Prune, k.Delete} {
		for _, s := range b.Keys() {
			if global[s] {
				t.Fatalf("screen binding %q reuses global key %q", b.Help().Desc, s)
			}
		}
	}
}

// The ? overlay lists exactly the enabled bindings, and each listed screen key
// is one the handler acts on.
func TestHelpOverlayIsGeneratedFromHandledBindings(t *testing.T) {
	for _, st := range uxStates {
		if st.name == "help" || st.name == "filter" || st.name == "menu" || st.name == "confirm" {
			continue
		}
		m := smokeFixtureModel()
		st.open(&m)
		press(&m, "?")
		if !m.ShowHelp {
			t.Fatalf("%s: ? did not open help", st.name)
		}
		help := stripped(m.helpContent())
		under := m
		under.ShowHelp = false
		for _, g := range m.helpGroups() {
			for _, b := range g.bindings {
				if !b.Enabled() || !helpListed(help, b) {
					t.Fatalf("%s: help lists disabled or unrendered binding %q", st.name, b.Help().Desc)
				}
			}
		}
		k := under.keys()
		for _, b := range []key.Binding{k.ToggleQueue, k.CycleRuns, k.PrevTab, k.NextTab, k.Sync, k.Prune, k.Delete, k.Open} {
			if listed := helpListed(help, b); listed != b.Enabled() {
				t.Fatalf("%s: binding %q listed=%v enabled=%v", st.name, b.Help().Desc, listed, b.Enabled())
			}
		}
	}

	m := smokeFixtureModel()
	press(&m, "2")
	if !strings.Contains(stripped(m.helpContent()), "toggle runs / job queue") {
		t.Fatal("Actions help omitted v")
	}
	press(&m, "v")
	if !m.QueueView {
		t.Fatal("v is listed on Actions but did not toggle the queue")
	}
	press(&m, "1")
	if strings.Contains(stripped(m.helpContent()), "toggle runs / job queue") {
		t.Fatal("Repositories help lists the Actions-only v key")
	}
	press(&m, "v")
	if m.QueueView != true || m.ActiveFocus != FocusRepos {
		t.Fatal("unlisted v changed state on Repositories")
	}
}

// helpListed reports whether the overlay has a row for exactly this binding.
func helpListed(help string, b key.Binding) bool {
	for _, line := range strings.Split(help, "\n") {
		fields := strings.TrimSpace(line)
		if strings.HasPrefix(fields, b.Help().Key+" ") && strings.TrimSpace(strings.TrimPrefix(fields, b.Help().Key)) == b.Help().Desc {
			return true
		}
	}
	return false
}

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// Global keys do the same thing from every screen, drill-down and the help overlay.
func TestGlobalKeysBehaveIdenticallyEverywhere(t *testing.T) {
	for _, st := range uxStates {
		if st.name == "filter" || st.name == "menu" || st.name == "confirm" {
			continue
		}
		m := smokeFixtureModel()
		st.open(&m)
		press(&m, "3")
		if m.ActiveFocus != FocusRunners || m.detailVisible() || m.OpenRun != nil || m.ShowHelp {
			t.Fatalf("%s: 3 did not jump straight to the Runners list", st.name)
		}

		m = smokeFixtureModel()
		st.open(&m)
		if !isQuit(pressHandler(&m, "q")) {
			t.Fatalf("%s: q did not quit", st.name)
		}

		m = smokeFixtureModel()
		st.open(&m)
		press(&m, "?")
		if !m.ShowHelp && st.name != "help" {
			t.Fatalf("%s: ? did not open help", st.name)
		}

		m = smokeFixtureModel()
		st.open(&m)
		press(&m, "/")
		if !m.Filtering || m.ActiveFocus == FocusOverview || m.detailVisible() {
			t.Fatalf("%s: / did not focus the nearest list filter (focus=%v detail=%v)", st.name, m.ActiveFocus, m.detailVisible())
		}
	}
}

// Modal layers own the keyboard: only their own keys and Ctrl+C act.
func TestModalLayersSuspendGlobalKeys(t *testing.T) {
	m := smokeFixtureModel()
	press(&m, "/")
	press(&m, "q1")
	if !m.Filtering || m.Search.Value() != "q1" || m.ActiveFocus != FocusRepos {
		t.Fatalf("filter input lost typed keys: filtering=%v value=%q", m.Filtering, m.Search.Value())
	}
	if !isQuit(pressHandler(&m, "ctrl+c")) {
		t.Fatal("ctrl+c did not quit from the filter")
	}
}

// A confirmation can only be accepted with Enter; every other key is inert and
// the captured target is untouched.
func TestConfirmationIgnoresEveryOtherKey(t *testing.T) {
	for _, action := range []string{"X", "d"} {
		m := smokeFixtureModel()
		if action == "d" {
			m.Repos[1].IsArchived = true
			m.Repos[1].Status = git.StatusArchived
			press(&m, "down")
		}
		press(&m, action)
		if m.PendingAction == "" || m.ActionTarget == nil {
			t.Fatalf("%s did not open a confirmation", action)
		}
		target := m.ActionTarget.Path
		for _, k := range []string{"d", "X", "y", "q", "1", "tab", " ", "r", "/", "?", "s", "down"} {
			if cmd := pressHandler(&m, k); cmd != nil || m.BusyAction != "" || m.PendingAction == "" || m.ActionTarget.Path != target {
				t.Fatalf("%s: key %q acted on a pending confirmation", action, k)
			}
		}
		press(&m, "esc")
		if m.PendingAction != "" || m.ActionTarget != nil {
			t.Fatalf("%s: Esc did not cancel", action)
		}
	}
}

func TestBreadcrumbShowsDrillDownPath(t *testing.T) {
	cases := map[string]string{
		"overview":      "Overview",
		"repo-detail":   "Repositories › alpha",
		"actions":       "Actions › Active runs",
		"queue":         "Actions › Job queue",
		"run":           "Actions › alpha / CI #42",
		"job":           "Actions › alpha / CI #42 › test",
		"runner-detail": "Runners › mac-builder",
	}
	for _, st := range uxStates {
		want, ok := cases[st.name]
		if !ok {
			continue
		}
		m := smokeFixtureModel()
		st.open(&m)
		row := strings.Split(stripped(renderAt(m, 120, 30)), "\n")[2]
		if !strings.HasPrefix(strings.TrimSpace(row), want) {
			t.Fatalf("%s breadcrumb = %q, want prefix %q", st.name, row, want)
		}
	}
}

func TestTabRowGeometryDrivesMouse(t *testing.T) {
	for _, width := range []int{40, 80, 200} {
		m := smokeFixtureModel()
		m.handleWindowSizeMsg(tea.WindowSizeMsg{Width: width, Height: 24})
		row := stripped(m.tabRow())
		for _, tab := range m.screenTabs() {
			if got := ansi.Cut(row, tab.start, tab.end); strings.TrimSpace(got) != tab.label {
				t.Fatalf("width %d: span [%d,%d) = %q, want %q", width, tab.start, tab.end, got, tab.label)
			}
			m.screenMouse(tea.MouseMsg{X: tab.start, Y: 1, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
			if m.ActiveFocus != tab.focus {
				t.Fatalf("width %d: click on %q opened %v", width, tab.label, m.ActiveFocus)
			}
		}
	}
}

// Toasts expire; an old expiry cannot clear a newer toast; rendering hides an
// expired toast even if its expiry message never arrives.
func TestToastsExpireWithoutHidingKeyHints(t *testing.T) {
	m := smokeFixtureModel()
	next, cmd := m.Update(actionResultMsg{id: "copy", label: "Copy link or ID"})
	m = next.(Model)
	if cmd == nil || m.ToastMsg == "" {
		t.Fatal("toast did not schedule its expiry")
	}
	first := m.ToastSeq
	m.setToast("newer", 1)
	next, _ = m.Update(toastExpiredMsg{seq: first})
	m = next.(Model)
	if m.ToastMsg != "newer" {
		t.Fatal("a stale expiry cleared a newer toast")
	}
	next, _ = m.Update(toastExpiredMsg{seq: m.ToastSeq})
	m = next.(Model)
	if m.ToastMsg != "" || m.ToastPriority != 0 {
		t.Fatal("toast did not expire")
	}

	m.setToast("error toast", 2)
	m.ToastAt = time.Now().Add(-toastErrorTTL - time.Second)
	frame := stripped(renderAt(m, 80, 24))
	if strings.Contains(frame, "error toast") {
		t.Fatal("expired toast still rendered without its expiry message")
	}
	m.setToast("info after expired error", 1)
	if m.ToastMsg != "info after expired error" {
		t.Fatal("expired error toast still blocked newer information")
	}
	lines := strings.Split(frame, "\n")
	if !strings.Contains(lines[len(lines)-1], "? keys") {
		t.Fatal("key hints missing")
	}
}

// Runner data is only rendered on Overview and Runners, so the 10-second
// runner poll makes no API calls elsewhere and refreshes on return.
func TestRunnerPollingSkipsScreensThatDoNotShowRunners(t *testing.T) {
	var calls atomic.Int32
	original := fetchOrgRunners
	fetchOrgRunners = func(string) ([]*jobs.RunnerItem, error) {
		calls.Add(1)
		return nil, nil
	}
	t.Cleanup(func() { fetchOrgRunners = original })

	run := func(m *Model, cmd tea.Cmd) {
		for _, msg := range drain(cmd) {
			if r, ok := msg.(loadedRunnersMsg); ok {
				m.handleLoadedRunnersMsg(r)
			}
		}
	}
	m := smokeFixtureModel()
	m.ActiveFocus = FocusRepos
	for i := 0; i < 6; i++ { // one minute of runner ticks
		run(&m, m.handleRunnerJobTickMsg())
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("hidden runner data polled %d times in a minute, want 0", got)
	}
	m.LastRunnersAttempt = time.Now().Add(-time.Minute) // the minute of ticks above

	run(&m, m.changeScreen(FocusRunners))
	if got := calls.Load(); got != 1 {
		t.Fatalf("entering Runners made %d calls, want 1 immediate refresh", got)
	}
	run(&m, m.changeScreen(FocusRepos))
	run(&m, m.changeScreen(FocusRunners))
	if got := calls.Load(); got != 1 {
		t.Fatalf("re-entering within the poll interval refetched: %d calls", got)
	}
	for i := 0; i < 6; i++ {
		run(&m, m.handleRunnerJobTickMsg())
	}
	if got := calls.Load(); got != 7 {
		t.Fatalf("visible runner screen polled %d times, want 7", got)
	}
	if m.IsRunnersLoading || m.LastRunnersRefresh.IsZero() {
		t.Fatal("runner fetch did not complete its loading state")
	}
}

// drain runs cmd and any batched commands, returning the messages produced
// within a short window; long timers are abandoned.
func drain(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	out := make(chan tea.Msg, 1)
	go func() { out <- cmd() }()
	select {
	case msg := <-out:
		if batch, ok := msg.(tea.BatchMsg); ok {
			var msgs []tea.Msg
			for _, c := range batch {
				msgs = append(msgs, drain(c)...)
			}
			return msgs
		}
		return []tea.Msg{msg}
	case <-time.After(50 * time.Millisecond):
		return nil
	}
}

var sgrColour = regexp.MustCompile(`\x1b\[[0-9;]*(3[0-9]|4[0-9]|9[0-7]|10[0-7])[;m]`)

// NO_COLOR removes every colour sequence, including the progress bar's, while
// the selection stays visible through reverse video.
func TestNoColorRemovesColourButKeepsMeaning(t *testing.T) {
	previous := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(0) // termenv.TrueColor: behave like a colour terminal
	saved := th
	t.Cleanup(func() {
		lipgloss.SetColorProfile(previous)
		th = saved
	})

	t.Setenv("NO_COLOR", "")
	th = newTheme(noColorRequested())
	m := smokeFixtureModel()
	m.ProgressBar = th.newProgress()
	press(&m, "2")
	if !sgrColour.MatchString(m.View()) {
		t.Fatal("control: colour terminal rendered no colour, so the NO_COLOR check would be vacuous")
	}

	t.Setenv("NO_COLOR", "1")
	th = newTheme(noColorRequested())
	for _, st := range uxStates {
		m := smokeFixtureModel()
		m.ProgressBar = th.newProgress()
		st.open(&m)
		frame := m.View()
		if loc := sgrColour.FindStringIndex(frame); loc != nil {
			t.Fatalf("%s: NO_COLOR frame contains colour %q", st.name, frame[loc[0]:loc[1]])
		}
		if st.name == "repos" && !strings.Contains(frame, "\x1b[7m") && !strings.Contains(frame, ";7m") {
			t.Fatal("NO_COLOR selection lost its reverse-video highlight")
		}
	}
}

// Every status maps to a colour, a distinct glyph and a text label.
func TestStatusSemanticsNeverDependOnColourAlone(t *testing.T) {
	seen := map[string]statusKind{}
	for k, g := range statusGlyphs {
		if other, dup := seen[g]; dup {
			t.Fatalf("kinds %d and %d share glyph %q", k, other, g)
		}
		seen[g] = k
		if ansi.StringWidth(g) != 1 {
			t.Fatalf("glyph %q is not one cell", g)
		}
	}
	for _, s := range []jobs.JobStatus{jobs.JobQueued, jobs.JobRunning, jobs.JobPassed, jobs.JobFailed, jobs.JobCancelled, jobs.JobSkipped, jobs.JobWaiting, jobs.JobUnknown} {
		if got := stripped(stateBadge(s)); got != statusGlyphs[jobKind(s)]+" "+string(s) {
			t.Fatalf("job %s rendered %q", s, got)
		}
	}
	for _, s := range []jobs.RunnerStatus{jobs.RunnerIdle, jobs.RunnerRunning, jobs.RunnerOffline, jobs.RunnerMaintenance, jobs.RunnerUnknown} {
		if _, label := runnerKind(s); label == "" {
			t.Fatalf("runner %s has no label", s)
		}
	}
	for _, s := range []git.RepoStatus{git.StatusPending, git.StatusSyncing, git.StatusUpToDate, git.StatusUpdated, git.StatusStashedApplied, git.StatusSwitchedDefault, git.StatusRebased, git.StatusRebaseConflict, git.StatusPRCreated, git.StatusCloned, git.StatusArchived, git.StatusError, git.StatusSkipped} {
		kind, label := repoKind(s)
		if label == "" || (s != git.StatusPending && kind == kindUnknown) {
			t.Fatalf("repository %s maps to kind %d label %q", s, kind, label)
		}
	}
}

// Runs show how long they took or have waited, and failed jobs name the
// failing step: data GitHub already returns with the runs and jobs listings.
func TestRunsShowTimingAndFailureReason(t *testing.T) {
	m := smokeFixtureModel()
	now := time.Now()
	failed := &jobs.RunItem{ID: 300, Number: 9, Attempt: 1, Repo: "beta", Workflow: "Release", Status: jobs.JobFailed, JobsKnown: true,
		StartedAt: now.Add(-10 * time.Minute), CompletedAt: now.Add(-6 * time.Minute)}
	m.JobQueue = append(m.JobQueue,
		&jobs.JobItem{ID: "run:300", RunID: 300, Repo: "beta", Run: failed, IsRunHeader: true, Status: jobs.JobFailed},
		&jobs.JobItem{ID: "#9", GHJobID: 9, RunID: 300, Repo: "beta", Run: failed, Name: "beta / publish", Status: jobs.JobFailed,
			Steps: []jobs.GHJobStep{{Number: 1, Name: "Checkout", Status: "completed", Conclusion: "success"}, {Number: 2, Name: "Upload assets", Status: "completed", Conclusion: "failure"}}})
	press(&m, "2")
	press(&m, "f") // Needs attention
	list := stripped(m.listContent())
	if !strings.Contains(list, "✗ FAILED  beta / Release  #9") || !strings.Contains(list, "took 4m · finished 6m ago") {
		t.Fatalf("failed run lacks status or timing:\n%s", list)
	}
	press(&m, "enter")
	if jobsList := stripped(m.listContent()); !strings.Contains(jobsList, "failed at step 2: Upload assets") {
		t.Fatalf("failed job lacks its failing step:\n%s", jobsList)
	}
	if tabs := stripped(m.tabRow()); !strings.Contains(tabs, "2 Actions ✗1") {
		t.Fatalf("Actions tab did not flag the failure: %q", tabs)
	}
}

func TestEmptyStatesSayWhatIsMissingAndWhatToDo(t *testing.T) {
	m := smokeFixtureModel()
	m.TargetOrg = ""
	m.JobQueue = nil
	press(&m, "2")
	if s := stripped(m.listContent()); !strings.Contains(s, "need a GitHub owner") {
		t.Fatalf("Actions without an owner: %q", s)
	}
	m = smokeFixtureModel()
	m.Runners = nil
	press(&m, "3")
	if s := stripped(m.listContent()); !strings.Contains(s, "No self-hosted runners are registered for fixture-org") || !strings.Contains(s, "Press r") {
		t.Fatalf("empty Runners: %q", s)
	}
	m = smokeFixtureModel()
	m.Repos = nil
	press(&m, "1")
	if s := stripped(m.listContent()); !strings.Contains(s, "No repositories found in") {
		t.Fatalf("empty Repositories: %q", s)
	}
	m = smokeFixtureModel()
	press(&m, "2")
	press(&m, "f")
	press(&m, "f")
	m.JobQueue = nil
	if s := stripped(m.listContent()); !strings.Contains(s, "No recent runs. Press f to show active runs") {
		t.Fatalf("empty Actions filter: %q", s)
	}
}
