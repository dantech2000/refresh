package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/commands"
	"github.com/dantech2000/refresh/internal/commands/runner"
)

// Every command's --help documents its exit codes, or at least links to the
// exit-code contract (REF-165). A new command without an entry in
// commands.exitCodeHelp fails here.
func TestEveryCommandDocumentsExitCodes(t *testing.T) {
	if missing := commands.DocumentExitCodes(newApp()); len(missing) > 0 {
		t.Fatalf("commands without exit-code help (add them to exitCodeHelp in internal/commands/exitcodes.go): %v", missing)
	}

	var paths [][]string
	var walk func(c *cli.Command, path []string)
	walk = func(c *cli.Command, path []string) {
		for _, sc := range c.Commands {
			if sc.Name == "help" {
				continue
			}
			p := append(append([]string(nil), path...), sc.Name)
			paths = append(paths, p)
			walk(sc, p)
		}
	}
	walk(newApp(), nil)

	for _, p := range paths {
		t.Run(strings.Join(p, "_"), func(t *testing.T) {
			var out, errOut bytes.Buffer
			args := append(append([]string{"refresh"}, p...), "--help")
			if err := run(context.Background(), args, &out, &errOut); err != nil {
				t.Fatalf("%v: %v", args, err)
			}
			if !strings.Contains(out.String(), runner.ExitCodesURL) {
				t.Errorf("%v help does not link the exit-code contract:\n%s", args, out.String())
			}
		})
	}
}
