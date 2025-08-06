package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"

	"github.com/fatih/color"
	"github.com/urfave/cli/v2"

	"github.com/dantech2000/refresh/internal/commands"
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
	sectionRegex := regexp.MustCompile(`(?m)^(NAME|USAGE|COMMANDS|GLOBAL OPTIONS|OPTIONS|DESCRIPTION|VERSION|COPYRIGHT):`)
	helpText = sectionRegex.ReplaceAllStringFunc(helpText, func(match string) string {
		return cyan.Sprint(match)
	})

	// Color command names (looking for lines with command format)
	commandRegex := regexp.MustCompile(`(?m)^(\s+)([a-zA-Z][a-zA-Z0-9-_]*(?:,\s*[a-zA-Z][a-zA-Z0-9-_]*)*)(\s+.*)$`)
	helpText = commandRegex.ReplaceAllStringFunc(helpText, func(match string) string {
		parts := commandRegex.FindStringSubmatch(match)
		if len(parts) >= 4 {
			return fmt.Sprintf("%s%s%s", parts[1], yellow.Sprint(parts[2]), parts[3])
		}
		return match
	})

	_, _ = fmt.Fprint(w, helpText)
}

func main() {
	// Set custom help printer for colored output
	cli.HelpPrinter = coloredHelpPrinter

	app := &cli.App{
		Name:  "refresh",
		Usage: "Manage and monitor AWS EKS node groups",
		Commands: []*cli.Command{
			commands.ListCommand(),
			commands.VersionCommand(),
			commands.UpdateAmiCommand(),
			commands.ManPageCommand(),
		},
	}

	if err := app.Run(os.Args); err != nil {
		color.Red("Error: %v", err)
		os.Exit(1)
	}
}
