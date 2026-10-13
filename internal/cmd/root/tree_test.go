package root_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/dantech2000/refresh/internal/cmd/root"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
)

// walk calls fn for cmd and every command below it.
func walk(cmd *cobra.Command, fn func(*cobra.Command)) {
	fn(cmd)
	for _, c := range cmd.Commands() {
		walk(c, fn)
	}
}

// newTree returns the command tree as main builds it.
func newTree() *cobra.Command {
	f, _, _, _ := cmdutiltest.NewFactory()
	return root.NewCmdRoot(f)
}

// fromCobra reports whether cobra made cmd: the help command, and the
// completion command with its shells.
func fromCobra(cmd *cobra.Command) bool {
	path := cmd.CommandPath()
	return path == "refresh help" || strings.HasPrefix(path, "refresh completion")
}

func TestCommandsHaveHelpText(t *testing.T) {
	walk(newTree(), func(cmd *cobra.Command) {
		path := cmd.CommandPath()
		if strings.TrimSpace(cmd.Short) == "" {
			t.Errorf("%s: no Short", path)
		}
		if strings.HasSuffix(cmd.Short, ".") {
			t.Errorf("%s: Short ends with a period: %q", path, cmd.Short)
		}
		if fromCobra(cmd) {
			return
		}
		if cmd.Long == "" {
			t.Errorf("%s: no Long", path)
		}
		if cmd.Example == "" {
			t.Errorf("%s: no Example", path)
		}
		for _, line := range strings.Split(cmd.Example, "\n") {
			if line != "" && !strings.HasPrefix(line, "  ") {
				t.Errorf("%s: Example line %q is not indented by two spaces", path, line)
			}
		}
	})
}

// TestRunnableCommandsSetArgs checks structure only: every command is
// runnable, and every command but the root and cobra's help command has a
// non-nil Args. It does not run the commands, so it does not show that Args
// is the right validator.
func TestRunnableCommandsSetArgs(t *testing.T) {
	walk(newTree(), func(cmd *cobra.Command) {
		// Cobra's help command takes the path of any command and sets no Args.
		if cmd.CommandPath() == "refresh help" {
			return
		}
		// The root is the exception: with no Args, cobra itself rejects an
		// unknown command and suggests the nearest one.
		if !cmd.HasParent() {
			if cmd.Args != nil {
				t.Errorf("%s: the root sets Args, which turns off cobra's suggestions", cmd.CommandPath())
			}
			return
		}
		// Cobra prints help and exits 0 for a group that is not runnable,
		// whatever follows the group name.
		if !cmd.Runnable() {
			t.Errorf("%s: not runnable (no Run or RunE)", cmd.CommandPath())
		}
		if cmd.Args == nil {
			t.Errorf("%s: Args is nil", cmd.CommandPath())
		}
	})
}

// The root's PersistentPreRunE completes the global flags, and cobra runs
// only the hook nearest to the command. A subcommand with a hook of its own
// would skip the root's without any error.
func TestOnlyTheRootHasAPersistentPreRun(t *testing.T) {
	walk(newTree(), func(cmd *cobra.Command) {
		if !cmd.HasParent() {
			if cmd.PersistentPreRunE == nil {
				t.Error("the root has no PersistentPreRunE; nothing completes the global flags")
			}
			return
		}
		if cmd.PersistentPreRun != nil || cmd.PersistentPreRunE != nil {
			t.Errorf("%s: sets a PersistentPreRun hook, which replaces the root's", cmd.CommandPath())
		}
	})
}

// TestFlagShorthands compares flags by name: a shorthand letter belongs to
// the one long flag name in the table, and a flag with that name carries the
// letter. It cannot see what a flag does, so a flag with the right name and
// another purpose passes.
func TestFlagShorthands(t *testing.T) {
	// shorthands gives each flag letter its one meaning, from "Global flags" in
	// CONTRACT.md. A flag with one of these names carries the letter, and no
	// other flag may use it.
	shorthands := map[string]string{
		"c": "cluster",
		"n": "nodegroup",
		"a": "addon",
		"o": "output",
		"r": "region",
		"t": "timeout",
		"d": "dry-run",
		"y": "yes",
		"q": "quiet",
		"w": "watch",
		"f": "filter",
		"A": "all-regions",
		"h": "help",
	}
	walk(newTree(), func(cmd *cobra.Command) {
		check := func(fl *pflag.Flag) {
			path := cmd.CommandPath()
			if fl.Shorthand != "" {
				want, ok := shorthands[fl.Shorthand]
				switch {
				case !ok:
					t.Errorf("%s: --%s uses -%s, which has no meaning in CONTRACT.md", path, fl.Name, fl.Shorthand)
				case want != fl.Name:
					t.Errorf("%s: --%s uses -%s, which means --%s", path, fl.Name, fl.Shorthand, want)
				}
			}
			for letter, name := range shorthands {
				if fl.Name == name && fl.Shorthand != letter {
					t.Errorf("%s: --%s has shorthand %q, want -%s", path, fl.Name, fl.Shorthand, letter)
				}
			}
		}
		// Help is not in the flag sets until the command runs.
		cmd.InitDefaultHelpFlag()
		cmd.LocalFlags().VisitAll(check)
		cmd.InheritedFlags().VisitAll(check)
	})
}

func TestHiddenCommands(t *testing.T) {
	var hidden []string
	walk(newTree(), func(cmd *cobra.Command) {
		if cmd.Hidden {
			hidden = append(hidden, cmd.CommandPath())
		}
	})
	if diff := cmp.Diff([]string{"refresh gen-docs"}, hidden); diff != "" {
		t.Errorf("hidden commands (-want +got):\n%s", diff)
	}
}

// TestCommandTree pins the commands of this phase against the tree in
// CONTRACT.md. A command that appears or disappears must change this list.
func TestCommandTree(t *testing.T) {
	want := []string{
		"refresh",
		"refresh addon",
		"refresh addon describe",
		"refresh addon list",
		"refresh addon update",
		"refresh cluster",
		"refresh cluster describe",
		"refresh cluster list",
		"refresh cluster upgrade-check",
		"refresh completion",
		"refresh completion bash",
		"refresh completion fish",
		"refresh completion powershell",
		"refresh completion zsh",
		"refresh context",
		"refresh context add",
		"refresh context current",
		"refresh context list",
		"refresh context remove",
		"refresh context use",
		"refresh gen-docs",
		"refresh help",
		"refresh nodegroup",
		"refresh nodegroup describe",
		"refresh nodegroup list",
		"refresh nodegroup scale",
		"refresh nodegroup update",
		"refresh status",
		"refresh ui",
		"refresh version",
	}
	var got []string
	walk(newTree(), func(cmd *cobra.Command) { got = append(got, cmd.CommandPath()) })
	sort.Strings(got)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("command tree (-want +got):\n%s", diff)
	}
}

func TestEveryCommandIsInAHelpGroup(t *testing.T) {
	tree := newTree()
	for _, cmd := range tree.Commands() {
		if !cmd.Hidden && cmd.GroupID == "" {
			t.Errorf("%s: no help group; it would be listed under \"Additional Commands\"", cmd.CommandPath())
		}
	}
}
