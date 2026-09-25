// Package dryrun provides dry-run functionality for previewing AMI updates.
// It implements clean separation of concerns and proper resource management.
package dryrun

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	awsClient "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/common"
	"github.com/dantech2000/refresh/internal/render"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
	refreshTypes "github.com/dantech2000/refresh/internal/types"
	"github.com/dantech2000/refresh/internal/ui"
)

// DryRunResult contains the results of a dry-run analysis.
type DryRunResult struct {
	mu             sync.RWMutex
	UpdatesNeeded  []NodegroupUpdate
	UpdatesSkipped []NodegroupUpdate
	AlreadyLatest  []NodegroupUpdate
	// CustomAMI holds custom-AMI nodegroups, which the real run skips (its
	// Skipped status with reason CustomAMI).
	CustomAMI []NodegroupUpdate
	// Unreadable holds nodegroups that could not be described (ActionUnknown).
	Unreadable []NodegroupUpdate
}

// NodegroupUpdate contains information about a nodegroup update action.
type NodegroupUpdate struct {
	Name       string
	Action     refreshTypes.DryRunAction
	CurrentAMI string
	LatestAMI  string
	Reason     string
	// Err is why the nodegroup could not be described (ActionUnknown).
	Err error
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
	reroll              bool
	quiet               bool
	latestAMICache      *awsClient.LatestAMICache
	describeNodegroupFn func(context.Context, string) (*types.Nodegroup, error)
	currentAmiFn        func(context.Context, *types.Nodegroup) string
	latestAmiFn         func(context.Context, *types.Nodegroup) string
}

var (
	dryrunDescribeCluster = func(ctx context.Context, eksClient *eks.Client, clusterName string) (string, error) {
		clusterOut, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeClusterOutput, error) {
			return eksClient.DescribeCluster(rc, &eks.DescribeClusterInput{
				Name: aws.String(clusterName),
			})
		})
		if err != nil {
			return "", awsClient.FormatAWSError(err, fmt.Sprintf("describing cluster %s", clusterName))
		}
		if clusterOut == nil || clusterOut.Cluster == nil {
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
		return nil, err
	}

	return &DryRunner{
		eksClient:   eksClient,
		ec2Client:   ec2.NewFromConfig(awsCfg),
		asgClient:   autoscaling.NewFromConfig(awsCfg),
		ssmClient:   ssm.NewFromConfig(awsCfg),
		clusterName: clusterName,
		k8sVersion:  k8sVersion,
		force:       force,
		quiet:       quiet,
	}, nil
}

// Options are the update flags that change the preview.
type Options struct {
	// Force previews `nodegroup update --force`: every nodegroup would be
	// force-updated (PodDisruptionBudgets are not honored).
	Force bool
	// Reroll previews `nodegroup update --reroll`: a nodegroup already on
	// the latest AMI is rolled instead of skipped.
	Reroll bool
	// Quiet suppresses the human preview.
	Quiet bool
}

// PerformDryRun shows what would be updated without making changes. It
// returns the nodegroups that could not be described (ActionUnknown), so the
// caller can report them as failures.
func PerformDryRun(ctx context.Context, awsCfg aws.Config, eksClient *eks.Client, clusterName string, selectedNodegroups []string, opts Options) ([]NodegroupUpdate, error) {
	runner, err := newDryRunner(ctx, awsCfg, eksClient, clusterName, opts.Force, opts.Quiet)
	if err != nil {
		return nil, err
	}
	runner.reroll = opts.Reroll

	result := runner.Analyze(ctx, selectedNodegroups)
	runner.DisplayResults(result)

	return result.Unreadable, nil
}

// Preview analyzes the selected nodegroups, in order, without printing
// anything. It backs the -o json/yaml dry-run document.
func Preview(ctx context.Context, awsCfg aws.Config, eksClient *eks.Client, clusterName string, selectedNodegroups []string, opts Options) ([]NodegroupUpdate, error) {
	dr, err := newDryRunner(ctx, awsCfg, eksClient, clusterName, opts.Force, true)
	if err != nil {
		return nil, err
	}
	dr.reroll = opts.Reroll
	out := make([]NodegroupUpdate, 0, len(selectedNodegroups))
	for _, ng := range selectedNodegroups {
		out = append(out, dr.analyzeNodegroup(ctx, ng))
	}
	return out, nil
}

// Action is the machine-readable name of a dry-run action, the `action` of
// a nodegroup in the NodegroupUpdatePlan document.
type Action string

// The dry-run actions.
const (
	// ActionUpdate: the update rolls the nodegroup to the latest AMI.
	ActionUpdate Action = "Update"
	// ActionForceUpdate: --force rolls the nodegroup.
	ActionForceUpdate Action = "ForceUpdate"
	// ActionSkipUpdating: the nodegroup is already updating.
	ActionSkipUpdating Action = "SkipUpdating"
	// ActionSkipLatest: the nodegroup already runs the latest AMI.
	ActionSkipLatest Action = "SkipLatest"
	// ActionSkipCustom: the nodegroup runs a custom AMI, which the update
	// never rolls.
	ActionSkipCustom Action = "SkipCustom"
	// ActionUnknown: the nodegroup could not be read.
	ActionUnknown Action = "Unknown"
)

// EnumValues lists every Action.
func (Action) EnumValues() []string {
	return []string{
		string(ActionUpdate), string(ActionForceUpdate), string(ActionSkipUpdating),
		string(ActionSkipLatest), string(ActionSkipCustom), string(ActionUnknown),
	}
}

// ActionName is the stable machine-readable name of a dry-run action.
func ActionName(a refreshTypes.DryRunAction) Action {
	switch a {
	case refreshTypes.ActionUpdate:
		return ActionUpdate
	case refreshTypes.ActionForceUpdate:
		return ActionForceUpdate
	case refreshTypes.ActionSkipUpdating:
		return ActionSkipUpdating
	case refreshTypes.ActionSkipLatest:
		return ActionSkipLatest
	case refreshTypes.ActionSkipCustom:
		return ActionSkipCustom
	default:
		return ActionUnknown
	}
}

// Analyze performs dry-run analysis on the selected nodegroups.
func (dr *DryRunner) Analyze(ctx context.Context, nodegroups []string) *DryRunResult {
	result := &DryRunResult{
		UpdatesNeeded:  make([]NodegroupUpdate, 0),
		UpdatesSkipped: make([]NodegroupUpdate, 0),
		AlreadyLatest:  make([]NodegroupUpdate, 0),
		CustomAMI:      make([]NodegroupUpdate, 0),
		Unreadable:     make([]NodegroupUpdate, 0),
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
	if err == nil && ng == nil {
		err = fmt.Errorf("empty DescribeNodegroup response")
	}
	if err != nil {
		update.Action = refreshTypes.ActionUnknown
		update.Reason = "could not describe the nodegroup"
		update.Err = err
		return update
	}

	// The real update (startNodegroupUpdates) decides with the same table,
	// so the preview names the action it will take.
	d := nodegroupsvc.DecideAMIUpdate(ctx, ng, nodegroupsvc.AMIUpdateOptions{Force: dr.force, Reroll: dr.reroll, Preview: true},
		func(ctx context.Context, ng *types.Nodegroup) (string, string) {
			return dr.currentAmi(ctx, ng), dr.latestAmi(ctx, ng)
		})
	update.Action, update.Reason = d.Action, d.Reason
	update.CurrentAMI, update.LatestAMI = d.CurrentAMI, d.LatestAMI
	return update
}

func (dr *DryRunner) describeNodegroup(ctx context.Context, ngName string) (*types.Nodegroup, error) {
	if dr.describeNodegroupFn != nil {
		return dr.describeNodegroupFn(ctx, ngName)
	}
	ngDesc, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeNodegroupOutput, error) {
		return dr.eksClient.DescribeNodegroup(rc, &eks.DescribeNodegroupInput{
			ClusterName:   aws.String(dr.clusterName),
			NodegroupName: aws.String(ngName),
		})
	})
	if err != nil {
		return nil, awsClient.FormatAWSError(err, fmt.Sprintf("describing nodegroup %s/%s", dr.clusterName, ngName))
	}
	if ngDesc == nil {
		return nil, nil
	}
	return ngDesc.Nodegroup, nil
}

func (dr *DryRunner) currentAmi(ctx context.Context, ng *types.Nodegroup) string {
	if dr.currentAmiFn != nil {
		return dr.currentAmiFn(ctx, ng)
	}
	return awsClient.CurrentAmiID(ctx, ng, dr.ec2Client, dr.asgClient)
}

// latestAmi resolves the latest recommended AMI for the nodegroup's AMI type
// at the nodegroup's own Kubernetes version (the real update keeps the
// nodegroup on its minor), memoized per (version, type). A failed lookup
// returns "", which the preview reports as "AMI status unknown, update
// recommended" rather than "already on latest".
func (dr *DryRunner) latestAmi(ctx context.Context, ng *types.Nodegroup) string {
	if dr.latestAmiFn != nil {
		return dr.latestAmiFn(ctx, ng)
	}
	if dr.latestAMICache == nil {
		dr.latestAMICache = awsClient.NewLatestAMIIDCache(dr.ssmClient)
	}
	latest, err := dr.latestAMICache.ForNodegroup(ctx, ng, dr.k8sVersion)
	if err != nil {
		return ""
	}
	return latest
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
	case refreshTypes.ActionSkipCustom:
		result.CustomAMI = append(result.CustomAMI, update)
	case refreshTypes.ActionUnknown:
		result.Unreadable = append(result.Unreadable, update)
	}

	// Print individual result if not quiet
	if !dr.quiet {
		dr.printUpdateStatus(update)
	}
}

// printUpdateStatus prints the status of a single update analysis.
func (dr *DryRunner) printUpdateStatus(update NodegroupUpdate) {
	ui.Outln(updateStatusLine(render.Default(os.Stdout), update))
}

// actionToken is the status token of a previewed action. The status says
// what the run would do: roll an outdated nodegroup (Warn), leave one that
// is already rolling (Progress) or current (Healthy), skip a custom AMI
// (Warn: it needs a launch template change), or nothing known (Fail).
func actionToken(th *render.Theme, a refreshTypes.DryRunAction) string {
	switch a {
	case refreshTypes.ActionUpdate:
		return th.Tokenf(render.Warn, "UPDATE")
	case refreshTypes.ActionForceUpdate:
		return th.Tokenf(render.Warn, "FORCE UPDATE")
	case refreshTypes.ActionSkipUpdating:
		return th.Tokenf(render.Progress, "SKIP")
	case refreshTypes.ActionSkipLatest:
		return th.Tokenf(render.Healthy, "SKIP")
	case refreshTypes.ActionSkipCustom:
		return th.Tokenf(render.Warn, "SKIP")
	default:
		return th.Tokenf(render.Fail, "UNKNOWN")
	}
}

// updateStatusLine is one nodegroup's line of the preview.
func updateStatusLine(th *render.Theme, u NodegroupUpdate) string {
	return fmt.Sprintf("%s: Nodegroup %s - %s", actionToken(th, u.Action), u.Name, u.Reason)
}

// DisplayResults shows the summary of the dry-run analysis.
func (dr *DryRunner) DisplayResults(result *DryRunResult) {
	if dr.quiet {
		return
	}

	th := render.Default(os.Stdout)
	ui.Outln("\n" + th.DryRun("Preview of nodegroup updates for cluster %s", dr.clusterName))
	if dr.force {
		ui.Outln(th.Line(render.Warn, "Force update would be enabled"))
	}
	if dr.reroll {
		ui.Outln(th.Line(render.Warn, "Re-roll would be enabled: nodegroups already on the latest AMI are rolled too"))
	}
	ui.Outln()

	ui.Outln(th.Section("SUMMARY"))
	ui.Outf("- Nodegroups that would be updated: %d\n", len(result.UpdatesNeeded))
	ui.Outf("- Nodegroups that would be skipped (already updating): %d\n", len(result.UpdatesSkipped))
	ui.Outf("- Nodegroups already on latest AMI: %d\n", len(result.AlreadyLatest))
	if len(result.CustomAMI) > 0 {
		ui.Outf("- Nodegroups that would be skipped (custom AMI): %d\n", len(result.CustomAMI))
	}
	if len(result.Unreadable) > 0 {
		ui.Outf("- Nodegroups that could not be read: %d\n", len(result.Unreadable))
	}

	// Detailed lists
	dr.printNodegroupList(th, render.Warn, "Would update:", result.UpdatesNeeded)
	dr.printNodegroupList(th, render.Progress, "Would skip (already updating):", result.UpdatesSkipped)
	dr.printNodegroupList(th, render.Healthy, "Already on latest AMI:", result.AlreadyLatest)
	dr.printNodegroupList(th, render.Warn, "Would skip (custom AMI, managed by the launch template):", result.CustomAMI)
	dr.printNodegroupList(th, render.Fail, "Could not read (see the warnings on stderr):", result.Unreadable)

	ui.Outln("\nTo execute these updates, run the same command without --dry-run")
}

// printNodegroupList prints a list of nodegroups under a header token.
func (dr *DryRunner) printNodegroupList(th *render.Theme, st render.Status, header string, updates []NodegroupUpdate) {
	if len(updates) == 0 {
		return
	}

	ui.Outf("\n%s\n", th.Line(st, "%s", header))
	for _, update := range updates {
		ui.Outf("  - %s\n", update.Name)
		if update.CurrentAMI != "" && update.LatestAMI != "" {
			ui.Outf("    Current: %s\n", update.CurrentAMI)
			ui.Outf("    Latest:  %s\n", update.LatestAMI)
		}
	}
}
