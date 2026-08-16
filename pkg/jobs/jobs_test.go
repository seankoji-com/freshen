package jobs

import (
	"strings"
	"testing"
	"time"
)

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
