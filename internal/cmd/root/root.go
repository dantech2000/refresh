// Package root builds the refresh command tree.
package root

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	versioncmd "github.com/dantech2000/refresh/internal/cmd/version"
	"github.com/dantech2000/refresh/internal/cmdutil"
)

type options struct {
	regions        []string
	profile        string
	timeout        time.Duration
	maxConcurrency int
	noColor        bool
	logLevel       string
	verbose        bool
}

// NewCmdRoot returns the root command with every subcommand attached. It
// reads and writes through f.IOStreams.
func NewCmdRoot(f *cmdutil.Factory) *cobra.Command {
	o := &options{}
	cmd := &cobra.Command{
		Use:   "refresh",
		Short: "EKS upgrade companion: status, readiness, patch, upgrade",
		Long: `refresh keeps EKS clusters up to date in four steps:

  1. refresh status                 Find what is out of date in the fleet.
  2. refresh cluster upgrade-check  Check if a cluster is ready to upgrade.
  3. refresh nodegroup update       Patch nodegroups and add-ons.
     refresh addon update
  4. refresh cluster upgrade        Upgrade control plane, nodegroups, add-ons.

Commands that change a cluster support --dry-run. Use --yes in scripts to
skip the confirmation prompts.`,
		Version:       f.Build.Version,
		SilenceErrors: true,
		SilenceUsage:  true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return o.complete(cmd.Root().PersistentFlags(), f)
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.SetIn(f.IOStreams.In)
	cmd.SetOut(f.IOStreams.Out)
	cmd.SetErr(f.IOStreams.ErrOut)

	pf := cmd.PersistentFlags()
	pf.StringArrayVarP(&o.regions, "region", "r", nil, "AWS region; repeatable on commands that sweep regions")
	pf.StringVar(&o.profile, "profile", "", "AWS shared-config profile")
	pf.DurationVarP(&o.timeout, "timeout", "t", time.Minute, "Budget for the API calls of a read command (env: REFRESH_TIMEOUT)")
	pf.IntVar(&o.maxConcurrency, "max-concurrency", 8, "Limit for parallel calls (env: REFRESH_MAX_CONCURRENCY)")
	pf.BoolVar(&o.noColor, "no-color", false, "Disable color (env: NO_COLOR)")
	pf.StringVar(&o.logLevel, "log-level", "warn", "Log level: debug, info, warn, error (env: REFRESH_LOG_LEVEL)")
	pf.BoolVar(&o.verbose, "verbose", false, "Same as --log-level debug")

	// Defining the flag here keeps cobra from adding its own with the -v
	// shorthand, which the CLI leaves unused.
	cmd.Flags().Bool("version", false, "Print the version")
	cmd.SetVersionTemplate(versioncmd.Format(f.Build))

	cmd.AddCommand(versioncmd.NewCmdVersion(f))
	// Cobra adds this command when the root runs. Adding it now puts it in
	// the tree for anything that walks the commands first.
	cmd.InitDefaultCompletionCmd()
	return cmd
}

// complete applies the environment to the global flags that are not on the
// command line, checks the values, and stores them in f. It is the only
// place that reads the environment for a global flag.
func (o *options) complete(flags *pflag.FlagSet, f *cmdutil.Factory) error {
	// A flag on the command line beats its environment variable.
	for _, e := range []struct{ flag, env string }{
		{"timeout", "REFRESH_TIMEOUT"},
		{"max-concurrency", "REFRESH_MAX_CONCURRENCY"},
		{"log-level", "REFRESH_LOG_LEVEL"},
	} {
		v := os.Getenv(e.env)
		if v == "" || flags.Changed(e.flag) {
			continue
		}
		if err := flags.Set(e.flag, v); err != nil {
			return fmt.Errorf("invalid %s: %w", e.env, err)
		}
	}

	// NO_COLOR is not parsed as a boolean: no-color.org says that any
	// non-empty value turns color off.
	if !flags.Changed("no-color") {
		o.noColor = os.Getenv("NO_COLOR") != ""
	}
	f.IOStreams.NoColor = o.noColor || os.Getenv("TERM") == "dumb"

	if o.maxConcurrency < 1 {
		return fmt.Errorf("invalid --max-concurrency %d: must be at least 1", o.maxConcurrency)
	}
	level, err := parseLogLevel(o.logLevel)
	if err != nil {
		return err
	}
	if o.verbose {
		level = slog.LevelDebug
	}

	f.Global = cmdutil.GlobalOptions{
		Regions:        o.regions,
		Profile:        o.profile,
		Timeout:        o.timeout,
		MaxConcurrency: o.maxConcurrency,
		LogLevel:       level,
	}
	return nil
}

func parseLogLevel(s string) (slog.Level, error) {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("invalid log level %q: must be one of debug, info, warn, error", s)
}
