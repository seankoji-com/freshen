package jobs

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestDefaultRunners(t *testing.T) {
	runners := DefaultRunners()
	if runners == nil {
		t.Fatalf("expected non-nil slice from DefaultRunners()")
	}
}

func TestDefaultJobQueue(t *testing.T) {
	queue := DefaultJobQueue()
	if queue == nil {
		t.Fatalf("expected non-nil slice from DefaultJobQueue()")
	}
}

func TestFilterAndSortJobQueue(t *testing.T) {
	queue := []*JobItem{
		{ID: "J-1", Status: JobQueued},
		{ID: "J-2", Status: JobPassed},
		{ID: "J-3", Status: JobRunning},
	}

	filtered := FilterAndSortJobQueue(queue)
	if len(filtered) != 2 {
		t.Fatalf("expected 2 items after filtering passed jobs, got %d", len(filtered))
	}
	if filtered[0].Status != JobRunning {
		t.Errorf("expected running job first, got: %s", filtered[0].Status)
	}
}

func TestPollStep(t *testing.T) {
	startTime := time.Now().Add(-90 * time.Second)
	runners := []*RunnerItem{
		{
			ID:     "runner-1",
			Name:   "test-runner",
			Status: RunnerRunning,
		},
	}
	queue := []*JobItem{
		{
			ID:        "J-1",
			Name:      "test job",
			Status:    JobRunning,
			StartedAt: startTime,
		},
	}

	PollStep(runners, queue)

	if queue[0].Duration == "-" || queue[0].Duration == "" {
		t.Errorf("expected duration to be set after PollStep, got: %q", queue[0].Duration)
	}
	if queue[0].Seconds < 90 {
		t.Errorf("expected elapsed seconds >= 90, got: %d", queue[0].Seconds)
	}
}

func TestMergeRunnersCrossReference(t *testing.T) {
	newRunners := []*RunnerItem{
		{ID: "runner-1", Name: "carey-mac-alpha", Status: RunnerIdle, Platform: "macOS/ARM64", Tags: []string{"self-hosted"}},
		{ID: "runner-2", Name: "carey-mac-beta", Status: RunnerIdle, Platform: "macOS/ARM64", Tags: []string{"self-hosted"}},
	}
	existingRunners := []*RunnerItem{
		{ID: "runner-1", Name: "carey-mac-alpha", OutputLogs: []string{"log line 1", "log line 2"}},
	}
	jobQueue := []*JobItem{
		{ID: "#101", Name: ".dotfiles / test", Status: JobRunning, RunnerName: "carey-mac-alpha"},
	}

	merged := MergeRunners(newRunners, existingRunners, jobQueue)

	if len(merged) != 2 {
		t.Fatalf("expected 2 merged runners, got %d", len(merged))
	}

	// carey-mac-alpha should inherit existing logs and have active job set
	alpha := merged[0]
	if alpha.Status != RunnerRunning {
		t.Errorf("expected carey-mac-alpha status RUNNING, got %s", alpha.Status)
	}
	if alpha.CurrentJobID != "#101" {
		t.Errorf("expected CurrentJobID #101, got %s", alpha.CurrentJobID)
	}
	if alpha.CurrentJob != ".dotfiles / test" {
		t.Errorf("expected CurrentJob '.dotfiles / test', got %s", alpha.CurrentJob)
	}
	if len(alpha.OutputLogs) != 2 {
		t.Errorf("expected existing logs to be preserved, got %d lines", len(alpha.OutputLogs))
	}

	// carey-mac-beta should have initial online log lines
	beta := merged[1]
	if beta.Status != RunnerIdle {
		t.Errorf("expected carey-mac-beta status IDLE, got %s", beta.Status)
	}
	if len(beta.OutputLogs) < 2 {
		t.Errorf("expected default log output for new runner, got %d lines", len(beta.OutputLogs))
	}
	if !strings.Contains(beta.OutputLogs[0], "Runner online") {
		t.Errorf("expected 'Runner online' in new runner logs, got: %s", beta.OutputLogs[0])
	}
}

// stubGH swaps the gh runner for the duration of a test, so the org job queue
// can be exercised against canned API responses instead of the network.
func stubGH(t *testing.T, handler func(path string) ([]byte, error)) {
	t.Helper()
	original := runGH
	t.Cleanup(func() { runGH = original })

	runGH = func(args ...string) ([]byte, error) {
		return handler(args[len(args)-1])
	}
}

// runsJSON is one repository's run list: an in-progress run, a queued run whose
// jobs have not materialised yet, and a completed run that must be ignored.
func runsJSON(base int64) string {
	return fmt.Sprintf(`{"total_count":3,"workflow_runs":[
	  {"id":%d,"name":"CI","display_title":"fix things","status":"in_progress","event":"pull_request","head_branch":"topic","created_at":"2026-08-16T10:00:00Z","run_started_at":"2026-08-16T10:00:05Z","pull_requests":[{"number":7,"title":"Fix things","html_url":"https://example.invalid/pull/7"}]},
	  {"id":%d,"name":"CI","display_title":"queued one","status":"queued","event":"push","head_branch":"main","created_at":"2026-08-16T10:01:00Z"},
	  {"id":%d,"name":"CI","display_title":"done","status":"completed","event":"push","head_branch":"main","created_at":"2026-08-16T09:00:00Z"}
	]}`, base+1, base+2, base+3)
}

// jobsJSON pairs with runsJSON: a live job plus a successful one that the queue drops.
func jobsJSON(base int64) string {
	return fmt.Sprintf(`{"total_count":2,"jobs":[
	  {"id":%d,"name":"build","status":"in_progress","started_at":"2026-08-16T10:00:10Z","runner_name":"carey-mac-alpha","runner_id":5},
	  {"id":%d,"name":"lint","status":"completed","conclusion":"success"}
	]}`, base+101, base+102)
}

// repoQueueHandler serves the two-endpoint conversation for a fixed set of repos,
// giving each repo its own ID space the way GitHub does.
func repoQueueHandler(bases map[string]int64) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		for repo, base := range bases {
			prefix := "/repos/acme/" + repo + "/"
			if !strings.HasPrefix(path, prefix) {
				continue
			}
			switch {
			case strings.Contains(path, fmt.Sprintf("/runs/%d/jobs", base+1)):
				return []byte(jobsJSON(base)), nil
			case strings.Contains(path, "/jobs?filter=latest"):
				// The queued run has no jobs yet.
				return []byte(`{"total_count":0,"jobs":[]}`), nil
			case strings.Contains(path, "/actions/runs?per_page="):
				return []byte(runsJSON(base)), nil
			}
		}
		return nil, fmt.Errorf("gh api: gh: Not Found (HTTP 404)")
	}
}

// FetchOrgJobQueue must poll per repository — GitHub has no org-wide workflow
// runs endpoint — and must leave completed runs out of the queue.
func TestFetchOrgJobQueuePerRepo(t *testing.T) {
	stubGH(t, repoQueueHandler(map[string]int64{"alpha": 1000, "beta": 2000}))

	queue, err := FetchOrgJobQueue("acme", []string{"alpha", "beta"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Per repo: the in-progress run contributes its live job (the successful
	// "lint" job is dropped) and the queued run falls back to a run header.
	if len(queue) != 4 {
		t.Fatalf("expected 4 queue items across 2 repos, got %d", len(queue))
	}

	perRepo := map[string]int{}
	for _, j := range queue {
		perRepo[j.Repo]++
		if j.RunID == 1003 || j.RunID == 2003 {
			t.Errorf("completed run %d should not appear in the queue", j.RunID)
		}
	}
	if perRepo["alpha"] != 2 || perRepo["beta"] != 2 {
		t.Errorf("expected 2 items per repo, got %v", perRepo)
	}
	if queue[0].Status != JobRunning {
		t.Errorf("expected a running job sorted first, got %s", queue[0].Status)
	}
	if queue[0].PRNumber != 7 {
		t.Errorf("expected PR metadata carried onto job items, got %d", queue[0].PRNumber)
	}
}

// One unreachable repository must not blank out the whole queue.
func TestFetchOrgJobQueuePartialFailure(t *testing.T) {
	stubGH(t, repoQueueHandler(map[string]int64{"alpha": 1000}))

	queue, err := FetchOrgJobQueue("acme", []string{"alpha", "gone"})
	if err != nil {
		t.Fatalf("partial failure should not surface an error: %v", err)
	}
	if len(queue) != 2 {
		t.Fatalf("expected the healthy repo's 2 items, got %d", len(queue))
	}

	// A sweep where every repo fails is a real error.
	stubGH(t, repoQueueHandler(nil))
	if _, err := FetchOrgJobQueue("acme", []string{"alpha", "gone"}); err == nil {
		t.Fatal("expected an error when every repository fails")
	}

	// Rate limiting is reported distinctly so the UI can surface it.
	stubGH(t, func(string) ([]byte, error) {
		return nil, fmt.Errorf("GitHub API rate limit exceeded")
	})
	if _, err := FetchOrgJobQueue("acme", []string{"alpha"}); err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("expected a rate limit error, got %v", err)
	}
}

func TestFetchOrgJobQueueNoRepos(t *testing.T) {
	stubGH(t, func(path string) ([]byte, error) {
		t.Errorf("no repos means no API calls, but %q was requested", path)
		return nil, nil
	})
	queue, err := FetchOrgJobQueue("acme", nil)
	if err != nil || len(queue) != 0 {
		t.Fatalf("expected an empty queue and no error, got %d items, err %v", len(queue), err)
	}
}
