package commands

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/go-logr/logr"
	"github.com/urfave/cli/v3"
	"k8s.io/klog/v2"

	"github.com/dantech2000/refresh/internal/cliconfig"
	"github.com/dantech2000/refresh/internal/commands/runner"
	appconfig "github.com/dantech2000/refresh/internal/config"
	"github.com/dantech2000/refresh/internal/sim"
	"github.com/dantech2000/refresh/internal/tui"
	"github.com/dantech2000/refresh/internal/tui/live"
	"github.com/dantech2000/refresh/internal/ui"
)

// Development-only switches for refresh ui. They are read here and nowhere
// else, and are not documented in the CLI reference.
const (
	// envDevSimulate runs the TUI against the simulated fleet (internal/sim)
	// instead of AWS.
	envDevSimulate = "REFRESH_DEV_SIMULATE"
	// envDevSimSpeed is how many simulated seconds pass per real second
	// (default 8).
	envDevSimSpeed = "REFRESH_DEV_SIM_SPEED"
	// envDevSimSeed seeds the simulated timings (default 1).
	envDevSimSeed = "REFRESH_DEV_SIM_SEED"
)

// simWarmup is how far the simulated fleet runs before the first frame, so
// the prod-eu upgrade is already rolling nodegroups when the TUI opens.
const simWarmup = 19 * time.Minute

// UICommand is the full-screen terminal UI. It is hidden while it is an
// experiment: the live backend is read-only so far.
func UICommand() *cli.Command {
	return &cli.Command{
		Name:   "ui",
		Usage:  "Full-screen terminal UI (experimental)",
		Hidden: true,
		Description: `Open the full-screen terminal UI: the fleet, readiness checks, live
nodegroup rolls, and cluster upgrades, with live event and log streams.

The UI is experimental. It shows the fleet, runs readiness checks, and
dry-runs changes, and prints the CLI command that makes each change. It is
read-only unless --allow-changes is given; then a nodegroup roll or an
add-on update can start from its dry run, after the same checks as the
nodegroup update and addon update --all commands. Upgrades stay dry runs.
It sweeps the config region, the regions given with -r, or with -A every EKS
region (REFRESH_EKS_REGIONS narrows that list).`,
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "all-regions", Aliases: []string{"A"}, Usage: "Sweep all EKS-supported regions"},
			&cli.StringSliceFlag{Name: "region", Aliases: []string{"r"}, Usage: "Region(s) to sweep (repeatable)"},
			&cli.DurationFlag{Name: "interval", Usage: "Time between fleet sweeps", Value: time.Minute},
			&cli.BoolFlag{Name: "allow-changes", Usage: "Let the UI start nodegroup rolls and add-on updates (after their dry runs and gates); without it the UI is read-only"},
			runner.WaitTimeoutFlag("How long the UI watches a roll it started (0 = no limit; the EKS update continues either way)", appconfig.DefaultUpdateTimeout),
			runner.KubeconfigFlag("the live node view of a roll and the health gate's workload/PDB checks"),
			runner.KubeContextFlag(),
		},
		Action: runUI,
	}
}

func runUI(ctx context.Context, cmd *cli.Command) error {
	if !runner.StdinIsTerminal() || !ui.IsTerminal(os.Stdout) {
		return cli.Exit("refresh ui needs an interactive terminal on stdin and stdout", runner.ExitError)
	}
	if envTruthy(os.Getenv(envDevSimulate)) {
		return runSimulated(ctx)
	}
	return runLive(ctx, cmd)
}

// runLive runs the TUI against the accounts behind the AWS config.
func runLive(ctx context.Context, cmd *cli.Command) error {
	// No deadline: the TUI runs until the user quits. Each sweep and call
	// has its own timeout.
	ctx, cancel, awsCfg, err := runner.SetupAWSWithDeadline(ctx, cmd, 0)
	if err != nil {
		return err
	}
	defer cancel()
	// Nothing may write to the terminal while the TUI owns it: client-go
	// logs list/watch failures through klog, and a kubeconfig exec plugin
	// (aws eks get-token with an expired SSO token) prints to the process's
	// stderr and may read its stdin. The TUI keeps the real terminal; the
	// rest of the process gets /dev/null until it exits. The feed and the
	// log pane report what failed.
	klog.SetLogger(logr.Discard())
	defer klog.ClearLogger()
	tty, restore, err := detachStdio()
	if err != nil {
		return err
	}
	defer restore()
	regions, skip := uiRegions(cmd, awsCfg)
	profile := cmd.String("profile")
	if profile == "" {
		profile = os.Getenv("AWS_PROFILE")
	}
	backend := live.New(awsCfg, live.Options{
		Regions:          regions,
		SkipInaccessible: skip,
		Interval:         cmd.Duration("interval"),
		SweepTimeout:     2 * runner.APITimeout(cmd),
		MaxConcurrency:   appconfig.ClampMaxConcurrency(cmd.Int("max-concurrency")),
		Context:          activeContextName(),
		Profile:          profile,
		AllowChanges:     cmd.Bool("allow-changes"),
		Kubeconfig:       cmd.String("kubeconfig"),
		KubeContext:      cmd.String("kube-context"),
		WaitTimeout:      runner.WaitTimeout(cmd, ""),
		CallTimeout:      runner.APITimeout(cmd),
	})
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		backend.Run(ctx)
	}()
	err = tui.Run(ctx, backend, tty.in, os.Stdout)
	stop()
	<-done
	backend.Close()
	return err
}

// stdio is the terminal the TUI keeps.
type stdio struct{ in *os.File }

// detachStdio points os.Stdin and os.Stderr at /dev/null and returns the
// original stdin for the TUI. restore puts both back. Code that captures
// them while the TUI runs (client-go's exec authenticator, klog) gets
// /dev/null.
func detachStdio() (stdio, func(), error) {
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return stdio{}, nil, err
	}
	in, errOut := os.Stdin, os.Stderr
	os.Stdin, os.Stderr = null, null
	return stdio{in: in}, func() {
		os.Stdin, os.Stderr = in, errOut
		_ = null.Close()
	}, nil
}

// uiRegions picks the regions to sweep, as `refresh status` does: -r, else
// -A (REFRESH_EKS_REGIONS, else the partition), else the config region.
// skip reports a default partition sweep, which skips closed regions.
func uiRegions(cmd *cli.Command, awsCfg aws.Config) (regions []string, skip bool) {
	if r := runner.Regions(cmd, cmd.Bool("all-regions")); len(r) > 0 {
		return r, false
	}
	if cmd.Bool("all-regions") {
		if env := appconfig.RegionsFromEnv(); len(env) > 0 {
			return env, false
		}
		return appconfig.GetRegionsForPartition(awsCfg.Region), true
	}
	if awsCfg.Region != "" {
		return []string{awsCfg.Region}, false
	}
	return appconfig.GetRegionsForPartition(awsCfg.Region), true
}

// activeContextName is the refresh context in use, or "".
func activeContextName() string {
	f, err := cliconfig.Load()
	if err != nil {
		return ""
	}
	name, _, ok, err := f.Active()
	if err != nil || !ok {
		return ""
	}
	return name
}

// runSimulated runs the TUI against the simulated fleet (internal/sim).
func runSimulated(ctx context.Context) error {
	speed, err := envFloat(envDevSimSpeed, 8)
	if err != nil {
		return cli.Exit(err.Error(), runner.ExitError)
	}
	seed, err := envFloat(envDevSimSeed, 1)
	if err != nil {
		return cli.Exit(err.Error(), runner.ExitError)
	}
	// Start the simulated clock in the past, so after the warmup it reads
	// about the wall-clock time.
	world := sim.New(sim.Options{Seed: uint64(seed), Start: time.Now().Add(-simWarmup), Warmup: simWarmup})
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		world.Run(ctx, speed)
	}()
	err = tui.Run(ctx, world, os.Stdin, os.Stdout)
	cancel()
	<-done
	return err
}

// envFloat reads a positive number from an env var, or def when unset.
func envFloat(name string, def float64) (float64, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f <= 0 {
		return 0, fmt.Errorf("%s must be a positive number, got %q", name, v)
	}
	return f, nil
}
