package cmdutil

import (
	"context"
	"errors"
	"io"
	"strings"
)

// ReadLine reads one line from the input stream and returns it without the
// line end and the spaces around it. At the end of the input it returns what
// it read and no error.
//
// A cancelled ctx always wins. When ctx is cancelled before the call,
// ReadLine reads nothing. When ctx is cancelled during the read, ReadLine
// returns the error of ctx, also when a line arrived at the same moment.
//
// The read itself cannot be stopped. After a cancellation it stays blocked
// in the background until the input gives a line or ends. On a terminal
// that is when the process exits, which is what follows an interrupt.
//
// Under a context from WithTimeout, the clock of that timeout stops while
// ReadLine waits, and ReadLine waits on the signal context: a deadline does
// not end a prompt.
func ReadLine(ctx context.Context, ios *IOStreams) (string, error) {
	ctx, done := promptContext(ctx)
	defer done()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	type result struct {
		line string
		err  error
	}
	read := make(chan result, 1)
	go func() {
		line, err := readLine(ios.In)
		read <- result{line, err}
	}()
	select {
	case r := <-read:
		// Both cases can be ready; select then picks either one.
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return strings.TrimSpace(r.line), r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// readLine reads up to and including the next line feed, one byte at a
// time, so that it takes nothing from the input that a later read needs.
func readLine(r io.Reader) (string, error) {
	var line []byte
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n == 1 {
			if buf[0] == '\n' {
				return string(line), nil
			}
			line = append(line, buf[0])
		}
		if errors.Is(err, io.EOF) {
			return string(line), nil
		}
		if err != nil {
			return "", err
		}
	}
}
