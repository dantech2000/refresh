package upgrade

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/mocks"
)

// versionsByK8s wires DescribeAddonVersions so the returned versions depend
// on the KubernetesVersion in the request — the heart of target-version
// compatibility.
func versionsByK8s(m *mocks.EKSAPI, addonName string, byK8s map[string][]string) {
	m.DescribeAddonVersionsFn = func(_ context.Context, in *eks.DescribeAddonVersionsInput, _ ...func(*eks.Options)) (*eks.DescribeAddonVersionsOutput, error) {
		if aws.ToString(in.AddonName) != addonName {
			return &eks.DescribeAddonVersionsOutput{}, nil
		}
		k8s := aws.ToString(in.KubernetesVersion)
		var infos []ekstypes.AddonVersionInfo
		for _, v := range byK8s[k8s] {
			infos = append(infos, ekstypes.AddonVersionInfo{
				AddonVersion:    aws.String(v),
				Compatibilities: []ekstypes.Compatibility{{ClusterVersion: aws.String(k8s)}},
			})
		}
		return &eks.DescribeAddonVersionsOutput{
			Addons: []ekstypes.AddonInfo{{AddonName: aws.String(addonName), AddonVersions: infos}},
		}, nil
	}
}

// Acceptance (REF-100): the chosen addon version is the one compatible with
// the NEW (target) control-plane version, not merely "latest for current".
func TestUpgradeAddons_ChoosesTargetCompatibleVersion(t *testing.T) {
	var mu sync.Mutex
	var captured *eks.UpdateAddonInput

	// Cluster's control plane has just been upgraded to 1.32 (the addon
	// phase runs after the control-plane step of the hop).
	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.32").
		WithAddon("vpc-cni", "v1.31.5-eksbuild.1", ekstypes.AddonStatusActive).
		Build()
	versionsByK8s(m, "vpc-cni", map[string][]string{
		"1.31": {"v1.31.9-eksbuild.1"},
		"1.32": {"v1.32.2-eksbuild.1"},
	})
	m.UpdateAddonFn = func(_ context.Context, in *eks.UpdateAddonInput, _ ...func(*eks.Options)) (*eks.UpdateAddonOutput, error) {
		mu.Lock()
		captured = in
		mu.Unlock()
		return &eks.UpdateAddonOutput{Update: &ekstypes.Update{
			Id:     aws.String("update-addon-1"),
			Status: ekstypes.UpdateStatusInProgress,
		}}, nil
	}
	svc := newTestService(m)

	if err := svc.UpgradeAddons(context.Background(), "prod-east", "1.32", nil, nil); err != nil {
		t.Fatalf("UpgradeAddons: %v", err)
	}

	if captured == nil {
		t.Fatal("UpdateAddon was never called")
	}
	if got := aws.ToString(captured.AddonVersion); got != "v1.32.2-eksbuild.1" {
		t.Fatalf("chosen addon version = %q, want v1.32.2-eksbuild.1 (compatible with target 1.32)", got)
	}
	if captured.ClientRequestToken == nil || *captured.ClientRequestToken == "" {
		t.Fatal("ClientRequestToken must be set on UpdateAddon")
	}
}

// Addons already at the latest target-compatible version are skipped.
func TestUpgradeAddons_AlreadyLatestSkipped(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.32").
		WithAddon("vpc-cni", "v1.32.2-eksbuild.1", ekstypes.AddonStatusActive).
		Build()
	versionsByK8s(m, "vpc-cni", map[string][]string{
		"1.32": {"v1.32.2-eksbuild.1"},
	})
	svc := newTestService(m)

	if err := svc.UpgradeAddons(context.Background(), "prod-east", "1.32", nil, nil); err != nil {
		t.Fatalf("UpgradeAddons: %v", err)
	}
	if m.Calls.UpdateAddon != 0 {
		t.Fatalf("UpdateAddon calls = %d, want 0", m.Calls.UpdateAddon)
	}
}

// Acceptance (REF-100): the --skip list passes through; skipped addons are
// never mutated.
func TestUpgradeAddons_SkipListRespected(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.32").
		WithAddon("vpc-cni", "v1.31.5-eksbuild.1", ekstypes.AddonStatusActive).
		Build()
	versionsByK8s(m, "vpc-cni", map[string][]string{
		"1.32": {"v1.32.2-eksbuild.1"},
	})
	svc := newTestService(m)

	if err := svc.UpgradeAddons(context.Background(), "prod-east", "1.32", []string{"vpc-cni"}, nil); err != nil {
		t.Fatalf("UpgradeAddons: %v", err)
	}
	if m.Calls.UpdateAddon != 0 {
		t.Fatalf("UpdateAddon calls = %d, want 0 (skipped)", m.Calls.UpdateAddon)
	}
}

// Updates run serially in dependency order: vpc-cni before coredns.
func TestUpgradeAddons_DependencyOrder(t *testing.T) {
	var mu sync.Mutex
	var order []string

	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.32").
		WithAddon("coredns", "v1.10.0-eksbuild.1", ekstypes.AddonStatusActive).
		WithAddon("vpc-cni", "v1.31.5-eksbuild.1", ekstypes.AddonStatusActive).
		Build()
	m.DescribeAddonVersionsFn = func(_ context.Context, in *eks.DescribeAddonVersionsInput, _ ...func(*eks.Options)) (*eks.DescribeAddonVersionsOutput, error) {
		name := aws.ToString(in.AddonName)
		v := map[string]string{"vpc-cni": "v1.32.2-eksbuild.1", "coredns": "v1.11.0-eksbuild.1"}[name]
		return &eks.DescribeAddonVersionsOutput{
			Addons: []ekstypes.AddonInfo{{
				AddonName:     in.AddonName,
				AddonVersions: []ekstypes.AddonVersionInfo{{AddonVersion: aws.String(v)}},
			}},
		}, nil
	}
	m.UpdateAddonFn = func(_ context.Context, in *eks.UpdateAddonInput, _ ...func(*eks.Options)) (*eks.UpdateAddonOutput, error) {
		mu.Lock()
		order = append(order, aws.ToString(in.AddonName))
		mu.Unlock()
		return &eks.UpdateAddonOutput{Update: &ekstypes.Update{
			Id:     aws.String("u-" + aws.ToString(in.AddonName)),
			Status: ekstypes.UpdateStatusInProgress,
		}}, nil
	}
	svc := newTestService(m)

	if err := svc.UpgradeAddons(context.Background(), "prod-east", "1.32", nil, nil); err != nil {
		t.Fatalf("UpgradeAddons: %v", err)
	}
	if len(order) != 2 || order[0] != "vpc-cni" || order[1] != "coredns" {
		t.Fatalf("update order = %v, want [vpc-cni coredns]", order)
	}
}

// A failed addon update halts the phase with the addon named.
func TestUpgradeAddons_FailureHaltsPhase(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.32").
		WithAddon("vpc-cni", "v1.31.5-eksbuild.1", ekstypes.AddonStatusActive).
		Build()
	versionsByK8s(m, "vpc-cni", map[string][]string{
		"1.32": {"v1.32.2-eksbuild.1"},
	})
	m.UpdateAddonFn = func(_ context.Context, _ *eks.UpdateAddonInput, _ ...func(*eks.Options)) (*eks.UpdateAddonOutput, error) {
		return nil, errors.New("addon update rejected")
	}
	svc := newTestService(m)

	err := svc.UpgradeAddons(context.Background(), "prod-east", "1.32", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "vpc-cni") {
		t.Fatalf("err = %v, want failure naming vpc-cni", err)
	}
}

// Resume support (REF-114): a re-run that finds an addon still UPDATING from a
// previous run attaches to that in-flight update (waits for ACTIVE) instead of
// hard-failing the pre-update health gate. Here the in-flight update settles at
// the target version, so no new UpdateAddon is submitted.
func TestUpgradeAddons_ResumeAttachesToInFlightUpdate(t *testing.T) {
	const chosen = "v1.32.2-eksbuild.1"
	var mu sync.Mutex
	describeCalls := 0

	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.32").
		WithAddon("vpc-cni", "v1.31.5-eksbuild.1", ekstypes.AddonStatusActive).
		Build()
	versionsByK8s(m, "vpc-cni", map[string][]string{"1.32": {chosen}})
	m.DescribeAddonFn = func(_ context.Context, in *eks.DescribeAddonInput, _ ...func(*eks.Options)) (*eks.DescribeAddonOutput, error) {
		mu.Lock()
		describeCalls++
		n := describeCalls
		mu.Unlock()
		// First two observations: still mid-update from the previous run.
		// Then it settles ACTIVE at the target version.
		status, version := ekstypes.AddonStatusActive, chosen
		if n <= 2 {
			status, version = ekstypes.AddonStatusUpdating, "v1.31.5-eksbuild.1"
		}
		return &eks.DescribeAddonOutput{Addon: &ekstypes.Addon{
			AddonName:    in.AddonName,
			AddonVersion: aws.String(version),
			Status:       status,
		}}, nil
	}
	svc := newTestService(m)

	if err := svc.UpgradeAddons(context.Background(), "prod-east", "1.32", nil, nil); err != nil {
		t.Fatalf("UpgradeAddons: %v", err)
	}
	if m.Calls.UpdateAddon != 0 {
		t.Fatalf("UpdateAddon calls = %d, want 0 (attached to in-flight update already at target)", m.Calls.UpdateAddon)
	}
	mu.Lock()
	got := describeCalls
	mu.Unlock()
	if got < 3 {
		t.Fatalf("DescribeAddon calls = %d, want >=3 (status read, poll until ACTIVE, re-read)", got)
	}
}

// Resume support (REF-114): if the attached-to in-flight update settles below
// the chosen target, the phase converges by issuing the update — and the
// pre-update health gate passes because the addon is ACTIVE again by then.
func TestUpgradeAddons_ResumeThenConverges(t *testing.T) {
	var mu sync.Mutex
	describeCalls := 0
	updated := false

	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.32").
		WithAddon("vpc-cni", "v1.31.5-eksbuild.1", ekstypes.AddonStatusActive).
		Build()
	versionsByK8s(m, "vpc-cni", map[string][]string{"1.32": {"v1.32.2-eksbuild.1"}})
	m.DescribeAddonFn = func(_ context.Context, in *eks.DescribeAddonInput, _ ...func(*eks.Options)) (*eks.DescribeAddonOutput, error) {
		mu.Lock()
		describeCalls++
		n := describeCalls
		mu.Unlock()
		status := ekstypes.AddonStatusUpdating
		if n >= 3 { // settles ACTIVE but still below target → must converge
			status = ekstypes.AddonStatusActive
		}
		return &eks.DescribeAddonOutput{Addon: &ekstypes.Addon{
			AddonName:    in.AddonName,
			AddonVersion: aws.String("v1.31.5-eksbuild.1"),
			Status:       status,
		}}, nil
	}
	m.UpdateAddonFn = func(_ context.Context, _ *eks.UpdateAddonInput, _ ...func(*eks.Options)) (*eks.UpdateAddonOutput, error) {
		mu.Lock()
		updated = true
		mu.Unlock()
		return &eks.UpdateAddonOutput{Update: &ekstypes.Update{
			Id:     aws.String("u-converge"),
			Status: ekstypes.UpdateStatusInProgress,
		}}, nil
	}
	svc := newTestService(m)

	if err := svc.UpgradeAddons(context.Background(), "prod-east", "1.32", nil, nil); err != nil {
		t.Fatalf("UpgradeAddons: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !updated {
		t.Fatal("expected UpdateAddon to be called to converge to target after attaching to the in-flight update")
	}
}

// An addon with no target-compatible version fails the phase up front.
func TestUpgradeAddons_NoCompatibleVersionFails(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("prod-east", "1.32").
		WithAddon("legacy-addon", "v0.9.0", ekstypes.AddonStatusActive).
		Build()
	svc := newTestService(m)

	err := svc.UpgradeAddons(context.Background(), "prod-east", "1.32", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "legacy-addon") {
		t.Fatalf("err = %v, want failure naming legacy-addon", err)
	}
	if m.Calls.UpdateAddon != 0 {
		t.Fatalf("UpdateAddon calls = %d, want 0", m.Calls.UpdateAddon)
	}
}
