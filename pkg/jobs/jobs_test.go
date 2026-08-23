package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return b
}

func TestSanitizeTerminal(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "strips OSC 8 hyperlink injection",
			input: "evil title \x1b]8;;https://evil.example\x1b\\click me\x1b]8;;\x1b\\",
			want:  "evil title ]8;;https://evil.example\\click me]8;;\\",
		},
		{
			name:  "strips bare ESC and control bytes",
			input: "run\x1b[31mred\x1b[0m\x07bell\x7fdel",
			want:  "run[31mred[0mbelldel",
		},
		{
			name:  "preserves spaces and normal text",
			input: "fix: normal PR title #64",
			want:  "fix: normal PR title #64",
		},
		{
			name:  "preserves UTF-8 multibyte runes",
			input: "unicode ✅ 日本語",
			want:  "unicode ✅ 日本語",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeTerminal(tc.input)
			if got != tc.want {
				t.Errorf("SanitizeTerminal(%q) = %q, want %q", tc.input, got, tc.want)
			}
			if strings.ContainsAny(got, "\x1b\x07\x7f") {
				t.Errorf("SanitizeTerminal(%q) = %q still contains control chars", tc.input, got)
			}
		})
	}
}

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
	  {"id":%d,"name":"CI","display_title":"done","status":"completed","event":"push","head_branch":"main","created_at":"2026-08-16T09:00:00Z","run_started_at":"2026-08-16T09:00:05Z","completed_at":"2026-08-16T09:05:05Z"}
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

	queue, _, err := FetchOrgJobQueue("acme", []string{"alpha", "beta"})
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

	queue, _, err := FetchOrgJobQueue("acme", []string{"alpha", "gone"})
	if err != nil {
		t.Fatalf("partial failure should not surface an error: %v", err)
	}
	if len(queue) != 2 {
		t.Fatalf("expected the healthy repo's 2 items, got %d", len(queue))
	}

	// A sweep where every repo fails is a real error.
	stubGH(t, repoQueueHandler(nil))
	if _, _, err := FetchOrgJobQueue("acme", []string{"alpha", "gone"}); err == nil {
		t.Fatal("expected an error when every repository fails")
	}

	// Rate limiting is reported distinctly so the UI can surface it.
	stubGH(t, func(string) ([]byte, error) {
		return nil, fmt.Errorf("GitHub API rate limit exceeded")
	})
	if _, _, err := FetchOrgJobQueue("acme", []string{"alpha"}); err == nil || !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("expected a rate limit error, got %v", err)
	}
}

func TestFetchOrgJobQueueNoRepos(t *testing.T) {
	stubGH(t, func(path string) ([]byte, error) {
		t.Errorf("no repos means no API calls, but %q was requested", path)
		return nil, nil
	})
	queue, _, err := FetchOrgJobQueue("acme", nil)
	if err != nil || len(queue) != 0 {
		t.Fatalf("expected an empty queue and no error, got %d items, err %v", len(queue), err)
	}
}

// FetchOrgJobQueue must surface completed-run durations from the same
// unfiltered runs page it already fetches, keyed by workflow name, without
// any extra API calls — the fixture's "done" run in runsJSON carries the
// timestamps this depends on.
func TestFetchOrgJobQueueHistoricalDurations(t *testing.T) {
	stubGH(t, repoQueueHandler(map[string]int64{"alpha": 1000, "beta": 2000}))

	_, history, err := FetchOrgJobQueue("acme", []string{"alpha", "beta"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	samples := history["CI"]
	if len(samples) != 2 {
		t.Fatalf("expected 2 historical samples (one completed run per repo) for workflow %q, got %d: %v", "CI", len(samples), samples)
	}
	for _, d := range samples {
		if d != 5*time.Minute {
			t.Errorf("expected each sample to be the fixture's 5m completed-run duration, got %s", d)
		}
	}
}

func TestFetchOrgRunners(t *testing.T) {
	t.Run("valid runners", func(t *testing.T) {
		resp := GHRunnersResponse{
			TotalCount: 2,
			Runners: []GHRunnerInfo{
				{ID: 1, Name: "carey-mac-alpha\x1b[31m", OS: "macOS", Status: "online", Busy: true, Labels: []GHRunnerLabel{{Name: "self-hosted"}, {Name: "ARM64"}}},
				{ID: 2, Name: "carey-mac-beta", OS: "macOS", Status: "offline", Busy: false, Labels: []GHRunnerLabel{{Name: "self-hosted"}}},
			},
		}
		stubGH(t, func(path string) ([]byte, error) {
			if !strings.Contains(path, "actions/runners") {
				t.Fatalf("unexpected path: %s", path)
			}
			return mustJSON(t, resp), nil
		})

		runners, err := FetchOrgRunners("acme")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(runners) != 2 {
			t.Fatalf("expected 2 runners, got %d", len(runners))
		}
		if runners[0].Status != RunnerRunning {
			t.Errorf("expected busy runner RUNNING, got %s", runners[0].Status)
		}
		if runners[0].Platform != "macOS/ARM64" {
			t.Errorf("expected platform macOS/ARM64, got %q", runners[0].Platform)
		}
		if strings.ContainsAny(runners[0].Name, "\x1b") {
			t.Errorf("expected sanitized runner name, got %q", runners[0].Name)
		}
		if runners[1].Status != RunnerOffline {
			t.Errorf("expected offline runner OFFLINE, got %s", runners[1].Status)
		}
	})

	t.Run("empty runners", func(t *testing.T) {
		stubGH(t, func(path string) ([]byte, error) {
			return mustJSON(t, GHRunnersResponse{TotalCount: 0}), nil
		})
		runners, err := FetchOrgRunners("acme")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(runners) != 0 {
			t.Errorf("expected 0 runners, got %d", len(runners))
		}
	})

	t.Run("malformed JSON", func(t *testing.T) {
		stubGH(t, func(path string) ([]byte, error) {
			return []byte("{not valid json"), nil
		})
		_, err := FetchOrgRunners("acme")
		if err == nil || !strings.Contains(err.Error(), "failed to parse runners JSON") {
			t.Fatalf("expected parse error, got %v", err)
		}
	})

	t.Run("gh command failure", func(t *testing.T) {
		stubGH(t, func(path string) ([]byte, error) {
			return nil, errors.New("gh api: 404 not found")
		})
		if _, err := FetchOrgRunners("acme"); err == nil {
			t.Fatal("expected error to propagate")
		}
	})
}

func TestExtractPRInfo(t *testing.T) {
	cases := []struct {
		name        string
		run         GHWorkflowRun
		wantNum     int
		wantTitle   string
		wantURLFunc func(t *testing.T, got string)
	}{
		{
			name: "PR with URL present",
			run: GHWorkflowRun{
				PullRequests: []GHPullRequestInfo{{Number: 5, Title: "my title", URL: "https://github.com/acme/repo/pull/5"}},
			},
			wantNum:   5,
			wantTitle: "my title",
			wantURLFunc: func(t *testing.T, got string) {
				if got != "https://github.com/acme/repo/pull/5" {
					t.Errorf("expected provided URL preserved, got %q", got)
				}
			},
		},
		{
			name: "PR without URL builds one",
			run: GHWorkflowRun{
				PullRequests: []GHPullRequestInfo{{Number: 9, Title: "another\x1b[0m"}},
			},
			wantNum:   9,
			wantTitle: "another[0m",
			wantURLFunc: func(t *testing.T, got string) {
				if got != "https://github.com/acme/repo/pull/9" {
					t.Errorf("expected constructed URL, got %q", got)
				}
			},
		},
		{
			name:      "no PR falls back to DisplayTitle",
			run:       GHWorkflowRun{DisplayTitle: "run title\x07"},
			wantNum:   0,
			wantTitle: "run title",
			wantURLFunc: func(t *testing.T, got string) {
				if got != "" {
					t.Errorf("expected empty URL, got %q", got)
				}
			},
		},
		{
			name:      "no PR, no DisplayTitle",
			run:       GHWorkflowRun{},
			wantNum:   0,
			wantTitle: "",
			wantURLFunc: func(t *testing.T, got string) {
				if got != "" {
					t.Errorf("expected empty URL, got %q", got)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			num, title, url := extractPRInfo(tc.run, "acme", "repo")
			if num != tc.wantNum {
				t.Errorf("expected PR number %d, got %d", tc.wantNum, num)
			}
			if title != tc.wantTitle {
				t.Errorf("expected title %q, got %q", tc.wantTitle, title)
			}
			tc.wantURLFunc(t, url)
		})
	}
}

func TestFetchJobLogs(t *testing.T) {
	t.Run("jobs list command fails", func(t *testing.T) {
		stubGH(t, func(path string) ([]byte, error) {
			return nil, errors.New("boom")
		})
		_, _, err := FetchJobLogs("acme", "repo", 1, 0, "", 10)
		if err == nil || !strings.Contains(err.Error(), "jobs list:") {
			t.Fatalf("expected jobs list error, got %v", err)
		}
	})

	t.Run("jobs list malformed JSON", func(t *testing.T) {
		stubGH(t, func(path string) ([]byte, error) {
			return []byte("{bad"), nil
		})
		_, _, err := FetchJobLogs("acme", "repo", 1, 0, "", 10)
		if err == nil || !strings.Contains(err.Error(), "jobs parse:") {
			t.Fatalf("expected jobs parse error, got %v", err)
		}
	})

	t.Run("no job found", func(t *testing.T) {
		stubGH(t, func(path string) ([]byte, error) {
			return mustJSON(t, GHJobsResponse{}), nil
		})
		_, _, err := FetchJobLogs("acme", "repo", 1, 0, "", 10)
		if err == nil || !strings.Contains(err.Error(), "no job found") {
			t.Fatalf("expected no-job error, got %v", err)
		}
	})

	t.Run("matches by GHJobID and returns trimmed raw logs", func(t *testing.T) {
		jobsResp := GHJobsResponse{Jobs: []GHJobInfo{{ID: 55, Name: "build", Status: "in_progress"}}}
		stubGH(t, func(path string) ([]byte, error) {
			if strings.Contains(path, "jobs?filter=latest") {
				return mustJSON(t, jobsResp), nil
			}
			if strings.Contains(path, "actions/jobs/55/logs") {
				return []byte("line1\nline2\nline3\n"), nil
			}
			t.Fatalf("unexpected path: %v", path)
			return nil, nil
		})

		lines, jobID, err := FetchJobLogs("acme", "repo", 1, 55, "", 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if jobID != 55 {
			t.Errorf("expected jobID 55, got %d", jobID)
		}
		if len(lines) != 2 || lines[0] != "line2" || lines[1] != "line3" {
			t.Errorf("expected last 2 lines, got %v", lines)
		}
	})

	t.Run("falls back to step glyphs when logs unavailable", func(t *testing.T) {
		jobsResp := GHJobsResponse{Jobs: []GHJobInfo{
			{ID: 60, Name: "build", Status: "in_progress", Steps: []GHJobStep{
				{Number: 1, Name: "checkout", Status: "completed", Conclusion: "success"},
				{Number: 2, Name: "test", Status: "in_progress"},
			}},
		}}
		stubGH(t, func(path string) ([]byte, error) {
			if strings.Contains(path, "jobs?filter=latest") {
				return mustJSON(t, jobsResp), nil
			}
			if strings.Contains(path, "actions/jobs/60/logs") {
				return nil, errors.New("logs unavailable")
			}
			t.Fatalf("unexpected path: %v", path)
			return nil, nil
		})

		lines, jobID, err := FetchJobLogs("acme", "repo", 1, 60, "", 10)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if jobID != 60 {
			t.Errorf("expected jobID 60, got %d", jobID)
		}
		if len(lines) != 2 {
			t.Fatalf("expected 2 step lines, got %d: %v", len(lines), lines)
		}
		if !strings.Contains(lines[0], "✓") || !strings.Contains(lines[0], "checkout") {
			t.Errorf("expected success glyph for checkout step, got %q", lines[0])
		}
		if !strings.Contains(lines[1], "▶") || !strings.Contains(lines[1], "test") {
			t.Errorf("expected in_progress glyph for test step, got %q", lines[1])
		}
	})

	t.Run("matches by job name substring fallback", func(t *testing.T) {
		jobsResp := GHJobsResponse{Jobs: []GHJobInfo{{ID: 70, Name: "build-and-test", Status: "queued"}}}
		stubGH(t, func(path string) ([]byte, error) {
			if strings.Contains(path, "jobs?filter=latest") {
				return mustJSON(t, jobsResp), nil
			}
			if strings.Contains(path, "actions/jobs/70/logs") {
				return []byte("only line\n"), nil
			}
			t.Fatalf("unexpected path: %v", path)
			return nil, nil
		})

		_, jobID, err := FetchJobLogs("acme", "repo", 1, 0, "build", 10)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if jobID != 70 {
			t.Errorf("expected substring match to find job 70, got %d", jobID)
		}
	})
}

func TestClassifyGHError(t *testing.T) {
	cases := []struct {
		name    string
		stderr  string
		execErr error
		want    string
	}{
		{
			name:    "rate limit message normalized",
			stderr:  "HTTP 403: API rate limit exceeded for installation ID 123.",
			execErr: errors.New("exit status 1"),
			want:    "GitHub API rate limit exceeded",
		},
		{
			name:    "multiline stderr truncated to first line",
			stderr:  "first line of error\nsecond line\nthird line",
			execErr: errors.New("exit status 1"),
			want:    "gh api: first line of error",
		},
		{
			name:    "empty stderr falls back to wrapped exec error",
			stderr:  "",
			execErr: errors.New("exit status 127"),
			want:    "gh api: exit status 127",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyGHError(tc.stderr, tc.execErr)
			if got == nil || got.Error() != tc.want {
				t.Errorf("classifyGHError(%q, %v) = %v, want %q", tc.stderr, tc.execErr, got, tc.want)
			}
		})
	}
}
