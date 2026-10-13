package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"go.uber.org/goleak"

	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantOut    string
		wantErrOut string
	}{
		{
			name:    "version",
			args:    []string{"version"},
			wantOut: "refresh version: dev\n",
		},
		{
			name:    "version flag",
			args:    []string{"--version"},
			wantOut: "refresh version: dev\n",
		},
		{
			name:    "json document",
			args:    []string{"version", "-o", "json"},
			wantOut: "{\n  \"apiVersion\": \"refresh.drod.dev/v2\",\n  \"kind\": \"Version\",\n  \"version\": \"dev\"\n}\n",
		},
		{
			name:       "unknown command",
			args:       []string{"bogus"},
			wantCode:   1,
			wantErrOut: "Error: unknown command \"bogus\" for \"refresh\"\n",
		},
		{
			name:       "unknown flag",
			args:       []string{"version", "--bogus"},
			wantCode:   1,
			wantErrOut: "Error: unknown flag: --bogus\n",
		},
		{
			name:       "invalid output format",
			args:       []string{"version", "-o", "jsom"},
			wantCode:   1,
			wantErrOut: "Error: invalid argument \"jsom\" for \"-o, --output\" flag: must be one of: table, json, yaml\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("REFRESH_NO_UPDATE_CHECK", "1")
			for _, name := range []string{"REFRESH_TIMEOUT", "REFRESH_MAX_CONCURRENCY", "REFRESH_LOG_LEVEL"} {
				t.Setenv(name, "")
			}
			streams, _, out, errOut := cmdutiltest.NewIOStreams()

			code := run(context.Background(), tt.args, streams)

			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if diff := cmp.Diff(tt.wantOut, out.String()); diff != "" {
				t.Errorf("stdout (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantErrOut, errOut.String()); diff != "" {
				t.Errorf("stderr (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRunHelp(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"version", "--help"}} {
		t.Run(fmt.Sprint(args), func(t *testing.T) {
			streams, _, out, errOut := cmdutiltest.NewIOStreams()

			code := run(context.Background(), args, streams)

			if code != 0 {
				t.Errorf("exit code = %d, want 0", code)
			}
			if !bytes.Contains(out.Bytes(), []byte("Usage:\n  refresh")) {
				t.Errorf("stdout has no usage:\n%s", out.String())
			}
			if errOut.Len() != 0 {
				t.Errorf("stderr = %q, want nothing", errOut.String())
			}
		})
	}
}

func TestExitCode(t *testing.T) {
	cause := errors.New("2 nodegroups are stale")
	tests := []struct {
		name        string
		err         error
		interrupted bool // the signal context is cancelled
		wantCode    int
		wantErrOut  string
	}{
		{name: "success", err: nil, wantCode: 0},
		{
			name:       "cancelled inside a result code",
			err:        &cmdutil.ExitError{Code: cmdutil.ExitIncomplete, Err: fmt.Errorf("status: %w", context.Canceled)},
			wantCode:   1,
			wantErrOut: "Error: status: context canceled\n",
		},
		{
			name:       "deadline keeps the result code",
			err:        &cmdutil.ExitError{Code: cmdutil.ExitIncomplete, Err: fmt.Errorf("status: %w", context.DeadlineExceeded)},
			wantCode:   4,
			wantErrOut: "Error: status: context deadline exceeded\n",
		},
		{
			name:        "interrupted, command returned nothing",
			interrupted: true,
			wantCode:    1,
			wantErrOut:  "Error: interrupted\n",
		},
		{
			name:        "interrupted, command returned a result code",
			err:         &cmdutil.ExitError{Code: cmdutil.ExitIncomplete, Err: cause},
			interrupted: true,
			wantCode:    1,
			wantErrOut:  "Error: 2 nodegroups are stale\n",
		},
		{
			name:        "interrupted, result already printed",
			err:         &cmdutil.ExitError{Code: cmdutil.ExitVerifyFailed},
			interrupted: true,
			wantCode:    1,
			wantErrOut:  "Error: interrupted\n",
		},
		{name: "plain error", err: cause, wantCode: 1, wantErrOut: "Error: 2 nodegroups are stale\n"},
		{
			name:       "wrapped plain error",
			err:        fmt.Errorf("status: %w", cause),
			wantCode:   1,
			wantErrOut: "Error: status: 2 nodegroups are stale\n",
		},
		{
			name:       "interrupt",
			err:        context.Canceled,
			wantCode:   1,
			wantErrOut: "Error: context canceled\n",
		},
		{
			name:       "error code with a message",
			err:        &cmdutil.ExitError{Code: cmdutil.ExitFailure, Err: cause},
			wantCode:   1,
			wantErrOut: "Error: 2 nodegroups are stale\n",
		},
		{
			name:     "needs attention, already printed",
			err:      &cmdutil.ExitError{Code: cmdutil.ExitNeedsAttention},
			wantCode: 2,
		},
		{
			name:       "blocked",
			err:        &cmdutil.ExitError{Code: cmdutil.ExitBlocked, Err: cause},
			wantCode:   3,
			wantErrOut: "Error: 2 nodegroups are stale\n",
		},
		{
			name:       "incomplete, wrapped",
			err:        fmt.Errorf("status: %w", &cmdutil.ExitError{Code: cmdutil.ExitIncomplete, Err: cause}),
			wantCode:   4,
			wantErrOut: "Error: status: 2 nodegroups are stale\n",
		},
		{
			// To stop a watch is the normal way to end it.
			name:        "a watch that Ctrl+C stopped",
			err:         cmdutil.ErrStopped,
			interrupted: true,
			wantCode:    0,
		},
		{
			name:     "verify failed, already printed",
			err:      &cmdutil.ExitError{Code: cmdutil.ExitVerifyFailed},
			wantCode: 5,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var errOut bytes.Buffer
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.interrupted {
				cancel()
			}

			code := exitCode(ctx, &errOut, tt.err)

			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if diff := cmp.Diff(tt.wantErrOut, errOut.String()); diff != "" {
				t.Errorf("stderr (-want +got):\n%s", diff)
			}
		})
	}
}

// TestInterrupt cancels the signal context while the update check of
// "refresh version" waits on the network. The command hides the failed
// check, so only the context shows that the run was interrupted.
func TestInterrupt(t *testing.T) {
	const jsonDoc = "{\n  \"apiVersion\": \"refresh.drod.dev/v2\",\n  \"kind\": \"Version\",\n  \"version\": \"v1.2.3\",\n" +
		"  \"commit\": \"abc1234\",\n  \"buildDate\": \"2026-01-02T03:04:05Z\"\n}\n"
	tests := []struct {
		name    string
		args    []string
		wantOut string
	}{
		{
			name:    "table",
			args:    []string{"version"},
			wantOut: "refresh version: v1.2.3\ncommit: abc1234\nbuilt: 2026-01-02T03:04:05Z\n",
		},
		{name: "json", args: []string{"version", "-o", "json"}, wantOut: jsonDoc},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("REFRESH_NO_UPDATE_CHECK", "")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f, _, out, errOut := cmdutiltest.NewFactory()
			f.IOStreams.OutTTY = true
			waiting := make(chan struct{})
			f.LatestRelease = func(ctx context.Context) (string, error) {
				close(waiting)
				<-ctx.Done()
				return "", ctx.Err()
			}

			done := make(chan int)
			go func() { done <- execute(ctx, tt.args, f) }()
			<-waiting // the command is inside the update check
			cancel()
			code := <-done

			if code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if diff := cmp.Diff(tt.wantOut, out.String()); diff != "" {
				t.Errorf("stdout (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff("Error: interrupted\n", errOut.String()); diff != "" {
				t.Errorf("stderr (-want +got):\n%s", diff)
			}
		})
	}
}

// TestInterruptBeforeRun checks that a run that starts after the signal
// exits 1 even when the command finds nothing wrong.
func TestInterruptBeforeRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f, _, _, errOut := cmdutiltest.NewFactory()

	code := execute(ctx, []string{"version"}, f)

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if diff := cmp.Diff("Error: interrupted\n", errOut.String()); diff != "" {
		t.Errorf("stderr (-want +got):\n%s", diff)
	}
}
