package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/fatih/color"
	"github.com/pterm/pterm"
	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/commands"
	addoncmd "github.com/dantech2000/refresh/internal/commands/addon"
	clustercmd "github.com/dantech2000/refresh/internal/commands/cluster"
	ctxcmd "github.com/dantech2000/refresh/internal/commands/ctxcmd"
	"github.com/dantech2000/refresh/internal/commands/factory"
	nodegroupcmd "github.com/dantech2000/refresh/internal/commands/nodegroup"
	"github.com/dantech2000/refresh/internal/commands/runner"
	statuscmd "github.com/dantech2000/refresh/internal/commands/statuscmd"
	appconfig "github.com/dantech2000/refresh/internal/config"
	"github.com/dantech2000/refresh/internal/flagcanon"
	"github.com/dantech2000/refresh/internal/ui"
)

var (
	// Compile regex patterns once at package initialization
	sectionRegex = regexp.MustCompile(`(?m)^(NAME|USAGE|COMMANDS|GLOBAL OPTIONS|OPTIONS|DESCRIPTION|VERSION|COPYRIGHT):`)
	commandRegex = regexp.MustCompile(`(?m)^(\s+)([a-zA-Z][a-zA-Z0-9-_]*(?:,\s*[a-zA-Z][a-zA-Z0-9-_]*)*)(\s+.*)$`)
	exitProcess  = os.Exit
)

func coloredHelpPrinter(w io.Writer, templ string, data interface{}) {
	// First, render the template using the default printer to a buffer
	var buf bytes.Buffer
	cli.HelpPrinterCustom(&buf, templ, data, nil)
	_, _ = fmt.Fprint(w, colorizeHelp(buf.String()))
}

// colorizeHelp applies section-header and command-name coloring to already
// rendered help text. Command-name coloring is scoped to the COMMANDS section
// only: applied globally, commandRegex matches the leading word of any indented
// line — including wrapped DESCRIPTION prose — and colors it as if it were a
// command. Section headers are colored wherever they appear. (REF-132)
func colorizeHelp(text string) string {
	cyan := color.New(color.FgCyan, color.Bold)
	yellow := color.New(color.FgYellow)

	lines := strings.Split(text, "\n")
	inCommands := false
	for i, line := range lines {
		if m := sectionRegex.FindStringSubmatch(line); m != nil {
			inCommands = m[1] == "COMMANDS"
			lines[i] = cyan.Sprint(line)
			continue
		}
		if inCommands {
			lines[i] = commandRegex.ReplaceAllStringFunc(line, func(match string) string {
				parts := commandRegex.FindStringSubmatch(match)
				return fmt.Sprintf("%s%s%s", parts[1], yellow.Sprint(parts[2]), parts[3])
			})
		}
	}
	return strings.Join(lines, "\n")
}

func newApp() *cli.Command {
	app := &cli.Command{
		Name:  "refresh",
		Usage: "EKS upgrade companion: status, readiness, patch, upgrade",
		Description: `refresh keeps EKS clusters up to date in four steps:

   1. refresh status                 Find what is out of date in the fleet.
   2. refresh cluster upgrade-check  Check if a cluster is ready to upgrade.
   3. refresh nodegroup update       Patch nodegroups and add-ons.
      refresh addon update
   4. refresh cluster upgrade        Upgrade control plane, add-ons, nodegroups.

Commands that change a cluster support --dry-run. Use --yes in scripts to
skip the confirmation prompts.`,
		Version:               commands.VersionInfo.Version,
		EnableShellCompletion: true,
		Flags: []cli.Flag{
			&cli.DurationFlag{
				Name:    "timeout",
				Aliases: []string{"t"},
				Usage:   "Operation timeout for API calls (e.g. 60s, 2m); REFRESH_TIMEOUT sets this and list/describe/check timeouts, not long-running update waits",
				Value:   appconfig.DefaultTimeout,
				Sources: cli.EnvVars(appconfig.EnvTimeout),
			},
			&cli.IntFlag{
				Name:    "max-concurrency",
				Aliases: []string{"C"},
				Usage:   "Global max concurrency for multi-region operations (for status: clusters evaluated at once per region; regions at once = min(4, this))",
				Value:   appconfig.DefaultMaxConcurrency,
				Sources: cli.EnvVars(appconfig.EnvMaxConcurrency),
			},
			// NO_COLOR is deliberately not a flag source: urfave/cli parses env
			// sources with strconv.ParseBool, so NO_COLOR=yes would fail every
			// command. no-color.org says any non-empty value disables color;
			// Before and run() check it directly.
			&cli.BoolFlag{
				Name:  "no-color",
				Usage: "Disable colored output (a non-empty NO_COLOR env var is also honored)",
			},
			// Global AWS overrides. urfave/cli v3 propagates parent flags to every
			// subcommand, so awsconfig.Load sees these on all AWS-touching commands
			// (nodegroup/addon/cluster describe, …), honoring the documented
			// "flags override the active context for this invocation" precedence.
			// -r is the canon region letter (REF-164); the multi-region commands
			// keep their own repeatable -r/--region slice, which shadows this
			// string flag by name and alias. No -p: it has no canon meaning.
			// (REF-47)
			&cli.StringFlag{
				Name:  "profile",
				Usage: "AWS shared-config profile (overrides the active context for this invocation)",
			},
			&cli.StringFlag{
				Name:    "region",
				Aliases: []string{"r"},
				Usage:   "AWS region (overrides the active context for this invocation)",
			},
			// Logging verbosity. Default warn (quiet); --verbose is a shortcut for
			// --log-level debug. No -v alias (that's --version). (REF-37)
			&cli.StringFlag{
				Name:    "log-level",
				Usage:   "Log verbosity: debug, info, warn, error",
				Value:   "warn",
				Sources: cli.EnvVars("REFRESH_LOG_LEVEL"),
			},
			&cli.BoolFlag{
				Name:  "verbose",
				Usage: "Shortcut for --log-level debug",
			},
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			// Single logger-configuration point: every service logger flows from
			// factory.NewDefaultLogger, which reads this level. (REF-37)
			level := factory.ParseLogLevel(cmd.String("log-level"))
			if cmd.Bool("verbose") {
				level = slog.LevelDebug
			}
			factory.SetDefaultLogLevel(level)
			if cmd.Bool("no-color") || os.Getenv("NO_COLOR") != "" {
				disableColor()
			}
			return ctx, nil
		},
		Commands: []*cli.Command{
			// Fleet front door
			statuscmd.Command(),
			// Resource-first groups
			clustercmd.Command(),
			nodegroupcmd.Command(),
			addoncmd.Command(),
			// Context (kubectx-style)
			ctxcmd.UseCommand(),
			ctxcmd.CurrentCommand(),
			ctxcmd.ContextCommand(),
			// Misc
			commands.VersionCommand(),
			commands.ManPageCommand(),
			commands.CompletionCommand(),
			// Hidden: generates the Markdown command reference for the docs site.
			commands.GenDocsCommand(),
		},
	}
	// A shorthand removed in 0.11.0 fails with its replacement instead of a
	// bare "flag provided but not defined". (REF-164)
	flagcanon.Install(app)
	return app
}

// disableColor turns off color on both streams in every output library.
func disableColor() {
	ui.SetColorDisabled(true)
	color.NoColor = true
	pterm.DisableColor()
}

// colorDisabled reports whether NO_COLOR (any non-empty value, per
// no-color.org) or a --no-color flag in args disables color. It runs before
// app.Run because help is printed without calling Before.
func colorDisabled(args []string) bool {
	if os.Getenv("NO_COLOR") != "" {
		return true
	}
	for _, a := range args {
		// urfave/cli classifies a token after trimming spaces, and reads a
		// bool flag with an empty "=" value as true. Match both, so help
		// printed before Before runs honors every --no-color urfave sees.
		a = strings.TrimSpace(a)
		if a == "--" {
			break
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") || name != "no-color" {
			continue
		}
		if !hasVal || val == "" {
			return true
		}
		if b, err := strconv.ParseBool(val); err == nil && b {
			return true
		}
	}
	return false
}

func run(ctx context.Context, args []string, out, errOut io.Writer) error {
	// Stdout color follows stdout alone; stderr decides for itself through
	// ui.Stderr and ui.StderrColor.
	ui.InitColor()
	if colorDisabled(args) {
		disableColor()
	}

	// Set custom help printer for colored output
	cli.HelpPrinter = coloredHelpPrinter

	// Make `--version`/`-v` print the same details as the `version` subcommand.
	cli.VersionPrinter = func(cmd *cli.Command) {
		commands.PrintVersion(cmd.Root().Writer)
	}

	app := newApp()
	// Every command's help ends with its exit codes (REF-165).
	_ = commands.DocumentExitCodes(app)
	app.Writer = out
	app.ErrWriter = errOut
	// urfave/cli prints ExitCoder messages (cli.Exit) to its package-level
	// ErrWriter, not app.ErrWriter. main passes ui.Stderr, which strips ANSI
	// when stderr is redirected, so a colored exit message never writes raw
	// escape codes into a log file.
	cli.ErrWriter = errOut
	// Run threads ctx into every command action, so signal cancellation from
	// main propagates to in-flight AWS calls. The kubeconfig notice dedupe is
	// per run.
	return app.Run(runner.WithKubeNotices(ctx), args)
}

func main() {
	// Cancel the root context on Ctrl+C / SIGTERM so in-flight AWS calls are
	// aborted instead of running to their timeout.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// After the first signal cancels ctx, restore default signal handling so a
	// second Ctrl+C terminates the process even if something ignores ctx.
	go func() {
		<-ctx.Done()
		stop()
	}()

	if err := run(ctx, os.Args, os.Stdout, ui.Stderr); err != nil {
		// Errors belong on stderr: scripted consumers piping stdout must not
		// find error text mixed into their data.
		_, _ = fmt.Fprintln(ui.Stderr, ui.StderrColor(color.FgRed).Sprintf("Error: %v", err))
		exitProcess(1)
	}
}
