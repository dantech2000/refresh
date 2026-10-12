package cmdutil_test

import (
	"context"
	"errors"
	"io"
	"strings"
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

func TestReadLineCancelled(t *testing.T) {
	r, w := io.Pipe()
	// Closing the input ends the background read, so no goroutine is left.
	defer func() { _ = w.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := cmdutil.ReadLine(ctx, &cmdutil.IOStreams{In: r})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("ReadLine() error = %v, want context.Canceled", err)
	}
}
