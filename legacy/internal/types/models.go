// Package types provides core data types used throughout the refresh CLI tool.
package types

// VersionInfo contains version information for the CLI tool. No command
// encodes it today (refresh version prints text only); the tags follow the
// camelCase document convention in case one does.
type VersionInfo struct {
	Version   string `json:"version" yaml:"version"`
	Commit    string `json:"commit,omitempty" yaml:"commit,omitempty"`
	BuildDate string `json:"buildDate,omitempty" yaml:"buildDate,omitempty"`
}
