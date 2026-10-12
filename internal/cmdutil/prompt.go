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
// When ctx is cancelled first, ReadLine returns the error of ctx. The read
// itself cannot be stopped: it goes on in the background until the input
// gives a byte or ends. That is right for a process that is about to exit.
func ReadLine(ctx context.Context, ios *IOStreams) (string, error) {
	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		line, err := readLine(ios.In)
		done <- result{line, err}
	}()
	select {
	case r := <-done:
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
