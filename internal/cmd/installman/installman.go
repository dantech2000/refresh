// Package installman implements "refresh install-man".
package installman

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"

	"github.com/dantech2000/refresh/internal/cmdutil"
)

type options struct {
	io      *cmdutil.IOStreams
	version string

	// home is the home directory of the user.
	home string
	// goos selects the tool that indexes the man pages.
	goos string
	// output runs a program and returns what it wrote to stdout.
	output func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// NewCmdInstallMan returns the install-man command.
func NewCmdInstallMan(f *cmdutil.Factory) *cobra.Command {
	o := &options{io: f.IOStreams, version: f.Build.Version, goos: runtime.GOOS, output: output}
	return &cobra.Command{
		Use:     "install-man",
		Aliases: []string{"install-manpage"},
		Short:   "Install the man pages of refresh",
		Long: `Write the man pages of refresh, one for each command, to a directory of
your own. The command needs no sudo.

The directory is the first of these that can be written:
~/.local/share/man/man1, ~/.local/man/man1, ~/man/man1. When "manpath" does
not list it, the command prints the MANPATH line for your shell profile.`,
		Example: `  refresh install-man
  man refresh
  man refresh-nodegroup-update`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// With no HOME the pages go under the current directory.
			o.home = os.Getenv("HOME")
			if o.home == "" {
				o.home = "."
			}
			return o.run(cmd.Context(), cmd.Root())
		},
	}
}

func (o *options) run(ctx context.Context, root *cobra.Command) error {
	dir := manDir(o.home)
	if !isWritableDir(dir) {
		return fmt.Errorf("cannot write to directory %s: permission denied", dir)
	}
	header := &doc.GenManHeader{
		Title:   "REFRESH",
		Section: "1",
		Source:  "refresh " + o.version,
		Manual:  "refresh manual",
	}
	// Cobra writes with os.Create, which follows a symbolic link and leaves
	// part of the pages when it fails. GenerateFiles prevents both, which
	// matters when the command runs with more rights than the user has.
	err := cmdutil.GenerateFiles(dir, func(tmp string) error {
		return doc.GenManTree(root, header, tmp)
	})
	if err != nil {
		return fmt.Errorf("writing the man pages to %s: %w", dir, err)
	}

	out := o.io.Out
	if _, err := fmt.Fprintf(out, "Man pages installed in: %s\n", dir); err != nil {
		return err
	}
	base := filepath.Dir(dir) // without man1
	if o.inManPath(ctx, base) {
		_, err = fmt.Fprintln(out, "You can now use 'man refresh' to view the documentation.")
	} else {
		_, err = fmt.Fprintf(out, `
To make the man pages accessible, add the following to your shell profile:
  export MANPATH="%[1]s:$MANPATH"

Or run temporarily:
  MANPATH="%[1]s:$MANPATH" man refresh
`, base)
	}
	if err != nil {
		return err
	}

	// The index is optional, so a missing tool or a failure is ignored.
	if name, args := manDBCommand(o.goos, base); name != "" {
		_, _ = o.output(ctx, name, args...)
	}
	return nil
}

// manDir returns the man1 directory to install into: the first of the
// user's man directories that can be written, or the standard one.
func manDir(home string) string {
	dirs := []string{
		filepath.Join(home, ".local", "share", "man", "man1"),
		filepath.Join(home, ".local", "man", "man1"),
		filepath.Join(home, "man", "man1"),
	}
	for _, dir := range dirs {
		if isWritableDir(dir) {
			return dir
		}
	}
	return dirs[0]
}

// isWritableDir makes dir when it is missing and reports whether a file can
// be written in it.
func isWritableDir(dir string) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: man reads the pages as any user
		return false
	}
	f, err := os.CreateTemp(dir, ".write_test_*")
	if err != nil {
		return false
	}
	_ = f.Close()
	_ = os.Remove(f.Name())
	return true
}

// inManPath reports whether the manpath program lists dir.
func (o *options) inManPath(ctx context.Context, dir string) bool {
	out, err := o.output(ctx, "manpath")
	if err != nil {
		return false
	}
	for p := range strings.SplitSeq(strings.TrimSpace(string(out)), ":") {
		if strings.TrimSpace(p) == dir {
			return true
		}
	}
	return false
}

// manDBCommand returns the command that indexes manDir on goos, or "" when
// there is none. makewhatis (macOS) and mandb (Linux) both take the man
// directory as an argument. Without it mandb rebuilds only the system
// databases, which a user cannot write and which do not include manDir.
func manDBCommand(goos, manDir string) (name string, args []string) {
	switch goos {
	case "darwin":
		return "makewhatis", []string{manDir}
	case "linux":
		return "mandb", []string{"-q", manDir}
	}
	return "", nil
}

// output runs the program name and returns its stdout.
func output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output() //nolint:gosec // G204: name is manpath, makewhatis, or mandb; never user input
}
