package cmdutil

import (
	"bytes"
	"testing"

	"github.com/dantech2000/refresh/internal/view"
)

func TestColorDepth(t *testing.T) {
	tests := []struct {
		colorterm, term string
		want            view.ColorDepth
	}{
		{"truecolor", "xterm-256color", view.TrueColor},
		{"24bit", "", view.TrueColor},
		{"", "xterm-256color", view.Color256},
		{"", "xterm", view.Color256},
		{"", "dumb", view.NoColor},
		{"", "", view.NoColor},
	}
	for _, tt := range tests {
		if got := colorDepth(tt.colorterm, tt.term); got != tt.want {
			t.Errorf("colorDepth(%q, %q) = %d, want %d", tt.colorterm, tt.term, got, tt.want)
		}
	}
}

func TestUnicodeLocale(t *testing.T) {
	tests := []struct {
		vars []string
		want bool
	}{
		{[]string{"", "", "en_US.UTF-8"}, true},
		{[]string{"", "", "en_US.utf8"}, true},
		{[]string{"", "", "C"}, false},
		// The first variable that is set decides.
		{[]string{"C", "", "en_US.UTF-8"}, false},
		{[]string{"", "", ""}, true},
	}
	for _, tt := range tests {
		if got := unicodeLocale(tt.vars...); got != tt.want {
			t.Errorf("unicodeLocale(%q) = %v, want %v", tt.vars, got, tt.want)
		}
	}
}

// Each stream decides alone: a theme has color only when its stream is a
// terminal and color is on, and the size is known only for a terminal.
func TestIOStreams_ThemeAndSize(t *testing.T) {
	size := func() (int, int) { return 120, 40 }
	tests := []struct {
		name          string
		ios           IOStreams
		wantOut       view.ColorDepth
		wantErr       view.ColorDepth
		wantW, wantH  int
		wantNoUnicode bool
	}{
		{name: "both are terminals", ios: IOStreams{OutTTY: true, ErrTTY: true, ColorDepth: view.TrueColor, Unicode: true, TermSize: size}, wantOut: view.TrueColor, wantErr: view.TrueColor, wantW: 120, wantH: 40},
		{name: "stdout is a pipe", ios: IOStreams{ErrTTY: true, ColorDepth: view.Color256, Unicode: true, TermSize: size}, wantErr: view.Color256},
		{name: "stderr is a file", ios: IOStreams{OutTTY: true, ColorDepth: view.Color256, Unicode: true, TermSize: size}, wantOut: view.Color256, wantW: 120, wantH: 40},
		{name: "color is off", ios: IOStreams{OutTTY: true, ErrTTY: true, NoColor: true, ColorDepth: view.TrueColor, Unicode: true}},
		{name: "no Unicode", ios: IOStreams{OutTTY: true, ColorDepth: view.Color256}, wantOut: view.Color256, wantNoUnicode: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.ios.Out, tt.ios.ErrOut = &bytes.Buffer{}, &bytes.Buffer{}
			if got := tt.ios.Theme(); got.Depth != tt.wantOut || got.Unicode == tt.wantNoUnicode {
				t.Errorf("Theme() = %+v, want depth %d", got, tt.wantOut)
			}
			if got := tt.ios.ErrTheme(); got.Depth != tt.wantErr {
				t.Errorf("ErrTheme() = %+v, want depth %d", got, tt.wantErr)
			}
			if w, h := tt.ios.Size(); w != tt.wantW || h != tt.wantH {
				t.Errorf("Size() = %d, %d, want %d, %d", w, h, tt.wantW, tt.wantH)
			}
		})
	}
}
