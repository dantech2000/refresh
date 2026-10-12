// Command refresh is the EKS upgrade companion.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/dantech2000/refresh/internal/build"
	"github.com/dantech2000/refresh/internal/cmd/root"
	"github.com/dantech2000/refresh/internal/cmdutil"
)

func main() {
	ctx, stop := notifyContext(context.Background())
	code := run(ctx, os.Args[1:], cmdutil.System())
	stop()
	os.Exit(code)
}

// run executes the command line in args and returns the exit code of the
// process.
func run(ctx context.Context, args []string, streams *cmdutil.IOStreams) int {
	f := &cmdutil.Factory{IOStreams: streams, Build: build.Get()}
	cmd := root.NewCmdRoot(f)
	cmd.SetArgs(args)
	return exitCode(streams.ErrOut, cmd.ExecuteContext(ctx))
}

// exitCode prints err once on errOut and returns the exit code for it. An
// ExitError sets the code, and any other error is exit 1. An ExitError
// without a message prints nothing: the command already printed its result.
func exitCode(errOut io.Writer, err error) int {
	if err != nil && err.Error() != "" {
		// Nothing can report a failed write to stderr.
		_, _ = fmt.Fprintln(errOut, "Error: "+err.Error())
	}
	return cmdutil.ExitCode(err)
}

// notifyContext returns a context that the first SIGINT or SIGTERM cancels,
// so the calls in flight stop. Default signal handling is back before the
// context is cancelled, so a second signal kills the process even when
// something ignores the context. The caller must call the returned function
// to release the signal handler.
func notifyContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-signals:
		case <-ctx.Done():
		}
		signal.Stop(signals)
		cancel()
	}()
	return ctx, cancel
}
