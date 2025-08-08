package commands

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/fatih/color"
	"github.com/urfave/cli/v2"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	appconfig "github.com/dantech2000/refresh/internal/config"
	"github.com/dantech2000/refresh/internal/services/cluster"
)

// UpdateAddonCommand updates an EKS add-on to a target version (or latest)
func UpdateAddonCommand() *cli.Command {
	return &cli.Command{
		Name:      "update-addon",
		Usage:     "Update an EKS add-on (optionally to latest)",
		ArgsUsage: "[cluster] [addon] [version]",
		Flags: []cli.Flag{
			&cli.DurationFlag{Name: "timeout", Aliases: []string{"t"}, Usage: "Operation timeout", Value: appconfig.DefaultTimeout, EnvVars: []string{"REFRESH_TIMEOUT"}},
			&cli.StringFlag{Name: "cluster", Aliases: []string{"c"}, Usage: "EKS cluster name or pattern"},
			&cli.StringFlag{Name: "addon", Aliases: []string{"a"}, Usage: "Add-on name (e.g., vpc-cni)"},
			&cli.StringFlag{Name: "version", Usage: "Target version or 'latest' (can be provided as third positional)", Value: "latest"},
			&cli.BoolFlag{Name: "health-check", Aliases: []string{"H"}, Usage: "Placeholder for future pre/post health validation"},
			&cli.BoolFlag{Name: "dry-run", Aliases: []string{"d"}, Usage: "Preview without applying changes"},
		},
		Action: func(c *cli.Context) error { return runUpdateAddon(c) },
	}
}

func runUpdateAddon(c *cli.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Duration("timeout"))
	defer cancel()

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		color.Red("Failed to load AWS config: %v", err)
		return err
	}
	if err := awsinternal.ValidateAWSCredentials(ctx, cfg); err != nil {
		color.Red("%v", err)
		fmt.Println()
		awsinternal.PrintCredentialHelp()
		return fmt.Errorf("AWS credential validation failed")
	}
	// Resolve cluster: positional first, then flag; list clusters if omitted
	requested := c.Args().First()
	if requested == "" {
		requested = c.String("cluster")
	}
	if strings.TrimSpace(requested) == "" {
		color.Yellow("No cluster specified. Available clusters:")
		svc := cluster.NewService(cfg, nil, nil)
		summaries, err := svc.List(ctx, cluster.ListOptions{})
		if err != nil {
			return err
		}
		_ = outputClustersTable(summaries, 0, false, false)
		return nil
	}
	clusterName, err := awsinternal.ClusterName(ctx, cfg, requested)
	if err != nil {
		return err
	}

	// Resolve addon: flag, then second positional (skip flags); validate/auto-resolve
	addonName := c.String("addon")
	if strings.TrimSpace(addonName) == "" {
		var nonFlags []string
		for _, tok := range c.Args().Slice() {
			if strings.HasPrefix(tok, "-") {
				continue
			}
			nonFlags = append(nonFlags, tok)
		}
		if len(nonFlags) >= 2 {
			addonName = nonFlags[1]
		}
	}
	addonName = strings.TrimSpace(addonName)
	if addonName == "" {
		return fmt.Errorf("missing add-on name; pass as second argument or --addon <name>")
	}

	eksClient := eks.NewFromConfig(cfg)

	// Resolve latest version if requested
	version := c.String("version")
	if !c.IsSet("version") {
		var nonFlags []string
		for _, tok := range c.Args().Slice() {
			if strings.HasPrefix(tok, "-") {
				continue
			}
			nonFlags = append(nonFlags, tok)
		}
		if len(nonFlags) >= 3 {
			version = nonFlags[2]
		}
		if strings.TrimSpace(version) == "" {
			version = "latest"
		}
	}

	targetVersion := version
	if strings.EqualFold(version, "latest") {
		avail, err := eksClient.DescribeAddonVersions(ctx, &eks.DescribeAddonVersionsInput{AddonName: aws.String(addonName)})
		if err != nil || len(avail.Addons) == 0 || len(avail.Addons[0].AddonVersions) == 0 {
			return awsinternal.FormatAWSError(err, "resolving latest add-on version")
		}
		// Choose the first as latest for now; SDK returns sorted versions
		targetVersion = aws.ToString(avail.Addons[0].AddonVersions[0].AddonVersion)
	}

	// Validate addon name formatting and attempt to auto-resolve if malformed
	validRe := regexp.MustCompile(`^[0-9A-Za-z][A-Za-z0-9-_]*$`)
	if !validRe.MatchString(addonName) {
		list, _ := eksClient.ListAddons(ctx, &eks.ListAddonsInput{ClusterName: aws.String(clusterName)})
		lower := strings.ToLower(addonName)
		resolved := ""
		for _, n := range list.Addons {
			if strings.EqualFold(n, addonName) || strings.Contains(strings.ToLower(n), lower) {
				resolved = n
				break
			}
		}
		if resolved != "" {
			addonName = resolved
		} else {
			return fmt.Errorf("invalid add-on name '%s'. Available: %s", addonName, strings.Join(list.Addons, ", "))
		}
	}

	dryRun := c.Bool("dry-run")
	if dryRun {
		color.Cyan("DRY RUN: Would update add-on %s to version %s on cluster %s", addonName, targetVersion, clusterName)
		return nil
	}

	out, err := eksClient.UpdateAddon(ctx, &eks.UpdateAddonInput{
		ClusterName:  aws.String(clusterName),
		AddonName:    aws.String(addonName),
		AddonVersion: aws.String(targetVersion),
	})
	if err != nil {
		return awsinternal.FormatAWSError(err, "updating add-on")
	}

	color.Green("Update started for add-on %s (ID: %s)", addonName, aws.ToString(out.Update.Id))
	color.White("Use AWS Console or 'refresh addon describe %s --addon %s' to check status.", clusterName, addonName)
	return nil
}
