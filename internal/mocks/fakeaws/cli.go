package fakeaws

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fatih/color"
	"github.com/urfave/cli/v3"
	"gopkg.in/yaml.v3"
)

// App wraps command groups in a root command that carries refresh's global
// flags, the way main.newApp does, so a test can run e.g.
// `refresh nodegroup update ...` in process. Exit errors are returned by Run,
// not turned into os.Exit.
func App(cmds ...*cli.Command) *cli.Command {
	return &cli.Command{
		Name: "refresh",
		Flags: []cli.Flag{
			&cli.DurationFlag{Name: "timeout", Aliases: []string{"t"}, Value: 30 * time.Second},
			&cli.IntFlag{Name: "max-concurrency", Aliases: []string{"C"}, Value: 4},
			&cli.BoolFlag{Name: "no-color"},
			&cli.StringFlag{Name: "profile"},
			&cli.StringFlag{Name: "region"},
			&cli.StringFlag{Name: "log-level", Value: "warn"},
			&cli.BoolFlag{Name: "verbose"},
		},
		Commands:       cmds,
		ExitErrHandler: func(context.Context, *cli.Command, error) {},
	}
}

// Run runs app with args (args[0] is the program name) and returns what it
// wrote to stdout and stderr, and the error the action returned. It swaps
// os.Stdout, os.Stderr, and fatih/color's writers for the duration, so it
// must not run in parallel with other tests.
func Run(t testing.TB, app *cli.Command, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	outR, outW, perr := os.Pipe()
	if perr != nil {
		t.Fatal(perr)
	}
	errR, errW, perr := os.Pipe()
	if perr != nil {
		t.Fatal(perr)
	}

	origOut, origErr := os.Stdout, os.Stderr
	origColorOut, origColorErr, origNoColor := color.Output, color.Error, color.NoColor
	os.Stdout, os.Stderr = outW, errW
	color.Output, color.Error, color.NoColor = outW, errW, true
	app.Writer, app.ErrWriter = outW, errW

	var outBuf, errBuf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(&outBuf, outR) }()
	go func() { defer wg.Done(); _, _ = io.Copy(&errBuf, errR) }()

	func() {
		defer func() {
			os.Stdout, os.Stderr = origOut, origErr
			color.Output, color.Error, color.NoColor = origColorOut, origColorErr, origNoColor
			_ = outW.Close()
			_ = errW.Close()
		}()
		err = app.Run(t.Context(), args)
	}()
	wg.Wait()
	_ = outR.Close()
	_ = errR.Close()
	return outBuf.String(), errBuf.String(), err
}

// RequireOneDocument fails t unless stdout is exactly one JSON or YAML
// (format) document whose top level is an object or a list, with nothing
// before or after it. It returns the decoded document. The object/list rule
// matters for YAML: a stray human line would otherwise parse as a string.
func RequireOneDocument(t testing.TB, format, stdout string) any {
	t.Helper()
	doc, err := decodeOne(format, stdout)
	if err != nil {
		t.Fatalf("stdout is not exactly one %s document: %v\n--- stdout ---\n%s", format, err, stdout)
	}
	return doc
}

func decodeOne(format, s string) (any, error) {
	var first, second any
	var err1, err2 error
	switch format {
	case "json":
		dec := json.NewDecoder(strings.NewReader(s))
		err1 = dec.Decode(&first)
		if err1 == nil {
			err2 = dec.Decode(&second)
		}
	case "yaml":
		dec := yaml.NewDecoder(strings.NewReader(s))
		err1 = dec.Decode(&first)
		if err1 == nil {
			err2 = dec.Decode(&second)
		}
	default:
		return nil, errors.New("unknown format " + format)
	}
	if err1 != nil {
		return nil, err1
	}
	if !errors.Is(err2, io.EOF) {
		return nil, errors.New("more than one document, or trailing text")
	}
	switch first.(type) {
	case map[string]any, []any:
		return first, nil
	default:
		return nil, errors.New("top level is not an object or a list")
	}
}
