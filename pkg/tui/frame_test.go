package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/seankoji-com/freshen/pkg/git"
	"github.com/seankoji-com/freshen/pkg/jobs"
)

func renderAt(m Model, w, h int) string {
	updated, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return updated.(Model).View()
}
func stripped(s string) string { return ansi.Strip(s) }
func smokeFixtureModel() Model {
	m := newTestModel("/tmp/freshen-fixture", "fixture-org")
	m.IsOrgSyncing = false
	m.IsJobQueueLoading = false
	m.IsRunnersLoading = false
	m.Repos = []*git.RepoItem{
		{Name: "alpha", GHRepoName: "alpha", Path: "/tmp/freshen-fixture/alpha", CurrentBranch: "feat/screens", DefaultBranch: "main", Status: git.StatusUpdated, StatusMsg: "Updated", HasLoadedCounts: true, OpenPRsCount: 2, OpenIssuesCount: 4, Logs: []string{"git pull", "Up to date"}},
		{Name: "beta", GHRepoName: "beta", Path: "/tmp/freshen-fixture/beta", CurrentBranch: "main", DefaultBranch: "main", Status: git.StatusPending},
	}
	m.Runners = []*jobs.RunnerItem{{ID: "runner-1", Name: "mac-builder", Platform: "macOS/ARM64", Status: jobs.RunnerRunning, Tags: []string{"self-hosted", "ARM64"}}}
	r := &jobs.RunItem{ID: 100, Number: 42, Attempt: 1, Repo: "alpha", Workflow: "CI", Title: "Build the new screens", Branch: "feat/screens", Event: "pull_request", Status: jobs.JobRunning, JobsKnown: true}
	m.JobQueue = []*jobs.JobItem{
		{ID: "run:100", RunID: 100, Repo: "alpha", Run: r, IsRunHeader: true, Status: jobs.JobRunning},
		{ID: "#1", GHJobID: 1, RunID: 100, Repo: "alpha", Run: r, Name: "alpha / build", Status: jobs.JobPassed, Duration: "24s", Steps: []jobs.GHJobStep{{Number: 1, Name: "Build", Status: "completed", Conclusion: "success"}}},
		{ID: "#2", GHJobID: 2, RunID: 100, Repo: "alpha", Run: r, Name: "alpha / test", Status: jobs.JobRunning, RunnerName: "mac-builder", Duration: "18s", Steps: []jobs.GHJobStep{{Number: 1, Name: "Set up", Status: "completed", Conclusion: "success"}, {Number: 2, Name: "Run tests", Status: "in_progress"}}},
		{ID: "#3", GHJobID: 3, RunID: 100, Repo: "alpha", Run: r, Name: "alpha / deploy", Status: jobs.JobQueued, Labels: []string{"self-hosted", "ARM64"}},
	}
	m.Width = 100
	m.Height = 28
	m.handleWindowSizeMsg(tea.WindowSizeMsg{Width: 100, Height: 28})
	return m
}
func press(m *Model, k string) tea.Cmd {
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	keys := map[string]tea.KeyType{"enter": tea.KeyEnter, "esc": tea.KeyEsc, "up": tea.KeyUp, "down": tea.KeyDown, "tab": tea.KeyTab, "shift+tab": tea.KeyShiftTab, "pgdown": tea.KeyPgDown, "home": tea.KeyHome, "end": tea.KeyEnd}
	if typ, ok := keys[k]; ok {
		msg = tea.KeyMsg{Type: typ}
	}
	next, cmd := m.Update(msg)
	*m = next.(Model)
	return cmd
}
func TestScreenFrameBounds(t *testing.T) {
	for _, size := range [][2]int{{1, 1}, {20, 6}, {40, 12}, {60, 18}, {80, 24}, {120, 40}, {200, 50}} {
		for _, focus := range []FocusType{FocusRepos, FocusJobs, FocusRunners} {
			for _, detail := range []bool{false, true} {
				m := smokeFixtureModel()
				m.ActiveFocus = focus
				m.Detail = detail
				if focus == FocusJobs && detail {
					m.OpenRun = m.JobQueue[0].Run
					m.OpenJobID = "#2"
				}
				frame := renderAt(m, size[0], size[1])
				lines := strings.Split(frame, "\n")
				if len(lines) != size[1] {
					t.Fatalf("%v: height %d", size, len(lines))
				}
				for _, line := range lines {
					if ansi.StringWidth(line) > size[0] {
						t.Fatalf("%v: overflow %q", size, line)
					}
				}
			}
		}
	}
}
func TestNavigationIsLocalAndReversible(t *testing.T) {
	m := smokeFixtureModel()
	press(&m, "end")
	press(&m, "down")
	if m.ActiveFocus != FocusRepos || m.SelectedIndex != 1 {
		t.Fatal("list boundary changed screen")
	}
	press(&m, "2")
	if m.ActiveFocus != FocusJobs {
		t.Fatal("2 should open Actions")
	}
	press(&m, "enter")
	if m.OpenRun == nil || m.OpenRun.ID != 100 || m.OpenJobID != "" {
		t.Fatal("run was not opened")
	}
	press(&m, "down")
	press(&m, "enter")
	if m.OpenJobID != "#2" {
		t.Fatal("job was not opened")
	}
	if !strings.Contains(stripped(m.Viewport.View()), "Run tests") {
		t.Fatal("missing current step")
	}
	press(&m, "esc")
	if m.OpenRun == nil || m.OpenJobID != "" {
		t.Fatal("Esc did not return to jobs")
	}
	press(&m, "esc")
	if m.OpenRun != nil {
		t.Fatal("Esc did not return to runs")
	}
	press(&m, "tab")
	if m.ActiveFocus != FocusRunners {
		t.Fatal("Tab order")
	}
	press(&m, "shift+tab")
	if m.ActiveFocus != FocusJobs {
		t.Fatal("reverse Tab order")
	}
}
func TestProgressUsesAllJobsAndRealSteps(t *testing.T) {
	m := smokeFixtureModel()
	press(&m, "2")
	if !strings.Contains(stripped(m.listContent()), "1/3 jobs complete") {
		t.Fatal(m.listContent())
	}
	press(&m, "enter")
	content := stripped(m.listContent())
	for _, want := range []string{"PASSED", "RUNNING", "QUEUED", "1/2 steps complete", "Run tests", "self-hosted"} {
		if !strings.Contains(content, want) {
			t.Errorf("missing %q in %s", want, content)
		}
	}
}
func TestSelectionSurvivesPollAndConfirmationPinsTarget(t *testing.T) {
	m := smokeFixtureModel()
	press(&m, "down")
	press(&m, "X")
	if m.PendingAction != "prune" || m.ActionTarget.Name != "beta" {
		t.Fatal("missing explicit confirmation")
	}
	m.handleOrgSyncedMsg(orgSyncedMsg{repos: []*git.RepoItem{{Name: "aardvark", Path: "/tmp/a"}, m.Repos[0], m.Repos[1]}})
	if m.ActionTarget.Name != "beta" {
		t.Fatal("poll retargeted confirmation")
	}
	press(&m, "esc")
	if m.PendingAction != "" {
		t.Fatal("confirmation not cancelled")
	}
	press(&m, "2")
	press(&m, "enter")
	press(&m, "down")
	queue := append([]*jobs.JobItem(nil), m.JobQueue...)
	queue[1], queue[2] = queue[2], queue[1]
	m.processJobQueueUpdate(queue, nil)
	if m.entries()[m.entryIndex(m.entries())].key != "#2" {
		t.Fatal("poll moved job selection")
	}
}
func TestSearchAndMouseShareVisibleEntries(t *testing.T) {
	m := smokeFixtureModel()
	press(&m, "/")
	press(&m, "beta")
	press(&m, "enter")
	if len(m.entries()) != 1 || m.entries()[0].key != "/tmp/freshen-fixture/beta/beta" {
		t.Fatal("search did not filter")
	}
	m.screenMouse(tea.MouseMsg{X: 5, Y: 4, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if m.SelectedIndex != 1 {
		t.Fatal("mouse selected unfiltered index")
	}
	press(&m, "esc")
	for i := 0; i < 30; i++ {
		m.Repos = append(m.Repos, &git.RepoItem{Name: fmt.Sprintf("repo-%02d", i), Path: fmt.Sprintf("/tmp/%d", i)})
	}
	press(&m, "end")
	visible, _, _ := m.visibleEntries()
	m.screenMouse(tea.MouseMsg{X: 5, Y: 4, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if m.selectionKey() != visible[0].key {
		t.Fatal("mouse ignored scroll window")
	}
}
func TestDetailScrollHasOneOwner(t *testing.T) {
	m := smokeFixtureModel()
	for i := 0; i < 100; i++ {
		m.Repos[0].Logs = append(m.Repos[0].Logs, fmt.Sprint(i))
	}
	press(&m, "enter")
	press(&m, "down")
	if m.Viewport.YOffset != 1 || m.SelectedIndex != 0 {
		t.Fatalf("detail key affected list or double scrolled: %d", m.Viewport.YOffset)
	}
	m.screenMouse(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	if m.Viewport.YOffset != 4 {
		t.Fatal("wheel scrolled more than once")
	}
}
func TestMissingJobNeverBecomesSuccess(t *testing.T) {
	m := smokeFixtureModel()
	m.processJobQueueUpdate(nil, nil)
	if strings.Contains(m.ToastMsg, "completed") || strings.Contains(m.ToastMsg, "passed") {
		t.Fatal("disappearance fabricated result")
	}
}
func TestLateRunResponseCannotReplaceAnotherRun(t *testing.T) {
	m := smokeFixtureModel()
	run := m.JobQueue[0].Run
	m.OpenRun = &jobs.RunItem{ID: 999, Repo: "beta"}
	m.receiveRunJobs(runJobsLoadedMsg{run: run, infos: []jobs.GHJobInfo{{ID: 42, Name: "wrong"}}})
	if m.OpenRun.ID != 999 || len(m.JobQueue) != 4 {
		t.Fatal("late response replaced current detail")
	}
}
func TestRepositoryShortcutsAreScopedAndNonblocking(t *testing.T) {
	m := smokeFixtureModel()
	press(&m, "2")
	press(&m, "p")
	if m.PendingAction != "" || m.BusyAction != "" {
		t.Fatal("Actions shortcut triggered repository mutation")
	}
	press(&m, "1")
	cmd := press(&m, "b")
	if cmd == nil || m.BusyAction == "" {
		t.Fatal("branch action needs a background command and busy feedback")
	}
	// The command is intentionally not executed: fixture paths are never mutated.
	press(&m, "p")
	if m.PendingAction != "" {
		t.Fatal("allowed concurrent repository mutation")
	}
	press(&m, "2")
	if m.ActiveFocus != FocusJobs {
		t.Fatal("busy operation blocked navigation")
	}
}

func TestQueueViewOpensExactJob(t *testing.T) {
	m := smokeFixtureModel()
	press(&m, "2")
	press(&m, "v")
	entries := m.entries()
	if len(entries) != 2 {
		t.Fatalf("queue should contain running and queued jobs, got %d", len(entries))
	}
	press(&m, "down")
	press(&m, "enter")
	if m.OpenRun == nil || m.OpenRun.ID != 100 || m.OpenJobID != "#3" {
		t.Fatal("queue selected wrong job")
	}
	if !strings.Contains(m.selectedURL(), "/job/3") {
		t.Fatal("queue copy target is not selected job")
	}
}
func TestSmallConfirmationCannotExecuteUnseenAction(t *testing.T) {
	m := smokeFixtureModel()
	m.handleWindowSizeMsg(tea.WindowSizeMsg{Width: 40, Height: 12})
	press(&m, "X")
	cmd := press(&m, "enter")
	if cmd != nil || m.PendingAction != "prune" || m.BusyAction != "" {
		t.Fatal("executed a clipped confirmation")
	}
	if !strings.Contains(m.ToastMsg, "Enlarge") {
		t.Fatal("missing resize instruction")
	}
}
func TestLateAttemptResponseIsDiscarded(t *testing.T) {
	m := smokeFixtureModel()
	old := *m.JobQueue[0].Run
	current := old
	current.Attempt++
	m.OpenRun = &current
	m.receiveRunJobs(runJobsLoadedMsg{run: &old, infos: []jobs.GHJobInfo{{ID: 999}}})
	for _, j := range m.JobQueue {
		if j.GHJobID == 999 {
			t.Fatal("old attempt injected a job")
		}
	}
}
func TestDemoCannotStartRepositoryOperations(t *testing.T) {
	m := terminalDemo{smokeFixtureModel()}
	for _, key := range []string{"s", "b", "o", "y"} {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		if next.(terminalDemo).BusyAction != "" {
			t.Fatal("demo started an operation")
		}
	}
	m.PendingAction = "sync-all"
	m.ActionTarget = m.Repos[0].Clone()
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if next.(terminalDemo).IsSyncing {
		t.Fatal("demo started bulk sync")
	}
}

func TestActionsRegisterWithShutdownBeforeCommandRuns(t *testing.T) {
	m := smokeFixtureModel()
	m.ActionURL = ""
	cmd := m.executeAction("copy") // Empty target fails locally, without clipboard access.
	finished := make(chan struct{})
	go func() { m.bgWG.Wait(); close(finished) }()
	select {
	case <-finished:
		t.Fatal("shutdown saw no work before command started")
	case <-time.After(20 * time.Millisecond):
	}
	result := cmd().(actionResultMsg)
	if result.err == nil {
		t.Fatal("expected empty-target error")
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("action did not release shutdown guard")
	}
}
func TestTerminalTransitionRetainsJobsAndReportsFailure(t *testing.T) {
	m := smokeFixtureModel()
	press(&m, "2")
	press(&m, "enter")
	m.OpenJobID = "#2"
	r := *m.OpenRun
	r.Status = jobs.JobFailed
	r.JobsKnown = false
	r.JobsError = "temporary jobs endpoint failure"
	m.processJobQueueUpdate([]*jobs.JobItem{{ID: "run:100", RunID: 100, Repo: "alpha", Run: &r, IsRunHeader: true, Status: jobs.JobFailed}}, nil)
	if len(m.runJobs()) != 3 || m.openJob() == nil {
		t.Fatal("terminal transition dropped job detail")
	}
	if !strings.Contains(m.ToastMsg, "failed") {
		t.Fatal("terminal run failure was silent")
	}
	if !strings.Contains(m.jobDetailContent(), "Cached job state") {
		t.Fatal("stale job result was not labelled")
	}
}
func TestLargeBudgetIsNeverShortenedByBackoff(t *testing.T) {
	base := 12 * time.Minute
	for _, errors := range []int{0, 1, 4} {
		if got := backoffInterval(base, errors); got < base {
			t.Fatalf("budget shortened to %s", got)
		}
	}
}
func TestPartialCoverageDoesNotBackOffHealthyRepositories(t *testing.T) {
	m := smokeFixtureModel()
	err := &jobs.QueueFetchError{Partial: true, Cause: fmt.Errorf("one repo unavailable")}
	m.handleLoadedJobQueueMsg(loadedJobQueueMsg{queue: m.JobQueue, err: err})
	if !m.JobQueueFetchFailed || m.ConsecutiveErrors[fetchSourceJobQueue] != 0 || m.ToastMsg != "" {
		t.Fatal("partial coverage treated as total outage")
	}
}
