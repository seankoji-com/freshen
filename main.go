package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/seankoji-com/freshen/pkg/git"
	"github.com/seankoji-com/freshen/pkg/tui"
)

const Version = "1.0.0"

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

	if nonInteractiveFlag {
		runNonInteractive(targetDir, orgFlag)
		return
	}

	// Launch TUI Application
	p := tea.NewProgram(
		tui.NewModel(targetDir, orgFlag),
		tea.WithAltScreen(),
	)

	if _, err := p.Run(); err != nil {
		fmt.Printf("Error running freshen TUI: %v\n", err)
		os.Exit(1)
	}
}

func runNonInteractive(targetDir, targetOrg string) {
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

		git.SyncRepository(item)
		fmt.Printf("  ↳ Result: %s (%s)\n", item.StatusMsg, item.CurrentBranch)
		if item.DraftPRURL != "" {
			fmt.Printf("  ↳ Draft PR: %s\n", item.DraftPRURL)
		}
	}

	fmt.Printf("\nBatch sync complete.\n")
}
