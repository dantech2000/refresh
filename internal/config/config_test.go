package config

import (
	"os"
	"testing"
	"time"
)

func TestDefaultConstants(t *testing.T) {
	if DefaultTimeout != 60*time.Second {
		t.Errorf("DefaultTimeout = %v, want 60s", DefaultTimeout)
	}
	if DefaultMaxConcurrency != 8 {
		t.Errorf("DefaultMaxConcurrency = %d, want 8", DefaultMaxConcurrency)
	}
	if DefaultPollInterval != 15*time.Second {
		t.Errorf("DefaultPollInterval = %v, want 15s", DefaultPollInterval)
	}
}

func TestRegionsFromEnv_NotSet(t *testing.T) {
	_ = os.Unsetenv(EnvEKSRegions)
	if r := RegionsFromEnv(); r != nil {
		t.Errorf("expected nil when env not set, got %v", r)
	}
}

func TestRegionsFromEnv_CommaSeparated(t *testing.T) {
	t.Setenv(EnvEKSRegions, "us-east-1,eu-west-1, ap-southeast-1 ")
	r := RegionsFromEnv()
	if len(r) != 3 {
		t.Fatalf("expected 3 regions, got %d: %v", len(r), r)
	}
	if r[0] != "us-east-1" || r[1] != "eu-west-1" || r[2] != "ap-southeast-1" {
		t.Errorf("unexpected regions: %v", r)
	}
}

func TestRegionsFromEnv_Empty(t *testing.T) {
	t.Setenv(EnvEKSRegions, "   ")
	if r := RegionsFromEnv(); r != nil {
		t.Errorf("expected nil for blank env value, got %v", r)
	}
}

func TestGetRegionsForPartition(t *testing.T) {
	tests := []struct {
		region    string
		wantFirst string
	}{
		{"us-east-1", "us-east-1"},
		{"us-gov-west-1", "us-gov-west-1"},
		{"cn-north-1", "cn-north-1"},
	}
	for _, tt := range tests {
		t.Run(tt.region, func(t *testing.T) {
			r := GetRegionsForPartition(tt.region)
			if len(r) == 0 {
				t.Fatal("expected non-empty region list")
			}
			if r[0] != tt.wantFirst {
				t.Errorf("first region = %s, want %s", r[0], tt.wantFirst)
			}
		})
	}
}
