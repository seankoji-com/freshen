package jobs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
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
	ID         string
	Name       string
	Repo       string
	Branch     string
	Event      string
	PRNumber   int
	PRTitle    string
	PRURL      string
	Status     JobStatus
	RunnerID   string
	RunnerName string
	QueuedAt   string
	Duration   string
	Logs       []string
	Seconds    int
	StartedAt  time.Time
	RunID      int64 // GitHub workflow run ID
	GHJobID    int64 // GitHub job ID within the run (populated lazily)
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
	Name        string `json:"name"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	Number      int    `json:"number"`
}

type GHJobInfo struct {
	ID          int64       `json:"id"`
	Name        string      `json:"name"`
	Status      string      `json:"status"`
	Conclusion  string      `json:"conclusion"`
	StartedAt   string      `json:"started_at"`
	RunnerName  string      `json:"runner_name"`
	RunnerID    int64       `json:"runner_id"`
	Steps       []GHJobStep `json:"steps"`
}

type GHJobsResponse struct {
	TotalCount int         `json:"total_count"`
	Jobs       []GHJobInfo `json:"jobs"`
}

// FetchOrgRunners queries GitHub API for all registered organization runners.
func FetchOrgRunners(org string, existingRunners []*RunnerItem, jobQueue []*JobItem) ([]*RunnerItem, error) {
	cmd := exec.Command("gh", "api", fmt.Sprintf("/orgs/%s/actions/runners?per_page=100", org))
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		// Return existing runners unchanged on error (not mock data)
		return existingRunners, err
	}

	var resp GHRunnersResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		return existingRunners, err
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

	return mergeRunners(parsed, existingRunners, jobQueue), nil
}

// mergeRunners preserves existing log history and cross-references active running jobs in queue.
func mergeRunners(newRunners []*RunnerItem, existing []*RunnerItem, jobQueue []*JobItem) []*RunnerItem {
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
				fmt.Sprintf("[%s] 󰄬 Runner online: %s (%s)", time.Now().Format("15:04:05"), r.Name, r.Platform),
				fmt.Sprintf("[%s] 󰓦 Labels: %s", time.Now().Format("15:04:05"), strings.Join(r.Tags, ", ")),
			}
		}

		// Cross-reference job queue to set current job on runner
		if job, active := activeJobMap[r.Name]; active {
			r.Status = RunnerRunning
			r.CurrentJobID = job.ID
			r.CurrentJob = job.Name
		}
	}

	// Always list runners in strict alphabetical order by name
	sort.Slice(newRunners, func(i, j int) bool {
		return strings.ToLower(newRunners[i].Name) < strings.ToLower(newRunners[j].Name)
	})

	return newRunners
}

// FetchOrgJobQueue polls GitHub API for all in_progress and queued workflow runs and their individual jobs.
func FetchOrgJobQueue(org string, repos []string) ([]*JobItem, error) {
	var allJobs []*JobItem
	seenRunIDs := make(map[int64]bool)
	seenJobIDs := make(map[int64]bool)

	for _, repo := range repos {
		// Single query for latest workflow runs per repo (includes both in_progress and queued)
		args := []string{
			"api",
			fmt.Sprintf("/repos/%s/%s/actions/runs?per_page=25", org, repo),
		}
		cmd := exec.Command("gh", args...)
		var out bytes.Buffer
		cmd.Stdout = &out
		if err := cmd.Run(); err != nil {
			continue
		}

		var resp GHWorkflowRunsResponse
		if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
			continue
		}

		for _, run := range resp.WorkflowRuns {
			if seenRunIDs[run.ID] {
				continue
			}
			// Only inspect active runs (in_progress or queued)
			if run.Status != "in_progress" && run.Status != "queued" && run.Status != "waiting" && run.Status != "requested" {
				continue
			}
			seenRunIDs[run.ID] = true

				// Query individual jobs for this workflow run
				jobsArgs := []string{
					"api",
					fmt.Sprintf("/repos/%s/%s/actions/runs/%d/jobs?filter=latest&per_page=50", org, repo, run.ID),
				}
				jobsCmd := exec.Command("gh", jobsArgs...)
				var jobsOut bytes.Buffer
				jobsCmd.Stdout = &jobsOut

				var parsedJobsFromRun int
				if err := jobsCmd.Run(); err == nil {
					var jobsResp GHJobsResponse
					if err := json.Unmarshal(jobsOut.Bytes(), &jobsResp); err == nil && len(jobsResp.Jobs) > 0 {
						for _, j := range jobsResp.Jobs {
							if seenJobIDs[j.ID] {
								continue
							}
							seenJobIDs[j.ID] = true

							// Filter out completed success/skipped jobs
							if j.Status == "completed" && (j.Conclusion == "success" || j.Conclusion == "skipped") {
								continue
							}

							js := JobQueued
							if j.Status == "in_progress" {
								js = JobRunning
							} else if j.Conclusion == "failure" {
								js = JobFailed
							}

							displayName := fmt.Sprintf("%s / %s", repo, j.Name)

							duration := "-"
							var startedAt time.Time
							var secs int
							if j.StartedAt != "" {
								if t, err := time.Parse(time.RFC3339, j.StartedAt); err == nil {
									startedAt = t
									secs = int(time.Since(t).Seconds())
									if secs > 0 {
										if secs >= 60 {
											duration = fmt.Sprintf("%dm %ds", secs/60, secs%60)
										} else {
											duration = fmt.Sprintf("%ds", secs)
										}
									}
								}
							}

							queuedAgo := "-"
							if run.CreatedAt != "" {
								if t, err := time.Parse(time.RFC3339, run.CreatedAt); err == nil {
									ago := int(time.Since(t).Seconds())
									if ago >= 60 {
										queuedAgo = fmt.Sprintf("%dm ago", ago/60)
									} else {
										queuedAgo = fmt.Sprintf("%ds ago", ago)
									}
								}
							}

							prNum := 0
							prTitle := ""
							prURL := ""
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

							jobItem := &JobItem{
								ID:         fmt.Sprintf("#%d", j.ID),
								Name:       displayName,
								Repo:       repo,
								Branch:     run.HeadBranch,
								Event:      run.Event,
								PRNumber:   prNum,
								PRTitle:    prTitle,
								PRURL:      prURL,
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

							allJobs = append(allJobs, jobItem)
							parsedJobsFromRun++
						}
					}
				}

				// Fallback if jobs endpoint returned no jobs
				if parsedJobsFromRun == 0 {
					js := JobQueued
					if run.Status == "in_progress" {
						js = JobRunning
					}

					name := run.DisplayTitle
					if name == "" {
						name = run.Name
					}
					displayName := fmt.Sprintf("%s / %s", repo, name)

					duration := "-"
					var startedAt time.Time
					var secs int
					if run.RunStartedAt != "" {
						if t, err := time.Parse(time.RFC3339, run.RunStartedAt); err == nil {
							startedAt = t
							secs = int(time.Since(t).Seconds())
							if secs > 0 {
								if secs >= 60 {
									duration = fmt.Sprintf("%dm %ds", secs/60, secs%60)
								} else {
									duration = fmt.Sprintf("%ds", secs)
								}
							}
						}
					}

					queuedAgo := "-"
					if run.CreatedAt != "" {
						if t, err := time.Parse(time.RFC3339, run.CreatedAt); err == nil {
							ago := int(time.Since(t).Seconds())
							if ago >= 60 {
								queuedAgo = fmt.Sprintf("%dm ago", ago/60)
							} else {
								queuedAgo = fmt.Sprintf("%ds ago", ago)
							}
						}
					}

					prNum := 0
					prTitle := ""
					prURL := ""
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

					job := &JobItem{
						ID:         fmt.Sprintf("#%d", run.ID),
						Name:       displayName,
						Repo:       repo,
						Branch:     run.HeadBranch,
						Event:      run.Event,
						PRNumber:   prNum,
						PRTitle:    prTitle,
						PRURL:      prURL,
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

					allJobs = append(allJobs, job)
				}
			}
		}

	return FilterAndSortJobQueue(allJobs), nil
}

// FetchJobLogs fetches the step log output for a specific running workflow job.
// It matches the specific targetGHJobID or targetJobName within the run, then fetches raw log text.
func FetchJobLogs(org, repo string, runID, targetGHJobID int64, targetJobName string, maxLines int) ([]string, int64, error) {
	// Step 1: get jobs list for this run to find the target job ID
	cmd := exec.Command("gh", "api",
		fmt.Sprintf("/repos/%s/%s/actions/runs/%d/jobs?filter=latest&per_page=50", org, repo, runID),
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, 0, fmt.Errorf("jobs list: %w", err)
	}

	var jobsResp GHJobsResponse
	if err := json.Unmarshal(out.Bytes(), &jobsResp); err != nil {
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

	// 2. Try matching targetJobName
	if jobID == 0 && targetJobName != "" {
		for _, j := range jobsResp.Jobs {
			if j.Name == targetJobName || strings.Contains(targetJobName, j.Name) || strings.Contains(j.Name, targetJobName) {
				jobID = j.ID
				steps = j.Steps
				break
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
	cmd2 := exec.Command("gh", "api",
		fmt.Sprintf("/repos/%s/%s/actions/jobs/%d/logs", org, repo, jobID),
	)
	var logOut bytes.Buffer
	cmd2.Stdout = &logOut
	if err := cmd2.Run(); err != nil {
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

	// Parse raw log — take last maxLines non-empty lines
	raw := logOut.String()
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

// FilterAndSortJobQueue filters out passed/completed jobs and sorts running jobs first, then queued.
func FilterAndSortJobQueue(queue []*JobItem) []*JobItem {
	var filtered []*JobItem
	for _, j := range queue {
		if j.Status != JobPassed && j.Status != "COMPLETED" && j.Status != "SUCCESS" {
			filtered = append(filtered, j)
		}
	}

	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].Status == JobRunning && filtered[j].Status != JobRunning {
			return true
		}
		if filtered[i].Status != JobRunning && filtered[j].Status == JobRunning {
			return false
		}
		return filtered[i].ID < filtered[j].ID
	})

	return filtered
}

// PollStep refreshes running job durations from their actual start times.
// No fake simulation — real durations only.
func PollStep(runners []*RunnerItem, jobQueue []*JobItem) {
	// Update running jobs' duration from real elapsed time
	for _, j := range jobQueue {
		if j.Status == JobRunning && !j.StartedAt.IsZero() {
			j.Seconds = int(time.Since(j.StartedAt).Seconds())
			if j.Seconds >= 60 {
				j.Duration = fmt.Sprintf("%dm %ds", j.Seconds/60, j.Seconds%60)
			} else {
				j.Duration = fmt.Sprintf("%ds", j.Seconds)
			}
		}
	}

	// Update runner heartbeats
	for _, r := range runners {
		if r.Status != RunnerOffline {
			r.LastHeartbeat = time.Now()
		}
	}

	// Re-sort so RUNNING stays at top
	sort.SliceStable(jobQueue, func(i, j int) bool {
		if jobQueue[i].Status == JobRunning && jobQueue[j].Status != JobRunning {
			return true
		}
		if jobQueue[i].Status != JobRunning && jobQueue[j].Status == JobRunning {
			return false
		}
		return jobQueue[i].ID < jobQueue[j].ID
	})
}

// DefaultRunners returns an empty slice — used only in tests without GitHub access.
func DefaultRunners() []*RunnerItem {
	return []*RunnerItem{}
}

// DefaultJobQueue returns an empty slice — used only in tests without GitHub access.
func DefaultJobQueue() []*JobItem {
	return []*JobItem{}
}
