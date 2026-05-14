package addon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/fatih/color"
	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v3"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/awsconfig"
	clustercmd "github.com/dantech2000/refresh/internal/commands/cluster"
	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/services/addons"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/ui"
)

func runList(c *cli.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Duration("timeout"))
	defer cancel()

	cfg, err := awsconfig.Load(ctx, c)
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

	requested := c.Args().First()
	if requested == "" {
		requested = c.String("cluster")
	}
	if strings.TrimSpace(requested) == "" {
		ui.Outln("No cluster specified. Available clusters:")
		ui.Outln()
		start := time.Now()
		svc := factory.NewClusterService(cfg, false, nil)
		summaries, err := svc.List(ctx, clustersvc.ListOptions{})
		if err != nil {
			return err
		}
		_ = clustercmd.OutputClustersTable(summaries, time.Since(start), false, false)
		return nil
	}

	clusterName, err := awsinternal.ClusterName(ctx, cfg, requested)
	if err != nil {
		return err
	}
	eksClient := eks.NewFromConfig(cfg)

	spinner := ui.NewFunSpinnerForCategory("addon")
	if err := spinner.Start(); err != nil {
		return err
	}
	defer spinner.Stop()

	start := time.Now()
	rows, err := fetchAddons(ctx, eksClient, clusterName, c.Bool("show-health"))
	spinner.Success("Add-on information gathered!")
	if err != nil {
		return err
	}

	switch strings.ToLower(c.String("format")) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"cluster": clusterName, "addons": rows, "count": len(rows)})
	case "yaml":
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(map[string]any{"cluster": clusterName, "addons": rows, "count": len(rows)})
	default:
		return outputAddonsTable(clusterName, rows, time.Since(start))
	}
}

func runDescribe(c *cli.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Duration("timeout"))
	defer cancel()

	cfg, err := awsconfig.Load(ctx, c)
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

	requested := c.Args().First()
	if requested == "" {
		requested = c.String("cluster")
	}
	if strings.TrimSpace(requested) == "" {
		fmt.Println("No cluster specified. Available clusters:")
		fmt.Println()
		svc := factory.NewClusterService(cfg, false, nil)
		start := time.Now()
		summaries, err := svc.List(ctx, clustersvc.ListOptions{})
		if err != nil {
			return err
		}
		_ = clustercmd.OutputClustersTable(summaries, time.Since(start), false, false)
		return nil
	}

	clusterName, err := awsinternal.ClusterName(ctx, cfg, requested)
	if err != nil {
		return err
	}

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

	d, err := eksClient.DescribeAddon(ctx, &eks.DescribeAddonInput{ClusterName: aws.String(clusterName), AddonName: aws.String(addonName)})
	if err != nil || d.Addon == nil {
		return awsinternal.FormatAWSError(err, "describing add-on")
	}

	health := mapAddonHealth(d.Addon.Status)
	details := addonDetails{
		Name:       aws.ToString(d.Addon.AddonName),
		Version:    aws.ToString(d.Addon.AddonVersion),
		Status:     string(d.Addon.Status),
		Health:     health,
		ARN:        aws.ToString(d.Addon.AddonArn),
		CreatedAt:  d.Addon.CreatedAt,
		ModifiedAt: d.Addon.ModifiedAt,
		Config:     map[string]any{},
	}

	if d.Addon.ConfigurationValues != nil && *d.Addon.ConfigurationValues != "" {
		var cfgMap map[string]any
		raw := *d.Addon.ConfigurationValues
		if err := yaml.Unmarshal([]byte(raw), &cfgMap); err == nil {
			details.Config = cfgMap
		} else {
			details.Config = map[string]any{"raw": raw}
		}
	}

	switch strings.ToLower(c.String("format")) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(details)
	case "yaml":
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(details)
	default:
		return outputAddonDetailsTable(clusterName, details)
	}
}

func runUpdate(c *cli.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Duration("timeout"))
	defer cancel()

	cfg, err := awsconfig.Load(ctx, c)
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

	requested := c.Args().First()
	if requested == "" {
		requested = c.String("cluster")
	}
	if strings.TrimSpace(requested) == "" {
		color.Yellow("No cluster specified. Available clusters:")
		svc := factory.NewClusterService(cfg, false, nil)
		summaries, err := svc.List(ctx, clustersvc.ListOptions{})
		if err != nil {
			return err
		}
		_ = clustercmd.OutputClustersTable(summaries, 0, false, false)
		return nil
	}

	clusterName, err := awsinternal.ClusterName(ctx, cfg, requested)
	if err != nil {
		return err
	}

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
		targetVersion = aws.ToString(avail.Addons[0].AddonVersions[0].AddonVersion)
	}

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

func runUpdateAll(c *cli.Context) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.Duration("timeout"))
	defer cancel()

	cfg, err := awsconfig.Load(ctx, c)
	if err != nil {
		color.Red("Failed to load AWS config: %v", err)
		return err
	}
	if err := awsinternal.ValidateAWSCredentials(ctx, cfg); err != nil {
		color.Red("%v", err)
		ui.Outln()
		awsinternal.PrintCredentialHelp()
		return fmt.Errorf("AWS credential validation failed")
	}

	requested := c.Args().First()
	if requested == "" {
		requested = c.String("cluster")
	}
	if strings.TrimSpace(requested) == "" {
		return fmt.Errorf("cluster name is required")
	}

	clusterName, err := awsinternal.ClusterName(ctx, cfg, requested)
	if err != nil {
		return err
	}

	eksClient := eks.NewFromConfig(cfg)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	addonSvc := addons.NewService(eksClient, logger)

	spinner := ui.NewFunSpinnerForCategory("addon")
	if err := spinner.Start(); err != nil {
		return err
	}
	defer spinner.Stop()

	if c.Bool("parallel") && c.Bool("dependency-order") {
		return fmt.Errorf("--parallel and --dependency-order cannot be used together: parallel execution defeats dependency ordering")
	}

	options := addons.UpdateAllOptions{
		DryRun:          c.Bool("dry-run"),
		Parallel:        c.Bool("parallel"),
		Wait:            c.Bool("wait"),
		WaitTimeout:     c.Duration("wait-timeout"),
		SkipAddons:      c.StringSlice("skip"),
		DependencyOrder: c.Bool("dependency-order"),
		HealthCheck:     c.Bool("health-check"),
	}

	results, err := addonSvc.UpdateAll(ctx, clusterName, options)
	spinner.Success("Addon updates processed!")
	if err != nil {
		return err
	}

	switch strings.ToLower(c.String("format")) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{
			"cluster": clusterName,
			"dryRun":  options.DryRun,
			"results": results,
		})
	case "yaml":
		enc := yaml.NewEncoder(os.Stdout)
		enc.SetIndent(2)
		defer func() { _ = enc.Close() }()
		return enc.Encode(map[string]any{
			"cluster": clusterName,
			"dryRun":  options.DryRun,
			"results": results,
		})
	default:
		return outputUpdateAllResults(clusterName, results, options.DryRun)
	}
}

func fetchAddons(ctx context.Context, eksClient *eks.Client, clusterName string, withHealth bool) ([]addonRow, error) {
	var addonNames []string
	var nextToken *string
	for {
		out, err := eksClient.ListAddons(ctx, &eks.ListAddonsInput{ClusterName: aws.String(clusterName), NextToken: nextToken})
		if err != nil {
			return nil, awsinternal.FormatAWSError(err, "listing add-ons")
		}
		addonNames = append(addonNames, out.Addons...)
		if out.NextToken == nil || (out.NextToken != nil && aws.ToString(out.NextToken) == "") {
			break
		}
		nextToken = out.NextToken
	}
	rows := make([]addonRow, 0, len(addonNames))
	for _, name := range addonNames {
		d, err := eksClient.DescribeAddon(ctx, &eks.DescribeAddonInput{ClusterName: aws.String(clusterName), AddonName: aws.String(name)})
		if err != nil || d.Addon == nil {
			rows = append(rows, addonRow{Name: name, Version: "", Status: "UNKNOWN", Health: "Unknown"})
			continue
		}
		health := ""
		if withHealth {
			health = mapAddonHealth(d.Addon.Status)
		}
		rows = append(rows, addonRow{Name: aws.ToString(d.Addon.AddonName), Version: aws.ToString(d.Addon.AddonVersion), Status: string(d.Addon.Status), Health: health})
	}
	return rows, nil
}

func mapAddonHealth(s ekstypes.AddonStatus) string {
	switch s {
	case ekstypes.AddonStatusActive:
		return color.GreenString("PASS")
	case ekstypes.AddonStatusDegraded:
		return color.RedString("FAIL")
	case ekstypes.AddonStatusCreateFailed, ekstypes.AddonStatusDeleteFailed:
		return color.RedString("FAIL")
	case ekstypes.AddonStatusCreating, ekstypes.AddonStatusDeleting, ekstypes.AddonStatusUpdating:
		return color.CyanString("[IN PROGRESS]")
	default:
		return color.WhiteString("UNKNOWN")
	}
}
