package commands

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/urfave/cli/v2"
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

func installManPage(c *cli.Context) error {
	// Generate the man page content
	app := c.App
	manContent, err := app.ToMan()
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
	if err := os.WriteFile(manPath, []byte(manContent), 0644); err != nil {
		return fmt.Errorf("failed to write man page to %s: %w", manPath, err)
	}

	// Success message with setup instructions
	fmt.Printf("Man page installed successfully to: %s\n", manPath)

	// Check if the directory is already in MANPATH
	manParentDir := filepath.Dir(manDir) // Remove /man1 to get the base man directory
	if !isInManPath(manParentDir) {
		fmt.Printf("\nTo make the man page accessible, add the following to your shell profile:\n")
		fmt.Printf("  export MANPATH=\"%s:$MANPATH\"\n", manParentDir)
		fmt.Printf("\nOr run temporarily:\n")
		fmt.Printf("  MANPATH=\"%s:$MANPATH\" man refresh\n", manParentDir)
	} else {
		fmt.Println("You can now use 'man refresh' to view the documentation.")
	}

	// Update man database if available
	updateManDB()

	return nil
}

func getManPageDir() string {
	// Priority list of user-writable directories
	homeDir := os.Getenv("HOME")
	userDirs := []string{
		filepath.Join(homeDir, ".local/share/man/man1"),
		filepath.Join(homeDir, ".local/man/man1"),
		filepath.Join(homeDir, "man/man1"),
	}

	// Check user directories first (writable without sudo)
	for _, dir := range userDirs {
		if isWritableDir(dir) {
			return dir
		}
	}

	// Default to the standard user-local directory
	return filepath.Join(homeDir, ".local/share/man/man1")
}

func isWritableDir(dir string) bool {
	// Create parent directories if they don't exist
	if err := os.MkdirAll(dir, 0755); err != nil {
		return false
	}

	// Test write permission by creating a temporary file
	testFile := filepath.Join(dir, ".write_test")
	file, err := os.Create(testFile)
	if err != nil {
		return false
	}
	_ = file.Close() // Ignore close error for test file
	_ = os.Remove(testFile) // Ignore remove error for test file
	return true
}

func isInManPath(dir string) bool {
	// Get the current MANPATH
	cmd := exec.Command("manpath")
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

func updateManDB() {
	switch runtime.GOOS {
	case "darwin":
		// On macOS, try to update the man database
		if _, err := exec.LookPath("makewhatis"); err == nil {
			cmd := exec.Command("makewhatis", "/usr/local/share/man")
			_ = cmd.Run() // Ignore errors, this is optional
		}
	case "linux":
		// On Linux, try to update the man database
		if _, err := exec.LookPath("mandb"); err == nil {
			cmd := exec.Command("mandb", "-q")
			_ = cmd.Run() // Ignore errors, this is optional
		}
	}
}
