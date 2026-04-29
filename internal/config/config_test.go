package config

import (
	"os"
	"sync"
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
	os.Unsetenv(EnvEKSRegions)
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

func TestGetEnvDuration_Valid(t *testing.T) {
	t.Setenv("TEST_DUR", "30s")
	if d := getEnvDuration("TEST_DUR"); d != 30*time.Second {
		t.Errorf("getEnvDuration = %v, want 30s", d)
	}
}

func TestGetEnvDuration_Invalid(t *testing.T) {
	t.Setenv("TEST_DUR", "notaduration")
	if d := getEnvDuration("TEST_DUR"); d != 0 {
		t.Errorf("expected 0 for invalid duration, got %v", d)
	}
}

func TestGetEnvDuration_Unset(t *testing.T) {
	os.Unsetenv("TEST_DUR_UNSET")
	if d := getEnvDuration("TEST_DUR_UNSET"); d != 0 {
		t.Errorf("expected 0 for unset env, got %v", d)
	}
}

func TestGetEnvInt_Valid(t *testing.T) {
	t.Setenv("TEST_INT", "42")
	if i := getEnvInt("TEST_INT"); i != 42 {
		t.Errorf("getEnvInt = %d, want 42", i)
	}
}

func TestGetEnvInt_Invalid(t *testing.T) {
	t.Setenv("TEST_INT", "abc")
	if i := getEnvInt("TEST_INT"); i != 0 {
		t.Errorf("expected 0 for invalid int, got %d", i)
	}
}

func TestConfig_GetTimeout_Default(t *testing.T) {
	c := &Config{Timeout: DefaultTimeout}
	if c.GetTimeout() != DefaultTimeout {
		t.Errorf("GetTimeout = %v, want %v", c.GetTimeout(), DefaultTimeout)
	}
}

func TestConfig_SetTimeout(t *testing.T) {
	c := &Config{Timeout: DefaultTimeout}
	c.SetTimeout(2 * time.Minute)
	if c.GetTimeout() != 2*time.Minute {
		t.Errorf("GetTimeout after set = %v, want 2m", c.GetTimeout())
	}
}

func TestConfig_GetMaxConcurrency_Default(t *testing.T) {
	c := &Config{MaxConcurrency: DefaultMaxConcurrency}
	if c.GetMaxConcurrency() != DefaultMaxConcurrency {
		t.Errorf("GetMaxConcurrency = %d, want %d", c.GetMaxConcurrency(), DefaultMaxConcurrency)
	}
}

func TestConfig_GetRegions_NilSafe(t *testing.T) {
	c := &Config{}
	if r := c.GetRegions(); r != nil {
		t.Errorf("expected nil for empty regions, got %v", r)
	}
}

func TestConfig_GetRegions_ReturnsCopy(t *testing.T) {
	c := &Config{Regions: []string{"us-east-1", "eu-west-1"}}
	r := c.GetRegions()
	r[0] = "modified"
	if c.Regions[0] != "us-east-1" {
		t.Error("GetRegions should return a copy, not a reference")
	}
}

func TestDefaultEKSRegions_NotEmpty(t *testing.T) {
	r := DefaultEKSRegions()
	if len(r) == 0 {
		t.Error("DefaultEKSRegions should not be empty")
	}
}

func TestGetLoadsEnvironmentOnce(t *testing.T) {
	oldConfig := globalConfig
	oldOnce := configOnce
	t.Cleanup(func() {
		globalConfig = oldConfig
		configOnce = oldOnce
	})

	globalConfig = nil
	configOnce = sync.Once{}
	t.Setenv(EnvTimeout, "2m")
	t.Setenv(EnvMaxConcurrency, "12")
	t.Setenv(EnvEKSRegions, "us-east-1,us-west-2")
	t.Setenv(EnvClusterName, "prod")
	t.Setenv(EnvKubeconfig, "/tmp/kubeconfig")

	cfg := Get()
	if cfg.Timeout != 2*time.Minute {
		t.Fatalf("Timeout = %v, want 2m", cfg.Timeout)
	}
	if cfg.MaxConcurrency != 12 {
		t.Fatalf("MaxConcurrency = %d, want 12", cfg.MaxConcurrency)
	}
	if cfg.ClusterName != "prod" || cfg.Kubeconfig != "/tmp/kubeconfig" {
		t.Fatalf("loaded config = %+v", cfg)
	}
	if got := cfg.GetRegions(); len(got) != 2 || got[0] != "us-east-1" || got[1] != "us-west-2" {
		t.Fatalf("Regions = %v", got)
	}

	t.Setenv(EnvTimeout, "5m")
	if again := Get(); again != cfg || again.Timeout != 2*time.Minute {
		t.Fatalf("Get() should return cached config, got %+v", again)
	}
}

func TestLoadConfigDefaultsAndSetters(t *testing.T) {
	t.Setenv(EnvTimeout, "")
	t.Setenv(EnvMaxConcurrency, "")
	t.Setenv(EnvEKSRegions, ",,")
	t.Setenv(EnvClusterName, "")
	t.Setenv(EnvKubeconfig, "")

	cfg := loadConfig()
	if cfg.Timeout != DefaultTimeout || cfg.MaxConcurrency != DefaultMaxConcurrency {
		t.Fatalf("defaults not loaded: %+v", cfg)
	}
	if cfg.Kubeconfig == "" {
		t.Fatal("default kubeconfig should be set when home is available")
	}
	if cfg.Regions != nil {
		t.Fatalf("Regions = %v, want nil", cfg.Regions)
	}

	cfg.SetMaxConcurrency(3)
	if cfg.GetMaxConcurrency() != 3 {
		t.Fatalf("GetMaxConcurrency() = %d, want 3", cfg.GetMaxConcurrency())
	}
}

func TestDefaultKubeconfigNoHome(t *testing.T) {
	t.Setenv("HOME", "")
	if got := defaultKubeconfig(); got != "" {
		t.Fatalf("defaultKubeconfig() = %q, want empty without HOME", got)
	}
}

func TestGetEnvIntUnset(t *testing.T) {
	t.Setenv("TEST_INT_UNSET", "")
	if i := getEnvInt("TEST_INT_UNSET"); i != 0 {
		t.Fatalf("getEnvInt unset = %d, want 0", i)
	}
}

func TestRegionsFromEnvOnlyCommas(t *testing.T) {
	t.Setenv(EnvEKSRegions, ",, ,")
	if r := RegionsFromEnv(); r != nil {
		t.Fatalf("RegionsFromEnv() = %v, want nil", r)
	}
}
