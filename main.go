package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/seankoji-com/freshen/pkg/git"
	"github.com/seankoji-com/freshen/pkg/tui"
)

// shutdownWait is how long main() waits for in-flight background git
// operations to stop after the TUI exits, before returning anyway.
const shutdownWait = 5 * time.Second

var Version = "1.0.0"

// validatePrerequisites checks that freshen has access to required tools.
// It exits with status 1 and writes to stderr if any critical prerequisite is missing.
// The gh auth check is skipped if versionFlag is true (version printing needs nothing).
func validatePrerequisites(versionFlag bool) {
	// Check for git on PATH
	_, err := exec.LookPath("git")
	if err != nil {
		fmt.Fprintln(os.Stderr, "freshen requires git on PATH.")
		os.Exit(1)
	}

	// Skip gh check if printing version (it needs nothing)
	if versionFlag {
		return
	}

	// Check for authenticated GitHub CLI
	cmd := exec.Command("gh", "auth", "status")
	err = cmd.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "freshen requires an authenticated GitHub CLI. Run: gh auth login.")
		os.Exit(1)
	}
}

func main() {
	var (
		dirFlag            string
		orgFlag            string
		nonInteractiveFlag bool
		versionFlag        bool
	)

	homeDir, _ := os.UserHomeDir()
	defaultReposDir := filepath.Join(homeDir, "repos")

	flag.StringVar(&dirFlag, "dir", defaultReposDir, "Target directory containing repository subfolders")
	flag.StringVar(&orgFlag, "org", "seankoji-com", "Target GitHub Organization")
	flag.BoolVar(&nonInteractiveFlag, "non-interactive", false, "Run in non-interactive terminal batch mode")
	flag.BoolVar(&nonInteractiveFlag, "y", false, "Run in non-interactive terminal batch mode (shorthand)")
	flag.BoolVar(&versionFlag, "version", false, "Show freshen version")
	flag.BoolVar(&versionFlag, "v", false, "Show freshen version (shorthand)")

	flag.Parse()

	validatePrerequisites(versionFlag)

	if versionFlag {
		fmt.Printf("freshen v%s\n", Version)
		os.Exit(0)
	}

	targetDir := dirFlag
	if targetDir == "" {
		targetDir = defaultReposDir
	}

	absDir, err := filepath.Abs(targetDir)
	if err == nil {
		targetDir = absDir
	}

	// ctx is cancelled on Ctrl+C/SIGTERM/terminal-close (SIGHUP), and again
	// explicitly on quit — it aborts any in-flight git operations so nothing
	// (like the auto-stash sync) keeps running after freshen exits.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	if nonInteractiveFlag {
		runNonInteractive(ctx, targetDir, orgFlag)
		return
	}

	// Launch TUI Application
	var bgWG sync.WaitGroup
	p := tea.NewProgram(
		tui.NewModel(targetDir, orgFlag, ctx, stop, &bgWG),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	// If the process receives a signal instead of a keypress, stop the TUI too.
	go func() {
		<-ctx.Done()
		p.Quit()
	}()

	_, runErr := p.Run()
	stop() // cancel any in-flight sync regardless of how we got here

	waitForBackgroundWork(&bgWG)

	if runErr != nil {
		fmt.Printf("Error running freshen TUI: %v\n", runErr)
		os.Exit(1)
	}
}

// waitForBackgroundWork blocks until all tracked background git operations
// finish, or shutdownWait elapses — guaranteeing the process actually exits
// instead of leaving orphaned git/gh subprocesses behind.
func waitForBackgroundWork(wg *sync.WaitGroup) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(shutdownWait):
		fmt.Println("Warning: background sync did not stop in time; exiting anyway.")
	}
}

func runNonInteractive(ctx context.Context, targetDir, targetOrg string) {
	fmt.Printf("======================================================================\n")
	fmt.Printf("                   FRESHEN REPOSITORY WORKFLOW                        \n")
	fmt.Printf("======================================================================\n")
	fmt.Printf("Target Directory: %s\n", targetDir)
	fmt.Printf("Target Org:       %s\n\n", targetOrg)

	// Fetch org repos
	orgRepos, err := git.FetchOrgRepos(targetOrg)
	if err != nil {
		fmt.Printf("Warning: failed to query GitHub org: %v\n", err)
	}

	// Delete archived repos & clone missing active repos
	for _, ghRepo := range orgRepos {
		localDir := git.GetLocalDirName(ghRepo.Name)
		localPath := filepath.Join(targetDir, localDir)

		if ghRepo.IsArchived {
			if _, statErr := os.Stat(localPath); statErr == nil {
				fmt.Printf("↳ Deleting local directory for archived repo '%s'...\n", localDir)
				_ = git.DeleteLocalRepo(localPath)
			}
		} else {
			if _, statErr := os.Stat(localPath); os.IsNotExist(statErr) {
				fmt.Printf("↳ Cloning new active repo '%s'...\n", ghRepo.Name)
				_ = git.CloneRepo(targetOrg, ghRepo.Name, localPath)
			}
		}
	}

	// Process all remaining active local repos
	entries, _ := git.ScanLocalDirectory(targetDir)
	for idx, name := range entries {
		path := filepath.Join(targetDir, name)
		if !git.IsGitRepo(path) {
			continue
		}

		fmt.Printf("[%d/%d] Processing %s...\n", idx+1, len(entries), name)
		item := &git.RepoItem{
			Name:       name,
			GHRepoName: git.GetGHRepoName(name),
			Path:       path,
			Logs:       make([]string, 0),
		}

		git.SyncRepository(ctx, item)
		fmt.Printf("  ↳ Result: %s (%s)\n", item.StatusMsg, item.CurrentBranch)
		if item.DraftPRURL != "" {
			fmt.Printf("  ↳ Draft PR: %s\n", item.DraftPRURL)
		}
	}

	fmt.Printf("\nBatch sync complete.\n")
}
