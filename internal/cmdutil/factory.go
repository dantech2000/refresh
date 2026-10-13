package cmdutil

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/build"
	"github.com/dantech2000/refresh/internal/contexts"
	"github.com/dantech2000/refresh/internal/kube"
)

// Factory gives a command its dependencies. main builds one for the run, and
// tests build one over buffers.
type Factory struct {
	IOStreams *IOStreams
	Build     build.Info

	// LatestRelease returns the tag of the newest release of refresh. It may
	// use the network.
	LatestRelease func(ctx context.Context) (string, error)

	// Contexts returns the store of the saved refresh contexts. A command
	// calls it when it needs the store, not before.
	Contexts func() (*contexts.Store, error)

	// AWS returns the provider of AWS clients. A command calls it when it
	// is about to use AWS, not before: the first call loads the AWS
	// configuration and resolves the credentials.
	AWS func(ctx context.Context) (*awsx.Provider, error)

	// Kube returns the Kubernetes client for a target, once per target and
	// run. It makes no request. A *kube.MismatchError, or any other error,
	// is returned and not printed: the command says what it does without
	// Kubernetes.
	Kube func(target kube.Target) (*kube.Client, error)

	// Now returns the time for what a command computes from the clock:
	// the days left of a support window, the age of an AMI. Nil means
	// time.Now. A test sets a fixed time.
	Now func() time.Time

	// After returns a channel that delivers after a duration, for the
	// pause between two reads of a watch. Nil means time.After.
	After func(d time.Duration) <-chan time.Time

	// AfterFunc starts a timer that calls f after a duration, and returns
	// the function that stops it. A change uses it to end a call that
	// hangs past a time budget. Nil means time.AfterFunc. A test that sets
	// a clock of its own sets it too, so no real timer runs.
	AfterFunc func(d time.Duration, f func()) (stop func() bool)

	// Tick returns a channel that delivers after a duration, for the node
	// observer beside the wait of a roll. It is apart from After: the
	// observer runs beside the wait, and a test clock that moves with each
	// pause of the wait must not move for the observer too. Nil means
	// time.After.
	Tick func(d time.Duration) <-chan time.Time

	// HTTPClient is the client for what a command reads from the web: the
	// AMI release notes of "nodegroup update --changelog". Nil means that
	// the run has no such access, and the command says so. A test sets a
	// client for a server of its own, so no test reaches the network.
	HTTPClient *http.Client

	// RouteKubeLogs routes what client-go and its informers log, once the
	// log level is known. They log through klog, which is one logger for
	// the whole process, so the process sets it and no package does: the
	// root command calls this once, after the global flags. Nil leaves
	// klog alone, so a test of a command does not change the logger of its
	// test binary.
	RouteKubeLogs func(level slog.Level)

	// Global holds the values of the global flags. The root command fills it
	// before a command runs.
	Global GlobalOptions
}

// GlobalOptions are the values of the flags that every command accepts,
// after the environment is applied.
type GlobalOptions struct {
	Regions        []string
	Profile        string
	Timeout        time.Duration
	MaxConcurrency int
	LogLevel       slog.Level
	// Kubeconfig and KubeContext are the values of --kubeconfig and
	// --kube-context. Empty means the kubeconfig that kubectl reads and
	// its current context.
	Kubeconfig  string
	KubeContext string
}
