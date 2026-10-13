package ui_test

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/cmd/root"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
)

// The command refuses bad input before it touches AWS or the terminal.
func TestUI_UsageErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		tty  bool
		want string
	}{
		{"no terminal", nil, false, "needs an interactive terminal"},
		{"all regions with a region", []string{"-A", "-r", "us-east-1"}, true, "cannot be used together"},
		{"short interval", []string{"--interval", "5s"}, true, "invalid --interval 5s: must be at least 10s"},
		{"negative wait", []string{"--wait-timeout", "-1s"}, true, "invalid --wait-timeout"},
		{"an argument", []string{"prod"}, true, "unknown command"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _, _, _ := cmdutiltest.NewFactory()
			f.IOStreams.InTTY, f.IOStreams.OutTTY = tc.tty, tc.tty
			cmd := root.NewCmdRoot(f)
			cmd.SetArgs(append([]string{"ui"}, tc.args...))
			err := cmd.ExecuteContext(t.Context())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

// The flags of the contract are registered, and the ones that do nothing
// yet say so.
func TestUI_FlagsSayWhatTheyDo(t *testing.T) {
	f, _, out, _ := cmdutiltest.NewFactory()
	cmd := root.NewCmdRoot(f)
	cmd.SetArgs([]string{"ui", "--help"})
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	help := out.String()
	for _, want := range []string{"-A, --all-regions", "--interval duration", "--allow-changes", "--wait-timeout duration", "no effect yet", "experimental"} {
		if !strings.Contains(help, want) {
			t.Errorf("help lacks %q:\n%s", want, help)
		}
	}
}
