package contextcmd

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/contexts"
)

type useOptions struct {
	ios   *cmdutil.IOStreams
	store func() (*contexts.Store, error)

	name string // empty means ask

	named      bool // a name was given, also when it is blank
	envContext string
}

func newCmdUse(f *cmdutil.Factory) *cobra.Command {
	o := &useOptions{ios: f.IOStreams, store: f.Contexts}
	return &cobra.Command{
		Use:   "use [context|-]",
		Short: "Switch the active context",
		Long: `Switch the active context. Later commands use its cluster, region, and
profile when you do not give them.

With "-", switch back to the context that was active before. Without an
argument, list the saved contexts and ask for one.

The flags and the positional of a command still beat the active context.
REFRESH_CONTEXT selects a context for one shell and beats the context that
this command saves.`,
		Example: `  refresh context use prod

  # Switch back to the context before
  refresh context use -

  # Choose from the list
  refresh context use`,
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeNames(f.Contexts),
		RunE: func(cmd *cobra.Command, args []string) error {
			o.complete(args)
			if err := o.validate(); err != nil {
				return err
			}
			return o.run(cmd.Context())
		},
	}
}

func (o *useOptions) complete(args []string) {
	if len(args) == 1 {
		o.name, o.named = contextName(args[0]), true
	}
	o.envContext = envContext()
}

func (o *useOptions) validate() error {
	if o.named {
		return validateName(o.name)
	}
	return nil
}

func (o *useOptions) run(ctx context.Context) error {
	store, file, err := loadFile(o.store)
	if err != nil {
		return err
	}
	if len(file.Contexts) == 0 {
		return errors.New("no contexts saved; add one with: refresh context add <name> --cluster <cluster> [--region <region>] [--profile <profile>]")
	}

	name := o.name
	if name == "" {
		if name, err = o.pick(ctx, file); err != nil {
			return err
		}
		// An interrupt during the question must not change the file.
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if err := file.Use(name); err != nil {
		return err
	}
	if err := store.Save(file); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(o.ios.Out, "Switched to context %q (%s)\n", file.Current, describe(file.Contexts[file.Current])); err != nil {
		return err
	}
	// REFRESH_CONTEXT beats the saved context, so commands in this shell
	// still use the context it names. Say so; "Switched" alone misleads.
	if o.envContext != "" && o.envContext != file.Current {
		_, err = fmt.Fprintf(o.ios.ErrOut,
			"warning: %s=%s overrides the saved context in this shell; unset %s to use %q here\n",
			contexts.EnvContext, o.envContext, contexts.EnvContext, file.Current)
	}
	return err
}

// pick lists the contexts on stderr and reads a number or a name from stdin.
func (o *useOptions) pick(ctx context.Context, file *contexts.File) (string, error) {
	names := file.Names()
	// Mark the context that commands use now, as "context list" does. An
	// unknown REFRESH_CONTEXT marks none; that error is not for this step.
	active, _, _, _ := file.Active(o.envContext) //nolint:dogsled // only the name matters for the marker
	if _, err := fmt.Fprintln(o.ios.ErrOut, "Available contexts:"); err != nil {
		return "", err
	}
	for i, name := range names {
		marker := " "
		if name == active {
			marker = "*"
		}
		c := file.Contexts[name]
		if _, err := fmt.Fprintf(o.ios.ErrOut, "  %s %d) %-20s cluster=%s region=%s\n", marker, i+1, name, c.Cluster, orDash(c.Region)); err != nil {
			return "", err
		}
	}
	if _, err := fmt.Fprint(o.ios.ErrOut, "Select context [number or name]: "); err != nil {
		return "", err
	}
	line, err := cmdutil.ReadLine(ctx, o.ios)
	if err != nil {
		return "", err
	}
	if line == "" {
		return "", errors.New("no selection made")
	}
	if i, err := strconv.Atoi(line); err == nil {
		if i < 1 || i > len(names) {
			return "", fmt.Errorf("selection %d out of range", i)
		}
		return names[i-1], nil
	}
	return line, nil
}
