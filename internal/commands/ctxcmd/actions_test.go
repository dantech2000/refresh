package ctxcmd

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/cliconfig"
	"github.com/urfave/cli/v3"
)

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = original })

	callErr := fn()
	_ = w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String(), callErr
}

// runAction runs an action function through a real command so the
// arguments are parsed (v3 removed cli.NewContext / direct flag.FlagSet setup).
func runAction(action cli.ActionFunc, args ...string) error {
	cmd := &cli.Command{Name: "test", Action: action}
	return cmd.Run(context.Background(), append([]string{"test"}, args...))
}

// runCommand runs cmd as a root command with the given argv tokens.
func runCommand(cmd *cli.Command, args ...string) error {
	return cmd.Run(context.Background(), append([]string{cmd.Name}, args...))
}

func TestContextActions(t *testing.T) {
	t.Setenv("REFRESH_CONFIG_HOME", t.TempDir())
	t.Setenv("REFRESH_CONTEXT", "")

	if err := runAction(runCurrent); err != nil {
		t.Fatalf("runCurrent empty: %v", err)
	}
	if err := runAction(runUse, "prod"); err == nil {
		t.Fatal("runUse should fail with no saved contexts")
	}

	add := contextAddCommand()
	_, err := captureStdout(t, func() error {
		root := &cli.Command{Name: "app", Commands: []*cli.Command{add}}
		return root.Run(context.Background(), []string{"app", "add", "--cluster", "prod-cluster", "--region", "us-east-1", "--profile", "prod", "--use", "prod"})
	})
	if err != nil {
		t.Fatalf("context add: %v", err)
	}
	f, err := cliconfig.Load()
	if err != nil {
		t.Fatal(err)
	}
	if f.Current != "prod" || f.Contexts["prod"].Cluster != "prod-cluster" {
		t.Fatalf("saved context = %+v", f)
	}

	if err := runAction(runCurrent); err != nil {
		t.Fatalf("runCurrent active: %v", err)
	}
	if err := runAction(runUse, "prod"); err != nil {
		t.Fatalf("runUse prod: %v", err)
	}

	out, err := captureStdout(t, func() error { return runCommand(contextListCommand()) })
	if err != nil {
		t.Fatalf("context list: %v", err)
	}
	if !strings.Contains(out, "prod") || !strings.Contains(out, "prod-cluster") {
		t.Errorf("context list output missing expected context: %q", out)
	}
	if !strings.Contains(out, "us-east-1") {
		t.Errorf("context list output missing region 'us-east-1': %q", out)
	}

	remove := contextRemoveCommand()
	root := &cli.Command{Name: "app", Commands: []*cli.Command{remove}}
	if err := root.Run(context.Background(), []string{"app", "remove", "prod"}); err != nil {
		t.Fatalf("context remove: %v", err)
	}
	if _, err := cliconfig.Load(); err != nil {
		t.Fatalf("config should still load: %v", err)
	}
}

// The "no context" hints go to stderr, so stdout stays empty for scripts.
// An unknown REFRESH_CONTEXT fails `current` and is a warning in `list`.
func TestContextHintsAndUnknownEnvContext(t *testing.T) {
	t.Setenv("REFRESH_CONFIG_HOME", t.TempDir())
	t.Setenv("REFRESH_CONTEXT", "")

	out, err := captureStdout(t, func() error { return runAction(runCurrent) })
	if err != nil || out != "" {
		t.Errorf("current with no context: stdout %q, err %v; want empty stdout, no error", out, err)
	}
	out, err = captureStdout(t, func() error { return runCommand(contextListCommand()) })
	if err != nil || out != "" {
		t.Errorf("list with no contexts: stdout %q, err %v; want empty stdout, no error", out, err)
	}

	f := &cliconfig.File{Contexts: map[string]cliconfig.Context{}}
	for _, n := range []string{"prod", "staging"} {
		if err := f.Set(n, cliconfig.Context{Cluster: n + "-eks"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Use("prod"); err != nil {
		t.Fatal(err)
	}
	if err := cliconfig.Save(f); err != nil {
		t.Fatal(err)
	}

	t.Setenv("REFRESH_CONTEXT", "stagee")
	out, err = captureStdout(t, func() error { return runAction(runCurrent) })
	if err == nil || !strings.Contains(err.Error(), `unknown context "stagee"`) || strings.Contains(out, "prod") {
		t.Errorf("current with REFRESH_CONTEXT=stagee: stdout %q, err %v; want an unknown-context error and no prod", out, err)
	}
	out, err = captureStdout(t, func() error { return runCommand(contextListCommand()) })
	if err != nil || !strings.Contains(out, "staging") || strings.Contains(out, "* ") {
		t.Errorf("list with REFRESH_CONTEXT=stagee: stdout %q, err %v; want every context and none marked active", out, err)
	}
}

// captureStderr is captureStdout for os.Stderr (ui.Stderr writes to it).
func captureStderr(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	original := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = original })

	callErr := fn()
	_ = w.Close()
	os.Stderr = original
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String(), callErr
}

// `refresh use` saves the new current context, but REFRESH_CONTEXT still
// wins in this shell. The command must say so instead of only "Switched".
func TestUseWarnsWhenRefreshContextOverrides(t *testing.T) {
	t.Setenv("REFRESH_CONFIG_HOME", t.TempDir())
	f := &cliconfig.File{Current: "staging", Contexts: map[string]cliconfig.Context{
		"prod":    {Cluster: "prod-eks"},
		"staging": {Cluster: "staging-eks"},
	}}
	if err := cliconfig.Save(f); err != nil {
		t.Fatal(err)
	}

	t.Setenv("REFRESH_CONTEXT", "staging")
	stderr, err := captureStderr(t, func() error {
		_, err := captureStdout(t, func() error { return runAction(runUse, "prod") })
		return err
	})
	if err != nil {
		t.Fatalf("use prod: %v", err)
	}
	if !strings.Contains(stderr, `REFRESH_CONTEXT=staging`) || !strings.Contains(stderr, `unset REFRESH_CONTEXT`) {
		t.Errorf("stderr = %q, want a note that REFRESH_CONTEXT=staging still wins", stderr)
	}
	if saved, _ := cliconfig.Load(); saved.Current != "prod" {
		t.Errorf("saved current = %q, want prod", saved.Current)
	}

	t.Setenv("REFRESH_CONTEXT", "prod")
	stderr, err = captureStderr(t, func() error {
		_, err := captureStdout(t, func() error { return runAction(runUse, "prod") })
		return err
	})
	if err != nil || strings.Contains(stderr, "REFRESH_CONTEXT") {
		t.Errorf("use prod with REFRESH_CONTEXT=prod: stderr %q, err %v; want no note", stderr, err)
	}
}

// The picker marks the context that commands use now (Active), the same
// one `context list` marks, not the saved current pointer.
func TestPickerMarksActiveContext(t *testing.T) {
	t.Setenv("REFRESH_CONTEXT", "a")
	f := &cliconfig.File{Current: "b", Contexts: map[string]cliconfig.Context{
		"a": {Cluster: "ca"},
		"b": {Cluster: "cb"},
	}}
	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = oldStdin })
	_, _ = w.WriteString("a\n")
	_ = w.Close()

	out, err := captureStdout(t, func() error {
		_, perr := pickContext(t.Context(), f)
		return perr
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(out, "\n") {
		marked := strings.Contains(line, "* ")
		switch {
		case strings.Contains(line, "1) a") && !marked:
			t.Errorf("line %q: want the active context a marked", line)
		case strings.Contains(line, "2) b") && marked:
			t.Errorf("line %q: want the saved current b unmarked while REFRESH_CONTEXT=a", line)
		}
	}
}

func TestContextActionErrorsAndPicker(t *testing.T) {
	t.Setenv("REFRESH_CONFIG_HOME", t.TempDir())
	t.Setenv("REFRESH_CONTEXT", "")

	// --cluster is required, so satisfy flag parsing and hit the name check.
	if err := runCommand(contextAddCommand(), "--cluster", "x"); err == nil {
		t.Fatal("context add should require name")
	}
	if err := runCommand(contextRemoveCommand()); err == nil {
		t.Fatal("context remove should require name")
	}
	// An empty context list prints its hint to stderr; stdout stays empty.
	if _, err := captureStdout(t, func() error { return runCommand(contextListCommand()) }); err != nil {
		t.Fatalf("context list empty: %v", err)
	}

	f := &cliconfig.File{Current: "b", Contexts: map[string]cliconfig.Context{
		"a": {Cluster: "ca", Region: "us-east-1"},
		"b": {Cluster: "cb"},
	}}

	oldStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = oldStdin })
	_, _ = w.WriteString("1\n")
	_ = w.Close()
	picked, err := pickContext(t.Context(), f)
	if err != nil || picked != "a" {
		t.Fatalf("pickContext numeric = %q, %v", picked, err)
	}

	r, w, err = os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	_, _ = w.WriteString("b\n")
	_ = w.Close()
	picked, err = pickContext(t.Context(), f)
	if err != nil || picked != "b" {
		t.Fatalf("pickContext name = %q, %v", picked, err)
	}

	r, w, err = os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	_, _ = w.WriteString("\n")
	_ = w.Close()
	if _, err := pickContext(t.Context(), f); err == nil {
		t.Fatal("pickContext should reject empty selection")
	}

	r, w, err = os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	_, _ = w.WriteString("99\n")
	_ = w.Close()
	if _, err := pickContext(t.Context(), f); err == nil {
		t.Fatal("pickContext should reject out-of-range selection")
	}
}
