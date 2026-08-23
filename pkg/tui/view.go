package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/seankoji-com/freshen/pkg/git"
	"github.com/seankoji-com/freshen/pkg/jobs"
)

// Hyperlink formats text as an OSC 8 terminal hyperlink.
func Hyperlink(text, url string) string {
	if url == "" {
		return text
	}
	return fmt.Sprintf("\x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\", url, text)
}

func (m *Model) updateViewport() {
	var sb strings.Builder

	switch m.ActiveFocus {

	case FocusRunners:
		if len(m.Runners) == 0 {
			m.Viewport.SetContent(lipgloss.NewStyle().Foreground(colorMuted).Render(" No registered runners found for org."))
			return
		}

		tags := m.getAvailableTags()
		if m.SelectedTagIndex >= len(tags) {
			m.SelectedTagIndex = 0
		}
		activeTag := tags[m.SelectedTagIndex]

		// Filter runners by activeTag
		var matchingRunners []*jobs.RunnerItem
		for _, r := range m.Runners {
			if activeTag == "ALL" {
				matchingRunners = append(matchingRunners, r)
			} else {
				for _, tag := range r.Tags {
					if tag == activeTag {
						matchingRunners = append(matchingRunners, r)
						break
					}
				}
			}
		}

		busyCount := 0
		for _, r := range matchingRunners {
			if r.Status == jobs.RunnerRunning {
				busyCount++
			}
		}
		loadPct := 0
		if len(matchingRunners) > 0 {
			loadPct = (busyCount * 100) / len(matchingRunners)
		}

		// --- TAG TABS BAR (Right Column Header) ---
		var tagPills []string
		for i, tag := range tags {
			if i == m.SelectedTagIndex {
				tagPills = append(tagPills, lipgloss.NewStyle().Foreground(colorSecondary).Bold(true).Render("["+tag+"]"))
			} else {
				tagPills = append(tagPills, lipgloss.NewStyle().Foreground(colorMuted).Render(tag))
			}
		}
		sb.WriteString(" " + iconRunner + "  TAGS:  " + strings.Join(tagPills, "  ") + "\n")
		sb.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(
			fmt.Sprintf(" %s", strings.Repeat("─", m.Viewport.Width-2)),
		) + "\n\n")

		// --- DETAILS TABLE ---
		label := func(k, v string, vc lipgloss.Color) string {
			return fmt.Sprintf(" %s  %s\n",
				lipgloss.NewStyle().Foreground(colorMuted).Width(14).Render(k),
				lipgloss.NewStyle().Foreground(vc).Render(v),
			)
		}

		sb.WriteString(label("Active Tag", activeTag, colorSecondary))
		sb.WriteString(label("Runner Count", fmt.Sprintf("%d matching runners", len(matchingRunners)), colorPrimary))
		sb.WriteString(label("Cluster Load", fmt.Sprintf("%d%% load (%d busy / %d total)", loadPct, busyCount, len(matchingRunners)), colorYellow))
		if len(tags) > 1 {
			sb.WriteString(label("Tag Navigation", "[← / →] or [h / l] to cycle through fleet tags", colorMuted))
		}

		// --- MATCHING RUNNERS TABLE ---
		sb.WriteString("\n")
		sb.WriteString(lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(fmt.Sprintf(" RUNNERS MATCHING [%s]", activeTag)) + "\n")
		sb.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(
			fmt.Sprintf(" %s", strings.Repeat("─", m.Viewport.Width-2)),
		) + "\n")

		maxJobLen := m.Viewport.Width - 29
		if maxJobLen < 15 {
			maxJobLen = 15
		}

		for idx, r := range matchingRunners {
			var rColor lipgloss.Color
			var rGlyph string
			switch r.Status {
			case jobs.RunnerRunning:
				rColor, rGlyph = colorSecondary, "⚡"
			case jobs.RunnerOffline:
				rColor, rGlyph = colorRed, "✖"
			case jobs.RunnerMaintenance:
				rColor, rGlyph = colorYellow, "⚠"
			default:
				rColor, rGlyph = colorGreen, "●"
			}
			glyphCell := lipgloss.NewStyle().Foreground(rColor).Width(2).Render(rGlyph)
			var currentJobStr string
			switch {
			case r.Status == jobs.RunnerRunning:
				matchedJ := findJobForRunner(r, m.JobQueue)
				if matchedJ != nil {
					dispName := matchedJ.Name
					repoPrefix := matchedJ.Repo + " / "
					if strings.HasPrefix(dispName, repoPrefix) {
						dispName = strings.TrimPrefix(dispName, repoPrefix)
					}
					currentJobStr = lipgloss.NewStyle().Foreground(colorYellow).Render(truncateString(dispName, maxJobLen))
				} else if r.CurrentJob != "-" && r.CurrentJob != "" {
					currentJobStr = lipgloss.NewStyle().Foreground(colorYellow).Render(truncateString(r.CurrentJob, maxJobLen))
				} else {
					currentJobStr = lipgloss.NewStyle().Foreground(colorYellow).Render(truncateString("active job in queue", maxJobLen))
				}
			case r.CurrentJob != "-" && r.CurrentJob != "":
				currentJobStr = lipgloss.NewStyle().Foreground(colorYellow).Render(truncateString(r.CurrentJob, maxJobLen))
			default:
				currentJobStr = lipgloss.NewStyle().Foreground(colorMuted).Render("idle")
			}

			isSelected := m.ActiveFocus == FocusRunners && idx == m.SelectedRunnerIndex
			rowContent := fmt.Sprintf("%s %s  %s",
				glyphCell,
				lipgloss.NewStyle().Foreground(colorSecondary).Width(20).Render(r.Name),
				currentJobStr,
			)
			if isSelected {
				sb.WriteString(selectedRowStyle.Render("> "+rowContent) + "\n")
			} else {
				sb.WriteString(normalRowStyle.Render("  "+rowContent) + "\n")
			}
		}

		// --- QUEUED / RUNNING JOBS ON MATCHING RUNNERS ---
		var assignedJobs []*jobs.JobItem
		matchingNames := make(map[string]bool)
		for _, r := range matchingRunners {
			matchingNames[r.Name] = true
			matchingNames[r.ID] = true
		}
		for _, j := range m.JobQueue {
			if matchingNames[j.RunnerName] || matchingNames[j.RunnerID] {
				assignedJobs = append(assignedJobs, j)
			}
		}
		if len(assignedJobs) > 0 {
			sb.WriteString("\n")
			sb.WriteString(lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(fmt.Sprintf(" QUEUED / RUNNING JOBS ON [%s] RUNNERS", activeTag)) + "\n")
			sb.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(
				fmt.Sprintf(" %s", strings.Repeat("─", m.Viewport.Width-2)),
			) + "\n")

			nameMaxLen := m.Viewport.Width - 32
			if nameMaxLen < 12 {
				nameMaxLen = 12
			}

			for _, j := range assignedJobs {
				var jColor lipgloss.Color
				var jGlyph string
				if j.Status == jobs.JobRunning {
					jColor, jGlyph = colorSecondary, "⚡"
				} else {
					jColor, jGlyph = colorYellow, "⏳"
				}
				jLink := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(j.ID)
				if j.RunID != 0 {
					jobURL := fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d", m.TargetOrg, j.Repo, j.RunID)
					if j.GHJobID != 0 {
						jobURL = fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d/job/%d", m.TargetOrg, j.Repo, j.RunID, j.GHJobID)
					}
					jLink = Hyperlink(jLink, jobURL)
				}
				jNameTrunc := truncateString(j.Name, nameMaxLen)
				sb.WriteString(fmt.Sprintf(" %s  %s  %s  %s\n",
					lipgloss.NewStyle().Foreground(jColor).Render(jGlyph+" "+string(j.Status)),
					jLink,
					lipgloss.NewStyle().Foreground(colorSecondary).Render(jNameTrunc),
					lipgloss.NewStyle().Foreground(colorMuted).Render(j.Duration),
				))
			}
		}

	case FocusJobs:
		if len(m.JobQueue) == 0 || m.SelectedJobIndex >= len(m.JobQueue) {
			m.Viewport.SetContent("No jobs in queue.")
			return
		}
		job := m.JobQueue[m.SelectedJobIndex]

		// If a run is focused, show summary of all jobs in that run
		if m.FocusedRunID != 0 {
			// Find all jobs belonging to the focused run
			var runJobs []*jobs.JobItem
			var runHeaderJob *jobs.JobItem
			for _, j := range m.JobQueue {
				if j.RunID == m.FocusedRunID {
					if j.IsRunHeader {
						runHeaderJob = j
					} else {
						runJobs = append(runJobs, j)
					}
				}
			}

			// Use run header for metadata if available, otherwise use first job
			metaJob := job
			if runHeaderJob != nil {
				metaJob = runHeaderJob
			} else if len(runJobs) > 0 {
				metaJob = runJobs[0]
			}

			// Header with run info
			runToken, _ := parseJobHierarchy(metaJob.Name, metaJob.Repo)
			runURL := fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d", m.TargetOrg, metaJob.Repo, metaJob.RunID)
			runLink := Hyperlink(lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(runToken), runURL)

			sb.WriteString(fmt.Sprintf(" %s FOCUSED RUN: %s\n\n", iconQueue, runLink))

			// Trigger info
			triggerStr := "push"
			if metaJob.Event != "" {
				triggerStr = metaJob.Event
			}
			if metaJob.PRNumber != 0 {
				prLabel := fmt.Sprintf("PR #%d", metaJob.PRNumber)
				prURL := metaJob.PRURL
				if prURL == "" {
					prURL = fmt.Sprintf("https://github.com/%s/%s/pull/%d", m.TargetOrg, metaJob.Repo, metaJob.PRNumber)
				}
				triggerStr = fmt.Sprintf("%s (%s)", Hyperlink(prLabel, prURL), metaJob.Event)
			} else if metaJob.Branch != "" {
				branchURL := fmt.Sprintf("https://github.com/%s/%s/tree/%s", m.TargetOrg, metaJob.Repo, metaJob.Branch)
				triggerStr = fmt.Sprintf("%s on %s", triggerStr, Hyperlink(metaJob.Branch, branchURL))
			}

			sb.WriteString(fmt.Sprintf(" %s  %s\n",
				lipgloss.NewStyle().Bold(true).Foreground(colorMuted).Width(10).Render("Repo:"),
				lipgloss.NewStyle().Foreground(colorPrimary).Render(metaJob.Repo),
			))
			sb.WriteString(fmt.Sprintf(" %s  %s\n",
				lipgloss.NewStyle().Bold(true).Foreground(colorMuted).Width(10).Render("Trigger:"),
				lipgloss.NewStyle().Foreground(colorYellow).Bold(true).Render(triggerStr),
			))
			sb.WriteString(fmt.Sprintf(" %s  %d jobs\n\n",
				lipgloss.NewStyle().Bold(true).Foreground(colorMuted).Width(10).Render("Total:"),
				len(runJobs),
			))

			// Summary table of all jobs in this run
			sb.WriteString(lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(" RUN SUMMARY") + "\n")
			sb.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render(
				fmt.Sprintf(" %s", strings.Repeat("─", m.Viewport.Width-2)),
			) + "\n")

			if len(runJobs) == 0 {
				sb.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render("  No jobs found for this run.") + "\n")
			} else {
				// Calculate status counts
				statusCounts := make(map[jobs.JobStatus]int)
				for _, j := range runJobs {
					statusCounts[j.Status]++
				}

				// Status summary line
				var statusParts []string
				if statusCounts[jobs.JobRunning] > 0 {
					statusParts = append(statusParts, lipgloss.NewStyle().Foreground(colorSecondary).Bold(true).Render(fmt.Sprintf("%d running", statusCounts[jobs.JobRunning])))
				}
				if statusCounts[jobs.JobQueued] > 0 {
					statusParts = append(statusParts, lipgloss.NewStyle().Foreground(colorYellow).Bold(true).Render(fmt.Sprintf("%d queued", statusCounts[jobs.JobQueued])))
				}
				if statusCounts[jobs.JobPassed] > 0 {
					statusParts = append(statusParts, lipgloss.NewStyle().Foreground(colorGreen).Bold(true).Render(fmt.Sprintf("%d passed", statusCounts[jobs.JobPassed])))
				}
				if statusCounts[jobs.JobFailed] > 0 {
					statusParts = append(statusParts, lipgloss.NewStyle().Foreground(colorRed).Bold(true).Render(fmt.Sprintf("%d failed", statusCounts[jobs.JobFailed])))
				}
				if statusCounts[jobs.JobCancelled] > 0 {
					statusParts = append(statusParts, lipgloss.NewStyle().Foreground(colorMuted).Bold(true).Render(fmt.Sprintf("%d cancelled", statusCounts[jobs.JobCancelled])))
				}
				sb.WriteString(" " + strings.Join(statusParts, "  |  ") + "\n\n")

				// Individual job rows
				nameMaxLen := m.Viewport.Width - 40
				if nameMaxLen < 15 {
					nameMaxLen = 15
				}

				for idx, j := range runJobs {
					var jColor lipgloss.Color
					var jGlyph string
					switch j.Status {
					case jobs.JobRunning:
						jColor, jGlyph = colorSecondary, "⚡"
					case jobs.JobPassed:
						jColor, jGlyph = colorGreen, "󰄬"
					case jobs.JobFailed:
						jColor, jGlyph = colorRed, "󰅙"
					case jobs.JobCancelled:
						jColor, jGlyph = colorMuted, "⊘"
					default:
						jColor, jGlyph = colorYellow, "⏳"
					}

					jLink := Hyperlink(lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(j.ID), fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d", m.TargetOrg, j.Repo, j.RunID))
					_, jobToken := parseJobHierarchy(j.Name, j.Repo)
					jNameTrunc := truncateString(jobToken, nameMaxLen)

					runnerStr := j.RunnerName
					if runnerStr == "" {
						runnerStr = "awaiting"
					}
					runnerStr = truncateString(runnerStr, 20)

					treeConnector := "├─"
					if idx == len(runJobs)-1 {
						treeConnector = "└─"
					}

					sb.WriteString(fmt.Sprintf("  %s %s  %s  %s  %s%s  %s\n",
						treeConnector,
						lipgloss.NewStyle().Foreground(jColor).Render(jGlyph+" "+string(j.Status)),
						jLink,
						lipgloss.NewStyle().Foreground(colorSecondary).Render(jNameTrunc),
						lipgloss.NewStyle().Foreground(colorMuted).Render(iconRunner+" "),
						lipgloss.NewStyle().Foreground(colorMuted).Render(runnerStr),
						lipgloss.NewStyle().Foreground(colorMuted).Render(j.Duration),
					))
				}
			}

			sb.WriteString("\n" + lipgloss.NewStyle().Foreground(colorMuted).Render(" Press Enter or Esc to unfocus") + "\n")
		} else {
			statusBadge := lipgloss.NewStyle().Foreground(colorYellow).Bold(true).Render("⏳ " + string(job.Status))
			if job.Status == jobs.JobRunning {
				statusBadge = lipgloss.NewStyle().Foreground(colorSecondary).Bold(true).Render("⚡ " + string(job.Status))
			} else if job.Status == jobs.JobPassed {
				statusBadge = lipgloss.NewStyle().Foreground(colorGreen).Bold(true).Render("󰄬 " + string(job.Status))
			} else if job.Status == jobs.JobFailed {
				statusBadge = lipgloss.NewStyle().Foreground(colorRed).Bold(true).Render("󰅙 " + string(job.Status))
			}

			jobURL := fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d", m.TargetOrg, job.Repo, job.RunID)
			if job.GHJobID != 0 {
				jobURL = fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d/job/%d", m.TargetOrg, job.Repo, job.RunID, job.GHJobID)
			}
			jobIDLink := Hyperlink(lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Render(job.ID), jobURL)

			runToken, jobToken := parseJobHierarchy(job.Name, job.Repo)
			runnerDisplay := job.RunnerName
			if runnerDisplay == "" {
				runnerDisplay = "Awaiting available runner node..."
			}

			triggerStr := "push"
			if job.Event != "" {
				triggerStr = job.Event
			}
			if job.PRNumber != 0 {
				prLabel := fmt.Sprintf("PR #%d", job.PRNumber)
				prURL := job.PRURL
				if prURL == "" {
					prURL = fmt.Sprintf("https://github.com/%s/%s/pull/%d", m.TargetOrg, job.Repo, job.PRNumber)
				}
				triggerStr = fmt.Sprintf("%s (%s)", Hyperlink(prLabel, prURL), job.Event)
			} else if job.Branch != "" {
				branchURL := fmt.Sprintf("https://github.com/%s/%s/tree/%s", m.TargetOrg, job.Repo, job.Branch)
				triggerStr = fmt.Sprintf("%s on %s", triggerStr, Hyperlink(job.Branch, branchURL))
			}

			sb.WriteString(fmt.Sprintf(" %s %s  |  %s  |  %s %s  |  Duration: %s\n\n",
				iconQueue,
				jobIDLink,
				statusBadge,
				iconFolder,
				lipgloss.NewStyle().Foreground(colorPrimary).Render(job.Repo),
				job.Duration,
			))

			sb.WriteString(fmt.Sprintf(" %s  %s\n",
				lipgloss.NewStyle().Bold(true).Foreground(colorMuted).Width(10).Render("Run:"),
				lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(runToken),
			))
			sb.WriteString(fmt.Sprintf(" %s  %s\n",
				lipgloss.NewStyle().Bold(true).Foreground(colorMuted).Width(10).Render("Trigger:"),
				lipgloss.NewStyle().Foreground(colorYellow).Bold(true).Render(triggerStr),
			))
			sb.WriteString(fmt.Sprintf(" %s  %s\n",
				lipgloss.NewStyle().Bold(true).Foreground(colorMuted).Width(10).Render("Job:"),
				lipgloss.NewStyle().Foreground(colorSecondary).Bold(true).Render(jobToken),
			))
			sb.WriteString(fmt.Sprintf(" %s  %s %s\n",
				lipgloss.NewStyle().Bold(true).Foreground(colorMuted).Width(10).Render("Runner:"),
				iconRunner,
				lipgloss.NewStyle().Foreground(colorSecondary).Bold(true).Render(runnerDisplay),
			))

			if job.Status == jobs.JobQueued {
				sb.WriteString("\n" + lipgloss.NewStyle().Foreground(colorYellow).Bold(true).Render("⏳ Job is currently queued. Awaiting available runner worker node...") + "\n")
			} else {
				dashCount := (m.Viewport.Width - 26) / 2
				if dashCount < 2 {
					dashCount = 2
				}
				divider := strings.Repeat("─", dashCount) + " ACTIVE RUNNER LOGS " + strings.Repeat("─", dashCount)
				sb.WriteString("\n" + lipgloss.NewStyle().Foreground(colorSecondary).Render(divider) + "\n\n")

				wrapWidth := m.Viewport.Width - 2
				if wrapWidth < 20 {
					wrapWidth = 40
				}
				logWrapper := lipgloss.NewStyle().Width(wrapWidth)

				if len(job.Logs) > 0 {
					for _, logLine := range job.Logs {
						styled := highlightLogLine(logLine)
						sb.WriteString(logWrapper.Render(styled) + "\n")
					}
				} else {
					var assignedRunner *jobs.RunnerItem
					for _, r := range m.Runners {
						if r.ID == job.RunnerID || r.Name == job.RunnerName {
							assignedRunner = r
							break
						}
					}

					if assignedRunner != nil && len(assignedRunner.OutputLogs) > 0 {
						for _, logLine := range assignedRunner.OutputLogs {
							styled := highlightLogLine(logLine)
							sb.WriteString(logWrapper.Render(styled) + "\n")
						}
					} else {
						sb.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render("  Fetching workflow execution logs...\n"))
					}
				}
			}
		}

	case FocusRepos:
		if len(m.Repos) == 0 || m.SelectedIndex >= len(m.Repos) {
			if m.IsOrgSyncing {
				m.Viewport.SetContent(fmt.Sprintf(" %s Fetching GitHub repositories...", m.Spinner.View()))
			} else {
				m.Viewport.SetContent(lipgloss.NewStyle().Foreground(colorMuted).Render(" No repositories found. Select a repo from the left pane."))
			}
			return
		}

		item := m.Repos[m.SelectedIndex]

		repoURL := item.URL
		if repoURL == "" {
			repoURL = fmt.Sprintf("https://github.com/%s/%s", m.TargetOrg, item.GHRepoName)
		}
		repoLink := Hyperlink(fmt.Sprintf("%s %s", iconGithub, subtitleStyle.Render(item.GHRepoName)), repoURL)

		branchDetail := fmt.Sprintf("%s (default)", item.CurrentBranch)
		if item.CurrentBranch != item.DefaultBranch {
			branchDetail = fmt.Sprintf("%s (default: %s)", item.CurrentBranch, item.DefaultBranch)
		}
		branchURL := fmt.Sprintf("%s/tree/%s", repoURL, item.CurrentBranch)
		branchLink := Hyperlink(fmt.Sprintf("%s %s", iconBranch, branchDetail), branchURL)

		prCountStr := "0 open"
		if item.OpenPRsCount > 0 {
			prCountStr = fmt.Sprintf("%d open", item.OpenPRsCount)
		}
		prURL := fmt.Sprintf("%s/pulls", repoURL)
		prLink := Hyperlink(fmt.Sprintf("%s %s", iconPR, prCountStr), prURL)

		issueCountStr := "0 open"
		if item.OpenIssuesCount > 0 {
			issueCountStr = fmt.Sprintf("%d open", item.OpenIssuesCount)
		}
		issueURL := fmt.Sprintf("%s/issues", repoURL)
		issueLink := Hyperlink(fmt.Sprintf("%s %s", iconIssue, issueCountStr), issueURL)

		sb.WriteString(fmt.Sprintf(" %s  |  %s  |  %s  |  %s  |  %s\n\n",
			repoLink, branchLink, lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render(item.StatusMsg), prLink, issueLink,
		))

		sb.WriteString(m.renderTabBar() + "\n\n")

		switch m.ActiveTab {

		case TabLogs:
			if item.ExistingPRURL != "" {
				sb.WriteString(fmt.Sprintf("%s %s %s\n", lipgloss.NewStyle().Bold(true).Foreground(colorYellow).Render("OPEN PR: "), iconPR, badgePR.Render(item.ExistingPRURL)))
			}
			if item.DraftPRURL != "" && item.DraftPRURL != item.ExistingPRURL {
				sb.WriteString(fmt.Sprintf("%s %s %s\n", lipgloss.NewStyle().Bold(true).Foreground(colorYellow).Render("DRAFT PR:"), iconPR, badgePR.Render(item.DraftPRURL)))
			}

			dashCount := (m.Viewport.Width - 18) / 2
			if dashCount < 2 {
				dashCount = 2
			}
			divider := strings.Repeat("─", dashCount) + " EXECUTION LOGS " + strings.Repeat("─", dashCount)
			sb.WriteString("\n" + lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Render(divider) + "\n")

			wrapWidth := m.Viewport.Width - 2
			if wrapWidth < 20 {
				wrapWidth = 40
			}
			logWrapper := lipgloss.NewStyle().Width(wrapWidth)

			for _, logLine := range item.Logs {
				styled := highlightLogLine(logLine)
				sb.WriteString(logWrapper.Render(styled) + "\n")
			}

		case TabBranches:
			sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Render("󰓦 BRANCHES & WORKTREES") + "\n")
			sb.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render("(Press 'X' to git fetch --prune, delete non-default local branches & prune worktrees)") + "\n\n")

			localBranches := item.BranchDetails.GetLocalBranches()
			sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorBlue).Render(" Local Branches:") + "\n")
			if len(localBranches) == 0 {
				sb.WriteString("  (None found)\n")
			} else {
				for _, b := range localBranches {
					sb.WriteString(fmt.Sprintf("  %s %s\n", iconBranch, b))
				}
			}

			remoteBranches := item.BranchDetails.GetRemoteBranches()
			sb.WriteString("\n" + lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Render(" Remote Branches:") + "\n")
			if len(remoteBranches) == 0 {
				sb.WriteString("  (None found)\n")
			} else {
				for _, b := range remoteBranches {
					sb.WriteString(fmt.Sprintf("  %s %s\n", iconBranch, b))
				}
			}

			sb.WriteString("\n" + lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render("󰉓 Git Worktrees:") + "\n")
			if len(item.BranchDetails.Worktrees) == 0 {
				sb.WriteString("  (No worktrees found)\n")
			} else {
				for _, w := range item.BranchDetails.Worktrees {
					sb.WriteString(fmt.Sprintf("  %s %s\n", iconWorktree, w))
				}
			}

			sb.WriteString("\n" + lipgloss.NewStyle().Bold(true).Foreground(colorYellow).Render("󰈔 Changed Files (Branch Diff Status):") + "\n")
			if len(item.BranchDetails.ChangedFiles) == 0 {
				sb.WriteString("  󰄬 Working tree is clean.\n")
			} else {
				for _, f := range item.BranchDetails.ChangedFiles {
					sb.WriteString(fmt.Sprintf("  %s\n", f))
				}
			}

		case TabIssues:
			spinnerStr := ""
			if item.IsLoadingIssues {
				spinnerStr = fmt.Sprintf("  %s %s", cellStatusIconStyle.Render(m.Spinner.View()), lipgloss.NewStyle().Foreground(colorMuted).Render("Updating..."))
			}
			sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Render(fmt.Sprintf("⊙ OPEN ISSUES (%d)", item.OpenIssuesCount)) + spinnerStr + "\n\n")

			if len(item.IssuesList) == 0 && item.IsLoadingIssues {
				sb.WriteString(fmt.Sprintf("  %s Loading open issues from GitHub...\n", cellStatusIconStyle.Render(m.Spinner.View())))
			} else if len(item.IssuesList) == 0 {
				sb.WriteString("  󰄬 No open issues found for this repository.\n")
			} else {
				titleWrapper := lipgloss.NewStyle().Width(m.Viewport.Width - 6)
				for _, issue := range item.IssuesList {
					header := fmt.Sprintf("#%-4d %s", issue.Number, issue.Title)
					sb.WriteString(fmt.Sprintf("  %s %s\n     %s\n\n",
						badgeIssue.Render("⊙"), titleWrapper.Render(header),
						lipgloss.NewStyle().Foreground(colorBlue).Underline(true).Render(issue.URL),
					))
				}
			}

		case TabPRs:
			spinnerStr := ""
			if item.IsLoadingPRs {
				spinnerStr = fmt.Sprintf("  %s %s", cellStatusIconStyle.Render(m.Spinner.View()), lipgloss.NewStyle().Foreground(colorMuted).Render("Updating..."))
			}
			sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Render(fmt.Sprintf("󰏫 OPEN PULL REQUESTS (%d)", item.OpenPRsCount)) + spinnerStr + "\n\n")

			if len(item.PRsList) == 0 && item.IsLoadingPRs {
				sb.WriteString(fmt.Sprintf("  %s Loading open pull requests from GitHub...\n", cellStatusIconStyle.Render(m.Spinner.View())))
			} else if len(item.PRsList) == 0 {
				sb.WriteString("  󰄬 No open pull requests found for this repository.\n")
			} else {
				titleWrapper := lipgloss.NewStyle().Width(m.Viewport.Width - 6)
				for _, pr := range item.PRsList {
					header := fmt.Sprintf("#%-4d %s (%s %s)", pr.Number, pr.Title, iconBranch, pr.HeadRefName)
					sb.WriteString(fmt.Sprintf("  %s %s\n     %s\n\n",
						badgePR.Render("󰏫"), titleWrapper.Render(header),
						lipgloss.NewStyle().Foreground(colorBlue).Underline(true).Render(pr.URL),
					))
				}
			}
		}
	}

	m.Viewport.SetContent(sb.String())
}

func (m Model) renderTabBar() string {
	t1 := "[1 Logs]"
	t2 := "[2 Branches & Worktrees]"
	t3 := "[3 Issues]"
	t4 := "[4 PRs]"

	switch m.ActiveTab {
	case TabLogs:
		return tabActiveStyle.Render(t1) + tabInactiveStyle.Render(t2) + tabInactiveStyle.Render(t3) + tabInactiveStyle.Render(t4)
	case TabBranches:
		return tabInactiveStyle.Render(t1) + tabActiveStyle.Render(t2) + tabInactiveStyle.Render(t3) + tabInactiveStyle.Render(t4)
	case TabIssues:
		return tabInactiveStyle.Render(t1) + tabInactiveStyle.Render(t2) + tabActiveStyle.Render(t3) + tabInactiveStyle.Render(t4)
	case TabPRs:
		return tabInactiveStyle.Render(t1) + tabInactiveStyle.Render(t2) + tabInactiveStyle.Render(t3) + tabActiveStyle.Render(t4)
	}
	return ""
}

func highlightLogLine(line string) string {
	if line == "" {
		return line
	}

	line = reTimestamp.ReplaceAllStringFunc(line, func(m string) string {
		return lipgloss.NewStyle().Foreground(colorMuted).Render(m)
	})

	line = reCmd.ReplaceAllStringFunc(line, func(m string) string {
		return lipgloss.NewStyle().Foreground(colorSecondary).Bold(true).Render(m)
	})

	line = reURL.ReplaceAllStringFunc(line, func(m string) string {
		return lipgloss.NewStyle().Foreground(colorBlue).Underline(true).Render(m)
	})

	line = reQuoted.ReplaceAllStringFunc(line, func(m string) string {
		return lipgloss.NewStyle().Foreground(colorYellow).Bold(true).Render(m)
	})

	if strings.Contains(line, "󰄬") || strings.Contains(line, "PASS") || strings.Contains(line, "successfully") || strings.Contains(line, "Up to date") {
		line = lipgloss.NewStyle().Foreground(colorGreen).Render(line)
	} else if strings.Contains(line, "󰅙") || strings.Contains(line, "error") || strings.Contains(line, "Failed") || strings.Contains(line, "conflict") {
		line = lipgloss.NewStyle().Foreground(colorRed).Render(line)
	}

	return line
}

func (m Model) View() string {
	if m.Width == 0 {
		return "Initializing freshen TUI..."
	}

	header := m.renderHeader()

	// 2. Split Layout Dimensions
	//
	// Lipgloss .Height(N) sets the INNER content area. The outer rendered block is N+2 lines
	// (top border + N content lines + bottom border).
	//
	// Left column: 3 boxes stacked, each rendered as (innerHeight + 2) lines.
	//   Total rendered left = (repoH + 2) + (runnersH + 2) + (jobsH + 2)
	//                       = totalInner + 6
	//
	// Right pane: 1 box, rendered as (rightBoxHeight + 2) lines.
	//
	// JoinHorizontal height = max(left, right).
	// We size rightBoxHeight so right = left: rightBoxHeight + 2 = totalInner + 6
	//   => rightBoxHeight = totalInner + 4
	//
	// Total output lines = 1 (header\n) + mainView lines + 1 (footer) = 1 + (rightBoxHeight + 2) + 1
	// We need: 1 + rightBoxHeight + 2 + 1 = m.Height
	//   => rightBoxHeight = m.Height - 4
	//   => totalInner = rightBoxHeight - 4 = m.Height - 8
	//
	halfWidth := m.Width / 2
	leftWidth := halfWidth - 1
	rightWidth := m.Width - leftWidth - 2

	// Inner content width: border (1) + padding (1) on each side = 4 chars overhead
	paneInnerWidth := leftWidth - 4
	if paneInnerWidth < 30 {
		paneInnerWidth = 30
	}

	rightBoxHeight := m.Height - 4
	if rightBoxHeight < 12 {
		rightBoxHeight = 12
	}

	// totalInner is the sum of content heights for the 3 left boxes
	totalInner := rightBoxHeight - 4
	if totalInner < 8 {
		totalInner = 8
	}

	// runnersBoxHeight=4 means inner content=4 lines, outer rendered=6 (top+4+bottom)
	runnersBoxHeight := 4 // inner content lines (renders as 6 outer lines)
	repoBoxHeight := (totalInner - runnersBoxHeight) * 60 / 100
	if repoBoxHeight < 4 {
		repoBoxHeight = 4
	}

	jobsBoxHeight := totalInner - repoBoxHeight - runnersBoxHeight
	if jobsBoxHeight < 3 {
		jobsBoxHeight = 3
	}

	// clipLine clips a rendered line to the inner pane width, preventing terminal wrapping
	clipLine := func(line string) string {
		firstLine := strings.Split(line, "\n")[0]
		return ansi.Truncate(firstLine, paneInnerWidth, "")
	}
	// sliceLines clips a slice to at most n lines (prevents .Height(N) from overflowing)
	sliceLines := func(lines []string, n int) []string {
		if len(lines) > n {
			return lines[:n]
		}
		return lines
	}

	repoPane := m.renderReposPanel(leftWidth, paneInnerWidth, repoBoxHeight, clipLine, sliceLines)
	runnersPane := m.renderRunnersPanel(leftWidth, paneInnerWidth, runnersBoxHeight, clipLine, sliceLines)
	jobsPane := m.renderJobsPanel(leftWidth, paneInnerWidth, jobsBoxHeight, clipLine, sliceLines)

	leftColumn := lipgloss.JoinVertical(lipgloss.Left, repoPane, runnersPane, jobsPane)

	// Render Details Viewport (Right)
	// logBoxStyle has Border (1+1) + Padding (1+1) = 4 chars horizontal overhead.
	// Width(rightWidth - 2) gives content width (rightWidth - 4) and outer width rightWidth.
	rightInnerWidth := rightWidth - 4
	if rightInnerWidth < 20 {
		rightInnerWidth = 20
	}

	rightPane := logBoxStyle.
		Width(rightWidth - 2).
		Height(rightBoxHeight).
		Render(m.Viewport.View())

	mainView := lipgloss.JoinHorizontal(lipgloss.Top, leftColumn, " ", rightPane)

	// 3. Footer Keybindings Help (on its own line below mainView)
	footer := m.renderFooter()

	// header has trailing \n (1 newline)
	// mainView has (rightBoxHeight + 2) lines
	// footer is placed on its own line preceded by \n
	// strings.Split gives: 1 + (rightBoxHeight + 2) + 1 = rightBoxHeight + 4 = m.Height lines total ✓
	return header + mainView + "\n" + footer
}

// renderHeader renders the top title/subtitle banner line.
func (m Model) renderHeader() string {
	shortTargetDir := git.ShortenHomePath(m.TargetDir)
	leftTitle := titleStyle.Render(fmt.Sprintf(" %s FRESHEN ", iconLeaf)) + " " +
		subtitleStyle.Render("GitHub Repository & CI Runner Workflow Manager")

	orgURL := fmt.Sprintf("https://github.com/%s", m.TargetOrg)
	clickableOrg := Hyperlink(fmt.Sprintf("%s %s", iconGithub, m.TargetOrg), orgURL)
	rightSubtitle := lipgloss.NewStyle().Foreground(colorMuted).Render(fmt.Sprintf("%s |  %s", shortTargetDir, clickableOrg))

	leftWidthVis := lipgloss.Width(leftTitle)
	rightWidthVis := lipgloss.Width(shortTargetDir+" | "+m.TargetOrg) + 3

	spacingLen := m.Width - leftWidthVis - rightWidthVis
	if spacingLen < 1 {
		spacingLen = 1
	}

	headerText := leftTitle + strings.Repeat(" ", spacingLen) + rightSubtitle
	headerStr := lipgloss.NewStyle().MaxWidth(m.Width).Render(headerText)
	headerLines := strings.Split(headerStr, "\n")
	return headerLines[0] + "\n"
}

// renderReposPanel renders PANEL 1: REPOSITORIES.
func (m Model) renderReposPanel(leftWidth, paneInnerWidth, repoBoxHeight int, clipLine func(string) string, sliceLines func([]string, int) []string) string {
	var repoLines []string

	// Fixed columns & overhead:
	// Row prefix: 2 chars ("> " or "  ")
	// Status icon: 2 chars ("• " or "󰄬 ")
	// Spaces: 3 chars (between name/branch, branch/prs, prs/issues)
	// PRs column: 4 chars
	// ISSUES column: 6 chars
	// Total overhead = 2 + 2 + 3 + 4 + 6 = 17 chars
	availRepoW := paneInnerWidth - 17
	if availRepoW < 14 {
		availRepoW = 14
	}
	repoNameW := (availRepoW * 45) / 100
	if repoNameW < 7 {
		repoNameW = 7
	}
	branchW := availRepoW - repoNameW
	if branchW < 7 {
		branchW = 7
	}

	dynCellNameStyle := lipgloss.NewStyle().Width(repoNameW)
	dynCellBranchStyle := lipgloss.NewStyle().Width(branchW)

	repoHeader := fmt.Sprintf("  %s%s %s %s %s",
		cellStatusIconStyle.Render(""),
		dynCellNameStyle.Bold(true).Foreground(colorPrimary).Render(truncateString("REPOSITORY", repoNameW)),
		dynCellBranchStyle.Bold(true).Foreground(colorPrimary).Render(truncateString("BRANCH", branchW)),
		cellPRsStyle.Bold(true).Foreground(colorPrimary).Render("PRs"),
		cellIssuesStyle.Bold(true).Foreground(colorPrimary).Render("ISSUES"),
	)
	repoLines = append(repoLines, clipLine(repoHeader))
	repoLines = append(repoLines, clipLine(lipgloss.NewStyle().Foreground(colorMuted).Render(strings.Repeat("─", paneInnerWidth))))

	if m.IsOrgSyncing {
		repoLines = append(repoLines, clipLine(cellStatusIconStyle.Render(m.Spinner.View())+" Fetching GitHub repositories..."))
	} else if len(m.Repos) == 0 {
		repoLines = append(repoLines, clipLine("No repositories found in target directory."))
	} else {
		maxRepoRows := repoBoxHeight - 4
		if maxRepoRows < 1 {
			maxRepoRows = 1
		}

		startIdx := 0
		if m.SelectedIndex >= maxRepoRows {
			startIdx = m.SelectedIndex - maxRepoRows + 1
		}
		endIdx := startIdx + maxRepoRows
		if endIdx > len(m.Repos) {
			endIdx = len(m.Repos)
		}

		for i := startIdx; i < endIdx; i++ {
			item := m.Repos[i]
			isSelected := m.ActiveFocus == FocusRepos && i == m.SelectedIndex

			nameStyle := dynCellNameStyle
			branchBaseStyle := dynCellBranchStyle
			prsStyle := cellPRsStyle
			issuesStyle := cellIssuesStyle
			statusIconStyle := cellStatusIconStyle

			if isSelected {
				nameStyle = nameStyle.Background(lipgloss.Color("#313244"))
				branchBaseStyle = branchBaseStyle.Background(lipgloss.Color("#313244"))
				prsStyle = prsStyle.Background(lipgloss.Color("#313244"))
				issuesStyle = issuesStyle.Background(lipgloss.Color("#313244"))
				statusIconStyle = statusIconStyle.Background(lipgloss.Color("#313244"))
			}

			statusIconStr := statusIconStyle.Render(m.renderStatusBadge(item))
			nameCell := nameStyle.Render(truncateString(item.Name, repoNameW))

			branchStr := item.CurrentBranch
			if branchStr == "" {
				branchStr = "-"
			}
			branchStr = truncateString(branchStr, branchW)
			displayText := branchStr

			var branchStyle lipgloss.Style
			isDefaultBranch := branchStr == "main" || branchStr == "master"

			if item.IsArchived {
				displayText = "Archived"
				branchStyle = lipgloss.NewStyle().Foreground(colorMuted).Strikethrough(true)
			} else if item.Status == git.StatusError || item.Status == git.StatusRebaseConflict {
				branchStyle = lipgloss.NewStyle().Foreground(colorRed).Bold(true)
			} else if item.HasUnstagedChanges {
				branchStyle = lipgloss.NewStyle().Foreground(colorYellow).Bold(true)
			} else if isDefaultBranch || branchStr == "-" {
				branchStyle = lipgloss.NewStyle().Foreground(colorMuted)
			} else {
				branchStyle = lipgloss.NewStyle().Foreground(colorGreen).Bold(true)
			}

			if isSelected {
				branchStyle = branchStyle.Background(lipgloss.Color("#313244"))
			}

			branchCell := branchBaseStyle.Render(branchStyle.Render(displayText))

			var prsCell string
			if !item.HasLoadedCounts && !item.HasLoadedPRs {
				prsCell = prsStyle.Foreground(colorMuted).Render("?")
			} else if item.OpenPRsCount > 0 {
				prsCell = prsStyle.Foreground(colorYellow).Bold(true).Render(fmt.Sprintf("%d", item.OpenPRsCount))
			} else {
				prsCell = prsStyle.Foreground(colorMuted).Render("—")
			}

			var issuesCell string
			if !item.HasLoadedCounts && !item.HasLoadedIssues {
				issuesCell = issuesStyle.Foreground(colorMuted).Render("?")
			} else if item.OpenIssuesCount > 0 {
				issuesCell = issuesStyle.Foreground(colorBlue).Bold(true).Render(fmt.Sprintf("%d", item.OpenIssuesCount))
			} else {
				issuesCell = issuesStyle.Foreground(colorMuted).Render("—")
			}

			line := fmt.Sprintf("%s%s %s %s %s", statusIconStr, nameCell, branchCell, prsCell, issuesCell)

			if isSelected {
				repoLines = append(repoLines, clipLine(selectedRowStyle.Render("> "+line)))
			} else {
				repoLines = append(repoLines, clipLine(normalRowStyle.Render("  "+line)))
			}
		}
	}

	repoStyle := borderBoxStyle
	if m.ActiveFocus == FocusRepos {
		repoStyle = borderFocusedStyle
	}
	return repoStyle.
		Width(leftWidth - 2).
		Height(repoBoxHeight).
		Render(strings.Join(sliceLines(repoLines, repoBoxHeight), "\n"))
}

// renderRunnersPanel renders PANEL 2: REGISTERED RUNNERS (TAG BROWSER).
func (m Model) renderRunnersPanel(leftWidth, paneInnerWidth, runnersBoxHeight int, clipLine func(string) string, sliceLines func([]string, int) []string) string {
	var runnerLines []string
	tags := m.getAvailableTags()
	if m.SelectedTagIndex >= len(tags) {
		m.SelectedTagIndex = 0
	}
	activeTag := tags[m.SelectedTagIndex]

	tagTitle := "REGISTERED RUNNERS"
	if activeTag != "ALL" {
		tagTitle = fmt.Sprintf("REGISTERED RUNNERS (%s)", activeTag)
	}

	runnerHeader := fmt.Sprintf(" %s %s", iconRunner, lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Render(tagTitle))
	runnerLines = append(runnerLines, clipLine(runnerHeader))
	runnerLines = append(runnerLines, clipLine(lipgloss.NewStyle().Foreground(colorMuted).Render(strings.Repeat("─", paneInnerWidth))))

	if m.IsRunnersLoading && len(m.Runners) == 0 {
		runnerLines = append(runnerLines, clipLine(fmt.Sprintf(" %s Fetching registered runners...", cellStatusIconStyle.Render(m.Spinner.View()))))
	}

	var runningNames, idleNames, offlineNames []string
	for _, r := range m.Runners {
		match := false
		if activeTag == "ALL" {
			match = true
		} else {
			for _, tag := range r.Tags {
				if tag == activeTag {
					match = true
					break
				}
			}
		}
		if !match {
			continue
		}

		switch r.Status {
		case jobs.RunnerRunning:
			runningNames = append(runningNames, r.Name)
		case jobs.RunnerOffline:
			offlineNames = append(offlineNames, r.Name)
		default:
			idleNames = append(idleNames, r.Name)
		}
	}

	if len(runningNames) > 0 {
		glyph := lipgloss.NewStyle().Foreground(colorSecondary).Bold(true).Width(2).Render("⚡")
		label := lipgloss.NewStyle().Foreground(colorSecondary).Bold(true).Render(fmt.Sprintf(" RUNNING (%d): ", len(runningNames)))
		names := lipgloss.NewStyle().Foreground(colorGreen).Render(strings.Join(runningNames, ", "))
		runnerLines = append(runnerLines, clipLine(" "+glyph+label+names))
	}
	if len(idleNames) > 0 {
		glyph := lipgloss.NewStyle().Foreground(colorGreen).Bold(true).Width(2).Render("●")
		label := lipgloss.NewStyle().Foreground(colorGreen).Bold(true).Render(fmt.Sprintf(" IDLE (%d): ", len(idleNames)))
		names := lipgloss.NewStyle().Foreground(colorMuted).Render(strings.Join(idleNames, ", "))
		runnerLines = append(runnerLines, clipLine(" "+glyph+label+names))
	}
	if len(offlineNames) > 0 {
		glyph := lipgloss.NewStyle().Foreground(colorRed).Bold(true).Width(2).Render("✖")
		label := lipgloss.NewStyle().Foreground(colorRed).Bold(true).Render(fmt.Sprintf(" OFFLINE (%d): ", len(offlineNames)))
		names := lipgloss.NewStyle().Foreground(colorRed).Render(strings.Join(offlineNames, ", "))
		runnerLines = append(runnerLines, clipLine(" "+glyph+label+names))
	}

	if !m.IsRunnersLoading && len(runningNames) == 0 && len(idleNames) == 0 && len(offlineNames) == 0 {
		if m.RunnerFetchFailed && !m.RunnerPermissionDenied {
			runnerLines = append(runnerLines, clipLine(lipgloss.NewStyle().Foreground(colorMuted).Render(" Failed to fetch registered runners.")))
		} else {
			runnerLines = append(runnerLines, clipLine(lipgloss.NewStyle().Foreground(colorMuted).Render(" No active self-hosted runners or jobs detected.")))
		}
	}

	runnerStyle := borderBoxStyle
	if m.ActiveFocus == FocusRunners {
		runnerStyle = borderFocusedStyle
	}
	return runnerStyle.
		Width(leftWidth - 2).
		Height(runnersBoxHeight).
		Render(strings.Join(sliceLines(runnerLines, runnersBoxHeight), "\n"))
}

// renderJobsPanel renders PANEL 3: OVERALL JOB QUEUE.
func (m Model) renderJobsPanel(leftWidth, paneInnerWidth, jobsBoxHeight int, clipLine func(string) string, sliceLines func([]string, int) []string) string {
	var jobsLines []string
	runningCount := 0
	queuedCount := 0
	for _, j := range m.JobQueue {
		if j.Status == jobs.JobRunning {
			runningCount++
		} else if j.Status == jobs.JobQueued {
			queuedCount++
		}
	}
	countsStr := ""
	if runningCount > 0 || queuedCount > 0 {
		countsStr = fmt.Sprintf(" (%d running, %d queued)", runningCount, queuedCount)
	}
	jobHeaderSpinner := ""
	if m.IsJobQueueLoading || m.IsOrgSyncing {
		jobHeaderSpinner = " " + m.Spinner.View()
	}
	jobHeader := fmt.Sprintf(" %s %s%s%s", iconQueue, lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render("OVERALL JOB QUEUE"), jobHeaderSpinner, lipgloss.NewStyle().Foreground(colorSecondary).Render(countsStr))
	jobsLines = append(jobsLines, clipLine(jobHeader))
	jobsLines = append(jobsLines, clipLine(lipgloss.NewStyle().Foreground(colorMuted).Render(strings.Repeat("─", paneInnerWidth))))

	if len(m.JobQueue) == 0 {
		if m.IsJobQueueLoading || m.IsOrgSyncing {
			jobsLines = append(jobsLines, clipLine(fmt.Sprintf(" %s Fetching active workflow jobs...", cellStatusIconStyle.Render(m.Spinner.View()))))
		} else {
			jobsLines = append(jobsLines, clipLine(lipgloss.NewStyle().Foreground(colorMuted).Render(" No queued or running workflow jobs.")))
		}
	}

	// maxJobRows: inner content area of jobs box minus header and divider lines
	maxJobRows := jobsBoxHeight - 2
	if maxJobRows < 1 {
		maxJobRows = 1
	}
	jobStartIdx := 0
	if m.SelectedJobIndex >= maxJobRows {
		jobStartIdx = m.SelectedJobIndex - maxJobRows + 1
	}
	jobEndIdx := jobStartIdx + maxJobRows
	if jobEndIdx > len(m.JobQueue) {
		jobEndIdx = len(m.JobQueue)
	}

	// Detect grouped runs to add tree branch connectors for matrix jobs
	runCounts := make(map[int64]int)
	runIndices := make(map[int64][]int)
	for idx, j := range m.JobQueue {
		if j.RunID != 0 {
			runCounts[j.RunID]++
			runIndices[j.RunID] = append(runIndices[j.RunID], idx)
		}
	}

	renderedRuns := make(map[int64]bool)

	for i := jobStartIdx; i < jobEndIdx; i++ {
		j := m.JobQueue[i]
		runToken, jobToken := parseJobHierarchy(j.Name, j.Repo)

		stSymbol := "⏳"
		stColor := colorYellow
		if j.Status == jobs.JobRunning {
			stSymbol = "⚡"
			stColor = colorSecondary
		} else if j.Status == jobs.JobPassed {
			stSymbol = "󰄬"
			stColor = colorGreen
		} else if j.Status == jobs.JobFailed {
			stSymbol = "󰅙"
			stColor = colorRed
		}

		stBadge := lipgloss.NewStyle().Foreground(stColor).Bold(true).Render(fmt.Sprintf("%s %-7s", stSymbol, j.Status))

		// If this job is part of a multi-job run and we haven't rendered the parent run header yet:
		if runCounts[j.RunID] > 1 && !renderedRuns[j.RunID] {
			renderedRuns[j.RunID] = true
			runTitleText := runToken
			if j.RunID != 0 {
				runURL := fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d", m.TargetOrg, j.Repo, j.RunID)
				runTitleText = Hyperlink(lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(runToken), runURL)
			} else {
				runTitleText = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(runToken)
			}

			// Format Trigger badge with hyperlink
			triggerBadge := ""
			if j.PRNumber != 0 {
				prLabel := fmt.Sprintf("PR #%d", j.PRNumber)
				if j.PRTitle != "" {
					prLabel = fmt.Sprintf("PR #%d: %s", j.PRNumber, j.PRTitle)
				}
				prURL := j.PRURL
				if prURL == "" {
					prURL = fmt.Sprintf("https://github.com/%s/%s/pull/%d", m.TargetOrg, j.Repo, j.PRNumber)
				}
				triggerBadge = lipgloss.NewStyle().Foreground(colorYellow).Bold(true).Render(" (" + Hyperlink(prLabel, prURL) + ")")
			} else if j.Event != "" {
				eventText := fmt.Sprintf("(%s)", j.Event)
				if j.Branch != "" {
					branchURL := fmt.Sprintf("https://github.com/%s/%s/tree/%s", m.TargetOrg, j.Repo, j.Branch)
					eventText = fmt.Sprintf("(%s on %s)", j.Event, Hyperlink(j.Branch, branchURL))
				}
				triggerBadge = " " + lipgloss.NewStyle().Foreground(colorMuted).Render(eventText)
			}

			runHeaderLine := fmt.Sprintf("  %s %s%s", stBadge, runTitleText, triggerBadge)
			jobsLines = append(jobsLines, clipLine(runHeaderLine))
		}

		runnerStr := j.RunnerName
		if runnerStr == "" {
			runnerStr = "awaiting"
		}
		runnerW := len(runnerStr) + 2 // include "→ " prefix
		if runnerW < 10 {
			runnerW = 10
		}
		if runnerW > 28 {
			runnerW = 28
		}
		runnerAssigned := lipgloss.NewStyle().Foreground(colorMuted).Width(runnerW).Render("→ " + truncateString(runnerStr, runnerW-2))

		var line string
		if runCounts[j.RunID] > 1 {
			// Child matrix job row
			indices := runIndices[j.RunID]
			treeConnector := "├─ "
			if indices[len(indices)-1] == i {
				treeConnector = "└─ "
			}

			nameW := paneInnerWidth - 4 - 5 - runnerW - 1
			if nameW < 10 {
				nameW = 10
			}
			jobStrText := lipgloss.NewStyle().Foreground(colorSecondary).Width(nameW).Render(truncateString(jobToken, nameW))
			if j.RunID != 0 {
				jobURL := fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d", m.TargetOrg, j.Repo, j.RunID)
				if j.GHJobID != 0 {
					jobURL = fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d/job/%d", m.TargetOrg, j.Repo, j.RunID, j.GHJobID)
				}
				jobStrText = Hyperlink(jobStrText, jobURL)
			}
			line = fmt.Sprintf("  %s%s %s", treeConnector, jobStrText, runnerAssigned)
		} else {
			// Single job run
			nameW := paneInnerWidth - 4 - 9 - 1 - runnerW - 1
			if nameW < 10 {
				nameW = 10
			}
			jobStrText := lipgloss.NewStyle().Width(nameW).Render(truncateString(jobToken, nameW))
			if j.RunID != 0 {
				jobURL := fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d", m.TargetOrg, j.Repo, j.RunID)
				if j.GHJobID != 0 {
					jobURL = fmt.Sprintf("https://github.com/%s/%s/actions/runs/%d/job/%d", m.TargetOrg, j.Repo, j.RunID, j.GHJobID)
				}
				jobStrText = Hyperlink(jobStrText, jobURL)
			}
			line = fmt.Sprintf("%s %s %s", stBadge, jobStrText, runnerAssigned)
		}

		if m.ActiveFocus == FocusJobs && i == m.SelectedJobIndex {
			jobsLines = append(jobsLines, clipLine(selectedRowStyle.MaxWidth(paneInnerWidth).Render("> "+line)))
		} else {
			jobsLines = append(jobsLines, clipLine(normalRowStyle.Render("  "+line)))
		}
	}

	jobsStyle := borderBoxStyle
	if m.ActiveFocus == FocusJobs {
		jobsStyle = borderFocusedStyle
	}
	return jobsStyle.
		Width(leftWidth - 2).
		Height(jobsBoxHeight).
		Render(strings.Join(sliceLines(jobsLines, jobsBoxHeight), "\n"))
}

// renderFooter renders the bottom keybindings help line, or the active toast if set.
func (m Model) renderFooter() string {
	footerText := "[w/⇥/⇧⇥] Focus  [↑/↓/j/k] Select  [1-4/←/→] Tabs  [a] Sync All  [r] Sync  [b] Branch  [p] Push/PR  [dd] Del Archived  [X] Prune  [c] Copy  [q] Quit"
	if m.ToastMsg != "" {
		msgText := m.ToastMsg
		if lipgloss.Width(msgText) > m.Width {
			msgText = truncateString(msgText, m.Width)
		}
		toastColor := colorGreen
		if m.ToastPriority == 2 {
			toastColor = colorRed
		}
		footerText = lipgloss.NewStyle().Foreground(toastColor).Bold(true).Render(msgText)
	} else if lipgloss.Width(footerText) > m.Width {
		footerText = truncateString(footerText, m.Width)
	}
	return footerText
}

func (m Model) renderStatusBadge(item *git.RepoItem) string {
	switch item.Status {
	case git.StatusUpToDate:
		return badgeUpToDate.Render(iconSuccess)
	case git.StatusUpdated, git.StatusCloned:
		return badgeUpdated.Render(iconSuccess)
	case git.StatusStashedApplied:
		return badgeStash.Render(iconStash)
	case git.StatusSwitchedDefault:
		return badgeUpToDate.Render(iconSwitch)
	case git.StatusRebased:
		return badgeRebased.Render(iconRebase)
	case git.StatusPRCreated:
		return badgePR.Render(iconPR)
	case git.StatusError, git.StatusRebaseConflict:
		return badgeError.Render(iconError)
	case git.StatusArchived:
		return badgeArchived.Render(iconTrash)
	case git.StatusSyncing:
		return m.Spinner.View()
	default:
		return lipgloss.NewStyle().Foreground(colorMuted).Render(iconPending)
	}
}

func truncateString(str string, maxLen int) string {
	runes := []rune(str)
	if len(runes) <= maxLen {
		return str
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}

func parseJobHierarchy(fullName, repo string) (runName, jobName string) {
	cleanName := fullName
	repoPrefix := repo + " / "
	if strings.HasPrefix(cleanName, repoPrefix) {
		cleanName = strings.TrimPrefix(cleanName, repoPrefix)
	}

	parts := strings.Split(cleanName, " / ")
	if len(parts) >= 2 {
		runName = parts[0]
		jobName = strings.Join(parts[1:], " / ")
	} else {
		runName = "-"
		jobName = cleanName
	}
	return runName, jobName
}

func findJobForRunner(r *jobs.RunnerItem, queue []*jobs.JobItem) *jobs.JobItem {
	if r == nil {
		return nil
	}
	// 1. Try exact runner name match (running)
	for _, j := range queue {
		if j.Status == jobs.JobRunning && (j.RunnerName == r.Name || j.RunnerID == r.ID) {
			return j
		}
	}
	// 2. Try case-insensitive runner name match (running)
	for _, j := range queue {
		if j.Status == jobs.JobRunning && j.RunnerName != "" && strings.EqualFold(j.RunnerName, r.Name) {
			return j
		}
	}
	// 3. Try any queued/running job assigned to this runner
	for _, j := range queue {
		if (j.RunnerName == r.Name || j.RunnerID == r.ID) && j.Name != "" {
			return j
		}
	}
	// No more fallback — returning a random running job would be misleading.
	return nil
}

func (m Model) getAvailableTags() []string {
	tagsSet := make(map[string]bool)
	for _, r := range m.Runners {
		for _, tag := range r.Tags {
			if tag != "" {
				tagsSet[tag] = true
			}
		}
	}
	tags := []string{"ALL"}
	var keys []string
	for k := range tagsSet {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return append(tags, keys...)
}

func (m Model) getMatchingRunners() []*jobs.RunnerItem {
	tags := m.getAvailableTags()
	tagIdx := m.SelectedTagIndex
	if tagIdx >= len(tags) {
		tagIdx = 0
	}
	activeTag := tags[tagIdx]

	var matching []*jobs.RunnerItem
	for _, r := range m.Runners {
		if activeTag == "ALL" {
			matching = append(matching, r)
		} else {
			for _, tag := range r.Tags {
				if tag == activeTag {
					matching = append(matching, r)
					break
				}
			}
		}
	}
	return matching
}
