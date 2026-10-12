package cmdutil_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"go.uber.org/goleak"

	"github.com/dantech2000/refresh/internal/cmdutil"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestReadLine(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		want     string
		wantRest string // what a later read still finds
	}{
		{name: "line", input: "prod\n", want: "prod"},
		{name: "spaces around the line", input: "  prod \n", want: "prod"},
		{name: "carriage return", input: "prod\r\n", want: "prod"},
		{name: "empty line", input: "\n", want: ""},
		{name: "end of input without a line end", input: "prod", want: "prod"},
		{name: "empty input", input: "", want: ""},
		{name: "second line stays in the input", input: "one\ntwo\n", want: "one", wantRest: "two\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := strings.NewReader(tt.input)
			got, err := cmdutil.ReadLine(context.Background(), &cmdutil.IOStreams{In: in})
			if err != nil {
				t.Fatalf("ReadLine() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("ReadLine() = %q, want %q", got, tt.want)
			}
			rest, err := io.ReadAll(in)
			if err != nil {
				t.Fatal(err)
			}
			if string(rest) != tt.wantRest {
				t.Errorf("input left = %q, want %q", rest, tt.wantRest)
			}
		})
	}
}

func TestReadLineReadError(t *testing.T) {
	want := errors.New("terminal went away")
	r, w := io.Pipe()
	_ = w.CloseWithError(want)
	if _, err := cmdutil.ReadLine(context.Background(), &cmdutil.IOStreams{In: r}); !errors.Is(err, want) {
		t.Errorf("ReadLine() error = %v, want %v", err, want)
	}
}

// A context that is cancelled before the call wins over input that is
// ready, and the input is not read.
func TestReadLineCancelledFirst(t *testing.T) {
	in := strings.NewReader("prod\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := cmdutil.ReadLine(ctx, &cmdutil.IOStreams{In: in})

	if !errors.Is(err, context.Canceled) || got != "" {
		t.Errorf("ReadLine() = %q, %v; want no line and context.Canceled", got, err)
	}
	if in.Len() != len("prod\n") {
		t.Errorf("ReadLine read %d bytes after the cancellation, want 0", len("prod\n")-in.Len())
	}
}

// cancelOnRead cancels the context inside the read that returns the line,
// so the line and the cancellation are ready together.
type cancelOnRead struct {
	cancel context.CancelFunc
	r      io.Reader
}

func (c *cancelOnRead) Read(p []byte) (int, error) {
	c.cancel()
	return c.r.Read(p)
}

func TestReadLineCancelledWithInputReady(t *testing.T) {
	// Each run has the same two ready cases; many runs make a wrong pick
	// by select show up.
	for range 200 {
		ctx, cancel := context.WithCancel(context.Background())
		in := &cancelOnRead{cancel: cancel, r: strings.NewReader("prod\n")}

		got, err := cmdutil.ReadLine(ctx, &cmdutil.IOStreams{In: in})

		if !errors.Is(err, context.Canceled) || got != "" {
			t.Fatalf("ReadLine() = %q, %v; want no line and context.Canceled", got, err)
		}
	}
}

// A cancellation ends the wait for input that never comes.
func TestReadLineCancelledWhileWaiting(t *testing.T) {
	r, w := io.Pipe()
	// Closing the input ends the background read, so no goroutine is left.
	defer func() { _ = w.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	reading := make(chan struct{})
	in := &signalOnRead{reading: reading, r: r}
	go func() {
		<-reading // ReadLine is inside the read
		cancel()
	}()

	_, err := cmdutil.ReadLine(ctx, &cmdutil.IOStreams{In: in})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("ReadLine() error = %v, want context.Canceled", err)
	}
}

// signalOnRead closes reading when the first read starts.
type signalOnRead struct {
	reading chan struct{}
	once    sync.Once
	r       io.Reader
}

func (s *signalOnRead) Read(p []byte) (int, error) {
	s.once.Do(func() { close(s.reading) })
	return s.r.Read(p)
}
