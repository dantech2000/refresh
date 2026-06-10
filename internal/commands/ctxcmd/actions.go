package ctxcmd

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/fatih/color"
	"github.com/urfave/cli/v2"

	"github.com/dantech2000/refresh/internal/cliconfig"
)

func runUse(c *cli.Context) error {
	f, err := cliconfig.Load()
	if err != nil {
		return err
	}
	if len(f.Contexts) == 0 {
		return fmt.Errorf("no contexts saved. Add one with: refresh context add <name> --cluster <cluster> [--region <r>] [--profile <p>]")
	}

	name := strings.TrimSpace(c.Args().First())
	if name == "" {
		picked, err := pickContext(f)
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
	return nil
}

func runCurrent(c *cli.Context) error {
	f, err := cliconfig.Load()
	if err != nil {
		return err
	}
	name, ctx, ok := f.Active()
	if !ok {
		color.Yellow("No active context. Set one with: refresh use <name>")
		return nil
	}
	fmt.Printf("%s  cluster=%s  region=%s  profile=%s\n",
		color.CyanString(name), ctx.Cluster, orDash(ctx.Region), orDash(ctx.Profile))
	return nil
}

func contextListCommand() *cli.Command {
	return &cli.Command{
		Name:    "list",
		Aliases: []string{"ls"},
		Usage:   "List saved contexts",
		Action: func(c *cli.Context) error {
			f, err := cliconfig.Load()
			if err != nil {
				return err
			}
			if len(f.Contexts) == 0 {
				color.Yellow("No saved contexts. Add one with: refresh context add <name> --cluster <cluster>")
				return nil
			}
			activeName, _, _ := f.Active()
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
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name", Required: true},
			&cli.StringFlag{Name: "region", Aliases: []string{"r"}, Usage: "AWS region (optional)"},
			&cli.StringFlag{Name: "profile", Aliases: []string{"p"}, Usage: "AWS shared-config profile (optional)"},
			&cli.BoolFlag{Name: "use", Usage: "Switch to this context after adding"},
		},
		Action: func(c *cli.Context) error {
			name := strings.TrimSpace(c.Args().First())
			if name == "" {
				return fmt.Errorf("context name is required")
			}
			f, err := cliconfig.Load()
			if err != nil {
				return err
			}
			ctx := cliconfig.Context{
				Cluster: c.String("cluster"),
				Region:  c.String("region"),
				Profile: c.String("profile"),
			}
			if err := f.Set(name, ctx); err != nil {
				return err
			}
			if c.Bool("use") {
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
		Name:         "remove",
		Aliases:      []string{"rm", "delete"},
		Usage:        "Remove a saved context",
		ArgsUsage:    "<name>",
		BashComplete: completeContextNames,
		Action: func(c *cli.Context) error {
			name := strings.TrimSpace(c.Args().First())
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

func pickContext(f *cliconfig.File) (string, error) {
	names := f.Names()
	color.Cyan("Available contexts:")
	for i, n := range names {
		marker := " "
		if n == f.Current {
			marker = color.GreenString("*")
		}
		ctx := f.Contexts[n]
		fmt.Printf("  %s %d) %-20s cluster=%s region=%s\n", marker, i+1, n, ctx.Cluster, orDash(ctx.Region))
	}
	fmt.Print("Select context [number or name]: ")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimSpace(line)
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
