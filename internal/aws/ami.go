// Package aws provides AWS SDK abstractions and utilities for EKS cluster management.
// It implements clean patterns for AMI resolution, cluster discovery, and error handling.
package aws

import (
	"context"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// AMIResolver handles AMI ID resolution for EKS nodegroups.
// It supports caching to reduce API calls for repeated lookups.
type AMIResolver struct {
	ec2Client         *ec2.Client
	autoscalingClient *autoscaling.Client
	ssmClient         *ssm.Client
	cache             *AMICache
}

// AMICache provides thread-safe caching for AMI lookups.
type AMICache struct {
	mu    sync.RWMutex
	items map[string]string
}

// NewAMICache creates a new AMI cache instance.
func NewAMICache() *AMICache {
	return &AMICache{
		items: make(map[string]string),
	}
}

// Get retrieves a value from the cache in a thread-safe manner.
func (c *AMICache) Get(key string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	val, ok := c.items[key]
	return val, ok
}

// Set stores a value in the cache in a thread-safe manner.
func (c *AMICache) Set(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = value
}

// NewAMIResolver creates a new AMI resolver with the provided AWS clients.
func NewAMIResolver(ec2Client *ec2.Client, autoscalingClient *autoscaling.Client, ssmClient *ssm.Client) *AMIResolver {
	return &AMIResolver{
		ec2Client:         ec2Client,
		autoscalingClient: autoscalingClient,
		ssmClient:         ssmClient,
		cache:             NewAMICache(),
	}
}

// CurrentAmiID resolves the current AMI ID for a nodegroup.
// It attempts resolution in order: launch template, then ASG instance.
func CurrentAmiID(ctx context.Context, ng *types.Nodegroup, ec2Client *ec2.Client, autoscalingClient *autoscaling.Client) string {
	// Try launch template first
	if amiID := resolveFromLaunchTemplate(ctx, ng, ec2Client); amiID != "" {
		return amiID
	}

	// Fall back to ASG instance
	return resolveFromASG(ctx, ng, autoscalingClient, ec2Client)
}

// resolveFromLaunchTemplate attempts to get the AMI ID from the nodegroup's launch template.
func resolveFromLaunchTemplate(ctx context.Context, ng *types.Nodegroup, ec2Client *ec2.Client) string {
	if ng.LaunchTemplate == nil || ng.LaunchTemplate.Version == nil || ng.LaunchTemplate.Id == nil {
		return ""
	}

	ltOut, err := ec2Client.DescribeLaunchTemplateVersions(ctx, &ec2.DescribeLaunchTemplateVersionsInput{
		LaunchTemplateId: ng.LaunchTemplate.Id,
		Versions:         []string{*ng.LaunchTemplate.Version},
	})
	if err != nil {
		return ""
	}

	if len(ltOut.LaunchTemplateVersions) == 0 ||
		ltOut.LaunchTemplateVersions[0].LaunchTemplateData == nil ||
		ltOut.LaunchTemplateVersions[0].LaunchTemplateData.ImageId == nil {
		return ""
	}

	return *ltOut.LaunchTemplateVersions[0].LaunchTemplateData.ImageId
}

// resolveFromASG attempts to get the AMI ID from instances in the nodegroup's ASG.
func resolveFromASG(ctx context.Context, ng *types.Nodegroup, autoscalingClient *autoscaling.Client, ec2Client *ec2.Client) string {
	if ng.Resources == nil || len(ng.Resources.AutoScalingGroups) == 0 || ng.Resources.AutoScalingGroups[0].Name == nil {
		return ""
	}

	asgName := *ng.Resources.AutoScalingGroups[0].Name

	describeAsgOut, err := autoscalingClient.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{
		AutoScalingGroupNames: []string{asgName},
	})
	if err != nil || len(describeAsgOut.AutoScalingGroups) == 0 || len(describeAsgOut.AutoScalingGroups[0].Instances) == 0 {
		return ""
	}

	instanceId := describeAsgOut.AutoScalingGroups[0].Instances[0].InstanceId
	if instanceId == nil {
		return ""
	}

	descInstOut, err := ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{*instanceId},
	})
	if err != nil {
		return ""
	}

	if len(descInstOut.Reservations) == 0 ||
		len(descInstOut.Reservations[0].Instances) == 0 ||
		descInstOut.Reservations[0].Instances[0].ImageId == nil {
		return ""
	}

	return *descInstOut.Reservations[0].Instances[0].ImageId
}

// LatestAmiID returns the latest recommended AMI ID for a given Kubernetes version.
// It defaults to AL2 x86_64 for backward compatibility.
func LatestAmiID(ctx context.Context, ssmClient *ssm.Client, k8sVersion string) string {
	return LatestAmiIDForType(ctx, ssmClient, k8sVersion, types.AMITypesAl2X8664)
}

// LatestAmiIDForType returns the latest recommended AMI ID for a specific AMI type.
// It queries AWS SSM Parameter Store for the EKS-optimized AMI.
// Supports AL2, AL2023, Bottlerocket, and Windows AMI types.
func LatestAmiIDForType(ctx context.Context, ssmClient *ssm.Client, k8sVersion string, amiType types.AMITypes) string {
	ssmParam := buildSSMParameterPath(k8sVersion, amiType)
	if ssmParam == "" {
		return ""
	}

	ssmOut, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name: aws.String(ssmParam),
	})
	if err != nil || ssmOut.Parameter == nil || ssmOut.Parameter.Value == nil {
		return ""
	}

	return *ssmOut.Parameter.Value
}

// buildSSMParameterPath constructs the SSM parameter path for the given AMI type.
// Reference: https://docs.aws.amazon.com/eks/latest/userguide/retrieve-ami-id.html
func buildSSMParameterPath(k8sVersion string, amiType types.AMITypes) string {
	basePrefix := "/aws/service/eks/optimized-ami/" + k8sVersion

	// AMI type to SSM path mapping
	amiPaths := map[types.AMITypes]string{
		// Amazon Linux 2
		types.AMITypesAl2X8664:    "/amazon-linux-2/recommended/image_id",
		types.AMITypesAl2Arm64:    "/amazon-linux-2-arm64/recommended/image_id",
		types.AMITypesAl2X8664Gpu: "/amazon-linux-2-gpu/recommended/image_id",

		// Amazon Linux 2023
		types.AMITypesAl2023X8664Standard: "/amazon-linux-2023/x86_64/standard/recommended/image_id",
		types.AMITypesAl2023Arm64Standard: "/amazon-linux-2023/arm64/standard/recommended/image_id",
		types.AMITypesAl2023X8664Nvidia:   "/amazon-linux-2023/x86_64/nvidia/recommended/image_id",
		types.AMITypesAl2023X8664Neuron:   "/amazon-linux-2023/x86_64/neuron/recommended/image_id",
		types.AMITypesAl2023Arm64Nvidia:   "/amazon-linux-2023/arm64/nvidia/recommended/image_id",

		// Bottlerocket
		types.AMITypesBottlerocketX8664:       "/bottlerocket/x86_64/recommended/image_id",
		types.AMITypesBottlerocketArm64:       "/bottlerocket/arm64/recommended/image_id",
		types.AMITypesBottlerocketX8664Nvidia: "/bottlerocket/x86_64/nvidia/recommended/image_id",
		types.AMITypesBottlerocketArm64Nvidia: "/bottlerocket/arm64/nvidia/recommended/image_id",

		// Windows
		types.AMITypesWindowsFull2019X8664: "/windows/windows-2019-full/recommended/image_id",
		types.AMITypesWindowsCore2019X8664: "/windows/windows-2019-core/recommended/image_id",
		types.AMITypesWindowsFull2022X8664: "/windows/windows-2022-full/recommended/image_id",
		types.AMITypesWindowsCore2022X8664: "/windows/windows-2022-core/recommended/image_id",
	}

	if path, ok := amiPaths[amiType]; ok {
		return basePrefix + path
	}

	// Custom AMI - cannot determine latest from SSM
	if amiType == types.AMITypesCustom {
		return ""
	}

	// Fallback: try to infer from AMI type string
	return inferSSMPath(basePrefix, string(amiType))
}

// inferSSMPath attempts to infer the SSM parameter path from the AMI type string.
func inferSSMPath(basePrefix, amiTypeStr string) string {
	amiTypeStr = strings.ToUpper(amiTypeStr)

	switch {
	case strings.Contains(amiTypeStr, "AL2023"):
		if strings.Contains(amiTypeStr, "ARM64") {
			return basePrefix + "/amazon-linux-2023/arm64/standard/recommended/image_id"
		}
		return basePrefix + "/amazon-linux-2023/x86_64/standard/recommended/image_id"

	case strings.Contains(amiTypeStr, "BOTTLEROCKET"):
		if strings.Contains(amiTypeStr, "ARM64") {
			return basePrefix + "/bottlerocket/arm64/recommended/image_id"
		}
		return basePrefix + "/bottlerocket/x86_64/recommended/image_id"

	default:
		// Default to AL2 x86_64
		return basePrefix + "/amazon-linux-2/recommended/image_id"
	}
}
