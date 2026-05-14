package ctxcmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/urfave/cli/v2"

	"github.com/dantech2000/refresh/internal/cliconfig"
)

func findSub(cmd *cli.Command, name string) *cli.Command {
	for _, sc := range cmd.Subcommands {
		if sc.Name == name {
			return sc
		}
		for _, a := range sc.Aliases {
			if a == name {
				return sc
			}
		}
	}
	return nil
}

func hasFlag(cmd *cli.Command, name string) bool {
	for _, f := range cmd.Flags {
		for _, n := range f.Names() {
			if n == name {
				return true
			}
		}
	}
	return false
}

func TestUseCommandStructure(t *testing.T) {
	cmd := UseCommand()
	if cmd.Name != "use" {
		t.Errorf("UseCommand name = %q, want use", cmd.Name)
	}
	if cmd.Action == nil {
		t.Error("UseCommand has nil Action")
	}
}

func TestCurrentCommandStructure(t *testing.T) {
	cmd := CurrentCommand()
	if cmd.Name != "current" {
		t.Errorf("CurrentCommand name = %q, want current", cmd.Name)
	}
	if cmd.Action == nil {
		t.Error("CurrentCommand has nil Action")
	}
}

func TestContextCommandStructure(t *testing.T) {
	cmd := ContextCommand()
	if cmd.Name != "context" {
		t.Fatalf("ContextCommand name = %q, want context", cmd.Name)
	}
	hasCtxAlias := false
	for _, a := range cmd.Aliases {
		if a == "ctx" {
			hasCtxAlias = true
		}
	}
	if !hasCtxAlias {
		t.Error("context: missing 'ctx' alias")
	}

	for _, want := range []string{"list", "add", "remove"} {
		if findSub(cmd, want) == nil {
			t.Errorf("context: missing subcommand %q", want)
		}
	}
	if findSub(cmd, "ls") == nil {
		t.Error("context: missing 'ls' alias for list")
	}
	if findSub(cmd, "rm") == nil {
		t.Error("context: missing 'rm' alias for remove")
	}

	add := findSub(cmd, "add")
	for _, name := range []string{"cluster", "region", "profile", "use"} {
		if !hasFlag(add, name) {
			t.Errorf("context add: missing --%s flag", name)
		}
	}
}

func TestOrDash(t *testing.T) {
	if got := orDash(""); got != "-" {
		t.Errorf("orDash(%q) = %q, want %q", "", got, "-")
	}
	if got := orDash("hello"); got != "hello" {
		t.Errorf("orDash(%q) = %q, want %q", "hello", got, "hello")
	}
}

func TestContextEndToEnd(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("REFRESH_CONFIG_HOME", dir)
	t.Setenv("REFRESH_CONTEXT", "")

	f := &cliconfig.File{Contexts: map[string]cliconfig.Context{}}
	if err := f.Set("dev", cliconfig.Context{Cluster: "dev-eks", Region: "us-east-1"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Use("dev"); err != nil {
		t.Fatal(err)
	}
	if err := cliconfig.Save(f); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "context.yaml")); err != nil {
		t.Fatalf("context.yaml not written: %v", err)
	}

	loaded, err := cliconfig.Load()
	if err != nil {
		t.Fatal(err)
	}
	name, ctx, ok := loaded.Active()
	if !ok || name != "dev" || ctx.Cluster != "dev-eks" || ctx.Region != "us-east-1" {
		t.Errorf("active = (%s, %+v, %v), want dev/dev-eks/us-east-1", name, ctx, ok)
	}
}
