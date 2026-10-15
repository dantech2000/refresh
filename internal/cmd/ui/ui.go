// Package ui implements "refresh ui", the full-screen terminal UI.
package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/change"
	"github.com/dantech2000/refresh/internal/cmd/fleetcmd"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/contexts"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/tui"
	"github.com/dantech2000/refresh/internal/tui/backend"
	"github.com/dantech2000/refresh/internal/tui/state"
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
	// deps builds the dependencies of a change from the provider.
	deps func(provider *awsx.Provider) change.Deps

	allRegions     bool
	interval       time.Duration
	allowChanges   bool
	waitTimeout    time.Duration
	waitTimeoutSet bool

	// profileInEffect returns the AWS profile of the run, and profile
	// holds it.
	profileInEffect func() (string, error)
	profile         string
}

// NewCmdUI returns the ui command.
func NewCmdUI(f *cmdutil.Factory) *cobra.Command {
	o := &options{ios: f.IOStreams, aws: f.AWS, contexts: f.Contexts, global: &f.Global, now: f.Now, after: f.After,
		profileInEffect: func() (string, error) { return cmdutil.ProfileInEffect(f) },
		deps: func(provider *awsx.Provider) change.Deps {
			return change.Deps{AWS: provider, Fetch: &fleet.Fetcher{Now: f.Now}, Kube: f.Kube, Now: f.Now, After: f.After, AfterFunc: f.AfterFunc, Tick: f.Tick}
		}}
	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Open the terminal UI for the fleet (experimental)",
		Long: `Open a full-screen terminal UI (experimental): the fleet with what is out
of date, each cluster with its nodegroups and add-ons, the readiness checks
of "cluster upgrade-check" on demand, and the changes of the cluster
commands with their dry runs, health gates, and live progress.

The UI reads the fleet with the same code as "refresh status", every
--interval and on ctrl+r. The change keys (p, a, U, B) run the dry run of
"nodegroup update", "addon update", "cluster upgrade", and "cluster
rollback" and show the plan with the equivalent command. Without
--allow-changes the UI is read-only: nothing starts from it. With the
flag, y in a dry run starts that plan and no other; a plan that changed
since, or a health gate that warns, asks again before anything runs.
A change started elsewhere (the CLI, the console) shows on the Change
screen and can be watched, never stopped or answered, from the UI.

The UI needs a terminal of at least 100 by 24 cells. While it runs, the
standard error of refresh and of the programs it starts (such as a
kubeconfig credential plugin) goes to the null device, so their messages
cannot write over the screen. Press ? in the UI for the keys.

Exit codes:
  0  The UI was closed.
  1  An error, or no interactive terminal.`,
		Example: `  # The configured region
  refresh ui

  # Every region, swept every 30 seconds
  refresh ui -A --interval 30s

  # Let the UI start changes after their dry runs
  refresh ui --allow-changes`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.validate(); err != nil {
				return err
			}
			o.waitTimeoutSet = cmd.Flags().Changed("wait-timeout")
			return o.run(cmd.Context())
		},
	}
	fleetcmd.AddAllRegionsFlag(cmd, &o.allRegions)
	cmd.Flags().DurationVar(&o.interval, "interval", time.Minute, "Time between fleet sweeps, at least 10s; ctrl+r sweeps now")
	cmd.Flags().BoolVar(&o.allowChanges, "allow-changes", false, "Let the UI start changes after their dry runs; without it the UI is read-only")
	cmd.Flags().DurationVar(&o.waitTimeout, "wait-timeout", 0, "How long the UI follows a change it starts or watches; unset means the default of the command that makes the change, 0 means no limit")
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
	// The top bar shows the profile in effect also when AWS_PROFILE or
	// the context names it. Loading the provider read the same settings.
	if o.profile, err = o.profileInEffect(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	b := backend.New(ctx, provider, backend.Options{
		Regions:         fleetcmd.SweepRegions(o.global, provider.Region(), o.allRegions),
		Interval:        o.interval,
		AllowChanges:    o.allowChanges,
		ReadTimeout:     o.global.Timeout,
		WaitTimeout:     o.waitTimeout,
		WaitTimeoutSet:  o.waitTimeoutSet,
		Deps:            o.deps(provider),
		Profile:         o.global.Profile,
		ProfileInEffect: o.profile,
		Context:         o.contextName(),
		Kubeconfig:      o.global.Kubeconfig,
		KubeContext:     o.global.KubeContext,
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
	defer restore() // a second call is harmless

	m := tui.New(ctx, b, o.ios.Theme())
	opts := []tea.ProgramOption{tea.WithContext(ctx), tea.WithInput(o.ios.In), tea.WithOutput(o.ios.Out)}
	if !o.ios.ColorEnabled() {
		opts = append(opts, tea.WithColorProfile(colorprofile.ASCII))
	}
	_, err = tea.NewProgram(m, opts...).Run()
	restore()
	sayUnfinished(o.ios.ErrOut, b.Unfinished())
	if err != nil && ctx.Err() != nil {
		// The session was interrupted; main says so.
		return nil
	}
	return err
}

// sayUnfinished names the changes of the session that had not ended when
// the UI closed: EKS goes on with them, and the commands can watch them.
func sayUnfinished(w io.Writer, changes []state.Change) {
	if len(changes) == 0 {
		return
	}
	_, _ = fmt.Fprintf(w, "%s still running in EKS; the UI no longer follows them:\n", plural(len(changes), "change"))
	for _, c := range changes {
		var ids []string
		for _, s := range c.Steps {
			if s.UpdateID != "" && s.FinishedAt.IsZero() {
				ids = append(ids, s.UpdateID)
			}
		}
		line := "  " + kindWord(c.Kind) + " " + c.Target.Cluster + " (" + c.Target.Region + ")"
		if len(ids) > 0 {
			line += ": update " + strings.Join(ids, ", ")
		}
		if c.Elsewhere {
			line += " (started elsewhere)"
		}
		_, _ = fmt.Fprintln(w, line)
	}
}

func kindWord(k change.Kind) string {
	switch k {
	case change.NodegroupUpdate:
		return "roll of"
	case change.AddonUpdate:
		return "add-on update of"
	case change.ClusterUpgrade:
		return "upgrade of"
	case change.ClusterRollback:
		return "rollback of"
	case change.NodegroupScale:
		return "scale of"
	}
	return string(k) + " of"
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
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
