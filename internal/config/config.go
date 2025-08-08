package config

import (
	"os"
	"strings"
	"time"
)

// Centralized CLI/env defaults used across commands

var (
	DefaultTimeout        = 60 * time.Second
	DefaultMaxConcurrency = 8
)

// RegionsFromEnv parses REFRESH_EKS_REGIONS (comma-separated)
func RegionsFromEnv() []string {
	env := strings.TrimSpace(os.Getenv("REFRESH_EKS_REGIONS"))
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
	return regions
}
