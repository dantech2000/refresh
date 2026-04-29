package commands

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/services/nodegroup"
	refreshTypes "github.com/dantech2000/refresh/internal/types"
)

func captureCommandStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = original })

	callErr := fn()
	_ = w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String(), callErr
}

func sampleClusterSummaries() []cluster.ClusterSummary {
	return []cluster.ClusterSummary{
		{
			Name:    "prod",
			Status:  "ACTIVE",
			Version: "1.30",
			Region:  "us-east-1",
			Health:  &health.HealthSummary{Decision: health.DecisionProceed},
			NodeCount: cluster.NodeCountInfo{
				Ready: 3,
				Total: 3,
			},
		},
		{
			Name:      "stage",
			Status:    "UPDATING",
			Version:   "1.29",
			Region:    "",
			Health:    &health.HealthSummary{Decision: health.DecisionBlock},
			NodeCount: cluster.NodeCountInfo{Ready: 0, Total: 2},
		},
	}
}

func TestClusterListOutputs(t *testing.T) {
	for _, fn := range []func([]cluster.ClusterSummary) error{
		outputClustersJSON,
		outputClustersYAML,
	} {
		out, err := captureCommandStdout(t, func() error { return fn(sampleClusterSummaries()) })
		if err != nil {
			t.Fatalf("output error: %v", err)
		}
		if !strings.Contains(out, "prod") || !strings.Contains(out, "count") {
			t.Fatalf("output = %q", out)
		}
	}

	for _, tc := range []struct {
		name        string
		multiRegion bool
		showHealth  bool
		items       []cluster.ClusterSummary
	}{
		{"empty", false, false, nil},
		{"single-no-health", false, false, sampleClusterSummaries()[:1]},
		{"single-health", false, true, sampleClusterSummaries()},
		{"multi-health", true, true, sampleClusterSummaries()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := captureCommandStdout(t, func() error {
				return outputClustersTable(tc.items, time.Second, tc.multiRegion, tc.showHealth)
			})
			if err != nil {
				t.Fatalf("table error: %v", err)
			}
			_, err = captureCommandStdout(t, func() error {
				return outputClustersTree(tc.items, time.Second, tc.multiRegion, tc.showHealth)
			})
			if err != nil {
				t.Fatalf("tree error: %v", err)
			}
		})
	}
}

func TestClusterDescribeOutputs(t *testing.T) {
	details := &cluster.ClusterDetails{
		Name:            "prod",
		Status:          "ACTIVE",
		Version:         "1.30",
		PlatformVersion: "eks.1",
		Endpoint:        strings.Repeat("x", 140),
		CreatedAt:       time.Now().Add(-48 * time.Hour),
		Health:          &health.HealthSummary{Decision: health.DecisionWarn},
		Networking: cluster.NetworkingInfo{
			VpcId:            "vpc-1",
			VpcCidr:          "10.0.0.0/16",
			SubnetIds:        []string{"subnet-1"},
			SecurityGroupIds: []string{"sg-1"},
		},
		Security: cluster.SecurityInfo{
			EncryptionEnabled:  true,
			LoggingEnabled:     []string{"api"},
			DeletionProtection: true,
		},
		Addons: []cluster.AddonInfo{{Name: "vpc-cni", Version: "v1", Status: "ACTIVE"}},
		Nodegroups: []cluster.NodegroupSummary{{
			Name:       "ng",
			Status:     "ACTIVE",
			ReadyNodes: 2,
		}},
	}

	for _, fn := range []func(*cluster.ClusterDetails) error{
		outputClusterDetailsJSON,
		outputClusterDetailsYAML,
	} {
		out, err := captureCommandStdout(t, func() error { return fn(details) })
		if err != nil {
			t.Fatalf("output error: %v", err)
		}
		if !strings.Contains(out, "prod") {
			t.Fatalf("output = %q", out)
		}
	}

	out, err := captureCommandStdout(t, func() error { return outputClusterDetailsTable(details, time.Second) })
	if err != nil {
		t.Fatalf("table error: %v", err)
	}
	if !strings.Contains(out, "Cluster Information") {
		t.Fatalf("table output = %q", out)
	}
}

func TestComparisonOutputs(t *testing.T) {
	comparison := &cluster.ClusterComparison{
		Clusters: []cluster.ClusterDetails{
			{Name: "prod", Status: "ACTIVE", Version: "1.30", Health: &health.HealthSummary{Decision: health.DecisionProceed}},
			{Name: "stage", Status: "ACTIVE", Version: "1.29", Health: &health.HealthSummary{Decision: health.DecisionBlock}},
		},
		Differences: []cluster.Difference{
			{Field: "version", Severity: "critical", Description: "version differs", Values: []cluster.ValuePair{{ClusterName: "prod", Value: "1.30"}}},
			{Field: "logging", Severity: "warning", Description: "logging differs"},
			{Field: "tag", Severity: "info", Description: "tag differs"},
		},
		Summary: cluster.ComparisonSummary{
			TotalDifferences:    3,
			CriticalDifferences: 1,
			WarningDifferences:  1,
			InfoDifferences:     1,
		},
	}

	for _, fn := range []func(*cluster.ClusterComparison) error{
		outputComparisonJSON,
		outputComparisonYAML,
	} {
		out, err := captureCommandStdout(t, func() error { return fn(comparison) })
		if err != nil {
			t.Fatalf("output error: %v", err)
		}
		if !strings.Contains(out, "version") {
			t.Fatalf("output = %q", out)
		}
	}

	if _, err := captureCommandStdout(t, func() error { return outputComparisonTable(comparison, time.Second) }); err != nil {
		t.Fatalf("comparison table error: %v", err)
	}
	comparison.Differences = nil
	comparison.Summary = cluster.ComparisonSummary{ClustersAreEquivalent: true}
	if _, err := captureCommandStdout(t, func() error { return outputComparisonTable(comparison, time.Second) }); err != nil {
		t.Fatalf("identical comparison table error: %v", err)
	}
}

func TestAddonAndNodegroupTables(t *testing.T) {
	for _, status := range []ekstypes.AddonStatus{
		ekstypes.AddonStatusActive,
		ekstypes.AddonStatusDegraded,
		ekstypes.AddonStatusCreateFailed,
		ekstypes.AddonStatusCreating,
		ekstypes.AddonStatus("UNKNOWN"),
	} {
		_ = mapAddonHealth(status)
	}

	_, err := captureCommandStdout(t, func() error { return outputAddonsTable("prod", nil, time.Second) })
	if err != nil {
		t.Fatalf("empty addons table: %v", err)
	}
	_, err = captureCommandStdout(t, func() error {
		return outputAddonsTable("prod", []addonRow{{Name: "vpc-cni", Version: "v1", Status: "ACTIVE", Health: "PASS"}}, time.Second)
	})
	if err != nil {
		t.Fatalf("addons table: %v", err)
	}

	items := []nodegroup.NodegroupSummary{
		{Name: "ng-1", Status: "ACTIVE", InstanceType: "m5.large", ReadyNodes: 2, DesiredSize: 2, AMIStatus: refreshTypes.AMILatest, Metrics: nodegroup.SummaryMetrics{CPU: 25}, Cost: nodegroup.SummaryCost{Monthly: 42}},
		{Name: "ng-2", Status: "UPDATING", InstanceType: "m5.xlarge", ReadyNodes: 0, DesiredSize: 2, AMIStatus: refreshTypes.AMIUnknown},
	}
	_, err = captureCommandStdout(t, func() error {
		return outputNodegroupsTableWithWindow("prod", "24h", nil, time.Second, nodegroup.ListOptions{})
	})
	if err != nil {
		t.Fatalf("empty nodegroup table: %v", err)
	}
	_, err = captureCommandStdout(t, func() error {
		return outputNodegroupsTableWithWindow("prod", "24h", items, time.Second, nodegroup.ListOptions{ShowCosts: true, ShowUtilization: true})
	})
	if err != nil {
		t.Fatalf("nodegroup table: %v", err)
	}
}
