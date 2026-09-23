// Package aws provides AWS SDK abstractions and utilities for EKS cluster management.
// It implements clean patterns for AMI resolution, cluster discovery, and error handling.
package aws

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

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

	instanceID := describeAsgOut.AutoScalingGroups[0].Instances[0].InstanceId
	if instanceID == nil {
		return ""
	}

	descInstOut, err := ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{*instanceID},
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

// LatestAmiIDForType returns the latest recommended AMI ID for a specific AMI type.
// It queries AWS SSM Parameter Store for the EKS-optimized AMI.
// Supports AL2, AL2023, Bottlerocket, and Windows AMI types.
//
// It returns ("", nil) when there is no recommended AMI to look up (custom or
// unrecognized AMI types), and a non-nil error when the SSM lookup itself
// fails (missing ssm:GetParameter permission, throttling, a missing
// parameter). Callers must not read a failed lookup as "no newer AMI".
func LatestAmiIDForType(ctx context.Context, ssmClient *ssm.Client, k8sVersion string, amiType types.AMITypes) (string, error) {
	ssmParam := buildSSMParameterPath(k8sVersion, amiType)
	if ssmParam == "" {
		return "", nil
	}

	ssmOut, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name: aws.String(ssmParam),
	})
	if err != nil {
		return "", fmt.Errorf("reading SSM parameter %s: %w", ssmParam, err)
	}
	if ssmOut == nil || ssmOut.Parameter == nil || aws.ToString(ssmOut.Parameter.Value) == "" {
		return "", fmt.Errorf("reading SSM parameter %s: empty value", ssmParam)
	}

	return *ssmOut.Parameter.Value, nil
}

// LatestReleaseVersionForType returns the latest recommended AMI *release
// version* (e.g. "1.31.0-20260601") for an AMI type — the human-meaningful
// counterpart of LatestAmiIDForType, used for changelog/version-delta display.
// For Bottlerocket this is the image_version parameter (e.g. "1.20.3-5d9ac849"),
// which matches the nodegroup's releaseVersion. Returns "" for custom and
// Windows AMIs (AWS publishes no release-version parameter for Windows) or
// when the SSM parameter is unavailable.
func LatestReleaseVersionForType(ctx context.Context, ssmClient *ssm.Client, k8sVersion string, amiType types.AMITypes) string {
	relPath := buildReleaseVersionParameterPath(k8sVersion, amiType)
	if relPath == "" {
		return ""
	}

	out, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(relPath)})
	if err != nil || out.Parameter == nil || out.Parameter.Value == nil {
		return ""
	}
	return *out.Parameter.Value
}

// NodegroupK8sVersion returns the Kubernetes minor a managed nodegroup runs
// (ng.Version), falling back to clusterVersion when the nodegroup doesn't
// report one. Latest-AMI lookups must use this, not the cluster version: an
// AMI patch (nodegroup.StartVersionUpdate) pins the nodegroup's current minor,
// so between a control-plane upgrade and the nodegroup upgrade the
// recommended AMI for the cluster's minor is never reachable.
func NodegroupK8sVersion(ng *types.Nodegroup, clusterVersion string) string {
	if ng != nil {
		if v := aws.ToString(ng.Version); v != "" {
			return v
		}
	}
	return clusterVersion
}

// AMILookupFunc resolves an AMI attribute (image ID or release version) for a
// Kubernetes version and AMI type. It returns ("", nil) when there is nothing
// to look up and a non-nil error when the lookup failed.
type AMILookupFunc func(ctx context.Context, k8sVersion string, amiType types.AMITypes) (string, error)

// LatestAMICache memoizes successful AMILookupFunc results per (k8sVersion,
// amiType). It is safe for concurrent use, and concurrent callers for the same
// key share one in-flight lookup, so a parallel fan-out over nodegroups costs
// one SSM call per distinct key.
//
// Failures are never memoized: callers that waited on a failed lookup get its
// error, and the next Get starts a fresh lookup. If a lookup failed only
// because its caller's ctx ended, waiters whose own ctx is still live retry
// it, so one caller's cancellation can't fail the others.
type LatestAMICache struct {
	lookup  AMILookupFunc
	mu      sync.Mutex
	entries map[amiCacheKey]*amiCacheEntry
}

type amiCacheKey struct {
	version string
	amiType types.AMITypes
}

// amiCacheEntry is one lookup, in flight until done is closed. value and err
// are written before done is closed and read only after it.
type amiCacheEntry struct {
	done  chan struct{}
	value string
	err   error
}

// NewLatestAMICache returns a cache backed by lookup.
func NewLatestAMICache(lookup AMILookupFunc) *LatestAMICache {
	return &LatestAMICache{lookup: lookup, entries: make(map[amiCacheKey]*amiCacheEntry)}
}

// NewLatestAMIIDCache returns a cache of LatestAmiIDForType lookups.
func NewLatestAMIIDCache(ssmClient *ssm.Client) *LatestAMICache {
	return NewLatestAMICache(func(ctx context.Context, v string, t types.AMITypes) (string, error) {
		return LatestAmiIDForType(ctx, ssmClient, v, t)
	})
}

// Get returns the lookup result for (k8sVersion, amiType). A successful
// result is shared by every later call; a failure is returned to the callers
// that waited on it and then forgotten.
func (c *LatestAMICache) Get(ctx context.Context, k8sVersion string, amiType types.AMITypes) (string, error) {
	key := amiCacheKey{version: k8sVersion, amiType: amiType}
	for {
		c.mu.Lock()
		e, ok := c.entries[key]
		if !ok {
			e = &amiCacheEntry{done: make(chan struct{})}
			c.entries[key] = e
			c.mu.Unlock()
			return c.run(ctx, key, e)
		}
		c.mu.Unlock()

		select {
		case <-e.done:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		if e.err == nil {
			return e.value, nil
		}
		// The lookup failed because its owner's ctx ended, but ours is still
		// live: look it up again instead of inheriting that cancellation.
		if isContextErr(e.err) && ctx.Err() == nil {
			continue
		}
		return "", e.err
	}
}

// run performs the lookup for an entry the caller just registered. A failed
// entry is removed before done is closed, so the next Get starts afresh.
func (c *LatestAMICache) run(ctx context.Context, key amiCacheKey, e *amiCacheEntry) (string, error) {
	defer close(e.done)
	e.value, e.err = c.lookup(ctx, key.version, key.amiType)
	if e.err != nil {
		c.mu.Lock()
		if c.entries[key] == e {
			delete(c.entries, key)
		}
		c.mu.Unlock()
	}
	return e.value, e.err
}

func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// ForNodegroup returns the lookup result for the nodegroup's own Kubernetes
// version (see NodegroupK8sVersion) and AMI type.
func (c *LatestAMICache) ForNodegroup(ctx context.Context, ng *types.Nodegroup, clusterVersion string) (string, error) {
	return c.Get(ctx, NodegroupK8sVersion(ng, clusterVersion), ng.AmiType)
}

// amiFamily is an EKS-optimized AMI family. Each family publishes its
// recommended-AMI SSM parameters under a different tree:
//
//   - Amazon Linux: /aws/service/eks/optimized-ami/<ver>/<ami-type>/recommended/{image_id,release_version}
//     https://docs.aws.amazon.com/eks/latest/userguide/retrieve-ami-id.html
//   - Bottlerocket: /aws/service/bottlerocket/aws-k8s-<ver>[-flavor]/<arch>/latest/{image_id,image_version}
//     https://docs.aws.amazon.com/eks/latest/userguide/retrieve-ami-id-bottlerocket.html
//   - Windows: /aws/service/ami-windows-latest/Windows_Server-<release>-English-<Core|Full>-EKS_Optimized-<ver>/image_id
//     https://docs.aws.amazon.com/eks/latest/userguide/retrieve-windows-ami-id.html
//     (no release-version parameter is published)
type amiFamily int

const (
	familyUnknown amiFamily = iota
	familyAmazonLinux
	familyBottlerocket
	familyWindows
)

// amiSSMSpec identifies one AMI type's parameter within its family's tree.
type amiSSMSpec struct {
	family amiFamily
	// variant is the Amazon Linux <ami-type> segment, the Bottlerocket
	// "-flavor" suffix (possibly empty), or the Windows "<release>-English-<option>".
	variant string
	arch    string // Bottlerocket only
}

var amiSSMSpecs = map[types.AMITypes]amiSSMSpec{
	// Amazon Linux 2
	types.AMITypesAl2X8664:    {family: familyAmazonLinux, variant: "amazon-linux-2"},
	types.AMITypesAl2Arm64:    {family: familyAmazonLinux, variant: "amazon-linux-2-arm64"},
	types.AMITypesAl2X8664Gpu: {family: familyAmazonLinux, variant: "amazon-linux-2-gpu"},

	// Amazon Linux 2023
	types.AMITypesAl2023X8664Standard: {family: familyAmazonLinux, variant: "amazon-linux-2023/x86_64/standard"},
	types.AMITypesAl2023Arm64Standard: {family: familyAmazonLinux, variant: "amazon-linux-2023/arm64/standard"},
	types.AMITypesAl2023X8664Nvidia:   {family: familyAmazonLinux, variant: "amazon-linux-2023/x86_64/nvidia"},
	types.AMITypesAl2023Arm64Nvidia:   {family: familyAmazonLinux, variant: "amazon-linux-2023/arm64/nvidia"},
	types.AMITypesAl2023X8664Neuron:   {family: familyAmazonLinux, variant: "amazon-linux-2023/x86_64/neuron"},

	// Bottlerocket
	types.AMITypesBottlerocketX8664:           {family: familyBottlerocket, arch: "x86_64"},
	types.AMITypesBottlerocketArm64:           {family: familyBottlerocket, arch: "arm64"},
	types.AMITypesBottlerocketX8664Nvidia:     {family: familyBottlerocket, variant: "-nvidia", arch: "x86_64"},
	types.AMITypesBottlerocketArm64Nvidia:     {family: familyBottlerocket, variant: "-nvidia", arch: "arm64"},
	types.AMITypesBottlerocketX8664Fips:       {family: familyBottlerocket, variant: "-fips", arch: "x86_64"},
	types.AMITypesBottlerocketArm64Fips:       {family: familyBottlerocket, variant: "-fips", arch: "arm64"},
	types.AMITypesBottlerocketX8664NvidiaFips: {family: familyBottlerocket, variant: "-nvidia-fips", arch: "x86_64"},
	types.AMITypesBottlerocketArm64NvidiaFips: {family: familyBottlerocket, variant: "-nvidia-fips", arch: "arm64"},

	// Windows
	types.AMITypesWindowsCore2019X8664: {family: familyWindows, variant: "2019-English-Core"},
	types.AMITypesWindowsFull2019X8664: {family: familyWindows, variant: "2019-English-Full"},
	types.AMITypesWindowsCore2022X8664: {family: familyWindows, variant: "2022-English-Core"},
	types.AMITypesWindowsFull2022X8664: {family: familyWindows, variant: "2022-English-Full"},
	types.AMITypesWindowsCore2025X8664: {family: familyWindows, variant: "2025-English-Core"},
	types.AMITypesWindowsFull2025X8664: {family: familyWindows, variant: "2025-English-Full"},
}

// buildSSMParameterPath returns the SSM parameter holding the latest
// recommended AMI ID for the AMI type, or "" when there is none to look up
// (custom or unrecognized AMI types).
func buildSSMParameterPath(k8sVersion string, amiType types.AMITypes) string {
	spec, ok := lookupAMISSMSpec(amiType)
	if !ok {
		return ""
	}
	switch spec.family {
	case familyAmazonLinux:
		return "/aws/service/eks/optimized-ami/" + k8sVersion + "/" + spec.variant + "/recommended/image_id"
	case familyBottlerocket:
		return "/aws/service/bottlerocket/aws-k8s-" + k8sVersion + spec.variant + "/" + spec.arch + "/latest/image_id"
	case familyWindows:
		return "/aws/service/ami-windows-latest/Windows_Server-" + spec.variant + "-EKS_Optimized-" + k8sVersion + "/image_id"
	default:
		return ""
	}
}

// buildReleaseVersionParameterPath returns the SSM parameter holding the
// latest recommended AMI release version for the AMI type, or "" when AWS
// publishes none (custom, unrecognized, and Windows AMI types).
func buildReleaseVersionParameterPath(k8sVersion string, amiType types.AMITypes) string {
	spec, ok := lookupAMISSMSpec(amiType)
	if !ok {
		return ""
	}
	img := buildSSMParameterPath(k8sVersion, amiType)
	switch spec.family {
	case familyAmazonLinux:
		return strings.TrimSuffix(img, "image_id") + "release_version"
	case familyBottlerocket:
		return strings.TrimSuffix(img, "image_id") + "image_version"
	default:
		return ""
	}
}

// Release-notes pages per AMI family.
const (
	amazonEKSAMIReleasesURL = "https://github.com/awslabs/amazon-eks-ami/releases"
	bottlerocketReleasesURL = "https://github.com/bottlerocket-os/bottlerocket/releases"
	windowsAMIReleasesURL   = "https://docs.aws.amazon.com/eks/latest/userguide/eks-ami-versions-windows.html"
)

// AMIReleaseNotes returns where an AMI type's release notes live. eksAMI is
// true only for the Amazon Linux families, whose releases are published by
// awslabs/amazon-eks-ami with a date-stamped release version (e.g.
// "1.31.0-20260601"). Bottlerocket and Windows version differently, so their
// versions must not be read as amazon-eks-ami dates. url is "" for custom and
// unrecognized AMI types.
func AMIReleaseNotes(amiType types.AMITypes) (url string, eksAMI bool) {
	spec, ok := lookupAMISSMSpec(amiType)
	if !ok {
		return "", false
	}
	switch spec.family {
	case familyAmazonLinux:
		return amazonEKSAMIReleasesURL, true
	case familyBottlerocket:
		return bottlerocketReleasesURL, false
	case familyWindows:
		return windowsAMIReleasesURL, false
	default:
		return "", false
	}
}

// lookupAMISSMSpec resolves an AMI type to its SSM spec, inferring one for
// AMI types newer than this SDK's enum. Custom AMIs have no recommended AMI.
func lookupAMISSMSpec(amiType types.AMITypes) (amiSSMSpec, bool) {
	if amiType == types.AMITypesCustom {
		return amiSSMSpec{}, false
	}
	if spec, ok := amiSSMSpecs[amiType]; ok {
		return spec, true
	}
	return inferAMISSMSpec(string(amiType))
}

// inferAMISSMSpec guesses the SSM spec from an AMI type string the SDK enum
// doesn't know yet. It only infers the base variant of each family; anything
// else resolves to "unknown" rather than compare against the wrong AMI.
func inferAMISSMSpec(amiTypeStr string) (amiSSMSpec, bool) {
	amiTypeStr = strings.ToUpper(amiTypeStr)

	// EKS AMI type strings use "ARM_64" (e.g. AL2023_ARM_64_STANDARD, BOTTLEROCKET_ARM_64);
	// check for "ARM" to match both "ARM64" and "ARM_64" variants.
	isArm := strings.Contains(amiTypeStr, "ARM")
	arch := "x86_64"
	if isArm {
		arch = "arm64"
	}

	switch {
	case strings.Contains(amiTypeStr, "AL2023"):
		return amiSSMSpec{family: familyAmazonLinux, variant: "amazon-linux-2023/" + arch + "/standard"}, true

	case strings.Contains(amiTypeStr, "BOTTLEROCKET"):
		return amiSSMSpec{family: familyBottlerocket, arch: arch}, true

	case strings.Contains(amiTypeStr, "AL2"):
		if isArm {
			return amiSSMSpec{family: familyAmazonLinux, variant: "amazon-linux-2-arm64"}, true
		}
		return amiSSMSpec{family: familyAmazonLinux, variant: "amazon-linux-2"}, true

	default:
		// Unrecognized AMI type (future families, custom strings): resolving
		// against the AL2 path would silently compare the wrong AMI — and the
		// AL2 parameter no longer exists for k8s >= 1.33. Report "unknown"
		// instead.
		return amiSSMSpec{}, false
	}
}
