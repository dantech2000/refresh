package aws

import (
	"context"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

func CurrentAmiID(ctx context.Context, ng *types.Nodegroup, ec2Client *ec2.Client, autoscalingClient *autoscaling.Client) string {
	// 1. Try launch template
	if ng.LaunchTemplate != nil && ng.LaunchTemplate.Version != nil && ng.LaunchTemplate.Id != nil {
		ltOut, err := ec2Client.DescribeLaunchTemplateVersions(ctx, &ec2.DescribeLaunchTemplateVersionsInput{
			LaunchTemplateId: ng.LaunchTemplate.Id,
			Versions:         []string{*ng.LaunchTemplate.Version},
		})
		if err == nil && len(ltOut.LaunchTemplateVersions) > 0 && ltOut.LaunchTemplateVersions[0].LaunchTemplateData != nil && ltOut.LaunchTemplateVersions[0].LaunchTemplateData.ImageId != nil {
			return *ltOut.LaunchTemplateVersions[0].LaunchTemplateData.ImageId
		}
	}
	// 2. Try ASG instance
	if ng.Resources != nil && len(ng.Resources.AutoScalingGroups) > 0 && ng.Resources.AutoScalingGroups[0].Name != nil {
		asgName := *ng.Resources.AutoScalingGroups[0].Name
		describeAsgOut, err := autoscalingClient.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{
			AutoScalingGroupNames: []string{asgName},
		})
		if err == nil && len(describeAsgOut.AutoScalingGroups) > 0 && len(describeAsgOut.AutoScalingGroups[0].Instances) > 0 {
			instanceId := describeAsgOut.AutoScalingGroups[0].Instances[0].InstanceId
			if instanceId != nil {
				descInstOut, err := ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
					InstanceIds: []string{*instanceId},
				})
				if err == nil && len(descInstOut.Reservations) > 0 && len(descInstOut.Reservations[0].Instances) > 0 && descInstOut.Reservations[0].Instances[0].ImageId != nil {
					return *descInstOut.Reservations[0].Instances[0].ImageId
				}
			}
		}
	}
	return ""
}

// LatestAmiID returns the latest recommended AMI ID for a given Kubernetes version and AMI type.
// It queries AWS SSM Parameter Store for the EKS-optimized AMI.
func LatestAmiID(ctx context.Context, ssmClient *ssm.Client, k8sVersion string) string {
	// Default to AL2 for backward compatibility
	return LatestAmiIDForType(ctx, ssmClient, k8sVersion, types.AMITypesAl2X8664)
}

// LatestAmiIDForType returns the latest recommended AMI ID for a specific AMI type.
// Supports AL2, AL2023, Bottlerocket, and Windows AMI types.
func LatestAmiIDForType(ctx context.Context, ssmClient *ssm.Client, k8sVersion string, amiType types.AMITypes) string {
	ssmParam := buildSSMParameterPath(k8sVersion, amiType)
	if ssmParam == "" {
		return ""
	}

	ssmOut, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name: aws.String(ssmParam),
	})
	if err == nil && ssmOut.Parameter != nil && ssmOut.Parameter.Value != nil {
		return *ssmOut.Parameter.Value
	}
	return ""
}

// buildSSMParameterPath constructs the SSM parameter path for the given AMI type.
// Reference: https://docs.aws.amazon.com/eks/latest/userguide/retrieve-ami-id.html
func buildSSMParameterPath(k8sVersion string, amiType types.AMITypes) string {
	basePrefix := "/aws/service/eks/optimized-ami/" + k8sVersion

	switch amiType {
	// Amazon Linux 2 (x86_64)
	case types.AMITypesAl2X8664:
		return basePrefix + "/amazon-linux-2/recommended/image_id"

	// Amazon Linux 2 (ARM64)
	case types.AMITypesAl2Arm64:
		return basePrefix + "/amazon-linux-2-arm64/recommended/image_id"

	// Amazon Linux 2 with GPU support
	case types.AMITypesAl2X8664Gpu:
		return basePrefix + "/amazon-linux-2-gpu/recommended/image_id"

	// Amazon Linux 2023 (x86_64) - Standard
	case types.AMITypesAl2023X8664Standard:
		return basePrefix + "/amazon-linux-2023/x86_64/standard/recommended/image_id"

	// Amazon Linux 2023 (ARM64) - Standard
	case types.AMITypesAl2023Arm64Standard:
		return basePrefix + "/amazon-linux-2023/arm64/standard/recommended/image_id"

	// Amazon Linux 2023 (x86_64) - Nvidia GPU
	case types.AMITypesAl2023X8664Nvidia:
		return basePrefix + "/amazon-linux-2023/x86_64/nvidia/recommended/image_id"

	// Amazon Linux 2023 (x86_64) - Neuron (AWS Inferentia/Trainium)
	case types.AMITypesAl2023X8664Neuron:
		return basePrefix + "/amazon-linux-2023/x86_64/neuron/recommended/image_id"

	// Amazon Linux 2023 (ARM64) - Nvidia GPU
	case types.AMITypesAl2023Arm64Nvidia:
		return basePrefix + "/amazon-linux-2023/arm64/nvidia/recommended/image_id"

	// Bottlerocket (x86_64)
	case types.AMITypesBottlerocketX8664:
		return basePrefix + "/bottlerocket/x86_64/recommended/image_id"

	// Bottlerocket (ARM64)
	case types.AMITypesBottlerocketArm64:
		return basePrefix + "/bottlerocket/arm64/recommended/image_id"

	// Bottlerocket (x86_64) with Nvidia GPU
	case types.AMITypesBottlerocketX8664Nvidia:
		return basePrefix + "/bottlerocket/x86_64/nvidia/recommended/image_id"

	// Bottlerocket (ARM64) with Nvidia GPU
	case types.AMITypesBottlerocketArm64Nvidia:
		return basePrefix + "/bottlerocket/arm64/nvidia/recommended/image_id"

	// Windows Server 2019 Full
	case types.AMITypesWindowsFull2019X8664:
		return basePrefix + "/windows/windows-2019-full/recommended/image_id"

	// Windows Server 2019 Core
	case types.AMITypesWindowsCore2019X8664:
		return basePrefix + "/windows/windows-2019-core/recommended/image_id"

	// Windows Server 2022 Full
	case types.AMITypesWindowsFull2022X8664:
		return basePrefix + "/windows/windows-2022-full/recommended/image_id"

	// Windows Server 2022 Core
	case types.AMITypesWindowsCore2022X8664:
		return basePrefix + "/windows/windows-2022-core/recommended/image_id"

	// Custom AMI - cannot determine latest from SSM
	case types.AMITypesCustom:
		return ""

	default:
		// Fallback: try to infer from AMI type string
		amiTypeStr := string(amiType)
		if strings.Contains(amiTypeStr, "AL2023") {
			if strings.Contains(amiTypeStr, "ARM64") {
				return basePrefix + "/amazon-linux-2023/arm64/standard/recommended/image_id"
			}
			return basePrefix + "/amazon-linux-2023/x86_64/standard/recommended/image_id"
		}
		if strings.Contains(amiTypeStr, "BOTTLEROCKET") {
			if strings.Contains(amiTypeStr, "ARM64") {
				return basePrefix + "/bottlerocket/arm64/recommended/image_id"
			}
			return basePrefix + "/bottlerocket/x86_64/recommended/image_id"
		}
		// Default to AL2 x86_64
		return basePrefix + "/amazon-linux-2/recommended/image_id"
	}
}
