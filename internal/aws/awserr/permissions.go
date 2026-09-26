package awserr

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

// RequiredPermissions is the single source of the AWS actions refresh calls.
// The permission-error hint prints it, and a test checks that the table in
// docs/concepts/configuration.md matches it row for row and that every AWS
// API call in the code appears in it.
var RequiredPermissions = []Permission{
	{[]string{"sts:GetCallerIdentity"}, "Region sweeps in which no region answered (credential check)"},
	{[]string{"eks:ListClusters"}, "`status`, `cluster list`, `nodegroup update --all-clusters`, partial cluster names"},
	{[]string{"eks:DescribeCluster"}, "Every cluster command"},
	{[]string{"eks:ListNodegroups", "eks:DescribeNodegroup"}, "`status`, `nodegroup *`, `cluster describe`/`upgrade-check`/`upgrade`/`rollback`, health checks, the busy check of `addon update`"},
	{[]string{"eks:ListAddons"}, "`status`, `addon *` (also to resolve a partial add-on name), `cluster describe`/`upgrade-check`/`upgrade`/`rollback`, the busy check of `nodegroup update`/`scale`"},
	{[]string{"eks:DescribeAddon", "eks:DescribeAddonVersions"}, "`status`, `addon *`, `cluster upgrade-check`/`upgrade`/`rollback`; `eks:DescribeAddon` also in the busy check of `nodegroup update`/`scale`"},
	{[]string{"eks:DescribeClusterVersions"}, "`status`, `cluster describe`/`upgrade-check`/`upgrade`/`rollback` (support calendar; `refresh` falls back to a built-in calendar)"},
	{[]string{"eks:ListInsights", "eks:DescribeInsight"}, "`cluster upgrade-check`, `cluster upgrade`, `cluster rollback`"},
	{[]string{"eks:StartInsightsRefresh", "eks:DescribeInsightsRefresh"}, "`cluster upgrade` (not with `--dry-run` or `--skip-insights-check`)"},
	{[]string{"eks:UpdateNodegroupVersion"}, "`nodegroup update`, `cluster upgrade`, `cluster rollback`"},
	{[]string{"eks:UpdateNodegroupConfig"}, "`nodegroup scale`"},
	{[]string{"eks:UpdateClusterVersion"}, "`cluster upgrade`, `cluster rollback`"},
	{[]string{"eks:UpdateAddon"}, "`addon update`, `cluster upgrade`, `cluster rollback`"},
	{[]string{"eks:DescribeUpdate"}, "`nodegroup update`, `nodegroup scale --wait`, `addon update --wait`, `cluster upgrade`, `cluster rollback`, `cluster upgrade-check` (rollback window)"},
	{[]string{"eks:ListUpdates"}, "`cluster rollback`, `cluster upgrade-check` (rollback window), `refresh ui` (watch a roll or upgrade started elsewhere)"},
	{[]string{"ssm:GetParameter"}, "Latest recommended AMI: `status`, `nodegroup list`/`describe`/`update`"},
	{[]string{"ec2:DescribeImages"}, "`status` (AMI age)"},
	{[]string{"ec2:DescribeInstances"}, "`status` (Karpenter detection), `nodegroup describe --show-instances`, current AMI lookup"},
	{[]string{"ec2:DescribeLaunchTemplateVersions"}, "Current AMI of a launch-template nodegroup (`nodegroup update --dry-run`)"},
	{[]string{"ec2:DescribeSubnets", "ec2:DescribeInstanceTypeOfferings"}, "Instance-type availability warning in `nodegroup update`/`scale`"},
	{[]string{"ec2:DescribeVpcs"}, "`cluster describe --detailed`"},
	{[]string{"autoscaling:DescribeAutoScalingGroups"}, "Health checks, `nodegroup describe`, current AMI lookup"},
	{[]string{"cloudwatch:GetMetricData"}, "Health checks (CPU capacity, control plane, quota usage)"},
	{[]string{"servicequotas:GetServiceQuota"}, "Health checks (EC2 vCPU quota)"},
}

// permissionHint renders RequiredPermissions as a plain-text list for the
// terminal: one line per row, with the Markdown backticks removed.
func permissionHint() string {
	// Two aligned columns, one action per row; a group's use is on its
	// first row.
	w := 0
	for _, p := range RequiredPermissions {
		for _, a := range p.Actions {
			w = max(w, len(a))
		}
	}
	var b strings.Builder
	for _, p := range RequiredPermissions {
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
