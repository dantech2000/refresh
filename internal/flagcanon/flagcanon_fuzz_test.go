package flagcanon

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/ui"
)

// fuzzTokens is the argv vocabulary FuzzDeprecatedAliases draws from.
var fuzzTokens = []string{
	"--wait-timeout=1m", // the replacement, on the leaf
	"--timeout=5m",      // deprecated alias, long form
	"-t=5m",             // deprecated alias, short form
	"--legacy",          // deprecated switch, true
	"--legacy=false",    // deprecated switch, false
	"pos",               // a positional
	"--",                // terminator: the rest is positional
	"-t 2m",             // alias and its value as two tokens
	"2m",                // a stray positional
}

// aliasArgs is what argv sets on the leaf, as modelled by modelAliasArgs.
type aliasArgs struct {
	waitSet, aliasSet, legacySet, legacyVal bool
}

// modelAliasArgs models urfave/cli's parse of leaf: flags up to "--", and
// "-t" takes the next token as its value.
func modelAliasArgs(leaf []string) aliasArgs {
	var a aliasArgs
	for i := 0; i < len(leaf); i++ {
		switch leaf[i] {
		case "--":
			return a
		case "--wait-timeout=1m":
			a.waitSet = true
		case "--timeout=5m", "-t=5m":
			a.aliasSet = true
		case "-t":
			a.aliasSet = true
			i++
		case "--legacy":
			a.legacySet, a.legacyVal = true, true
		case "--legacy=false":
			a.legacySet, a.legacyVal = true, false
		}
	}
	return a
}

// localSeen is what the leaf action read through LocalIsSet.
type localSeen struct {
	ran, wait, alias, bogus, short bool
}

// runAliasArgs runs leaf through a root with its own --wait-timeout and a
// leaf "update" with a DeprecatedDuration and a DeprecatedSwitch. It returns
// what the action saw, the warnings written to stderr, and the run error.
func runAliasArgs(rootWait bool, leaf []string) (argv []string, seen localSeen, stderr string, err error) {
	var errOut bytes.Buffer
	prev := ui.Stderr
	ui.Stderr = &errOut
	defer func() { ui.Stderr = prev }()

	root := &cli.Command{
		Name:           "refresh",
		Writer:         io.Discard,
		ErrWriter:      io.Discard,
		ExitErrHandler: func(context.Context, *cli.Command, error) {},
		Flags:          []cli.Flag{&cli.DurationFlag{Name: "wait-timeout"}},
		Commands: []*cli.Command{{
			Name: "update",
			Flags: []cli.Flag{
				&cli.DurationFlag{Name: "wait-timeout", Value: time.Minute},
				DeprecatedDuration("timeout", "wait-timeout", "t"),
				DeprecatedSwitch("legacy", "no-legacy"),
			},
			Action: func(_ context.Context, c *cli.Command) error {
				seen = localSeen{
					ran:   true,
					wait:  LocalIsSet(c, "wait-timeout"),
					alias: LocalIsSet(c, "timeout"),
					bogus: LocalIsSet(c, "bogus"),
					short: LocalIsSet(c, "t"),
				}
				return nil
			},
		}},
	}
	argv = []string{"refresh"}
	if rootWait {
		argv = append(argv, "--wait-timeout=2m")
	}
	argv = append(argv, "update")
	argv = append(argv, leaf...)
	err = root.Run(context.Background(), argv)
	return argv, seen, errOut.String(), err
}

// FuzzDeprecatedAliases runs argv built from fuzzTokens through a command
// with a DeprecatedDuration and a DeprecatedSwitch, and checks:
//
//   - the alias and its replacement together fail, in either order, and
//     either flag alone succeeds;
//   - LocalIsSet sees only the leaf's own flags: an ancestor flag of the
//     same name never counts, and an unknown name is never set;
//   - the deprecation warning appears exactly when the alias was set, and
//     the switch's warning follows its last value.
func FuzzDeprecatedAliases(f *testing.F) {
	f.Add(false, []byte{1})
	f.Add(false, []byte{0, 1})
	f.Add(false, []byte{2, 5, 0})
	f.Add(true, []byte{1})
	f.Add(true, []byte{3, 4})
	f.Add(false, []byte{4, 3, 6, 1, 0})
	f.Add(false, []byte{7, 8, 5})
	f.Add(false, []byte{0, 6, 2})
	f.Fuzz(func(t *testing.T, rootWait bool, picks []byte) {
		if len(picks) > 12 {
			picks = picks[:12]
		}
		leaf := make([]string, 0, 2*len(picks))
		for _, p := range picks {
			leaf = append(leaf, strings.Fields(fuzzTokens[int(p)%len(fuzzTokens)])...)
		}
		want := modelAliasArgs(leaf)
		argv, seen, stderr, err := runAliasArgs(rootWait, leaf)

		if want.waitSet && want.aliasSet {
			if err == nil || !strings.Contains(err.Error(), "pass only --wait-timeout") {
				t.Fatalf("argv %q: err = %v, want the both-flags error", argv, err)
			}
			if seen.ran {
				t.Fatalf("argv %q: the action ran despite conflicting flags", argv)
			}
			return
		}
		if err != nil || !seen.ran {
			t.Fatalf("argv %q: err = %v, ran = %v; want success", argv, err, seen.ran)
		}
		if seen.wait != want.waitSet || seen.alias != want.aliasSet {
			t.Fatalf("argv %q: LocalIsSet wait-timeout=%v timeout=%v, want %v %v", argv, seen.wait, seen.alias, want.waitSet, want.aliasSet)
		}
		if seen.bogus || seen.short {
			t.Fatalf("argv %q: LocalIsSet reported an unknown name or an alias as set", argv)
		}
		if got := strings.Contains(stderr, "--timeout on 'update' is deprecated"); got != want.aliasSet {
			t.Fatalf("argv %q: alias warning shown = %v, want %v; stderr %q", argv, got, want.aliasSet, stderr)
		}
		onByDefault := strings.Contains(stderr, "--legacy on 'update' is deprecated")
		falseForm := strings.Contains(stderr, "--legacy=false on 'update' is deprecated")
		switch {
		case !want.legacySet && (onByDefault || falseForm):
			t.Fatalf("argv %q: switch warning without the switch; stderr %q", argv, stderr)
		case want.legacySet && want.legacyVal && !onByDefault:
			t.Fatalf("argv %q: want the on-by-default warning; stderr %q", argv, stderr)
		case want.legacySet && !want.legacyVal && !falseForm:
			t.Fatalf("argv %q: want the --legacy=false warning; stderr %q", argv, stderr)
		}
	})
}
