package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/seankoji-com/freshen/pkg/config"
	"github.com/seankoji-com/freshen/pkg/git"
	"github.com/seankoji-com/freshen/pkg/tui"
)

// shutdownWait is how long main() waits for in-flight background git
// operations to stop after the TUI exits, before returning anyway.
const shutdownWait = 5 * time.Second

var Version = "1.0.0"

// aliasFlags implements flag.Value for a repeatable --alias local=remote flag,
// letting a user add to or override the built-in repo alias pairs in
// pkg/git without editing source.
type aliasFlags []string

func (a *aliasFlags) String() string { return strings.Join(*a, ",") }

func (a *aliasFlags) Set(value string) error {
	local, remote, err := parseAlias(value)
	if err != nil {
		return fmt.Errorf("invalid --alias value %q: %w", value, err)
	}
	if err := git.AddAlias(local, remote); err != nil {
		return fmt.Errorf("invalid --alias value %q: %w", value, err)
	}
	*a = append(*a, value)
	return nil
}

func parseAlias(value string) (string, string, error) {
	local, remote, ok := strings.Cut(value, "=")
	local = strings.TrimSpace(local)
	remote = strings.TrimSpace(remote)
	if !ok || local == "" || remote == "" {
		return "", "", fmt.Errorf("expected local=remote")
	}
	return local, remote, nil
}

func applyConfigAliases(aliases []string) error {
	for _, alias := range aliases {
		local, remote, err := parseAlias(alias)
		if err != nil {
			return fmt.Errorf("invalid alias %q: %w", alias, err)
		}
		if err := git.AddAlias(local, remote); err != nil {
			return fmt.Errorf("invalid alias %q: %w", alias, err)
		}
	}
	return nil
}

func configConcurrency(value int) int {
	if value > 0 {
		return value
	}
	return 4
}

// validatePrerequisites checks that freshen has access to required tools.
// It exits with status 1 and writes to stderr if any critical prerequisite is missing.
// Both the git-on-PATH check and the gh auth check are skipped if versionFlag
// is true (version printing needs nothing).
func validatePrerequisites(versionFlag, needsGitHub bool) {
	// Skip all checks if printing version (it needs nothing)
	if versionFlag {
		return
	}

	// Check for git on PATH
	_, err := exec.LookPath("git")
	if err != nil {
		fmt.Fprintln(os.Stderr, "freshen requires git on PATH.")
		os.Exit(1)
	}

	if !needsGitHub {
		return
	}
	// Check for authenticated GitHub CLI, bounded so a hung network doesn't
	// block indefinitely before any logging is configured.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "auth", "status")
	err = cmd.Run()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			fmt.Fprintln(os.Stderr, "freshen timed out checking GitHub CLI auth status (network issue?). Check your connection and try again.")
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "freshen requires an authenticated GitHub CLI. Run: gh auth login.")
		os.Exit(1)
	}
}

func main() {
	var (
		dirFlag            string
		orgFlag            string
		ownerFlag          string
		concurrencyFlag    int
		nonInteractiveFlag bool
		versionFlag        bool
		deleteArchivedFlag bool
		aliasFlag          aliasFlags
	)

	homeDir, _ := os.UserHomeDir()
	defaultReposDir := filepath.Join(homeDir, "repos")
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "freshen config: %v\n", err)
		os.Exit(1)
	}
	if cfg.Workspace != "" {
		defaultReposDir = cfg.Workspace
	}
	defaultOwner := cfg.Owner
	if value := os.Getenv("FRESHEN_OWNER"); value != "" {
		defaultOwner = value
	}
	if value := os.Getenv("FRESHEN_ORG"); value != "" {
		defaultOwner = value
	}

	flag.StringVar(&dirFlag, "dir", defaultReposDir, "Target directory containing repository subfolders")
	flag.StringVar(&ownerFlag, "owner", defaultOwner, "Optional GitHub user or organization")
	flag.StringVar(&orgFlag, "org", "", "Deprecated alias for --owner")
	flag.IntVar(&concurrencyFlag, "concurrency", configConcurrency(cfg.Concurrency), "Number of concurrent repository sync operations")
	flag.BoolVar(&nonInteractiveFlag, "non-interactive", false, "Run in non-interactive terminal batch mode")
	flag.BoolVar(&nonInteractiveFlag, "y", false, "Run in non-interactive terminal batch mode (shorthand)")
	flag.BoolVar(&versionFlag, "version", false, "Show freshen version")
	flag.BoolVar(&versionFlag, "v", false, "Show freshen version (shorthand)")
	flag.BoolVar(&deleteArchivedFlag, "delete-archived", false, "Allow archived repository deletion in non-interactive mode")
	flag.Var(&aliasFlag, "alias", "Repeatable repo alias mapping in the form local=remote, adding to/overriding the built-in defaults")

	flag.Parse()

	if versionFlag {
		fmt.Printf("freshen v%s\n", Version)
		os.Exit(0)
	}
	if err := applyConfigAliases(cfg.Aliases); err != nil {
		fmt.Fprintf(os.Stderr, "freshen config: %v\n", err)
		os.Exit(1)
	}
	if orgFlag != "" {
		ownerFlag = orgFlag
	}
	if cfg.Workspace == "" && !nonInteractiveFlag && dirFlag == defaultReposDir {
		cfg, err = runFirstSetup(defaultReposDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "freshen: %v\n", err)
			os.Exit(1)
		}
		defaultReposDir = cfg.Workspace
	}
	if ownerFlag == "" && !nonInteractiveFlag {
		promptErr := ""
		for {
			owner, err := runOwnerPrompt(promptErr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "freshen: %v\n", err)
				os.Exit(1)
			}
			if owner == "" {
				break
			}
			if !ownerExists(owner) {
				promptErr = fmt.Sprintf("%q wasn't found as a GitHub user or organization.", owner)
				continue
			}
			ownerFlag = owner
			cfg.Owner = owner
			if err := config.Save(cfg); err != nil {
				fmt.Fprintf(os.Stderr, "freshen config: %v\n", err)
				os.Exit(1)
			}
			break
		}
	}
	validatePrerequisites(false, ownerFlag != "")

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
		runNonInteractive(ctx, targetDir, ownerFlag, deleteArchivedFlag)
		return
	}

	// Launch TUI Application
	logFile := configureTUILogging()
	if logFile != nil {
		defer logFile.Close()
	}

	var bgWG sync.WaitGroup
	p := tea.NewProgram(
		tui.NewModel(targetDir, ownerFlag, concurrencyFlag, ctx, stop, &bgWG),
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

// logPath returns the file the TUI writes its slog output to.
func logPath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "freshen", "freshen.log")
}

// configureTUILogging redirects slog away from stderr for the interactive run.
//
// The TUI owns the terminal's alternate screen. Anything else written to
// stdout/stderr while it is running paints over the frame and desynchronises
// Bubble Tea's line-diffing renderer, which then repaints rows in the wrong
// place — the duplicated repo rows and stacked panel headers. Logs go to a file
// instead; set FRESHEN_LOG_LEVEL (debug|info|warn|error) to change verbosity.
//
// Returns the open log file so the caller can close it, or nil when logging
// was discarded.
func configureTUILogging() *os.File {
	var level slog.Level
	if err := level.UnmarshalText([]byte(strings.ToLower(os.Getenv("FRESHEN_LOG_LEVEL")))); err != nil {
		level = slog.LevelInfo
	}

	path := logPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: level})))
			return f
		}
	}

	// No writable log file — discard rather than corrupt the display.
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	return nil
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

func runNonInteractive(ctx context.Context, targetDir, targetOrg string, deleteArchived bool) {
	fmt.Printf("======================================================================\n")
	fmt.Printf("                   FRESHEN REPOSITORY WORKFLOW                        \n")
	fmt.Printf("======================================================================\n")
	fmt.Printf("Target Directory: %s\n", targetDir)
	fmt.Printf("GitHub Owner:     %s\n\n", targetOrg)
	if targetOrg == "" {
		fmt.Println("No GitHub owner configured; processing local repositories only.")
	}

	// Fetch org repos
	var orgRepos []git.GHRepoInfo
	var err error
	if targetOrg != "" {
		orgRepos, err = git.FetchOrgRepos(targetOrg)
	}
	if err != nil {
		fmt.Printf("Warning: failed to query GitHub org: %v\n", err)
	}

	// Delete archived repos & clone missing active repos
	for _, ghRepo := range orgRepos {
		localDir, ok := git.GetLocalDirName(ghRepo.Name)
		if !ok {
			// An unusable name must never be joined onto targetDir: the join would
			// resolve to targetDir itself and hand the whole repos root to RemoveAll.
			fmt.Printf("Warning: skipping repo '%s': no safe local directory name\n", ghRepo.Name)
			continue
		}
		localPath := filepath.Join(targetDir, localDir)

		if ghRepo.IsArchived && deleteArchived {
			if _, statErr := os.Stat(localPath); statErr == nil {
				fmt.Printf("↳ Deleting local directory for archived repo '%s'...\n", localDir)
				_ = git.DeleteLocalRepo(targetDir, localPath)
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

		// Explicit batch mode (--non-interactive/-y): keep today's full
		// behavior, same as [a]/[r] in the interactive TUI.
		git.SyncRepository(ctx, item, nil, false)
		fmt.Printf("  ↳ Result: %s (%s)\n", item.StatusMsg, item.CurrentBranch)
		if item.DraftPRURL != "" {
			fmt.Printf("  ↳ Draft PR: %s\n", item.DraftPRURL)
		}
	}

	fmt.Printf("\nBatch sync complete.\n")
}
