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
	statuscmd "github.com/dantech2000/refresh/internal/commands/statuscmd"
	appconfig "github.com/dantech2000/refresh/internal/config"
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
	return &cli.Command{
		Name:                  "refresh",
		Usage:                 "Manage and monitor AWS EKS clusters and nodegroups",
		Version:               commands.VersionInfo.Version,
		EnableShellCompletion: true,
		Flags: []cli.Flag{
			&cli.DurationFlag{
				Name:    "timeout",
				Aliases: []string{"t"},
				Usage:   "Operation timeout for API calls (e.g. 60s, 2m)",
				Value:   appconfig.DefaultTimeout,
				Sources: cli.EnvVars("REFRESH_TIMEOUT"),
			},
			&cli.IntFlag{
				Name:    "max-concurrency",
				Aliases: []string{"C"},
				Usage:   "Global max concurrency for multi-region operations",
				Value:   appconfig.DefaultMaxConcurrency,
				Sources: cli.EnvVars("REFRESH_MAX_CONCURRENCY"),
			},
			&cli.BoolFlag{
				Name:    "no-color",
				Usage:   "Disable colored output (NO_COLOR env is also honored)",
				Sources: cli.EnvVars("NO_COLOR"),
			},
			// Global AWS overrides. urfave/cli v3 propagates parent flags to every
			// subcommand, so awsconfig.Load sees these on all AWS-touching commands
			// (nodegroup/addon/cluster describe, …), honoring the documented
			// "flags override the active context for this invocation" precedence.
			// No -r/-p aliases: -p already means --poll-interval/--parallel and the
			// list commands keep their own repeatable -r/--region slice, which
			// shadows this string flag cleanly. (REF-47)
			&cli.StringFlag{
				Name:  "profile",
				Usage: "AWS shared-config profile (overrides the active context for this invocation)",
			},
			&cli.StringFlag{
				Name:  "region",
				Usage: "AWS region (overrides the active context for this invocation)",
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
			if cmd.Bool("no-color") {
				color.NoColor = true
				pterm.DisableColor()
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
}

func run(ctx context.Context, args []string, out, errOut io.Writer) error {
	// Set custom help printer for colored output
	cli.HelpPrinter = coloredHelpPrinter

	// Make `--version`/`-v` print the same details as the `version` subcommand.
	cli.VersionPrinter = func(cmd *cli.Command) {
		commands.PrintVersion(cmd.Root().Writer)
	}

	app := newApp()
	app.Writer = out
	app.ErrWriter = errOut
	// Run threads ctx into every command action, so signal cancellation from
	// main propagates to in-flight AWS calls.
	return app.Run(ctx, args)
}

func main() {
	// Cancel the root context on Ctrl+C / SIGTERM so in-flight AWS calls are
	// aborted instead of running to their timeout.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args, os.Stdout, os.Stderr); err != nil {
		// Errors belong on stderr: scripted consumers piping stdout must not
		// find error text mixed into their data.
		fmt.Fprintln(os.Stderr, color.RedString("Error: %v", err))
		exitProcess(1)
	}
}
