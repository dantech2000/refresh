// Package dryrun provides dry-run functionality for previewing AMI updates.
// It implements clean separation of concerns and proper resource management.
package dryrun

import (
	"context"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/fatih/color"

	awsClient "github.com/dantech2000/refresh/internal/aws"
	refreshTypes "github.com/dantech2000/refresh/internal/types"
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
	eksClient     *eks.Client
	ec2Client     *ec2.Client
	asgClient     *autoscaling.Client
	ssmClient     *ssm.Client
	clusterName   string
	k8sVersion    string
	force         bool
	quiet         bool
}

// NewDryRunner creates a new dry runner instance.
func NewDryRunner(eksClient *eks.Client, clusterName string, force, quiet bool) (*DryRunner, error) {
	ctx := context.Background()

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %v", err)
	}

	clusterOut, err := eksClient.DescribeCluster(ctx, &eks.DescribeClusterInput{
		Name: aws.String(clusterName),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to describe cluster: %v", err)
	}

	return &DryRunner{
		eksClient:   eksClient,
		ec2Client:   ec2.NewFromConfig(awsCfg),
		asgClient:   autoscaling.NewFromConfig(awsCfg),
		ssmClient:   ssm.NewFromConfig(awsCfg),
		clusterName: clusterName,
		k8sVersion:  aws.ToString(clusterOut.Cluster.Version),
		force:       force,
		quiet:       quiet,
	}, nil
}

// PerformDryRun shows what would be updated without making changes.
func PerformDryRun(ctx context.Context, eksClient *eks.Client, clusterName string, selectedNodegroups []string, force bool, quiet bool) error {
	runner, err := NewDryRunner(eksClient, clusterName, force, quiet)
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

	ngDesc, err := dr.eksClient.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{
		ClusterName:   aws.String(dr.clusterName),
		NodegroupName: aws.String(ngName),
	})
	if err != nil {
		update.Action = refreshTypes.ActionSkipUpdating
		update.Reason = fmt.Sprintf("failed to describe: %v", err)
		return update
	}

	ng := ngDesc.Nodegroup

	// Check if already updating
	if ng.Status == types.NodegroupStatusUpdating {
		update.Action = refreshTypes.ActionSkipUpdating
		update.Reason = "already updating"
		return update
	}

	// Get AMI information
	update.CurrentAMI = awsClient.CurrentAmiID(ctx, ng, dr.ec2Client, dr.asgClient)
	update.LatestAMI = awsClient.LatestAmiID(ctx, dr.ssmClient, dr.k8sVersion)

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
	fmt.Printf("%s: Nodegroup %s - %s\n", update.Action, update.Name, update.Reason)
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
	fmt.Println()

	// Summary
	color.Cyan("Summary:")
	fmt.Printf("- Nodegroups that would be updated: %d\n", len(result.UpdatesNeeded))
	fmt.Printf("- Nodegroups that would be skipped (already updating): %d\n", len(result.UpdatesSkipped))
	fmt.Printf("- Nodegroups already on latest AMI: %d\n", len(result.AlreadyLatest))

	// Detailed lists
	dr.printNodegroupList("Would update:", result.UpdatesNeeded, color.GreenString)
	dr.printNodegroupList("Would skip (already updating):", result.UpdatesSkipped, color.YellowString)
	dr.printNodegroupList("Already on latest AMI:", result.AlreadyLatest, color.CyanString)

	fmt.Println("\nTo execute these updates, run the same command without --dry-run")
}

// printNodegroupList prints a list of nodegroups with the given header.
func (dr *DryRunner) printNodegroupList(header string, updates []NodegroupUpdate, colorFn func(format string, a ...interface{}) string) {
	if len(updates) == 0 {
		return
	}

	fmt.Printf("\n%s\n", colorFn(header))
	for _, update := range updates {
		fmt.Printf("  - %s\n", update.Name)
		if update.CurrentAMI != "" && update.LatestAMI != "" {
			fmt.Printf("    Current: %s\n", update.CurrentAMI)
			fmt.Printf("    Latest:  %s\n", update.LatestAMI)
		}
	}
}
