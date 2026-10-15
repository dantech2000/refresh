package root_test

import (
	"os"
	"path/filepath"
	"slices"
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

// fromCobra reports whether cmd keeps the help text of cobra: the help
// command. The completion command is cobra's too, and has refresh's text.
func fromCobra(cmd *cobra.Command) bool {
	return cmd.CommandPath() == "refresh help"
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
// another purpose passes. Every letter in the table is used by a flag: a
// letter that no flag has is not reserved.
func TestFlagShorthands(t *testing.T) {
	// shorthands gives each flag letter its one meaning, from "Global flags" in
	// CONTRACT.md. A flag with one of these names carries the letter, and no
	// other flag may use it.
	shorthands := map[string]string{
		"c": "cluster",
		"n": "nodegroup",
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
	used := map[string]bool{}
	walk(newTree(), func(cmd *cobra.Command) {
		check := func(fl *pflag.Flag) {
			path := cmd.CommandPath()
			if fl.Shorthand != "" {
				used[fl.Shorthand] = true
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
	for letter, name := range shorthands {
		if !used[letter] {
			t.Errorf("-%s is reserved for --%s, and no flag uses it", letter, name)
		}
	}
}

// A flag of a command with the name or the letter of a global flag hides
// the global one for that command: "refresh -t 5s cluster list" would run
// with the default of the local flag, and cobra reports no error. Only
// the commands in the table may do it, each for the reason given.
func TestNoCommandFlagShadowsGlobalFlag(t *testing.T) {
	allowed := map[string]string{
		"refresh context add --region":  "it is the region that the context stores, not the region of this run",
		"refresh context add --profile": "it is the profile that the context stores, not the profile of this run",
	}
	tree := newTree()
	global := tree.PersistentFlags()
	var got []string
	walk(tree, func(cmd *cobra.Command) {
		if cmd == tree {
			return
		}
		cmd.LocalFlags().VisitAll(func(fl *pflag.Flag) {
			g := global.Lookup(fl.Name)
			if g == nil && fl.Shorthand != "" {
				g = global.ShorthandLookup(fl.Shorthand)
			}
			if g == nil || g == fl {
				return
			}
			key := cmd.CommandPath() + " --" + fl.Name
			got = append(got, key)
			if allowed[key] == "" {
				t.Errorf("%s shadows the global --%s", key, g.Name)
			}
		})
	})
	for key := range allowed {
		if !slices.Contains(got, key) {
			t.Errorf("%s no longer shadows a global flag: remove it from the table", key)
		}
	}
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
		"refresh cluster rollback",
		"refresh cluster upgrade",
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

// TestCommandsWithoutOutputAreInTheContract compares the commands that have
// no -o with the list under "Commands that print no document" in
// CONTRACT.md. A command that loses or gains -o must change the contract.
func TestCommandsWithoutOutputAreInTheContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "CONTRACT.md"))
	if err != nil {
		t.Fatalf("reading the contract: %v", err)
	}
	const heading = "### Commands that print no document\n"
	_, section, ok := strings.Cut(string(data), heading)
	if !ok {
		t.Fatalf("CONTRACT.md has no %q section", strings.TrimSpace(heading))
	}
	section, _, _ = strings.Cut(section, "\n#")
	var want []string
	for line := range strings.SplitSeq(section, "\n") {
		names, _, ok := strings.Cut(strings.TrimPrefix(line, "- "), ":")
		if !strings.HasPrefix(line, "- ") || !ok {
			continue
		}
		for name := range strings.SplitSeq(names, ",") {
			want = append(want, strings.Trim(strings.TrimSpace(name), "`"))
		}
	}
	var got []string
	walk(newTree(), func(cmd *cobra.Command) {
		if cmd.HasSubCommands() || cmd.Hidden || cmd.Flags().Lookup("output") != nil {
			return
		}
		path := strings.TrimPrefix(cmd.CommandPath(), "refresh ")
		// The contract names the completion command once, not each shell.
		if strings.HasPrefix(path, "completion ") {
			path = "completion"
		}
		if !slices.Contains(got, path) {
			got = append(got, path)
		}
	})
	sort.Strings(want)
	sort.Strings(got)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("commands without -o (-contract +tree):\n%s", diff)
	}
}

// The aliases of the tree. The old CLI had ng, ctx, ls (of context list),
// rm, and delete; the rewrite keeps them and adds ls to every list. Its
// get, update-ami, and install-manpage are gone, as migrating.md says.
func TestAliases(t *testing.T) {
	want := map[string][]string{
		"refresh context":        {"ctx"},
		"refresh context list":   {"ls"},
		"refresh context remove": {"rm", "delete"},
		"refresh cluster list":   {"ls"},
		"refresh nodegroup":      {"ng"},
		"refresh nodegroup list": {"ls"},
		"refresh addon list":     {"ls"},
	}
	got := map[string][]string{}
	walk(newTree(), func(cmd *cobra.Command) {
		if len(cmd.Aliases) > 0 {
			got[cmd.CommandPath()] = cmd.Aliases
		}
	})
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("aliases (-want +got):\n%s", diff)
	}
}
