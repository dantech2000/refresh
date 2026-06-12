// Package dryrun provides dry-run functionality for previewing AMI updates.
// It implements clean separation of concerns and proper resource management.
package dryrun

import (
	"context"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/fatih/color"

	awsClient "github.com/dantech2000/refresh/internal/aws"
	refreshTypes "github.com/dantech2000/refresh/internal/types"
	"github.com/dantech2000/refresh/internal/ui"
)

// DryRunResult contains the results of a dry-run analysis.
type DryRunResult struct {
	mu             sync.RWMutex
	UpdatesNeeded  []NodegroupUpdate
	UpdatesSkipped []NodegroupUpdate
	AlreadyLatest  []NodegroupUpdate
}

// NodegroupUpdate contains information about a nodegroup update action.
type NodegroupUpdate struct {
	Name       string
	Action     refreshTypes.DryRunAction
	CurrentAMI string
	LatestAMI  string
	Reason     string
}

// DryRunner handles dry-run operations for AMI updates.
type DryRunner struct {
	eksClient           *eks.Client
	ec2Client           *ec2.Client
	asgClient           *autoscaling.Client
	ssmClient           *ssm.Client
	clusterName         string
	k8sVersion          string
	force               bool
	quiet               bool
	latestByType        map[types.AMITypes]string
	describeNodegroupFn func(context.Context, string) (*types.Nodegroup, error)
	currentAmiFn        func(context.Context, *types.Nodegroup) string
	latestAmiFn         func(context.Context, *types.Nodegroup) string
}

var (
	dryrunDescribeCluster = func(ctx context.Context, eksClient *eks.Client, clusterName string) (string, error) {
		clusterOut, err := eksClient.DescribeCluster(ctx, &eks.DescribeClusterInput{
			Name: aws.String(clusterName),
		})
		if err != nil {
			return "", err
		}
		if clusterOut.Cluster == nil {
			return "", fmt.Errorf("empty DescribeCluster response for %s", clusterName)
		}
		return aws.ToString(clusterOut.Cluster.Version), nil
	}
	newDryRunner = NewDryRunner
)

// NewDryRunner creates a new dry runner instance. All clients are built from
// awsCfg — the same config the caller used for its EKS client — so the preview
// queries the same account/region as the real run and honors the caller's
// context (flags, timeouts, cancellation).
func NewDryRunner(ctx context.Context, awsCfg aws.Config, eksClient *eks.Client, clusterName string, force, quiet bool) (*DryRunner, error) {
	if eksClient == nil {
		return nil, fmt.Errorf("eks client is required")
	}

	k8sVersion, err := dryrunDescribeCluster(ctx, eksClient, clusterName)
	if err != nil {
		return nil, fmt.Errorf("failed to describe cluster: %w", err)
	}

	return &DryRunner{
		eksClient:    eksClient,
		ec2Client:    ec2.NewFromConfig(awsCfg),
		asgClient:    autoscaling.NewFromConfig(awsCfg),
		ssmClient:    ssm.NewFromConfig(awsCfg),
		clusterName:  clusterName,
		k8sVersion:   k8sVersion,
		force:        force,
		quiet:        quiet,
		latestByType: make(map[types.AMITypes]string),
	}, nil
}

// PerformDryRun shows what would be updated without making changes.
func PerformDryRun(ctx context.Context, awsCfg aws.Config, eksClient *eks.Client, clusterName string, selectedNodegroups []string, force bool, quiet bool) error {
	runner, err := newDryRunner(ctx, awsCfg, eksClient, clusterName, force, quiet)
	if err != nil {
		return err
	}

	result := runner.Analyze(ctx, selectedNodegroups)
	runner.DisplayResults(result)

	return nil
}

// Analyze performs dry-run analysis on the selected nodegroups.
func (dr *DryRunner) Analyze(ctx context.Context, nodegroups []string) *DryRunResult {
	result := &DryRunResult{
		UpdatesNeeded:  make([]NodegroupUpdate, 0),
		UpdatesSkipped: make([]NodegroupUpdate, 0),
		AlreadyLatest:  make([]NodegroupUpdate, 0),
	}

	for _, ng := range nodegroups {
		update := dr.analyzeNodegroup(ctx, ng)
		dr.categorizeUpdate(result, update)
	}

	return result
}

// analyzeNodegroup analyzes a single nodegroup and determines what action would be taken.
func (dr *DryRunner) analyzeNodegroup(ctx context.Context, ngName string) NodegroupUpdate {
	update := NodegroupUpdate{
		Name: ngName,
	}

	ng, err := dr.describeNodegroup(ctx, ngName)
	if err != nil {
		update.Action = refreshTypes.ActionSkipUpdating
		update.Reason = fmt.Sprintf("failed to describe: %v", err)
		return update
	}

	// Check if already updating
	if ng.Status == types.NodegroupStatusUpdating {
		update.Action = refreshTypes.ActionSkipUpdating
		update.Reason = "already updating"
		return update
	}

	// Get AMI information
	update.CurrentAMI = dr.currentAmi(ctx, ng)
	update.LatestAMI = dr.latestAmi(ctx, ng)

	// Determine action
	if dr.force {
		update.Action = refreshTypes.ActionForceUpdate
		update.Reason = "force flag specified"
		return update
	}

	if update.CurrentAMI == "" || update.LatestAMI == "" {
		update.Action = refreshTypes.ActionUpdate
		update.Reason = "AMI status unknown, update recommended"
		return update
	}

	if update.CurrentAMI == update.LatestAMI {
		update.Action = refreshTypes.ActionSkipLatest
		update.Reason = "already on latest AMI"
		return update
	}

	update.Action = refreshTypes.ActionUpdate
	update.Reason = "AMI is outdated"
	return update
}

func (dr *DryRunner) describeNodegroup(ctx context.Context, ngName string) (*types.Nodegroup, error) {
	if dr.describeNodegroupFn != nil {
		return dr.describeNodegroupFn(ctx, ngName)
	}
	ngDesc, err := dr.eksClient.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{
		ClusterName:   aws.String(dr.clusterName),
		NodegroupName: aws.String(ngName),
	})
	if err != nil {
		return nil, err
	}
	return ngDesc.Nodegroup, nil
}

func (dr *DryRunner) currentAmi(ctx context.Context, ng *types.Nodegroup) string {
	if dr.currentAmiFn != nil {
		return dr.currentAmiFn(ctx, ng)
	}
	return awsClient.CurrentAmiID(ctx, ng, dr.ec2Client, dr.asgClient)
}

// latestAmi resolves the latest recommended AMI for the nodegroup's AMI type,
// memoized per type (the result is constant for a given cluster version).
func (dr *DryRunner) latestAmi(ctx context.Context, ng *types.Nodegroup) string {
	if dr.latestAmiFn != nil {
		return dr.latestAmiFn(ctx, ng)
	}
	if v, ok := dr.latestByType[ng.AmiType]; ok {
		return v
	}
	v := awsClient.LatestAmiIDForType(ctx, dr.ssmClient, dr.k8sVersion, ng.AmiType)
	if dr.latestByType == nil {
		dr.latestByType = make(map[types.AMITypes]string)
	}
	dr.latestByType[ng.AmiType] = v
	return v
}

// categorizeUpdate adds an update to the appropriate category in the result.
func (dr *DryRunner) categorizeUpdate(result *DryRunResult, update NodegroupUpdate) {
	result.mu.Lock()
	defer result.mu.Unlock()

	switch update.Action {
	case refreshTypes.ActionUpdate, refreshTypes.ActionForceUpdate:
		result.UpdatesNeeded = append(result.UpdatesNeeded, update)
	case refreshTypes.ActionSkipUpdating:
		result.UpdatesSkipped = append(result.UpdatesSkipped, update)
	case refreshTypes.ActionSkipLatest:
		result.AlreadyLatest = append(result.AlreadyLatest, update)
	}

	// Print individual result if not quiet
	if !dr.quiet {
		dr.printUpdateStatus(update)
	}
}

// printUpdateStatus prints the status of a single update analysis.
func (dr *DryRunner) printUpdateStatus(update NodegroupUpdate) {
	ui.Outf("%s: Nodegroup %s - %s\n", update.Action.ColorString(), update.Name, update.Reason)
}

// DisplayResults shows the summary of the dry-run analysis.
func (dr *DryRunner) DisplayResults(result *DryRunResult) {
	if dr.quiet {
		return
	}

	// Header
	color.Cyan("\nDRY RUN: Preview of nodegroup updates for cluster %s\n", dr.clusterName)
	if dr.force {
		color.Yellow("Force update would be enabled")
	}
	ui.Outln()

	// Summary
	color.Cyan("Summary:")
	ui.Outf("- Nodegroups that would be updated: %d\n", len(result.UpdatesNeeded))
	ui.Outf("- Nodegroups that would be skipped (already updating): %d\n", len(result.UpdatesSkipped))
	ui.Outf("- Nodegroups already on latest AMI: %d\n", len(result.AlreadyLatest))

	// Detailed lists
	dr.printNodegroupList("Would update:", result.UpdatesNeeded, color.GreenString)
	dr.printNodegroupList("Would skip (already updating):", result.UpdatesSkipped, color.YellowString)
	dr.printNodegroupList("Already on latest AMI:", result.AlreadyLatest, color.CyanString)

	ui.Outln("\nTo execute these updates, run the same command without --dry-run")
}

// printNodegroupList prints a list of nodegroups with the given header.
func (dr *DryRunner) printNodegroupList(header string, updates []NodegroupUpdate, colorFn func(format string, a ...any) string) {
	if len(updates) == 0 {
		return
	}

	ui.Outf("\n%s\n", colorFn(header))
	for _, update := range updates {
		ui.Outf("  - %s\n", update.Name)
		if update.CurrentAMI != "" && update.LatestAMI != "" {
			ui.Outf("    Current: %s\n", update.CurrentAMI)
			ui.Outf("    Latest:  %s\n", update.LatestAMI)
		}
	}
}
