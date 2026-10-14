package root_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"gopkg.in/yaml.v3"

	"github.com/dantech2000/refresh/internal/cmd/root"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
)

// clearEnv gives a test a known environment: no variable that refresh reads
// is set, and the update check is off.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"REFRESH_TIMEOUT", "REFRESH_MAX_CONCURRENCY", "REFRESH_LOG_LEVEL", "NO_COLOR",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("REFRESH_NO_UPDATE_CHECK", "1")
}

// execute runs a new root command with args and returns the error of the
// run.
func execute(f *cmdutil.Factory, args ...string) error {
	cmd := root.NewCmdRoot(f)
	cmd.SetArgs(args)
	return cmd.ExecuteContext(context.Background())
}

func TestVersion(t *testing.T) {
	const table = "refresh version: v1.2.3\ncommit: abc1234\nbuilt: 2026-01-02T03:04:05Z\n"
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "table", args: []string{"version"}, want: table},
		{name: "explicit table", args: []string{"version", "-o", "table"}, want: table},
		{name: "version flag", args: []string{"--version"}, want: table},
		{
			name: "json",
			args: []string{"version", "-o", "json"},
			want: `{
  "apiVersion": "refresh.drod.dev/v2",
  "kind": "Version",
  "version": "v1.2.3",
  "commit": "abc1234",
  "buildDate": "2026-01-02T03:04:05Z"
}
`,
		},
		{
			name: "yaml",
			args: []string{"version", "--output", "yaml"},
			want: `apiVersion: refresh.drod.dev/v2
kind: Version
version: v1.2.3
commit: abc1234
buildDate: "2026-01-02T03:04:05Z"
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			f, _, out, errOut := cmdutiltest.NewFactory()

			err := execute(f, tt.args...)

			if code := cmdutil.ExitCode(err); code != cmdutil.ExitOK {
				t.Errorf("exit code = %d (error: %v), want 0", code, err)
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

// TestVersionPrintsOneDocument reads the machine output back with a decoder
// and checks that stdout holds one document and nothing else.
func TestVersionPrintsOneDocument(t *testing.T) {
	want := map[string]any{
		"apiVersion": "refresh.drod.dev/v2",
		"kind":       "Version",
		"version":    "v1.2.3",
		"commit":     "abc1234",
		"buildDate":  "2026-01-02T03:04:05Z",
	}
	tests := []struct {
		format string
		decode func(io.Reader) func(any) error
	}{
		{format: "json", decode: func(r io.Reader) func(any) error { return json.NewDecoder(r).Decode }},
		{format: "yaml", decode: func(r io.Reader) func(any) error { return yaml.NewDecoder(r).Decode }},
	}
	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			clearEnv(t)
			f, _, out, _ := cmdutiltest.NewFactory()
			// A terminal is the case in which a hint or a notice could
			// reach stdout by mistake.
			f.IOStreams.OutTTY = true

			if err := execute(f, "version", "-o", tt.format); err != nil {
				t.Fatalf("version -o %s: %v", tt.format, err)
			}

			decode := tt.decode(bytes.NewReader(out.Bytes()))
			var got map[string]any
			if err := decode(&got); err != nil {
				t.Fatalf("reading the first document: %v\nstdout:\n%s", err, out.String())
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("document (-want +got):\n%s", diff)
			}
			var extra any
			if err := decode(&extra); !errors.Is(err, io.EOF) {
				t.Errorf("after the first document: value %v, error %v; want io.EOF", extra, err)
			}
		})
	}
}

func TestUsageErrors(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		env     map[string]string
		wantErr string
	}{
		{name: "unknown command", args: []string{"bogus"}, wantErr: `unknown command "bogus" for "refresh"`},
		{
			name:    "command with a typing error",
			args:    []string{"versio"},
			wantErr: "unknown command \"versio\" for \"refresh\"\n\nDid you mean this?\n\tversion\n",
		},
		{
			name:    "unknown flag",
			args:    []string{"version", "--bogus"},
			wantErr: "unknown flag: --bogus\nRun 'refresh version --help' for usage",
		},
		{
			name: "unknown global flag",
			// Cobra does not look for the command after a flag it cannot
			// read, so the hint names the root.
			args:    []string{"--bogus", "version"},
			wantErr: "unknown flag: --bogus\nRun 'refresh --help' for usage",
		},
		{
			name:    "unknown flag of the root",
			args:    []string{"--bogus"},
			wantErr: "unknown flag: --bogus\nRun 'refresh --help' for usage",
		},
		{
			name:    "v is not the version",
			args:    []string{"-v"},
			wantErr: "unknown shorthand flag: 'v' in -v\nRun 'refresh --help' for usage",
		},
		{
			name:    "extra argument",
			args:    []string{"version", "extra"},
			wantErr: `unknown command "extra" for "refresh version"`,
		},
		{
			name:    "format that version does not support",
			args:    []string{"version", "-o", "plain"},
			wantErr: "invalid argument \"plain\" for \"-o, --output\" flag: must be one of: table, json, yaml\nRun 'refresh version --help' for usage",
		},
		{
			name:    "invalid timeout",
			args:    []string{"--timeout", "soon", "version"},
			wantErr: "invalid argument \"soon\" for \"-t, --timeout\" flag: time: invalid duration \"soon\"\nRun 'refresh version --help' for usage",
		},
		{
			name:    "invalid timeout in the environment",
			args:    []string{"version"},
			env:     map[string]string{"REFRESH_TIMEOUT": "soon"},
			wantErr: `invalid REFRESH_TIMEOUT: invalid argument "soon" for "-t, --timeout" flag: time: invalid duration "soon"`,
		},
		{
			name:    "invalid concurrency in the environment",
			args:    []string{"version"},
			env:     map[string]string{"REFRESH_MAX_CONCURRENCY": "many"},
			wantErr: `invalid REFRESH_MAX_CONCURRENCY: invalid argument "many" for "--max-concurrency" flag: strconv.ParseInt: parsing "many": invalid syntax`,
		},
		{
			name:    "an empty region",
			args:    []string{"-r", "us-east-1,,eu-west-1", "version"},
			wantErr: `invalid --region "us-east-1,,eu-west-1": an empty region name`,
		},
		{
			name:    "concurrency below one",
			args:    []string{"--max-concurrency", "0", "version"},
			wantErr: "invalid --max-concurrency 0: must be at least 1",
		},
		{
			name:    "invalid log level",
			args:    []string{"--log-level", "loud", "version"},
			wantErr: `invalid log level "loud": must be one of debug, info, warn, error`,
		},
		{
			name:    "invalid log level in the environment",
			args:    []string{"version"},
			env:     map[string]string{"REFRESH_LOG_LEVEL": "loud"},
			wantErr: `invalid log level "loud": must be one of debug, info, warn, error`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			for name, value := range tt.env {
				t.Setenv(name, value)
			}
			f, _, out, errOut := cmdutiltest.NewFactory()

			err := execute(f, tt.args...)

			if code := cmdutil.ExitCode(err); code != cmdutil.ExitFailure {
				t.Errorf("exit code = %d, want 1", code)
			}
			if err == nil || err.Error() != tt.wantErr {
				t.Errorf("error = %v, want %q", err, tt.wantErr)
			}
			// The root command is silent: main prints the error, once.
			if out.Len() != 0 {
				t.Errorf("stdout = %q, want nothing", out.String())
			}
			if errOut.Len() != 0 {
				t.Errorf("stderr = %q, want nothing", errOut.String())
			}
		})
	}
}

func TestHelp(t *testing.T) {
	globalFlags := []string{
		"-r, --region", "--profile", "-t, --timeout", "--max-concurrency",
		"--no-color", "--log-level", "--verbose", "--kubeconfig", "--kube-context",
	}
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "root",
			args: []string{"--help"},
			want: append([]string{
				"refresh keeps EKS clusters up to date", "Usage:\n  refresh [flags]\n  refresh [command]",
				"\n  completion ", "\n  version ", "-h, --help", "--version",
			}, globalFlags...),
		},
		{
			name: "root without arguments",
			args: nil,
			want: []string{"Usage:\n  refresh [flags]\n  refresh [command]"},
		},
		{
			name: "version",
			args: []string{"version", "--help"},
			want: append([]string{
				"Usage:\n  refresh version [flags]", "--no-update-check", "-o, --output", "Global Flags:",
			}, globalFlags...),
		},
		{
			name: "help command",
			args: []string{"help", "version"},
			want: []string{"Usage:\n  refresh version [flags]"},
		},
		{
			name: "completion",
			args: []string{"completion", "--help"},
			want: []string{"\n  bash ", "\n  fish ", "\n  powershell ", "\n  zsh "},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			f, _, out, errOut := cmdutiltest.NewFactory()

			err := execute(f, tt.args...)

			if code := cmdutil.ExitCode(err); code != cmdutil.ExitOK {
				t.Errorf("exit code = %d (error: %v), want 0", code, err)
			}
			for _, want := range tt.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("stdout does not contain %q", want)
				}
			}
			if t.Failed() {
				t.Logf("stdout:\n%s", out.String())
			}
			if errOut.Len() != 0 {
				t.Errorf("stderr = %q, want nothing", errOut.String())
			}
		})
	}
}

func TestCompletion(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		t.Run(shell, func(t *testing.T) {
			clearEnv(t)
			f, _, out, errOut := cmdutiltest.NewFactory()

			if err := execute(f, "completion", shell); err != nil {
				t.Fatalf("completion %s: %v", shell, err)
			}

			if !strings.Contains(out.String(), "refresh") {
				t.Errorf("stdout does not look like a completion script for refresh:\n%.200s", out.String())
			}
			if errOut.Len() != 0 {
				t.Errorf("stderr = %q, want nothing", errOut.String())
			}
		})
	}
}

func TestGlobalFlags(t *testing.T) {
	defaults := cmdutil.GlobalOptions{
		Timeout:        time.Minute,
		MaxConcurrency: 32,
		LogLevel:       slog.LevelWarn,
	}
	with := func(change func(*cmdutil.GlobalOptions)) cmdutil.GlobalOptions {
		o := defaults
		change(&o)
		return o
	}
	tests := []struct {
		name string
		args []string
		env  map[string]string
		want cmdutil.GlobalOptions
	}{
		{name: "defaults", want: defaults},
		{
			name: "environment",
			env: map[string]string{
				"REFRESH_TIMEOUT":         "90s",
				"REFRESH_MAX_CONCURRENCY": "3",
				"REFRESH_LOG_LEVEL":       "info",
			},
			want: cmdutil.GlobalOptions{Timeout: 90 * time.Second, MaxConcurrency: 3, LogLevel: slog.LevelInfo},
		},
		{
			name: "flags",
			args: []string{"--timeout", "5m", "--max-concurrency", "2", "--log-level", "error"},
			want: cmdutil.GlobalOptions{Timeout: 5 * time.Minute, MaxConcurrency: 2, LogLevel: slog.LevelError},
		},
		{
			name: "a flag beats its environment variable",
			args: []string{"-t", "5m", "--max-concurrency", "2", "--log-level", "error"},
			env: map[string]string{
				"REFRESH_TIMEOUT":         "90s",
				"REFRESH_MAX_CONCURRENCY": "3",
				"REFRESH_LOG_LEVEL":       "info",
			},
			want: cmdutil.GlobalOptions{Timeout: 5 * time.Minute, MaxConcurrency: 2, LogLevel: slog.LevelError},
		},
		{
			name: "a flag with its default value beats the environment",
			args: []string{"--timeout", "1m", "--max-concurrency", "32", "--log-level", "warn"},
			env: map[string]string{
				"REFRESH_TIMEOUT":         "90s",
				"REFRESH_MAX_CONCURRENCY": "3",
				"REFRESH_LOG_LEVEL":       "info",
			},
			want: defaults,
		},
		{
			name: "a flag hides an invalid environment value",
			args: []string{"--timeout", "5m"},
			env:  map[string]string{"REFRESH_TIMEOUT": "soon"},
			want: with(func(o *cmdutil.GlobalOptions) { o.Timeout = 5 * time.Minute }),
		},
		{
			name: "each variable applies alone",
			args: []string{"--max-concurrency", "2"},
			env:  map[string]string{"REFRESH_TIMEOUT": "90s", "REFRESH_MAX_CONCURRENCY": "3"},
			want: with(func(o *cmdutil.GlobalOptions) { o.Timeout = 90 * time.Second; o.MaxConcurrency = 2 }),
		},
		{
			name: "zero timeout",
			args: []string{"--timeout", "0"},
			want: with(func(o *cmdutil.GlobalOptions) { o.Timeout = 0 }),
		},
		{
			name: "verbose",
			args: []string{"--verbose"},
			want: with(func(o *cmdutil.GlobalOptions) { o.LogLevel = slog.LevelDebug }),
		},
		{
			name: "verbose beats the environment",
			args: []string{"--verbose"},
			env:  map[string]string{"REFRESH_LOG_LEVEL": "error"},
			want: with(func(o *cmdutil.GlobalOptions) { o.LogLevel = slog.LevelDebug }),
		},
		{
			name: "verbose hides an invalid environment value",
			args: []string{"--verbose"},
			env:  map[string]string{"REFRESH_LOG_LEVEL": "loud"},
			want: with(func(o *cmdutil.GlobalOptions) { o.LogLevel = slog.LevelDebug }),
		},
		{
			name: "log-level debug hides an invalid environment value",
			args: []string{"--log-level", "debug"},
			env:  map[string]string{"REFRESH_LOG_LEVEL": "loud"},
			want: with(func(o *cmdutil.GlobalOptions) { o.LogLevel = slog.LevelDebug }),
		},
		{
			name: "verbose beats the log-level flag",
			args: []string{"--verbose", "--log-level", "error"},
			want: with(func(o *cmdutil.GlobalOptions) { o.LogLevel = slog.LevelDebug }),
		},
		{
			name: "verbose hides an invalid log-level flag",
			args: []string{"--log-level", "loud", "--verbose"},
			want: with(func(o *cmdutil.GlobalOptions) { o.LogLevel = slog.LevelDebug }),
		},
		{
			name: "verbose=false keeps the environment",
			args: []string{"--verbose=false"},
			env:  map[string]string{"REFRESH_LOG_LEVEL": "error"},
			want: with(func(o *cmdutil.GlobalOptions) { o.LogLevel = slog.LevelError }),
		},
		{
			name: "log level in capitals",
			env:  map[string]string{"REFRESH_LOG_LEVEL": "DEBUG"},
			want: with(func(o *cmdutil.GlobalOptions) { o.LogLevel = slog.LevelDebug }),
		},
		{
			name: "regions and profile",
			args: []string{"-r", "us-east-1", "--region", "eu-west-1", "--profile", "prod"},
			want: with(func(o *cmdutil.GlobalOptions) {
				o.Regions = []string{"us-east-1", "eu-west-1"}
				o.Profile = "prod"
			}),
		},
		{
			name: "regions separated by commas",
			args: []string{"-r", "us-east-1,eu-west-1", "-r", "us-west-2"},
			want: with(func(o *cmdutil.GlobalOptions) { o.Regions = []string{"us-east-1", "eu-west-1", "us-west-2"} }),
		},
	}
	for _, tt := range tests {
		// The global flags work before and after the command name.
		for _, order := range []string{"before the command", "after the command"} {
			t.Run(tt.name+"/"+order, func(t *testing.T) {
				clearEnv(t)
				for name, value := range tt.env {
					t.Setenv(name, value)
				}
				f, _, _, _ := cmdutiltest.NewFactory()
				args := append([]string{"version"}, tt.args...)
				if order == "before the command" {
					args = append(append([]string{}, tt.args...), "version")
				}

				if err := execute(f, args...); err != nil {
					t.Fatalf("refresh %s: %v", strings.Join(args, " "), err)
				}

				if diff := cmp.Diff(tt.want, f.Global); diff != "" {
					t.Errorf("global options (-want +got):\n%s", diff)
				}
			})
		}
	}
}

func TestColor(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		noColor string // NO_COLOR
		term    string
		want    bool
	}{
		{name: "terminal", want: true},
		{name: "no-color flag", args: []string{"--no-color"}},
		{name: "NO_COLOR", noColor: "1"},
		{name: "NO_COLOR with any value", noColor: "false"},
		{name: "dumb terminal", term: "dumb"},
		{name: "no-color=false alone", args: []string{"--no-color=false"}, want: true},
		{name: "no-color=false does not undo NO_COLOR", args: []string{"--no-color=false"}, noColor: "1"},
		{name: "no-color=false does not undo a dumb terminal", args: []string{"--no-color=false"}, term: "dumb"},
		{name: "NO_COLOR on a dumb terminal", noColor: "1", term: "dumb"},
		{name: "flag and NO_COLOR", args: []string{"--no-color"}, noColor: "1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("NO_COLOR", tt.noColor)
			if tt.term != "" {
				t.Setenv("TERM", tt.term)
			}
			f, _, _, _ := cmdutiltest.NewFactory()
			f.IOStreams.OutTTY, f.IOStreams.ErrTTY = true, true

			if err := execute(f, append(tt.args, "version", "--no-update-check")...); err != nil {
				t.Fatalf("version: %v", err)
			}

			if got := f.IOStreams.ColorEnabled(); got != tt.want {
				t.Errorf("ColorEnabled() = %t, want %t", got, tt.want)
			}
			if got := f.IOStreams.ErrColorEnabled(); got != tt.want {
				t.Errorf("ErrColorEnabled() = %t, want %t", got, tt.want)
			}
			if got, want := f.IOStreams.Dumb, tt.term == "dumb"; got != want {
				t.Errorf("Dumb = %t, want %t", got, want)
			}
		})
	}
}

// TestColorNeedsTerminal checks that a stream that is not a terminal gets no
// color, whatever the flags say.
func TestColorNeedsTerminal(t *testing.T) {
	clearEnv(t)
	f, _, _, _ := cmdutiltest.NewFactory()
	f.IOStreams.ErrTTY = true

	if err := execute(f, "version"); err != nil {
		t.Fatalf("version: %v", err)
	}

	if f.IOStreams.ColorEnabled() {
		t.Error("ColorEnabled() = true for a stdout that is not a terminal")
	}
	if !f.IOStreams.ErrColorEnabled() {
		t.Error("ErrColorEnabled() = false for a stderr that is a terminal")
	}
}
