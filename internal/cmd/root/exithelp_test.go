package root_test

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/cobra"
)

// exitEntry matches the first line of the entry of one exit code.
var exitEntry = regexp.MustCompile(`^  (\d)  (.*)$`)

// exitHelp returns the entries of the "Exit codes:" paragraph of a long
// help text: the lines of each code, by code.
func exitHelp(t *testing.T, long string) map[string][]string {
	t.Helper()
	_, list, found := strings.Cut(long, "\nExit codes:\n")
	if !found {
		t.Fatalf("the help has no \"Exit codes:\" paragraph:\n%s", long)
	}
	out := map[string][]string{}
	code := ""
	for line := range strings.SplitSeq(list, "\n") {
		if m := exitEntry.FindStringSubmatch(line); m != nil {
			code = m[1]
			out[code] = []string{m[2]}
			continue
		}
		if code == "" || !strings.HasPrefix(line, "     ") {
			t.Fatalf("the line %q of the exit codes belongs to no code", line)
		}
		out[code] = append(out[code], strings.TrimSpace(line))
	}
	return out
}

// The five commands that change a cluster list their exit
// codes in their help, and say the same case in the same words. Each
// lists the codes that it can return: 2 is for a dry run alone, and a
// scale has no check after the change that could give 5.
func TestChangeCommandsAgreeOnExitCodes(t *testing.T) {
	codes := map[string][]string{
		"nodegroup update": {"0", "1", "2", "3", "4", "5"},
		"nodegroup scale":  {"0", "1", "2", "3", "4"},
		"addon update":     {"0", "1", "2", "3", "4", "5"},
		"cluster upgrade":  {"0", "1", "2", "3", "4", "5"},
		"cluster rollback": {"0", "1", "2", "3", "4", "5"},
	}
	// shared is the number of lines that every command has for a code.
	shared := map[string]int{"0": 1, "1": 1, "2": 1, "3": 3, "4": 2}
	tree := newTree()
	help := map[string]map[string][]string{}
	for path, want := range codes {
		cmd, _, err := tree.Find(strings.Fields(path))
		if err != nil {
			t.Fatal(err)
		}
		help[path] = exitHelp(t, cmd.Long)
		var got []string
		for _, code := range []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"} {
			if help[path][code] != nil {
				got = append(got, code)
			}
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("%s: the exit codes of the help (-want +got):\n%s", path, diff)
		}
		if two := strings.Join(help[path]["2"], " "); !strings.Contains(two, "--dry-run only") {
			t.Errorf("%s: exit code 2 is %q, want it for a dry run only", path, two)
		}
	}
	const reference = "nodegroup update"
	for path := range codes {
		for code, n := range shared {
			want, got := help[reference][code], help[path][code]
			if len(want) < n || len(got) < n {
				t.Errorf("%s: exit code %s has %d line(s), want at least the %d shared", path, code, len(got), n)
				continue
			}
			if diff := cmp.Diff(want[:n], got[:n]); diff != "" {
				t.Errorf("%s and %s say exit code %s in other words (-%s +%s):\n%s", reference, path, code, reference, path, diff)
			}
		}
	}
}

// Every command that runs something lists its exit codes in its help, in
// one form: an "Exit codes:" paragraph at the end, with the codes in
// rising order, and 0 and 1 among them. A group of commands only prints
// its help, and the commands that cobra adds have cobra's own help.
func TestEveryCommandListsItsExitCodes(t *testing.T) {
	walk(newTree(), func(cmd *cobra.Command) {
		if !cmd.Runnable() || cmd.HasSubCommands() || cmd.Hidden || fromCobra(cmd) {
			return
		}
		t.Run(cmd.CommandPath(), func(t *testing.T) {
			help := exitHelp(t, cmd.Long)
			for _, code := range []string{"0", "1"} {
				if help[code] == nil {
					t.Errorf("the help does not list exit code %s", code)
				}
			}
			var order []string
			_, list, _ := strings.Cut(cmd.Long, "\nExit codes:\n")
			for line := range strings.SplitSeq(list, "\n") {
				if m := exitEntry.FindStringSubmatch(line); m != nil {
					order = append(order, m[1])
				}
			}
			if !slices.IsSorted(order) {
				t.Errorf("the exit codes are in the order %v, want them rising", order)
			}
		})
	})
}
