package upgrade

import (
	"context"
	"errors"
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

	// Resume after an interrupted multi-hop run: the control plane may have
	// reached a hop's version while that hop's addons/nodegroups never
	// caught up. Planning only the next control-plane move would leave them
	// on the previous era's versions, so finish the current version first.
	if len(hops) > 0 && hops[0] != currentVersion && s.needsCatchUp(ctx, addonsSvc, addonList, nodegroups, currentVersion, opts) {
		plan.Hops = append(plan.Hops, s.catchUpHop(ctx, addonsSvc, addonList, nodegroups, cluster, currentVersion, opts))
		// Only nodegroups the catch-up can actually roll advance; ones
		// already beyond the kubelet skew stay put so the next hop's
		// readiness step still blocks on them.
		for _, ng := range nodegroups {
			if !beyondKubeletSkew(ng.Version, currentVersion) {
				advanceSimulation(simNodegroups, []nodegroupState{ng}, currentVersion, opts.SkipNodegroups)
			}
		}
	}

	for _, hopTo := range hops {
		hop := Hop{From: prevVersion(plan, hopTo), To: hopTo}

		hop.Steps = append(hop.Steps, s.readinessStep(ctx, clusterName, currentVersion, hopTo, nodegroups, simNodegroups, plan))
		hop.Steps = append(hop.Steps, controlPlaneStep(currentVersion, aws.ToString(cluster.Version), hopTo, cluster.Status))
		hop.Steps = append(hop.Steps, s.addonSteps(ctx, addonsSvc, addonList, hopTo, opts.SkipAddons)...)
		hop.Steps = append(hop.Steps, nodegroupSteps(nodegroups, hopTo, opts.SkipNodegroups)...)

		plan.Hops = append(plan.Hops, hop)

		// Advance the simulation: after this hop, rollable nodegroups sit at
		// the hop target.
		advanceSimulation(simNodegroups, nodegroups, hopTo, opts.SkipNodegroups)
	}

	return plan, nil
}

// advanceSimulation moves every rollable nodegroup's simulated version up to
// version (custom-AMI and skipped nodegroups stay where they are).
func advanceSimulation(sim map[string]string, nodegroups []nodegroupState, version string, skip []string) {
	for _, ng := range nodegroups {
		if !ng.CustomAMI && !matchesAny(ng.Name, skip) && !versionAtLeast(sim[ng.Name], version) {
			sim[ng.Name] = version
		}
	}
}

// needsCatchUp reports whether addons or nodegroups lag the live control-plane
// version, i.e. a previous run moved the control plane but was interrupted
// before the rest of that hop finished. Signals:
//   - a rollable nodegroup sits exactly one minor behind the control plane
//     (nodegroup rolls are the last phase of a hop, so this catches every
//     interruption point on clusters with managed nodegroups; nodegroups
//     further behind predate the run and stay with the kubelet-skew gate);
//   - an addon's installed version is not among the versions EKS lists as
//     compatible with the control-plane version.
//
// An addon that is merely not the newest compatible build does not trigger a
// catch-up; the next hop's addon phase moves it anyway. Version-lookup API
// errors are not treated as lag here: the regular hop steps surface them.
func (s *Service) needsCatchUp(ctx context.Context, svc *addons.ServiceImpl, addonList []addons.AddonSummary, nodegroups []nodegroupState, cpVersion string, opts PlanOptions) bool {
	cpMinor, err := minorVersion(cpVersion)
	if err != nil {
		return false
	}
	for _, ng := range nodegroups {
		if ng.CustomAMI || matchesAny(ng.Name, opts.SkipNodegroups) {
			continue
		}
		if ngMinor, err := minorVersion(ng.Version); err == nil && ngMinor == cpMinor-1 {
			return true
		}
	}
	for _, a := range addonList {
		if isSkippedAddon(a.Name, opts.SkipAddons) {
			continue
		}
		versions, err := svc.GetAvailableVersions(ctx, a.Name, cpVersion)
		if err != nil {
			continue
		}
		compatible := false
		for _, v := range versions {
			if addons.CompareVersions(a.Version, v.Version) == 0 {
				compatible = true
				break
			}
		}
		if !compatible {
			return true
		}
	}
	return false
}

// catchUpHop builds a same-version hop that finishes the work of an
// interrupted hop: addons to the latest version compatible with the live
// control plane and nodegroups rolled to it. The control-plane step is
// already satisfied, and no readiness step is needed because the control
// plane does not move. A nodegroup already beyond the kubelet skew of the
// control plane is not rolled across that gap here: its step is blocked,
// like the skew blocker in a regular hop's readiness step.
func (s *Service) catchUpHop(ctx context.Context, svc *addons.ServiceImpl, addonList []addons.AddonSummary, nodegroups []nodegroupState, cluster *ekstypes.Cluster, cpVersion string, opts PlanOptions) Hop {
	hop := Hop{From: cpVersion, To: cpVersion}
	hop.Steps = append(hop.Steps, controlPlaneStep(cpVersion, aws.ToString(cluster.Version), cpVersion, cluster.Status))
	hop.Steps = append(hop.Steps, s.addonSteps(ctx, svc, addonList, cpVersion, opts.SkipAddons)...)
	ngSteps := nodegroupSteps(nodegroups, cpVersion, opts.SkipNodegroups)
	for i, ng := range nodegroups {
		if ngSteps[i].Status == StatusPending && beyondKubeletSkew(ng.Version, cpVersion) {
			ngSteps[i].Status = StatusBlocked
			ngSteps[i].Reason = fmt.Sprintf("nodegroup %s at %s already exceeds the kubelet skew limit (%d minors) against the control plane at %s",
				ng.Name, ng.Version, kubeletSkew, cpVersion)
		}
	}
	hop.Steps = append(hop.Steps, ngSteps...)
	return hop
}

// beyondKubeletSkew reports whether a nodegroup at ngVersion lags cpVersion
// by more than the supported kubelet skew.
func beyondKubeletSkew(ngVersion, cpVersion string) bool {
	ngMinor, err1 := minorVersion(ngVersion)
	cpMinor, err2 := minorVersion(cpVersion)
	if err1 != nil || err2 != nil {
		return false
	}
	return cpMinor-ngMinor > kubeletSkew
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
//
// EKS evaluates upgrade insights against the next minor after the live
// control plane only, so insights are queried just for the hop that starts
// from liveVersion. Later hops get a skew-only check here; the engine
// re-runs the full readiness gate against live state immediately before each
// hop's control-plane phase (checkHopReadiness).
func (s *Service) readinessStep(ctx context.Context, clusterName, liveVersion, hopTo string, nodegroups []nodegroupState, simNodegroups map[string]string, plan *Plan) Step {
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

	if liveMinor, err := minorVersion(liveVersion); err == nil && hopMinor > liveMinor+1 {
		step.Reason = "skew OK; insights are checked against live state before this hop"
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

// checkHopReadiness re-evaluates the readiness gate for hopTo against the
// live cluster (current control-plane and nodegroup versions, insights for
// the next minor). It is a no-op when the control plane is already at or past
// hopTo, and returns an error naming the blocker when the hop must not start.
func (s *Service) checkHopReadiness(ctx context.Context, clusterName, hopTo string, progress ProgressFunc) error {
	progress = ensureProgress(progress)

	cluster, err := s.describeCluster(ctx, clusterName)
	if err != nil {
		return err
	}
	liveVersion := aws.ToString(cluster.Version)
	if versionAtLeast(liveVersion, hopTo) {
		return nil
	}

	nodegroups, err := s.listNodegroupStates(ctx, clusterName)
	if err != nil {
		return err
	}
	live := make(map[string]string, len(nodegroups))
	for _, ng := range nodegroups {
		live[ng.Name] = ng.Version
	}

	scratch := &Plan{}
	step := s.readinessStep(ctx, clusterName, liveVersion, hopTo, nodegroups, live, scratch)
	for _, w := range scratch.Warnings {
		progress("warning: %s", w)
	}
	if step.Status == StatusBlocked {
		return fmt.Errorf("readiness for %s is blocked against live cluster state: %s", hopTo, step.Reason)
	}
	progress("readiness for %s: %s", hopTo, step.Reason)
	return nil
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
			// Either way the step can't be planned safely, but only an
			// empty catalogue means "incompatible"; an API failure
			// (throttling after retries, AccessDenied) says so instead.
			step.Status = StatusBlocked
			if errors.Is(err, addons.ErrNoVersionsFound) {
				step.Reason = fmt.Sprintf("no version of %s is compatible with %s: %v", a.Name, hopTo, err)
			} else {
				step.Reason = fmt.Sprintf("could not look up versions of %s compatible with %s (rerun to retry): %v", a.Name, hopTo, err)
			}
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
		case ng.Status == ekstypes.NodegroupStatusUpdating:
			step.Reason = "an update is already in progress; the orchestrator will attach and wait for it"
		}
		steps = append(steps, step)
	}
	return steps
}
