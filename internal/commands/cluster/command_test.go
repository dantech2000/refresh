package cluster

import (
	"testing"
)

func TestClusterCommandStructure(t *testing.T) {
	cmd := Command()
	if cmd.Name != "cluster" {
		t.Fatalf("expected name 'cluster', got %q", cmd.Name)
	}

	findSubcmd := func(name string) bool {
		for _, sc := range cmd.Commands {
			if sc.Name == name {
				return true
			}
			for _, a := range sc.Aliases {
				if a == name {
					return true
				}
			}
		}
		return false
	}

	for _, name := range []string{"list", "describe", "get"} {
		if !findSubcmd(name) {
			t.Errorf("cluster: missing subcommand or alias %q", name)
		}
	}
}
