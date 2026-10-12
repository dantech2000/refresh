package commands

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"
)

func runCompletion(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	app := &cli.Command{
		Name:                  "refresh",
		EnableShellCompletion: true,
		Commands: []*cli.Command{
			CompletionCommand(),
			{Name: "secret", Hidden: true, Usage: "internal", Flags: []cli.Flag{&cli.StringFlag{Name: "out"}}},
		},
	}
	app.Writer = &out
	err := app.Run(context.Background(), append([]string{"refresh", "completion"}, args...))
	return out.String(), err
}

func TestCompletionBash(t *testing.T) {
	out, err := runCompletion(t, "bash")
	if err != nil {
		t.Fatalf("completion bash: %v", err)
	}
	if !strings.Contains(out, "_refresh_bash_autocomplete") || !strings.Contains(out, "complete ") {
		t.Fatalf("bash completion script malformed: %q", out)
	}
}

func TestCompletionZsh(t *testing.T) {
	out, err := runCompletion(t, "zsh")
	if err != nil {
		t.Fatalf("completion zsh: %v", err)
	}
	if !strings.Contains(out, "#compdef refresh") || !strings.Contains(out, "compdef _refresh refresh") {
		t.Fatalf("zsh completion script malformed: %q", out)
	}
}

func TestCompletionFish(t *testing.T) {
	out, err := runCompletion(t, "fish")
	if err != nil {
		t.Fatalf("completion fish: %v", err)
	}
	if !strings.Contains(out, "complete") || !strings.Contains(out, "refresh") {
		t.Fatalf("fish completion script malformed: %q", out)
	}
}

// urfave's fish generator writes a hidden command's flags and lists it with
// the top-level commands; the script must not name it at all (#419).
func TestCompletionFishOmitsHiddenCommands(t *testing.T) {
	out, err := runCompletion(t, "fish")
	if err != nil {
		t.Fatalf("completion fish: %v", err)
	}
	if strings.Contains(out, "secret") {
		t.Errorf("fish completion names a hidden command:\n%s", out)
	}
	if !strings.Contains(out, "completion") {
		t.Errorf("fish completion lost the visible commands:\n%s", out)
	}
}

func TestCompletionUnknownShell(t *testing.T) {
	if _, err := runCompletion(t, "powershell"); err == nil {
		t.Fatal("expected error for unsupported shell")
	}
	if _, err := runCompletion(t); err == nil {
		t.Fatal("expected error when shell argument is missing")
	}
}
