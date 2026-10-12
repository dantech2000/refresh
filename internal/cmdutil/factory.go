package cmdutil

import (
	"context"
	"log/slog"
	"time"

	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/build"
	"github.com/dantech2000/refresh/internal/contexts"
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
}
