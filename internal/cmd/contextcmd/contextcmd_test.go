package contextcmd_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"go.uber.org/goleak"

	"github.com/dantech2000/refresh/internal/cmd/root"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
	"github.com/dantech2000/refresh/internal/contexts"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// twoContexts is a store with prod and stage, where prod is current.
func twoContexts() *contexts.File {
	return &contexts.File{
		Current: "prod",
		Contexts: map[string]contexts.Context{
			"prod":  {Cluster: "prod-eks", Region: "us-east-1", Profile: "prod-admin"},
			"stage": {Cluster: "stage-eks"},
		},
	}
}

// step is one run of refresh against the store of the test.
type step struct {
	args       []string
	env        string // REFRESH_CONTEXT
	stdin      string
	wantOut    string
	wantErrOut string
	wantErr    string // the error that main prints; exit code 1
}

func TestContextCommands(t *testing.T) {
	tests := []struct {
		name     string
		file     *contexts.File // nil means no file
		steps    []step
		wantFile *contexts.File
	}{
		{
			name: "empty store",
			steps: []step{
				{
					args:       []string{"current"},
					wantErrOut: "No active context. Set one with: refresh use <name>\n",
				},
				{
					args:       []string{"context", "list"},
					wantErrOut: "No saved contexts. Add one with: refresh context add <name> --cluster <cluster>\n",
				},
				{
					args:    []string{"use", "prod"},
					wantErr: "no contexts saved; add one with: refresh context add <name> --cluster <cluster> [--region <region>] [--profile <profile>]",
				},
				{
					args:    []string{"use"},
					wantErr: "no contexts saved; add one with: refresh context add <name> --cluster <cluster> [--region <region>] [--profile <profile>]",
				},
				{args: []string{"context", "remove", "prod"}, wantErr: `unknown context "prod"`},
			},
			wantFile: &contexts.File{Contexts: map[string]contexts.Context{}},
		},
		{
			name: "add, use, and remove",
			steps: []step{
				{
					args:    []string{"context", "add", "prod", "--cluster", "prod-eks", "--region", "us-east-1", "--profile", "prod-admin"},
					wantOut: "Saved context \"prod\"\n",
				},
				{
					// Saving a context does not make it active.
					args:       []string{"current"},
					wantErrOut: "No active context. Set one with: refresh use <name>\n",
				},
				{
					args:    []string{"context", "add", "stage", "-c", "stage-eks", "--use"},
					wantOut: "Saved context \"stage\"\n",
				},
				{args: []string{"current"}, wantOut: "stage  cluster=stage-eks  region=-  profile=-\n"},
				{
					args:    []string{"use", "prod"},
					wantOut: "Switched to context \"prod\" (cluster=prod-eks region=us-east-1 profile=prod-admin)\n",
				},
				{
					args: []string{"context", "list"},
					wantOut: "* prod                 cluster=prod-eks region=us-east-1 profile=prod-admin\n" +
						"  stage                cluster=stage-eks region=- profile=-\n",
				},
				{
					args:    []string{"use", "-"},
					wantOut: "Switched to context \"stage\" (cluster=stage-eks region=- profile=-)\n",
				},
				{
					args:    []string{"use", "-"},
					wantOut: "Switched to context \"prod\" (cluster=prod-eks region=us-east-1 profile=prod-admin)\n",
				},
				{args: []string{"context", "remove", "prod"}, wantOut: "Removed context \"prod\"\n"},
				{
					args:       []string{"current"},
					wantErrOut: "No active context. Set one with: refresh use <name>\n",
				},
			},
			wantFile: &contexts.File{
				Previous: "stage",
				Contexts: map[string]contexts.Context{"stage": {Cluster: "stage-eks"}},
			},
		},
		{
			name: "add replaces a context and keeps the current one",
			file: twoContexts(),
			steps: []step{
				{
					args:    []string{"context", "add", "stage", "-c", "stage-2", "-r", "eu-west-1"},
					wantOut: "Saved context \"stage\"\n",
				},
			},
			wantFile: &contexts.File{
				Current: "prod",
				Contexts: map[string]contexts.Context{
					"prod":  {Cluster: "prod-eks", Region: "us-east-1", Profile: "prod-admin"},
					"stage": {Cluster: "stage-2", Region: "eu-west-1"},
				},
			},
		},
		{
			name: "aliases",
			file: twoContexts(),
			steps: []step{
				{
					args: []string{"ctx", "ls"},
					wantOut: "* prod                 cluster=prod-eks region=us-east-1 profile=prod-admin\n" +
						"  stage                cluster=stage-eks region=- profile=-\n",
				},
				{args: []string{"ctx", "rm", "stage"}, wantOut: "Removed context \"stage\"\n"},
				{args: []string{"context", "delete", "prod"}, wantOut: "Removed context \"prod\"\n"},
			},
			wantFile: &contexts.File{Contexts: map[string]contexts.Context{}},
		},
		{
			name: "REFRESH_CONTEXT selects the context for one shell",
			file: twoContexts(),
			steps: []step{
				{args: []string{"current"}, env: "stage", wantOut: "stage  cluster=stage-eks  region=-  profile=-\n"},
				{
					args: []string{"context", "list"}, env: "stage",
					wantOut: "  prod                 cluster=prod-eks region=us-east-1 profile=prod-admin\n" +
						"* stage                cluster=stage-eks region=- profile=-\n",
				},
				{
					// "use" saves the new context, and REFRESH_CONTEXT still
					// wins in this shell. The command says so.
					args: []string{"use", "prod"}, env: "stage",
					wantOut:    "Switched to context \"prod\" (cluster=prod-eks region=us-east-1 profile=prod-admin)\n",
					wantErrOut: "warning: REFRESH_CONTEXT=stage overrides the saved context in this shell; unset REFRESH_CONTEXT to use \"prod\" here\n",
				},
				{
					args: []string{"use", "prod"}, env: "prod",
					wantOut: "Switched to context \"prod\" (cluster=prod-eks region=us-east-1 profile=prod-admin)\n",
				},
			},
			wantFile: twoContexts(),
		},
		{
			name: "unknown REFRESH_CONTEXT",
			file: twoContexts(),
			steps: []step{
				{
					// Never a silent fallback to prod.
					args: []string{"current"}, env: "stagee",
					wantErr: `unknown context "stagee" from REFRESH_CONTEXT; known: prod, stage`,
				},
				{
					// The list is how the user finds the right name: it
					// warns, marks no context, and succeeds.
					args: []string{"context", "list"}, env: "stagee",
					wantOut: "  prod                 cluster=prod-eks region=us-east-1 profile=prod-admin\n" +
						"  stage                cluster=stage-eks region=- profile=-\n",
					wantErrOut: "warning: unknown context \"stagee\" from REFRESH_CONTEXT; known: prod, stage\n",
				},
			},
			wantFile: twoContexts(),
		},
		{
			name: "errors change nothing",
			file: twoContexts(),
			steps: []step{
				{args: []string{"use", "nope"}, wantErr: `unknown context "nope"`},
				{args: []string{"use", "-"}, wantErr: "no previous context to switch to"},
				{args: []string{"use", "prod", "stage"}, wantErr: "accepts at most 1 arg(s), received 2"},
				{args: []string{"current", "prod"}, wantErr: `unknown command "prod" for "refresh current"`},
				{args: []string{"context", "remove"}, wantErr: "accepts 1 arg(s), received 0"},
				{args: []string{"context", "remove", "nope"}, wantErr: `unknown context "nope"`},
				{args: []string{"context", "add", "--cluster", "x"}, wantErr: "accepts 1 arg(s), received 0"},
				{args: []string{"context", "add", "new"}, wantErr: `required flag(s) "cluster" not set`},
				{args: []string{"context", "add", "new", "--cluster", ""}, wantErr: "cluster is required"},
				{args: []string{"context", "add", "", "--cluster", "x"}, wantErr: "context name is required"},
				{args: []string{"context", "list", "extra"}, wantErr: `unknown command "extra" for "refresh context list"`},
				{args: []string{"context", "bogus"}, wantErr: `unknown command "bogus" for "refresh context"`},
				{args: []string{"context", "list", "-o", "json"}, wantErr: "unknown shorthand flag: 'o' in -o"},
			},
			wantFile: twoContexts(),
		},
		{
			name: "use without a name asks",
			file: twoContexts(),
			steps: []step{
				{
					args: []string{"use"}, stdin: "2\n",
					wantOut: "Switched to context \"stage\" (cluster=stage-eks region=- profile=-)\n",
					wantErrOut: "Available contexts:\n" +
						"  * 1) prod                 cluster=prod-eks region=us-east-1\n" +
						"    2) stage                cluster=stage-eks region=-\n" +
						"Select context [number or name]: ",
				},
				{
					args: []string{"use"}, stdin: "prod\n",
					wantOut: "Switched to context \"prod\" (cluster=prod-eks region=us-east-1 profile=prod-admin)\n",
					wantErrOut: "Available contexts:\n" +
						"    1) prod                 cluster=prod-eks region=us-east-1\n" +
						"  * 2) stage                cluster=stage-eks region=-\n" +
						"Select context [number or name]: ",
				},
				{
					// The marker follows REFRESH_CONTEXT, as "context list" does.
					args: []string{"use"}, env: "stage", stdin: "1\n",
					wantOut: "Switched to context \"prod\" (cluster=prod-eks region=us-east-1 profile=prod-admin)\n",
					wantErrOut: "Available contexts:\n" +
						"    1) prod                 cluster=prod-eks region=us-east-1\n" +
						"  * 2) stage                cluster=stage-eks region=-\n" +
						"Select context [number or name]: " +
						"warning: REFRESH_CONTEXT=stage overrides the saved context in this shell; unset REFRESH_CONTEXT to use \"prod\" here\n",
				},
			},
			wantFile: &contexts.File{
				Current:  "prod",
				Previous: "stage",
				Contexts: twoContexts().Contexts,
			},
		},
		{
			name: "use without a name, bad answers",
			file: twoContexts(),
			steps: []step{
				{args: []string{"use"}, stdin: "\n", wantErr: "no selection made"},
				{args: []string{"use"}, stdin: "", wantErr: "no selection made"},
				{args: []string{"use"}, stdin: "99\n", wantErr: "selection 99 out of range"},
				{args: []string{"use"}, stdin: "0\n", wantErr: "selection 0 out of range"},
				{args: []string{"use"}, stdin: "nope\n", wantErr: `unknown context "nope"`},
			},
			wantFile: twoContexts(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, _, _, _ := cmdutiltest.NewFactory()
			store := cmdutiltest.NewContextStore(t, f, tt.file)
			for i, s := range tt.steps {
				runStep(t, store, s, fmt.Sprintf("step %d (refresh %s)", i+1, strings.Join(s.args, " ")))
			}

			got, err := store.Load()
			if err != nil {
				t.Fatalf("reading the store after the steps: %v", err)
			}
			if diff := cmp.Diff(tt.wantFile, got); diff != "" {
				t.Errorf("store after the steps (-want +got):\n%s", diff)
			}
		})
	}
}

// runStep runs one step against store and checks its output.
func runStep(t *testing.T, store *contexts.Store, s step, label string) {
	t.Helper()
	t.Setenv(contexts.EnvContext, s.env)
	f, in, out, errOut := cmdutiltest.NewFactory()
	f.Contexts = func() (*contexts.Store, error) { return store, nil }
	in.WriteString(s.stdin)
	cmd := root.NewCmdRoot(f)
	cmd.SetArgs(s.args)

	err := cmd.ExecuteContext(context.Background())

	if s.wantErr == "" {
		if err != nil {
			t.Fatalf("%s: error = %v", label, err)
		}
		if diff := cmp.Diff(s.wantOut, out.String()); diff != "" {
			t.Errorf("%s: stdout (-want +got):\n%s", label, diff)
		}
		if diff := cmp.Diff(s.wantErrOut, errOut.String()); diff != "" {
			t.Errorf("%s: stderr (-want +got):\n%s", label, diff)
		}
		return
	}
	if err == nil || err.Error() != s.wantErr {
		t.Errorf("%s: error = %v, want %q", label, err, s.wantErr)
	}
	if code := cmdutil.ExitCode(err); code != cmdutil.ExitFailure {
		t.Errorf("%s: exit code = %d, want 1", label, code)
	}
	if out.Len() != 0 {
		t.Errorf("%s: stdout = %q, want nothing with an error", label, out.String())
	}
	// An error leaves stderr to main, except for the prompt that came
	// before the error.
	if !isPrompt(s.args) && errOut.Len() != 0 {
		t.Errorf("%s: stderr = %q, want nothing", label, errOut.String())
	}
}

// isPrompt reports whether args is "use" with no name, which asks on stderr.
func isPrompt(args []string) bool {
	return len(args) == 1 && args[0] == "use"
}

func TestUseInterrupted(t *testing.T) {
	t.Setenv(contexts.EnvContext, "")
	f, _, out, _ := cmdutiltest.NewFactory()
	store := cmdutiltest.NewContextStore(t, f, twoContexts())
	// Input that never arrives: only the context ends the wait.
	reader, writer := newPipe(t)
	f.IOStreams.In = reader
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := root.NewCmdRoot(f)
	cmd.SetArgs([]string{"use"})

	err := cmd.ExecuteContext(ctx)
	// The end of the input stops the read that the command left behind.
	_ = writer.Close()

	if cmdutil.ExitCode(err) != cmdutil.ExitFailure {
		t.Errorf("error = %v, want an interrupt with exit code 1", err)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", out.String())
	}
	got, loadErr := store.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if diff := cmp.Diff(twoContexts(), got); diff != "" {
		t.Errorf("store after an interrupt (-want +got):\n%s", diff)
	}
}

func TestStoreErrors(t *testing.T) {
	commands := [][]string{
		{"current"}, {"use", "prod"}, {"context", "list"},
		{"context", "add", "prod", "-c", "x"}, {"context", "remove", "prod"},
	}
	for _, args := range commands {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Setenv(contexts.EnvContext, "")
			f, _, out, _ := cmdutiltest.NewFactory()
			store := cmdutiltest.NewContextStore(t, f, nil)
			writeFile(t, store.Path, "contexts: [not, a, map")
			cmd := root.NewCmdRoot(f)
			cmd.SetArgs(args)

			err := cmd.ExecuteContext(context.Background())

			if err == nil || !strings.Contains(err.Error(), "parsing "+store.Path) {
				t.Errorf("error = %v, want a parse error that names the file", err)
			}
			if out.Len() != 0 {
				t.Errorf("stdout = %q, want nothing", out.String())
			}
			// A file that cannot be read is never replaced.
			if got := readFile(t, store.Path); got != "contexts: [not, a, map" {
				t.Errorf("file content = %q, want it unchanged", got)
			}
		})
	}
}

func TestCompletion(t *testing.T) {
	const names = "prod\tcluster=prod-eks region=us-east-1 profile=prod-admin\n" +
		"stage\tcluster=stage-eks region=- profile=-\n"
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "use", args: []string{"use", ""}, want: names},
		{name: "use with a prefix", args: []string{"use", "st"}, want: names}, // the shell filters
		{name: "use, second argument", args: []string{"use", "prod", ""}, want: ""},
		{name: "context remove", args: []string{"context", "remove", ""}, want: names},
		{name: "context rm", args: []string{"context", "rm", ""}, want: names},
		{name: "context remove, second argument", args: []string{"context", "remove", "prod", ""}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(contexts.EnvContext, "")
			f, _, out, _ := cmdutiltest.NewFactory()
			cmdutiltest.NewContextStore(t, f, twoContexts())
			cmd := root.NewCmdRoot(f)
			cmd.SetArgs(append([]string{"__complete"}, tt.args...))

			if err := cmd.ExecuteContext(context.Background()); err != nil {
				t.Fatalf("completion: %v", err)
			}

			// Cobra ends the answer with the directive line.
			got, _, _ := strings.Cut(out.String(), ":")
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("completions (-want +got):\n%s", diff)
			}
		})
	}
}
