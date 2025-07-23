package types

import (
	"time"

	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/fatih/color"
)

type NodegroupInfo struct {
	Name         string
	Status       string
	InstanceType string
	Desired      int32
	CurrentAmi   string
	AmiStatus    AMIStatus
}

type VersionInfo struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	BuildDate string `json:"build_date,omitempty"`
}

type UpdateProgress struct {
	NodegroupName string
	UpdateID      string
	ClusterName   string
	Status        types.UpdateStatus
	StartTime     time.Time
	LastChecked   time.Time
	ErrorMessage  string
}

type ProgressMonitor struct {
	Updates     []UpdateProgress
	StartTime   time.Time
	Quiet       bool
	NoWait      bool
	Timeout     time.Duration
	LastPrinted int // Track lines printed in last update
}

type MonitorConfig struct {
	PollInterval    time.Duration
	MaxRetries      int
	BackoffMultiple float64
	Quiet           bool
	NoWait          bool
	Timeout         time.Duration
}

// AMIStatus represents the status of a nodegroup's AMI
type AMIStatus int

const (
	AMILatest AMIStatus = iota
	AMIOutdated
	AMIUpdating
	AMIUnknown
)

func (s AMIStatus) String() string {
	switch s {
	case AMILatest:
		return color.GreenString("Latest")
	case AMIOutdated:
		return color.RedString("Outdated")
	case AMIUpdating:
		return color.YellowString("Updating")
	case AMIUnknown:
		return color.WhiteString("Unknown")
	default:
		return color.WhiteString("Unknown")
	}
}

// DryRunAction represents what action would be taken in dry run mode
type DryRunAction int

const (
	ActionUpdate DryRunAction = iota
	ActionSkipUpdating
	ActionSkipLatest
	ActionForceUpdate
)

func (a DryRunAction) String() string {
	switch a {
	case ActionUpdate:
		return color.GreenString("UPDATE")
	case ActionSkipUpdating:
		return color.YellowString("SKIP")
	case ActionSkipLatest:
		return color.GreenString("SKIP")
	case ActionForceUpdate:
		return color.CyanString("FORCE UPDATE")
	default:
		return color.WhiteString("UNKNOWN")
	}
}
