package root_test

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/cmd/root"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
)

// The completion of a shell never names a hidden command (#419). The
// scripts of cobra ask the binary at completion time, so the answer of
// that request is what a user sees: every command of the root that is
// not hidden, and no hidden one. The scripts name none either.
func TestCompletion_OmitsHiddenCommands(t *testing.T) {
	clearEnv(t)
	f, _, _, _ := cmdutiltest.NewFactory()
	var hidden, shown []string
	for _, c := range root.NewCmdRoot(f).Commands() {
		if c.Hidden {
			hidden = append(hidden, c.Name())
		} else if c.Name() != "help" && c.Name() != "completion" {
			shown = append(shown, c.Name())
		}
	}
	if len(hidden) == 0 || len(shown) == 0 {
		t.Fatalf("hidden commands %v, shown %v; the test needs one of each", hidden, shown)
	}
	names := func(out string) map[string]bool {
		got := map[string]bool{}
		for _, line := range strings.Split(out, "\n") {
			name, _, _ := strings.Cut(line, "\t")
			got[name] = true
		}
		return got
	}
	t.Run("the commands of the root", func(t *testing.T) {
		f, _, out, _ := cmdutiltest.NewFactory()
		if err := execute(f, "__complete", ""); err != nil {
			t.Fatal(err)
		}
		got := names(out.String())
		for _, name := range shown {
			if !got[name] {
				t.Errorf("the completion has no %q:\n%s", name, out)
			}
		}
		for _, name := range hidden {
			if got[name] {
				t.Errorf("the completion names the hidden command %q:\n%s", name, out)
			}
		}
	})
	t.Run("a prefix of a hidden command", func(t *testing.T) {
		for _, name := range hidden {
			f, _, out, _ := cmdutiltest.NewFactory()
			if err := execute(f, "__complete", name[:len(name)-1]); err != nil {
				t.Fatal(err)
			}
			if names(out.String())[name] {
				t.Errorf("the completion of %q names the hidden command %q:\n%s", name[:len(name)-1], name, out)
			}
		}
	})
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		t.Run(shell, func(t *testing.T) {
			f, _, out, _ := cmdutiltest.NewFactory()
			if err := execute(f, "completion", shell); err != nil {
				t.Fatal(err)
			}
			for _, name := range hidden {
				if strings.Contains(out.String(), name) {
					t.Errorf("the %s script names the hidden command %q", shell, name)
				}
			}
		})
	}
}
