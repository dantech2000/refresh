package commands

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/sim"
	"github.com/dantech2000/refresh/internal/tui"
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
// experiment: only the simulated backend exists so far.
func UICommand() *cli.Command {
	return &cli.Command{
		Name:   "ui",
		Usage:  "Full-screen terminal UI (experimental)",
		Hidden: true,
		Description: `Open the full-screen terminal UI: the fleet, readiness checks, live
nodegroup rolls, and cluster upgrades, with live event and log streams.

The UI is experimental and has no live AWS backend yet.`,
		Action: runUI,
	}
}

func runUI(ctx context.Context, _ *cli.Command) error {
	if !envTruthy(os.Getenv(envDevSimulate)) {
		return cli.Exit("refresh ui is experimental and has no live AWS backend yet", runner.ExitError)
	}
	if !runner.StdinIsTerminal() || !ui.IsTerminal(os.Stdout) {
		return cli.Exit("refresh ui needs an interactive terminal on stdin and stdout", runner.ExitError)
	}
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
