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

// The EKS update types the world starts.
const (
	TypeVersionUpdate   = "VersionUpdate"
	TypeVersionRollback = "VersionRollback"
	TypeConfigUpdate    = "ConfigUpdate"
	TypeAddonUpdate     = "AddonUpdate"
)

// Update is an EKS update of a cluster, a nodegroup, or an add-on.
type Update struct {
	ID   string
	Type string
	// Status defaults to Successful for a seeded update.
	Status UpdateStatus
	// Nodegroup or Addon names what the update changes. Both are empty for
	// an update of the cluster itself.
	Nodegroup string
	Addon     string
	Params    []UpdateParam
	CreatedAt time.Time
	Errors    []UpdateError

	token  string
	script UpdateScript
	// scheduled is set for an update the world started and has not
	// finished. A seeded InProgress update is not scheduled and never ends.
	scheduled bool
	ends      time.Time
	apply     func()
	restore   func()
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
	// update changes its resource.
	Result UpdateStatus
	// Errors are what a Failed or Cancelled update reports. Empty reports
	// one error with the code Unknown.
	Errors []UpdateError
}

// ClusterVersionRequest is the input of World.UpdateClusterVersion.
type ClusterVersionRequest struct {
	Version string
	// Token is the client request token. A second request with the token of
	// an earlier one returns that update and starts nothing.
	Token string
}

// NodegroupVersionRequest is the input of World.UpdateNodegroupVersion.
type NodegroupVersionRequest struct {
	// Version is the target Kubernetes version. "" keeps the nodegroup's.
	Version string
	// ReleaseVersion is the target AMI release. "" means LatestRelease.
	ReleaseVersion string
	Token          string
}

// NodegroupConfigRequest is the input of World.UpdateNodegroupConfig. A nil
// size keeps the current one.
type NodegroupConfigRequest struct {
	Desired, Min, Max *int32
	Token             string
}

// AddonRequest is the input of World.UpdateAddon.
type AddonRequest struct {
	// Version is the target add-on version. "" keeps the add-on's.
	Version string
	Token   string
}

// UpdateClusterVersion starts a control-plane version update, or a rollback
// when the version is lower than the cluster's. It returns the update as the
// call that started it sees it: InProgress.
//
// The error is a *NotFoundError, an *InUseError when an update of the
// control plane is in progress, or an *InvalidError.
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
	u := &Update{Type: kind, token: req.Token, Params: []UpdateParam{{Type: "Version", Value: req.Version}}}
	return w.start(c, u, &c.UpdateScripts, &c.Status, func() { c.Version = req.Version })
}

// UpdateNodegroupVersion starts a nodegroup roll. See UpdateClusterVersion
// for the result.
func (w *World) UpdateNodegroupVersion(region, cluster, nodegroup string, req NodegroupVersionRequest) (Update, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	c, err := w.cluster(region, cluster)
	if err != nil {
		return Update{}, err
	}
	ng := c.Nodegroup(nodegroup)
	if ng == nil {
		return Update{}, &NotFoundError{Kind: "nodegroup", Name: nodegroup}
	}
	version := cmp.Or(req.Version, ng.Version)
	release := cmp.Or(req.ReleaseVersion, LatestRelease(version))
	u := &Update{
		Type: TypeVersionUpdate, Nodegroup: ng.Name, token: req.Token,
		Params: []UpdateParam{{Type: "Version", Value: version}, {Type: "ReleaseVersion", Value: release}},
	}
	return w.start(c, u, &ng.UpdateScripts, &ng.Status, func() { ng.Version, ng.ReleaseVersion = version, release })
}

// UpdateNodegroupConfig starts a change of a nodegroup's scaling config. See
// UpdateClusterVersion for the result.
func (w *World) UpdateNodegroupConfig(region, cluster, nodegroup string, req NodegroupConfigRequest) (Update, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	c, err := w.cluster(region, cluster)
	if err != nil {
		return Update{}, err
	}
	ng := c.Nodegroup(nodegroup)
	if ng == nil {
		return Update{}, &NotFoundError{Kind: "nodegroup", Name: nodegroup}
	}
	u := &Update{Type: TypeConfigUpdate, Nodegroup: ng.Name, token: req.Token}
	sizes := []struct {
		name string
		to   *int32
		size *int32
	}{{"DesiredSize", req.Desired, &ng.Desired}, {"MinSize", req.Min, &ng.Min}, {"MaxSize", req.Max, &ng.Max}}
	for _, s := range sizes {
		if s.to != nil {
			u.Params = append(u.Params, UpdateParam{Type: s.name, Value: fmt.Sprint(*s.to)})
		}
	}
	return w.start(c, u, &ng.UpdateScripts, &ng.Status, func() {
		for _, s := range sizes {
			if s.to != nil {
				*s.size = *s.to
			}
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
		Type: TypeAddonUpdate, Addon: a.Name, token: req.Token,
		Params: []UpdateParam{{Type: "AddonVersion", Value: version}},
	}
	return w.start(c, u, &a.UpdateScripts, &a.Status, func() { a.Version = version })
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
	c.InsightsRefresh = InsightsRefresh{Status: "IN_PROGRESS", StartedAt: w.now, EndsAt: w.now.Add(w.state.InsightsRefresh)}
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

// start records u as an update of the resource whose status field is given,
// and returns it as InProgress. scripts is the resource's script queue. The
// caller holds w.mu.
func (w *World) start(c *Cluster, u *Update, scripts *[]UpdateScript, status *string, apply func()) (Update, error) {
	target := cmp.Or(u.Nodegroup, u.Addon, c.Name)
	for _, old := range c.Updates {
		if old.Nodegroup != u.Nodegroup || old.Addon != u.Addon {
			continue
		}
		if u.token != "" && old.token == u.token {
			started := old.snapshot()
			started.Status, started.Errors = UpdateInProgress, nil
			return started, nil
		}
		if old.Status == UpdateInProgress {
			return Update{}, &InUseError{Name: target, UpdateID: old.ID}
		}
	}

	w.lastUpdate++
	u.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", w.lastUpdate)
	u.Status, u.CreatedAt = UpdateInProgress, w.now
	u.script = w.state.DefaultUpdate
	if len(*scripts) > 0 {
		u.script, *scripts = (*scripts)[0], (*scripts)[1:]
	}
	u.scheduled, u.ends = !u.script.Never, w.now.Add(u.script.After)

	before := *status
	*status = "UPDATING"
	u.apply = func() { apply(); *status = "ACTIVE" }
	u.restore = func() { *status = before }
	c.Updates = append(c.Updates, u)

	started := u.snapshot()
	w.settle()
	return started, nil
}

// snapshot returns a copy of u that shares no memory with the world.
func (u *Update) snapshot() Update {
	return Update{
		ID: u.ID, Type: u.Type, Status: u.Status, Nodegroup: u.Nodegroup, Addon: u.Addon,
		Params: slices.Clone(u.Params), CreatedAt: u.CreatedAt, Errors: slices.Clone(u.Errors),
	}
}

// settle finishes every update and insights refresh that is due, in the
// order they are due. The caller holds w.mu.
func (w *World) settle() {
	var due []*Update
	for _, c := range w.state.Clusters {
		if r := &c.InsightsRefresh; r.Status == "IN_PROGRESS" && !r.EndsAt.After(w.now) {
			r.Status = "COMPLETED"
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
		if u.Status == UpdateSuccessful {
			u.apply()
			continue
		}
		u.restore()
		u.Errors = slices.Clone(u.script.Errors)
		if len(u.Errors) == 0 {
			u.Errors = []UpdateError{{Code: "Unknown", Message: "the update did not complete"}}
		}
	}
}
