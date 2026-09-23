package ui

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
)

// ErrPromptCancelled is returned by ReadLine when ctx is cancelled (Ctrl+C,
// SIGTERM) while waiting for input.
var ErrPromptCancelled = errors.New("cancelled")

// ErrPromptTimeout is returned by ReadLine when the context it waits on hits
// its deadline before an answer arrives. A prompt under WithPromptScope never
// returns it: it waits on the scope's signal-only context.
var ErrPromptTimeout = errors.New("timed out waiting for an answer")

// PromptError maps a non-nil ReadLine error to the error a prompt returns.
func PromptError(err error) error {
	switch {
	case errors.Is(err, ErrPromptCancelled):
		return errors.New("operation cancelled")
	case errors.Is(err, ErrPromptTimeout):
		return errors.New("operation cancelled: timed out waiting for an answer")
	default:
		return errors.New("operation cancelled: failed to read input")
	}
}

type promptScopeKey struct{}

// promptScope tells ReadLine which context to wait on and how to stop the
// caller's API deadline while the user answers.
type promptScope struct {
	wait  context.Context
	pause func() (resume func())
}

// WithPromptScope returns ctx marked so that a prompt read under ctx (or
// under a context derived from it) waits on wait instead of ctx. Only Ctrl+C
// or SIGTERM should cancel wait, so an API deadline on ctx cannot cut a prompt
// short. If pause is non-nil, ReadLine calls it when a prompt starts and calls
// the func it returns when the prompt ends; use it to stop an API deadline
// while the user answers. If ctx already has a scope, the outer wait context
// is kept and both pause hooks run.
func WithPromptScope(ctx, wait context.Context, pause func() (resume func())) context.Context {
	if outer, ok := ctx.Value(promptScopeKey{}).(promptScope); ok {
		wait = outer.wait
		pause = chainPause(outer.pause, pause)
	}
	return context.WithValue(ctx, promptScopeKey{}, promptScope{wait: wait, pause: pause})
}

// chainPause returns a pause hook that runs a and b. Either may be nil.
func chainPause(a, b func() func()) func() func() {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	return func() func() {
		ra, rb := a(), b()
		return func() { rb(); ra() }
	}
}

// promptContext returns the context a prompt under ctx waits on and the func
// to call when the prompt ends.
func promptContext(ctx context.Context) (context.Context, func()) {
	s, ok := ctx.Value(promptScopeKey{}).(promptScope)
	if !ok {
		return ctx, func() {}
	}
	resume := func() {}
	if s.pause != nil {
		resume = s.pause()
	}
	return s.wait, resume
}

// promptCtxError maps the error of the context a prompt waited on.
func promptCtxError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrPromptTimeout
	}
	return ErrPromptCancelled
}

type lineResult struct {
	line string
	err  error
}

// PromptReader reads answers to interactive prompts from one shared
// bufio.Reader, so buffered input piped for several prompts
// (`printf 'y\ny\n' | refresh …`) is not lost between prompts. Each read runs
// in a goroutine so the caller can stop waiting when ctx is cancelled.
type PromptReader struct {
	mu      sync.Mutex
	br      *bufio.Reader
	pending chan lineResult // in-flight read left over from a cancelled call
}

// NewPromptReader returns a PromptReader over r.
func NewPromptReader(r io.Reader) *PromptReader {
	return &PromptReader{br: bufio.NewReader(r)}
}

// ReadLine returns the next input line with surrounding whitespace trimmed.
// A final line without a trailing newline is returned as a normal line; io.EOF
// is returned only when no input is left. If ctx is cancelled first it returns
// ErrPromptCancelled (ErrPromptTimeout if its deadline passed); the in-flight
// read is kept, so its line goes to the next ReadLine call instead of being
// dropped. If ctx carries a WithPromptScope scope, ReadLine waits on the
// scope's context instead of ctx and pauses the scope's deadline meanwhile.
func (p *PromptReader) ReadLine(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	ctx, resume := promptContext(ctx)
	defer resume()
	if err := ctx.Err(); err != nil {
		return "", promptCtxError(err)
	}
	ch := p.pending
	if ch == nil {
		ch = make(chan lineResult, 1)
		go func(br *bufio.Reader) {
			line, err := br.ReadString('\n')
			if err != nil && line != "" {
				err = nil // last line without trailing newline
			}
			ch <- lineResult{line: strings.TrimSpace(line), err: err}
		}(p.br)
	}

	select {
	case res := <-ch:
		p.pending = nil
		return res.line, res.err
	case <-ctx.Done():
		p.pending = ch
		return "", promptCtxError(ctx.Err())
	}
}

var (
	stdinMu     sync.Mutex
	stdinSource *os.File
	stdinReader *PromptReader
)

// stdinPromptReader returns the process-wide PromptReader for os.Stdin. It is
// rebuilt if os.Stdin is swapped (tests replace it with a pipe).
func stdinPromptReader() *PromptReader {
	stdinMu.Lock()
	defer stdinMu.Unlock()
	if stdinReader == nil || stdinSource != os.Stdin {
		stdinSource = os.Stdin
		stdinReader = NewPromptReader(os.Stdin)
	}
	return stdinReader
}

// ReadLine reads one prompt answer from stdin. See PromptReader.ReadLine.
func ReadLine(ctx context.Context) (string, error) {
	return stdinPromptReader().ReadLine(ctx)
}

// Confirm reads a yes/no answer from stdin. Only "y" or "yes" (any case)
// returns true; a bare Enter, EOF, read error, or cancellation returns false.
func Confirm(ctx context.Context) bool {
	answer, err := ReadLine(ctx)
	if err != nil {
		return false
	}
	answer = strings.ToLower(answer)
	return answer == "y" || answer == "yes"
}
