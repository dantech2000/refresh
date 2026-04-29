package nodegroup

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/services/common"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func firstInstanceType(types []string) string {
	if len(types) == 0 {
		return "Unknown"
	}
	return types[0]
}

func normalizeWindow(w string) string {
	switch w {
	case "1h", "3h", "24h":
		return w
	default:
		return "24h"
	}
}

// getNodegroupInstanceIDs resolves backing ASG instances for a managed nodegroup.
func (s *ServiceImpl) getNodegroupInstanceIDs(ctx context.Context, clusterName, nodegroupName string) ([]string, bool) {
	out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeNodegroupOutput, error) {
		return s.eksClient.DescribeNodegroup(rc, &eks.DescribeNodegroupInput{
			ClusterName:   aws.String(clusterName),
			NodegroupName: aws.String(nodegroupName),
		})
	})
	if err != nil || out.Nodegroup == nil || out.Nodegroup.Resources == nil {
		return nil, false
	}
	var asgNames []string
	for _, asg := range out.Nodegroup.Resources.AutoScalingGroups {
		if asg.Name != nil && *asg.Name != "" {
			asgNames = append(asgNames, *asg.Name)
		}
	}
	if len(asgNames) == 0 || s.asgClient == nil {
		return nil, false
	}
	var ids []string
	for _, name := range asgNames {
		asgOut, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*autoscaling.DescribeAutoScalingGroupsOutput, error) {
			return s.asgClient.DescribeAutoScalingGroups(rc, &autoscaling.DescribeAutoScalingGroupsInput{
				AutoScalingGroupNames: []string{name},
			})
		})
		if err != nil || len(asgOut.AutoScalingGroups) == 0 {
			continue
		}
		for _, inst := range asgOut.AutoScalingGroups[0].Instances {
			if inst.InstanceId != nil && *inst.InstanceId != "" {
				ids = append(ids, *inst.InstanceId)
			}
		}
	}
	if len(ids) == 0 {
		return nil, false
	}
	return ids, true
}

// getInstanceDetails describes EC2 instances and converts to InstanceDetails.
func (s *ServiceImpl) getInstanceDetails(ctx context.Context, instanceIDs []string) ([]InstanceDetails, bool) {
	if s.ec2Client == nil || len(instanceIDs) == 0 {
		return nil, false
	}
	out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*ec2.DescribeInstancesOutput, error) {
		return s.ec2Client.DescribeInstances(rc, &ec2.DescribeInstancesInput{InstanceIds: instanceIDs})
	})
	if err != nil {
		s.logger.Warn("failed to describe instances", "error", err)
		return nil, false
	}
	var results []InstanceDetails
	for _, res := range out.Reservations {
		for _, inst := range res.Instances {
			lifecycle := "on-demand"
			if inst.InstanceLifecycle != "" {
				lifecycle = string(inst.InstanceLifecycle)
			}
			state := ""
			if inst.State != nil {
				state = string(inst.State.Name)
			}
			az := ""
			if inst.Placement != nil && inst.Placement.AvailabilityZone != nil {
				az = *inst.Placement.AvailabilityZone
			}
			results = append(results, InstanceDetails{
				InstanceID:   aws.ToString(inst.InstanceId),
				InstanceType: string(inst.InstanceType),
				LaunchTime:   aws.ToTime(inst.LaunchTime),
				Lifecycle:    lifecycle,
				State:        state,
				AZ:           az,
			})
		}
	}
	return results, len(results) > 0
}

// analyzeWorkloads summarizes pods running on nodegroup nodes and PDB posture.
func (s *ServiceImpl) analyzeWorkloads(ctx context.Context, clusterName, nodegroupName string) (WorkloadInfo, bool) {
	k8s, err := health.GetKubernetesClient()
	if err != nil || k8s == nil {
		return WorkloadInfo{}, false
	}
	instanceIDs, ok := s.getNodegroupInstanceIDs(ctx, clusterName, nodegroupName)
	if !ok || len(instanceIDs) == 0 {
		return WorkloadInfo{}, false
	}
	idSet := make(map[string]struct{}, len(instanceIDs))
	for _, id := range instanceIDs {
		idSet[id] = struct{}{}
	}

	nodes, err := k8s.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil || len(nodes.Items) == 0 {
		return WorkloadInfo{}, false
	}
	nodeOnNg := make(map[string]bool)
	for _, n := range nodes.Items {
		if n.Spec.ProviderID == "" {
			continue
		}
		if iid := extractInstanceIDFromProviderID(n.Spec.ProviderID); iid != "" {
			if _, exists := idSet[iid]; exists {
				nodeOnNg[n.Name] = true
			}
		}
	}
	if len(nodeOnNg) == 0 {
		selector := fmt.Sprintf("eks.amazonaws.com/nodegroup=%s", nodegroupName)
		if labeled, lerr := k8s.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: selector}); lerr == nil {
			for _, n := range labeled.Items {
				nodeOnNg[n.Name] = true
			}
		}
		if len(nodeOnNg) == 0 {
			return WorkloadInfo{}, false
		}
	}

	pods, err := k8s.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return WorkloadInfo{}, false
	}
	total, critical := 0, 0
	for _, p := range pods.Items {
		if nodeOnNg[p.Spec.NodeName] {
			if p.Status.Phase == corev1.PodSucceeded {
				continue
			}
			total++
			if p.Namespace == "kube-system" {
				critical++
			}
		}
	}

	pdbs, _ := k8s.PolicyV1().PodDisruptionBudgets("").List(ctx, metav1.ListOptions{})
	pdbStatus := "Unknown"
	if pdbs != nil {
		pdbStatus = fmt.Sprintf("%d PDBs observed", len(pdbs.Items))
	}
	return WorkloadInfo{TotalPods: total, CriticalPods: critical, PodDisruption: pdbStatus}, true
}

func extractInstanceIDFromProviderID(providerID string) string {
	for i := len(providerID) - 1; i >= 0; i-- {
		if providerID[i] == '/' {
			return providerID[i+1:]
		}
	}
	return ""
}
