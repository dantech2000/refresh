// Package cmdutiltest has the test doubles for commands: IO streams over
// buffers and a factory that uses them.
package cmdutiltest

import (
	"bytes"

	"github.com/dantech2000/refresh/internal/build"
	"github.com/dantech2000/refresh/internal/cmdutil"
)

// NewIOStreams returns streams over buffers, and the buffers for stdin,
// stdout, and stderr. No stream is a terminal; a test sets InTTY, OutTTY, or
// ErrTTY to change that.
func NewIOStreams() (ios *cmdutil.IOStreams, in, out, errOut *bytes.Buffer) {
	in, out, errOut = &bytes.Buffer{}, &bytes.Buffer{}, &bytes.Buffer{}
	return &cmdutil.IOStreams{In: in, Out: out, ErrOut: errOut}, in, out, errOut
}

// NewFactory returns a factory over buffers, and the buffers for stdin,
// stdout, and stderr. Its build is v1.2.3, commit abc1234, built at
// 2026-01-02T03:04:05Z.
func NewFactory() (f *cmdutil.Factory, in, out, errOut *bytes.Buffer) {
	ios, in, out, errOut := NewIOStreams()
	f = &cmdutil.Factory{
		IOStreams: ios,
		Build:     build.Info{Version: "v1.2.3", Commit: "abc1234", Date: "2026-01-02T03:04:05Z"},
	}
	return f, in, out, errOut
}
