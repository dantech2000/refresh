// Package types provides core data types used throughout the refresh CLI tool.
package types

// VersionInfo contains version information for the CLI tool.
type VersionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	BuildDate string `json:"build_date,omitempty"`
}
