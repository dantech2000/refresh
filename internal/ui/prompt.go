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
// ErrPromptCancelled; the in-flight read is kept, so its line goes to the next
// ReadLine call instead of being dropped.
func (p *PromptReader) ReadLine(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return "", ErrPromptCancelled
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
		return "", ErrPromptCancelled
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
