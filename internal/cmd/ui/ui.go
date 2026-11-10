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
	bctx, cancel := backendContext(ctx)
	defer cancel()

	b := backend.New(bctx, provider, backend.Options{
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
	defer restore() // it runs once; this covers a return before the call below

	m := tui.New(ctx, b, o.ios.Theme())
	opts := []tea.ProgramOption{tea.WithContext(ctx), tea.WithInput(o.ios.In), tea.WithOutput(o.ios.Out)}
	if !o.ios.ColorEnabled() {
		opts = append(opts, tea.WithColorProfile(colorprofile.ASCII))
	}
	_, err = tea.NewProgram(m, opts...).Run()
	restore()
	trace := m.Panic()
	err = panicked(err, trace)
	sayPanic(o.ios.ErrOut, err, trace)
	// The backend still runs: what is unfinished is what the session
	// left, also after an interrupt.
	sayUnfinished(o.ios.ErrOut, b.Unfinished())
	if err != nil && !errors.Is(err, tea.ErrProgramPanic) && ctx.Err() != nil {
		// The session was interrupted; main says so.
		return nil
	}
	return err
}

// panicked is the error of a session that Run ended with err: a panic in
// a command of the model, which the model recorded as trace, ends the
// program as a quit, with no error from Run.
func panicked(err error, trace string) error {
	if trace != "" && err == nil {
		return tea.ErrProgramPanic
	}
	return err
}

// backendContext is the context of the backend: the values of the
// session, and its own cancel, which run calls after it has said what is
// unfinished. An interrupt ends the session context; if it ended the
// backend too, the handles of the changes would finish, cancelled,
// before Unfinished reads them, and the words on the way out would name
// nothing.
func backendContext(session context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(context.WithoutCancel(session))
}

// sayPanic writes the panic that ended the UI, and its stack, to w. Bubble
// Tea printed it to os.Stderr, which was the null device then, or never,
// for a panic in a command. A panic outside the model leaves no trace,
// and the user is told how to get one.
func sayPanic(w io.Writer, err error, trace string) {
	if !errors.Is(err, tea.ErrProgramPanic) {
		return
	}
	if trace == "" {
		_, _ = fmt.Fprintln(w, "the UI stopped on a panic outside its own code; with TEA_DEBUG=1 the next run writes the trace to bubbletea-panic-<time>.log in this directory")
		return
	}
	_, _ = fmt.Fprintln(w, "the UI stopped on a panic:")
	_, _ = fmt.Fprintln(w, strings.TrimRight(trace, "\n"))
}

// sayUnfinished names the changes of the session that had not ended when
// the UI closed. EKS goes on with an update in flight, and the commands
// can watch it. A change of this UI stops with it: what it had not
// started does not run, and the command of its dry run is named.
func sayUnfinished(w io.Writer, changes []state.Change) {
	var inEKS, cut []string
	for _, c := range changes {
		var ids, notStarted []string
		for _, s := range c.Steps {
			switch {
			case s.UpdateID != "" && s.FinishedAt.IsZero():
				ids = append(ids, s.UpdateID)
			case s.Status == change.StepNotAttempted:
				notStarted = append(notStarted, stepName(s.Ref))
			}
		}
		what := "  " + kindWord(c.Kind) + " " + c.Target.Cluster + " (" + c.Target.Region + ")"
		if c.Elsewhere || len(ids) > 0 {
			line := what
			if len(ids) > 0 {
				line += ": update " + strings.Join(ids, ", ")
			}
			if c.Elsewhere {
				line += " (started elsewhere)"
			}
			inEKS = append(inEKS, line)
		}
		var line string
		switch {
		case c.Elsewhere:
			continue
		case len(notStarted) > 0:
			line = what + ": did not start " + strings.Join(notStarted, ", ")
		case len(ids) > 0:
			continue // the update in flight is the rest of it
		case len(c.Steps) == 0:
			line = what + ": nothing was started"
		default:
			line = what + ": stopped before its end"
		}
		if c.Command != "" {
			line += "\n    the same change from the command line: " + c.Command
		}
		cut = append(cut, line)
	}
	if len(inEKS) > 0 {
		_, _ = fmt.Fprintf(w, "%s still running in EKS; the UI no longer follows %s:\n", plural(len(inEKS), "change"), them(len(inEKS)))
		for _, line := range inEKS {
			_, _ = fmt.Fprintln(w, line)
		}
	}
	if len(cut) > 0 {
		_, _ = fmt.Fprintf(w, "%s of this UI stopped with it:\n", plural(len(cut), "change"))
		for _, line := range cut {
			_, _ = fmt.Fprintln(w, line)
		}
	}
}

// stepName names a step for a person.
func stepName(ref change.StepRef) string {
	switch ref.Kind {
	case change.StepControlPlane:
		return "the control plane"
	case change.StepNodegroup:
		return "nodegroup " + ref.Name
	case change.StepAddon:
		return "add-on " + ref.Name
	}
	return ref.Name
}

func them(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
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
