package upgrade

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/services/addons"
	"github.com/dantech2000/refresh/internal/services/common"
)

// PlanOptions tunes plan generation.
type PlanOptions struct {
	// SkipAddons are addon names the user manages out-of-band (Helm/GitOps);
	// they appear in the plan as manual steps and are never mutated.
	SkipAddons []string
	// SkipNodegroups are substring patterns for nodegroups to leave alone.
	SkipNodegroups []string
}

// BuildPlan derives the full ordered upgrade plan for clusterName to reach
// targetVersion. The plan is also the resume mechanism: steps whose desired
// state is already satisfied by the live cluster are marked completed, so a
// rerun after a partial upgrade (or a second run after success) executes only
// what remains.
func (s *Service) BuildPlan(ctx context.Context, clusterName, targetVersion string, opts PlanOptions) (*Plan, error) {
	cluster, err := s.describeCluster(ctx, clusterName)
	if err != nil {
		return nil, err
	}
	currentVersion := aws.ToString(cluster.Version)

	hops, err := expandHops(currentVersion, targetVersion)
	if err != nil {
		return nil, err
	}
	if len(hops) == 0 {
		// Control plane already at the target. Still plan a same-version hop
		// so addons/nodegroups that lag behind it (e.g. after an interrupted
		// run) get caught up; a fully-current cluster derives every step as
		// completed and the run is a no-op.
		hops = []string{targetVersion}
	}

	plan := &Plan{
		ClusterName:    clusterName,
		CurrentVersion: currentVersion,
		TargetVersion:  targetVersion,
	}

	if err := s.checkVersionOffered(ctx, targetVersion, plan); err != nil {
		return nil, err
	}

	nodegroups, err := s.listNodegroupStates(ctx, clusterName)
	if err != nil {
		return nil, err
	}

	addonsSvc := s.addonsService()
	addonList, err := addonsSvc.List(ctx, clusterName, addons.ListOptions{})
	if err != nil {
		return nil, err
	}
	addonList = addons.SortByDependency(addonList)

	// Simulated state advances hop by hop so later hops plan against where
	// the cluster WILL be, while completed-step detection uses live state.
	simNodegroups := make(map[string]string, len(nodegroups))
	for _, ng := range nodegroups {
		simNodegroups[ng.Name] = ng.Version
	}

	for _, hopTo := range hops {
		hop := Hop{From: prevVersion(plan, hopTo), To: hopTo}

		hop.Steps = append(hop.Steps, s.readinessStep(ctx, clusterName, hopTo, nodegroups, simNodegroups, plan))
		hop.Steps = append(hop.Steps, controlPlaneStep(currentVersion, aws.ToString(cluster.Version), hopTo, cluster.Status))
		hop.Steps = append(hop.Steps, s.addonSteps(ctx, addonsSvc, addonList, hopTo, opts.SkipAddons)...)
		hop.Steps = append(hop.Steps, nodegroupSteps(nodegroups, hopTo, opts.SkipNodegroups)...)

		plan.Hops = append(plan.Hops, hop)

		// Advance the simulation: after this hop, rollable nodegroups sit at
		// the hop target.
		for _, ng := range nodegroups {
			if !ng.CustomAMI && !matchesAny(ng.Name, opts.SkipNodegroups) && !versionAtLeast(simNodegroups[ng.Name], hopTo) {
				simNodegroups[ng.Name] = hopTo
			}
		}
	}

	return plan, nil
}

// prevVersion returns the From version for the next hop: the previous hop's
// target, or the plan's current version for the first hop.
func prevVersion(plan *Plan, _ string) string {
	if len(plan.Hops) == 0 {
		return plan.CurrentVersion
	}
	return plan.Hops[len(plan.Hops)-1].To
}

// checkVersionOffered verifies EKS offers the target version. An API error
// degrades to a plan warning (older SDK endpoints/permissions); an explicit
// "not offered" answer is a hard error.
func (s *Service) checkVersionOffered(ctx context.Context, targetVersion string, plan *Plan) error {
	out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeClusterVersionsOutput, error) {
		return s.eksClient.DescribeClusterVersions(rc, &eks.DescribeClusterVersionsInput{
			ClusterVersions: []string{targetVersion},
		})
	})
	if err != nil {
		plan.Warnings = append(plan.Warnings,
			fmt.Sprintf("could not verify that EKS offers %s (continuing): %v", targetVersion, err))
		return nil
	}
	for _, v := range out.ClusterVersions {
		if aws.ToString(v.ClusterVersion) == targetVersion {
			return nil
		}
	}
	return fmt.Errorf("version %s is not offered by EKS", targetVersion)
}

// readinessStep builds the per-hop readiness gate: kubelet version skew plus
// EKS Cluster Insights (UPGRADE_READINESS) for the hop target.
func (s *Service) readinessStep(ctx context.Context, clusterName, hopTo string, nodegroups []nodegroupState, simNodegroups map[string]string, plan *Plan) Step {
	step := Step{
		Type:        StepReadiness,
		Description: fmt.Sprintf("readiness for %s (insights + version skew)", hopTo),
		Version:     hopTo,
		Status:      StatusPending,
	}

	// Version skew: every nodegroup must stay within the supported kubelet
	// skew of the hop target once the control plane moves.
	hopMinor, err := minorVersion(hopTo)
	if err != nil {
		step.Status = StatusBlocked
		step.Reason = err.Error()
		return step
	}
	var skewViolations []string
	for _, ng := range nodegroups {
		simVersion := simNodegroups[ng.Name]
		ngMinor, err := minorVersion(simVersion)
		if err != nil {
			continue
		}
		if hopMinor-ngMinor > kubeletSkew {
			skewViolations = append(skewViolations,
				fmt.Sprintf("nodegroup %s at %s would exceed the kubelet skew limit (%d minors) against %s", ng.Name, simVersion, kubeletSkew, hopTo))
		}
	}
	if len(skewViolations) > 0 {
		step.Status = StatusBlocked
		step.Reason = strings.Join(skewViolations, "; ")
		return step
	}

	// Cluster Insights: blocking on ERROR, warn on WARNING; unavailable
	// insights degrade to a plan warning rather than blocking the upgrade.
	insights, err := s.listUpgradeInsights(ctx, clusterName, hopTo)
	if err != nil {
		plan.Warnings = append(plan.Warnings,
			fmt.Sprintf("cluster insights unavailable for %s (continuing): %v", hopTo, err))
		step.Reason = "insights unavailable; skew OK"
		return step
	}

	var errorsFound, warningsFound []string
	for _, in := range insights {
		if in.InsightStatus == nil {
			continue
		}
		name := aws.ToString(in.Name)
		if name == "" {
			name = aws.ToString(in.Id)
		}
		switch in.InsightStatus.Status {
		case ekstypes.InsightStatusValueError:
			errorsFound = append(errorsFound, name)
		case ekstypes.InsightStatusValueWarning:
			warningsFound = append(warningsFound, name)
		}
	}
	if len(errorsFound) > 0 {
		step.Status = StatusBlocked
		step.Reason = fmt.Sprintf("%d blocking insight(s): %s", len(errorsFound), strings.Join(errorsFound, ", "))
		return step
	}
	if len(warningsFound) > 0 {
		step.Reason = fmt.Sprintf("%d insight warning(s): %s", len(warningsFound), strings.Join(warningsFound, ", "))
		plan.Warnings = append(plan.Warnings,
			fmt.Sprintf("insight warnings for %s: %s", hopTo, strings.Join(warningsFound, ", ")))
	} else {
		step.Reason = "0 blocking insights; skew OK"
	}
	return step
}

// listUpgradeInsights fetches UPGRADE_READINESS insights for the given
// Kubernetes version.
func (s *Service) listUpgradeInsights(ctx context.Context, clusterName, k8sVersion string) ([]ekstypes.InsightSummary, error) {
	return awsinternal.ListAllPages(ctx, fmt.Sprintf("listing upgrade insights for cluster %s", clusterName),
		func(rc context.Context, token *string) (*eks.ListInsightsOutput, error) {
			return s.eksClient.ListInsights(rc, &eks.ListInsightsInput{
				ClusterName: aws.String(clusterName),
				Filter: &ekstypes.InsightsFilter{
					Categories:         []ekstypes.Category{ekstypes.CategoryUpgradeReadiness},
					KubernetesVersions: []string{k8sVersion},
				},
				NextToken: token,
			})
		},
		func(out *eks.ListInsightsOutput) ([]ekstypes.InsightSummary, *string) {
			return out.Insights, out.NextToken
		},
	)
}

// controlPlaneStep derives the control-plane step for a hop, marking it
// completed when the live cluster is already at or past the hop target.
func controlPlaneStep(_, liveVersion, hopTo string, status ekstypes.ClusterStatus) Step {
	step := Step{
		Type:        StepControlPlane,
		Description: fmt.Sprintf("control plane → %s", hopTo),
		Version:     hopTo,
		Status:      StatusPending,
	}
	if versionAtLeast(liveVersion, hopTo) {
		step.Status = StatusCompleted
		step.Reason = fmt.Sprintf("control plane already at %s", liveVersion)
		return step
	}
	if status == ekstypes.ClusterStatusUpdating {
		step.Reason = "an update is already in progress; the orchestrator will attach and watch"
	}
	return step
}

// addonSteps derives one step per addon for the hop: the latest version
// compatible with the hop target, completed when the addon already runs it,
// blocked when no compatible version exists.
func (s *Service) addonSteps(ctx context.Context, svc *addons.ServiceImpl, addonList []addons.AddonSummary, hopTo string, skip []string) []Step {
	steps := make([]Step, 0, len(addonList))
	for _, a := range addonList {
		step := Step{
			Type:        StepAddon,
			Target:      a.Name,
			Description: fmt.Sprintf("addon %s → latest compatible with %s", a.Name, hopTo),
			Status:      StatusPending,
		}
		if matchesAny(a.Name, skip) {
			step.Status = StatusManual
			step.Reason = "skipped via --skip (managed out-of-band)"
			steps = append(steps, step)
			continue
		}
		versions, err := svc.GetAvailableVersions(ctx, a.Name, hopTo)
		if err != nil {
			step.Status = StatusBlocked
			step.Reason = fmt.Sprintf("no version of %s is compatible with %s: %v", a.Name, hopTo, err)
			steps = append(steps, step)
			continue
		}
		chosen := versions[0].Version
		step.Version = chosen
		step.Description = fmt.Sprintf("addon %s → %s (compatible with %s)", a.Name, chosen, hopTo)
		if addons.CompareVersions(a.Version, chosen) >= 0 {
			step.Status = StatusCompleted
			step.Reason = fmt.Sprintf("already at %s", a.Version)
		}
		steps = append(steps, step)
	}
	return steps
}

// nodegroupSteps derives one step per nodegroup for the hop. Custom-AMI
// nodegroups surface as manual actions (the operator owns their AMI
// lifecycle); skipped patterns likewise are never mutated.
func nodegroupSteps(nodegroups []nodegroupState, hopTo string, skipPatterns []string) []Step {
	steps := make([]Step, 0, len(nodegroups))
	for _, ng := range nodegroups {
		step := Step{
			Type:        StepNodegroup,
			Target:      ng.Name,
			Description: fmt.Sprintf("nodegroup %s → %s", ng.Name, hopTo),
			Version:     hopTo,
			Status:      StatusPending,
		}
		switch {
		case versionAtLeast(ng.Version, hopTo):
			step.Status = StatusCompleted
			step.Reason = fmt.Sprintf("already at %s", ng.Version)
		case matchesAny(ng.Name, skipPatterns):
			step.Status = StatusManual
			step.Reason = "skipped via --skip-nodegroup"
		case ng.CustomAMI:
			step.Status = StatusManual
			step.Reason = fmt.Sprintf("custom AMI nodegroup: build and roll a %s-compatible AMI yourself", hopTo)
		}
		steps = append(steps, step)
	}
	return steps
}
