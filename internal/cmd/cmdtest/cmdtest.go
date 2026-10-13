// Package cmdtest runs a whole refresh command against a fake AWS, for the
// tests of the command packages. A test of a command package imports it
// from its external test package: cmdtest imports the root command, and the
// root command imports every command package.
package cmdtest

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/cmd/fleetcmd"
	"github.com/dantech2000/refresh/internal/cmd/root"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
	"github.com/dantech2000/refresh/internal/contexts"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// Fake is a fake AWS that serves a world, and a factory that reaches it. A
// test runs a whole command through it and reads what the command wrote.
type Fake struct {
	World   *fleettest.World
	Server  *awsxtest.Server
	Factory *cmdutil.Factory
	// Out and Err are what the command wrote to stdout and stderr.
	Out, Err *bytes.Buffer
}

// NewFake starts a fake AWS for world. The home region of the run is
// region. Nothing in the environment of the developer reaches the command:
// the variables that refresh reads are cleared, the context store is empty
// and in a directory of the test, and the kubeconfig is a file that does
// not exist.
func NewFake(t *testing.T, world *fleettest.World, region string) *Fake {
	t.Helper()
	for _, name := range []string{
		"REFRESH_TIMEOUT", "REFRESH_MAX_CONCURRENCY", "REFRESH_LOG_LEVEL", "NO_COLOR",
		fleetcmd.EnvRegions, cmdutil.EnvCluster, contexts.EnvContext,
	} {
		t.Setenv(name, "")
	}
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "no-kubeconfig"))
	srv := awsxtest.NewServer(world)
	t.Cleanup(srv.Close)
	f, _, out, errOut := cmdutiltest.NewFactory()
	cmdutiltest.NewContextStore(t, f, nil)
	cmdutiltest.UseAWS(f, srv.Config(region))
	return &Fake{World: world, Server: srv, Factory: f, Out: out, Err: errOut}
}

// UseWorldClock makes the clock of the world the clock of the command:
// the factory reads the time of the world, and a pause of the command
// moves the world forward by that long and ends at once. A command that
// waits for an update then makes its polls as fast as the fake answers,
// and the updates of the world progress as if the time had passed. The
// timer of a time budget never fires: no call of such a test hangs, and
// the clock of the world decides when a budget runs out. The clock of the
// node observer never ticks either: an observer takes its first snapshot
// and no other, so nothing beside the wait depends on real time.
func (e *Fake) UseWorldClock() {
	e.Factory.Now = e.World.Now
	e.Factory.Tick = func(time.Duration) <-chan time.Time { return nil }
	e.Factory.AfterFunc = func(time.Duration, func()) (stop func() bool) {
		return func() bool { return true }
	}
	e.Factory.After = func(d time.Duration) <-chan time.Time {
		e.World.Advance(d)
		done := make(chan time.Time, 1)
		done <- e.World.Now()
		return done
	}
}

// Run executes the refresh command line in args and returns the error of
// the run.
func (e *Fake) Run(ctx context.Context, args ...string) error {
	cmd := root.NewCmdRoot(e.Factory)
	cmd.SetArgs(args)
	return cmd.ExecuteContext(ctx)
}

// Operations returns the operation and the region of every request, in
// arrival order.
func (e *Fake) Operations() []string {
	var out []string
	for _, r := range e.Server.Requests() {
		out = append(out, string(r.Operation)+" "+r.Region)
	}
	return out
}

// Count returns how many requests for op the server got.
func (e *Fake) Count(op fleettest.Operation) int {
	n := 0
	for _, r := range e.Server.Requests() {
		if r.Operation == op {
			n++
		}
	}
	return n
}
