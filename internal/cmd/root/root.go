// Package root builds the refresh command tree.
package root

import (
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	addoncmd "github.com/dantech2000/refresh/internal/cmd/addon"
	clustercmd "github.com/dantech2000/refresh/internal/cmd/cluster"
	"github.com/dantech2000/refresh/internal/cmd/contextcmd"
	"github.com/dantech2000/refresh/internal/cmd/gendocs"
	nodegroupcmd "github.com/dantech2000/refresh/internal/cmd/nodegroup"
	statuscmd "github.com/dantech2000/refresh/internal/cmd/status"
	uicmd "github.com/dantech2000/refresh/internal/cmd/ui"
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
	kubeconfig     string
	kubeContext    string
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
		Example: `  # Save a context for a cluster and make it active
  refresh context add prod --cluster prod-eks --region us-east-1 --use

  # Show the active context
  refresh context current`,
		// No Args: cobra's own check for a root with subcommands answers an
		// unknown command with "Did you mean this?" suggestions, and an Args
		// function here would replace it.
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
	// Every command gets this through the root: a flag error ends with the
	// way to its help.
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return fmt.Errorf("%w\nRun '%s --help' for usage", err, c.CommandPath())
	})
	cmd.SetIn(f.IOStreams.In)
	cmd.SetOut(f.IOStreams.Out)
	cmd.SetErr(f.IOStreams.ErrOut)

	pf := cmd.PersistentFlags()
	pf.StringSliceVarP(&o.regions, "region", "r", nil, "AWS region; a command that sweeps regions takes several, repeated or separated by commas")
	pf.StringVar(&o.profile, "profile", "", "AWS shared-config profile")
	pf.DurationVarP(&o.timeout, "timeout", "t", time.Minute, "Budget for the API calls of a read command (env: REFRESH_TIMEOUT)")
	pf.IntVar(&o.maxConcurrency, "max-concurrency", 32, "Limit for AWS requests in flight in one region (env: REFRESH_MAX_CONCURRENCY)")
	pf.BoolVar(&o.noColor, "no-color", false, "Disable color (env: NO_COLOR)")
	pf.StringVar(&o.logLevel, "log-level", "warn", "Log level: debug, info, warn, error (env: REFRESH_LOG_LEVEL)")
	pf.BoolVar(&o.verbose, "verbose", false, "Same as --log-level debug")
	pf.StringVar(&o.kubeconfig, "kubeconfig", "", "Kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)")
	pf.StringVar(&o.kubeContext, "kube-context", "", "Kubeconfig context (default: the current context)")

	// Defining the flag here keeps cobra from adding its own with the -v
	// shorthand, which the CLI leaves unused.
	cmd.Flags().Bool("version", false, "Print the version")
	cmd.SetVersionTemplate(versioncmd.Format(f.Build))

	cmd.AddGroup(
		&cobra.Group{ID: groupCore, Title: "Core Commands:"},
		&cobra.Group{ID: groupOther, Title: "Other Commands:"},
	)
	addCommands(cmd, groupCore,
		statuscmd.NewCmdStatus(f),
		clustercmd.NewCmdCluster(f),
		nodegroupcmd.NewCmdNodegroup(f),
		addoncmd.NewCmdAddon(f),
		contextcmd.NewCmdContext(f),
		uicmd.NewCmdUI(f),
	)
	addCommands(cmd, groupOther,
		versioncmd.NewCmdVersion(f),
		gendocs.NewCmdGenDocs(f),
	)
	// Cobra adds these two commands when the root runs. Adding them now
	// puts them in the tree for anything that walks the commands first.
	cmd.SetHelpCommandGroupID(groupOther)
	cmd.SetCompletionCommandGroupID(groupOther)
	cmd.InitDefaultHelpCmd()
	cmd.InitDefaultCompletionCmd()
	// Cobra's completion command prints help and exits 0 for a shell that
	// it does not know. With Args and RunE that is an error.
	if completion, _, err := cmd.Find([]string{"completion"}); err == nil && completion != cmd {
		completion.Args = cobra.NoArgs
		completion.RunE = func(c *cobra.Command, _ []string) error { return c.Help() }
	}
	return cmd
}

// The groups of the root help.
const (
	groupCore  = "core"
	groupOther = "other"
)

func addCommands(parent *cobra.Command, groupID string, cmds ...*cobra.Command) {
	for _, c := range cmds {
		c.GroupID = groupID
		parent.AddCommand(c)
	}
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
		// --verbose sets the level, so the level in the environment is
		// not used and not checked.
		if v == "" || flags.Changed(e.flag) || (e.flag == "log-level" && o.verbose) {
			continue
		}
		if err := flags.Set(e.flag, v); err != nil {
			return fmt.Errorf("invalid %s: %w", e.env, err)
		}
	}

	// Any one of the three turns color off; nothing turns it back on.
	// NO_COLOR is not parsed as a boolean: no-color.org says that any
	// non-empty value counts.
	f.IOStreams.NoColor = o.noColor || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb"

	// A region that is named twice is one region. Cobra also parses the
	// flags of a line twice for a shell completion, which repeats every
	// value of a list flag.
	var regions []string
	for _, r := range o.regions {
		if strings.TrimSpace(r) == "" {
			return fmt.Errorf("invalid --region %q: an empty region name", strings.Join(o.regions, ","))
		}
		if !slices.Contains(regions, r) {
			regions = append(regions, r)
		}
	}
	if o.maxConcurrency < 1 {
		return fmt.Errorf("invalid --max-concurrency %d: must be at least 1", o.maxConcurrency)
	}
	// --verbose is --log-level debug: it wins, and the level it replaces
	// is not checked.
	level := slog.LevelDebug
	if !o.verbose {
		var err error
		if level, err = parseLogLevel(o.logLevel); err != nil {
			return err
		}
	}

	if f.RouteKubeLogs != nil {
		f.RouteKubeLogs(level)
	}

	f.Global = cmdutil.GlobalOptions{
		Regions:        regions,
		Profile:        o.profile,
		Timeout:        o.timeout,
		MaxConcurrency: o.maxConcurrency,
		LogLevel:       level,
		Kubeconfig:     o.kubeconfig,
		KubeContext:    o.kubeContext,
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
