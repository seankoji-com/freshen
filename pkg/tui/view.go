package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
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
	case FocusJobs:
		m.Viewport.SetContent(m.jobDetailContent())
		return
	case FocusRunners:
		m.Viewport.SetContent(m.runnerDetailContent())
		return
	case FocusRepos:
		if len(m.Repos) == 0 || m.SelectedIndex >= len(m.Repos) {
			if m.IsOrgSyncing {
				m.Viewport.SetContent(fmt.Sprintf(" %s Fetching GitHub repositories...", m.Spinner.View()))
			} else {
				m.Viewport.SetContent(lipgloss.NewStyle().Foreground(colorMuted).Render(" No repositories found. Esc returns to the list."))
			}
			return
		}

		item := m.Repos[m.SelectedIndex]

		repoURL := item.URL
		if repoURL == "" {
			repoName := item.GHRepoName
			if repoName == "" {
				repoName = item.Name
			}
			repoURL = fmt.Sprintf("https://github.com/%s/%s", m.TargetOrg, repoName)
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

		fmt.Fprintf(&sb, " %s  |  %s  |  %s  |  %s  |  %s\n\n",
			repoLink, branchLink, lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render(item.StatusMsg), prLink, issueLink,
		)

		switch m.ActiveTab {

		case TabLogs:
			if item.ExistingPRURL != "" {
				fmt.Fprintf(&sb, "%s %s %s\n", lipgloss.NewStyle().Bold(true).Foreground(colorYellow).Render("OPEN PR: "), iconPR, badgePR.Render(item.ExistingPRURL))
			}
			if item.DraftPRURL != "" && item.DraftPRURL != item.ExistingPRURL {
				fmt.Fprintf(&sb, "%s %s %s\n", lipgloss.NewStyle().Bold(true).Foreground(colorYellow).Render("DRAFT PR:"), iconPR, badgePR.Render(item.DraftPRURL))
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
			if m.RepoDetailLoading != "" && m.RepoDetailLoading == item.Path {
				sb.WriteString("Loading branches and worktrees…\n")
				break
			}
			sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Render("󰓦 BRANCHES & WORKTREES") + "\n")
			sb.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render("(Press 'X' to git fetch --prune, delete non-default local branches & prune worktrees)") + "\n\n")

			localBranches := item.BranchDetails.GetLocalBranches()
			sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorBlue).Render(" Local Branches:") + "\n")
			if len(localBranches) == 0 {
				sb.WriteString("  (None found)\n")
			} else {
				for _, b := range localBranches {
					fmt.Fprintf(&sb, "  %s %s\n", iconBranch, b)
				}
			}

			remoteBranches := item.BranchDetails.GetRemoteBranches()
			sb.WriteString("\n" + lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Render(" Remote Branches:") + "\n")
			if len(remoteBranches) == 0 {
				sb.WriteString("  (None found)\n")
			} else {
				for _, b := range remoteBranches {
					fmt.Fprintf(&sb, "  %s %s\n", iconBranch, b)
				}
			}

			sb.WriteString("\n" + lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render("󰉓 Git Worktrees:") + "\n")
			if len(item.BranchDetails.Worktrees) == 0 {
				sb.WriteString("  (No worktrees found)\n")
			} else {
				for _, w := range item.BranchDetails.Worktrees {
					fmt.Fprintf(&sb, "  %s %s\n", iconWorktree, w)
				}
			}

			sb.WriteString("\n" + lipgloss.NewStyle().Bold(true).Foreground(colorYellow).Render("󰈔 Changed Files (Branch Diff Status):") + "\n")
			if len(item.BranchDetails.ChangedFiles) == 0 {
				sb.WriteString("  󰄬 Working tree is clean.\n")
			} else {
				for _, f := range item.BranchDetails.ChangedFiles {
					fmt.Fprintf(&sb, "  %s\n", f)
				}
			}

		case TabIssues:
			spinnerStr := ""
			if item.IsLoadingIssues {
				spinnerStr = fmt.Sprintf("  %s %s", cellStatusIconStyle.Render(m.Spinner.View()), lipgloss.NewStyle().Foreground(colorMuted).Render("Updating..."))
			}
			sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Render(fmt.Sprintf("⊙ OPEN ISSUES (%d)", item.OpenIssuesCount)) + spinnerStr + "\n\n")

			if len(item.IssuesList) == 0 && item.IsLoadingIssues {
				fmt.Fprintf(&sb, "  %s Loading open issues from GitHub...\n", cellStatusIconStyle.Render(m.Spinner.View()))
			} else if !item.HasLoadedIssues {
				sb.WriteString("Issues not loaded. Press r to retry.\n")
			} else if len(item.IssuesList) == 0 {
				sb.WriteString("  󰄬 No open issues found for this repository.\n")
			} else {
				titleWrapper := lipgloss.NewStyle().Width(m.Viewport.Width - 6)
				for _, issue := range item.IssuesList {
					header := fmt.Sprintf("#%-4d %s", issue.Number, issue.Title)
					fmt.Fprintf(&sb, "  %s %s\n     %s\n\n",
						badgeIssue.Render("⊙"), titleWrapper.Render(header),
						lipgloss.NewStyle().Foreground(colorBlue).Underline(true).Render(issue.URL),
					)
				}
			}

		case TabPRs:
			spinnerStr := ""
			if item.IsLoadingPRs {
				spinnerStr = fmt.Sprintf("  %s %s", cellStatusIconStyle.Render(m.Spinner.View()), lipgloss.NewStyle().Foreground(colorMuted).Render("Updating..."))
			}
			sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorSecondary).Render(fmt.Sprintf("󰏫 OPEN PULL REQUESTS (%d)", item.OpenPRsCount)) + spinnerStr + "\n\n")

			if len(item.PRsList) == 0 && item.IsLoadingPRs {
				fmt.Fprintf(&sb, "  %s Loading open pull requests from GitHub...\n", cellStatusIconStyle.Render(m.Spinner.View()))
			} else if !item.HasLoadedPRs {
				sb.WriteString("Pull requests not loaded. Press r to retry.\n")
			} else if len(item.PRsList) == 0 {
				sb.WriteString("  󰄬 No open pull requests found for this repository.\n")
			} else {
				titleWrapper := lipgloss.NewStyle().Width(m.Viewport.Width - 6)
				for _, pr := range item.PRsList {
					header := fmt.Sprintf("#%-4d %s (%s %s)", pr.Number, pr.Title, iconBranch, pr.HeadRefName)
					fmt.Fprintf(&sb, "  %s %s\n     %s\n\n",
						badgePR.Render("󰏫"), titleWrapper.Render(header),
						lipgloss.NewStyle().Foreground(colorBlue).Underline(true).Render(pr.URL),
					)
				}
			}
		}
	}

	m.Viewport.SetContent(sb.String())
}

func (m Model) renderTabBar() string {
	t1 := "Logs"
	t2 := "Branches"
	t3 := "Issues"
	t4 := "PRs"

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

func (m Model) View() string { return m.screenView() }

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
	case git.StatusSkipped:
		return badgeSkipped.Render(iconSkipped)
	case git.StatusSyncing:
		return m.Spinner.View()
	default:
		return lipgloss.NewStyle().Foreground(colorMuted).Render(iconPending)
	}
}

func findJobForRunner(r *jobs.RunnerItem, queue []*jobs.JobItem) *jobs.JobItem {
	if r == nil {
		return nil
	}
	// 1. Try exact runner name match (running)
	for _, j := range queue {
		if j.Status == jobs.JobRunning && ((r.Name != "" && j.RunnerName == r.Name) || (r.ID != "" && j.RunnerID == r.ID)) {
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
		if ((r.Name != "" && j.RunnerName == r.Name) || (r.ID != "" && j.RunnerID == r.ID)) && j.Name != "" {
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
