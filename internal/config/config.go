// Package config provides centralized configuration management for the refresh CLI tool.
// It handles environment variables, defaults, and validation using clean code patterns.
package config

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Default configuration values following Go best practices for constants.
const (
	// DefaultTimeout is the default operation timeout for API calls.
	DefaultTimeout = 60 * time.Second

	// DefaultMaxConcurrency is the default maximum number of concurrent operations.
	DefaultMaxConcurrency = 8

	// DefaultPollInterval is the default interval for polling update status.
	DefaultPollInterval = 15 * time.Second

	// DefaultUpdateTimeout is the default timeout for AMI update operations.
	DefaultUpdateTimeout = 40 * time.Minute

	// DefaultCacheTTL is the default time-to-live for cached data.
	DefaultCacheTTL = 5 * time.Minute

	// DefaultListCacheTTL is the TTL for list operation cache.
	DefaultListCacheTTL = 2 * time.Minute
)

// Environment variable names as constants for type safety and refactoring.
const (
	EnvTimeout        = "REFRESH_TIMEOUT"
	EnvMaxConcurrency = "REFRESH_MAX_CONCURRENCY"
	EnvEKSRegions     = "REFRESH_EKS_REGIONS"
	EnvClusterName    = "EKS_CLUSTER_NAME"
	EnvKubeconfig     = "KUBECONFIG"
	EnvAWSRegion      = "AWS_REGION"
	EnvAWSProfile     = "AWS_PROFILE"
)

// Config holds all runtime configuration loaded from environment and defaults.
// It is thread-safe through the use of sync.RWMutex.
type Config struct {
	mu             sync.RWMutex
	Timeout        time.Duration
	MaxConcurrency int
	Regions        []string
	ClusterName    string
	Kubeconfig     string
}

// globalConfig is the singleton configuration instance.
var (
	globalConfig *Config
	configOnce   sync.Once
)

// Get returns the global configuration instance, initializing it if necessary.
// This function is thread-safe and follows the singleton pattern.
func Get() *Config {
	configOnce.Do(func() {
		globalConfig = loadConfig()
	})
	return globalConfig
}

// loadConfig loads configuration from environment variables with fallback to defaults.
func loadConfig() *Config {
	cfg := &Config{
		Timeout:        DefaultTimeout,
		MaxConcurrency: DefaultMaxConcurrency,
		Regions:        nil,
		ClusterName:    "",
		Kubeconfig:     defaultKubeconfig(),
	}

	// Load timeout from environment
	if timeout := getEnvDuration(EnvTimeout); timeout > 0 {
		cfg.Timeout = timeout
	}

	// Load max concurrency from environment
	if maxConc := getEnvInt(EnvMaxConcurrency); maxConc > 0 {
		cfg.MaxConcurrency = maxConc
	}

	// Load regions from environment
	cfg.Regions = RegionsFromEnv()

	// Load cluster name from environment
	if clusterName := os.Getenv(EnvClusterName); clusterName != "" {
		cfg.ClusterName = clusterName
	}

	// Load kubeconfig from environment
	if kubeconfig := os.Getenv(EnvKubeconfig); kubeconfig != "" {
		cfg.Kubeconfig = kubeconfig
	}

	return cfg
}

// GetTimeout returns the configured timeout value in a thread-safe manner.
func (c *Config) GetTimeout() time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Timeout
}

// GetMaxConcurrency returns the configured max concurrency in a thread-safe manner.
func (c *Config) GetMaxConcurrency() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.MaxConcurrency
}

// GetRegions returns the configured regions in a thread-safe manner.
func (c *Config) GetRegions() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	// Return a copy to prevent external modification
	if c.Regions == nil {
		return nil
	}
	regions := make([]string, len(c.Regions))
	copy(regions, c.Regions)
	return regions
}

// SetTimeout updates the timeout value in a thread-safe manner.
func (c *Config) SetTimeout(timeout time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Timeout = timeout
}

// SetMaxConcurrency updates the max concurrency in a thread-safe manner.
func (c *Config) SetMaxConcurrency(maxConc int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.MaxConcurrency = maxConc
}

// RegionsFromEnv parses REFRESH_EKS_REGIONS environment variable (comma-separated).
// Returns nil if the environment variable is not set or empty.
func RegionsFromEnv() []string {
	env := strings.TrimSpace(os.Getenv(EnvEKSRegions))
	if env == "" {
		return nil
	}

	parts := strings.Split(env, ",")
	regions := make([]string, 0, len(parts))

	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			regions = append(regions, p)
		}
	}

	if len(regions) == 0 {
		return nil
	}

	return regions
}

// defaultKubeconfig returns the default kubeconfig path.
func defaultKubeconfig() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home + "/.kube/config"
}

// getEnvDuration parses a duration from an environment variable.
// Returns 0 if the variable is not set or cannot be parsed.
func getEnvDuration(key string) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return 0
	}

	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0
	}

	return duration
}

// getEnvInt parses an integer from an environment variable.
// Returns 0 if the variable is not set or cannot be parsed.
func getEnvInt(key string) int {
	value := os.Getenv(key)
	if value == "" {
		return 0
	}

	intValue, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}

	return intValue
}

// DefaultEKSRegions returns the default list of EKS-supported regions
// for the commercial AWS partition.
func DefaultEKSRegions() []string {
	return []string{
		"us-east-1", "us-east-2", "us-west-1", "us-west-2",
		"eu-west-1", "eu-west-2", "eu-west-3", "eu-central-1", "eu-north-1",
		"ap-southeast-1", "ap-southeast-2", "ap-northeast-1", "ap-northeast-2", "ap-south-1",
		"ca-central-1", "sa-east-1",
	}
}

// GovCloudRegions returns the list of EKS-supported GovCloud regions.
func GovCloudRegions() []string {
	return []string{"us-gov-west-1", "us-gov-east-1"}
}

// ChinaRegions returns the list of EKS-supported China regions.
func ChinaRegions() []string {
	return []string{"cn-north-1", "cn-northwest-1"}
}

// GetRegionsForPartition returns the appropriate regions based on the current
// AWS partition detected from the provided region.
func GetRegionsForPartition(currentRegion string) []string {
	switch {
	case strings.HasPrefix(currentRegion, "us-gov-"):
		return GovCloudRegions()
	case strings.HasPrefix(currentRegion, "cn-"):
		return ChinaRegions()
	default:
		return DefaultEKSRegions()
	}
}
