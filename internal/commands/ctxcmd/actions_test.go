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
	// Empty context list prints via color.Yellow, which writes to the original stdout
	// handle (not the captured pipe). We verify only that it returns no error.
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
	picked, err := pickContext(f)
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
	picked, err = pickContext(f)
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
	if _, err := pickContext(f); err == nil {
		t.Fatal("pickContext should reject empty selection")
	}

	r, w, err = os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	_, _ = w.WriteString("99\n")
	_ = w.Close()
	if _, err := pickContext(f); err == nil {
		t.Fatal("pickContext should reject out-of-range selection")
	}
}
