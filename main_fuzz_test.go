package main

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/flagcanon"
)

// fuzzLeafPaths lists every leaf command of the real tree ("cluster
// describe"), in walk order.
func fuzzLeafPaths() [][]string {
	var out [][]string
	var walk func(path []string, c *cli.Command)
	walk = func(path []string, c *cli.Command) {
		if len(c.Commands) == 0 && len(path) > 0 {
			out = append(out, path)
		}
		for _, sub := range c.Commands {
			walk(append(append([]string(nil), path...), sub.Name), sub)
		}
	}
	walk(nil, newApp())
	return out
}

// parseOnly runs argv (without the program name) through a fresh copy of
// the real command tree with every action and hook replaced, so nothing
// reaches AWS, the cluster API, or the terminal. It returns whether the
// leaf action ran, the parsed --no-color value, and the run error.
func parseOnly(argv []string) (ran, noColor bool, err error) {
	app := newApp()
	app.Writer, app.ErrWriter = io.Discard, io.Discard
	app.ExitErrHandler = func(context.Context, *cli.Command, error) {}
	_ = app.Walk(func(c *cli.Command) error {
		c.Before, c.After = nil, nil
		c.Action = func(_ context.Context, cmd *cli.Command) error {
			ran, noColor = true, cmd.Bool("no-color")
			return nil
		}
		return nil
	})
	err = app.Run(context.Background(), append([]string{"refresh"}, argv...))
	return ran, noColor, err
}

var removedErrRe = regexp.MustCompile(`^-(\S+) was removed from '([^']*)' in `)

// removedLetter returns the letter and command path a removed-shorthand
// error names, or ok=false for any other error.
func removedLetter(err error) (letter, path string, ok bool) {
	if err == nil {
		return "", "", false
	}
	m := removedErrRe.FindStringSubmatch(err.Error())
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// flagName is the name a flag token sets ("--skip=x" -> "skip"), or "" when
// tok is not a flag token. Like urfave/cli, it classifies the token after
// trimming spaces.
func flagName(tok string) string {
	tok = strings.TrimSpace(tok)
	if !strings.HasPrefix(tok, "-") || tok == "-" || tok == "--" {
		return ""
	}
	name, _, _ := strings.Cut(strings.TrimLeft(tok, "-"), "=")
	return name
}

// splitTokens turns the fuzz input into argv tokens: one per line.
func splitTokens(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// FuzzRemovedShorthand parses arbitrary argv against the real command tree
// and checks the removed-shorthand error (REF-164):
//
//   - parsing never panics;
//   - the error fires only for a letter the removed table lists for the
//     command being run, and it names that letter's replacement;
//   - a token before "--" actually sets that letter as a flag, so values of
//     other flags ("--cluster=-s") and positionals never trigger it;
//   - tokens after a "--" terminator never change it;
//   - "--cluster -<letter>" hands the letter to --cluster as a value.
//
// It also checks colorDisabled, main's pre-parse argv scan: whenever urfave
// parses --no-color as true, the scan saw it too, so help printed before
// Before runs is never colored against the user's wish.
func FuzzRemovedShorthand(f *testing.F) {
	f.Setenv("NO_COLOR", "")
	paths := fuzzLeafPaths()
	index := map[string]uint8{}
	for i, p := range paths {
		index[strings.Join(p, " ")] = uint8(i)
	}
	seeds := []struct {
		path string
		args string
	}{
		{"cluster describe", "prod\n-d"},
		{"cluster describe", "prod\n-s=1"},
		{"cluster describe", "prod\n--\n-a"},
		{"cluster describe", "--cluster=-s"},
		{"cluster describe", "--cluster\n-s"},
		{"cluster upgrade", "prod\n--to\n1.33\n-p\n5s"},
		{"cluster upgrade", "prod\n--to=-s"},
		{"nodegroup update", "prod\n-f"},
		{"nodegroup update", "prod\n---s"},
		{"nodegroup update", "prod\n-sd"},
		{"addon update", "prod\n--all\n-p"},
		{"addon update-all", "prod\n--skip\n-p"},
		{"context add", "prod\n-c\nx\n-p\nops"},
		{"cluster list", "-s\n-p"},
		{"status", "--no-color\n-A"},
		{"status", "--no-color=false"},
		{"nodegroup scale", "-n\nx\n--no-color=t\n--op-timeout\n9m\n--wait-timeout\n1m"},
	}
	for _, s := range seeds {
		i, ok := index[s.path]
		if !ok {
			f.Fatalf("seed names unknown command %q", s.path)
		}
		f.Add(i, uint8(0), s.args)
	}
	removed := flagcanon.Removed()

	f.Fuzz(func(t *testing.T, pathIdx, split uint8, raw string) {
		path := paths[int(pathIdx)%len(paths)]
		pathStr := strings.Join(path, " ")
		tokens := splitTokens(raw)
		argv := append(append([]string(nil), path...), tokens...)

		ran, noColor, err := parseOnly(argv)
		if ran && noColor && !colorDisabled(append([]string{"refresh"}, argv...)) {
			t.Fatalf("urfave parsed --no-color=true from %q but colorDisabled missed it", argv)
		}

		checkRemoved := func(argv, tokens []string, err error) {
			t.Helper()
			letter, p, ok := removedLetter(err)
			if !ok {
				return
			}
			hint, listed := removed[p][letter]
			if p != pathStr || !listed {
				t.Fatalf("argv %q: removed-shorthand error for -%s on %q, but %q lists %v", argv, letter, p, pathStr, removed[pathStr])
			}
			if want := fmt.Sprintf("-%s was removed from '%s' in %s; %s", letter, p, flagcanon.CanonVersion, hint); err.Error() != want {
				t.Fatalf("argv %q: error %q, want %q", argv, err, want)
			}
			for _, tok := range tokens {
				if strings.TrimSpace(tok) == "--" {
					break
				}
				if flagName(tok) == letter {
					return
				}
			}
			t.Fatalf("argv %q: removed-shorthand error for -%s, but no token before \"--\" sets it", argv, letter)
		}
		checkRemoved(argv, tokens, err)

		// Tokens after "--" are positionals. Skip a split right after a flag
		// token without "=", which may take "--" itself as its value.
		k := int(split) % (len(tokens) + 1)
		if k == 0 || flagName(tokens[k-1]) == "" || strings.Contains(tokens[k-1], "=") {
			head := append(append([]string(nil), tokens[:k]...), "--")
			withTail := append(append([]string(nil), head...), tokens[k:]...)
			_, _, errHead := parseOnly(append(append([]string(nil), path...), head...))
			_, _, errTail := parseOnly(append(append([]string(nil), path...), withTail...))
			l1, _, ok1 := removedLetter(errHead)
			l2, _, ok2 := removedLetter(errTail)
			if ok1 != ok2 || l1 != l2 {
				t.Fatalf("path %q: tokens after \"--\" changed the removed-shorthand error: %v vs %v (head %q, tail %q)", pathStr, errHead, errTail, head, tokens[k:])
			}
		}

		// A removed letter placed as the value of --cluster is a value.
		for letter := range removed[pathStr] {
			valArgv := append(append([]string(nil), path...), "--cluster", "-"+letter)
			if _, _, err := parseOnly(valArgv); err != nil {
				if l, _, ok := removedLetter(err); ok {
					t.Fatalf("argv %q: -%s as the value of --cluster fired the removed-shorthand error for -%s", valArgv, letter, l)
				}
			}
		}
	})
}

// fuzzRegion turns fuzz input into one region token value: no commas (the
// slice flag splits on them), no newlines, and never a leading "-".
func fuzzRegion(s string) string {
	s = strings.NewReplacer(",", "", "\n", "").Replace(s)
	if strings.HasPrefix(s, "-") {
		s = "x" + s
	}
	return s
}

// regionsVia runs argv through the real tree and returns what
// runner.Regions resolves on the leaf command.
func regionsVia(t *testing.T, path, argv []string, allRegions bool) []string {
	t.Helper()
	app := newApp()
	app.Writer, app.ErrWriter = io.Discard, io.Discard
	app.ExitErrHandler = func(context.Context, *cli.Command, error) {}
	app.Before = nil
	cmd := app
	for _, name := range path {
		cmd = cmd.Command(name)
	}
	var got []string
	ran := false
	cmd.Action = func(_ context.Context, c *cli.Command) error {
		got, ran = runner.Regions(c, allRegions), true
		return nil
	}
	if err := app.Run(context.Background(), append([]string{"refresh"}, argv...)); err != nil || !ran {
		t.Fatalf("run %q: ran=%v err=%v", argv, ran, err)
	}
	return got
}

// FuzzRegions checks runner.Regions on the real multi-region commands:
//
//   - explicit local -r/--region values win, trimmed, with blanks dropped;
//   - without them, a sweep (all-regions) returns nil and otherwise the
//     global --region placed before the subcommand is the scan list;
//   - without local values, the global --region before the subcommand and
//     --region after it resolve the same;
//   - the result is nil or holds only non-empty, trimmed names.
func FuzzRegions(f *testing.F) {
	f.Setenv("REFRESH_EKS_REGIONS", "")
	f.Add(false, "eu-west-1", "", false)
	f.Add(true, "eu-west-1", "us-east-1\nus-west-2", false)
	f.Add(false, "", "us-east-1\n \nus-east-1", true)
	f.Add(true, " cn-north-1 ", "", true)
	f.Add(false, "-r", " ", false)
	f.Add(false, "us-east-1", "", true)
	f.Fuzz(func(t *testing.T, clusterList bool, global, locals string, allRegions bool) {
		path := []string{"status"}
		if clusterList {
			path = []string{"cluster", "list"}
		}
		global = fuzzRegion(global)
		var localVals, argvLocal []string
		for _, l := range splitTokens(locals) {
			l = fuzzRegion(l)
			argvLocal = append(argvLocal, "--region="+l)
			if v := strings.TrimSpace(l); v != "" {
				localVals = append(localVals, v)
			}
		}
		if len(argvLocal) > 8 {
			t.Skip("keep argv short")
		}

		argv := append([]string{"--region=" + global}, path...)
		argv = append(argv, argvLocal...)
		got := regionsVia(t, path, argv, allRegions)
		for _, r := range got {
			if r == "" || r != strings.TrimSpace(r) {
				t.Fatalf("argv %q: Regions = %q holds a blank or untrimmed name", argv, got)
			}
		}
		if got != nil && len(got) == 0 {
			t.Fatalf("argv %q: Regions = empty non-nil slice, want nil", argv)
		}

		var want []string
		switch {
		case len(localVals) > 0:
			want = localVals
		case allRegions:
			want = nil
		case strings.TrimSpace(global) != "":
			want = []string{strings.TrimSpace(global)}
		}
		if !slices.Equal(got, want) {
			t.Fatalf("argv %q (all-regions=%v): Regions = %q, want %q", argv, allRegions, got, want)
		}

		if len(localVals) == 0 && !allRegions {
			after := append(append([]string(nil), path...), "--region="+global)
			if got2 := regionsVia(t, path, after, false); !slices.Equal(got2, got) {
				t.Fatalf("global --region before the subcommand gives %q, after it %q", got, got2)
			}
		}
	})
}
