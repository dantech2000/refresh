package nodegroup

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
)

func TestCalculateTrend(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cache := NewCache()
	collector := &UtilizationCollector{
		logger: logger,
		cache:  cache,
	}

	tests := []struct {
		name     string
		results  []cwtypes.MetricDataResult
		expected string
	}{
		{
			name:     "empty results",
			results:  []cwtypes.MetricDataResult{},
			expected: "stable",
		},
		{
			name: "too few datapoints",
			results: []cwtypes.MetricDataResult{
				{
					Timestamps: []time.Time{time.Now()},
					Values:     []float64{50.0},
				},
			},
			expected: "stable",
		},
		{
			name: "increasing trend",
			results: []cwtypes.MetricDataResult{
				{
					Timestamps: []time.Time{
						time.Now().Add(-4 * time.Hour),
						time.Now().Add(-3 * time.Hour),
						time.Now().Add(-2 * time.Hour),
						time.Now().Add(-1 * time.Hour),
						time.Now(),
					},
					Values: []float64{10, 20, 30, 40, 50},
				},
			},
			expected: "increasing",
		},
		{
			name: "decreasing trend",
			results: []cwtypes.MetricDataResult{
				{
					Timestamps: []time.Time{
						time.Now().Add(-4 * time.Hour),
						time.Now().Add(-3 * time.Hour),
						time.Now().Add(-2 * time.Hour),
						time.Now().Add(-1 * time.Hour),
						time.Now(),
					},
					Values: []float64{50, 40, 30, 20, 10},
				},
			},
			expected: "decreasing",
		},
		{
			name: "stable trend",
			results: []cwtypes.MetricDataResult{
				{
					Timestamps: []time.Time{
						time.Now().Add(-4 * time.Hour),
						time.Now().Add(-3 * time.Hour),
						time.Now().Add(-2 * time.Hour),
						time.Now().Add(-1 * time.Hour),
						time.Now(),
					},
					Values: []float64{50, 51, 49, 50, 51},
				},
			},
			expected: "stable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := collector.calculateTrend(tt.results)
			if result != tt.expected {
				t.Errorf("calculateTrend() = %s, want %s", result, tt.expected)
			}
		})
	}
}

func TestNormalizeWindow(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"1h", "1h"},
		{"3h", "3h"},
		{"24h", "24h"},
		{"invalid", "24h"},
		{"", "24h"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := normalizeWindow(tt.input)
			if result != tt.expected {
				t.Errorf("normalizeWindow(%s) = %s, want %s", tt.input, result, tt.expected)
			}
		})
	}
}

func TestNewUtilizationCollector(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cache := NewCache()

	collector := NewUtilizationCollector(nil, logger, cache)

	if collector == nil {
		t.Fatal("Expected non-nil collector")
	}
	if collector.logger != logger {
		t.Error("Logger not set correctly")
	}
	if collector.cache != cache {
		t.Error("Cache not set correctly")
	}
	if collector.cacheTTL != 2*time.Minute {
		t.Errorf("Cache TTL = %v, want 2m", collector.cacheTTL)
	}
}

func TestUtilizationData(t *testing.T) {
	data := UtilizationData{
		Current: 45.5,
		Average: 40.0,
		Peak:    80.0,
		Trend:   "stable",
	}

	if data.Current != 45.5 {
		t.Errorf("Current = %f, want 45.5", data.Current)
	}
	if data.Average != 40.0 {
		t.Errorf("Average = %f, want 40.0", data.Average)
	}
	if data.Peak != 80.0 {
		t.Errorf("Peak = %f, want 80.0", data.Peak)
	}
	if data.Trend != "stable" {
		t.Errorf("Trend = %s, want stable", data.Trend)
	}
}

func TestCollectEC2CPUForInstancesEmpty(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cache := NewCache()
	collector := NewUtilizationCollector(nil, logger, cache)

	data, ok := collector.CollectEC2CPUForInstances(context.Background(), []string{}, "1h")
	if ok {
		t.Error("Expected ok=false for empty instance list")
	}
	if data.Average != 0 {
		t.Errorf("Expected zero data for empty instance list")
	}
}

func TestCollectMemoryForInstancesEmpty(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cache := NewCache()
	collector := NewUtilizationCollector(nil, logger, cache)

	data, ok := collector.CollectMemoryForInstances(context.Background(), []string{}, "1h")
	if ok {
		t.Error("Expected ok=false for empty instance list")
	}
	if data.Average != 0 {
		t.Errorf("Expected zero data for empty instance list")
	}
}

func TestCollectNetworkForInstancesEmpty(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cache := NewCache()
	collector := NewUtilizationCollector(nil, logger, cache)

	data, ok := collector.CollectNetworkForInstances(context.Background(), []string{}, "1h")
	if ok {
		t.Error("Expected ok=false for empty instance list")
	}
	if data.Average != 0 {
		t.Errorf("Expected zero data for empty instance list")
	}
}

func TestCollectDiskForInstancesEmpty(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cache := NewCache()
	collector := NewUtilizationCollector(nil, logger, cache)

	data, ok := collector.CollectDiskForInstances(context.Background(), []string{}, "1h")
	if ok {
		t.Error("Expected ok=false for empty instance list")
	}
	if data.Average != 0 {
		t.Errorf("Expected zero data for empty instance list")
	}
}

func TestCollectAllMetrics(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	cache := NewCache()
	collector := NewUtilizationCollector(nil, logger, cache)

	metrics := collector.CollectAllMetrics(context.Background(), []string{}, "24h")

	if metrics.TimeRange != "24h" {
		t.Errorf("TimeRange = %s, want 24h", metrics.TimeRange)
	}
}

func TestUtilizationMetrics(t *testing.T) {
	metrics := UtilizationMetrics{
		CPU:       UtilizationData{Average: 50.0},
		Memory:    UtilizationData{Average: 60.0},
		Network:   UtilizationData{Average: 10.0},
		Storage:   UtilizationData{Average: 30.0},
		TimeRange: "24h",
	}

	if metrics.CPU.Average != 50.0 {
		t.Errorf("CPU.Average = %f, want 50.0", metrics.CPU.Average)
	}
	if metrics.Memory.Average != 60.0 {
		t.Errorf("Memory.Average = %f, want 60.0", metrics.Memory.Average)
	}
	if metrics.TimeRange != "24h" {
		t.Errorf("TimeRange = %s, want 24h", metrics.TimeRange)
	}
}
