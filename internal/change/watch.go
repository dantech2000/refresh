package change

import (
	"context"
	"errors"
	"time"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"k8s.io/client-go/kubernetes"
	metricsv1beta1 "k8s.io/metrics/pkg/client/clientset/versioned/typed/metrics/v1beta1"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/exitcode"
	"github.com/dantech2000/refresh/internal/fanout"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
)

// WatchOptions say how a watch follows a change.
type WatchOptions struct {
	// Nodes starts the node observer of a nodegroup roll.
	Nodes bool
	// Limits: Read bounds the first reads, Wait bounds the watch, and
	// Poll is its interval.
	Limits Limits
}

// Watch follows a change that something else started: the CLI, the
// console, an automation. It waits for the update in flight on a
// nodegroup or on the control plane, shows the nodes of a nodegroup roll,
// and verifies a nodegroup after it. The handle has no controls. The
// result has one step, with Attached set, or none when the first read
// finds nothing in flight, or an update that is not a change of refresh
// (a configuration update of the control plane). The kind of the handle
// and its start time are those of the update, once it is read.
func Watch(ctx context.Context, d Deps, r Resource, o WatchOptions) *Handle {
	h := newHandle(NodegroupUpdate, r.Target, true, d.now)
	go func() { h.finish(watch(ctx, d, r, o, h.event, h.describe)) }()
	return h
}

// watch is the run of a Watch. It reports through emit with the events
// of a run, so a front end draws it as one, and names the change through
// describe once the update is read.
func watch(ctx context.Context, d Deps, r Resource, o WatchOptions, emit func(Event), describeAs func(Kind, time.Time)) Result {
	clients := d.AWS.Clients(r.Region)
	// The first reads have the read budget: a read that hangs ends the
	// watch, not the claim of its cluster.
	rctx, read := d.budget(ctx, o.Limits.Read, ErrReadTimeout)
	read.start()
	in, err := inFlightUpdate(rctx, clients, r)
	if err == nil {
		var settled Settled
		settled, err = describe(rctx, clients, r)
		if err == nil && in.ID == "" && !changing(r, settled) {
			read.stop()
			return Result{Status: NothingToDo, Err: errors.New(r.String() + " is not changing")}
		}
		if err == nil {
			return watchUpdate(ctx, rctx, read, d, r, o, in, settled, emit, describeAs)
		}
	}
	read.stop()
	if rctx.Err() != nil {
		// The budget names why the read ended; the read itself only says
		// that its context did.
		err = context.Cause(rctx)
	}
	return watchFailed(r, err)
}

// watchUpdate follows the update in flight. rctx and read are the read
// budget of the first reads, which ends with them.
func watchUpdate(ctx, rctx context.Context, read *budget, d Deps, r Resource, o WatchOptions, in InFlightUpdate, settled Settled, emit func(Event), describeAs func(Kind, time.Time)) Result {
	clients := d.AWS.Clients(r.Region)
	kind, phase, ok := watchKind(r, in)
	if !ok {
		read.stop()
		return Result{Status: NothingToDo, Err: errors.New(r.String() + ": the update in flight (" + in.Type + ") is not a change that refresh makes")}
	}
	startedAt := in.CreatedAt
	if startedAt.IsZero() {
		startedAt = d.now()
	}
	describeAs(kind, startedAt)
	stepKind := r.Kind
	if kind == NodegroupScale {
		stepKind = StepScale
	}
	step := Step{Ref: StepRef{Phase: PhaseRef{Target: r.Target, Kind: phase}, Kind: stepKind, Name: r.Name}, Action: Attach, Reason: InFlight, From: settled.Version, To: settled.Version}
	plan := &Plan{Kind: kind, Clusters: []ClusterPlan{{Target: r.Target, Phases: []Phase{{Ref: step.Ref.Phase, Steps: []Step{step}}}}}, At: d.now()}
	emit(Event{At: d.now(), Kind: PlanReady, Plan: plan})
	emit(Event{At: d.now(), Kind: PhaseStarted, Phase: step.Ref.Phase})
	res := StepResult{Ref: step.Ref, Status: StepStarted, Attached: true, UpdateID: in.ID, StartedAt: startedAt}
	report := func() {
		emit(Event{At: d.now(), Kind: StepChanged, Phase: step.Ref.Phase, Step: step.Ref, Result: &res})
	}
	report()

	wctx, wait := d.budget(ctx, o.Limits.Wait, ErrWaitTimeout)
	wait.start()
	defer wait.stop()
	opts := WaitOptions{Interval: o.Limits.Poll, After: d.After, budget: wait, Progress: func(p UpdateProgress) {
		emit(Event{At: d.now(), Kind: UpdatePolled, Phase: step.Ref.Phase, Step: step.Ref, Update: &p})
	}}
	poll := d.AWS.PollClients(r.Region)
	waitFn := func(ctx context.Context) error {
		if in.ID != "" {
			return WaitUpdate(ctx, poll, r, in.ID, opts)
		}
		_, err := WaitSettled(ctx, poll, r, settledFn(r), opts)
		return err
	}

	// The snapshot gives the endpoint that the kubeconfig is matched to,
	// and the nodegroup that the verification judges. It is the last of
	// the first reads.
	fetcher := &fleet.Fetcher{Now: d.Now}
	var snap *fleet.Snapshot
	var kube kubeClients
	if r.Kind == StepNodegroup {
		snap, _ = fetcher.Fetch(rctx, clients, r.Cluster, fleet.Need{})
		if rctx.Err() != nil {
			read.stop()
			res = watchWaitFailed(rctx, r, res, rctx.Err(), d.now())
			report()
			emit(Event{At: d.now(), Kind: PhaseEnded, Phase: step.Ref.Phase})
			return watchResult(plan, r, res, nil)
		}
		kube.client, kube.metrics, kube.noKube = kubeAccess(d, snap)
	}
	read.stop()
	observe := func(ctx context.Context) {
		if kind == NodegroupUpdate && o.Nodes && kube.client != nil {
			observeNodes(ctx, d, emit, kube.client, step, startedAt, opts.interval())
		}
	}
	if err := fanout.RunAlongside(wctx, observe, waitFn); err != nil {
		res = watchWaitFailed(wctx, r, res, err, d.now())
		report()
		emit(Event{At: d.now(), Kind: PhaseEnded, Phase: step.Ref.Phase})
		return watchResult(plan, r, res, nil)
	}
	res.Status, res.FinishedAt = StepSucceeded, d.now()

	var verification []health.Result
	if r.Kind == StepNodegroup {
		after, _ := fetcher.Fresh().Fetch(wctx, clients, r.Cluster, fleet.Need{Nodegroups: true})
		checks := health.Verify([]string{r.Name}, health.Baseline{}, health.WaitOptions{Interval: opts.interval(), After: d.After, Now: d.Now})
		verification = health.Run(wctx, checks, &health.Input{Snapshot: after, Clients: clients, Kube: kube.client, NoKube: kube.noKube, Metrics: kube.metrics, Now: d.now()})
		emit(Event{At: d.now(), Kind: VerifyReport, Phase: step.Ref.Phase, Step: step.Ref, Checks: verification})
		for _, v := range verification {
			if v.Outcome == health.Fail {
				res.VerifyIssue = v.Message
				break
			}
		}
	}
	report()
	emit(Event{At: d.now(), Kind: PhaseEnded, Phase: step.Ref.Phase})
	return watchResult(plan, r, res, verification)
}

// kubeClients is the Kubernetes access of a watch.
type kubeClients struct {
	client  kubernetes.Interface
	metrics metricsv1beta1.NodeMetricsInterface
	noKube  string
}

// changing reports whether the resource is in the status that an update
// leaves it in while it runs. A resource that is being created or
// deleted is busy, but no change of refresh is behind it.
func changing(r Resource, s Settled) bool {
	switch r.Kind {
	case StepControlPlane:
		return s.Status == string(ekstypes.ClusterStatusUpdating)
	case StepAddon:
		return s.Status == string(ekstypes.AddonStatusUpdating)
	}
	return s.Status == string(ekstypes.NodegroupStatusUpdating)
}

// watchKind names the change behind an update in flight on r, and the
// phase of its one step. It reports false for an update that is not a
// change refresh makes: a configuration update of the control plane, or
// a control plane that is UPDATING with no update listed, which cannot
// be told apart from one.
func watchKind(r Resource, in InFlightUpdate) (Kind, PhaseKind, bool) {
	switch r.Kind {
	case StepControlPlane:
		switch in.Type {
		case string(ekstypes.UpdateTypeVersionUpdate):
			return ClusterUpgrade, PhaseControlPlane, true
		case string(ekstypes.UpdateTypeVersionRollback):
			return ClusterRollback, PhaseControlPlane, true
		}
		return "", "", false
	case StepAddon:
		return AddonUpdate, PhaseAddons, true
	}
	if in.Type == string(ekstypes.UpdateTypeVersionUpdate) || in.Type == "" {
		return NodegroupUpdate, PhaseRoll, true
	}
	return NodegroupScale, PhaseScale, true
}

// settledFn is the done function of a wait for r when it has no update
// to follow: the one the engines use for the resource.
func settledFn(r Resource) func(Settled) (bool, error) {
	switch r.Kind {
	case StepControlPlane:
		return clusterSettled
	case StepAddon:
		return addonSettled
	}
	return nodegroupSettled
}

// watchFailed is the result of a watch whose first read failed.
func watchFailed(r Resource, err error) Result {
	f := fleet.FailureFromError(resourceKind(StepRef{Kind: r.Kind, Name: r.Name}), r.Name, err)
	f.Cluster, f.Region = r.Cluster, r.Region
	return Result{Status: Failed, Failures: []apidoc.Failure{f}, Err: err, ended: ending{code: exitcode.Failure}}
}

// watchWaitFailed is the step of a watch whose wait did not end in
// success, by the table of a run's wait.
func watchWaitFailed(ctx context.Context, r Resource, res StepResult, err error, now time.Time) StepResult {
	failure := func(reason apidoc.Reason, msg string) *apidoc.Failure {
		f := apidoc.NewFailure(apidoc.FailureUpdate, r.Name, reason, msg)
		f.Cluster, f.Region, f.UpdateID = r.Cluster, r.Region, res.UpdateID
		return &f
	}
	var ended *UpdateError
	var unobserved *UnobservedError
	switch {
	case errors.As(err, &ended):
		res.Status, res.FinishedAt = StepFailed, now
		reason := apidoc.ReasonUpdateFailed
		if ended.Cancelled() {
			res.Status, reason = StepCancelled, apidoc.ReasonUpdateCancelled
		}
		res.Failure = failure(reason, err.Error())
	case errors.As(err, &unobserved):
		res.Status = StepInProgress
		res.Failure = failure(apidoc.ReasonNotMonitored, err.Error())
		res.Failure.Operation, res.Failure.AWSErrorCode = awsx.Operation(err), awsx.ErrorCode(err)
	case ctx.Err() != nil && timedOut(ctx):
		res.Status = StepInProgress
		res.Failure = failure(apidoc.ReasonTimeout, "--wait-timeout passed before the update of "+r.String()+" finished; it may still be running")
	case ctx.Err() != nil:
		res.Status = StepInProgress
		res.Failure = failure(apidoc.ReasonInterrupted, "interrupted while the update of "+r.String()+" was running; it may still be running")
	default:
		res.Status, res.FinishedAt = StepFailed, now
		res.Failure = failure(apidoc.ReasonUpdateFailed, err.Error())
	}
	return res
}

// watchResult is the result of a watch with its one step.
func watchResult(plan *Plan, r Resource, res StepResult, verification []health.Result) Result {
	status := Succeeded
	switch {
	case res.Status == StepInProgress:
		status = Incomplete
		if res.Failure != nil && res.Failure.Reason == apidoc.ReasonInterrupted {
			status = Interrupted
		}
	case res.Status != StepSucceeded:
		status = Failed
	case res.VerifyIssue != "":
		status = VerifyFailed
	}
	out := Result{Status: status, Plan: plan, Clusters: []ClusterResult{{Target: r.Target, Status: status, Steps: []StepResult{res}, Verification: verification}}}
	if res.Failure != nil {
		out.Err = errors.New(res.Failure.Error)
	} else if res.VerifyIssue != "" {
		out.Err = errors.New(res.VerifyIssue)
	}
	if status == Interrupted {
		out.ended = ending{interrupted: true}
	}
	return out
}
