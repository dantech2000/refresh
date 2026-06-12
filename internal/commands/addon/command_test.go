package addon

import (
	"testing"

	"github.com/urfave/cli/v3"
)

func findSub(cmd *cli.Command, name string) *cli.Command {
	for _, sc := range cmd.Commands {
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

func TestCommandStructure(t *testing.T) {
	cmd := Command()
	if cmd.Name != "addon" {
		t.Fatalf("expected name 'addon', got %q", cmd.Name)
	}

	cases := []struct {
		lookup    string
		wantName  string
		wantAlias string
	}{
		{"list", "list", ""},
		{"describe", "describe", "get"},
		{"get", "describe", "get"},
		{"update", "update", ""},
	}
	for _, tc := range cases {
		sc := findSub(cmd, tc.lookup)
		if sc == nil {
			t.Errorf("addon: missing subcommand %q", tc.lookup)
			continue
		}
		if sc.Name != tc.wantName {
			t.Errorf("addon %q: name = %q, want %q", tc.lookup, sc.Name, tc.wantName)
		}
		if tc.wantAlias != "" {
			found := false
			for _, a := range sc.Aliases {
				if a == tc.wantAlias {
					found = true
				}
			}
			if !found {
				t.Errorf("addon %q: missing alias %q (have %v)", tc.lookup, tc.wantAlias, sc.Aliases)
			}
		}
	}
}

func TestUpdateMergedFlags(t *testing.T) {
	upd := findSub(Command(), "update")
	if upd == nil {
		t.Fatal("addon update subcommand missing")
	}
	for _, name := range []string{"all", "cluster", "addon", "version", "parallel", "wait", "wait-timeout", "skip", "format", "dry-run", "dependency-order", "health-check"} {
		if !hasFlag(upd, name) {
			t.Errorf("addon update: missing --%s flag", name)
		}
	}
}

func TestUpdateAllHiddenFlags(t *testing.T) {
	sc := findSub(Command(), "update-all")
	if sc == nil {
		t.Fatal("addon update-all (hidden alias) missing")
	}
	for _, name := range []string{"dependency-order", "health-check"} {
		if !hasFlag(sc, name) {
			t.Errorf("addon update-all: missing --%s flag", name)
		}
	}
}

func TestUpdateAllHiddenAlias(t *testing.T) {
	sc := findSub(Command(), "update-all")
	if sc == nil {
		t.Fatal("addon update-all (hidden alias) missing — backward compat broken")
	}
	if !sc.Hidden {
		t.Errorf("addon update-all should be Hidden:true (no longer the canonical name)")
	}
	if sc.Action == nil {
		t.Errorf("addon update-all: nil Action")
	}
}
