package ui

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPromptReaderSequentialReadsShareBuffer(t *testing.T) {
	// One piped stream answering several prompts: every line must reach its
	// prompt (a per-prompt bufio.Reader would swallow the rest of the buffer).
	p := NewPromptReader(strings.NewReader("y\n  yes \r\nn\nlast"))
	for _, want := range []string{"y", "yes", "n", "last"} {
		got, err := p.ReadLine(t.Context())
		if err != nil || got != want {
			t.Fatalf("ReadLine() = %q, %v; want %q", got, err, want)
		}
	}
	if _, err := p.ReadLine(t.Context()); !errors.Is(err, io.EOF) {
		t.Fatalf("ReadLine() at end = %v, want io.EOF", err)
	}
}

func TestPromptReaderEOFWithoutNewline(t *testing.T) {
	p := NewPromptReader(strings.NewReader("prod"))
	got, err := p.ReadLine(t.Context())
	if err != nil || got != "prod" {
		t.Fatalf("ReadLine() = %q, %v; want %q, nil", got, err, "prod")
	}
}

func TestPromptReaderCancel(t *testing.T) {
	r, w := io.Pipe()
	t.Cleanup(func() { _ = w.Close() })
	p := NewPromptReader(r)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := p.ReadLine(ctx)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrPromptCancelled) {
			t.Fatalf("ReadLine() err = %v, want ErrPromptCancelled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ReadLine did not return after ctx was cancelled")
	}

	// The line the abandoned read eventually gets goes to the next caller.
	go func() { _, _ = w.Write([]byte("y\n")) }()
	got, err := p.ReadLine(t.Context())
	if err != nil || got != "y" {
		t.Fatalf("ReadLine() after cancel = %q, %v; want %q", got, err, "y")
	}
}

func TestPromptReaderAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	p := NewPromptReader(strings.NewReader("y\n"))
	if _, err := p.ReadLine(ctx); !errors.Is(err, ErrPromptCancelled) {
		t.Fatalf("ReadLine() err = %v, want ErrPromptCancelled", err)
	}
}

func TestConfirmSharedStdin(t *testing.T) {
	withStdin(t, "y\nn\nyes\n")
	for i, want := range []bool{true, false, true, false} {
		if got := Confirm(t.Context()); got != want {
			t.Fatalf("Confirm() #%d = %v, want %v", i+1, got, want)
		}
	}
}

func withStdin(t *testing.T, input string) {
	t.Helper()
	original := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = original })
	_, _ = w.WriteString(input)
	_ = w.Close()
}
