package types

import (
	"time"

	"github.com/aws/aws-sdk-go-v2/service/eks/types"
)

// UpdateProgress tracks the progress of a nodegroup update operation.
type UpdateProgress struct {
	NodegroupName string
	UpdateID      string
	ClusterName   string
	Status        types.UpdateStatus
	StartTime     time.Time
	LastChecked   time.Time
	// ErrorMessage holds errors reported by the AWS update itself.
	ErrorMessage string
	// LastCheckError holds a transient status-polling failure (throttle,
	// network blip). It is display-only: the update may well still be running
	// in AWS, so it must not be rendered as a FAILED update.
	LastCheckError string
	// MonitorErr is set when status polling failed permanently (e.g.
	// AccessDenied or ResourceNotFound on DescribeUpdate). The monitor stops
	// polling this update, but its EKS outcome is unknown: it is not Failed.
	MonitorErr error
}

// ProgressMonitor holds the state of a set of nodegroup updates being
// monitored. It is not safe for concurrent use: the monitoring loop owns it
// and is its only writer.
type ProgressMonitor struct {
	Updates   []UpdateProgress
	StartTime time.Time
	// Live repaints the progress view in place (a render.LiveRegion); nil
	// until the first progress frame is drawn.
	Live FrameDrawer
}

// FrameDrawer paints one frame of lines, over the previous frame when it
// can. render.LiveRegion implements it; the interface keeps this domain
// package free of the view layer.
type FrameDrawer interface {
	Draw(frame []string)
}

// MonitorConfig contains configuration for the update monitoring process.
type MonitorConfig struct {
	PollInterval time.Duration
	Quiet        bool
	Timeout      time.Duration
}
