package cmdutil_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/cmdutil"
)

func TestValidateWatch(t *testing.T) {
	tty := &cmdutil.IOStreams{OutTTY: true}
	pipe := &cmdutil.IOStreams{}
	tests := []struct {
		name     string
		ios      *cmdutil.IOStreams
		format   cmdutil.Format
		watch    bool
		interval time.Duration
		wantErr  string
	}{
		{name: "no watch", ios: pipe, format: cmdutil.FormatJSON},
		{name: "a table on a terminal", ios: tty, format: cmdutil.FormatTable, watch: true, interval: time.Second},
		{name: "json", ios: tty, format: cmdutil.FormatJSON, watch: true, interval: time.Second, wantErr: "--watch cannot be used with -o json"},
		{name: "plain", ios: tty, format: cmdutil.FormatPlain, watch: true, interval: time.Second, wantErr: "--watch cannot be used with -o plain"},
		{name: "not a terminal", ios: pipe, format: cmdutil.FormatTable, watch: true, interval: time.Second, wantErr: "--watch needs a terminal on stdout"},
		{name: "an interval that is too short", ios: tty, format: cmdutil.FormatTable, watch: true, interval: 100 * time.Millisecond, wantErr: "invalid --watch-interval 100ms: must be at least 1s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := cmdutil.ValidateWatch(tt.ios, tt.format, tt.watch, tt.interval)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("error = %v, want none", err)
				}
				return
			}
			if err == nil || !strings.HasPrefix(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want one that starts with %q", err, tt.wantErr)
			}
		})
	}
}

// A watch reads again after each interval, goes on after a partial read,
// and ends with ErrStopped when the user stops it.
func TestWatch(t *testing.T) {
	var out bytes.Buffer
	ios := &cmdutil.IOStreams{Out: &out, OutTTY: true}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	tick := make(chan time.Time)
	var intervals []time.Duration
	after := func(d time.Duration) <-chan time.Time {
		intervals = append(intervals, d)
		return tick
	}
	reads := 0
	draw := func(context.Context) error {
		reads++
		out.WriteString("read\n")
		switch reads {
		case 2:
			// A partial read: the watch goes on.
			return cmdutil.IncompleteError([]apidoc.Failure{{Kind: apidoc.FailureRegion}})
		case 3:
			cancel()
		}
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- cmdutil.Watch(ctx, ios, 10*time.Second, after, draw) }()
	tick <- time.Time{}
	tick <- time.Time{}
	if err := <-done; !errors.Is(err, cmdutil.ErrStopped) {
		t.Fatalf("error = %v, want ErrStopped", err)
	}
	if reads != 3 {
		t.Errorf("reads = %d, want 3", reads)
	}
	const clearScreen = "\x1b[H\x1b[2J"
	if want := strings.Repeat(clearScreen+"read\n", 3); out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
	if len(intervals) != 2 || intervals[0] != 10*time.Second {
		t.Errorf("pauses = %v, want two of 10s", intervals)
	}
}

// An error that is not a partial result ends the watch.
func TestWatch_ErrorEndsIt(t *testing.T) {
	ios := &cmdutil.IOStreams{Out: &bytes.Buffer{}, OutTTY: true}
	boom := errors.New("credentials expired")
	err := cmdutil.Watch(t.Context(), ios, time.Second, nil, func(context.Context) error { return boom })
	if !errors.Is(err, boom) {
		t.Errorf("error = %v, want the error of the read", err)
	}
}

// Ctrl+C during the pause stops the watch too.
func TestWatch_StoppedDuringThePause(t *testing.T) {
	ios := &cmdutil.IOStreams{Out: &bytes.Buffer{}, OutTTY: true}
	ctx, cancel := context.WithCancel(t.Context())
	after := func(time.Duration) <-chan time.Time {
		cancel()
		return make(chan time.Time)
	}
	err := cmdutil.Watch(ctx, ios, time.Second, after, func(context.Context) error { return nil })
	if !errors.Is(err, cmdutil.ErrStopped) {
		t.Errorf("error = %v, want ErrStopped", err)
	}
}
