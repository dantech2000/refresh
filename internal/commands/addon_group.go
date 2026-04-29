package commands

import (
	"time"

	"github.com/urfave/cli/v2"

	appconfig "github.com/dantech2000/refresh/internal/config"
)

// AddonCommand groups add-on commands
func AddonCommand() *cli.Command {
	return &cli.Command{
		Name:  "addon",
		Usage: "EKS add-on operations (list, get, update)",
		Subcommands: []*cli.Command{
			addonListCommand(),
			addonDescribeCommand(),
			addonUpdateCommand(),
			addonUpdateAllHiddenCommand(),
		},
	}
}

func addonListCommand() *cli.Command {
	orig := ListAddonsCommand()
	return &cli.Command{
		Name:        "list",
		Usage:       orig.Usage,
		Description: orig.Description,
		ArgsUsage:   orig.ArgsUsage,
		Flags:       orig.Flags,
		Action:      func(c *cli.Context) error { return runListAddons(c) },
	}
}

func addonDescribeCommand() *cli.Command {
	orig := DescribeAddonCommand()
	return &cli.Command{
		Name:        "describe",
		Aliases:     []string{"get"},
		Usage:       orig.Usage,
		Description: orig.Description,
		ArgsUsage:   orig.ArgsUsage,
		Flags:       orig.Flags,
		Action:      func(c *cli.Context) error { return runDescribeAddon(c) },
	}
}

// addonUpdateCommand merges single-addon update and update-all behavior.
// Use --all to update every addon in the cluster.
func addonUpdateCommand() *cli.Command {
	return &cli.Command{
		Name:      "update",
		Usage:     "Update an EKS add-on (use --all to update every add-on)",
		ArgsUsage: "[cluster] [addon] [version]",
		Flags: []cli.Flag{
			&cli.DurationFlag{Name: "timeout", Aliases: []string{"t"}, Usage: "Operation timeout", Value: appconfig.DefaultTimeout, EnvVars: []string{"REFRESH_TIMEOUT"}},
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name or pattern"},
			&cli.StringFlag{Name: "addon", Aliases: []string{"a"}, Usage: "Add-on name (e.g., vpc-cni)"},
			&cli.StringFlag{Name: "version", Usage: "Target version or 'latest' (can be provided as third positional)", Value: "latest"},
			&cli.BoolFlag{Name: "all", Usage: "Update all add-ons in the cluster to their latest versions"},
			&cli.BoolFlag{Name: "health-check", Aliases: []string{"H"}, Usage: "Verify each addon is ACTIVE before updating and validate version compatibility with the cluster"},
			&cli.BoolFlag{Name: "dry-run", Aliases: []string{"d"}, Usage: "Preview without applying changes"},
			&cli.BoolFlag{Name: "parallel", Aliases: []string{"p"}, Usage: "(--all only) Update addons in parallel"},
			&cli.BoolFlag{Name: "wait", Aliases: []string{"w"}, Usage: "(--all only) Wait for each update to complete"},
			&cli.DurationFlag{Name: "wait-timeout", Usage: "(--all only) Per-addon wait timeout", Value: 5 * time.Minute},
			&cli.BoolFlag{Name: "dependency-order", Usage: "(--all only) Update addons in dependency-safe order (vpc-cni -> coredns/kube-proxy -> others)"},
			&cli.StringSliceFlag{Name: "skip", Aliases: []string{"s"}, Usage: "(--all only) Skip specific addons (repeatable)"},
			&cli.StringFlag{Name: "format", Aliases: []string{"o"}, Usage: "(--all only) Output format (table, json, yaml)", Value: "table"},
		},
		Action: func(c *cli.Context) error {
			if c.Bool("all") {
				return runUpdateAllAddons(c)
			}
			return runUpdateAddon(c)
		},
	}
}

// addonUpdateAllHiddenCommand keeps `addon update-all` working as a hidden alias.
func addonUpdateAllHiddenCommand() *cli.Command {
	orig := UpdateAllAddonsCommand()
	return &cli.Command{
		Name:        "update-all",
		Hidden:      true,
		Usage:       orig.Usage,
		Description: orig.Description,
		ArgsUsage:   orig.ArgsUsage,
		Flags:       orig.Flags,
		Action:      orig.Action,
	}
}

