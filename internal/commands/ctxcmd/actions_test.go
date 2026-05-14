package ctxcmd

import (
	"bytes"
	"flag"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/cliconfig"
	"github.com/urfave/cli/v2"
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

func commandContext(args ...string) *cli.Context {
	set := flag.NewFlagSet("test", flag.ContinueOnError)
	_ = set.Parse(args)
	return cli.NewContext(cli.NewApp(), set, nil)
}

func TestContextActions(t *testing.T) {
	t.Setenv("REFRESH_CONFIG_HOME", t.TempDir())
	t.Setenv("REFRESH_CONTEXT", "")

	if err := runCurrent(commandContext()); err != nil {
		t.Fatalf("runCurrent empty: %v", err)
	}
	if err := runUse(commandContext("prod")); err == nil {
		t.Fatal("runUse should fail with no saved contexts")
	}

	add := contextAddCommand()
	_, err := captureStdout(t, func() error {
		app := cli.NewApp()
		app.Commands = []*cli.Command{add}
		return app.Run([]string{"app", "add", "--cluster", "prod-cluster", "--region", "us-east-1", "--profile", "prod", "--use", "prod"})
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

	if err := runCurrent(commandContext()); err != nil {
		t.Fatalf("runCurrent active: %v", err)
	}
	if err := runUse(commandContext("prod")); err != nil {
		t.Fatalf("runUse prod: %v", err)
	}

	list := contextListCommand()
	out, err := captureStdout(t, func() error { return list.Action(commandContext()) })
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
	app := cli.NewApp()
	app.Commands = []*cli.Command{remove}
	if err := app.Run([]string{"app", "remove", "prod"}); err != nil {
		t.Fatalf("context remove: %v", err)
	}
	if _, err := cliconfig.Load(); err != nil {
		t.Fatalf("config should still load: %v", err)
	}
}

func TestContextActionErrorsAndPicker(t *testing.T) {
	t.Setenv("REFRESH_CONFIG_HOME", t.TempDir())
	t.Setenv("REFRESH_CONTEXT", "")

	if err := contextAddCommand().Action(commandContext()); err == nil {
		t.Fatal("context add should require name")
	}
	if err := contextRemoveCommand().Action(commandContext()); err == nil {
		t.Fatal("context remove should require name")
	}
	// Empty context list prints via color.Yellow, which writes to the original stdout
	// handle (not the captured pipe). We verify only that it returns no error.
	if _, err := captureStdout(t, func() error { return contextListCommand().Action(commandContext()) }); err != nil {
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
