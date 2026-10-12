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
	"github.com/dantech2000/refresh/internal/updatecheck"
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
	return execute(ctx, args, &cmdutil.Factory{
		IOStreams:     streams,
		Build:         build.Get(),
		LatestRelease: latestRelease,
	})
}

// execute runs the command line in args with the dependencies in f. ctx is
// the signal context: when it is cancelled, the run was interrupted.
func execute(ctx context.Context, args []string, f *cmdutil.Factory) int {
	cmd := root.NewCmdRoot(f)
	cmd.SetArgs(args)
	return exitCode(ctx, f.IOStreams.ErrOut, cmd.ExecuteContext(ctx))
}

// exitCode prints err once on errOut and returns the exit code for it. An
// ExitError sets the code, and any other error is exit 1. An ExitError
// without a message prints nothing: the command already printed its result.
//
// An interrupt is always exit 1, whatever the command returned: a result
// that Ctrl+C cut short is not a partial result. The rule reads the signal
// context and not the error, because a run that the --timeout deadline cut
// short keeps its own code.
func exitCode(ctx context.Context, errOut io.Writer, err error) int {
	interrupted := ctx.Err() != nil
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if msg == "" && interrupted {
		msg = "interrupted"
	}
	if msg != "" {
		// Nothing can report a failed write to stderr.
		_, _ = fmt.Fprintln(errOut, "Error: "+msg)
	}
	if interrupted {
		return cmdutil.ExitFailure
	}
	return cmdutil.ExitCode(err)
}

// latestRelease asks GitHub for the newest release tag. A cache file keeps
// the network call to about one a day.
func latestRelease(ctx context.Context) (string, error) {
	// Without a cache path the checker still works; it then uses the
	// network on every call.
	cachePath, _ := updatecheck.DefaultCachePath()
	checker := updatecheck.Checker{CachePath: cachePath}
	return checker.LatestTag(ctx)
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
