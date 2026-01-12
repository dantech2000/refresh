package nodegroup

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"
)

func TestStaticPriceMap(t *testing.T) {
	// Test that common instance types have prices
	expectedTypes := []string{
		"m5.large", "m5.xlarge", "m5.2xlarge",
		"c5.large", "c5.xlarge",
		"t3.medium", "t3.large",
	}

	for _, instanceType := range expectedTypes {
		if _, exists := staticPriceMap[instanceType]; !exists {
			t.Errorf("Expected price for %s in staticPriceMap", instanceType)
		}
	}
}

func TestGetFallbackPrice(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cache := NewCache()
	analyzer := NewCostAnalyzer(nil, logger, cache, "us-east-1")

	tests := []struct {
		instanceType string
		expectOK     bool
	}{
		{"m5.large", true},
		{"m5.xlarge", true},
		{"t3.medium", true},
		{"unknown-type", false},
	}

	for _, tt := range tests {
		t.Run(tt.instanceType, func(t *testing.T) {
			hourly, monthly, ok := analyzer.GetFallbackPrice(tt.instanceType)
			if ok != tt.expectOK {
				t.Errorf("GetFallbackPrice(%s) ok = %v, want %v", tt.instanceType, ok, tt.expectOK)
			}
			if tt.expectOK {
				if hourly <= 0 {
					t.Errorf("Expected positive hourly price for %s", tt.instanceType)
				}
				if monthly != hourly*730.0 {
					t.Errorf("Monthly price should be hourly * 730")
				}
			}
		})
	}
}

func TestRegionToPricingLocation(t *testing.T) {
	tests := []struct {
		region   string
		expected string
	}{
		{"us-east-1", "US East (N. Virginia)"},
		{"us-west-2", "US West (Oregon)"},
		{"eu-west-1", "EU (Ireland)"},
		{"ap-southeast-1", "Asia Pacific (Singapore)"},
		{"unknown-region", ""},
	}

	for _, tt := range tests {
		t.Run(tt.region, func(t *testing.T) {
			result := regionToPricingLocation(tt.region)
			if result != tt.expected {
				t.Errorf("regionToPricingLocation(%s) = %s, want %s", tt.region, result, tt.expected)
			}
		})
	}
}

func TestNewCostAnalyzer(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cache := NewCache()

	analyzer := NewCostAnalyzer(nil, logger, cache, "us-east-1")

	if analyzer == nil {
		t.Fatal("Expected non-nil analyzer")
	}
	if analyzer.logger != logger {
		t.Error("Logger not set correctly")
	}
	if analyzer.cache != cache {
		t.Error("Cache not set correctly")
	}
	if analyzer.region != "us-east-1" {
		t.Errorf("Region = %s, want us-east-1", analyzer.region)
	}
	if analyzer.cacheTTL != 60*time.Minute {
		t.Errorf("Cache TTL = %v, want 60m", analyzer.cacheTTL)
	}
}

func TestSetEC2Client(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cache := NewCache()
	analyzer := NewCostAnalyzer(nil, logger, cache, "us-east-1")

	if analyzer.ec2Client != nil {
		t.Error("EC2 client should be nil initially")
	}

	analyzer.SetEC2Client(nil)
	// Just testing that it doesn't panic
}

func TestCostComparison(t *testing.T) {
	comparison := CostComparison{
		InstanceType:        "m5.large",
		OnDemandHourly:      0.096,
		OnDemandMonthly:     70.08,
		SpotHourly:          0.035,
		SpotMonthly:         25.55,
		SavingsPercent:      63.5,
		EstimatedRisk:       "medium",
		RecommendedCapacity: "mixed",
	}

	if comparison.InstanceType != "m5.large" {
		t.Errorf("InstanceType = %s, want m5.large", comparison.InstanceType)
	}
	if comparison.SavingsPercent != 63.5 {
		t.Errorf("SavingsPercent = %f, want 63.5", comparison.SavingsPercent)
	}
}

func TestSpotPriceResult(t *testing.T) {
	now := time.Now()
	result := SpotPriceResult{
		InstanceType:     "m5.large",
		AvailabilityZone: "us-east-1a",
		SpotPrice:        0.035,
		Timestamp:        now,
	}

	if result.InstanceType != "m5.large" {
		t.Errorf("InstanceType = %s, want m5.large", result.InstanceType)
	}
	if result.AvailabilityZone != "us-east-1a" {
		t.Errorf("AvailabilityZone = %s, want us-east-1a", result.AvailabilityZone)
	}
	if result.SpotPrice != 0.035 {
		t.Errorf("SpotPrice = %f, want 0.035", result.SpotPrice)
	}
}

func TestEstimateOnDemandUSDNoPricingClient(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cache := NewCache()
	analyzer := NewCostAnalyzer(nil, logger, cache, "us-east-1")

	_, _, ok := analyzer.EstimateOnDemandUSD(context.Background(), "m5.large")
	if ok {
		t.Error("Expected ok=false when pricing client is nil")
	}
}

func TestEstimateSpotUSDNoEC2Client(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cache := NewCache()
	analyzer := NewCostAnalyzer(nil, logger, cache, "us-east-1")

	_, _, ok := analyzer.EstimateSpotUSD(context.Background(), "m5.large")
	if ok {
		t.Error("Expected ok=false when EC2 client is nil")
	}
}

func TestGetSpotPricesByAZNoEC2Client(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cache := NewCache()
	analyzer := NewCostAnalyzer(nil, logger, cache, "us-east-1")

	_, err := analyzer.GetSpotPricesByAZ(context.Background(), "m5.large")
	if err == nil {
		t.Error("Expected error when EC2 client is nil")
	}
}

func TestCalculateSpotSavingsNoPrices(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cache := NewCache()
	analyzer := NewCostAnalyzer(nil, logger, cache, "us-east-1")

	_, ok := analyzer.CalculateSpotSavings(context.Background(), "m5.large")
	if ok {
		t.Error("Expected ok=false when prices unavailable")
	}
}

func TestCompareCostsNoPrices(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cache := NewCache()
	analyzer := NewCostAnalyzer(nil, logger, cache, "us-east-1")

	result, err := analyzer.CompareCosts(context.Background(), "m5.large", 3)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Expected non-nil result")
	}
	// Without pricing clients, costs should be 0
	if result.OnDemandHourly != 0 {
		t.Errorf("OnDemandHourly = %f, want 0", result.OnDemandHourly)
	}
}

func TestPricingFilter(t *testing.T) {
	filter := pricingFilter{
		Type:  "TERM_MATCH",
		Field: "instanceType",
		Value: "m5.large",
	}

	if filter.Type != "TERM_MATCH" {
		t.Errorf("Type = %s, want TERM_MATCH", filter.Type)
	}
	if filter.Field != "instanceType" {
		t.Errorf("Field = %s, want instanceType", filter.Field)
	}
	if filter.Value != "m5.large" {
		t.Errorf("Value = %s, want m5.large", filter.Value)
	}
}

func TestToPricingFilters(t *testing.T) {
	filters := []pricingFilter{
		{Type: "TERM_MATCH", Field: "instanceType", Value: "m5.large"},
		{Type: "TERM_MATCH", Field: "location", Value: "US East (N. Virginia)"},
	}

	result := toPricingFilters(filters)

	if len(result) != 2 {
		t.Errorf("Expected 2 filters, got %d", len(result))
	}
}

func TestFindPath(t *testing.T) {
	doc := map[string]any{
		"terms": map[string]any{
			"OnDemand": map[string]any{
				"key1": "value1",
			},
		},
	}

	result := findPath(doc, "terms.OnDemand")
	if len(result) == 0 {
		t.Error("Expected non-empty result for valid path")
	}

	result = findPath(doc, "invalid.path")
	if len(result) != 0 {
		t.Error("Expected empty result for invalid path")
	}
}

func TestExtractFirstUSD(t *testing.T) {
	// Empty map
	result := extractFirstUSD(map[string]any{})
	if result != 0 {
		t.Errorf("Expected 0 for empty map, got %f", result)
	}

	// Valid pricing structure
	onDemand := map[string]any{
		"term1": map[string]any{
			"priceDimensions": map[string]any{
				"dim1": map[string]any{
					"pricePerUnit": map[string]any{
						"USD": "0.096",
					},
				},
			},
		},
	}

	result = extractFirstUSD(onDemand)
	if result != 0.096 {
		t.Errorf("Expected 0.096, got %f", result)
	}
}
