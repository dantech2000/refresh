package commands

import (
	"testing"

	"github.com/urfave/cli/v2"
)

// findSub returns the subcommand whose Name or Aliases match `name`, or nil.
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

func TestClusterCommandStructure(t *testing.T) {
	cmd := ClusterCommand()
	if cmd.Name != "cluster" {
		t.Fatalf("expected name 'cluster', got %q", cmd.Name)
	}

	cases := []struct {
		lookup     string
		wantName   string
		wantAlias  string
		mustExist  bool
	}{
		{"list", "list", "", true},
		{"describe", "describe", "get", true},
		{"get", "describe", "get", true},
		{"diff", "diff", "compare", true},
		{"compare", "diff", "compare", true},
	}
	for _, tc := range cases {
		sc := findSub(cmd, tc.lookup)
		if sc == nil && tc.mustExist {
			t.Errorf("cluster: missing subcommand %q", tc.lookup)
			continue
		}
		if sc.Name != tc.wantName {
			t.Errorf("cluster %q: name = %q, want %q", tc.lookup, sc.Name, tc.wantName)
		}
		if tc.wantAlias != "" {
			found := false
			for _, a := range sc.Aliases {
				if a == tc.wantAlias {
					found = true
				}
			}
			if !found {
				t.Errorf("cluster %q: missing alias %q (have %v)", tc.lookup, tc.wantAlias, sc.Aliases)
			}
		}
	}
}

func TestNodegroupCommandStructure(t *testing.T) {
	cmd := NodegroupCommand()
	if cmd.Name != "nodegroup" {
		t.Fatalf("expected name 'nodegroup', got %q", cmd.Name)
	}
	hasNgAlias := false
	for _, a := range cmd.Aliases {
		if a == "ng" {
			hasNgAlias = true
		}
	}
	if !hasNgAlias {
		t.Errorf("nodegroup: missing 'ng' alias")
	}

	cases := []struct {
		lookup    string
		wantName  string
		wantAlias string
	}{
		{"list", "list", ""},
		{"describe", "describe", "get"},
		{"get", "describe", "get"},
		{"scale", "scale", ""},
		{"update", "update", "update-ami"},
		{"update-ami", "update", "update-ami"},
	}
	for _, tc := range cases {
		sc := findSub(cmd, tc.lookup)
		if sc == nil {
			t.Errorf("nodegroup: missing subcommand %q", tc.lookup)
			continue
		}
		if sc.Name != tc.wantName {
			t.Errorf("nodegroup %q: name = %q, want %q", tc.lookup, sc.Name, tc.wantName)
		}
		if tc.wantAlias != "" {
			found := false
			for _, a := range sc.Aliases {
				if a == tc.wantAlias {
					found = true
				}
			}
			if !found {
				t.Errorf("nodegroup %q: missing alias %q (have %v)", tc.lookup, tc.wantAlias, sc.Aliases)
			}
		}
	}
}

func TestAddonCommandStructure(t *testing.T) {
	cmd := AddonCommand()
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

func TestAddonUpdateMergedFlags(t *testing.T) {
	upd := findSub(AddonCommand(), "update")
	if upd == nil {
		t.Fatal("addon update subcommand missing")
	}
	for _, name := range []string{"all", "cluster", "addon", "version", "parallel", "wait", "wait-timeout", "skip", "format", "dry-run", "dependency-order", "health-check"} {
		if !hasFlag(upd, name) {
			t.Errorf("addon update: missing --%s flag", name)
		}
	}
}

func TestAddonUpdateAllHiddenFlags(t *testing.T) {
	sc := findSub(AddonCommand(), "update-all")
	if sc == nil {
		t.Fatal("addon update-all (hidden alias) missing")
	}
	for _, name := range []string{"dependency-order", "health-check"} {
		if !hasFlag(sc, name) {
			t.Errorf("addon update-all: missing --%s flag", name)
		}
	}
}

func TestAddonUpdateAllHiddenAlias(t *testing.T) {
	sc := findSub(AddonCommand(), "update-all")
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

// Ensures every group registered in main is wired with its expected name.
func TestRootGroupNames(t *testing.T) {
	tests := []struct {
		got  *cli.Command
		want string
	}{
		{ClusterCommand(), "cluster"},
		{NodegroupCommand(), "nodegroup"},
		{AddonCommand(), "addon"},
	}
	for _, tt := range tests {
		if tt.got.Name != tt.want {
			t.Errorf("root group: got %q, want %q", tt.got.Name, tt.want)
		}
	}
}
