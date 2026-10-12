package cmdutil_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
)

func TestExitError(t *testing.T) {
	cause := errors.New("gate blocked the change")
	tests := []struct {
		name     string
		err      error
		wantCode int
		wantMsg  string
	}{
		{name: "nil", err: nil, wantCode: cmdutil.ExitOK},
		{name: "plain error", err: cause, wantCode: cmdutil.ExitFailure, wantMsg: "gate blocked the change"},
		{
			name:     "exit error with a message",
			err:      &cmdutil.ExitError{Code: cmdutil.ExitBlocked, Err: cause},
			wantCode: cmdutil.ExitBlocked,
			wantMsg:  "gate blocked the change",
		},
		{
			name:     "exit error without a message",
			err:      &cmdutil.ExitError{Code: cmdutil.ExitNeedsAttention},
			wantCode: cmdutil.ExitNeedsAttention,
		},
		{
			name:     "wrapped exit error",
			err:      fmt.Errorf("cluster prod: %w", &cmdutil.ExitError{Code: cmdutil.ExitIncomplete, Err: cause}),
			wantCode: cmdutil.ExitIncomplete,
			wantMsg:  "cluster prod: gate blocked the change",
		},
		{
			name:     "verify failed",
			err:      &cmdutil.ExitError{Code: cmdutil.ExitVerifyFailed, Err: cause},
			wantCode: cmdutil.ExitVerifyFailed,
			wantMsg:  "gate blocked the change",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cmdutil.ExitCode(tt.err); got != tt.wantCode {
				t.Errorf("ExitCode() = %d, want %d", got, tt.wantCode)
			}
			if tt.err == nil {
				return
			}
			if got := tt.err.Error(); got != tt.wantMsg {
				t.Errorf("Error() = %q, want %q", got, tt.wantMsg)
			}
			if tt.wantMsg != "" && !errors.Is(tt.err, cause) {
				t.Errorf("errors.Is(err, cause) = false, want true")
			}
		})
	}
}

func TestColorEnabled(t *testing.T) {
	tests := []struct {
		name             string
		outTTY, errTTY   bool
		noColor          bool
		wantOut, wantErr bool
	}{
		{name: "both terminals", outTTY: true, errTTY: true, wantOut: true, wantErr: true},
		{name: "no terminal"},
		{name: "stdout piped", errTTY: true, wantErr: true},
		{name: "stderr redirected", outTTY: true, wantOut: true},
		{name: "color off", outTTY: true, errTTY: true, noColor: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ios, _, _, _ := cmdutiltest.NewIOStreams()
			ios.OutTTY, ios.ErrTTY, ios.NoColor = tt.outTTY, tt.errTTY, tt.noColor
			if got := ios.ColorEnabled(); got != tt.wantOut {
				t.Errorf("ColorEnabled() = %t, want %t", got, tt.wantOut)
			}
			if got := ios.ErrColorEnabled(); got != tt.wantErr {
				t.Errorf("ErrColorEnabled() = %t, want %t", got, tt.wantErr)
			}
		})
	}
}

func TestAddOutputFlag(t *testing.T) {
	tests := []struct {
		name    string
		initial cmdutil.Format
		args    []string
		want    cmdutil.Format
		wantErr string
	}{
		{name: "default is the first allowed format", want: cmdutil.FormatTable},
		{name: "default set by the command", initial: cmdutil.FormatYAML, want: cmdutil.FormatYAML},
		{name: "short flag", args: []string{"-o", "json"}, want: cmdutil.FormatJSON},
		{name: "long flag", args: []string{"--output=yaml"}, want: cmdutil.FormatYAML},
		{name: "any letter case", args: []string{"-o", "JSON"}, want: cmdutil.FormatJSON},
		{
			name:    "format that the command does not support",
			args:    []string{"-o", "tree"},
			wantErr: `invalid argument "tree" for "-o, --output" flag: must be one of: table, json, yaml`,
		},
		{
			name:    "unknown format",
			args:    []string{"-o", "jsom"},
			wantErr: `invalid argument "jsom" for "-o, --output" flag: must be one of: table, json, yaml`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.initial
			cmd := &cobra.Command{
				Use:           "test",
				SilenceErrors: true,
				SilenceUsage:  true,
				RunE:          func(*cobra.Command, []string) error { return nil },
			}
			cmdutil.AddOutputFlag(cmd, &got, cmdutil.FormatTable, cmdutil.FormatJSON, cmdutil.FormatYAML)
			cmd.SetArgs(tt.args)

			err := cmd.Execute()
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("Execute() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("format = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatIsMachine(t *testing.T) {
	want := map[cmdutil.Format]bool{
		cmdutil.FormatTable: false,
		cmdutil.FormatJSON:  true,
		cmdutil.FormatYAML:  true,
		cmdutil.FormatPlain: false,
		cmdutil.FormatTree:  false,
	}
	for format, machine := range want {
		if got := format.IsMachine(); got != machine {
			t.Errorf("%q.IsMachine() = %t, want %t", format, got, machine)
		}
	}
}

func TestPrintDocument(t *testing.T) {
	doc := apidoc.Version{Version: "v1.2.3"}
	tests := []struct {
		format  cmdutil.Format
		want    string
		wantErr string
	}{
		{
			format: cmdutil.FormatJSON,
			want:   "{\n  \"apiVersion\": \"refresh.drod.dev/v2\",\n  \"kind\": \"Version\",\n  \"version\": \"v1.2.3\"\n}\n",
		},
		{
			format: cmdutil.FormatYAML,
			want:   "apiVersion: refresh.drod.dev/v2\nkind: Version\nversion: v1.2.3\n",
		},
		{format: cmdutil.FormatTable, wantErr: `cannot print the Version document as "table"`},
		{format: cmdutil.FormatPlain, wantErr: `cannot print the Version document as "plain"`},
	}
	for _, tt := range tests {
		t.Run(string(tt.format), func(t *testing.T) {
			ios, _, out, errOut := cmdutiltest.NewIOStreams()
			err := cmdutil.PrintDocument(ios, tt.format, doc)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("PrintDocument() error = %v, want %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("PrintDocument() error = %v", err)
			}
			if diff := cmp.Diff(tt.want, out.String()); diff != "" {
				t.Errorf("stdout (-want +got):\n%s", diff)
			}
			if errOut.Len() != 0 {
				t.Errorf("stderr = %q, want nothing", errOut.String())
			}
		})
	}
}
