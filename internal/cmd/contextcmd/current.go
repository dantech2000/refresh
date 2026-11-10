package contextcmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/cmdutil"
)

type currentOptions struct {
	f   *cmdutil.Factory
	ios *cmdutil.IOStreams
}

func newCmdCurrent(f *cmdutil.Factory) *cobra.Command {
	o := &currentOptions{f: f, ios: f.IOStreams}
	return &cobra.Command{
		Use:   "current",
		Short: "Print the active context",
		Long: `Print the active context: its name, cluster, region, and profile.

The active context is the one that REFRESH_CONTEXT names, or else the one
that "refresh context use" selected. A REFRESH_CONTEXT that names no saved context is
an error. When no context is active, the command prints a hint on stderr and
nothing on stdout.

Exit codes:
  0  Done, also when no context is active.
  1  An error.`,
		Example: `  refresh context current

  # Use another context for one command
  REFRESH_CONTEXT=stage refresh context current`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return o.run() },
	}
}

func (o *currentOptions) run() error {
	name, c, ok, err := cmdutil.ActiveContext(o.f)
	if err != nil {
		return err
	}
	if !ok {
		// A hint, not data: it goes to stderr and stdout stays empty.
		_, err := fmt.Fprintln(o.ios.ErrOut, "No active context. Set one with: refresh context use <name>")
		return err
	}
	_, err = fmt.Fprintf(o.ios.Out, "%s  cluster=%s  region=%s  profile=%s\n",
		name, c.Cluster, orDash(c.Region), orDash(c.Profile))
	return err
}
