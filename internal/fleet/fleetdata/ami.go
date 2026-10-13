package fleetdata

import (
	"strings"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
)

// AMIParametersAsOf is the date of the last check of the SSM parameter
// paths against AWS.
const AMIParametersAsOf = "2026-10-12"

// amiFamily is a family of EKS-optimized AMIs. Each family publishes its
// recommended AMI under its own tree of SSM parameters:
//
//   - Amazon Linux: /aws/service/eks/optimized-ami/<version>/<variant>/recommended/image_id
//   - Bottlerocket: /aws/service/bottlerocket/aws-k8s-<version>[-flavor]/<arch>/latest/image_id
//   - Windows: /aws/service/ami-windows-latest/Windows_Server-<release>-English-<Core|Full>-EKS_Optimized-<version>/image_id
type amiFamily int

const (
	amazonLinux amiFamily = iota + 1
	bottlerocket
	windows
)

// amiSpec says where the parameter of one AMI type is in the tree of its
// family.
type amiSpec struct {
	family amiFamily
	// variant is the Amazon Linux path segment, the Bottlerocket flavor
	// suffix, or the Windows release and option.
	variant string
	arch    string // Bottlerocket only
}

var amiSpecs = map[ekstypes.AMITypes]amiSpec{
	ekstypes.AMITypesAl2X8664:    {family: amazonLinux, variant: "amazon-linux-2"},
	ekstypes.AMITypesAl2Arm64:    {family: amazonLinux, variant: "amazon-linux-2-arm64"},
	ekstypes.AMITypesAl2X8664Gpu: {family: amazonLinux, variant: "amazon-linux-2-gpu"},

	ekstypes.AMITypesAl2023X8664Standard: {family: amazonLinux, variant: "amazon-linux-2023/x86_64/standard"},
	ekstypes.AMITypesAl2023Arm64Standard: {family: amazonLinux, variant: "amazon-linux-2023/arm64/standard"},
	ekstypes.AMITypesAl2023X8664Nvidia:   {family: amazonLinux, variant: "amazon-linux-2023/x86_64/nvidia"},
	ekstypes.AMITypesAl2023Arm64Nvidia:   {family: amazonLinux, variant: "amazon-linux-2023/arm64/nvidia"},
	ekstypes.AMITypesAl2023X8664Neuron:   {family: amazonLinux, variant: "amazon-linux-2023/x86_64/neuron"},

	ekstypes.AMITypesBottlerocketX8664:           {family: bottlerocket, arch: "x86_64"},
	ekstypes.AMITypesBottlerocketArm64:           {family: bottlerocket, arch: "arm64"},
	ekstypes.AMITypesBottlerocketX8664Nvidia:     {family: bottlerocket, variant: "-nvidia", arch: "x86_64"},
	ekstypes.AMITypesBottlerocketArm64Nvidia:     {family: bottlerocket, variant: "-nvidia", arch: "arm64"},
	ekstypes.AMITypesBottlerocketX8664Fips:       {family: bottlerocket, variant: "-fips", arch: "x86_64"},
	ekstypes.AMITypesBottlerocketArm64Fips:       {family: bottlerocket, variant: "-fips", arch: "arm64"},
	ekstypes.AMITypesBottlerocketX8664NvidiaFips: {family: bottlerocket, variant: "-nvidia-fips", arch: "x86_64"},
	ekstypes.AMITypesBottlerocketArm64NvidiaFips: {family: bottlerocket, variant: "-nvidia-fips", arch: "arm64"},

	ekstypes.AMITypesWindowsCore2019X8664: {family: windows, variant: "2019-English-Core"},
	ekstypes.AMITypesWindowsFull2019X8664: {family: windows, variant: "2019-English-Full"},
	ekstypes.AMITypesWindowsCore2022X8664: {family: windows, variant: "2022-English-Core"},
	ekstypes.AMITypesWindowsFull2022X8664: {family: windows, variant: "2022-English-Full"},
	ekstypes.AMITypesWindowsCore2025X8664: {family: windows, variant: "2025-English-Core"},
	ekstypes.AMITypesWindowsFull2025X8664: {family: windows, variant: "2025-English-Full"},
}

// AMIParameter returns the SSM parameter that holds the ID of the latest
// recommended AMI for an AMI type and a Kubernetes version such as "1.33".
// ok is false when there is none to look up: a CUSTOM type, whose AMI the
// user manages, and a type that this table cannot place.
//
// An AMI type that is newer than the table gets the base variant of its
// family when its name says the family. Any other type has no parameter:
// to compare a nodegroup with the AMI of another type would call a current
// nodegroup stale.
func AMIParameter(version, amiType string) (path string, ok bool) {
	if amiType == string(ekstypes.AMITypesCustom) || version == "" {
		return "", false
	}
	spec, known := amiSpecs[ekstypes.AMITypes(amiType)]
	if !known {
		if spec, known = inferSpec(amiType); !known {
			return "", false
		}
	}
	switch spec.family {
	case amazonLinux:
		return "/aws/service/eks/optimized-ami/" + version + "/" + spec.variant + "/recommended/image_id", true
	case bottlerocket:
		return "/aws/service/bottlerocket/aws-k8s-" + version + spec.variant + "/" + spec.arch + "/latest/image_id", true
	case windows:
		return "/aws/service/ami-windows-latest/Windows_Server-" + spec.variant + "-EKS_Optimized-" + version + "/image_id", true
	}
	return "", false
}

// AMITypes returns the AMI types that the table knows, for a check against
// the types that EKS offers.
func AMITypes() []string {
	out := make([]string, 0, len(amiSpecs))
	for t := range amiSpecs {
		out = append(out, string(t))
	}
	return out
}

// inferSpec guesses the place of an AMI type that the table does not have,
// from its name.
func inferSpec(amiType string) (amiSpec, bool) {
	name := strings.ToUpper(amiType)
	// EKS writes ARM_64; some older names have ARM64.
	arm := strings.Contains(name, "ARM")
	arch := "x86_64"
	if arm {
		arch = "arm64"
	}
	switch {
	case strings.Contains(name, "AL2023"):
		return amiSpec{family: amazonLinux, variant: "amazon-linux-2023/" + arch + "/standard"}, true
	case strings.Contains(name, "BOTTLEROCKET"):
		return amiSpec{family: bottlerocket, arch: arch}, true
	case strings.Contains(name, "AL2"):
		if arm {
			return amiSpec{family: amazonLinux, variant: "amazon-linux-2-arm64"}, true
		}
		return amiSpec{family: amazonLinux, variant: "amazon-linux-2"}, true
	}
	return amiSpec{}, false
}
