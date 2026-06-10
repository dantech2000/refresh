package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"
)

func runCompletion(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	app := &cli.App{
		Name:                 "refresh",
		EnableBashCompletion: true,
		Commands:             []*cli.Command{CompletionCommand()},
	}
	app.Writer = &out
	err := app.Run(append([]string{"refresh", "completion"}, args...))
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

func TestCompletionUnknownShell(t *testing.T) {
	if _, err := runCompletion(t, "powershell"); err == nil {
		t.Fatal("expected error for unsupported shell")
	}
	if _, err := runCompletion(t); err == nil {
		t.Fatal("expected error when shell argument is missing")
	}
}
