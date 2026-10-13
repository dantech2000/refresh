// Package ui implements "refresh ui", the full-screen terminal UI.
package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/cmd/fleetcmd"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/contexts"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/tui"
	"github.com/dantech2000/refresh/internal/tui/backend"
)

// minInterval is the shortest time between two sweeps.
const minInterval = 10 * time.Second

type options struct {
	ios      *cmdutil.IOStreams
	aws      func(ctx context.Context) (*awsx.Provider, error)
	contexts func() (*contexts.Store, error)
	global   *cmdutil.GlobalOptions
	now      func() time.Time
	after    func(time.Duration) <-chan time.Time

	allRegions   bool
	interval     time.Duration
	allowChanges bool
	waitTimeout  time.Duration
}

// NewCmdUI returns the ui command.
func NewCmdUI(f *cmdutil.Factory) *cobra.Command {
	o := &options{ios: f.IOStreams, aws: f.AWS, contexts: f.Contexts, global: &f.Global, now: f.Now, after: f.After}
	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Open the terminal UI for the fleet (experimental)",
		Long: `Open a full-screen terminal UI (experimental): the fleet with what is out of date, each
cluster with its nodegroups and add-ons, and the readiness checks of
"cluster upgrade-check" on demand.

The UI reads the fleet with the same code as "refresh status", every
--interval and on ctrl+r. It is read-only in this version: the screens that
start a change come in a later one, and --allow-changes has no effect yet.

The UI needs a terminal of at least 100 by 24 cells. While it runs, the
standard error of refresh and of the programs it starts (such as a
kubeconfig credential plugin) goes to the null device, so their messages
cannot write over the screen. Press ? in the UI for the keys.`,
		Example: `  # The configured region
  refresh ui

  # Every region, swept every 30 seconds
  refresh ui -A --interval 30s`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.validate(); err != nil {
				return err
			}
			return o.run(cmd.Context())
		},
	}
	fleetcmd.AddAllRegionsFlag(cmd, &o.allRegions)
	cmd.Flags().DurationVar(&o.interval, "interval", time.Minute, "Time between fleet sweeps, at least 10s; ctrl+r sweeps now")
	cmd.Flags().BoolVar(&o.allowChanges, "allow-changes", false, "Let the UI start changes after their dry runs (no effect yet: the change screens are not built)")
	cmd.Flags().DurationVar(&o.waitTimeout, "wait-timeout", 0, "How long the UI follows a change it starts; 0 means the default of the command that makes the change (no effect yet)")
	return cmd
}

func (o *options) validate() error {
	if err := fleetcmd.ValidateSweepFlags(o.global, o.allRegions); err != nil {
		return err
	}
	if o.interval < minInterval {
		return fmt.Errorf("invalid --interval %s: must be at least %s", o.interval, minInterval)
	}
	if o.waitTimeout < 0 {
		return fmt.Errorf("invalid --wait-timeout %s: must be 0 or more", o.waitTimeout)
	}
	if !o.ios.InTTY || !o.ios.OutTTY {
		return errors.New("refresh ui needs an interactive terminal on stdin and stdout")
	}
	return nil
}

func (o *options) run(ctx context.Context) error {
	provider, err := o.aws(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	b := backend.New(ctx, provider, backend.Options{
		Regions:      fleetcmd.SweepRegions(o.global, provider.Region(), o.allRegions),
		Interval:     o.interval,
		AllowChanges: o.allowChanges,
		ReadTimeout:  o.global.Timeout,
		Profile:      o.global.Profile,
		Context:      o.contextName(),
		SweepError: func(ctx context.Context, res *fleet.SweepResult) error {
			return fleetcmd.SweepError(ctx, provider, res)
		},
		Now:   o.now,
		After: o.after,
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.Run()
	}()
	defer func() {
		cancel()
		<-done
	}()

	// A credential plugin, or anything else that writes to file
	// descriptor 2, must not write over the screen.
	restore, err := detachStderr()
	if err != nil {
		return err
	}
	defer restore()

	m := tui.New(ctx, b, o.ios.Theme())
	opts := []tea.ProgramOption{tea.WithContext(ctx), tea.WithInput(o.ios.In), tea.WithOutput(o.ios.Out)}
	if !o.ios.ColorEnabled() {
		opts = append(opts, tea.WithColorProfile(colorprofile.ASCII))
	}
	_, err = tea.NewProgram(m, opts...).Run()
	if err != nil && ctx.Err() != nil {
		// The session was interrupted; main says so.
		return nil
	}
	return err
}

// contextName is the active refresh context, for the top bar, or "".
func (o *options) contextName() string {
	store, err := o.contexts()
	if err != nil {
		return ""
	}
	file, err := store.Load()
	if err != nil {
		return ""
	}
	name, _, ok, err := file.Active(os.Getenv(contexts.EnvContext))
	if err != nil || !ok {
		return ""
	}
	return name
}
