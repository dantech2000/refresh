package diag

// The IAM actions a Failure's Operation names. Each one is listed in
// awserr.RequiredPermissions (a test checks this), so a user who gets
// ReasonAccessDenied can find the action in the documented IAM table.
const (
	OpListClusters                   = "eks:ListClusters"
	OpDescribeCluster                = "eks:DescribeCluster"
	OpDescribeClusterVersions        = "eks:DescribeClusterVersions"
	OpListNodegroups                 = "eks:ListNodegroups"
	OpDescribeNodegroup              = "eks:DescribeNodegroup"
	OpListAddons                     = "eks:ListAddons"
	OpDescribeAddon                  = "eks:DescribeAddon"
	OpDescribeAddonVersions          = "eks:DescribeAddonVersions"
	OpListInsights                   = "eks:ListInsights"
	OpDescribeInsight                = "eks:DescribeInsight"
	OpStartInsightsRefresh           = "eks:StartInsightsRefresh"
	OpDescribeInsightsRefresh        = "eks:DescribeInsightsRefresh"
	OpDescribeUpdate                 = "eks:DescribeUpdate"
	OpListUpdates                    = "eks:ListUpdates"
	OpUpdateClusterVersion           = "eks:UpdateClusterVersion"
	OpUpdateNodegroupVersion         = "eks:UpdateNodegroupVersion"
	OpUpdateNodegroupConfig          = "eks:UpdateNodegroupConfig"
	OpUpdateAddon                    = "eks:UpdateAddon"
	OpGetParameter                   = "ssm:GetParameter"
	OpDescribeImages                 = "ec2:DescribeImages"
	OpDescribeInstances              = "ec2:DescribeInstances"
	OpDescribeLaunchTemplateVersions = "ec2:DescribeLaunchTemplateVersions"
	OpDescribeAutoScalingGroups      = "autoscaling:DescribeAutoScalingGroups"
)
