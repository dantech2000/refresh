package aws

import (
	"context"

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

func LatestAmiID(ctx context.Context, ssmClient *ssm.Client, k8sVersion string) string {
	ssmParam := "/aws/service/eks/optimized-ami/" + k8sVersion + "/amazon-linux-2/recommended/image_id"
	ssmOut, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name: aws.String(ssmParam),
	})
	if err == nil && ssmOut.Parameter != nil && ssmOut.Parameter.Value != nil {
		return *ssmOut.Parameter.Value
	}
	return ""
}
