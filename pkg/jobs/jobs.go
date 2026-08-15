package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

type RunnerStatus string

const (
	RunnerIdle        RunnerStatus = "IDLE"
	RunnerRunning     RunnerStatus = "RUNNING"
	RunnerOffline     RunnerStatus = "OFFLINE"
	RunnerMaintenance RunnerStatus = "MAINTENANCE"
)

type JobStatus string

const (
	JobQueued    JobStatus = "QUEUED"
	JobRunning   JobStatus = "RUNNING"
	JobPassed    JobStatus = "PASSED"
	JobFailed    JobStatus = "FAILED"
	JobCancelled JobStatus = "CANCELLED"
)

type RunnerItem struct {
	ID            string
	Name          string
	Platform      string
	Status        RunnerStatus
	CurrentJobID  string
	CurrentJob    string
	Tags          []string
	OutputLogs    []string
	LastHeartbeat time.Time
	StepCount     int
}

type JobItem struct {
	ID          string
	Name        string
	Repo        string
	Branch      string
	Event       string
	PRNumber    int
	PRTitle     string
	PRURL       string
	Status      JobStatus
	RunnerID    string
	RunnerName  string
	QueuedAt    string
	Duration    string
	Logs        []string
	Seconds     int
	StartedAt   time.Time
	RunID       int64 // GitHub workflow run ID
	GHJobID     int64 // GitHub job ID within the run (populated lazily)
	IsRunHeader bool  // True when this JobItem is a run-level header, not a specific job
}

// parseNumericID extracts the integer portion of a job ID string (e.g. "#123" -> 123).
// Returns 0 if parsing fails, so the sort remains stable (string fallback).
func parseNumericID(id string) int {
	s := strings.TrimPrefix(id, "#")
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return 0
}

// --- GitHub API response types ---

type GHRunnerLabel struct {
	Name string `json:"name"`
}

type GHRunnerInfo struct {
	ID     int             `json:"id"`
	Name   string          `json:"name"`
	OS     string          `json:"os"`
	Status string          `json:"status"`
	Busy   bool            `json:"busy"`
	Labels []GHRunnerLabel `json:"labels"`
}

type GHRunnersResponse struct {
	TotalCount int            `json:"total_count"`
	Runners    []GHRunnerInfo `json:"runners"`
}

type GHPullRequestInfo struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"html_url"`
}

type GHWorkflowRun struct {
	ID           int64               `json:"id"`
	Name         string              `json:"name"`
	DisplayTitle string              `json:"display_title"`
	Status       string              `json:"status"`
	Event        string              `json:"event"`
	HeadBranch   string              `json:"head_branch"`
	CreatedAt    string              `json:"created_at"`
	UpdatedAt    string              `json:"updated_at"`
	RunStartedAt string              `json:"run_started_at"`
	RunnerName   string              `json:"runner_name"`
	RunnerID     int                 `json:"runner_id"`
	PullRequests []GHPullRequestInfo `json:"pull_requests"`
	Repository   struct {
		Name string `json:"name"`
	} `json:"repository"`
}

type GHWorkflowRunsResponse struct {
	TotalCount   int             `json:"total_count"`
	WorkflowRuns []GHWorkflowRun `json:"workflow_runs"`
}

type GHJobStep struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Number     int    `json:"number"`
}

type GHJobInfo struct {
	ID         int64       `json:"id"`
	Name       string      `json:"name"`
	Status     string      `json:"status"`
	Conclusion string      `json:"conclusion"`
	StartedAt  string      `json:"started_at"`
	RunnerName string      `json:"runner_name"`
	RunnerID   int64       `json:"runner_id"`
	Steps      []GHJobStep `json:"steps"`
}

type GHJobsResponse struct {
	TotalCount int         `json:"total_count"`
	Jobs       []GHJobInfo `json:"jobs"`
}

// ghCommandTimeout is the maximum duration for any gh CLI invocation.
const ghCommandTimeout = 30 * time.Second

// runGHCommand runs gh with the given args, capturing both stdout and stderr.
// Returns the stdout bytes. On failure, the error includes stderr content.
func runGHCommand(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), ghCommandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "gh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("gh api call timed out after %s: args=%v", ghCommandTimeout, args)
	}
	if err != nil {
		errStr := strings.TrimSpace(stderr.String())
		if strings.Contains(errStr, "API rate limit exceeded") {
			return nil, fmt.Errorf("GitHub API rate limit exceeded")
		}
		// Take only the first line of stderr to avoid multiline dumps in UI toasts
		if firstLine, _, ok := strings.Cut(errStr, "\n"); ok && firstLine != "" {
			errStr = firstLine
		}
		if errStr != "" {
			return nil, fmt.Errorf("gh api: %s", errStr)
		}
		return nil, fmt.Errorf("gh api: %w", err)
	}
	return stdout.Bytes(), nil
}

// formatDuration returns a human-readable duration string from seconds.
func formatDuration(secs int) string {
	if secs <= 0 {
		return "-"
	}
	if secs >= 60 {
		return fmt.Sprintf("%dm %ds", secs/60, secs%60)
	}
	return fmt.Sprintf("%ds", secs)
}

// formatQueuedAgo returns a relative-time string from a timestamp.
func formatQueuedAgo(timestamp string) string {
	if timestamp == "" {
		return "-"
	}
	t, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return "-"
	}
	ago := int(time.Since(t).Seconds())
	if ago >= 3600 {
		return fmt.Sprintf("%dh %dm ago", ago/3600, (ago%3600)/60)
	}
	if ago >= 60 {
		return fmt.Sprintf("%dm ago", ago/60)
	}
	if ago > 0 {
		return fmt.Sprintf("%ds ago", ago)
	}
	return "just now"
}

// extractPRInfo extracts PR number, title, and URL from a workflow run.
func extractPRInfo(run GHWorkflowRun, org, repo string) (prNum int, prTitle string, prURL string) {
	if len(run.PullRequests) > 0 {
		prNum = run.PullRequests[0].Number
		prTitle = run.PullRequests[0].Title
		prURL = run.PullRequests[0].URL
		if prURL == "" && prNum != 0 {
			prURL = fmt.Sprintf("https://github.com/%s/%s/pull/%d", org, repo, prNum)
		}
	}
	if prTitle == "" && run.DisplayTitle != "" {
		prTitle = run.DisplayTitle
	}
	return
}

// buildJobItemFromJob creates a JobItem from a GHJobInfo (standard jobs path).
func buildJobItemFromJob(j GHJobInfo, run GHWorkflowRun, repo string) *JobItem {
	js := jobStatusFromGH(j.Status, j.Conclusion)

	displayName := fmt.Sprintf("%s / %s", repo, j.Name)

	duration, startedAt, secs := parseDuration(j.StartedAt)
	queuedAgo := formatQueuedAgo(run.CreatedAt)

	jobItem := &JobItem{
		ID:         fmt.Sprintf("#%d", j.ID),
		Name:       displayName,
		Repo:       repo,
		Branch:     run.HeadBranch,
		Event:      run.Event,
		Status:     js,
		RunnerName: j.RunnerName,
		QueuedAt:   queuedAgo,
		Duration:   duration,
		Seconds:    secs,
		StartedAt:  startedAt,
		RunID:      run.ID,
		GHJobID:    j.ID,
	}
	if j.RunnerID != 0 {
		jobItem.RunnerID = fmt.Sprintf("runner-%d", j.RunnerID)
	}
	return jobItem
}

// buildJobItemFromRun creates a JobItem from a workflow run (fallback when no jobs available).
func buildJobItemFromRun(run GHWorkflowRun, repo string) *JobItem {
	js := JobQueued
	if run.Status == "in_progress" {
		js = JobRunning
	}

	name := run.DisplayTitle
	if name == "" {
		name = run.Name
	}
	displayName := fmt.Sprintf("%s / %s", repo, name)

	duration, startedAt, secs := parseDuration(run.RunStartedAt)
	queuedAgo := formatQueuedAgo(run.CreatedAt)

	job := &JobItem{
		ID:         fmt.Sprintf("#%d", run.ID),
		Name:       displayName,
		Repo:       repo,
		Branch:     run.HeadBranch,
		Event:      run.Event,
		Status:     js,
		RunnerName: run.RunnerName,
		QueuedAt:   queuedAgo,
		Duration:   duration,
		Seconds:    secs,
		StartedAt:  startedAt,
		RunID:      run.ID,
	}
	if run.RunnerID != 0 {
		job.RunnerID = fmt.Sprintf("runner-%d", run.RunnerID)
	}
	return job
}

// parseDuration parses an RFC 3339 timestamp and returns a formatted duration string,
// the parsed time.Time for further use, and elapsed seconds.
func parseDuration(timestamp string) (string, time.Time, int) {
	if timestamp == "" {
		return "-", time.Time{}, 0
	}
	t, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return "-", time.Time{}, 0
	}
	secs := int(time.Since(t).Seconds())
	return formatDuration(secs), t, secs
}

// jobStatusFromGH maps GitHub API status/conclusion to our JobStatus.
func jobStatusFromGH(status, conclusion string) JobStatus {
	if status == "in_progress" {
		return JobRunning
	}
	if conclusion == "failure" {
		return JobFailed
	}
	if conclusion == "cancelled" {
		return JobCancelled
	}
	return JobQueued
}

// FetchOrgRunners queries GitHub API for all registered organization runners.
func FetchOrgRunners(org string) ([]*RunnerItem, error) {
	out, err := runGHCommand("api", fmt.Sprintf("/orgs/%s/actions/runners?per_page=100", org))
	if err != nil {
		return nil, err
	}

	var resp GHRunnersResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("failed to parse runners JSON: %w", err)
	}

	var parsed []*RunnerItem
	for _, r := range resp.Runners {
		st := RunnerIdle
		if r.Status == "offline" {
			st = RunnerOffline
		} else if r.Busy {
			st = RunnerRunning
		}

		var tags []string
		platform := r.OS
		for _, lbl := range r.Labels {
			tags = append(tags, lbl.Name)
			if lbl.Name == "ARM64" || lbl.Name == "X64" || lbl.Name == "x86_64" {
				platform = fmt.Sprintf("%s/%s", r.OS, lbl.Name)
			}
		}

		item := &RunnerItem{
			ID:            fmt.Sprintf("runner-%d", r.ID),
			Name:          r.Name,
			Platform:      platform,
			Status:        st,
			CurrentJobID:  "-",
			CurrentJob:    "-",
			Tags:          tags,
			LastHeartbeat: time.Now(),
		}
		parsed = append(parsed, item)
	}

	return parsed, nil
}

// MergeRunners preserves existing log history and cross-references active running jobs in queue.
func MergeRunners(newRunners []*RunnerItem, existing []*RunnerItem, jobQueue []*JobItem) []*RunnerItem {
	existingLogs := make(map[string][]string)
	existingSteps := make(map[string]int)
	for _, r := range existing {
		if len(r.OutputLogs) > 0 {
			existingLogs[r.Name] = r.OutputLogs
			existingSteps[r.Name] = r.StepCount
		}
	}

	// Map active running jobs to runners by runner name
	activeJobMap := make(map[string]*JobItem)
	for _, j := range jobQueue {
		if j.Status == JobRunning && j.RunnerName != "" {
			activeJobMap[j.RunnerName] = j
		}
	}

	for _, r := range newRunners {
		if logs, found := existingLogs[r.Name]; found {
			r.OutputLogs = logs
			r.StepCount = existingSteps[r.Name]
		} else {
			r.OutputLogs = []string{
				fmt.Sprintf("[%s] Runner online: %s (%s)", time.Now().Format("15:04:05"), r.Name, r.Platform),
				fmt.Sprintf("[%s] Labels: %s", time.Now().Format("15:04:05"), strings.Join(r.Tags, ", ")),
			}
		}

		// Cross-reference job queue to set current job on runner
		if job, active := activeJobMap[r.Name]; active {
			r.Status = RunnerRunning
			r.CurrentJobID = job.ID
			r.CurrentJob = job.Name
		}
	}

	return newRunners
}

// FetchOrgJobQueue polls GitHub API for active workflow runs across the organization using org-level endpoints.
// This executes 2 API calls total (in_progress + queued) rather than N calls per repository, minimizing rate limits.
func FetchOrgJobQueue(org string, repos []string) ([]*JobItem, error) {
	var allJobs []*JobItem
	seenRunIDs := make(map[int64]bool)
	seenJobIDs := make(map[int64]bool)
	var fetchErrors []string

	statuses := []string{"in_progress", "queued"}
	for _, status := range statuses {
		out, err := runGHCommand(
			"api",
			fmt.Sprintf("/orgs/%s/actions/runs?status=%s&per_page=50", org, status),
		)
		if err != nil {
			slog.Error("gh api org workflow runs failed", "org", org, "status", status, "error", err)
			fetchErrors = append(fetchErrors, fmt.Sprintf("status=%s: %v", status, err))
			continue
		}

		var resp GHWorkflowRunsResponse
		if err := json.Unmarshal(out, &resp); err != nil {
			slog.Error("failed to parse org workflow runs JSON", "org", org, "status", status, "error", err)
			fetchErrors = append(fetchErrors, fmt.Sprintf("status=%s parse error", status))
			continue
		}

		for _, run := range resp.WorkflowRuns {
			if seenRunIDs[run.ID] {
				continue
			}

			repoName := run.Repository.Name
			if repoName == "" {
				repoName = "unknown"
			}

			// Filter to tracked repositories if repos filter is provided
			if len(repos) > 0 {
				matched := false
				for _, r := range repos {
					if strings.EqualFold(r, repoName) {
						matched = true
						break
					}
				}
				if !matched {
					continue
				}
			}

			seenRunIDs[run.ID] = true

			jobsOut, err := runGHCommand(
				"api",
				fmt.Sprintf("/repos/%s/%s/actions/runs/%d/jobs?filter=latest&per_page=50", org, repoName, run.ID),
			)

			var parsedJobsFromRun int
			if err == nil {
				var jobsResp GHJobsResponse
				if err := json.Unmarshal(jobsOut, &jobsResp); err != nil {
					slog.Error("failed to parse jobs JSON", "runID", run.ID, "repo", repoName, "error", err)
					fetchErrors = append(fetchErrors, fmt.Sprintf("jobs parse error for run %d", run.ID))
				} else if len(jobsResp.Jobs) > 0 {
					for _, j := range jobsResp.Jobs {
						if seenJobIDs[j.ID] {
							continue
						}
						seenJobIDs[j.ID] = true

						// Filter out completed success/skipped jobs
						if j.Status == "completed" && (j.Conclusion == "success" || j.Conclusion == "skipped") {
							continue
						}

						jobItem := buildJobItemFromJob(j, run, repoName)
						prNum, prTitle, prURL := extractPRInfo(run, org, repoName)
						jobItem.PRNumber = prNum
						jobItem.PRTitle = prTitle
						jobItem.PRURL = prURL

						allJobs = append(allJobs, jobItem)
						parsedJobsFromRun++
					}
				}
			} else {
				slog.Debug("gh jobs endpoint failed for run, using fallback", "runID", run.ID, "repo", repoName, "error", err)
			}

			// Fallback if jobs endpoint returned no jobs
			if parsedJobsFromRun == 0 {
				jobItem := buildJobItemFromRun(run, repoName)
				prNum, prTitle, prURL := extractPRInfo(run, org, repoName)
				jobItem.PRNumber = prNum
				jobItem.PRTitle = prTitle
				jobItem.PRURL = prURL

				allJobs = append(allJobs, jobItem)
			}
		}
	}

	if len(fetchErrors) > 0 {
		rateLimitCount := 0
		for _, e := range fetchErrors {
			if strings.Contains(e, "rate limit") {
				rateLimitCount++
			}
		}
		if rateLimitCount > 0 {
			return FilterAndSortJobQueue(allJobs), fmt.Errorf("GitHub API rate limit exceeded")
		}
		return FilterAndSortJobQueue(allJobs), fmt.Errorf("failed fetching org job queue (%s)", fetchErrors[0])
	}
	return FilterAndSortJobQueue(allJobs), nil
}

// FetchJobLogs fetches the step log output for a specific running workflow job.
func FetchJobLogs(org, repo string, runID, targetGHJobID int64, targetJobName string, maxLines int) ([]string, int64, error) {
	// Step 1: get jobs list for this run to find the target job ID
	out, err := runGHCommand(
		"api",
		fmt.Sprintf("/repos/%s/%s/actions/runs/%d/jobs?filter=latest&per_page=50", org, repo, runID),
	)
	if err != nil {
		return nil, 0, fmt.Errorf("jobs list: %w", err)
	}

	var jobsResp GHJobsResponse
	if err := json.Unmarshal(out, &jobsResp); err != nil {
		return nil, 0, fmt.Errorf("jobs parse: %w", err)
	}

	var jobID int64
	var steps []GHJobStep

	// 1. Try matching targetGHJobID directly
	if targetGHJobID != 0 {
		for _, j := range jobsResp.Jobs {
			if j.ID == targetGHJobID {
				jobID = j.ID
				steps = j.Steps
				break
			}
		}
	}

	// 2. Try matching targetJobName -- exact match first, then substring fallback
	if jobID == 0 && targetJobName != "" {
		for _, j := range jobsResp.Jobs {
			if j.Name == targetJobName {
				jobID = j.ID
				steps = j.Steps
				break
			}
		}
		// substring fallback only if exact match not found
		if jobID == 0 {
			for _, j := range jobsResp.Jobs {
				if strings.Contains(targetJobName, j.Name) || strings.Contains(j.Name, targetJobName) {
					jobID = j.ID
					steps = j.Steps
					break
				}
			}
		}
	}

	// 3. Fallback to first in_progress job
	if jobID == 0 {
		for _, j := range jobsResp.Jobs {
			if j.Status == "in_progress" {
				jobID = j.ID
				steps = j.Steps
				break
			}
		}
	}

	// 4. Fallback to first job in response
	if jobID == 0 && len(jobsResp.Jobs) > 0 {
		jobID = jobsResp.Jobs[0].ID
		steps = jobsResp.Jobs[0].Steps
	}

	if jobID == 0 {
		return nil, 0, fmt.Errorf("no job found for run %d", runID)
	}

	// Step 2: fetch raw log text
	logOut, err := runGHCommand(
		"api",
		fmt.Sprintf("/repos/%s/%s/actions/jobs/%d/logs", org, repo, jobID),
	)
	if err != nil {
		// Fall back to step names if logs unavailable
		var lines []string
		for _, s := range steps {
			glyph := "○"
			if s.Status == "in_progress" {
				glyph = "▶"
			} else if s.Conclusion == "success" {
				glyph = "✓"
			} else if s.Conclusion == "failure" {
				glyph = "✗"
			} else if s.Conclusion == "skipped" {
				glyph = "⊘"
			}
			lines = append(lines, fmt.Sprintf("  %s  Step %d: %s", glyph, s.Number, s.Name))
		}
		return lines, jobID, nil
	}

	// Parse raw log -- take last maxLines non-empty lines
	raw := string(logOut)
	var lines []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		if line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return lines, jobID, nil
}

// jobSortKey returns a stable sort key: running first, then by numeric ID.
func jobSortKey(j *JobItem) (isRunning int, numericID int, stringID string) {
	if j.Status == JobRunning {
		isRunning = 0
	} else {
		isRunning = 1
	}
	return isRunning, parseNumericID(j.ID), j.ID
}

// FilterAndSortJobQueue filters out passed/cancelled jobs and sorts running jobs first, then by numeric ID.
func FilterAndSortJobQueue(queue []*JobItem) []*JobItem {
	var filtered []*JobItem
	for _, j := range queue {
		if j.Status != JobPassed && j.Status != JobCancelled {
			filtered = append(filtered, j)
		}
	}

	sort.SliceStable(filtered, func(i, j int) bool {
		irI, nidI, sidI := jobSortKey(filtered[i])
		irJ, nidJ, sidJ := jobSortKey(filtered[j])
		if irI != irJ {
			return irI < irJ
		}
		if nidI != 0 && nidJ != 0 && nidI != nidJ {
			return nidI < nidJ
		}
		return sidI < sidJ
	})

	return filtered
}

// PollStep refreshes running job durations from their real elapsed start times.
// Uses the same sort comparator as FilterAndSortJobQueue for consistency.
func PollStep(runners []*RunnerItem, jobQueue []*JobItem) {
	for _, j := range jobQueue {
		if j.Status == JobRunning && !j.StartedAt.IsZero() {
			j.Seconds = int(time.Since(j.StartedAt).Seconds())
			j.Duration = formatDuration(j.Seconds)
		}
	}

	for _, r := range runners {
		if r.Status != RunnerOffline {
			r.LastHeartbeat = time.Now()
		}
	}

	sort.SliceStable(jobQueue, func(i, j int) bool {
		irI, nidI, sidI := jobSortKey(jobQueue[i])
		irJ, nidJ, sidJ := jobSortKey(jobQueue[j])
		if irI != irJ {
			return irI < irJ
		}
		if nidI != 0 && nidJ != 0 && nidI != nidJ {
			return nidI < nidJ
		}
		return sidI < sidJ
	})
}

// DefaultRunners returns an empty slice -- used only in tests without GitHub access.
func DefaultRunners() []*RunnerItem {
	return []*RunnerItem{}
}

// DefaultJobQueue returns an empty slice -- used only in tests without GitHub access.
func DefaultJobQueue() []*JobItem {
	return []*JobItem{}
}
