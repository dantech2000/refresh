package aws

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
)

// ──────────────────────────────────────────────────────────────────────────────
// AMICache
// ──────────────────────────────────────────────────────────────────────────────

func TestNewAMICache_Empty(t *testing.T) {
	c := NewAMICache()
	if _, ok := c.Get("anything"); ok {
		t.Error("new cache should be empty")
	}
}

func TestAMICache_SetAndGet(t *testing.T) {
	c := NewAMICache()
	c.Set("al2023-amd64", "ami-12345678")
	val, ok := c.Get("al2023-amd64")
	if !ok {
		t.Error("Get after Set should return true")
	}
	if val != "ami-12345678" {
		t.Errorf("Get = %q, want %q", val, "ami-12345678")
	}
}

func TestAMICache_MissReturnsFalse(t *testing.T) {
	c := NewAMICache()
	c.Set("k1", "v1")
	_, ok := c.Get("k2")
	if ok {
		t.Error("Get for missing key should return false")
	}
}

func TestAMICache_Overwrite(t *testing.T) {
	c := NewAMICache()
	c.Set("key", "old")
	c.Set("key", "new")
	val, _ := c.Get("key")
	if val != "new" {
		t.Errorf("overwritten value = %q, want %q", val, "new")
	}
}

func TestAMICache_ConcurrentAccess(t *testing.T) {
	c := NewAMICache()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := "key"
			c.Set(key, "value")
			_, _ = c.Get(key)
			_ = n
		}(i)
	}
	wg.Wait()
}

// ──────────────────────────────────────────────────────────────────────────────
// buildSSMParameterPath
// ──────────────────────────────────────────────────────────────────────────────

func TestBuildSSMParameterPath_AL2X8664(t *testing.T) {
	path := buildSSMParameterPath("1.29", types.AMITypesAl2X8664)
	want := "/aws/service/eks/optimized-ami/1.29/amazon-linux-2/recommended/image_id"
	if path != want {
		t.Errorf("got %q, want %q", path, want)
	}
}

func TestBuildSSMParameterPath_AL2Arm64(t *testing.T) {
	path := buildSSMParameterPath("1.29", types.AMITypesAl2Arm64)
	if !strings.Contains(path, "amazon-linux-2-arm64") {
		t.Errorf("expected arm64 path, got %q", path)
	}
}

func TestBuildSSMParameterPath_AL2GPU(t *testing.T) {
	path := buildSSMParameterPath("1.29", types.AMITypesAl2X8664Gpu)
	if !strings.Contains(path, "amazon-linux-2-gpu") {
		t.Errorf("expected GPU path, got %q", path)
	}
}

func TestBuildSSMParameterPath_AL2023X8664(t *testing.T) {
	path := buildSSMParameterPath("1.29", types.AMITypesAl2023X8664Standard)
	if !strings.Contains(path, "amazon-linux-2023") || !strings.Contains(path, "x86_64") {
		t.Errorf("expected AL2023 x86_64 path, got %q", path)
	}
}

func TestBuildSSMParameterPath_AL2023Arm64(t *testing.T) {
	path := buildSSMParameterPath("1.29", types.AMITypesAl2023Arm64Standard)
	if !strings.Contains(path, "amazon-linux-2023") || !strings.Contains(path, "arm64") {
		t.Errorf("expected AL2023 arm64 path, got %q", path)
	}
}

func TestBuildSSMParameterPath_BottlerocketX8664(t *testing.T) {
	path := buildSSMParameterPath("1.29", types.AMITypesBottlerocketX8664)
	if !strings.Contains(path, "bottlerocket") || !strings.Contains(path, "x86_64") {
		t.Errorf("expected bottlerocket x86_64 path, got %q", path)
	}
}

func TestBuildSSMParameterPath_BottlerocketArm64(t *testing.T) {
	path := buildSSMParameterPath("1.29", types.AMITypesBottlerocketArm64)
	if !strings.Contains(path, "bottlerocket") || !strings.Contains(path, "arm64") {
		t.Errorf("expected bottlerocket arm64 path, got %q", path)
	}
}

func TestBuildSSMParameterPath_WindowsFull2019(t *testing.T) {
	path := buildSSMParameterPath("1.29", types.AMITypesWindowsFull2019X8664)
	if !strings.Contains(path, "windows") || !strings.Contains(path, "2019") {
		t.Errorf("expected windows 2019 full path, got %q", path)
	}
}

func TestBuildSSMParameterPath_WindowsFull2022(t *testing.T) {
	path := buildSSMParameterPath("1.29", types.AMITypesWindowsFull2022X8664)
	if !strings.Contains(path, "windows") || !strings.Contains(path, "2022") {
		t.Errorf("expected windows 2022 path, got %q", path)
	}
}

func TestBuildSSMParameterPath_RemainingExplicitTypes(t *testing.T) {
	cases := map[types.AMITypes]string{
		types.AMITypesAl2023X8664Nvidia:        "nvidia",
		types.AMITypesAl2023X8664Neuron:        "neuron",
		types.AMITypesAl2023Arm64Nvidia:        "arm64/nvidia",
		types.AMITypesBottlerocketX8664Nvidia:  "x86_64/nvidia",
		types.AMITypesBottlerocketArm64Nvidia:  "arm64/nvidia",
		types.AMITypesWindowsCore2019X8664:     "windows-2019-core",
		types.AMITypesWindowsCore2022X8664:     "windows-2022-core",
	}
	for amiType, want := range cases {
		path := buildSSMParameterPath("1.30", amiType)
		if !strings.Contains(path, want) {
			t.Fatalf("buildSSMParameterPath(%s) = %q, want substring %q", amiType, path, want)
		}
	}
}

func TestCurrentAmiIDEmptyNodegroupPaths(t *testing.T) {
	if got := CurrentAmiID(context.Background(), &types.Nodegroup{}, nil, nil); got != "" {
		t.Fatalf("CurrentAmiID empty nodegroup = %q, want empty", got)
	}
	if got := CurrentAmiID(context.Background(), &types.Nodegroup{
		LaunchTemplate: &types.LaunchTemplateSpecification{},
	}, nil, nil); got != "" {
		t.Fatalf("CurrentAmiID incomplete launch template = %q, want empty", got)
	}
	if got := CurrentAmiID(context.Background(), &types.Nodegroup{
		Resources: &types.NodegroupResources{AutoScalingGroups: []types.AutoScalingGroup{{}}},
	}, nil, nil); got != "" {
		t.Fatalf("CurrentAmiID incomplete ASG = %q, want empty", got)
	}
}

func TestLatestAmiIDForCustomSkipsSSM(t *testing.T) {
	if got := LatestAmiIDForType(context.Background(), nil, "1.30", types.AMITypesCustom); got != "" {
		t.Fatalf("LatestAmiIDForType custom = %q, want empty", got)
	}
}

func TestNewAMIResolverStoresClients(t *testing.T) {
	resolver := NewAMIResolver(nil, nil, nil)
	if resolver == nil || resolver.cache == nil {
		t.Fatalf("resolver = %+v", resolver)
	}
	resolver.cache.Set("k", "ami")
	if got, ok := resolver.cache.Get("k"); !ok || got != "ami" {
		t.Fatalf("resolver cache = %q, %v", got, ok)
	}
}

func TestBuildSSMParameterPathUnknownUsesInference(t *testing.T) {
	path := buildSSMParameterPath("1.30", types.AMITypes("AL2023_ARM64_CUSTOMISH"))
	if !strings.Contains(path, "amazon-linux-2023/arm64") {
		t.Fatalf("unknown AL2023 arm path = %q", path)
	}
}

var _ = aws.String

func TestBuildSSMParameterPath_CustomReturnsEmpty(t *testing.T) {
	path := buildSSMParameterPath("1.29", types.AMITypesCustom)
	if path != "" {
		t.Errorf("expected empty path for custom AMI, got %q", path)
	}
}

func TestBuildSSMParameterPath_ContainsK8sVersion(t *testing.T) {
	for _, version := range []string{"1.27", "1.28", "1.29", "1.30"} {
		path := buildSSMParameterPath(version, types.AMITypesAl2X8664)
		if !strings.Contains(path, version) {
			t.Errorf("path %q does not contain k8s version %s", path, version)
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// inferSSMPath
// ──────────────────────────────────────────────────────────────────────────────

func TestInferSSMPath_AL2023X8664(t *testing.T) {
	path := inferSSMPath("/base", "AL2023_X86_64_STANDARD")
	if !strings.Contains(path, "amazon-linux-2023") || !strings.Contains(path, "x86_64") {
		t.Errorf("unexpected path: %q", path)
	}
}

func TestInferSSMPath_AL2023Arm64(t *testing.T) {
	path := inferSSMPath("/base", "AL2023_ARM_64_STANDARD")
	if !strings.Contains(path, "amazon-linux-2023") || !strings.Contains(path, "arm64") {
		t.Errorf("unexpected path: %q", path)
	}
}

func TestInferSSMPath_BottlerocketX8664(t *testing.T) {
	path := inferSSMPath("/base", "BOTTLEROCKET_X86_64")
	if !strings.Contains(path, "bottlerocket") || !strings.Contains(path, "x86_64") {
		t.Errorf("unexpected path: %q", path)
	}
}

func TestInferSSMPath_BottlerocketArm64(t *testing.T) {
	path := inferSSMPath("/base", "BOTTLEROCKET_ARM_64")
	if !strings.Contains(path, "bottlerocket") || !strings.Contains(path, "arm64") {
		t.Errorf("unexpected path: %q", path)
	}
}

func TestInferSSMPath_UnknownFallsBackToAL2(t *testing.T) {
	path := inferSSMPath("/base", "UNKNOWN_TYPE")
	if !strings.Contains(path, "amazon-linux-2") {
		t.Errorf("unknown type should fall back to AL2, got %q", path)
	}
}
