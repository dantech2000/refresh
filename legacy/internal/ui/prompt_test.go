package ui

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"
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
	defer goleak.VerifyNone(t)
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

// A prompt under WithPromptScope waits on the scope's context, so an API
// deadline that has already passed does not cancel it.
func TestPromptReaderScopeIgnoresAPIDeadline(t *testing.T) {
	r, w := io.Pipe()
	t.Cleanup(func() { _ = w.Close() })
	p := NewPromptReader(r)

	apiCtx, cancel := context.WithTimeout(t.Context(), time.Nanosecond)
	defer cancel()
	<-apiCtx.Done()
	paused, resumed := 0, 0
	started := make(chan struct{})
	ctx := WithPromptScope(apiCtx, t.Context(), func() func() {
		paused++
		close(started)
		return func() { resumed++ }
	})

	// Answer only once the prompt is waiting, after the deadline passed.
	go func() {
		<-started
		_, _ = w.Write([]byte("y\n"))
	}()
	got, err := p.ReadLine(ctx)
	if err != nil || got != "y" {
		t.Fatalf("ReadLine() = %q, %v; want %q, nil", got, err, "y")
	}
	if paused != 1 || resumed != 1 {
		t.Errorf("pause hook: paused %d, resumed %d; want 1, 1", paused, resumed)
	}
}

// Cancelling the scope's (signal) context still cancels the prompt.
func TestPromptReaderScopeSignalCancels(t *testing.T) {
	r, w := io.Pipe()
	t.Cleanup(func() { _ = w.Close() })
	p := NewPromptReader(r)

	signalCtx, stop := context.WithCancel(t.Context())
	ctx := WithPromptScope(context.WithoutCancel(signalCtx), signalCtx, nil)
	done := make(chan error, 1)
	go func() {
		_, err := p.ReadLine(ctx)
		done <- err
	}()
	stop()
	select {
	case err := <-done:
		if !errors.Is(err, ErrPromptCancelled) {
			t.Fatalf("ReadLine() err = %v, want ErrPromptCancelled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ReadLine did not return after the signal context was cancelled")
	}
}

// Nested scopes keep the outermost wait context and run every pause hook.
func TestWithPromptScopeNested(t *testing.T) {
	outerWait := t.Context()
	var calls []string
	hook := func(name string) func() func() {
		return func() func() {
			calls = append(calls, "pause "+name)
			return func() { calls = append(calls, "resume "+name) }
		}
	}
	ctx := WithPromptScope(t.Context(), outerWait, hook("outer"))
	innerWait, cancel := context.WithCancel(t.Context())
	cancel()
	ctx = WithPromptScope(ctx, innerWait, hook("inner"))

	wait, resume := promptContext(ctx)
	if wait != outerWait {
		t.Error("nested scope must keep the outer wait context")
	}
	resume()
	want := "pause outer,pause inner,resume inner,resume outer"
	if got := strings.Join(calls, ","); got != want {
		t.Errorf("hooks = %s, want %s", got, want)
	}
}

// Without a scope, a deadline is reported as a timeout, not as a user cancel.
func TestPromptReaderDeadlineIsTimeout(t *testing.T) {
	r, w := io.Pipe()
	t.Cleanup(func() { _ = w.Close() })
	p := NewPromptReader(r)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	_, err := p.ReadLine(ctx)
	if !errors.Is(err, ErrPromptTimeout) {
		t.Fatalf("ReadLine() err = %v, want ErrPromptTimeout", err)
	}
	if a, b := PromptError(err).Error(), PromptError(ErrPromptCancelled).Error(); a == b {
		t.Errorf("timeout and cancel map to the same error text %q", a)
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
