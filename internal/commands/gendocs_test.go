package commands

import (
	"strings"
	"testing"

	"github.com/urfave/cli/v3"
)

func sampleTree() *cli.Command {
	return &cli.Command{
		Name:  "demo",
		Usage: "demo command",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "format",
				Aliases: []string{"o"},
				Usage:   "Output format (table|json)",
				Value:   "table",
				Sources: cli.EnvVars("DEMO_FORMAT"),
			},
			&cli.BoolFlag{Name: "verbose", Usage: "Chatty | output"}, // pipe must be escaped
			&cli.BoolFlag{Name: "secret", Hidden: true, Usage: "hidden"},
		},
		Commands: []*cli.Command{
			{
				Name:      "child",
				Aliases:   []string{"c"},
				Usage:     "a child command",
				ArgsUsage: "[thing]",
				Flags: []cli.Flag{
					&cli.IntFlag{Name: "count", Usage: "how many"},
				},
			},
			{Name: "help", Usage: "framework help"}, // must be excluded
		},
	}
}

func TestRenderCommandPage(t *testing.T) {
	out := renderCommandPage(sampleTree(), "refresh")

	wantContains := []string{
		"# refresh demo",
		generatedNote,
		"| Flag | Env | Default | Description |",
		"`--format, -o string`", // long form first, then alias, with type
		"`DEMO_FORMAT`",
		"`table`",           // visible default
		"Chatty \\| output", // pipe escaped in a cell
		"## Subcommands",
		"### refresh demo child",
		"**Aliases:** `c`",
		"[thing]", // ArgsUsage in the child synopsis
	}
	for _, w := range wantContains {
		if !strings.Contains(out, w) {
			t.Errorf("rendered page missing %q\n---\n%s", w, out)
		}
	}

	for _, notWant := range []string{
		"secret",                // hidden flag excluded
		"### refresh demo help", // framework help excluded
	} {
		if strings.Contains(out, notWant) {
			t.Errorf("rendered page should not contain %q", notWant)
		}
	}
}

func TestVisibleFiltersHelpAndHidden(t *testing.T) {
	root := sampleTree()
	subs := visibleSubcommands(root)
	if len(subs) != 1 || subs[0].Name != "child" {
		t.Fatalf("visibleSubcommands = %v, want only [child]", names(subs))
	}
	flags := visibleFlags(root.Flags)
	if len(flags) != 2 {
		t.Fatalf("visibleFlags returned %d, want 2 (secret hidden flag excluded)", len(flags))
	}
}

func names(cmds []*cli.Command) []string {
	out := make([]string, len(cmds))
	for i, c := range cmds {
		out[i] = c.Name
	}
	return out
}

func TestRenderIndexListsCommandsAndGlobalFlags(t *testing.T) {
	root := sampleTree()
	idx := renderIndex(root, visibleSubcommands(root))
	for _, w := range []string{
		"# Command reference",
		"[`refresh child`](child.md)",
		"## Global flags",
		"`--format, -o string`",
	} {
		if !strings.Contains(idx, w) {
			t.Errorf("index missing %q\n%s", w, idx)
		}
	}
}
