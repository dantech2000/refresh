// Package types provides core data types used throughout the refresh CLI tool.
package types

// NodegroupInfo contains essential information about an EKS nodegroup.
type NodegroupInfo struct {
	Name         string
	Status       string
	InstanceType string
	Desired      int32
	CurrentAmi   string
	AmiStatus    AMIStatus
}

// VersionInfo contains version information for the CLI tool.
type VersionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	BuildDate string `json:"build_date,omitempty"`
}
