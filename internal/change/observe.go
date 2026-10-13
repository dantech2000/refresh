package change

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	asgtypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"
	"k8s.io/client-go/kubernetes"

	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/noderoll"
)

const (
	// scalingActivitiesPage is how many of the newest activities of a
	// group one read covers. A roll that waits for capacity tries the
	// launch again every few minutes, so the newest ones hold the failure.
	scalingActivitiesPage = 20
	// watchedSnapshotInterval is the pause between two snapshots of an
	// observer that reads from a watch, and listedSnapshotInterval the
	// longest pause of one that lists the nodes for each snapshot.
	watchedSnapshotInterval = time.Second
	listedSnapshotInterval  = 3 * time.Second
	// maxSnapshotErrors is how many snapshots in a row may fail before
	// the observer of a roll gives up.
	maxSnapshotErrors = 5
	// recentNodeEvents is how many events of a roll a NodeProgress event
	// carries.
	recentNodeEvents = 8
)

// observers returns the function that runs beside the wait of a batch,
// for fanout.RunAlongside: the Auto Scaling watch of the rolls that were
// started, and with Common.Nodes the node observer of a batch that has
// exactly one of them. Both
// report through the event hook and read only. Neither returns anything
// to the wait, so neither can change a result. RunAlongside ends their
// context when the wait returns, and joins them.
func (r *runner) observers(cp *ClusterPlan, ph Phase, started []int, results []StepResult, kube kubernetes.Interface, noKube string, ticks <-chan struct{}) func(context.Context) {
	type roll struct {
		step      Step
		startedAt time.Time
		groups    []string
	}
	rolls := make([]roll, 0, len(started))
	var since time.Time
	for _, i := range started {
		ro := roll{step: ph.Steps[i], startedAt: results[i].StartedAt}
		for _, n := range cp.snapshot.Nodegroups {
			if n.Name == ro.step.Ref.Name {
				ro.groups = n.AutoScalingGroups
			}
		}
		if since.IsZero() || ro.startedAt.Before(since) {
			since = ro.startedAt
		}
		rolls = append(rolls, ro)
	}
	clients := r.d.AWS.Clients(cp.Region)
	// The node view is for one roll. Several rolls at once are followed
	// by the lines of their updates.
	nodes := r.common.Nodes && len(rolls) == 1
	if nodes && kube == nil {
		r.notice("no Kubernetes access to " + cp.Cluster + " (" + noKube + "); the nodes of the roll are not shown")
	}
	return func(ctx context.Context) {
		var wg sync.WaitGroup
		wg.Go(func() {
			var groups []string
			for _, ro := range rolls {
				groups = append(groups, ro.groups...)
			}
			r.watchScaling(ctx, clients, groups, since, ticks)
		})
		if nodes && kube != nil {
			for _, ro := range rolls {
				wg.Go(func() { r.observeNodes(ctx, kube, ro.step, ro.startedAt) })
			}
		}
		wg.Wait()
	}
}

// watchScaling reports each failed Auto Scaling activity of groups since
// a time, once, as a notice. A launch that the EC2 vCPU quota refuses is
// one: EKS shows a roll that waits on it only as an update in progress.
//
// It reads once for each tick of the wait, so it has the pace of
// --poll-interval and no clock of its own. It is best effort: a read that
// fails is tried again on the next tick, and an error that another read
// will not fix, such as a missing permission, ends the watch with no
// word.
func (r *runner) watchScaling(ctx context.Context, c *awsx.Clients, groups []string, since time.Time, ticks <-chan struct{}) {
	if len(groups) == 0 {
		return
	}
	seen := map[string]bool{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}
		failures, err := scalingFailures(ctx, c, groups, since)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil && awsx.IsPermanentAPIError(err):
			return
		case err != nil:
			continue
		}
		for _, f := range failures {
			if !seen[f.id] {
				seen[f.id] = true
				r.notice("Auto Scaling group " + f.group + ": " + f.message)
			}
		}
	}
}

// scalingFailure is an activity of an Auto Scaling group that failed or
// was cancelled.
type scalingFailure struct {
	// id is the activity ID: the same failure is reported once.
	id, group, message string
	at                 time.Time
}

// scalingFailures returns the failed and the cancelled activities of
// groups that started at or after since, oldest first. It reads the
// newest scalingActivitiesPage activities of each group.
func scalingFailures(ctx context.Context, c *awsx.Clients, groups []string, since time.Time) ([]scalingFailure, error) {
	var out []scalingFailure
	for _, group := range groups {
		resp, err := c.AutoScaling.DescribeScalingActivities(ctx, &autoscaling.DescribeScalingActivitiesInput{
			AutoScalingGroupName: aws.String(group),
			MaxRecords:           aws.Int32(scalingActivitiesPage),
		})
		if err != nil {
			return nil, err
		}
		for _, a := range resp.Activities {
			if a.StatusCode != asgtypes.ScalingActivityStatusCodeFailed && a.StatusCode != asgtypes.ScalingActivityStatusCodeCancelled {
				continue
			}
			at := aws.ToTime(a.StartTime)
			if at.Before(since) {
				continue
			}
			msg := strings.Join(strings.Fields(aws.ToString(a.StatusMessage)), " ")
			if msg == "" {
				msg = strings.Join(strings.Fields(aws.ToString(a.Description)), " ")
			}
			out = append(out, scalingFailure{id: aws.ToString(a.ActivityId), group: group, message: msg, at: at})
		}
	}
	slices.SortStableFunc(out, func(a, b scalingFailure) int { return a.at.Compare(b.at) })
	return out, nil
}

// observeNodes follows the nodes of one roll and reports each snapshot as
// a NodeProgress event, until ctx ends. A failed attach, or
// maxSnapshotErrors failed snapshots in a row, ends it with one notice:
// the roll goes on without the view.
//
// The pause between two snapshots is one second when the observer reads
// from a watch, which costs no request, and the poll interval, at most
// three seconds, when it lists. The clock is Deps.Tick.
func (r *runner) observeNodes(ctx context.Context, kube kubernetes.Interface, s Step, startedAt time.Time) {
	name := s.Ref.Name
	obs, err := noderoll.Attach(ctx, kube, name, noderoll.Identity{TargetAMI: s.ToRelease, StartedAt: startedAt})
	if err != nil {
		if ctx.Err() == nil {
			r.notice("could not read the nodes of nodegroup " + name + " (" + err.Error() + "); the roll goes on without the node view")
		}
		return
	}
	defer obs.Close()
	tick := r.d.Tick
	if tick == nil {
		tick = time.After
	}
	pause := watchedSnapshotInterval
	if !obs.Watching() {
		pause = min(max(r.common.Limits.Poll, time.Second), listedSnapshotInterval)
	}
	tracker := noderoll.NewTracker()
	failed := 0
	for {
		snap, err := obs.Snapshot(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			if failed++; failed >= maxSnapshotErrors {
				r.notice("could not read the nodes of nodegroup " + name + " any more; the roll goes on without the node view")
				return
			}
		default:
			failed = 0
			tracker.Observe(snap)
			view := NodeView{Nodegroup: name, Snapshot: snap, Recent: tracker.Recent(recentNodeEvents), Watching: obs.Watching(), StartedAt: startedAt}
			r.emit(Event{Kind: NodeProgress, Phase: s.Ref.Phase, Step: s.Ref, Nodes: &view})
		}
		select {
		case <-ctx.Done():
			return
		case <-tick(pause):
		}
	}
}
