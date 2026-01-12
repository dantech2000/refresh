package addons

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
)

// Mock EKS client for testing
type mockEKSClient struct {
	addons map[string]*ekstypes.Addon
}

func (m *mockEKSClient) ListAddons(ctx context.Context, params *eks.ListAddonsInput, optFns ...func(*eks.Options)) (*eks.ListAddonsOutput, error) {
	var names []string
	for name := range m.addons {
		names = append(names, name)
	}
	return &eks.ListAddonsOutput{Addons: names}, nil
}

func (m *mockEKSClient) DescribeAddon(ctx context.Context, params *eks.DescribeAddonInput, optFns ...func(*eks.Options)) (*eks.DescribeAddonOutput, error) {
	addon, ok := m.addons[aws.ToString(params.AddonName)]
	if !ok {
		return nil, &ekstypes.ResourceNotFoundException{Message: aws.String("not found")}
	}
	return &eks.DescribeAddonOutput{Addon: addon}, nil
}

func (m *mockEKSClient) DescribeAddonVersions(ctx context.Context, params *eks.DescribeAddonVersionsInput, optFns ...func(*eks.Options)) (*eks.DescribeAddonVersionsOutput, error) {
	return &eks.DescribeAddonVersionsOutput{
		Addons: []ekstypes.AddonInfo{
			{
				AddonName: params.AddonName,
				AddonVersions: []ekstypes.AddonVersionInfo{
					{
						AddonVersion: aws.String("v1.15.0"),
						Compatibilities: []ekstypes.Compatibility{
							{ClusterVersion: aws.String("1.28")},
						},
					},
					{
						AddonVersion: aws.String("v1.14.0"),
						Compatibilities: []ekstypes.Compatibility{
							{ClusterVersion: aws.String("1.28")},
						},
					},
				},
			},
		},
	}, nil
}

func (m *mockEKSClient) UpdateAddon(ctx context.Context, params *eks.UpdateAddonInput, optFns ...func(*eks.Options)) (*eks.UpdateAddonOutput, error) {
	return &eks.UpdateAddonOutput{
		Update: &ekstypes.Update{
			Id:     aws.String("update-123"),
			Status: ekstypes.UpdateStatusInProgress,
		},
	}, nil
}

func (m *mockEKSClient) DescribeCluster(ctx context.Context, params *eks.DescribeClusterInput, optFns ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
	return &eks.DescribeClusterOutput{
		Cluster: &ekstypes.Cluster{
			Name:    params.Name,
			Version: aws.String("1.28"),
		},
	}, nil
}

func TestNewService(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client := &mockEKSClient{}

	svc := NewService(client, logger)

	if svc == nil {
		t.Fatal("Expected non-nil service")
	}
	if svc.eksClient != client {
		t.Error("EKS client not set correctly")
	}
	if svc.logger != logger {
		t.Error("Logger not set correctly")
	}
}

func TestList(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	client := &mockEKSClient{
		addons: map[string]*ekstypes.Addon{
			"vpc-cni": {
				AddonName:    aws.String("vpc-cni"),
				AddonVersion: aws.String("v1.14.0"),
				Status:       ekstypes.AddonStatusActive,
			},
			"coredns": {
				AddonName:    aws.String("coredns"),
				AddonVersion: aws.String("v1.10.1"),
				Status:       ekstypes.AddonStatusActive,
			},
		},
	}

	svc := NewService(client, logger)
	ctx := context.Background()

	summaries, err := svc.List(ctx, "test-cluster", ListOptions{ShowHealth: true})
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if len(summaries) != 2 {
		t.Errorf("Expected 2 addons, got %d", len(summaries))
	}
}

func TestDescribe(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	client := &mockEKSClient{
		addons: map[string]*ekstypes.Addon{
			"vpc-cni": {
				AddonName:    aws.String("vpc-cni"),
				AddonVersion: aws.String("v1.14.0"),
				AddonArn:     aws.String("arn:aws:eks:us-east-1:123456789:addon/cluster/vpc-cni/xxx"),
				Status:       ekstypes.AddonStatusActive,
				CreatedAt:    aws.Time(time.Now().Add(-24 * time.Hour)),
				ModifiedAt:   aws.Time(time.Now()),
			},
		},
	}

	svc := NewService(client, logger)
	ctx := context.Background()

	details, err := svc.Describe(ctx, "test-cluster", "vpc-cni", DescribeOptions{ShowVersions: true})
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if details.Name != "vpc-cni" {
		t.Errorf("Name = %s, want vpc-cni", details.Name)
	}
	if details.Version != "v1.14.0" {
		t.Errorf("Version = %s, want v1.14.0", details.Version)
	}
	if details.Health != "PASS" {
		t.Errorf("Health = %s, want PASS", details.Health)
	}
}

func TestUpdate(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	client := &mockEKSClient{
		addons: map[string]*ekstypes.Addon{
			"vpc-cni": {
				AddonName:    aws.String("vpc-cni"),
				AddonVersion: aws.String("v1.14.0"),
				Status:       ekstypes.AddonStatusActive,
			},
		},
	}

	svc := NewService(client, logger)
	ctx := context.Background()

	// Test dry run
	result, err := svc.Update(ctx, "test-cluster", "vpc-cni", UpdateOptions{
		Version: "v1.15.0",
		DryRun:  true,
	})
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result.Status != "DRY_RUN" {
		t.Errorf("Status = %s, want DRY_RUN", result.Status)
	}

	// Test actual update
	result, err = svc.Update(ctx, "test-cluster", "vpc-cni", UpdateOptions{
		Version: "v1.15.0",
		DryRun:  false,
	})
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result.UpdateID == "" {
		t.Error("Expected non-empty UpdateID")
	}
}

func TestUpdateLatest(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	client := &mockEKSClient{
		addons: map[string]*ekstypes.Addon{
			"vpc-cni": {
				AddonName:    aws.String("vpc-cni"),
				AddonVersion: aws.String("v1.14.0"),
				Status:       ekstypes.AddonStatusActive,
			},
		},
	}

	svc := NewService(client, logger)
	ctx := context.Background()

	result, err := svc.Update(ctx, "test-cluster", "vpc-cni", UpdateOptions{
		Version: "latest",
		DryRun:  true,
	})
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result.NewVersion != "v1.15.0" {
		t.Errorf("NewVersion = %s, want v1.15.0", result.NewVersion)
	}
}

func TestGetAvailableVersions(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	client := &mockEKSClient{}

	svc := NewService(client, logger)
	ctx := context.Background()

	versions, err := svc.GetAvailableVersions(ctx, "vpc-cni", "1.28")
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if len(versions) != 2 {
		t.Errorf("Expected 2 versions, got %d", len(versions))
	}
	if versions[0].Version != "v1.15.0" {
		t.Errorf("First version = %s, want v1.15.0", versions[0].Version)
	}
}

func TestMapAddonHealth(t *testing.T) {
	tests := []struct {
		status   ekstypes.AddonStatus
		expected string
	}{
		{ekstypes.AddonStatusActive, "PASS"},
		{ekstypes.AddonStatusDegraded, "FAIL"},
		{ekstypes.AddonStatusCreateFailed, "FAIL"},
		{ekstypes.AddonStatusCreating, "IN_PROGRESS"},
		{ekstypes.AddonStatusUpdating, "IN_PROGRESS"},
		{ekstypes.AddonStatus("UNKNOWN"), "UNKNOWN"},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			result := mapAddonHealth(tt.status)
			if result != tt.expected {
				t.Errorf("mapAddonHealth(%s) = %s, want %s", tt.status, result, tt.expected)
			}
		})
	}
}

func TestCountVersionsBehind(t *testing.T) {
	versions := []AddonVersionInfo{
		{Version: "v1.15.0"},
		{Version: "v1.14.0"},
		{Version: "v1.13.0"},
	}

	tests := []struct {
		current  string
		expected int
	}{
		{"v1.15.0", 0},
		{"v1.14.0", 1},
		{"v1.13.0", 2},
		{"v1.12.0", 3}, // Not in list
	}

	for _, tt := range tests {
		t.Run(tt.current, func(t *testing.T) {
			result := countVersionsBehind(tt.current, versions)
			if result != tt.expected {
				t.Errorf("countVersionsBehind(%s) = %d, want %d", tt.current, result, tt.expected)
			}
		})
	}
}

func TestFilterBySeverity(t *testing.T) {
	findings := []AddonSecurityFinding{
		{Severity: "critical"},
		{Severity: "high"},
		{Severity: "medium"},
		{Severity: "low"},
		{Severity: "info"},
	}

	tests := []struct {
		minSeverity   string
		expectedCount int
	}{
		{"critical", 1},
		{"high", 2},
		{"medium", 3},
		{"low", 4},
		{"info", 5},
	}

	for _, tt := range tests {
		t.Run(tt.minSeverity, func(t *testing.T) {
			result := filterBySeverity(findings, tt.minSeverity)
			if len(result) != tt.expectedCount {
				t.Errorf("filterBySeverity(%s) returned %d findings, want %d", tt.minSeverity, len(result), tt.expectedCount)
			}
		})
	}
}

func TestRequiresIRSA(t *testing.T) {
	tests := []struct {
		addonName string
		expected  bool
	}{
		{"vpc-cni", true},
		{"aws-ebs-csi-driver", true},
		{"coredns", false},
		{"kube-proxy", false},
	}

	for _, tt := range tests {
		t.Run(tt.addonName, func(t *testing.T) {
			result := requiresIRSA(tt.addonName)
			if result != tt.expected {
				t.Errorf("requiresIRSA(%s) = %v, want %v", tt.addonName, result, tt.expected)
			}
		})
	}
}

func TestSecurityScan(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	client := &mockEKSClient{
		addons: map[string]*ekstypes.Addon{
			"vpc-cni": {
				AddonName:    aws.String("vpc-cni"),
				AddonVersion: aws.String("v1.14.0"),
				Status:       ekstypes.AddonStatusActive,
			},
		},
	}

	svc := NewService(client, logger)
	ctx := context.Background()

	result, err := svc.SecurityScan(ctx, "test-cluster", SecurityScanOptions{
		CheckOutdated:          true,
		CheckVulnerabilities:   true,
		CheckMisconfigurations: true,
		MinSeverity:            "low",
	})
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if result.ClusterName != "test-cluster" {
		t.Errorf("ClusterName = %s, want test-cluster", result.ClusterName)
	}
	if result.Summary.TotalAddons != 1 {
		t.Errorf("TotalAddons = %d, want 1", result.Summary.TotalAddons)
	}
}

func TestAddonSummary(t *testing.T) {
	summary := AddonSummary{
		Name:    "vpc-cni",
		Version: "v1.14.0",
		Status:  "ACTIVE",
		Health:  "PASS",
	}

	if summary.Name != "vpc-cni" {
		t.Errorf("Name = %s, want vpc-cni", summary.Name)
	}
}

func TestAddonDetails(t *testing.T) {
	details := AddonDetails{
		Name:    "vpc-cni",
		Version: "v1.14.0",
		Status:  "ACTIVE",
		Health:  "PASS",
		ARN:     "arn:aws:eks:us-east-1:123456789:addon/cluster/vpc-cni/xxx",
	}

	if details.Name != "vpc-cni" {
		t.Errorf("Name = %s, want vpc-cni", details.Name)
	}
}

func TestSecuritySummary(t *testing.T) {
	summary := SecuritySummary{
		TotalAddons:   5,
		ScannedAddons: 5,
		CriticalCount: 1,
		HighCount:     2,
		MediumCount:   3,
		LowCount:      1,
		InfoCount:     0,
		OutdatedCount: 2,
	}

	if summary.TotalAddons != 5 {
		t.Errorf("TotalAddons = %d, want 5", summary.TotalAddons)
	}
	if summary.CriticalCount != 1 {
		t.Errorf("CriticalCount = %d, want 1", summary.CriticalCount)
	}
}

func TestAddonUpdateResult(t *testing.T) {
	result := AddonUpdateResult{
		AddonName:       "vpc-cni",
		PreviousVersion: "v1.14.0",
		NewVersion:      "v1.15.0",
		UpdateID:        "update-123",
		Status:          "IN_PROGRESS",
		StartedAt:       time.Now(),
	}

	if result.AddonName != "vpc-cni" {
		t.Errorf("AddonName = %s, want vpc-cni", result.AddonName)
	}
}
