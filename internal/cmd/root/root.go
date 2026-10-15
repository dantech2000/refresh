// Package root builds the refresh command tree.
package root

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/dantech2000/refresh/internal/awsx"
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
			if err := cmdutil.CheckClusterFlag(cmd); err != nil {
				return err
			}
			return o.complete(cmd.Root().PersistentFlags(), f)
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	// Every command gets this through the root: a flag error ends with the
	// way to its help.
	cmd.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		if plain := numberFlagError(err, ""); plain != nil {
			err = plain
		}
		return fmt.Errorf("%w\nRun '%s --help' for usage", err, c.CommandPath())
	})
	cmd.SetIn(f.IOStreams.In)
	cmd.SetOut(f.IOStreams.Out)
	cmd.SetErr(f.IOStreams.ErrOut)

	pf := cmd.PersistentFlags()
	pf.StringSliceVarP(&o.regions, "region", "r", nil, "AWS region; a command that sweeps regions takes several, repeated or separated by commas (env for a sweep with -A: REFRESH_EKS_REGIONS)")
	pf.StringVar(&o.profile, "profile", "", "AWS shared-config profile (env: AWS_PROFILE)")
	pf.DurationVarP(&o.timeout, "timeout", "t", time.Minute, "Budget for the API calls of a read; in a changing command, for the reads and the requests outside a wait; with --watch, for each refresh; 0 means no limit (env: REFRESH_TIMEOUT)")
	pf.IntVar(&o.maxConcurrency, "max-concurrency", 32, "Limit for AWS requests in flight in one region (env: REFRESH_MAX_CONCURRENCY)")
	pf.BoolVar(&o.noColor, "no-color", false, "Disable color (env: NO_COLOR)")
	pf.StringVar(&o.logLevel, "log-level", "warn", "Log level: debug, info, warn, error (env: REFRESH_LOG_LEVEL)")
	pf.BoolVar(&o.verbose, "verbose", false, "Same as --log-level debug")
	pf.StringVar(&o.kubeconfig, "kubeconfig", "", "Kubeconfig file (default: $KUBECONFIG, then ~/.kube/config)")
	pf.StringVar(&o.kubeContext, "kube-context", "", "Kubeconfig context, trusted even when its server is not the cluster endpoint (default: the context whose server is the cluster endpoint; for the cluster of a read command, the current context)")

	// Defining the flag here keeps cobra from adding its own with the -v
	// shorthand, which the CLI leaves unused.
	cmd.Flags().Bool("version", false, "Print the version, the commit, and the build date")
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
		describeCompletion(completion)
	}
	return cmd
}

// completionHelp is the help of each shell's completion command: the
// paragraph of its Long about the shell, and its Example.
var completionHelp = map[string]struct{ about, example string }{
	"bash": {
		about: "The script needs the bash-completion package.",
		example: `  # Load the script into this session
  source <(refresh completion bash)

  # Install it for every new session, on Linux
  refresh completion bash > /etc/bash_completion.d/refresh

  # Install it for every new session, on macOS with Homebrew
  refresh completion bash > $(brew --prefix)/etc/bash_completion.d/refresh`,
	},
	"zsh": {
		about: `Completion must be on in zsh. If it is not, add this line to ~/.zshrc:
"autoload -U compinit; compinit".`,
		example: `  # Load the script into this session
  source <(refresh completion zsh)

  # Install it for every new session
  refresh completion zsh > "${fpath[1]}/_refresh"`,
	},
	"fish": {
		about: "fish loads the script from its completions directory.",
		example: `  # Load the script into this session
  refresh completion fish | source

  # Install it for every new session
  refresh completion fish > ~/.config/fish/completions/refresh.fish`,
	},
	"powershell": {
		about: `To load the script in every new session, add the line of the example
to your PowerShell profile.`,
		example: `  # Load the script into this session
  refresh completion powershell | Out-String | Invoke-Expression`,
	},
}

// describeCompletion replaces the help text of cobra's completion command
// and of its shells with text in the voice of the other commands.
func describeCompletion(completion *cobra.Command) {
	completion.Short = "Print a shell completion script"
	completion.Long = `Print the completion script of refresh for bash, zsh, fish, or PowerShell.
The script completes commands, flags, and the names of clusters,
nodegroups, and add-ons from your account.

Run "refresh completion <shell> --help" for the steps for one shell.`
	completion.Example = `  # Load the script into this zsh session
  source <(refresh completion zsh)

  # Install it for every new fish session
  refresh completion fish > ~/.config/fish/completions/refresh.fish`
	for _, shell := range completion.Commands() {
		help, ok := completionHelp[shell.Name()]
		if !ok {
			continue
		}
		name := shell.Name()
		if name == "powershell" {
			name = "PowerShell"
		}
		shell.Short = "Print the completion script for " + name
		shell.Long = "Print the completion script for " + name + ".\n\n" + help.about + `
Start a new shell after you install the script.

Exit codes:
  0  Done.
  1  An error.`
		shell.Example = help.example
		if fl := shell.Flags().Lookup("no-descriptions"); fl != nil {
			fl.Usage = "Leave the description of each command and flag out of the completions"
		}
	}
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
	// A flag on the command line beats its environment variable. source
	// names where each value came from, for the error of a bad one.
	source := map[string]string{"timeout": "--timeout", "max-concurrency": "--max-concurrency"}
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
			if plain := numberFlagError(err, e.env); plain != nil {
				return plain
			}
			return fmt.Errorf("invalid %s: %w", e.env, err)
		}
		source[e.flag] = e.env
	}

	// Any one of the three turns color off; nothing turns it back on.
	// NO_COLOR is not parsed as a boolean: no-color.org says that any
	// non-empty value counts.
	f.IOStreams.Dumb = os.Getenv("TERM") == "dumb"
	f.IOStreams.NoColor = o.noColor || os.Getenv("NO_COLOR") != "" || f.IOStreams.Dumb

	// A region that is named twice is one region. Cobra also parses the
	// flags of a line twice for a shell completion, which repeats every
	// value of a list flag.
	//
	// pflag reads -r "" as no value at all, so an empty flag shows only as
	// a flag that is set and holds nothing.
	if flags.Changed("region") && len(o.regions) == 0 {
		return errors.New(`invalid --region "": an empty region name`)
	}
	var regions []string
	for _, r := range o.regions {
		// "-r 'us-east-1, us-west-2'" has a space after the comma.
		r = strings.TrimSpace(r)
		if r == "" {
			return fmt.Errorf("invalid --region %q: an empty region name", strings.Join(o.regions, ","))
		}
		if err := awsx.CheckRegion(r); err != nil {
			return fmt.Errorf("invalid --region: %w", err)
		}
		if !slices.Contains(regions, r) {
			regions = append(regions, r)
		}
	}
	if o.timeout < 0 {
		return fmt.Errorf("invalid %s %s: must be 0 (no limit) or more", source["timeout"], o.timeout)
	}
	if o.maxConcurrency < 1 {
		return fmt.Errorf("invalid %s %d: must be at least 1", source["max-concurrency"], o.maxConcurrency)
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

	if f.RouteLogs != nil {
		f.RouteLogs(level)
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

// intBits is the size of each integer flag type that the CLI uses.
var intBits = map[string]int{"int": strconv.IntSize, "int32": 32, "int64": 64}

// numberFlagError rewrites the error of a whole-number flag whose value
// does not parse, which pflag reports with the text of strconv:
//
//	--desired must be a whole number, not "abc"
//	--desired is too large (at most 2147483647)
//
// name is how to call the value, such as an environment variable; "" means
// the flag. It returns nil for any other error.
func numberFlagError(err error, name string) error {
	var invalid *pflag.InvalidValueError
	var num *strconv.NumError
	if !errors.As(err, &invalid) || !errors.As(err, &num) {
		return nil
	}
	bits, ok := intBits[invalid.GetFlag().Value.Type()]
	if !ok {
		return nil
	}
	if name == "" {
		name = "--" + invalid.GetFlag().Name
	}
	if errors.Is(num.Err, strconv.ErrRange) {
		limit := int64(1)<<(bits-1) - 1
		if strings.HasPrefix(strings.TrimSpace(invalid.GetValue()), "-") {
			return fmt.Errorf("%s is too small (at least %d)", name, -limit-1)
		}
		return fmt.Errorf("%s is too large (at most %d)", name, limit)
	}
	return fmt.Errorf("%s must be a whole number, not %q", name, invalid.GetValue())
}
