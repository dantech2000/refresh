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
	// Force and RollbackTimeoutMinutes record what the request that started
	// the update asked for. EKS does not report them back; they are here so
	// a test can check what the product sent.
	Force                  bool
	RollbackTimeoutMinutes int32

	// op, token, and request identify the call that started the update,
	// for a retry with the same client request token. request is the
	// request as it was sent, before any default was filled in.
	op      Operation
	token   string
	request string
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

// The request types below keep, for every optional field, whether the
// request had it: a nil pointer is a field that was left out. A field that
// is set to its empty value resolves like one that was left out, but it is
// another request as far as a client request token goes.

// ClusterVersionRequest is the input of World.UpdateClusterVersion.
type ClusterVersionRequest struct {
	// Version is the target Kubernetes version. EKS requires it.
	Version *string
	// Force is the request's force flag, which a rollback sets to skip the
	// insights check.
	Force *bool
	// Rollback is the request's rollbackConfig.
	Rollback *RollbackRequest
	// Token is the client request token. A second request for the same
	// operation and resource with the token of an earlier one returns that
	// update and starts nothing, whatever happened to the resource since.
	// The requests are compared as they were sent, before defaults: if a
	// field differs, or is set in one and not in the other, the second
	// request is an *InvalidError.
	Token string
}

// RollbackRequest is the rollbackConfig of an UpdateClusterVersion request.
type RollbackRequest struct {
	TimeoutMinutes *int32
}

// NodegroupVersionRequest is the input of World.UpdateNodegroupVersion.
type NodegroupVersionRequest struct {
	// Version is the target Kubernetes version. Nil keeps the nodegroup's.
	Version *string
	// ReleaseVersion is the target AMI release. Nil means LatestRelease.
	ReleaseVersion *string
	// Force is the request's force flag: roll even when a pod disruption
	// budget blocks a drain.
	Force *bool
	// Token is the client request token. See ClusterVersionRequest.
	Token string
}

// NodegroupConfigRequest is the input of World.UpdateNodegroupConfig.
type NodegroupConfigRequest struct {
	// Scaling is the request's scalingConfig.
	Scaling *ScalingRequest
	// Token is the client request token. See ClusterVersionRequest.
	Token string
}

// ScalingRequest is the scalingConfig of an UpdateNodegroupConfig request.
// A nil size keeps the current one.
type ScalingRequest struct {
	Desired, Min, Max *int32
}

// AddonRequest is the input of World.UpdateAddon.
type AddonRequest struct {
	// Version is the target add-on version. Nil keeps the add-on's.
	Version *string
	// ConfigurationValues, when not nil, replace the add-on's configuration.
	ConfigurationValues *string
	// Token is the client request token. See ClusterVersionRequest.
	Token string
}

// sent renders an optional request field, so that a field that was left
// out differs from one that was sent empty.
func sent[T any](p *T) string {
	if p == nil {
		return "unset"
	}
	return fmt.Sprintf("%q", fmt.Sprint(*p))
}

// value returns what an optional request field holds, or the zero value.
func value[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
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
	if req.Version == nil {
		return Update{}, &InvalidError{Message: "The version parameter is required."}
	}
	rollback := "unset"
	if req.Rollback != nil {
		rollback = "{timeoutMinutes=" + sent(req.Rollback.TimeoutMinutes) + "}"
	}
	request := fmt.Sprintf("version=%s force=%s rollbackConfig=%s", sent(req.Version), sent(req.Force), rollback)
	if u, ok, err := replay(c, EKSUpdateClusterVersion, "", "", req.Token, request); ok {
		return u, err
	}
	kind := TypeVersionUpdate
	version := *req.Version
	if _, to, ok := parseVersion(version); ok {
		if _, from, ok := parseVersion(c.Version); ok && to < from {
			kind = TypeVersionRollback
		}
	}
	u := &Update{
		Type: kind, op: EKSUpdateClusterVersion, token: req.Token, request: request, Force: value(req.Force),
		Params: []UpdateParam{{Type: "Version", Value: version}},
	}
	if req.Rollback != nil {
		u.RollbackTimeoutMinutes = value(req.Rollback.TimeoutMinutes)
	}
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
	request := fmt.Sprintf("version=%s releaseVersion=%s force=%s", sent(req.Version), sent(req.ReleaseVersion), sent(req.Force))
	if u, ok, err := replay(c, EKSUpdateNodegroupVersion, ng.Name, "", req.Token, request); ok {
		return u, err
	}
	version := cmp.Or(value(req.Version), ng.Version)
	release := cmp.Or(value(req.ReleaseVersion), LatestRelease(version))
	u := &Update{
		Type: TypeVersionUpdate, Nodegroup: ng.Name, op: EKSUpdateNodegroupVersion, token: req.Token, request: request, Force: value(req.Force),
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
	request, scaling := "scalingConfig=unset", ScalingRequest{}
	if req.Scaling != nil {
		scaling = *req.Scaling
		request = fmt.Sprintf("scalingConfig={desiredSize=%s minSize=%s maxSize=%s}", sent(scaling.Desired), sent(scaling.Min), sent(scaling.Max))
	}
	if u, ok, err := replay(c, EKSUpdateNodegroupConfig, ng.Name, "", req.Token, request); ok {
		return u, err
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
	}{{"DesiredSize", &ng.Desired, scaling.Desired}, {"MinSize", &ng.Min, scaling.Min}, {"MaxSize", &ng.Max, scaling.Max}} {
		if s.to != nil {
			sizes = append(sizes, size{s.name, s.field, *s.to})
		}
	}
	u := &Update{Type: TypeConfigUpdate, Nodegroup: ng.Name, op: EKSUpdateNodegroupConfig, token: req.Token, request: request}
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
	request := fmt.Sprintf("addonVersion=%s configurationValues=%s", sent(req.Version), sent(req.ConfigurationValues))
	if u, ok, err := replay(c, EKSUpdateAddon, "", a.Name, req.Token, request); ok {
		return u, err
	}
	version := cmp.Or(value(req.Version), a.Version)
	u := &Update{
		Type: TypeAddonUpdate, Addon: a.Name, op: EKSUpdateAddon, token: req.Token, request: request,
		Params: []UpdateParam{{Type: "AddonVersion", Value: version}},
	}
	// The configuration is read now: the caller owns the pointer.
	configure := func() {}
	if req.ConfigurationValues != nil {
		values := *req.ConfigurationValues
		u.Params = append(u.Params, UpdateParam{Type: "ConfigurationValues", Value: values})
		configure = func() { a.ConfigurationValues = values }
	}
	t := target{name: a.Name, status: &a.Status, issues: &a.HealthIssues, scripts: &a.UpdateScripts, modified: &a.ModifiedAt}
	return w.start(c, u, t, func() {
		a.Version = version
		configure()
	})
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

// replay answers a request that repeats an earlier one: the same client
// request token for the same operation on the same resource. It returns the
// update the first call started, as that call saw it, and ok. A request
// that reuses the token with other fields is an *InvalidError. It runs
// before any default is resolved, so the answer does not depend on what
// happened to the resource since the first call.
func replay(c *Cluster, op Operation, nodegroup, addon, token, request string) (u Update, ok bool, err error) {
	if token == "" {
		return Update{}, false, nil
	}
	for _, old := range c.Updates {
		if old.token != token || old.op != op || old.Nodegroup != nodegroup || old.Addon != addon {
			continue
		}
		if old.request != request {
			return Update{}, true, &InvalidError{Message: "The client request token was used before with different parameters."}
		}
		started := old.snapshot()
		started.Status, started.Errors = UpdateInProgress, nil
		return started, true, nil
	}
	return Update{}, false, nil
}

// start records u as an update of t and returns it as InProgress. apply is
// the change a Successful update makes. The caller holds w.mu and has
// called replay.
func (w *World) start(c *Cluster, u *Update, t target, apply func()) (Update, error) {
	for _, old := range c.Updates {
		if old.Nodegroup != u.Nodegroup || old.Addon != u.Addon {
			continue
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
		Force: u.Force, RollbackTimeoutMinutes: u.RollbackTimeoutMinutes,
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
