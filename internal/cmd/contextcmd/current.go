package contextcmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/contexts"
)

type currentOptions struct {
	io    *cmdutil.IOStreams
	store func() (*contexts.Store, error)

	envContext string
}

// NewCmdCurrent returns the "current" command.
func NewCmdCurrent(f *cmdutil.Factory) *cobra.Command {
	o := &currentOptions{io: f.IOStreams, store: f.Contexts}
	return &cobra.Command{
		Use:   "current",
		Short: "Print the active context",
		Long: `Print the active context: its name, cluster, region, and profile.

The active context is the one that REFRESH_CONTEXT names, or else the one
that "refresh use" selected. A REFRESH_CONTEXT that names no saved context is
an error. When no context is active, the command prints a hint on stderr and
nothing on stdout.`,
		Example: `  refresh current

  # Use another context for one command
  REFRESH_CONTEXT=stage refresh current`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			o.envContext = envContext()
			return o.run()
		},
	}
}

func (o *currentOptions) run() error {
	_, file, err := loadFile(o.store)
	if err != nil {
		return err
	}
	name, c, ok, err := file.Active(o.envContext)
	if err != nil {
		return err
	}
	if !ok {
		// A hint, not data: it goes to stderr and stdout stays empty.
		_, err := fmt.Fprintln(o.io.ErrOut, "No active context. Set one with: refresh use <name>")
		return err
	}
	_, err = fmt.Fprintf(o.io.Out, "%s  cluster=%s  region=%s  profile=%s\n",
		name, c.Cluster, orDash(c.Region), orDash(c.Profile))
	return err
}
