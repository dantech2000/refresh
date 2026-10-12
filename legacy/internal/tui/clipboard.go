package tui

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// copiedMsg reports how a copy went: native is true when a system clipboard
// tool took the text. OSC 52 is sent either way, but a terminal can drop it
// silently (tmux without set-clipboard on, screen, some SSH setups), so only
// a native copy is reported as certain.
type copiedMsg struct {
	text   string
	native bool
}

// clipboardTool is a command that reads the text to copy on stdin.
type clipboardTool struct {
	name string
	args []string
	ok   func() bool // whether it applies here
}

// clipboardTools are tried in order. Tests replace them.
var clipboardTools = []clipboardTool{
	{name: "pbcopy", ok: func() bool { return runtime.GOOS == "darwin" }},
	{name: "wl-copy", ok: func() bool { return os.Getenv("WAYLAND_DISPLAY") != "" }},
	{name: "xclip", args: []string{"-selection", "clipboard"}, ok: func() bool { return os.Getenv("DISPLAY") != "" }},
	{name: "xsel", args: []string{"--clipboard", "--input"}, ok: func() bool { return os.Getenv("DISPLAY") != "" }},
}

// copyText copies text through OSC 52 and, when one is available, a system
// clipboard tool, then reports the result as a copiedMsg.
func copyText(text string) tea.Cmd {
	return tea.Batch(tea.SetClipboard(text), func() tea.Msg {
		return copiedMsg{text: text, native: nativeCopy(text)}
	})
}

func nativeCopy(text string) bool {
	for _, t := range clipboardTools {
		if !t.ok() {
			continue
		}
		path, err := exec.LookPath(t.name)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		cmd := exec.CommandContext(ctx, path, t.args...) //nolint:gosec // path is LookPath of a fixed tool name from clipboardTools
		cmd.Stdin = strings.NewReader(text)
		err = cmd.Run()
		cancel()
		if err == nil {
			return true
		}
	}
	return false
}
