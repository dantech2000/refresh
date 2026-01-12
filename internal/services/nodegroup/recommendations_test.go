package nodegroup

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
	nodegroups map[string]*ekstypes.Nodegroup
}

func (m *mockEKSClient) ListNodegroups(ctx context.Context, params *eks.ListNodegroupsInput, optFns ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
	var names []string
	for name := range m.nodegroups {
		names = append(names, name)
	}
	return &eks.ListNodegroupsOutput{Nodegroups: names}, nil
}

func (m *mockEKSClient) DescribeNodegroup(ctx context.Context, params *eks.DescribeNodegroupInput, optFns ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
	ng, ok := m.nodegroups[aws.ToString(params.NodegroupName)]
	if !ok {
		return nil, &ekstypes.ResourceNotFoundException{Message: aws.String("not found")}
	}
	return &eks.DescribeNodegroupOutput{Nodegroup: ng}, nil
}

func (m *mockEKSClient) DescribeCluster(ctx context.Context, params *eks.DescribeClusterInput, optFns ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
	return &eks.DescribeClusterOutput{
		Cluster: &ekstypes.Cluster{
			Name:    params.Name,
			Version: aws.String("1.28"),
		},
	}, nil
}

func (m *mockEKSClient) UpdateNodegroupConfig(ctx context.Context, params *eks.UpdateNodegroupConfigInput, optFns ...func(*eks.Options)) (*eks.UpdateNodegroupConfigOutput, error) {
	return &eks.UpdateNodegroupConfigOutput{}, nil
}

func TestSuggestSmallerInstance(t *testing.T) {
	tests := []struct {
		current  string
		expected string
	}{
		{"m5.2xlarge", "m5.xlarge"},
		{"m5.xlarge", "m5.large"},
		{"m5.large", "m5.medium"},
		{"t3.large", "t3.medium"},
		{"unknown", ""},
	}

	for _, tt := range tests {
		t.Run(tt.current, func(t *testing.T) {
			result := suggestSmallerInstance(tt.current)
			if result != tt.expected {
				t.Errorf("suggestSmallerInstance(%s) = %s, want %s", tt.current, result, tt.expected)
			}
		})
	}
}

func TestSuggestLargerInstance(t *testing.T) {
	tests := []struct {
		current  string
		expected string
	}{
		{"m5.large", "m5.xlarge"},
		{"m5.xlarge", "m5.2xlarge"},
		{"t3.medium", "t3.large"},
		{"unknown", ""},
	}

	for _, tt := range tests {
		t.Run(tt.current, func(t *testing.T) {
			result := suggestLargerInstance(tt.current)
			if result != tt.expected {
				t.Errorf("suggestLargerInstance(%s) = %s, want %s", tt.current, result, tt.expected)
			}
		})
	}
}

func TestSuggestCostEffectiveAlternative(t *testing.T) {
	tests := []struct {
		current  string
		expected string
	}{
		{"m5.large", "m6g.large"},
		{"c5.xlarge", "c6g.xlarge"},
		{"t3.medium", "t4g.medium"},
		{"unknown", ""},
	}

	for _, tt := range tests {
		t.Run(tt.current, func(t *testing.T) {
			result := suggestCostEffectiveAlternative(tt.current)
			if result != tt.expected {
				t.Errorf("suggestCostEffectiveAlternative(%s) = %s, want %s", tt.current, result, tt.expected)
			}
		})
	}
}

func TestEstimateRightSizingSavings(t *testing.T) {
	result := estimateRightSizingSavings(1000.0)
	if result != 400.0 {
		t.Errorf("estimateRightSizingSavings(1000) = %f, want 400", result)
	}
}

func TestEstimateArchitectureSavings(t *testing.T) {
	result := estimateArchitectureSavings(1000.0)
	if result != 200.0 {
		t.Errorf("estimateArchitectureSavings(1000) = %f, want 200", result)
	}
}

func TestPriorityOrder(t *testing.T) {
	tests := []struct {
		priority string
		expected int
	}{
		{"high", 0},
		{"medium", 1},
		{"low", 2},
		{"unknown", 3},
	}

	for _, tt := range tests {
		t.Run(tt.priority, func(t *testing.T) {
			result := priorityOrder(tt.priority)
			if result != tt.expected {
				t.Errorf("priorityOrder(%s) = %d, want %d", tt.priority, result, tt.expected)
			}
		})
	}
}

func TestAnalyzeRightSizing(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cache := NewCache()

	analyzer := &RecommendationsAnalyzer{
		logger: logger,
		cache:  cache,
	}

	// Test under-utilized nodegroup
	underUtilized := NodegroupAnalysisData{
		Name:         "test-ng",
		InstanceType: "m5.xlarge",
		DesiredSize:  3,
		CPUUtil:      UtilizationData{Average: 20, Peak: 40},
		HasCPUData:   true,
		MonthlyCost:  500.0,
		HasCostData:  true,
	}

	recs := analyzer.analyzeRightSizing(context.Background(), underUtilized)
	if len(recs) == 0 {
		t.Error("Expected recommendations for under-utilized nodegroup")
	}

	// Verify recommendation type
	foundRightSize := false
	for _, rec := range recs {
		if rec.Type == "right-size" {
			foundRightSize = true
			if rec.Priority != "high" {
				t.Errorf("Expected high priority for under-utilized, got %s", rec.Priority)
			}
		}
	}
	if !foundRightSize {
		t.Error("Expected right-size recommendation")
	}

	// Test over-utilized nodegroup
	overUtilized := NodegroupAnalysisData{
		Name:         "test-ng",
		InstanceType: "m5.large",
		DesiredSize:  3,
		CPUUtil:      UtilizationData{Average: 80, Peak: 95},
		HasCPUData:   true,
	}

	recs = analyzer.analyzeRightSizing(context.Background(), overUtilized)
	foundPerformance := false
	for _, rec := range recs {
		if rec.Type == "right-size" && rec.Impact == "performance" {
			foundPerformance = true
		}
	}
	if !foundPerformance {
		t.Error("Expected performance-related right-size recommendation for over-utilized nodegroup")
	}
}

func TestAnalyzeSpotOpportunities(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cache := NewCache()

	analyzer := &RecommendationsAnalyzer{
		logger: logger,
		cache:  cache,
	}

	// On-demand nodegroup should get spot recommendation
	onDemand := NodegroupAnalysisData{
		Name:         "test-ng",
		InstanceType: "m5.large",
		DesiredSize:  5,
		CapacityType: "ON_DEMAND",
		MonthlyCost:  1000.0,
		HasCostData:  true,
	}

	recs := analyzer.analyzeSpotOpportunities(context.Background(), onDemand, "test-cluster")
	foundSpot := false
	for _, rec := range recs {
		if rec.Type == "spot-integration" {
			foundSpot = true
		}
	}
	if !foundSpot {
		t.Error("Expected spot-integration recommendation for on-demand nodegroup")
	}

	// Spot nodegroup should NOT get spot recommendation
	spot := NodegroupAnalysisData{
		Name:         "test-ng",
		InstanceType: "m5.large",
		DesiredSize:  5,
		CapacityType: "SPOT",
	}

	recs = analyzer.analyzeSpotOpportunities(context.Background(), spot, "test-cluster")
	if len(recs) > 0 {
		t.Error("Should not recommend spot for already-spot nodegroup")
	}
}

func TestNodegroupAnalysisData(t *testing.T) {
	data := NodegroupAnalysisData{
		Name:         "test",
		InstanceType: "m5.large",
		DesiredSize:  3,
		MinSize:      1,
		MaxSize:      5,
		CapacityType: "ON_DEMAND",
	}

	if data.Name != "test" {
		t.Errorf("Expected Name 'test', got %s", data.Name)
	}
	if data.InstanceType != "m5.large" {
		t.Errorf("Expected InstanceType 'm5.large', got %s", data.InstanceType)
	}
}

func TestNewRecommendationsAnalyzer(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cache := NewCache()
	eksClient := &mockEKSClient{}

	analyzer := NewRecommendationsAnalyzer(eksClient, nil, nil, nil, nil, logger, cache)

	if analyzer == nil {
		t.Fatal("Expected non-nil analyzer")
	}
	if analyzer.eksClient != eksClient {
		t.Error("EKS client not set correctly")
	}
	if analyzer.logger != logger {
		t.Error("Logger not set correctly")
	}
	if analyzer.cache != cache {
		t.Error("Cache not set correctly")
	}
}

func TestAnalyzeNodegroupsEmpty(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cache := NewCache()
	eksClient := &mockEKSClient{nodegroups: make(map[string]*ekstypes.Nodegroup)}

	analyzer := NewRecommendationsAnalyzer(eksClient, nil, nil, nil, nil, logger, cache)

	ctx := context.Background()
	recs, err := analyzer.AnalyzeNodegroups(ctx, "test-cluster", RecommendationOptions{
		RightSizing: true,
	})

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("Expected 0 recommendations for empty cluster, got %d", len(recs))
	}
}

func TestAnalyzeNodegroupsWithData(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cache := NewCache()

	eksClient := &mockEKSClient{
		nodegroups: map[string]*ekstypes.Nodegroup{
			"test-ng": {
				NodegroupName: aws.String("test-ng"),
				InstanceTypes: []string{"m5.large"},
				CapacityType:  ekstypes.CapacityTypesOnDemand,
				Status:        ekstypes.NodegroupStatusActive,
				ScalingConfig: &ekstypes.NodegroupScalingConfig{
					DesiredSize: aws.Int32(3),
					MinSize:     aws.Int32(1),
					MaxSize:     aws.Int32(5),
				},
				Resources: &ekstypes.NodegroupResources{
					AutoScalingGroups: []ekstypes.AutoScalingGroup{
						{Name: aws.String("test-asg")},
					},
				},
			},
		},
	}

	analyzer := NewRecommendationsAnalyzer(eksClient, nil, nil, nil, nil, logger, cache)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	recs, err := analyzer.AnalyzeNodegroups(ctx, "test-cluster", RecommendationOptions{
		RightSizing:  true,
		SpotAnalysis: true,
	})

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	// Without utilization data, we might not get right-sizing recommendations
	// but we should get spot recommendations for on-demand nodegroups
	t.Logf("Got %d recommendations", len(recs))
}
