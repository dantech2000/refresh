package awsx

import (
	"fmt"
	"strings"
)

// Permission is one row of the required IAM permissions: the actions and the
// commands that call them. UsedBy is Markdown (command names in backticks).
type Permission struct {
	Actions []string
	UsedBy  string
}

// PermissionsDocURL is the published version of the IAM permissions table.
const PermissionsDocURL = "https://drod.dev/refresh/concepts/configuration/#required-iam-permissions"

// RequiredPermissions returns the AWS actions that refresh calls, each with
// the commands that need it. It is the one source of that list: the
// permission-error hint prints it, and the IAM table in the docs must match
// it row for row.
func RequiredPermissions() []Permission {
	return []Permission{
		{[]string{"sts:GetCallerIdentity"}, "Region sweeps in which no region answered (credential check)"},
		{[]string{"eks:ListClusters"}, "`status`, `cluster list`, `ui`, `nodegroup update --all-clusters`, and the close matches for a cluster name that is not found"},
		{[]string{"eks:DescribeCluster"}, "Every cluster command"},
		{[]string{"eks:ListNodegroups", "eks:DescribeNodegroup"}, "`status`, `nodegroup *`, `cluster describe`/`upgrade-check`/`upgrade`/`rollback`, health checks, the busy check of `addon update`"},
		{[]string{"eks:ListAddons"}, "`status`, `addon list`/`update`, `cluster describe`/`upgrade-check`/`upgrade`/`rollback`, the busy check of `nodegroup update`/`scale`"},
		{[]string{"eks:DescribeAddon", "eks:DescribeAddonVersions"}, "`status`, `addon *`, `cluster upgrade-check`/`upgrade`/`rollback`; `eks:DescribeAddon` also in `cluster describe` and the busy check of `nodegroup update`/`scale`"},
		{[]string{"eks:DescribeClusterVersions"}, "`status`, `cluster describe`/`upgrade-check`/`upgrade`/`rollback` (support calendar; `refresh` falls back to a built-in calendar)"},
		{[]string{"eks:ListInsights", "eks:DescribeInsight"}, "`cluster upgrade-check`, `cluster upgrade`, `cluster rollback`"},
		{[]string{"eks:StartInsightsRefresh", "eks:DescribeInsightsRefresh"}, "`cluster upgrade` (not with `--dry-run` or `--skip-insights-check`)"},
		{[]string{"eks:UpdateNodegroupVersion"}, "`nodegroup update`, `cluster upgrade`, `cluster rollback`"},
		{[]string{"eks:UpdateNodegroupConfig"}, "`nodegroup scale`"},
		{[]string{"eks:UpdateClusterVersion"}, "`cluster upgrade`, `cluster rollback`"},
		{[]string{"eks:UpdateAddon"}, "`addon update`, `cluster upgrade`, `cluster rollback`"},
		{[]string{"eks:DescribeUpdate"}, "`nodegroup update`, `nodegroup scale`, `addon update` (not with `--no-wait`), `cluster upgrade`, `cluster rollback`, `cluster upgrade-check` (rollback window)"},
		{[]string{"eks:ListUpdates"}, "`cluster rollback`, `cluster upgrade-check` (rollback window), `addon update` and `nodegroup scale` (the type of the update that keeps a target busy), `ui` (watch a roll or upgrade started elsewhere)"},
		{[]string{"ssm:GetParameter"}, "Latest recommended AMI: `status`, `cluster describe`, `nodegroup list`/`describe`/`update`"},
		{[]string{"ec2:DescribeImages"}, "`status` (AMI age)"},
		{[]string{"ec2:DescribeInstances"}, "`status` (Karpenter detection), `nodegroup describe --show-instances`, current AMI of a nodegroup without a launch-template image (`status`, `cluster describe`, `nodegroup list`/`describe`/`update`)"},
		{[]string{"ec2:DescribeLaunchTemplateVersions"}, "Current AMI of a launch-template nodegroup: `status`, `nodegroup list`/`describe`/`update`"},
		{[]string{"ec2:DescribeSubnets", "ec2:DescribeInstanceTypeOfferings"}, "Instance-type availability warning in `nodegroup update`/`scale`"},
		{[]string{"ec2:DescribeInstanceTypes"}, "The vCPUs of the nodes that a roll or a scale-up launches, for the quota check of `nodegroup update`/`scale`, `cluster upgrade`, `cluster rollback`"},
		{[]string{"ec2:DescribeVpcs"}, "`cluster describe`"},
		{[]string{"autoscaling:DescribeAutoScalingGroups"}, "Health checks, the instances of a nodegroup (`status`, `cluster describe`, `nodegroup list`/`describe`/`update`)"},
		{[]string{"autoscaling:DescribeScalingActivities"}, "A roll's failed node launches (such as the EC2 vCPU quota): `nodegroup update`, `cluster upgrade`, `cluster rollback`, `ui`"},
		{[]string{"cloudwatch:GetMetricData"}, "Health checks (CPU capacity, control plane, quota usage)"},
		{[]string{"servicequotas:GetServiceQuota"}, "Health checks (EC2 vCPU quota)"},
	}
}

// permissionHint renders RequiredPermissions as a plain-text list for the
// terminal: one line per row, with the Markdown backticks removed.
func permissionHint() string {
	// Two aligned columns, one action per row; a group's use is on its
	// first row.
	w := 0
	for _, p := range RequiredPermissions() {
		for _, a := range p.Actions {
			w = max(w, len(a))
		}
	}
	var b strings.Builder
	for _, p := range RequiredPermissions() {
		for i, a := range p.Actions {
			used := ""
			if i == 0 {
				used = strings.ReplaceAll(p.UsedBy, "`", "")
			}
			b.WriteString(strings.TrimRight(fmt.Sprintf("  %-*s  %s", w, a, used), " "))
			b.WriteString("\n")
		}
	}
	return b.String()
}
