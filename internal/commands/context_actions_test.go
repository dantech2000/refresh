package commands

import (
	"flag"
	"os"
	"testing"

	"github.com/dantech2000/refresh/internal/cliconfig"
	"github.com/urfave/cli/v2"
)

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
	_, err := captureCommandStdout(t, func() error {
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
	if _, err := captureCommandStdout(t, func() error { return list.Action(commandContext()) }); err != nil {
		t.Fatalf("context list: %v", err)
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
	if _, err := captureCommandStdout(t, func() error { return contextListCommand().Action(commandContext()) }); err != nil {
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
