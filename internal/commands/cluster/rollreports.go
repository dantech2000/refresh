package cluster

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"k8s.io/client-go/kubernetes"

	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/rollview"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
	"github.com/dantech2000/refresh/internal/services/upgrade"
)

// rollReports shows the failed node launches of an upgrade's or rollback's
// nodegroup rolls: in the live panel while it draws, else on a line.
type rollReports struct {
	panel atomic.Pointer[rollview.Notes]
}

// watch is the engine's ScalingWatch: it reads the nodegroup's Auto
// Scaling activities and reports each failed launch once.
func (r *rollReports) watch(awsCfg aws.Config, clusterName string, line func(string)) *upgrade.ScalingWatch {
	svc := factory.NewNodegroupService(awsCfg, false, nil)
	return &upgrade.ScalingWatch{
		Read: func(ctx context.Context, ng string, since time.Time) ([]nodegroupsvc.ScalingFailure, error) {
			return svc.ScalingFailures(ctx, clusterName, ng, since)
		},
		Report: func(ng string, f nodegroupsvc.ScalingFailure) {
			msg := fmt.Sprintf("Auto Scaling could not launch a node for %s: %s", ng, f.Message)
			if notes := r.panel.Load(); notes != nil {
				notes.Add(msg)
				return
			}
			line(msg)
		},
	}
}

// observer is the engine's NodegroupObserver: the live panel of each roll,
// which shows the reports while it draws.
func (r *rollReports) observer(kube kubernetes.Interface, waitTimeout, poll time.Duration) upgrade.RollObserver {
	return func(octx context.Context, ng string) {
		notes := &rollview.Notes{}
		r.panel.Store(notes)
		defer r.panel.Store(nil)
		rollview.LiveRollForUpdate(octx, kube, ng, waitTimeout, poll, notes)
	}
}
