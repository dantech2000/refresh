package addon

import (
	"context"
	"fmt"
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
	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/services/addons"
	"github.com/dantech2000/refresh/internal/ui"
)

func runList(c *cli.Context) error {
	ctx, cancel, cfg, err := runner.SetupAWS(c)
	if err != nil {
		return err
	}
	defer cancel()

	clusterName, listed, err := runner.ResolveClusterOrList(ctx, cfg, c)
	if err != nil || listed {
		return err
	}
	eksClient := eks.NewFromConfig(cfg)

	var rows []addonRow
	start := time.Now()
	if err := runner.WithSpinner("addon", "Add-on information gathered!", func() error {
		var ferr error
		rows, ferr = fetchAddons(ctx, eksClient, clusterName, c.Bool("show-health"))
		return ferr
	}); err != nil {
		return err
	}

	payload := map[string]any{"cluster": clusterName, "addons": rows, "count": len(rows)}
	if handled, err := runner.EncodeStdout(c.String("format"), payload); handled {
		return err
	}
	return outputAddonsTable(clusterName, rows, time.Since(start))
}

func runDescribe(c *cli.Context) error {
	ctx, cancel, cfg, err := runner.SetupAWS(c)
	if err != nil {
		return err
	}
	defer cancel()

	clusterName, listed, err := runner.ResolveClusterOrList(ctx, cfg, c)
	if err != nil || listed {
		return err
	}

	addonName := runner.SecondPositional(c, "addon")
	if addonName == "" {
		return fmt.Errorf("missing add-on name; pass as second argument or --addon <name>")
	}

	eksClient := eks.NewFromConfig(cfg)
	addonName, err = resolveAddonName(ctx, eksClient, clusterName, addonName)
	if err != nil {
		return err
	}

	d, err := eksClient.DescribeAddon(ctx, &eks.DescribeAddonInput{ClusterName: aws.String(clusterName), AddonName: aws.String(addonName)})
	if err != nil || d.Addon == nil {
		return awsinternal.FormatAWSError(err, "describing add-on")
	}

	details := addonDetails{
		Name:       aws.ToString(d.Addon.AddonName),
		Version:    aws.ToString(d.Addon.AddonVersion),
		Status:     string(d.Addon.Status),
		Health:     mapAddonHealth(d.Addon.Status),
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

	if handled, err := runner.EncodeStdout(c.String("format"), details); handled {
		return err
	}
	return outputAddonDetailsTable(clusterName, details)
}

// listAddonsAPI is the subset of the EKS client used by resolveAddonName,
// extracted so the resolver is testable.
type listAddonsAPI interface {
	ListAddons(ctx context.Context, in *eks.ListAddonsInput, optFns ...func(*eks.Options)) (*eks.ListAddonsOutput, error)
}

var validAddonRe = regexp.MustCompile(`^[0-9A-Za-z][A-Za-z0-9-_]*$`)

// resolveAddonName matches a user-supplied addon string against the cluster's
// installed addons, allowing case-insensitive substring matches. Returns a
// formatted error if ListAddons fails (e.g. AccessDeniedException) instead of
// dereferencing the nil response.
func resolveAddonName(ctx context.Context, eksClient listAddonsAPI, clusterName, addonName string) (string, error) {
	if validAddonRe.MatchString(addonName) {
		return addonName, nil
	}
	list, err := eksClient.ListAddons(ctx, &eks.ListAddonsInput{ClusterName: aws.String(clusterName)})
	if err != nil {
		return "", awsinternal.FormatAWSError(err, fmt.Sprintf("listing add-ons for cluster %s", clusterName))
	}
	lower := strings.ToLower(addonName)
	for _, n := range list.Addons {
		if strings.EqualFold(n, addonName) || strings.Contains(strings.ToLower(n), lower) {
			return n, nil
		}
	}
	return "", fmt.Errorf("invalid add-on name '%s'. Available: %s", addonName, strings.Join(list.Addons, ", "))
}

func runUpdate(c *cli.Context) error {
	ctx, cancel, cfg, err := runner.SetupAWS(c)
	if err != nil {
		return err
	}
	defer cancel()

	clusterName, listed, err := runner.ResolveClusterOrList(ctx, cfg, c)
	if err != nil || listed {
		return err
	}

	addonName := runner.SecondPositional(c, "addon")
	if addonName == "" {
		return fmt.Errorf("missing add-on name; pass as second argument or --addon <name>")
	}

	eksClient := eks.NewFromConfig(cfg)

	// version slot is third positional after (cluster, addon). PositionalSlot
	// shifts the expected index down by 1 for each prior flag that was set, so
	// `--addon=foo my-cluster v1.2.3` correctly picks up v1.2.3.
	version := runner.PositionalSlot(c, "version", "cluster", "addon")
	if version == "" {
		version = "latest"
	}

	targetVersion := version
	if strings.EqualFold(version, "latest") {
		avail, err := eksClient.DescribeAddonVersions(ctx, &eks.DescribeAddonVersionsInput{AddonName: aws.String(addonName)})
		if err != nil || len(avail.Addons) == 0 || len(avail.Addons[0].AddonVersions) == 0 {
			return awsinternal.FormatAWSError(err, "resolving latest add-on version")
		}
		targetVersion = aws.ToString(avail.Addons[0].AddonVersions[0].AddonVersion)
	}

	addonName, err = resolveAddonName(ctx, eksClient, clusterName, addonName)
	if err != nil {
		return err
	}

	if c.Bool("dry-run") {
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
	ctx, cancel, cfg, err := runner.SetupAWSStrict(c)
	if err != nil {
		return err
	}
	defer cancel()

	requested := runner.RequestedCluster(c)
	if strings.TrimSpace(requested) == "" {
		return fmt.Errorf("cluster name is required")
	}

	clusterName, err := awsinternal.ClusterName(ctx, cfg, requested)
	if err != nil {
		return err
	}

	if c.Bool("parallel") && c.Bool("dependency-order") {
		return fmt.Errorf("--parallel and --dependency-order cannot be used together: parallel execution defeats dependency ordering")
	}

	eksClient := eks.NewFromConfig(cfg)
	logger := factory.NewDefaultLogger(nil)
	addonSvc := addons.NewService(eksClient, logger)

	options := addons.UpdateAllOptions{
		DryRun:          c.Bool("dry-run"),
		Parallel:        c.Bool("parallel"),
		Wait:            c.Bool("wait"),
		WaitTimeout:     c.Duration("wait-timeout"),
		SkipAddons:      c.StringSlice("skip"),
		DependencyOrder: c.Bool("dependency-order"),
		HealthCheck:     c.Bool("health-check"),
	}

	var results []addons.AddonUpdateResult
	if err := runner.WithSpinner("addon", "Addon updates processed!", func() error {
		var rerr error
		results, rerr = addonSvc.UpdateAll(ctx, clusterName, options)
		return rerr
	}); err != nil {
		return err
	}

	payload := map[string]any{
		"cluster": clusterName,
		"dryRun":  options.DryRun,
		"results": results,
	}
	if handled, err := runner.EncodeStdout(c.String("format"), payload); handled {
		return err
	}
	return outputUpdateAllResults(clusterName, results, options.DryRun)
}

func fetchAddons(ctx context.Context, eksClient *eks.Client, clusterName string, withHealth bool) ([]addonRow, error) {
	addonNames, err := awsinternal.ListAllPages(ctx, "listing add-ons",
		func(rc context.Context, token *string) (*eks.ListAddonsOutput, error) {
			return eksClient.ListAddons(rc, &eks.ListAddonsInput{ClusterName: aws.String(clusterName), NextToken: token})
		},
		func(out *eks.ListAddonsOutput) ([]string, *string) { return out.Addons, out.NextToken },
	)
	if err != nil {
		return nil, err
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
		return ui.BadgePass()
	case ekstypes.AddonStatusDegraded:
		return ui.BadgeFail()
	case ekstypes.AddonStatusCreateFailed, ekstypes.AddonStatusDeleteFailed:
		return ui.BadgeFail()
	case ekstypes.AddonStatusCreating, ekstypes.AddonStatusDeleting, ekstypes.AddonStatusUpdating:
		return ui.BadgeInProgress()
	default:
		return ui.BadgeUnknown()
	}
}
