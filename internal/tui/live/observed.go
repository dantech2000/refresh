package live

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/commands/factory"
	"github.com/dantech2000/refresh/internal/common"
	"github.com/dantech2000/refresh/internal/noderoll"
	statussvc "github.com/dantech2000/refresh/internal/services/status"
	"github.com/dantech2000/refresh/internal/tui/state"
)

// Changes started elsewhere (the CLI, the console, another tool) were
// invisible in the TUI except as a busy cluster: a real `cluster upgrade`
// from the CLI showed nothing on the Rolls or Upgrade screens. The sweep now
// adopts them: a nodegroup that is UPDATING gets an observed roll with the
// node view, and a cluster that is UPDATING gets an observed upgrade that
// follows the control-plane update. Neither claims the cluster or can be
// controlled from here.

// inProgressUpdate returns the EKS update in progress on a cluster (nodegroup
// "") or one of its nodegroups, or nil when there is none.
func inProgressUpdate(ctx context.Context, cfg aws.Config, cluster, nodegroup string) (*ekstypes.Update, error) {
	api := factory.NewEKSClient(cfg)
	ng := func() *string {
		if nodegroup == "" {
			return nil
		}
		return aws.String(nodegroup)
	}
	ids, err := awsinternal.ListAllPages(ctx, "listing updates of "+cluster,
		func(rc context.Context, token *string) (*eks.ListUpdatesOutput, error) {
			return api.ListUpdates(rc, &eks.ListUpdatesInput{Name: aws.String(cluster), NodegroupName: ng(), NextToken: token})
		},
		func(out *eks.ListUpdatesOutput) ([]string, *string) { return out.UpdateIds, out.NextToken })
	if err != nil {
		return nil, err
	}
	var newest *ekstypes.Update
	// Newest first is not promised, so read them all; a cluster keeps few.
	for _, id := range slices.Backward(ids) {
		out, err := describeUpdate(ctx, api, cluster, nodegroup, id)
		if err != nil {
			return nil, err
		}
		u := out.Update
		if u == nil || u.Status != ekstypes.UpdateStatusInProgress {
			continue
		}
		if newest == nil || aws.ToTime(u.CreatedAt).After(aws.ToTime(newest.CreatedAt)) {
			newest = u
		}
	}
	return newest, nil
}

func describeUpdate(ctx context.Context, api *eks.Client, cluster, nodegroup, id string) (*eks.DescribeUpdateOutput, error) {
	in := &eks.DescribeUpdateInput{Name: aws.String(cluster), UpdateId: aws.String(id)}
	if nodegroup != "" {
		in.NodegroupName = aws.String(nodegroup)
	}
	out, err := common.WithRetry(ctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeUpdateOutput, error) {
		return api.DescribeUpdate(rc, in)
	})
	if err != nil {
		return nil, awsinternal.FormatAWSError(err, "describing update "+id)
	}
	return out, nil
}

// waitClusterUpdate polls a cluster-level update until it ends or ctx ends.
func waitClusterUpdate(ctx context.Context, cfg aws.Config, cluster, id string, every time.Duration) (ekstypes.UpdateStatus, string, error) {
	api := factory.NewEKSClient(cfg)
	for {
		out, err := describeUpdate(ctx, api, cluster, "", id)
		if err == nil && out.Update != nil && out.Update.Status != ekstypes.UpdateStatusInProgress {
			var msgs []string
			for _, e := range out.Update.Errors {
				msgs = append(msgs, aws.ToString(e.ErrorMessage))
			}
			return out.Update.Status, strings.Join(msgs, "; "), nil
		}
		if err != nil && common.IsPermanentAPIError(err) {
			return "", "", err
		}
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(every):
		}
	}
}

// updateParam is the value of the update's param of the given type.
func updateParam(u *ekstypes.Update, typ ekstypes.UpdateParamType) string {
	for _, p := range u.Params {
		if p.Type == typ {
			return aws.ToString(p.Value)
		}
	}
	return ""
}

// adoptExternal starts watching the changes the sweep found running that this
// backend did not start. The caller holds b.mu.
func (b *Backend) adoptExternal(rows []statussvc.ClusterStatus) {
	if b.runCtx == nil || b.roll.findUpdate == nil {
		return // not running (tests that call sweep directly)
	}
	for _, row := range rows {
		t := target{name: row.Name, region: row.Region}
		if strings.EqualFold(row.State, string(ekstypes.ClusterStatusUpdating)) && !b.adopting["cluster/"+t.region+"/"+t.name] && b.ownUpgrade(t) == nil {
			b.adopting["cluster/"+t.region+"/"+t.name] = true
			b.work.Add(1)
			go func() {
				defer b.work.Done()
				b.adoptUpgrade(b.runCtx, t, row.Version)
			}()
		}
		for _, ng := range row.Nodegroups {
			key := acceptKey(t, ng.Name)
			if !strings.EqualFold(ng.Status, string(ekstypes.NodegroupStatusUpdating)) || b.adopting[key] || b.runningRoll(t, ng.Name) {
				continue
			}
			b.adopting[key] = true
			name := ng.Name
			b.work.Add(1)
			go func() {
				defer b.work.Done()
				b.adoptRoll(b.runCtx, t, name)
			}()
		}
	}
}

// runningRoll reports whether a roll of t's nodegroup is running here. The
// caller holds b.mu.
func (b *Backend) runningRoll(t target, ng string) bool {
	for _, r := range b.rolls {
		if r.t == t && r.st.Nodegroup == ng && r.st.Running() {
			return true
		}
	}
	return false
}

// ownUpgrade returns a running upgrade of t, whoever started it. The caller
// holds b.mu.
func (b *Backend) ownUpgrade(t target) *liveUpgrade {
	for _, u := range b.upgrades {
		if u.t == t && u.st.Running() {
			return u
		}
	}
	return nil
}

func (b *Backend) adoptRoll(ctx context.Context, t target, ng string) {
	key := acceptKey(t, ng)
	defer func() {
		b.mu.Lock()
		delete(b.adopting, key)
		b.mu.Unlock()
	}()
	cfg := b.cfgOf(t)
	cctx, cancel := context.WithTimeout(ctx, b.opts.SweepTimeout)
	u, err := b.roll.findUpdate(cctx, cfg, t.name, ng)
	cancel()
	if err != nil || u == nil {
		return // gone already, or unreadable: the next sweep looks again
	}
	kube, _, how := b.roll.kubeFor(ctx, cfg, t.name)
	version := updateParam(u, ekstypes.UpdateParamTypeVersion)
	release := updateParam(u, ekstypes.UpdateParamTypeReleaseVersion)
	id := aws.ToString(u.Id)
	b.mu.Lock()
	if b.runningRoll(t, ng) {
		b.mu.Unlock()
		return
	}
	// A watch of this update that ended early (an error while EKS still
	// reports UPDATING) resumes in the same roll, not a second one.
	for _, old := range b.rolls {
		if old.t == t && old.updateID == id {
			old.st.EndedAt, old.st.Failed = time.Time{}, ""
			b.rollEvent(old, state.Event{Source: state.SourceRoll, Level: state.LevelInfo, Subject: "started elsewhere", Text: "watching update " + id + " again"})
			b.mu.Unlock()
			b.watchRoll(ctx, old, cfg, t, id, kube, nil, false)
			return
		}
	}
	r := &liveRoll{t: t, updateID: id, tracker: noderoll.NewTracker(), warned: map[string]bool{}}
	r.st = state.Roll{
		Nodegroup: ng, FromVersion: version, ToVersion: version, ToAMI: release,
		StartedAt: aws.ToTime(u.CreatedAt), StartedElsewhere: true, MaxUnavailableText: "per update config",
	}
	if r.st.StartedAt.IsZero() {
		r.st.StartedAt = b.now()
	}
	b.rollEvent(r, state.Event{Source: state.SourceRoll, Level: state.LevelInfo, Subject: "started elsewhere", Text: "watching update " + id, Detail: "not started by this UI; it cannot be stopped here"})
	b.rollEvent(r, state.Event{Source: state.SourceRoll, Level: state.LevelInfo, Subject: "node view", Text: how})
	b.emit(state.Event{Cluster: b.keyOf(t), Source: state.SourceRoll, Level: state.LevelProgress, Subject: ng, Text: "roll started elsewhere · watching", Detail: "update " + id})
	b.rolls = append(b.rolls, r)
	b.mu.Unlock()
	// The pending pods before it started are unknown: the post-roll pod
	// check is skipped, the nodegroup check still runs.
	b.watchRoll(ctx, r, cfg, t, id, kube, nil, false)
}

func (b *Backend) adoptUpgrade(ctx context.Context, t target, from string) {
	key := "cluster/" + t.region + "/" + t.name
	defer func() {
		b.mu.Lock()
		delete(b.adopting, key)
		b.mu.Unlock()
	}()
	cfg := b.cfgOf(t)
	cctx, cancel := context.WithTimeout(ctx, b.opts.SweepTimeout)
	u, err := b.roll.findUpdate(cctx, cfg, t.name, "")
	cancel()
	if err != nil || u == nil {
		return
	}
	to := updateParam(u, ekstypes.UpdateParamTypeVersion)
	what := "Control plane " + to
	if u.Type == ekstypes.UpdateTypeVersionRollback {
		what = "Control plane rollback to " + to
	}
	started := aws.ToTime(u.CreatedAt)
	if started.IsZero() {
		started = b.now()
	}
	id := aws.ToString(u.Id)
	b.mu.Lock()
	if b.ownUpgrade(t) != nil {
		b.mu.Unlock()
		return
	}
	// A watch of this update that ended early resumes the same entry.
	for _, old := range b.upgrades {
		if old.t == t && old.updateID == id {
			old.st.EndedAt, old.st.Failed = time.Time{}, ""
			old.st.Phases[0].Status, old.st.Phases[0].Summary = state.PhaseRunning, ""
			b.upgradeEvent(old, state.LevelInfo, "started elsewhere", "watching update "+id+" again", "")
			b.mu.Unlock()
			b.watchClusterUpdate(ctx, old, cfg, id, started, to)
			return
		}
	}
	lu := &liveUpgrade{t: t, updateID: id, answers: make(chan bool, 1), wake: make(chan struct{}, 1)}
	lu.st = state.Upgrade{StartedElsewhere: true, Cluster: b.keyOf(t), From: from, To: to, StartedAt: started,
		Phases: []state.Phase{{Name: what, Weight: 1, Status: state.PhaseRunning, StartedAt: started,
			Items: []state.PhaseItem{{Name: "control plane", Text: from + " → " + to}}}}}
	b.upgradeEvent(lu, state.LevelInfo, "started elsewhere", "watching update "+aws.ToString(u.Id), "not started by this UI; add-ons and nodegroup rolls it runs show on the Rolls screen")
	b.emit(state.Event{Cluster: b.keyOf(t), Source: state.SourceUpgrade, Level: state.LevelProgress, Subject: "upgrade", Text: "started elsewhere · watching", Detail: from + " → " + to})
	b.upgrades = append(b.upgrades, lu)
	b.mu.Unlock()
	b.watchClusterUpdate(ctx, lu, cfg, id, started, to)
}

// watchClusterUpdate follows a control-plane update started elsewhere until
// EKS reports its end, and records it on lu.
func (b *Backend) watchClusterUpdate(ctx context.Context, lu *liveUpgrade, cfg aws.Config, id string, started time.Time, to string) {
	t := lu.t
	status, msg, err := b.roll.waitCluster(ctx, cfg, t.name, id)
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	lu.st.EndedAt = now
	ph := &lu.st.Phases[0]
	ph.EndedAt = now
	switch {
	case status == ekstypes.UpdateStatusSuccessful:
		ph.Status, ph.Progress = state.PhaseDone, 1
		doneItems(ph)
		b.upgradeEvent(lu, state.LevelOK, "control plane", "on "+to, now.Sub(started).Round(time.Second).String())
	case err != nil && ctx.Err() != nil:
		// Shutting down: say nothing.
	default:
		why := strings.TrimSpace(string(status) + " " + msg)
		if err != nil {
			why = err.Error()
		}
		ph.Status, ph.Summary = state.PhaseFailed, why
		lu.st.Failed = why
		b.upgradeEvent(lu, state.LevelError, "control plane", "update "+strings.ToLower(string(status)), msg)
	}
	b.emit(state.Event{Cluster: b.keyOf(t), Source: state.SourceUpgrade, Level: state.LevelInfo, Subject: "upgrade", Text: fmt.Sprintf("control-plane update elsewhere ended · %s", strings.ToLower(string(status)))})
}
