package ctxcmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/fatih/color"
	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/cliconfig"
	"github.com/dantech2000/refresh/internal/ui"
)

func runUse(ctx context.Context, cmd *cli.Command) error {
	f, err := cliconfig.Load()
	if err != nil {
		return err
	}
	if len(f.Contexts) == 0 {
		return fmt.Errorf("no contexts saved. Add one with: refresh context add <name> --cluster <cluster> [--region <r>] [--profile <p>]")
	}

	name := strings.TrimSpace(cmd.Args().First())
	if name == "" {
		picked, err := pickContext(ctx, f)
		if err != nil {
			return err
		}
		name = picked
	}

	if err := f.Use(name); err != nil {
		return err
	}
	if err := cliconfig.Save(f); err != nil {
		return err
	}
	active := f.Contexts[f.Current]
	color.Green("Switched to context %q (cluster=%s region=%s profile=%s)",
		f.Current, active.Cluster, orDash(active.Region), orDash(active.Profile))
	// REFRESH_CONTEXT beats the saved current context, so in this shell
	// commands still use the env context. Say so rather than let "Switched"
	// stand alone.
	if env := os.Getenv("REFRESH_CONTEXT"); env != "" && env != f.Current {
		_, _ = ui.StderrColor(color.FgYellow).Fprintf(ui.Stderr,
			"warning: REFRESH_CONTEXT=%s overrides the saved context in this shell; unset REFRESH_CONTEXT to use %q here\n", env, f.Current)
	}
	return nil
}

func runCurrent(_ context.Context, _ *cli.Command) error {
	f, err := cliconfig.Load()
	if err != nil {
		return err
	}
	name, ctx, ok, err := f.Active()
	if err != nil {
		return err
	}
	if !ok {
		// A hint, not data: stderr keeps `refresh current` stdout empty.
		_, _ = fmt.Fprintln(ui.Stderr, ui.StderrColor(color.FgYellow).Sprint("No active context. Set one with: refresh use <name>"))
		return nil
	}
	fmt.Printf("%s  cluster=%s  region=%s  profile=%s\n",
		color.CyanString(name), ctx.Cluster, orDash(ctx.Region), orDash(ctx.Profile))
	return nil
}

func contextListCommand() *cli.Command {
	return &cli.Command{
		Name:        "list",
		Aliases:     []string{"ls"},
		Usage:       "List saved contexts",
		Description: `List every saved context with its cluster, region, and profile. The active context (set via 'refresh use') is marked with a '*'.`,
		Action: func(_ context.Context, _ *cli.Command) error {
			f, err := cliconfig.Load()
			if err != nil {
				return err
			}
			if len(f.Contexts) == 0 {
				_, _ = fmt.Fprintln(ui.Stderr, ui.StderrColor(color.FgYellow).Sprint("No saved contexts. Add one with: refresh context add <name> --cluster <cluster>"))
				return nil
			}
			// The list is how a user finds the right name, so an unknown
			// REFRESH_CONTEXT is a warning here, not an error.
			activeName, _, _, aerr := f.Active()
			if aerr != nil {
				_, _ = fmt.Fprintln(ui.Stderr, ui.StderrColor(color.FgYellow).Sprintf("warning: %v", aerr))
			}
			for _, n := range f.Names() {
				ctx := f.Contexts[n]
				marker := "  "
				if n == activeName {
					marker = color.GreenString("* ")
				}
				fmt.Printf("%s%-20s cluster=%s region=%s profile=%s\n",
					marker, n, ctx.Cluster, orDash(ctx.Region), orDash(ctx.Profile))
			}
			return nil
		},
	}
}

func contextAddCommand() *cli.Command {
	return &cli.Command{
		Name:      "add",
		Usage:     "Add or update a saved context",
		ArgsUsage: "<name>",
		Description: `Create or overwrite a named context that binds a cluster to an optional
region and AWS profile. Re-running with the same name updates it in place.
Pass --use to switch to the context immediately after saving.

  refresh context add prod --cluster prod-eks --region us-east-1 --profile prod
  refresh context add prod --cluster prod-eks --use`,
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name", Required: true},
			&cli.StringFlag{Name: "region", Aliases: []string{"r"}, Usage: "AWS region (optional)"},
			&cli.StringFlag{Name: "profile", Usage: "AWS shared-config profile (optional)"},
			&cli.BoolFlag{Name: "use", Usage: "Switch to this context after adding"},
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
			name := strings.TrimSpace(cmd.Args().First())
			if name == "" {
				return fmt.Errorf("context name is required")
			}
			f, err := cliconfig.Load()
			if err != nil {
				return err
			}
			ctx := cliconfig.Context{
				Cluster: cmd.String("cluster"),
				Region:  cmd.String("region"),
				Profile: cmd.String("profile"),
			}
			if err := f.Set(name, ctx); err != nil {
				return err
			}
			if cmd.Bool("use") {
				if err := f.Use(name); err != nil {
					return err
				}
			}
			if err := cliconfig.Save(f); err != nil {
				return err
			}
			color.Green("Saved context %q", name)
			return nil
		},
	}
}

func contextRemoveCommand() *cli.Command {
	return &cli.Command{
		Name:          "remove",
		Aliases:       []string{"rm", "delete"},
		Usage:         "Remove a saved context",
		ArgsUsage:     "<name>",
		Description:   `Delete a saved context by name. If the removed context was the active or previous one, those pointers are cleared.`,
		ShellComplete: completeContextNames,
		Action: func(_ context.Context, cmd *cli.Command) error {
			name := strings.TrimSpace(cmd.Args().First())
			if name == "" {
				return fmt.Errorf("context name is required")
			}
			f, err := cliconfig.Load()
			if err != nil {
				return err
			}
			if err := f.Remove(name); err != nil {
				return err
			}
			if err := cliconfig.Save(f); err != nil {
				return err
			}
			color.Green("Removed context %q", name)
			return nil
		},
	}
}

func pickContext(ctx context.Context, f *cliconfig.File) (string, error) {
	names := f.Names()
	// Mark the context commands use now, as `context list` does: with
	// REFRESH_CONTEXT set, that is not the saved current one. An unknown
	// REFRESH_CONTEXT marks none; `use` reports it after the switch.
	activeName, _, _, _ := f.Active() //nolint:dogsled // only the name matters for the marker
	color.Cyan("Available contexts:")
	for i, n := range names {
		marker := " "
		if n == activeName {
			marker = color.GreenString("*")
		}
		ctx := f.Contexts[n]
		fmt.Printf("  %s %d) %-20s cluster=%s region=%s\n", marker, i+1, n, ctx.Cluster, orDash(ctx.Region))
	}
	fmt.Print("Select context [number or name]: ")
	line, err := ui.ReadLine(ctx)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if line == "" {
		return "", fmt.Errorf("no selection made")
	}
	if i, err := strconv.Atoi(line); err == nil {
		if i < 1 || i > len(names) {
			return "", fmt.Errorf("selection %d out of range", i)
		}
		return names[i-1], nil
	}
	return line, nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
