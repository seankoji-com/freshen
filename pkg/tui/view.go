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

// setDetail fills the detail viewport, wrapping every row to its width: the
// frame clips anything wider, and detail text has no horizontal scroll.
func (m *Model) setDetail(content string) {
	m.Viewport.SetContent(ansi.Wrap(content, max(1, m.Viewport.Width), ""))
}

func (m *Model) updateViewport() {
	var sb strings.Builder

	switch m.ActiveFocus {
	case FocusJobs:
		m.setDetail(m.jobDetailContent())
		return
	case FocusRunners:
		m.setDetail(m.runnerDetailContent())
		return
	case FocusRepos, FocusConcerns:
		if len(m.Repos) == 0 || m.SelectedIndex < 0 || m.SelectedIndex >= len(m.Repos) {
			if m.IsOrgSyncing {
				m.setDetail(fmt.Sprintf(" %s Fetching GitHub repositories…", m.Spinner.View()))
			} else {
				m.setDetail(th.dim.Render(" This repository is no longer in the snapshot. Esc returns to the list."))
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
		ghName := item.GHRepoName
		if ghName == "" {
			ghName = item.Name
		}
		repoLink := Hyperlink(th.section.Render(ghName), repoURL)

		branchDetail := "not cloned"
		if !item.IsNew {
			branchDetail = fmt.Sprintf("%s (default)", item.CurrentBranch)
			if item.CurrentBranch != item.DefaultBranch {
				branchDetail = fmt.Sprintf("%s (default: %s)", item.CurrentBranch, item.DefaultBranch)
			}
		}
		branchLink := Hyperlink("branch "+branchDetail, fmt.Sprintf("%s/tree/%s", repoURL, item.CurrentBranch))

		counts := "PR and issue counts not loaded"
		if item.HasLoadedCounts {
			prLink := Hyperlink(th.pr.Render(countPhrase(item.OpenPRsCount, "open PR", "open PRs")), repoURL+"/pulls")
			issueLink := Hyperlink(th.issue.Render(countPhrase(item.OpenIssuesCount, "open issue", "open issues")), repoURL+"/issues")
			counts = prLink + th.dim.Render(" · ") + issueLink
		}
		status := m.renderStatusBadge(item)
		if _, label := repoKind(item.Status); item.StatusMsg != "" && !strings.EqualFold(item.StatusMsg, label) {
			status += th.dim.Render(" · " + item.StatusMsg)
		}
		sep := th.dim.Render("  ·  ")
		fmt.Fprintf(&sb, " %s%s%s%s%s\n", repoLink, sep, branchLink, sep, status)
		fmt.Fprintf(&sb, " %s", counts)
		if item.HasUnstagedChanges {
			sb.WriteString(sep + th.warning.Render(statusGlyphs[kindWarning]+" local changes"))
		}
		sb.WriteString("\n\n")

		switch m.ActiveTab {

		case TabLogs:
			if item.ExistingPRURL != "" {
				fmt.Fprintf(&sb, "%s %s\n", th.pr.Render("Open PR: "), th.link.Render(item.ExistingPRURL))
			}
			if item.DraftPRURL != "" && item.DraftPRURL != item.ExistingPRURL {
				fmt.Fprintf(&sb, "%s %s\n", th.pr.Render("Draft PR:"), th.link.Render(item.DraftPRURL))
			}

			sb.WriteString("\n" + sectionRule("Sync log", m.Viewport.Width-2) + "\n")
			if len(item.Logs) == 0 {
				sb.WriteString(th.dim.Render("No sync has run for this repository in this session. Press s to sync it.") + "\n")
			}

			wrapWidth := max(10, m.Viewport.Width-2)
			logWrapper := lipgloss.NewStyle().Width(wrapWidth)

			for _, logLine := range item.Logs {
				styled := highlightLogLine(logLine)
				sb.WriteString(logWrapper.Render(styled) + "\n")
			}

		case TabBranches:
			if item.Path != "" && (m.RepoDetailLoading == item.Path || m.RepoDetailQueued == item.Path) {
				sb.WriteString(m.Spinner.View() + " Loading branches and worktrees…\n")
				break
			}
			details := item.BranchDetails
			if details.Unread {
				sb.WriteString(th.status(kindWarning, "Not read since the checkout moved to "+jobs.SanitizeTerminal(item.CurrentBranch)+".") + " Press r to read branches and worktrees.\n")
				break
			}
			if details.Failed() {
				sb.WriteString(th.status(kindWarning, "Some git reads failed.") + " Press r to retry.\n\n")
			}
			sb.WriteString(th.dim.Render("X removes clean worktrees and deletes local branches already on a remote, after confirmation.") + "\n\n")
			// A failed read never reads as empty: it states the failure and
			// labels any entries kept from an earlier read.
			listSection := func(heading lipgloss.Style, title string, items []string, readErr string) {
				if readErr != "" {
					sb.WriteString(heading.Render(title) + "  " + th.status(kindWarning, "unavailable") + "\n")
					sb.WriteString("  " + th.dim.Render("Could not read: "+jobs.SanitizeTerminal(readErr)) + "\n")
					if len(items) > 0 {
						sb.WriteString(th.dim.Render(fmt.Sprintf("  From an earlier read (%d):", len(items))) + "\n")
					}
				} else {
					sb.WriteString(heading.Render(fmt.Sprintf("%s (%d)", title, len(items))) + "\n")
					if len(items) == 0 {
						sb.WriteString(th.dim.Render("  none found") + "\n")
					}
				}
				for _, entry := range items {
					sb.WriteString("  " + entry + "\n")
				}
				sb.WriteString("\n")
			}
			listSection(th.branchHeading, "Local branches", details.GetLocalBranches(), details.BranchesErr)
			listSection(th.branchHeading, "Remote branches", details.GetRemoteBranches(), details.BranchesErr)
			listSection(th.worktreeHeading, "Worktrees", details.Worktrees, details.WorktreesErr)
			if len(details.ChangedFiles) == 0 && details.StatusErr == "" {
				sb.WriteString(th.warning.Render("Changed files (0)") + "\n  " + th.status(kindSuccess, "working tree is clean") + "\n")
			} else {
				listSection(th.warning, "Changed files", details.ChangedFiles, details.StatusErr)
			}

		case TabIssues:
			spinnerStr := ""
			if item.IsLoadingIssues {
				spinnerStr = "  " + m.Spinner.View() + th.dim.Render(" updating…")
			}
			sb.WriteString(th.section.Render(fmt.Sprintf("Open issues (%d)", item.OpenIssuesCount)) + spinnerStr + "\n\n")

			switch {
			case m.TargetOrg == "":
				sb.WriteString("Issues need a GitHub owner. Restart with -owner NAME.\n")
			case len(item.IssuesList) == 0 && item.IsLoadingIssues:
				sb.WriteString("  " + m.Spinner.View() + " Loading open issues from GitHub…\n")
			case !item.HasLoadedIssues:
				sb.WriteString(th.warning.Render(statusGlyphs[kindWarning]+" Issues not loaded.") + " Press r to retry.\n")
			case len(item.IssuesList) == 0:
				sb.WriteString("  " + th.status(kindSuccess, "No open issues.") + "\n")
			default:
				titleWrapper := lipgloss.NewStyle().Width(max(10, m.Viewport.Width-6))
				for _, issue := range item.IssuesList {
					header := fmt.Sprintf("#%-4d %s", issue.Number, issue.Title)
					fmt.Fprintf(&sb, "  %s\n    %s\n\n", th.issue.Render(titleWrapper.Render(header)), th.link.Render(issue.URL))
				}
			}

		case TabPRs:
			spinnerStr := ""
			if item.IsLoadingPRs {
				spinnerStr = "  " + m.Spinner.View() + th.dim.Render(" updating…")
			}
			sb.WriteString(th.section.Render(fmt.Sprintf("Open pull requests (%d)", item.OpenPRsCount)) + spinnerStr + "\n\n")

			switch {
			case m.TargetOrg == "":
				sb.WriteString("Pull requests need a GitHub owner. Restart with -owner NAME.\n")
			case len(item.PRsList) == 0 && item.IsLoadingPRs:
				sb.WriteString("  " + m.Spinner.View() + " Loading open pull requests from GitHub…\n")
			case !item.HasLoadedPRs:
				sb.WriteString(th.warning.Render(statusGlyphs[kindWarning]+" Pull requests not loaded.") + " Press r to retry.\n")
			case len(item.PRsList) == 0:
				sb.WriteString("  " + th.status(kindSuccess, "No open pull requests.") + "\n")
			default:
				titleWrapper := lipgloss.NewStyle().Width(max(10, m.Viewport.Width-6))
				for _, pr := range item.PRsList {
					header := fmt.Sprintf("#%-4d %s (branch %s)", pr.Number, pr.Title, pr.HeadRefName)
					fmt.Fprintf(&sb, "  %s\n    %s\n\n", th.pr.Render(titleWrapper.Render(header)), th.link.Render(pr.URL))
				}
			}
		}
	}

	m.setDetail(sb.String())
}

// detailTabNames are the repository detail tab labels, longest first.
var detailTabNames = [][4]string{
	{"Logs", "Branches", "Issues", "PRs"},
	{"Log", "Br", "Iss", "PR"},
}

// renderTabBar shows the repository detail tabs; [ and ] move between them.
// It uses the longest labels that fit budget cells, falling back to the
// active tab alone so the current tab is always named.
func (m Model) renderTabBar(budget int) string {
	for _, names := range detailTabNames {
		var sb strings.Builder
		for i, name := range names {
			if TabType(i) == m.ActiveTab {
				sb.WriteString(th.subTabActive.Render(name))
			} else {
				sb.WriteString(th.subTabInactive.Render(name))
			}
		}
		if ansi.StringWidth(sb.String()) <= budget {
			return sb.String()
		}
	}
	return th.subTabActive.Render(detailTabNames[0][m.ActiveTab])
}

// sectionRule is a heading followed by a rule filling the remaining width.
func sectionRule(title string, width int) string {
	rest := max(2, width-ansi.StringWidth(title)-1)
	return th.section.Render(title) + " " + th.divider.Render(strings.Repeat("─", rest))
}

func highlightLogLine(line string) string {
	if line == "" {
		return line
	}

	render := func(style lipgloss.Style) func(string) string {
		return func(s string) string { return style.Render(s) }
	}
	line = reTimestamp.ReplaceAllStringFunc(line, render(th.logTimestamp))
	line = reCmd.ReplaceAllStringFunc(line, render(th.logCommand))
	line = reURL.ReplaceAllStringFunc(line, render(th.link))
	line = reQuoted.ReplaceAllStringFunc(line, render(th.logQuoted))

	if strings.Contains(line, "󰄬") || strings.Contains(line, "PASS") || strings.Contains(line, "successfully") || strings.Contains(line, "Up to date") {
		line = th.fg(th.green).Render(line)
	} else if strings.Contains(line, "󰅙") || strings.Contains(line, "error") || strings.Contains(line, "Failed") || strings.Contains(line, "conflict") {
		line = th.fg(th.red).Render(line)
	}

	return line
}

func (m Model) View() string { return m.screenView() }

// renderStatusBadge renders a repository's sync state as glyph and label.
func (m Model) renderStatusBadge(item *git.RepoItem) string {
	if item.IsNew && item.Status == git.StatusPending {
		return th.status(kindUnknown, "not cloned")
	}
	kind, label := repoKind(item.Status)
	return th.status(kind, label)
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
