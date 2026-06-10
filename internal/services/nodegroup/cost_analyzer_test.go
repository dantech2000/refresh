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

func TestEstimateOnDemandUSD_FallbackWhenNoPricingClient(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cache := NewCache()
	analyzer := NewCostAnalyzer(nil, logger, cache, "us-east-1")

	// Known instance type should fall back to static map
	perHour, perMonth, ok := analyzer.EstimateOnDemandUSD(context.Background(), "m5.large")
	if !ok {
		t.Fatal("expected ok=true for m5.large (static fallback)")
	}
	if perHour != 0.096 {
		t.Errorf("perHour = %f, want 0.096", perHour)
	}
	if perMonth != 0.096*730.0 {
		t.Errorf("perMonth = %f, want %f", perMonth, 0.096*730.0)
	}
}

func TestEstimateOnDemandUSD_NoFallbackForUnknownType(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cache := NewCache()
	analyzer := NewCostAnalyzer(nil, logger, cache, "us-east-1")

	// Unknown instance type should return false when no pricing client and no static entry
	_, _, ok := analyzer.EstimateOnDemandUSD(context.Background(), "p4d.24xlarge")
	if ok {
		t.Error("expected ok=false for unknown instance type with no pricing client")
	}
}

func TestEstimateOnDemandUSD_CachesStaticFallback(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cache := NewCache()
	analyzer := NewCostAnalyzer(nil, logger, cache, "us-east-1")

	// First call populates cache
	analyzer.EstimateOnDemandUSD(context.Background(), "t3.medium")

	// Second call should hit cache
	key := "price:us-east-1:t3.medium"
	if _, ok := cache.Get(key); !ok {
		t.Error("expected cache to be populated after fallback")
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
