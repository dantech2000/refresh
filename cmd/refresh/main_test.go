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
		name       string
		err        error
		wantCode   int
		wantErrOut string
	}{
		{name: "success", err: nil, wantCode: 0},
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
			name:     "verify failed, already printed",
			err:      &cmdutil.ExitError{Code: cmdutil.ExitVerifyFailed},
			wantCode: 5,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var errOut bytes.Buffer

			code := exitCode(&errOut, tt.err)

			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if diff := cmp.Diff(tt.wantErrOut, errOut.String()); diff != "" {
				t.Errorf("stderr (-want +got):\n%s", diff)
			}
		})
	}
}
