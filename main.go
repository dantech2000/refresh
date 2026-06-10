package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"syscall"

	"github.com/fatih/color"
	"github.com/urfave/cli/v2"

	"github.com/dantech2000/refresh/internal/commands"
	addoncmd "github.com/dantech2000/refresh/internal/commands/addon"
	clustercmd "github.com/dantech2000/refresh/internal/commands/cluster"
	ctxcmd "github.com/dantech2000/refresh/internal/commands/ctxcmd"
	nodegroupcmd "github.com/dantech2000/refresh/internal/commands/nodegroup"
	workloadcmd "github.com/dantech2000/refresh/internal/commands/workload"
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

	// Get the rendered help text
	helpText := buf.String()

	// Define colors
	cyan := color.New(color.FgCyan, color.Bold)
	yellow := color.New(color.FgYellow)

	// Apply colors to section headers
	helpText = sectionRegex.ReplaceAllStringFunc(helpText, func(match string) string {
		return cyan.Sprint(match)
	})

	// Color command names (looking for lines with command format)
	helpText = commandRegex.ReplaceAllStringFunc(helpText, func(match string) string {
		parts := commandRegex.FindStringSubmatch(match)
		return fmt.Sprintf("%s%s%s", parts[1], yellow.Sprint(parts[2]), parts[3])
	})

	_, _ = fmt.Fprint(w, helpText)
}

func newApp() *cli.App {
	return &cli.App{
		Name:    "refresh",
		Usage:   "Manage and monitor AWS EKS clusters and node groups",
		Version: commands.VersionInfo.Version,
		Flags: []cli.Flag{
			&cli.DurationFlag{
				Name:    "timeout",
				Aliases: []string{"t"},
				Usage:   "Operation timeout for API calls (e.g. 60s, 2m)",
				Value:   appconfig.DefaultTimeout,
				EnvVars: []string{"REFRESH_TIMEOUT"},
			},
			&cli.IntFlag{
				Name:    "max-concurrency",
				Aliases: []string{"C"},
				Usage:   "Global max concurrency for multi-region operations",
				Value:   appconfig.DefaultMaxConcurrency,
				EnvVars: []string{"REFRESH_MAX_CONCURRENCY"},
			},
		},
		Commands: []*cli.Command{
			// Resource-first groups
			clustercmd.Command(),
			nodegroupcmd.Command(),
			addoncmd.Command(),
			workloadcmd.Command(),
			// Context (kubectx-style)
			ctxcmd.UseCommand(),
			ctxcmd.CurrentCommand(),
			ctxcmd.ContextCommand(),
			// Misc
			commands.VersionCommand(),
			commands.ManPageCommand(),
		},
	}
}

func run(ctx context.Context, args []string, out, errOut io.Writer) error {
	// Set custom help printer for colored output
	cli.HelpPrinter = coloredHelpPrinter

	// Make `--version`/`-v` print the same details as the `version` subcommand.
	cli.VersionPrinter = func(c *cli.Context) {
		commands.PrintVersion(c.App.Writer)
	}

	app := newApp()
	app.Writer = out
	app.ErrWriter = errOut
	return app.RunContext(ctx, args)
}

func main() {
	// Cancel the root context on Ctrl+C / SIGTERM so in-flight AWS calls are
	// aborted instead of running to their timeout.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args, os.Stdout, os.Stderr); err != nil {
		color.Red("Error: %v", err)
		exitProcess(1)
	}
}
