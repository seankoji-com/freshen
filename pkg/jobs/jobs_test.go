package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// mockGH installs a fake runGHCommand for the duration of the test and
// restores the real implementation on cleanup.
func mockGH(t *testing.T, fn func(args []string) ([]byte, error)) {
	t.Helper()
	orig := runGHCommand
	runGHCommand = func(args ...string) ([]byte, error) {
		return fn(args)
	}
	t.Cleanup(func() { runGHCommand = orig })
}

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

func TestFetchOrgRunners(t *testing.T) {
	t.Run("valid runners", func(t *testing.T) {
		resp := GHRunnersResponse{
			TotalCount: 2,
			Runners: []GHRunnerInfo{
				{ID: 1, Name: "carey-mac-alpha\x1b[31m", OS: "macOS", Status: "online", Busy: true, Labels: []GHRunnerLabel{{Name: "self-hosted"}, {Name: "ARM64"}}},
				{ID: 2, Name: "carey-mac-beta", OS: "macOS", Status: "offline", Busy: false, Labels: []GHRunnerLabel{{Name: "self-hosted"}}},
			},
		}
		mockGH(t, func(args []string) ([]byte, error) {
			if len(args) < 2 || !strings.Contains(args[1], "actions/runners") {
				t.Fatalf("unexpected args: %v", args)
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
		mockGH(t, func(args []string) ([]byte, error) {
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
		mockGH(t, func(args []string) ([]byte, error) {
			return []byte("{not valid json"), nil
		})
		_, err := FetchOrgRunners("acme")
		if err == nil || !strings.Contains(err.Error(), "failed to parse runners JSON") {
			t.Fatalf("expected parse error, got %v", err)
		}
	})

	t.Run("gh command failure", func(t *testing.T) {
		mockGH(t, func(args []string) ([]byte, error) {
			return nil, errors.New("gh api: 404 not found")
		})
		if _, err := FetchOrgRunners("acme"); err == nil {
			t.Fatal("expected error to propagate")
		}
	})
}

func TestFetchOrgJobQueue(t *testing.T) {
	t.Run("in_progress run with jobs parsed", func(t *testing.T) {
		runsResp := GHWorkflowRunsResponse{
			TotalCount: 1,
			WorkflowRuns: []GHWorkflowRun{
				{
					ID:           100,
					Name:         "CI",
					DisplayTitle: "fix bug",
					Status:       "in_progress",
					Event:        "push",
					HeadBranch:   "main",
					CreatedAt:    time.Now().UTC().Format(time.RFC3339),
					PullRequests: []GHPullRequestInfo{{Number: 5, Title: "fix bug\x1b[31m", URL: ""}},
					Repository: struct {
						Name string `json:"name"`
					}{Name: "freshen"},
				},
			},
		}
		jobsResp := GHJobsResponse{
			TotalCount: 1,
			Jobs: []GHJobInfo{
				{ID: 200, Name: "build", Status: "in_progress", RunnerName: "runner-x", RunnerID: 7, StartedAt: time.Now().UTC().Format(time.RFC3339)},
			},
		}

		mockGH(t, func(args []string) ([]byte, error) {
			url := args[1]
			switch {
			case strings.Contains(url, "status=in_progress"):
				return mustJSON(t, runsResp), nil
			case strings.Contains(url, "status=queued"):
				return mustJSON(t, GHWorkflowRunsResponse{}), nil
			case strings.Contains(url, "actions/runs/100/jobs"):
				return mustJSON(t, jobsResp), nil
			}
			t.Fatalf("unexpected args: %v", args)
			return nil, nil
		})

		jobs, err := FetchOrgJobQueue("acme", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(jobs) != 1 {
			t.Fatalf("expected 1 job, got %d", len(jobs))
		}
		j := jobs[0]
		if j.Name != "freshen / build" {
			t.Errorf("expected name 'freshen / build', got %q", j.Name)
		}
		if j.PRNumber != 5 {
			t.Errorf("expected PR number 5, got %d", j.PRNumber)
		}
		if j.PRTitle != "fix bug[31m" {
			t.Errorf("expected sanitized PR title, got %q", j.PRTitle)
		}
		if j.PRURL != "https://github.com/acme/freshen/pull/5" {
			t.Errorf("expected constructed PR URL, got %q", j.PRURL)
		}
		if j.RunnerID != "runner-7" {
			t.Errorf("expected runner-7, got %q", j.RunnerID)
		}
	})

	t.Run("queued run falls back when jobs endpoint empty", func(t *testing.T) {
		runsResp := GHWorkflowRunsResponse{
			WorkflowRuns: []GHWorkflowRun{
				{
					ID:         101,
					Name:       "CI",
					Status:     "queued",
					Event:      "push",
					HeadBranch: "main",
					CreatedAt:  time.Now().UTC().Format(time.RFC3339),
					Repository: struct {
						Name string `json:"name"`
					}{Name: "freshen"},
				},
			},
		}

		mockGH(t, func(args []string) ([]byte, error) {
			url := args[1]
			switch {
			case strings.Contains(url, "status=in_progress"):
				return mustJSON(t, GHWorkflowRunsResponse{}), nil
			case strings.Contains(url, "status=queued"):
				return mustJSON(t, runsResp), nil
			case strings.Contains(url, "actions/runs/101/jobs"):
				return mustJSON(t, GHJobsResponse{}), nil
			}
			t.Fatalf("unexpected args: %v", args)
			return nil, nil
		})

		jobs, err := FetchOrgJobQueue("acme", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(jobs) != 1 {
			t.Fatalf("expected 1 fallback job, got %d", len(jobs))
		}
		if jobs[0].Status != JobQueued {
			t.Errorf("expected fallback job QUEUED, got %s", jobs[0].Status)
		}
		if jobs[0].ID != "#101" {
			t.Errorf("expected fallback ID #101, got %s", jobs[0].ID)
		}
	})

	t.Run("jobs endpoint unmarshal error still falls back (#75)", func(t *testing.T) {
		runsResp := GHWorkflowRunsResponse{
			WorkflowRuns: []GHWorkflowRun{
				{
					ID:         102,
					Name:       "CI",
					Status:     "in_progress",
					Event:      "push",
					HeadBranch: "main",
					CreatedAt:  time.Now().UTC().Format(time.RFC3339),
					Repository: struct {
						Name string `json:"name"`
					}{Name: "freshen"},
				},
			},
		}

		mockGH(t, func(args []string) ([]byte, error) {
			url := args[1]
			switch {
			case strings.Contains(url, "status=in_progress"):
				return mustJSON(t, runsResp), nil
			case strings.Contains(url, "status=queued"):
				return mustJSON(t, GHWorkflowRunsResponse{}), nil
			case strings.Contains(url, "actions/runs/102/jobs"):
				return []byte("{not valid json"), nil
			}
			t.Fatalf("unexpected args: %v", args)
			return nil, nil
		})

		jobs, err := FetchOrgJobQueue("acme", nil)
		if err == nil || !strings.Contains(err.Error(), "failed fetching org job queue") {
			t.Fatalf("expected aggregated fetch error, got %v", err)
		}
		if len(jobs) != 1 {
			t.Fatalf("expected fallback job despite parse error, got %d", len(jobs))
		}
		if jobs[0].Status != JobRunning {
			t.Errorf("expected fallback job RUNNING (run.Status in_progress), got %s", jobs[0].Status)
		}
	})

	t.Run("rate limit errors aggregate into single message", func(t *testing.T) {
		mockGH(t, func(args []string) ([]byte, error) {
			url := args[1]
			switch {
			case strings.Contains(url, "status=in_progress"):
				return nil, fmt.Errorf("GitHub API rate limit exceeded")
			case strings.Contains(url, "status=queued"):
				return mustJSON(t, GHWorkflowRunsResponse{}), nil
			}
			t.Fatalf("unexpected args: %v", args)
			return nil, nil
		})

		jobs, err := FetchOrgJobQueue("acme", nil)
		if err == nil || err.Error() != "GitHub API rate limit exceeded" {
			t.Fatalf("expected rate limit error, got %v", err)
		}
		if len(jobs) != 0 {
			t.Errorf("expected no jobs on rate limit, got %d", len(jobs))
		}
	})

	t.Run("repos filter excludes untracked repositories", func(t *testing.T) {
		runsResp := GHWorkflowRunsResponse{
			WorkflowRuns: []GHWorkflowRun{
				{ID: 103, Status: "queued", CreatedAt: time.Now().UTC().Format(time.RFC3339), Repository: struct {
					Name string `json:"name"`
				}{Name: "other-repo"}},
			},
		}
		mockGH(t, func(args []string) ([]byte, error) {
			url := args[1]
			switch {
			case strings.Contains(url, "status=in_progress"):
				return mustJSON(t, GHWorkflowRunsResponse{}), nil
			case strings.Contains(url, "status=queued"):
				return mustJSON(t, runsResp), nil
			}
			t.Fatalf("unexpected args: %v", args)
			return nil, nil
		})

		jobs, err := FetchOrgJobQueue("acme", []string{"freshen"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(jobs) != 0 {
			t.Errorf("expected repo filter to exclude run, got %d jobs", len(jobs))
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
		mockGH(t, func(args []string) ([]byte, error) {
			return nil, errors.New("boom")
		})
		_, _, err := FetchJobLogs("acme", "repo", 1, 0, "", 10)
		if err == nil || !strings.Contains(err.Error(), "jobs list:") {
			t.Fatalf("expected jobs list error, got %v", err)
		}
	})

	t.Run("jobs list malformed JSON", func(t *testing.T) {
		mockGH(t, func(args []string) ([]byte, error) {
			return []byte("{bad"), nil
		})
		_, _, err := FetchJobLogs("acme", "repo", 1, 0, "", 10)
		if err == nil || !strings.Contains(err.Error(), "jobs parse:") {
			t.Fatalf("expected jobs parse error, got %v", err)
		}
	})

	t.Run("no job found", func(t *testing.T) {
		mockGH(t, func(args []string) ([]byte, error) {
			return mustJSON(t, GHJobsResponse{}), nil
		})
		_, _, err := FetchJobLogs("acme", "repo", 1, 0, "", 10)
		if err == nil || !strings.Contains(err.Error(), "no job found") {
			t.Fatalf("expected no-job error, got %v", err)
		}
	})

	t.Run("matches by GHJobID and returns trimmed raw logs", func(t *testing.T) {
		jobsResp := GHJobsResponse{Jobs: []GHJobInfo{{ID: 55, Name: "build", Status: "in_progress"}}}
		mockGH(t, func(args []string) ([]byte, error) {
			url := args[1]
			if strings.Contains(url, "jobs?filter=latest") {
				return mustJSON(t, jobsResp), nil
			}
			if strings.Contains(url, "actions/jobs/55/logs") {
				return []byte("line1\nline2\nline3\n"), nil
			}
			t.Fatalf("unexpected args: %v", args)
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
		mockGH(t, func(args []string) ([]byte, error) {
			url := args[1]
			if strings.Contains(url, "jobs?filter=latest") {
				return mustJSON(t, jobsResp), nil
			}
			if strings.Contains(url, "actions/jobs/60/logs") {
				return nil, errors.New("logs unavailable")
			}
			t.Fatalf("unexpected args: %v", args)
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
		mockGH(t, func(args []string) ([]byte, error) {
			url := args[1]
			if strings.Contains(url, "jobs?filter=latest") {
				return mustJSON(t, jobsResp), nil
			}
			if strings.Contains(url, "actions/jobs/70/logs") {
				return []byte("only line\n"), nil
			}
			t.Fatalf("unexpected args: %v", args)
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
