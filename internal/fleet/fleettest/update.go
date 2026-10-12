package fleettest

import (
	"cmp"
	"fmt"
	"slices"
	"time"
)

// UpdateStatus is the status of an EKS update.
type UpdateStatus string

// The EKS update statuses.
const (
	UpdateInProgress UpdateStatus = "InProgress"
	UpdateSuccessful UpdateStatus = "Successful"
	UpdateFailed     UpdateStatus = "Failed"
	UpdateCancelled  UpdateStatus = "Cancelled"
)

// UpdateType is the type of an EKS update.
type UpdateType string

// The EKS update types the world starts.
const (
	TypeVersionUpdate   UpdateType = "VersionUpdate"
	TypeVersionRollback UpdateType = "VersionRollback"
	TypeConfigUpdate    UpdateType = "ConfigUpdate"
	TypeAddonUpdate     UpdateType = "AddonUpdate"
)

// Update is an EKS update of a cluster, a nodegroup, or an add-on.
type Update struct {
	ID   string
	Type UpdateType
	// Status defaults to Successful for a seeded update.
	Status UpdateStatus
	// Nodegroup or Addon names what the update changes. Both are empty for
	// an update of the cluster itself.
	Nodegroup string
	Addon     string
	Params    []UpdateParam
	CreatedAt time.Time
	Errors    []UpdateError

	// op and token identify the call that started the update, for a retry
	// with the same client request token.
	op    Operation
	token string
	// scheduled is set for an update the world started and has not
	// finished. A seeded InProgress update is not scheduled and never ends.
	scheduled bool
	ends      time.Time
	script    UpdateScript
	finish    func(now time.Time, ok bool)
}

// UpdateParam is one parameter of an update, such as the target version.
type UpdateParam struct {
	Type  string
	Value string
}

// UpdateError is why an update failed.
type UpdateError struct {
	Code        string
	Message     string
	ResourceIDs []string
}

// UpdateScript says how an update ends.
type UpdateScript struct {
	// After is how long the update stays InProgress on the world's clock.
	// Zero ends it at once: the call that starts it still answers
	// InProgress, and the next read sees the result.
	After time.Duration
	// Never keeps the update InProgress whatever the clock reads.
	Never bool
	// Result is the final status. "" means Successful. Only a Successful
	// update applies its change to the resource.
	Result UpdateStatus
	// Errors are what a Failed or Cancelled update reports. Empty reports
	// one error with the code Unknown.
	Errors []UpdateError
	// ResourceStatus is the status the resource has when the update ends,
	// whatever the result. "" means ACTIVE after a Successful update and
	// the status from before the update otherwise. Set it for an update
	// that fails part way, such as a nodegroup left DEGRADED or an add-on
	// left UPDATE_FAILED.
	ResourceStatus ResourceStatus
	// ResourceIssues, when not nil, replace the health issues of the
	// resource when the update ends.
	ResourceIssues []Issue
}

func (s UpdateScript) clone() UpdateScript {
	s.Errors = cloneErrors(s.Errors)
	s.ResourceIssues = cloneIssues(s.ResourceIssues)
	return s
}

func cloneErrors(errs []UpdateError) []UpdateError {
	out := slices.Clone(errs)
	for i := range out {
		out[i].ResourceIDs = slices.Clone(out[i].ResourceIDs)
	}
	return out
}

func cloneIssues(issues []Issue) []Issue {
	out := slices.Clone(issues)
	for i := range out {
		out[i].ResourceIDs = slices.Clone(out[i].ResourceIDs)
	}
	return out
}

// ClusterVersionRequest is the input of World.UpdateClusterVersion.
type ClusterVersionRequest struct {
	Version string
	// Token is the client request token. A second request for the same
	// operation and resource with the token of an earlier one returns that
	// update and starts nothing. If its other fields differ from the
	// earlier request, it is an *InvalidError.
	Token string
}

// NodegroupVersionRequest is the input of World.UpdateNodegroupVersion.
type NodegroupVersionRequest struct {
	// Version is the target Kubernetes version. "" keeps the nodegroup's.
	Version string
	// ReleaseVersion is the target AMI release. "" means LatestRelease.
	ReleaseVersion string
	// Token is the client request token. See ClusterVersionRequest.
	Token string
}

// NodegroupConfigRequest is the input of World.UpdateNodegroupConfig. A nil
// size keeps the current one.
type NodegroupConfigRequest struct {
	Desired, Min, Max *int32
	// Token is the client request token. See ClusterVersionRequest.
	Token string
}

// AddonRequest is the input of World.UpdateAddon.
type AddonRequest struct {
	// Version is the target add-on version. "" keeps the add-on's.
	Version string
	// Token is the client request token. See ClusterVersionRequest.
	Token string
}

// target is the resource an update changes.
type target struct {
	name     string
	status   *ResourceStatus
	issues   *[]Issue
	scripts  *[]UpdateScript
	modified *time.Time // nil for a cluster, which has no such field
}

// UpdateClusterVersion starts a control-plane version update, or a rollback
// when the version is lower than the cluster's. It returns the update as the
// call that started it sees it: InProgress.
//
// The error is a *NotFoundError, an *InUseError when the control plane has
// an update in progress, or an *InvalidError.
func (w *World) UpdateClusterVersion(region, cluster string, req ClusterVersionRequest) (Update, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	c, err := w.cluster(region, cluster)
	if err != nil {
		return Update{}, err
	}
	// EKS rejects other lengths. A real upgrade failed on a 32-character
	// token.
	if n := len(req.Token); req.Token != "" && (n < 33 || n > 126) {
		return Update{}, &InvalidError{Message: "The client request token parameter must be between 33 and 126 characters."}
	}
	kind := TypeVersionUpdate
	if _, to, ok := parseVersion(req.Version); ok {
		if _, from, ok := parseVersion(c.Version); ok && to < from {
			kind = TypeVersionRollback
		}
	}
	version := req.Version
	u := &Update{Type: kind, op: EKSUpdateClusterVersion, token: req.Token, Params: []UpdateParam{{Type: "Version", Value: version}}}
	t := target{name: c.Name, status: &c.Status, issues: &c.HealthIssues, scripts: &c.UpdateScripts}
	return w.start(c, u, t, func() { c.Version = version })
}

// UpdateNodegroupVersion starts a nodegroup roll. See UpdateClusterVersion
// for the result.
func (w *World) UpdateNodegroupVersion(region, cluster, nodegroup string, req NodegroupVersionRequest) (Update, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	c, ng, err := w.nodegroup(region, cluster, nodegroup)
	if err != nil {
		return Update{}, err
	}
	version := cmp.Or(req.Version, ng.Version)
	release := cmp.Or(req.ReleaseVersion, LatestRelease(version))
	u := &Update{
		Type: TypeVersionUpdate, Nodegroup: ng.Name, op: EKSUpdateNodegroupVersion, token: req.Token,
		Params: []UpdateParam{{Type: "Version", Value: version}, {Type: "ReleaseVersion", Value: release}},
	}
	return w.start(c, u, ng.target(), func() { ng.Version, ng.ReleaseVersion = version, release })
}

// UpdateNodegroupConfig starts a change of a nodegroup's scaling config. See
// UpdateClusterVersion for the result.
func (w *World) UpdateNodegroupConfig(region, cluster, nodegroup string, req NodegroupConfigRequest) (Update, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	c, ng, err := w.nodegroup(region, cluster, nodegroup)
	if err != nil {
		return Update{}, err
	}
	// The sizes are read now: the caller owns the pointers.
	type size struct {
		name  string
		field *int32
		to    int32
	}
	var sizes []size
	for _, s := range []struct {
		name  string
		field *int32
		to    *int32
	}{{"DesiredSize", &ng.Desired, req.Desired}, {"MinSize", &ng.Min, req.Min}, {"MaxSize", &ng.Max, req.Max}} {
		if s.to != nil {
			sizes = append(sizes, size{s.name, s.field, *s.to})
		}
	}
	u := &Update{Type: TypeConfigUpdate, Nodegroup: ng.Name, op: EKSUpdateNodegroupConfig, token: req.Token}
	for _, s := range sizes {
		u.Params = append(u.Params, UpdateParam{Type: s.name, Value: fmt.Sprint(s.to)})
	}
	return w.start(c, u, ng.target(), func() {
		for _, s := range sizes {
			*s.field = s.to
		}
	})
}

// UpdateAddon starts an add-on update. See UpdateClusterVersion for the
// result.
func (w *World) UpdateAddon(region, cluster, addon string, req AddonRequest) (Update, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	c, err := w.cluster(region, cluster)
	if err != nil {
		return Update{}, err
	}
	a := c.Addon(addon)
	if a == nil {
		return Update{}, &NotFoundError{Kind: "addon", Name: addon}
	}
	version := cmp.Or(req.Version, a.Version)
	u := &Update{
		Type: TypeAddonUpdate, Addon: a.Name, op: EKSUpdateAddon, token: req.Token,
		Params: []UpdateParam{{Type: "AddonVersion", Value: version}},
	}
	t := target{name: a.Name, status: &a.Status, issues: &a.HealthIssues, scripts: &a.UpdateScripts, modified: &a.ModifiedAt}
	return w.start(c, u, t, func() { a.Version = version })
}

// StartInsightsRefresh starts an insights refresh of the cluster. It stays
// IN_PROGRESS for State.InsightsRefresh on the world's clock.
func (w *World) StartInsightsRefresh(region, cluster string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	c, err := w.cluster(region, cluster)
	if err != nil {
		return err
	}
	c.InsightsRefresh = InsightsRefresh{Status: RefreshInProgress, StartedAt: w.now, EndsAt: w.now.Add(w.state.InsightsRefresh)}
	w.settle()
	return nil
}

func (w *World) cluster(region, name string) (*Cluster, error) {
	c := w.state.Cluster(region, name)
	if c == nil {
		return nil, &NotFoundError{Kind: "cluster", Name: name}
	}
	return c, nil
}

func (w *World) nodegroup(region, cluster, name string) (*Cluster, *Nodegroup, error) {
	c, err := w.cluster(region, cluster)
	if err != nil {
		return nil, nil, err
	}
	ng := c.Nodegroup(name)
	if ng == nil {
		return nil, nil, &NotFoundError{Kind: "nodegroup", Name: name}
	}
	return c, ng, nil
}

func (ng *Nodegroup) target() target {
	return target{name: ng.Name, status: &ng.Status, issues: &ng.HealthIssues, scripts: &ng.UpdateScripts, modified: &ng.ModifiedAt}
}

// start records u as an update of t and returns it as InProgress. apply is
// the change a Successful update makes. The caller holds w.mu.
func (w *World) start(c *Cluster, u *Update, t target, apply func()) (Update, error) {
	for _, old := range c.Updates {
		if old.Nodegroup != u.Nodegroup || old.Addon != u.Addon {
			continue
		}
		// A token names one call: the same operation on the same resource.
		if u.token != "" && old.token == u.token && old.op == u.op {
			if !slices.Equal(old.Params, u.Params) {
				return Update{}, &InvalidError{Message: "The client request token was used before with different parameters."}
			}
			started := old.snapshot()
			started.Status, started.Errors = UpdateInProgress, nil
			return started, nil
		}
		if old.Status == UpdateInProgress {
			return Update{}, &InUseError{Name: t.name, UpdateID: old.ID}
		}
	}

	w.lastUpdate++
	u.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", w.lastUpdate)
	u.Status, u.CreatedAt = UpdateInProgress, w.now
	u.script = w.state.DefaultUpdate.clone()
	if len(*t.scripts) > 0 {
		u.script, *t.scripts = (*t.scripts)[0].clone(), (*t.scripts)[1:]
	}
	u.scheduled, u.ends = !u.script.Never, w.now.Add(u.script.After)

	before := *t.status
	*t.status = StatusUpdating
	script := u.script
	u.finish = func(now time.Time, ok bool) {
		*t.status = before
		if ok {
			apply()
			*t.status = StatusActive
			if t.modified != nil {
				*t.modified = now
			}
		}
		if script.ResourceStatus != "" {
			*t.status = script.ResourceStatus
		}
		if script.ResourceIssues != nil {
			*t.issues = cloneIssues(script.ResourceIssues)
		}
	}
	c.Updates = append(c.Updates, u)

	started := u.snapshot()
	w.settle()
	return started, nil
}

// snapshot returns a copy of u that shares no memory with the world.
func (u *Update) snapshot() Update {
	return Update{
		ID: u.ID, Type: u.Type, Status: u.Status, Nodegroup: u.Nodegroup, Addon: u.Addon,
		Params: slices.Clone(u.Params), CreatedAt: u.CreatedAt, Errors: cloneErrors(u.Errors),
	}
}

// settle finishes every update and insights refresh that is due, in the
// order they are due. The caller holds w.mu.
func (w *World) settle() {
	var due []*Update
	for _, c := range w.state.Clusters {
		if r := &c.InsightsRefresh; r.Status == RefreshInProgress && !r.EndsAt.After(w.now) {
			r.Status = RefreshCompleted
		}
		for _, u := range c.Updates {
			if u.scheduled && !u.ends.After(w.now) {
				due = append(due, u)
			}
		}
	}
	slices.SortStableFunc(due, func(a, b *Update) int {
		return cmp.Or(a.ends.Compare(b.ends), cmp.Compare(a.ID, b.ID))
	})
	for _, u := range due {
		u.scheduled = false
		u.Status = cmp.Or(u.script.Result, UpdateSuccessful)
		ok := u.Status == UpdateSuccessful
		u.finish(w.now, ok)
		if ok {
			continue
		}
		u.Errors = cloneErrors(u.script.Errors)
		if len(u.Errors) == 0 {
			u.Errors = []UpdateError{{Code: "Unknown", Message: "the update did not complete"}}
		}
	}
}
