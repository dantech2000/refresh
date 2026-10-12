package commands

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	docs "github.com/urfave/cli-docs/v3"
	"github.com/urfave/cli/v3"
)

const (
	// ManPageFileMode defines the file permissions for the man page file
	ManPageFileMode = 0644
	// ManPageDirMode defines the directory permissions for man page directories
	ManPageDirMode = 0755
)

func ManPageCommand() *cli.Command {
	return &cli.Command{
		Name:    "install-man",
		Aliases: []string{"install-manpage"},
		Usage:   "Install the man page for refresh",
		Description: `Install the refresh man page for offline documentation access.
		
This command generates the man page from the CLI definition and installs it
to a user-accessible directory ($HOME/.local/share/man/man1 by default).
No sudo privileges required - works seamlessly across macOS, Linux, and Unix systems.`,
		Action: installManPage,
	}
}

func installManPage(ctx context.Context, cmd *cli.Command) error {
	// Generate the man page content (ToMan moved to urfave/cli-docs in v3)
	manContent, err := docs.ToMan(cmd.Root())
	if err != nil {
		return fmt.Errorf("failed to generate man page: %w", err)
	}

	// Determine the man page installation directory
	manDir := getManPageDir()
	manPath := filepath.Join(manDir, "refresh.1")

	// Verify we can write to the directory
	if !isWritableDir(manDir) {
		return fmt.Errorf("cannot write to directory %s: permission denied", manDir)
	}

	// Write the man page file
	if err := os.WriteFile(manPath, []byte(manContent), ManPageFileMode); err != nil {
		return fmt.Errorf("failed to write man page to %s: %w", manPath, err)
	}

	// Success message with setup instructions
	fmt.Printf("Man page installed successfully to: %s\n", manPath)

	// Check if the directory is already in MANPATH
	manParentDir := filepath.Dir(manDir) // Remove /man1 to get the base man directory
	if !isInManPath(ctx, manParentDir) {
		fmt.Printf("\nTo make the man page accessible, add the following to your shell profile:\n")
		fmt.Printf("  export MANPATH=\"%s:$MANPATH\"\n", manParentDir)
		fmt.Printf("\nOr run temporarily:\n")
		fmt.Printf("  MANPATH=\"%s:$MANPATH\" man refresh\n", manParentDir)
	} else {
		fmt.Println("You can now use 'man refresh' to view the documentation.")
	}

	// Update man database if available
	updateManDB(ctx, manParentDir)

	return nil
}

func getManPageDir() string {
	// Priority list of user-writable directories
	homeDir := os.Getenv("HOME")
	if homeDir == "" {
		// Fallback to current directory if HOME is not set
		homeDir = "."
	}
	userDirs := []string{
		filepath.Join(homeDir, ".local", "share", "man", "man1"),
		filepath.Join(homeDir, ".local", "man", "man1"),
		filepath.Join(homeDir, "man", "man1"),
	}

	// Check user directories first (writable without sudo)
	for _, dir := range userDirs {
		if isWritableDir(dir) {
			return dir
		}
	}

	// Default to the standard user-local directory
	return filepath.Join(homeDir, ".local", "share", "man", "man1")
}

func isWritableDir(dir string) bool {
	// Create parent directories if they don't exist
	if err := os.MkdirAll(dir, ManPageDirMode); err != nil {
		return false
	}

	// Test write permission by creating a temporary file with unique name
	testFile := filepath.Join(dir, fmt.Sprintf(".write_test_%d", time.Now().UnixNano()))
	file, err := os.Create(testFile)
	if err != nil {
		return false
	}
	_ = file.Close() // Ignore close error for test file

	// Clean up temporary file with error logging
	if err := os.Remove(testFile); err != nil {
		log.Printf("Warning: failed to clean up temporary file %s: %v", testFile, err)
	}
	return true
}

func isInManPath(ctx context.Context, dir string) bool {
	// Get the current MANPATH
	cmd := exec.CommandContext(ctx, "manpath")
	output, err := cmd.Output()
	if err != nil {
		return false
	}

	manpathStr := strings.TrimSpace(string(output))
	paths := strings.Split(manpathStr, ":")

	for _, path := range paths {
		if strings.TrimSpace(path) == dir {
			return true
		}
	}
	return false
}

// updateManDB refreshes the man index for manDir (the base man directory,
// e.g. ~/.local/share/man), where install-man wrote the page. It is optional,
// so a missing tool or a failure is ignored.
func updateManDB(ctx context.Context, manDir string) {
	name, args := manDBCommand(runtime.GOOS, manDir)
	if name == "" {
		return
	}
	if _, err := exec.LookPath(name); err != nil {
		return
	}
	_ = exec.CommandContext(ctx, name, args...).Run() //nolint:gosec // G204: name is makewhatis or mandb from manDBCommand; errors are ignored, this is optional
}

// manDBCommand returns the command that indexes manDir on goos, or "" when
// there is none. makewhatis (macOS) and mandb (Linux) both take the man
// directory as an argument; without it mandb rebuilds only the system
// databases, which a user cannot write and which do not include manDir.
func manDBCommand(goos, manDir string) (string, []string) {
	switch goos {
	case "darwin":
		return "makewhatis", []string{manDir}
	case "linux":
		return "mandb", []string{"-q", manDir}
	}
	return "", nil
}
