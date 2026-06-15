package clusterview

import (
	"strings"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/render"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
)

func sampleDetails() *clustersvc.ClusterDetails {
	return &clustersvc.ClusterDetails{
		Name:            "prod-west",
		Status:          "ACTIVE",
		Version:         "1.32",
		PlatformVersion: "eks.8",
		Endpoint:        "https://ABC123.gr7.us-west-2.eks.amazonaws.com",
		Region:          "us-west-2",
		Networking:      clustersvc.NetworkingInfo{VpcId: "vpc-0a1b", VpcCidr: "10.0.0.0/16"},
		Security:        clustersvc.SecurityInfo{EncryptionEnabled: true, LoggingEnabled: []string{"api", "audit"}},
		Nodegroups: []clustersvc.NodegroupSummary{
			{Name: "general", Status: "ACTIVE", InstanceType: "m6i.large", DesiredSize: 6, ReadyNodes: 6, ReadyKnown: true},
			{Name: "spot", Status: "DEGRADED", InstanceType: "m6i.xlarge", DesiredSize: 2, ReadyNodes: 1, ReadyKnown: true},
		},
		Addons: []clustersvc.AddonInfo{
			{Name: "vpc-cni", Version: "v1.18.3", Status: "ACTIVE", Health: "Healthy"},
		},
		Health: &health.HealthSummary{
			OverallScore: 82,
			Decision:     health.DecisionWarn,
			Warnings:     []string{"node balance skewed across AZs"},
		},
	}
}

func TestClusterDetailLines_HealthIssues(t *testing.T) {
	th := render.New(render.ColorNone, true)
	d := sampleDetails()
	d.HealthIssues = []clustersvc.HealthIssue{{
		Code:        "InternalFailure",
		Message:     "control plane could not assume the cluster IAM role",
		ResourceIds: []string{"arn:aws:iam::123456789012:role/eksClusterRole"},
	}}
	joined := strings.Join(clusterDetailLines(th, d, 0), "\n")
	for _, want := range []string{
		"▸ HEALTH ISSUES  1",
		"InternalFailure: control plane could not assume the cluster IAM role",
		"arn:aws:iam::123456789012:role/eksClusterRole",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("describe view missing %q in:\n%s", want, joined)
		}
	}

	// No issues → no HEALTH ISSUES section.
	clean := strings.Join(clusterDetailLines(th, sampleDetails(), 0), "\n")
	if strings.Contains(clean, "HEALTH ISSUES") {
		t.Errorf("clean cluster should not render a HEALTH ISSUES section:\n%s", clean)
	}
}

func TestClusterDetailLines_HealthChecksItemized(t *testing.T) {
	th := render.New(render.ColorNone, true)
	d := sampleDetails()
	d.Health = &health.HealthSummary{
		OverallScore: 75,
		Decision:     health.DecisionWarn,
		Results: []health.HealthResult{
			{Name: "Node Health", Status: health.StatusPass, Score: 100, Message: "all nodes Ready"},
			{Name: "Control Plane", Status: health.StatusWarn, Score: 70, Message: "etcd 82% of the 8 GiB limit"},
			{Name: "Service Quotas", Status: health.StatusPass, Skipped: true, Message: "headroom unavailable (clients not configured)"},
		},
	}
	joined := strings.Join(clusterDetailLines(th, d, 0), "\n")
	if strings.Contains(joined, "\x1b") {
		t.Fatalf("ColorNone health card contains ANSI escapes:\n%s", joined)
	}
	for _, want := range []string{
		"▸ HEALTH",
		"Node Health", "all nodes Ready",
		"Control Plane", "etcd 82% of the 8 GiB limit",
		"Service Quotas", "headroom unavailable", // skipped check is still listed
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("itemized health card missing %q in:\n%s", want, joined)
		}
	}
}

func TestClusterDetailLines_Pretty(t *testing.T) {
	th := render.New(render.ColorNone, true)
	joined := strings.Join(clusterDetailLines(th, sampleDetails(), 0), "\n")

	if strings.Contains(joined, "\x1b") {
		t.Fatalf("ColorNone detail output contains ANSI escapes:\n%s", joined)
	}
	for _, want := range []string{
		"prod-west   ● ACTIVE",             // header with status token
		"▸ OVERVIEW",                       // section header
		"1.32 · eks.8",                     // version KV with platform
		"encryption  ● enabled (KMS)",      // security token
		"▸ NODEGROUPS  1 active · 8 nodes", // section + meta: nodes = desired capacity (6+2); spot is DEGRADED
		"✗ DEGRADED",                       // spot's status cell
		"6/6",                              // measured nodes cell (ReadyKnown)
		"▸ ADD-ONS  1 installed",
		"vpc-cni",
		"▸ HEALTH  82/100 · ▲ WARN",      // health card verdict
		"node balance skewed across AZs", // health summary line
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("detail output missing %q in:\n%s", want, joined)
		}
	}
}

// REF-130: without measured readiness the per-nodegroup NODES cell shows the
// desired count only, and the section header still reports desired capacity.
func TestClusterDetailLines_ReadinessUnknown(t *testing.T) {
	th := render.New(render.ColorNone, true)
	d := sampleDetails()
	for i := range d.Nodegroups {
		d.Nodegroups[i].ReadyKnown = false
		d.Nodegroups[i].ReadyNodes = 0
	}
	joined := strings.Join(clusterDetailLines(th, d, 0), "\n")

	if !strings.Contains(joined, "1 active · 8 nodes") {
		t.Errorf("header should report desired capacity (8):\n%s", joined)
	}
	if strings.Contains(joined, "6/6") || strings.Contains(joined, "0/6") || strings.Contains(joined, "1/2") {
		t.Errorf("unmeasured readiness must not render a fraction:\n%s", joined)
	}
}

func TestClusterDetailLines_ASCIIAndMinimal(t *testing.T) {
	th := render.New(render.ColorNone, false)
	// Minimal cluster: no addons, no nodegroups, no health, zero CreatedAt.
	d := &clustersvc.ClusterDetails{Name: "bare", Status: "ACTIVE", Version: "1.30"}
	joined := strings.Join(clusterDetailLines(th, d, 0), "\n")
	if strings.Contains(joined, "NODEGROUPS") || strings.Contains(joined, "ADD-ONS") || strings.Contains(joined, "HEALTH") {
		t.Errorf("minimal cluster should omit empty sections:\n%s", joined)
	}
	if !strings.Contains(joined, "[OK] ACTIVE") { // ASCII status token
		t.Errorf("ASCII status token missing:\n%s", joined)
	}
	if strings.ContainsAny(joined, "●▲✗▸") {
		t.Errorf("ASCII fallback still contains Unicode glyphs:\n%s", joined)
	}
}

func TestClusterDetailLines_CreatedAge(t *testing.T) {
	th := render.New(render.ColorNone, true)
	d := sampleDetails()
	d.CreatedAt = time.Now().Add(-48 * time.Hour)
	joined := strings.Join(clusterDetailLines(th, d, 0), "\n")
	if !strings.Contains(joined, "created") {
		t.Errorf("created row missing:\n%s", joined)
	}
}
