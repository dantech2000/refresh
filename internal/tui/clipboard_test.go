package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/tui/state"
)

// Inside tmux (set-clipboard external) OSC 52 never arrived, yet the TUI said
// "copied". A system clipboard tool is used when there is one, and only that
// counts as a certain copy.
func TestCopyUsesTheSystemClipboardTool(t *testing.T) {
	out := filepath.Join(t.TempDir(), "clip")
	old := clipboardTools
	t.Cleanup(func() { clipboardTools = old })
	always := func() bool { return true }

	clipboardTools = []clipboardTool{{name: "false", ok: always}, {name: "sh", args: []string{"-c", "cat > " + out}, ok: always}}
	if !nativeCopy("refresh addon update --all") {
		t.Fatal("the working tool was not used")
	}
	if b, _ := os.ReadFile(out); string(b) != "refresh addon update --all" {
		t.Fatalf("copied %q", b)
	}
	clipboardTools = []clipboardTool{{name: "false", ok: always}, {name: "no-such-tool-here", ok: always}}
	if nativeCopy("x") {
		t.Fatal("a failed copy reported success")
	}

	m := Model{}
	next, _ := m.Update(copiedMsg{text: "cmd", native: true})
	if got := next.(Model); got.notice != "copied: cmd" || got.noticeLvl != state.LevelOK {
		t.Errorf("native notice = %q", got.notice)
	}
	next, _ = m.Update(copiedMsg{text: "cmd"})
	if got := next.(Model); !strings.Contains(got.notice, "may not allow it") || got.noticeLvl != state.LevelWarn {
		t.Errorf("OSC 52 notice = %q", got.notice)
	}
}
