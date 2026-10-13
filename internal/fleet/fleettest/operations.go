package fleettest

// The Kubernetes requests refresh makes, named as operations so that a
// Fault can apply to them. Their service is "kube". Deny, Code, Delay,
// Hang, and Reset apply; kubetest ignores the rest.
const (
	KubeListNamespaces  Operation = "kube:ListNamespaces"
	KubeListNodes       Operation = "kube:ListNodes"
	KubeWatchNodes      Operation = "kube:WatchNodes"
	KubeListPods        Operation = "kube:ListPods"
	KubeListPDBs        Operation = "kube:ListPodDisruptionBudgets"
	KubeListDeployments Operation = "kube:ListDeployments"
	KubeListEvents      Operation = "kube:ListEvents"
	KubeWatchEvents     Operation = "kube:WatchEvents"
	KubeListNodeMetrics Operation = "kube:ListNodeMetrics"
)

// The AWS operations refresh calls.
const (
	STSGetCallerIdentity Operation = "sts:GetCallerIdentity"

	EKSListClusters                      Operation = "eks:ListClusters"
	EKSDescribeCluster                   Operation = "eks:DescribeCluster"
	EKSListNodegroups                    Operation = "eks:ListNodegroups"
	EKSDescribeNodegroup                 Operation = "eks:DescribeNodegroup"
	EKSListAddons                        Operation = "eks:ListAddons"
	EKSDescribeAddon                     Operation = "eks:DescribeAddon"
	EKSDescribeAddonVersions             Operation = "eks:DescribeAddonVersions"
	EKSDescribeClusterVersions           Operation = "eks:DescribeClusterVersions"
	EKSListInsights                      Operation = "eks:ListInsights"
	EKSDescribeInsight                   Operation = "eks:DescribeInsight"
	EKSStartInsightsRefresh              Operation = "eks:StartInsightsRefresh"
	EKSDescribeInsightsRefresh           Operation = "eks:DescribeInsightsRefresh"
	EKSUpdateNodegroupVersion            Operation = "eks:UpdateNodegroupVersion"
	EKSUpdateNodegroupConfig             Operation = "eks:UpdateNodegroupConfig"
	EKSUpdateClusterVersion              Operation = "eks:UpdateClusterVersion"
	EKSUpdateAddon                       Operation = "eks:UpdateAddon"
	EKSDescribeUpdate                    Operation = "eks:DescribeUpdate"
	EKSListUpdates                       Operation = "eks:ListUpdates"
	SSMGetParameter                      Operation = "ssm:GetParameter"
	EC2DescribeImages                    Operation = "ec2:DescribeImages"
	EC2DescribeInstances                 Operation = "ec2:DescribeInstances"
	EC2DescribeLaunchTemplateVersions    Operation = "ec2:DescribeLaunchTemplateVersions"
	EC2DescribeSubnets                   Operation = "ec2:DescribeSubnets"
	EC2DescribeInstanceTypeOfferings     Operation = "ec2:DescribeInstanceTypeOfferings"
	EC2DescribeVpcs                      Operation = "ec2:DescribeVpcs"
	AutoScalingDescribeAutoScalingGroups Operation = "autoscaling:DescribeAutoScalingGroups"
	AutoScalingDescribeScalingActivities Operation = "autoscaling:DescribeScalingActivities"
	CloudWatchGetMetricData              Operation = "cloudwatch:GetMetricData"
	ServiceQuotasGetServiceQuota         Operation = "servicequotas:GetServiceQuota"
)
