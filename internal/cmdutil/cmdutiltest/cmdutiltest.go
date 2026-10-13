// Package cmdutiltest has the test doubles for commands: IO streams over
// buffers and a factory that uses them.
package cmdutiltest

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"

	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/build"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/contexts"
)

// NewIOStreams returns streams over buffers, and the buffers for stdin,
// stdout, and stderr. No stream is a terminal; a test sets InTTY, OutTTY, or
// ErrTTY to change that.
func NewIOStreams() (ios *cmdutil.IOStreams, in, out, errOut *bytes.Buffer) {
	in, out, errOut = &bytes.Buffer{}, &bytes.Buffer{}, &bytes.Buffer{}
	return &cmdutil.IOStreams{In: in, Out: out, ErrOut: errOut}, in, out, errOut
}

// Now is the time of the clock of a test factory.
var Now = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// NewFactory returns a factory over buffers, and the buffers for stdin,
// stdout, and stderr. Its build is v1.2.3, commit abc1234, built at
// 2026-01-02T03:04:05Z, and its newest release is the same v1.2.3, so no
// test uses the network or prints an update hint by accident.
func NewFactory() (f *cmdutil.Factory, in, out, errOut *bytes.Buffer) {
	ios, in, out, errOut := NewIOStreams()
	f = &cmdutil.Factory{
		IOStreams: ios,
		Build:     build.Info{Version: "v1.2.3", Commit: "abc1234", Date: "2026-01-02T03:04:05Z"},
		Now:       func() time.Time { return Now },
		LatestRelease: func(context.Context) (string, error) {
			return "v1.2.3", nil
		},
		Contexts: func() (*contexts.Store, error) {
			return nil, errors.New("this test factory has no context store; set Contexts or use NewContextStore")
		},
	}
	f.AWS = func(context.Context) (*awsx.Provider, error) {
		return nil, errors.New("this test factory has no AWS; use UseAWS")
	}
	// The real resolution: a test points $KUBECONFIG or --kubeconfig at
	// the file that a kubetest server wrote.
	f.Kube = cmdutil.NewKube(f)
	return f, in, out, errOut
}

// NewContextStore gives f a context store in a temporary directory of the
// test, holding file when it is not nil, and returns the store.
func NewContextStore(t *testing.T, f *cmdutil.Factory, file *contexts.File) *contexts.Store {
	t.Helper()
	store := &contexts.Store{Path: filepath.Join(t.TempDir(), "context.yaml")}
	if file != nil {
		if err := store.Save(file); err != nil {
			t.Fatalf("saving the context file: %v", err)
		}
	}
	f.Contexts = func() (*contexts.Store, error) { return store, nil }
	return store
}

// UseAWS makes f reach AWS through cfg. A test passes the configuration of
// a fake server:
//
//	srv := awsxtest.NewServer(world)
//	cmdutiltest.UseAWS(f, srv.Config("us-east-1"))
//
// The provider is built at the first use, as in a real run, so its request
// limit is the --max-concurrency of the command line. A retry follows its
// failed attempt at once, so a test of a failure does not wait.
func UseAWS(f *cmdutil.Factory, cfg aws.Config) {
	var (
		mu       sync.Mutex
		provider *awsx.Provider
	)
	f.AWS = func(context.Context) (*awsx.Provider, error) {
		mu.Lock()
		defer mu.Unlock()
		if provider == nil {
			// As in a real run, the first --region is the home region.
			if len(f.Global.Regions) > 0 {
				cfg.Region = f.Global.Regions[0]
			}
			provider = awsx.NewProvider(cfg, func(o *awsx.ProviderOptions) {
				o.Backoff = retry.BackoffDelayerFunc(func(int, error) (time.Duration, error) { return 0, nil })
				o.MaxInFlight = f.Global.MaxConcurrency
			})
		}
		return provider, nil
	}
}
