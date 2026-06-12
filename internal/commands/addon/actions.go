package addon

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/fatih/color"
	"github.com/urfave/cli/v3"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/services/addons"
	"github.com/dantech2000/refresh/internal/ui"
)

func runList(ctx context.Context, cmd *cli.Command) error {
	if err := runner.ValidateFormat(cmd.String("format"), runner.FormatsStandard); err != nil {
		return err
	}
	ctx, cancel, cfg, err := runner.SetupAWS(ctx, cmd)
	if err != nil {
		return err
	}
	defer cancel()

	clusterName, listed, err := runner.ResolveClusterOrList(ctx, cfg, cmd)
	if err != nil || listed {
		return err
	}

	addonSvc := factory.NewAddonService(cfg, nil)

	var summaries []addons.AddonSummary
	start := time.Now()
	if err := runner.WithSpinner("addon", "Add-on information gathered!", func() error {
		var ferr error
		summaries, ferr = addonSvc.List(ctx, clusterName, addons.ListOptions{ShowHealth: cmd.Bool("show-health")})
		return ferr
	}); err != nil {
		return err
	}

	payload := map[string]any{"cluster": clusterName, "addons": summaries, "count": len(summaries)}
	if handled, err := runner.EncodeStdout(cmd.String("format"), payload); handled {
		return err
	}
	return outputAddonsTable(clusterName, summaries, time.Since(start))
}

func runDescribe(ctx context.Context, cmd *cli.Command) error {
	if err := runner.ValidateFormat(cmd.String("format"), runner.FormatsStandard); err != nil {
		return err
	}
	ctx, cancel, cfg, err := runner.SetupAWS(ctx, cmd)
	if err != nil {
		return err
	}
	defer cancel()

	clusterName, listed, err := runner.ResolveClusterOrList(ctx, cfg, cmd)
	if err != nil || listed {
		return err
	}

	addonName := runner.PositionalSlot(cmd, "addon", "cluster")
	if addonName == "" {
		return fmt.Errorf("missing add-on name; pass as second argument or --addon <name>")
	}

	addonSvc := factory.NewAddonService(cfg, nil)
	addonName, err = resolveAddonName(ctx, addonSvc.EKS(), clusterName, addonName)
	if err != nil {
		return err
	}

	details, err := addonSvc.Describe(ctx, clusterName, addonName, addons.DescribeOptions{ShowConfiguration: true})
	if err != nil {
		return awsinternal.FormatAWSError(err, "describing add-on")
	}

	if handled, err := runner.EncodeStdout(cmd.String("format"), details); handled {
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

// warnAllOnlyFlags warns when flags that only apply to `addon update --all`
// are passed on the single-addon path, where they're silently inert. (REF-55)
func warnAllOnlyFlags(cmd *cli.Command) {
	var set []string
	if cmd.Bool("parallel") {
		set = append(set, "--parallel")
	}
	if len(cmd.StringSlice("skip")) > 0 {
		set = append(set, "--skip")
	}
	if cmd.Bool("dependency-order") {
		set = append(set, "--dependency-order")
	}
	if len(set) > 0 {
		fmt.Fprintf(os.Stderr, "warning: %s only apply with --all and are ignored for a single add-on update\n",
			strings.Join(set, ", "))
	}
}

func runUpdate(ctx context.Context, cmd *cli.Command) error {
	if err := runner.ValidateFormat(cmd.String("format"), runner.FormatsStandard); err != nil {
		return err
	}
	warnAllOnlyFlags(cmd)
	ctx, cancel, cfg, err := runner.SetupAWSStrict(ctx, cmd)
	if err != nil {
		return err
	}
	defer cancel()

	clusterName, listed, err := runner.ResolveClusterOrList(ctx, cfg, cmd)
	if err != nil || listed {
		return err
	}

	addonName := runner.PositionalSlot(cmd, "addon", "cluster")
	if addonName == "" {
		return fmt.Errorf("missing add-on name; pass as second argument or --addon <name>")
	}

	// Route through the addons service so single-addon updates get the same
	// version resolution, compatibility validation, optional health checks,
	// and optional wait behavior as `update --all`.
	addonSvc := factory.NewAddonService(cfg, nil)
	addonName, err = resolveAddonName(ctx, addonSvc.EKS(), clusterName, addonName)
	if err != nil {
		return err
	}

	// version slot is third positional after (cluster, addon). PositionalSlot
	// shifts the expected index down by 1 for each prior flag that was set, so
	// `--addon=foo my-cluster v1.2.3` correctly picks up v1.2.3.
	version := runner.PositionalSlot(cmd, "version", "cluster", "addon")
	if version == "" {
		version = "latest"
	}

	result, err := addonSvc.Update(ctx, clusterName, addonName, addons.UpdateOptions{
		Version:     version,
		DryRun:      cmd.Bool("dry-run"),
		HealthCheck: cmd.Bool("health-check"),
		Wait:        cmd.Bool("wait"),
		WaitTimeout: cmd.Duration("wait-timeout"),
	})
	if err != nil {
		return err
	}

	// Honor -o json|yaml for the single-addon result (REF-55); table/plain fall
	// through to the human-readable summary below.
	if handled, encErr := runner.EncodeStdout(cmd.String("format"), result); handled {
		return encErr
	}

	switch result.Status {
	case "DRY_RUN":
		color.Cyan("DRY RUN: Would update add-on %s from %s to %s on cluster %s",
			addonName, result.PreviousVersion, result.NewVersion, clusterName)
	case "COMPLETED":
		color.Green("Add-on %s updated to %s (was %s)", addonName, result.NewVersion, result.PreviousVersion)
	case "COMPLETED_WITH_ISSUES":
		color.Yellow("Add-on %s updated to %s, but the post-update health check found issues: %s",
			addonName, result.NewVersion, result.HealthIssues)
	default:
		color.Green("Update started for add-on %s (ID: %s)", addonName, result.UpdateID)
		color.White("Use AWS Console or 'refresh addon describe %s --addon %s' to check status.", clusterName, addonName)
	}
	return nil
}

func runUpdateAll(ctx context.Context, cmd *cli.Command) error {
	if err := runner.ValidateFormat(cmd.String("format"), runner.FormatsStandard); err != nil {
		return err
	}
	ctx, cancel, cfg, err := runner.SetupAWSStrict(ctx, cmd)
	if err != nil {
		return err
	}
	defer cancel()

	requested := runner.RequestedCluster(cmd)
	if strings.TrimSpace(requested) == "" {
		return fmt.Errorf("cluster name is required")
	}

	clusterName, err := awsinternal.ClusterName(ctx, cfg, requested)
	if err != nil {
		return err
	}

	if cmd.Bool("parallel") && cmd.Bool("dependency-order") {
		return fmt.Errorf("--parallel and --dependency-order cannot be used together: parallel execution defeats dependency ordering")
	}

	addonSvc := factory.NewAddonService(cfg, nil)

	options := addons.UpdateAllOptions{
		DryRun:          cmd.Bool("dry-run"),
		Parallel:        cmd.Bool("parallel"),
		Wait:            cmd.Bool("wait"),
		WaitTimeout:     cmd.Duration("wait-timeout"),
		SkipAddons:      cmd.StringSlice("skip"),
		DependencyOrder: cmd.Bool("dependency-order"),
		HealthCheck:     cmd.Bool("health-check"),
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
	if handled, err := runner.EncodeStdout(cmd.String("format"), payload); handled {
		if err != nil {
			return err
		}
		return updateAllFailureError(results)
	}
	if err := outputUpdateAllResults(clusterName, results, options.DryRun); err != nil {
		return err
	}
	return updateAllFailureError(results)
}

// updateAllFailureError returns a non-nil error when any addon update failed,
// so `addon update --all` exits non-zero and scripts can detect failure.
func updateAllFailureError(results []addons.AddonUpdateResult) error {
	failed := 0
	for _, r := range results {
		if strings.HasPrefix(r.Status, "FAILED") || r.Status == "WAIT_FAILED" {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d addon update(s) failed", failed, len(results))
	}
	return nil
}

// healthBadge converts the addons service's plain health vocabulary
// (PASS/FAIL/IN_PROGRESS/UNKNOWN) into the shared colored badges.
func healthBadge(health string) string {
	switch health {
	case "":
		return ""
	case "PASS":
		return ui.BadgePass()
	case "FAIL":
		return ui.BadgeFail()
	case "IN_PROGRESS":
		return ui.BadgeInProgress()
	default:
		return ui.BadgeUnknown()
	}
}
