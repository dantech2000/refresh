// Package changecmd holds what the commands that change a cluster share:
// the dependencies of a change, the dry run, the run with the terminal as
// its front end, and the progress lines.
//
// It is beside the command packages and not in cmdutil, because cmdutil
// knows nothing about the fleet and internal/change imports it. It is the
// one place that knows both a change and a terminal: internal/change
// returns typed errors and results, and this package gives them the words
// of the command line.
package changecmd

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/change"
	"github.com/dantech2000/refresh/internal/cmd/fleetcmd"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/view"
)

// Deps returns the function that builds the dependencies of a change of
// one cluster in the region of the run. Calling that function is the
// first use of the network: it loads the AWS configuration.
func Deps(f *cmdutil.Factory) func(ctx context.Context) (change.Deps, error) {
	return func(ctx context.Context) (change.Deps, error) {
		// The checks of a command that reads one cluster: one region, and
		// a region at all.
		if _, err := cmdutil.ClusterClients(ctx, f); err != nil {
			return change.Deps{}, err
		}
		provider, err := f.AWS(ctx)
		if err != nil {
			return change.Deps{}, err
		}
		return depsOf(f, provider), nil
	}
}

// FleetDeps returns the function that builds the dependencies of a change
// of every cluster of a region sweep. It has no check for one region:
// the sweep names its regions. Calling that function is the first use of
// the network.
func FleetDeps(f *cmdutil.Factory) func(ctx context.Context) (change.Deps, error) {
	return func(ctx context.Context) (change.Deps, error) {
		provider, err := f.AWS(ctx)
		if err != nil {
			return change.Deps{}, err
		}
		return depsOf(f, provider), nil
	}
}

// depsOf returns the dependencies of a change from the factory: the
// clients, the Kubernetes access, and the clock.
func depsOf(f *cmdutil.Factory, provider *awsx.Provider) change.Deps {
	return change.Deps{
		AWS:       provider,
		Fetch:     &fleet.Fetcher{Now: f.Now},
		Kube:      f.Kube,
		Now:       f.Now,
		After:     f.After,
		AfterFunc: f.AfterFunc,
		Tick:      f.Tick,
		FollowUp:  change.FollowUp{Profile: cmdutil.ExplicitProfile(f)},
	}
}

// Common returns the part of a request that the shared flags fill.
// timeout is --timeout. --wait-timeout bounds each step alone, and the
// steps of a batch together.
func Common(flags *cmdutil.ChangeFlags, timeout time.Duration) change.Common {
	return change.Common{
		Policy: health.Policy{
			Yes:            flags.Yes,
			RequireHealthy: flags.RequireHealthy,
			Force:          flags.Force,
			Skip:           flags.SkipHealthCheck,
		},
		NoWait: flags.NoWait,
		Limits: change.Limits{Read: timeout, Wait: flags.WaitTimeout, Poll: flags.PollInterval},
	}
}

// Renderer is the front end of a run on a terminal. changecmd's own
// renderer writes progress lines; a command with a live panel passes one
// that draws it.
type Renderer interface {
	// Render shows one event of the run.
	Render(e change.Event)
	// Pause takes down what the renderer redraws, while a prompt shows,
	// and Resume puts it back.
	Pause()
	Resume()
	// Close ends the rendering before the result prints.
	Close()
	// View returns the table and plain form of the result.
	View(res change.Result) cmdutil.HumanView
}

// PrintPlan is a dry run: it reads live state under the --timeout budget,
// prints the plan, and returns the exit code of the plan. It changes
// nothing and never prompts.
//
// after, when not nil, runs once the plan is printed, with the plan: a
// command adds what only it knows how to show, on stderr. A timeout of 0
// is no limit: the plan of a fleet gives each cluster its own budget.
func PrintPlan(ctx context.Context, ios *cmdutil.IOStreams, format cmdutil.Format, d change.Deps, req change.Request, timeout time.Duration, after func(ctx context.Context, plan *change.Plan)) error {
	ctx, cancel := cmdutil.WithTimeout(ctx, timeout)
	defer cancel()
	plan, err := change.NewPlan(ctx, d, req)
	if err != nil {
		return planError(ctx, d, err)
	}
	doc := plan.Document()
	if err := cmdutil.Print(ios, format, doc, view.PlanView(doc)); err != nil {
		return err
	}
	if format != cmdutil.FormatTable {
		cmdutil.ReportFailures(ios, doc.Failures)
	}
	fleetcmd.ReportSkippedRegions(ios, doc.SkippedRegions)
	if after != nil {
		after(ctx, plan)
	}
	// An interrupt is not a partial result.
	if errors.Is(ctx.Err(), context.Canceled) {
		return fmt.Errorf("interrupted: %w", context.Canceled)
	}
	return planExit(plan)
}

// planExit returns the exit error of a dry run, or nil for a plan that a
// real run would start with nothing to report.
func planExit(plan *change.Plan) error {
	code := plan.ExitCode()
	if code == cmdutil.ExitOK {
		return nil
	}
	// A cluster of a fleet can be blocked while the fleet asks.
	blocked := slices.ContainsFunc(plan.Clusters, func(cp change.ClusterPlan) bool { return cp.Decision() == change.DecisionBlocked })
	var why string
	switch {
	case code == cmdutil.ExitIncomplete:
		why = "the plan is incomplete: something could not be read, or a target is already changing"
	case blocked:
		why = "a real run would be blocked"
	case code == cmdutil.ExitBlocked:
		// No gate and no blocker: every target is already changing.
		why = "a real run would change nothing: every target is already changing"
	default:
		why = "a real run would ask about health warnings"
	}
	return &cmdutil.ExitError{Code: code, Err: errors.New("dry run: " + why)}
}

// planError returns the error of a plan that could not be computed, in
// the words of the command line. A cluster, an add-on, or a nodegroup that
// does not exist gets the names that are close to it and the command that
// lists them all.
func planError(ctx context.Context, d change.Deps, err error) error {
	var read *change.ClusterReadError
	if errors.As(err, &read) {
		return fleetcmd.ClusterError(ctx, d.Fetch, d.AWS.Clients(read.Region), read.Cluster, read.Err)
	}
	var missing *change.NotFoundError
	if errors.As(err, &missing) {
		what, list := "nodegroup", "refresh nodegroup list -c "
		if missing.Kind == change.StepAddon {
			what, list = "add-on", "refresh addon list -c "
		}
		return cmdutil.NotFoundError(what, missing.Name, "cluster "+missing.Cluster, missing.Names, list+missing.Cluster)
	}
	// No region of a fleet answered. The sweep tells bad credentials from
	// regions that are closed, as the read commands do.
	var discovery *change.DiscoveryError
	if errors.As(err, &discovery) {
		if sweepErr := fleetcmd.SweepError(ctx, d.AWS, discovery.Sweep); sweepErr != nil {
			return sweepErr
		}
	}
	return err
}

// RunChange runs the change with the terminal as its front end: the
// events go to r, and a question is a prompt on stderr. It prints the
// document or the view of the result, and returns the exit code of the
// run. cmdutil.RequireYes ran before it, so a prompt is only reached
// where one can show.
func RunChange(ctx context.Context, ios *cmdutil.IOStreams, format cmdutil.Format, d change.Deps, req change.Request, r Renderer) error {
	res, err := change.Run(ctx, d, req, change.Hooks{
		Event: r.Render,
		Ask: func(ctx context.Context, q change.Question) (bool, error) {
			r.Pause()
			defer r.Resume()
			return cmdutil.Confirm(ctx, ios, q.Text)
		},
	})
	r.Close()
	doc := res.Document()
	if doc == nil {
		// No plan was computed, so there is nothing to print but the
		// error.
		if err != nil {
			return planError(ctx, d, err)
		}
		return &cmdutil.ExitError{Code: res.ExitCode(), Err: res.Err}
	}
	if perr := cmdutil.Print(ios, format, doc, r.View(res)); perr != nil {
		return perr
	}
	if format != cmdutil.FormatTable {
		cmdutil.ReportFailures(ios, failuresOf(doc))
	}
	if res.Plan != nil {
		fleetcmd.ReportSkippedRegions(ios, res.Plan.SkippedRegions)
	}
	if err != nil {
		return planError(ctx, d, err)
	}
	if code := res.ExitCode(); code != cmdutil.ExitOK {
		return &cmdutil.ExitError{Code: code, Err: res.Err}
	}
	return nil
}

// failuresOf returns the failures list of a plan or a run document.
func failuresOf(doc apidoc.Document) []apidoc.Failure {
	switch d := doc.(type) {
	case apidoc.Plan:
		return d.Failures
	case apidoc.Run:
		return d.Failures
	}
	return nil
}
