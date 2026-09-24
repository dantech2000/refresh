// Package types provides core data types used throughout the refresh CLI tool.
package types

// VersionInfo contains version information for the CLI tool.
type VersionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	BuildDate string `json:"build_date,omitempty"`
}

// RegionFailure is one region a multi-region read could not list. Commands
// put these under "failures" in their -o json/yaml document, so a partial
// result is never mistaken for a complete one. Error is one line.
type RegionFailure struct {
	Region string `json:"region" yaml:"region"`
	Error  string `json:"error" yaml:"error"`
}
