package ctxcmd

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/cliconfig"
)

// The confirmation lines are status tokens: with a non-UTF-8 locale they fall
// back to ASCII glyphs, and a pipe gets no escape codes.
func TestUseLineIsAStatusToken(t *testing.T) {
	t.Setenv("REFRESH_CONFIG_HOME", t.TempDir())
	t.Setenv("REFRESH_CONTEXT", "")
	t.Setenv("LC_ALL", "C")
	f := &cliconfig.File{Contexts: map[string]cliconfig.Context{"prod": {Cluster: "prod-eks"}}}
	if err := cliconfig.Save(f); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error { return runAction(runUse, "prod") })
	if err != nil {
		t.Fatalf("use prod: %v", err)
	}
	if want := `[OK] Switched to context "prod" (cluster=prod-eks region=- profile=-)`; !strings.Contains(out, want) {
		t.Errorf("stdout = %q, want %q", out, want)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("piped stdout has ANSI escapes: %q", out)
	}
}
