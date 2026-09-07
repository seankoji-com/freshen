package tui

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
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
	m.ActiveFocus = FocusRepos
	m.Width = 100
	m.Height = 28
	m.handleWindowSizeMsg(tea.WindowSizeMsg{Width: 100, Height: 28})
	m.moveSelection(0)
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
		for _, focus := range []FocusType{FocusOverview, FocusRepos, FocusJobs, FocusRunners, FocusConcerns} {
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

func TestOverviewIsLandingScreenAndPreservesNumberShortcuts(t *testing.T) {
	fresh := newTestModel("/tmp/freshen-fixture", "fixture-org")
	if fresh.ActiveFocus != FocusOverview {
		t.Fatal("new models should start on Overview")
	}

	m := smokeFixtureModel()
	m.ActiveFocus = FocusOverview

	press(&m, "1")
	if m.ActiveFocus != FocusRepos {
		t.Fatal("1 should preserve the Repositories shortcut")
	}
	press(&m, "0")
	if m.ActiveFocus != FocusOverview {
		t.Fatal("0 should open Overview")
	}
	press(&m, "tab")
	if m.ActiveFocus != FocusRepos {
		t.Fatal("Tab should move from Overview to Repositories")
	}
	press(&m, "shift+tab")
	if m.ActiveFocus != FocusOverview {
		t.Fatal("Shift+Tab should return to Overview")
	}
	press(&m, "4")
	if m.ActiveFocus != FocusConcerns {
		t.Fatal("4 should open Concerns")
	}
	press(&m, "tab")
	if m.ActiveFocus != FocusOverview {
		t.Fatal("Tab should wrap from Concerns to Overview")
	}
}

func TestOverviewSummarisesSnapshotsWithoutInventingCounts(t *testing.T) {
	m := smokeFixtureModel()
	m.ActiveFocus = FocusOverview
	m.Repos[1].Status = git.StatusError
	m.Repos[1].StatusMsg = "Pull failed"
	m.RunnerPermissionDenied = true
	m.RunnerFetchFailed = true
	failed := &jobs.RunItem{ID: 200, Number: 43, Repo: "beta", Workflow: "CI", Status: jobs.JobFailed}
	m.JobQueue = append(m.JobQueue, &jobs.JobItem{ID: "run:200", RunID: 200, Repo: "beta", Run: failed, IsRunHeader: true, Status: jobs.JobFailed})

	view := stripped(renderAt(m, 120, 32))
	for _, want := range []string{
		"2 repositories",
		"PR and issue counts loaded for 1/2",
		"1 need attention",
		"1 recent failures",
		"observed busy",
		"2 open PRs · 4 open issues",
		"beta / CI #43",
		"showing observed assignments only",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("Overview missing %q:\n%s", want, view)
		}
	}
	headline := stripped(m.summaryHeadline(m.buildOrgSummary()))
	if strings.Contains(headline, "2 open PRs") || strings.Contains(headline, "4 open issues") {
		t.Fatal("partial repository counts were presented as organization totals")
	}
}

func TestOverviewMarksFailedSnapshotsUnavailableInWideAndCompactLayouts(t *testing.T) {
	m := smokeFixtureModel()
	m.ActiveFocus = FocusOverview
	m.JobQueue = nil
	m.Runners = nil
	m.JobQueueFetchFailed = true
	m.RunnerFetchFailed = true
	m.IsJobQueueLoading = false
	m.IsRunnersLoading = false

	wide := stripped(m.summaryContent(100, 24))
	for _, want := range []string{"Actions data unavailable", "Runner data unavailable"} {
		if !strings.Contains(wide, want) {
			t.Fatalf("wide Overview missing %q:\n%s", want, wide)
		}
	}
	for _, misleading := range []string{"0 recent failures", "0 offline"} {
		if strings.Contains(wide, misleading) {
			t.Fatalf("wide Overview presented unavailable data as %q:\n%s", misleading, wide)
		}
	}

	compact := stripped(m.summaryContent(45, 14))
	for _, want := range []string{"Actions  data unavailable", "Runners  data unavailable"} {
		if !strings.Contains(compact, want) {
			t.Fatalf("compact Overview missing %q:\n%s", want, compact)
		}
	}
	for _, misleading := range []string{"0 failed", "0 offline"} {
		if strings.Contains(compact, misleading) {
			t.Fatalf("compact Overview presented unavailable data as %q:\n%s", misleading, compact)
		}
	}
}

func TestOverviewAttentionDisclosesOverflow(t *testing.T) {
	m := newTestModel("/tmp/freshen-fixture", "fixture-org")
	m.IsOrgSyncing = false
	m.IsJobQueueLoading = false
	m.IsRunnersLoading = false
	for i := 0; i < 7; i++ {
		m.Repos = append(m.Repos, &git.RepoItem{Name: fmt.Sprintf("repo-%d", i), Path: fmt.Sprintf("/tmp/repo-%d", i), Status: git.StatusError, StatusMsg: "sync failed"})
	}
	summary := m.buildOrgSummary()
	rows := stripped(m.summaryAttentionRows(summary, 80, 3))
	if !strings.Contains(rows, "+ 5 more") || !strings.Contains(rows, "use 1–4 for details") {
		t.Fatalf("attention overflow was hidden:\n%s", rows)
	}
	compact := stripped(m.firstAttentionLine(summary, 80))
	if !strings.Contains(compact, "Attention (1 of 7)") {
		t.Fatalf("compact attention omitted total: %q", compact)
	}
}

func TestConcernsPageOrdersWorkAndOpensRepositoryDetail(t *testing.T) {
	m := smokeFixtureModel()
	m.Repos[0].OpenPRsCount = 0
	m.Repos[1].HasLoadedCounts = true
	m.Repos[1].OpenPRsCount = 1
	m.ActiveFocus = FocusConcerns
	m.moveSelection(0)

	entries := m.entries()
	if len(entries) != 2 || entries[0].index != 1 {
		t.Fatalf("PR-bearing repository should lead issue-only repository: %+v", entries)
	}
	view := stripped(renderAt(m, 80, 24))
	for _, want := range []string{"Concerns · 2 repositories", "1 open PR", "4 open issues"} {
		if !strings.Contains(view, want) {
			t.Fatalf("Concerns missing %q:\n%s", want, view)
		}
	}

	press(&m, "enter")
	if !m.Detail || m.ActiveFocus != FocusConcerns || m.SelectedIndex != 1 {
		t.Fatal("Concerns should open the selected repository without losing the return screen")
	}
	press(&m, "esc")
	if m.Detail || m.ActiveFocus != FocusConcerns {
		t.Fatal("Esc should return to Concerns")
	}
}
func TestConcernsPageShowsUnknownMembershipInList(t *testing.T) {
	m := smokeFixtureModel()
	m.ActiveFocus = FocusConcerns
	m.moveSelection(0)

	content := stripped(m.listContent())
	if !strings.Contains(content, "1 repository has unknown counts and is not ranked") || !strings.Contains(content, "alpha") {
		t.Fatalf("partially populated Concerns list hid unknown membership:\n%s", content)
	}
}
func TestConcernsMouseSelectionAccountsForCoverageBanner(t *testing.T) {
	m := smokeFixtureModel()
	m.Repos[1].HasLoadedCounts = true
	m.Repos[1].OpenPRsCount = 1
	m.Repos = append(m.Repos, &git.RepoItem{Name: "gamma", Path: "/tmp/freshen-fixture/gamma"})
	m.ActiveFocus = FocusConcerns
	m.ScreenCursor[FocusConcerns] = ""
	m.SelectedIndex = -1

	// Body row 0 is the warning, row 1 is blank, and row 2 is the first repo.
	m.screenMouse(tea.MouseMsg{X: 5, Y: 5, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if m.SelectedIndex != 0 || m.ScreenCursor[FocusConcerns] != m.Repos[0].Path+"/"+m.Repos[0].Name {
		t.Fatalf("coverage banner shifted mouse selection: selected=%d cursor=%q", m.SelectedIndex, m.ScreenCursor[FocusConcerns])
	}
}
func TestEmptyConcernsCannotReuseRepositorySelection(t *testing.T) {
	m := smokeFixtureModel()
	m.SelectedIndex = 1
	m.ScreenCursor[FocusRepos] = m.Repos[1].Path + "/" + m.Repos[1].Name
	for _, repo := range m.Repos {
		repo.HasLoadedCounts = false
	}
	m.ActiveFocus = FocusConcerns

	cmd := press(&m, "s")
	if cmd != nil || m.BusyAction != "" || m.ActionTarget != nil {
		t.Fatal("empty Concerns screen reused an off-screen repository action target")
	}
	if !strings.Contains(m.ToastMsg, "unavailable") {
		t.Fatalf("missing unavailable-action feedback: %q", m.ToastMsg)
	}
}
func TestConcernsDetailClosesWhenRepositoryDisappears(t *testing.T) {
	m := smokeFixtureModel()
	m.ActiveFocus = FocusConcerns
	m.moveSelection(0)
	press(&m, "enter")
	selected := m.ScreenCursor[FocusConcerns]
	if selected == "" || !m.Detail {
		t.Fatal("fixture did not open a concern repository")
	}
	m.ScreenCursor[FocusRepos] = selected

	replacement := m.Repos[1].Clone()
	replacement.HasLoadedCounts = false
	m.handleOrgSyncedMsg(orgSyncedMsg{repos: []*git.RepoItem{replacement}})
	if m.Detail {
		t.Fatal("detail remained open after its concern repository disappeared")
	}
	if m.ScreenCursor[FocusConcerns] != "" || m.ScreenCursor[FocusRepos] != "" {
		t.Fatalf("stale repository cursors were retained: repos=%q concerns=%q", m.ScreenCursor[FocusRepos], m.ScreenCursor[FocusConcerns])
	}
	if m.SelectedIndex != -1 {
		t.Fatalf("missing concern reset selection to %d, want -1", m.SelectedIndex)
	}
}
func TestConcernsDetailSurvivesResolvedConcernUntilReturn(t *testing.T) {
	m := smokeFixtureModel()
	m.ActiveFocus = FocusConcerns
	m.moveSelection(0)
	press(&m, "enter")
	selected := m.ScreenCursor[FocusConcerns]
	fresh := m.Repos[0].Clone()
	fresh.OpenPRsCount = 0
	fresh.OpenIssuesCount = 0
	fresh.HasLoadedCounts = true
	resolvedBeta := m.Repos[1].Clone()
	resolvedBeta.HasLoadedCounts = true

	m.handleOrgSyncedMsg(orgSyncedMsg{repos: []*git.RepoItem{fresh, resolvedBeta}})
	if !m.Detail || m.SelectedIndex != 0 || m.ScreenCursor[FocusConcerns] != selected {
		t.Fatalf("resolved concern displaced open detail: detail=%v selected=%d cursor=%q", m.Detail, m.SelectedIndex, m.ScreenCursor[FocusConcerns])
	}
	press(&m, "esc")
	if m.Detail || m.ScreenCursor[FocusConcerns] != "" || m.SelectedIndex != -1 {
		t.Fatalf("returning to the empty concern list kept stale selection: detail=%v selected=%d cursor=%q", m.Detail, m.SelectedIndex, m.ScreenCursor[FocusConcerns])
	}
}
func TestFailedCountsRefreshPreservesKnownConcernsAsStale(t *testing.T) {
	m := smokeFixtureModel()
	m.ActiveFocus = FocusConcerns
	fresh := &git.RepoItem{
		Name:          m.Repos[0].Name,
		GHRepoName:    m.Repos[0].GHRepoName,
		Path:          m.Repos[0].Path,
		CurrentBranch: m.Repos[0].CurrentBranch,
	}

	m.NotifyOrgCountsError = true
	m.handleOrgSyncedMsg(orgSyncedMsg{repos: []*git.RepoItem{fresh}, countsErr: fmt.Errorf("temporary GraphQL failure")})
	if len(m.Repos) != 1 || !m.Repos[0].HasLoadedCounts || !m.Repos[0].CountsStale {
		t.Fatalf("last-known counts were not retained as stale: %+v", m.Repos)
	}
	if m.Repos[0].OpenPRsCount != 2 || m.Repos[0].OpenIssuesCount != 4 || len(m.concernRepoIndices()) != 1 {
		t.Fatalf("known concern disappeared after failed refresh: %+v", m.Repos[0])
	}
	view := stripped(renderAt(m, 80, 24))
	if !strings.Contains(view, "last successful refresh") || !strings.Contains(m.ToastMsg, "keeping last known counts") {
		t.Fatalf("stale counts were not disclosed:\n%s\ntoast: %q", view, m.ToastMsg)
	}
	headline := stripped(m.summaryHeadline(m.buildOrgSummary()))
	if !strings.Contains(headline, "known:") || !strings.Contains(headline, "last successful refresh") {
		t.Fatalf("stale counts looked current in headline: %q", headline)
	}
}
func TestMissingSuccessfulCountsResponseIsTemporarilyStale(t *testing.T) {
	m := smokeFixtureModel()
	m.ActiveFocus = FocusConcerns
	m.Repos[0].CountsUpdatedAt = time.Now()
	fresh := &git.RepoItem{Name: m.Repos[0].Name, GHRepoName: "renamed", Path: m.Repos[0].Path}

	m.handleOrgSyncedMsg(orgSyncedMsg{repos: []*git.RepoItem{fresh}})
	if !m.Repos[0].HasLoadedCounts || !m.Repos[0].CountsStale || len(m.concernRepoIndices()) != 1 {
		t.Fatalf("missing successful response discarded last-known counts: %+v", m.Repos[0])
	}
}
func TestClearedConcernSelectionCannotRearmImmediateAction(t *testing.T) {
	m := smokeFixtureModel()
	m.Repos[1].HasLoadedCounts = true
	m.Repos[1].OpenPRsCount = 1
	m.ActiveFocus = FocusConcerns
	m.moveSelection(0)
	resolved := m.Repos[0].Clone()
	resolved.OpenPRsCount = 0
	resolved.OpenIssuesCount = 0
	replacement := m.Repos[1].Clone()

	m.handleOrgSyncedMsg(orgSyncedMsg{repos: []*git.RepoItem{resolved, replacement}})
	if m.SelectedIndex != -1 || m.ScreenCursor[FocusConcerns] != "" {
		t.Fatalf("changed concern list retained selection: selected=%d cursor=%q", m.SelectedIndex, m.ScreenCursor[FocusConcerns])
	}
	cmd := press(&m, "s")
	if cmd != nil || m.ActionTarget != nil || m.BusyAction != "" {
		t.Fatal("cleared concern selection silently rearmed an immediate sync")
	}
	if !strings.Contains(m.ToastMsg, "unavailable") {
		t.Fatalf("missing unavailable-action feedback: %q", m.ToastMsg)
	}
}
func TestExpiredCountsAreNotCarriedAcrossFailure(t *testing.T) {
	m := smokeFixtureModel()
	m.ActiveFocus = FocusConcerns
	m.Repos[0].CountsUpdatedAt = time.Now().Add(-repoCountsStaleTTL - time.Minute)
	fresh := &git.RepoItem{Name: m.Repos[0].Name, GHRepoName: m.Repos[0].GHRepoName, Path: m.Repos[0].Path}

	m.handleOrgSyncedMsg(orgSyncedMsg{repos: []*git.RepoItem{fresh}})
	if m.Repos[0].HasLoadedCounts || m.Repos[0].CountsStale || len(m.concernRepoIndices()) != 0 {
		t.Fatalf("expired counts survived the stale-data TTL: %+v", m.Repos[0])
	}
}
func TestEmptyCursorRequiresNavigationBeforeRepoAction(t *testing.T) {
	m := smokeFixtureModel()
	m.ScreenCursor[FocusRepos] = ""
	m.SelectedIndex = 0

	m.captureActionTarget()
	if m.ActionTarget != nil {
		t.Fatalf("empty cursor targeted repository %q", m.ActionTarget.Name)
	}
	m.moveSelection(1)
	if m.SelectedIndex != 0 || m.ScreenCursor[FocusRepos] != m.Repos[0].Path+"/"+m.Repos[0].Name {
		t.Fatalf("first navigation skipped the first row: selected=%d cursor=%q", m.SelectedIndex, m.ScreenCursor[FocusRepos])
	}
}
func TestRepoTickTracksAndDoesNotOverlapRefresh(t *testing.T) {
	m := smokeFixtureModel()
	first := m.startOrgRefresh(false, false)
	if first == nil || !m.IsOrgSyncing {
		t.Fatalf("first request did not start and track one refresh: command=%v syncing=%v", first, m.IsOrgSyncing)
	}
	if second := m.startOrgRefresh(false, true); second != nil || !m.IsOrgSyncing {
		t.Fatalf("overlapping request dispatched another refresh: command=%v syncing=%v", second, m.IsOrgSyncing)
	}
	if !m.NotifyOrgCountsError || !strings.Contains(m.ToastMsg, "already in progress") {
		t.Fatalf("manual retry was not acknowledged or promoted: notify=%v toast=%q", m.NotifyOrgCountsError, m.ToastMsg)
	}
	queued, handled := m.handleOrgSyncedMsg(orgSyncedMsg{repos: []*git.RepoItem{m.Repos[0].Clone(), m.Repos[1].Clone()}})
	if handled || queued == nil || !m.IsOrgSyncing || m.PendingOrgRefresh || !m.NotifyOrgCountsError {
		t.Fatalf("manual retry was not queued: handled=%v command=%v syncing=%v pending=%v notify=%v", handled, queued, m.IsOrgSyncing, m.PendingOrgRefresh, m.NotifyOrgCountsError)
	}
	m.handleOrgSyncedMsg(orgSyncedMsg{repos: []*git.RepoItem{m.Repos[0].Clone(), m.Repos[1].Clone()}, countsErr: fmt.Errorf("temporary GraphQL failure")})
	if m.NotifyOrgCountsError || !strings.Contains(m.ToastMsg, "Review counts refresh failed") {
		t.Fatalf("queued retry did not report failure: notify=%v toast=%q", m.NotifyOrgCountsError, m.ToastMsg)
	}

	m.IsOrgSyncing = true
	m.OrgSyncStartedAt = time.Now().Add(-orgRefreshStuckAfter - time.Second)
	if replacement := m.startOrgRefresh(false, false); replacement == nil {
		t.Fatal("stale in-flight guard permanently blocked repository refresh")
	}
}
func TestStaleOrgRefreshGenerationCannotOverwriteCurrentState(t *testing.T) {
	m := smokeFixtureModel()
	m.OrgRefreshGeneration = 7
	m.IsOrgSyncing = true
	m.OrgSyncStartedAt = time.Now()
	original := m.Repos[0]

	cmd, handled := m.handleOrgSyncedMsg(orgSyncedMsg{
		repos:      []*git.RepoItem{{Name: "stale", Path: "/tmp/stale"}},
		generation: 6,
	})
	if !handled || cmd != nil {
		t.Fatalf("stale completion was not ignored: handled=%v command=%v", handled, cmd)
	}
	if !m.IsOrgSyncing || m.Repos[0] != original || !m.LastOrgRefresh.IsZero() {
		t.Fatalf("stale completion mutated current state: syncing=%v repos=%+v refreshed=%v", m.IsOrgSyncing, m.Repos, m.LastOrgRefresh)
	}
}

func TestOrgRefreshFallbackOwnsPreviousRepoSnapshot(t *testing.T) {
	m := smokeFixtureModel()
	m.TargetOrg = ""
	m.TargetDir = t.TempDir() + "/missing"
	originalScan := scanLocalDirectoryContext
	entered := make(chan struct{})
	release := make(chan struct{})
	scanLocalDirectoryContext = func(context.Context, string) ([]string, error) {
		close(entered)
		<-release
		return nil, fmt.Errorf("scan failed")
	}
	t.Cleanup(func() { scanLocalDirectoryContext = originalScan })

	wantBranch := m.Repos[0].CurrentBranch
	wantLogs := append([]string(nil), m.Repos[0].Logs...)
	cmd := m.startOrgRefresh(false, false)
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	<-entered

	stop := make(chan struct{})
	var writer sync.WaitGroup
	writer.Add(1)
	go func() {
		defer writer.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				m.applyRepoSnapshot(&git.RepoItem{Name: "alpha", CurrentBranch: fmt.Sprintf("branch-%d", i), Logs: []string{fmt.Sprintf("log-%d", i)}})
			}
		}
	}()
	close(release)
	msg := (<-result).(orgSyncedMsg)
	close(stop)
	writer.Wait()

	if len(msg.repos) == 0 || msg.repos[0].CurrentBranch != wantBranch || !reflect.DeepEqual(msg.repos[0].Logs, wantLogs) {
		t.Fatalf("background refresh read live RepoItems instead of its owned snapshot: %+v", msg.repos)
	}
}

func TestLocalRepoBranchesDiscardExpiredContextSentinels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	current, defaultBranch, ok := localRepoBranches(ctx, t.TempDir())
	if ok || current != "" || defaultBranch != "" {
		t.Fatalf("expired branch lookup leaked values into the model: current=%q default=%q ok=%v", current, defaultBranch, ok)
	}
}

func TestPartialLocalScanPreservesAndFlagsLastKnownMetadata(t *testing.T) {
	m := smokeFixtureModel()
	m.ActiveFocus = FocusOverview
	m.NotifyOrgCountsError = true
	freshAlpha := &git.RepoItem{
		Name:            "alpha",
		GHRepoName:      "alpha-renamed",
		Path:            m.Repos[0].Path,
		HasLoadedCounts: true,
		OpenPRsCount:    2,
		OpenIssuesCount: 4,
		Status:          git.StatusPending,
	}
	freshBeta := m.Repos[1].Clone()

	m.handleOrgSyncedMsg(orgSyncedMsg{
		repos:    []*git.RepoItem{freshAlpha, freshBeta},
		localErr: fmt.Errorf("local scan deadline exceeded"),
	})
	if !m.LocalScanFailed || m.OrgRefreshFailed {
		t.Fatalf("partial local scan flags are wrong: local=%v org=%v", m.LocalScanFailed, m.OrgRefreshFailed)
	}
	if got := m.Repos[0]; got.GHRepoName != "alpha-renamed" || got.CurrentBranch != "feat/screens" || got.DefaultBranch != "main" || got.Status != git.StatusUpdated {
		t.Fatalf("partial refresh did not combine new remote and last-known local metadata: %+v", got)
	}
	if !strings.Contains(stripped(m.summaryHeadline(m.buildOrgSummary())), "local metadata incomplete") {
		t.Fatalf("Overview headline did not flag partial local state: %q", stripped(m.summaryHeadline(m.buildOrgSummary())))
	}
	if !strings.Contains(m.ToastMsg, "Local repository metadata is incomplete") {
		t.Fatalf("explicit refresh did not report partial local state: %q", m.ToastMsg)
	}
	m.ToastMsg = ""
	if !strings.Contains(stripped(m.statusLine()), "Local repository metadata incomplete") {
		t.Fatalf("status line did not persist partial local state: %q", stripped(m.statusLine()))
	}
}

func TestBlockedLocalScanReportsInProgressInsteadOfRetrying(t *testing.T) {
	m := smokeFixtureModel()
	m.ActiveFocus = FocusOverview
	m.NotifyOrgCountsError = true

	m.handleOrgSyncedMsg(orgSyncedMsg{
		repos:    cloneRepoItems(m.Repos),
		localErr: fmt.Errorf("%w: fixture", git.ErrDirectoryScanInProgress),
	})
	if !m.LocalScanFailed || !m.LocalScanInProgress {
		t.Fatalf("blocked scan state was not retained: failed=%v inProgress=%v", m.LocalScanFailed, m.LocalScanInProgress)
	}
	if !strings.Contains(m.ToastMsg, "still running") || strings.Contains(m.ToastMsg, "Press r") {
		t.Fatalf("blocked scan toast advertised an inert retry: %q", m.ToastMsg)
	}
	m.ToastMsg = ""
	if status := stripped(m.statusLine()); !strings.Contains(status, "still running") || strings.Contains(status, "press r") {
		t.Fatalf("blocked scan status advertised an inert retry: %q", status)
	}
}

func TestStuckLocalScanOffersFreshAttempt(t *testing.T) {
	m := smokeFixtureModel()
	m.ActiveFocus = FocusOverview
	m.NotifyOrgCountsError = true

	m.handleOrgSyncedMsg(orgSyncedMsg{
		repos:    cloneRepoItems(m.Repos),
		localErr: fmt.Errorf("%w: fixture", git.ErrDirectoryScanStuck),
	})
	if !m.LocalScanFailed || !m.LocalScanStuck || m.LocalScanInProgress {
		t.Fatalf("stuck scan state was not retained: failed=%v stuck=%v inProgress=%v", m.LocalScanFailed, m.LocalScanStuck, m.LocalScanInProgress)
	}
	if !strings.Contains(m.ToastMsg, "was abandoned") || !strings.Contains(m.ToastMsg, "Press r") {
		t.Fatalf("stuck scan toast did not offer a fresh attempt: %q", m.ToastMsg)
	}
	m.ToastMsg = ""
	if status := stripped(m.statusLine()); !strings.Contains(status, "abandoned") || !strings.Contains(status, "press r") {
		t.Fatalf("stuck scan status did not offer a fresh attempt: %q", status)
	}
}

func TestAppendMissingReposRetainsLocalOnlyRepoAsStale(t *testing.T) {
	remote := &git.RepoItem{Name: "org-repo", Path: "/repos/org-repo"}
	localOnly := &git.RepoItem{
		Name:            "personal-fork",
		Path:            "/repos/personal-fork",
		CurrentBranch:   "wip",
		HasLoadedCounts: true,
		OpenIssuesCount: 3,
	}

	got := appendMissingRepos([]*git.RepoItem{remote}, []*git.RepoItem{remote.Clone(), localOnly})
	if len(got) != 2 || got[1].Name != "personal-fork" || got[1].CurrentBranch != "wip" || !got[1].CountsStale || !got[1].LocalMetadataStale {
		t.Fatalf("local-only repository was not retained as stale: %+v", got)
	}
}

func TestFailedRefreshCannotResurrectDeletedRepository(t *testing.T) {
	m := smokeFixtureModel()
	m.OrgRefreshGeneration = 5
	deleted := m.Repos[1].Clone()
	m.receiveAction(actionResultMsg{id: "delete", label: "Delete archived clone", path: deleted.Path})
	if removedAt, found := m.RemovedRepoPaths[deleted.Path]; len(m.Repos) != 1 || !found || removedAt != 5 {
		t.Fatalf("successful delete did not record its tombstone: repos=%d removed=%v", len(m.Repos), m.RemovedRepoPaths)
	}
	deleted.LocalMetadataStale = true

	m.handleOrgSyncedMsg(orgSyncedMsg{
		repos:      []*git.RepoItem{m.Repos[0].Clone(), deleted},
		localErr:   fmt.Errorf("scan failed"),
		generation: 5,
	})
	if len(m.Repos) != 1 || m.Repos[0].Name != "alpha" {
		t.Fatalf("failed refresh resurrected a deleted repository: %+v", m.Repos)
	}
}

func TestPostDeleteRefreshCanClearTombstone(t *testing.T) {
	m := smokeFixtureModel()
	m.OrgRefreshGeneration = 5
	reappeared := m.Repos[1].Clone()
	m.receiveAction(actionResultMsg{id: "delete", label: "Delete archived clone", path: reappeared.Path})
	m.OrgRefreshGeneration = 6
	reappeared.IsNew = true

	m.handleOrgSyncedMsg(orgSyncedMsg{
		repos:      []*git.RepoItem{m.Repos[0].Clone(), reappeared},
		generation: 6,
	})
	if len(m.Repos) != 2 {
		t.Fatalf("post-delete refresh did not restore a newly observed repository: %+v", m.Repos)
	}
	if _, found := m.RemovedRepoPaths[reappeared.Path]; found {
		t.Fatalf("post-delete refresh did not clear tombstone: %v", m.RemovedRepoPaths)
	}
}

func TestAppendedRepositoryCountsStillExpire(t *testing.T) {
	m := smokeFixtureModel()
	stale := m.Repos[0].Clone()
	stale.LocalMetadataStale = true
	stale.CountsStale = true
	stale.CountsUpdatedAt = time.Now().Add(-repoCountsStaleTTL - time.Minute)

	m.handleOrgSyncedMsg(orgSyncedMsg{
		repos:    []*git.RepoItem{stale, m.Repos[1].Clone()},
		localErr: fmt.Errorf("scan failed"),
	})
	if m.Repos[0].HasLoadedCounts || m.Repos[0].OpenPRsCount != 0 || m.Repos[0].OpenIssuesCount != 0 {
		t.Fatalf("appended repository retained expired counts: %+v", m.Repos[0])
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
	if confirmation := stripped(m.menuContent()); strings.Contains(confirmation, "Default branch: main") || !strings.Contains(confirmation, "aborts on default-branch drift") || !strings.Contains(confirmation, "Changed or unavailable worktrees") {
		t.Fatalf("prune confirmation omitted the branch safety check: %q", confirmation)
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
func TestManualOverviewRefreshClearsDirectoryScanLockout(t *testing.T) {
	m := smokeFixtureModel()
	m.ActiveFocus = FocusOverview
	m.LocalScanUnresponsive = true

	if cmd := m.refreshScreen(); cmd == nil {
		t.Fatal("manual refresh did not start a new repository scan")
	}
	if m.LocalScanUnresponsive {
		t.Fatal("manual refresh left the directory scan locked out")
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
	m.BusyAction = "test operation"

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
	release := make(chan struct{})
	cmd := m.actionCmd(func() actionResultMsg { <-release; return actionResultMsg{err: fmt.Errorf("test result")} })
	finished := make(chan struct{})
	go func() { m.bgWG.Wait(); close(finished) }()
	select {
	case <-finished:
		t.Fatal("shutdown saw no work before command started")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
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
	err := &jobs.QueueFetchError{Partial: true, Failed: 1, Total: 20, Cause: fmt.Errorf("one repo unavailable")}
	m.handleLoadedJobQueueMsg(loadedJobQueueMsg{queue: m.JobQueue, err: err})
	if !m.JobQueueFetchFailed || m.ConsecutiveErrors[fetchSourceJobQueue] != 0 || m.ToastMsg != "" {
		t.Fatal("partial coverage treated as total outage")
	}
}

func TestWidespreadCoverageLossWarnsEveryScreen(t *testing.T) {
	m := smokeFixtureModel()
	err := &jobs.QueueFetchError{Partial: true, Failed: 19, Total: 20, Cause: fmt.Errorf("19 of 20 repositories unavailable")}
	m.handleLoadedJobQueueMsg(loadedJobQueueMsg{queue: m.JobQueue, err: err})
	if m.ConsecutiveErrors[fetchSourceJobQueue] != 1 || m.ToastPriority != 2 {
		t.Fatal("widespread outage did not back off and warn")
	}
	for _, focus := range []FocusType{FocusOverview, FocusRepos, FocusJobs, FocusRunners, FocusConcerns} {
		m.ActiveFocus = focus
		if !strings.Contains(stripped(m.View()), "Actions incomplete") {
			t.Fatal("coverage warning hidden on screen", focus)
		}
	}
}
func TestPendingFinalResultsAreDistinctFromFetchFailure(t *testing.T) {
	m := smokeFixtureModel()
	press(&m, "2")
	press(&m, "enter")
	m.OpenJobID = "#2"
	r := *m.OpenRun
	r.Status = jobs.JobFailed
	r.JobsKnown = false
	m.processJobQueueUpdate([]*jobs.JobItem{{ID: "run:100", RunID: 100, Repo: "alpha", Run: &r, IsRunHeader: true, Status: jobs.JobFailed}}, nil)
	if !m.OpenRun.JobsStale || m.OpenRun.JobsError != "" {
		t.Fatal("pending refresh was represented as a fetch error")
	}
	content := m.jobDetailContent()
	if strings.Contains(content, "refresh failed") || !strings.Contains(content, "previous poll") {
		t.Fatal(content)
	}
}
func TestDerivedRunnersOnlyReflectActiveAssignments(t *testing.T) {
	old := []*jobs.RunnerItem{{ID: "runner-old", Name: "old", Status: jobs.RunnerRunning}}
	queue := []*jobs.JobItem{{ID: "1", RunnerName: "finished", Status: jobs.JobPassed}, {ID: "2", RunnerName: "active", Status: jobs.JobRunning}}
	got := extractRunnersFromJobQueue(queue, old)
	if len(got) != 1 || got[0].Name != "active" {
		t.Fatalf("phantom runners retained: %+v", got)
	}
}
func TestDiscardedActionCommandDoesNotStrandShutdown(t *testing.T) {
	m := smokeFixtureModel()
	m.actionCmd(func() actionResultMsg { <-m.ctx.Done(); return actionResultMsg{} })
	m.cancel()
	done := make(chan struct{})
	go func() { m.bgWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("discarded result command stranded shutdown")
	}
}
