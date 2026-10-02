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

// IsChange reports whether op is a call that changes the cluster (an
// update). Its failure means the change did not start, not that data is
// missing.
func IsChange(op string) bool {
	switch op {
	case OpUpdateClusterVersion, OpUpdateNodegroupVersion, OpUpdateNodegroupConfig, OpUpdateAddon:
		return true
	}
	return false
}

// NotStarted reports whether f is a change (IsChange) that AWS rejected, so
// it did not start. A change whose call got no clear answer (a network
// error, a timeout, an AWS-side failure) may have started: it is not one.
func (f Failure) NotStarted() bool {
	if !IsChange(f.Operation) {
		return false
	}
	switch f.Reason {
	case ReasonAccessDenied, ReasonCredentialError, ReasonThrottled, ReasonNotFound,
		ReasonRegionUnavailable, ReasonInvalidRequest, ReasonNotAttempted:
		return true
	}
	return false
}
