package main

import (
	"strings"
	"testing"
)

// On a leaf command, "h" and "help" are positional values (a cluster may be
// named "h"), not the help subcommand: the action runs. Before, urfave/cli
// printed help and exited 0, so `cluster upgrade-check h` passed without a
// check. --help still prints help, and group commands keep "help".
func TestLeafTreatsHelpAsPositional(t *testing.T) {
	for _, path := range fuzzLeafPaths() {
		for _, word := range []string{"h", "help"} {
			argv := append(append([]string(nil), path...), word)
			ran, _, err := parseOnly(argv)
			if err != nil && strings.Contains(err.Error(), "Required flag") {
				continue // the leaf needs a flag before its action runs
			}
			if !ran {
				t.Errorf("refresh %s: the action did not run (err %v); %q was taken as the help command", strings.Join(argv, " "), err, word)
			}
		}
		if ran, _, _ := parseOnly(append(append([]string(nil), path...), "--help")); ran {
			t.Errorf("refresh %s --help ran the action instead of printing help", strings.Join(path, " "))
		}
	}
	for _, group := range [][]string{{"cluster"}, {"nodegroup"}, {"addon"}, {"context"}} {
		if ran, _, _ := parseOnly(append(group, "help")); ran {
			t.Errorf("refresh %s help ran an action instead of printing help", strings.Join(group, " "))
		}
	}
}
