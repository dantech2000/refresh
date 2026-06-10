// Package config provides shared configuration constants and region helpers
// for the refresh CLI. Per-invocation configuration (timeouts, concurrency)
// flows through urfave/cli flags and their environment variables; this
// package only holds the defaults those flags reference.
package config

import (
	"os"
	"strings"
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
)

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
