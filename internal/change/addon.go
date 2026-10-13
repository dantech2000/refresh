package change

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
)

// validate rejects a request that names its add-ons in two ways or in
// none, a version for more than one add-on, and a skip list without All.
func (r AddonUpdateRequest) validate() error {
	switch {
	case r.Cluster == "":
		return errors.New("no cluster to update the add-ons of")
	case r.All && len(r.Addons) > 0:
		return errors.New("name the add-ons or pass --all, not both")
	case !r.All && len(r.Addons) == 0:
		return errors.New("name at least one add-on, or pass --all")
	case r.Version != "" && (r.All || len(r.Addons) != 1):
		return errors.New("--version needs exactly one add-on")
	case len(r.Skip) > 0 && !r.All:
		return errors.New("--skip-addon needs --all")
	}
	return nil
}

// addonTarget is the version that a step moves an add-on to.
type addonTarget struct {
	// Version is --version, or the latest version that the cluster can
	// run.
	Version string
	// Pinned says that Version is --version.
	Pinned bool
	// Known is false when the catalogue could not be read.
	Known bool
	// Supported are the versions that the cluster can run, newest first,
	// for a pinned version.
	Supported []string
}

// addonDecision is what the step table says about one add-on.
type addonDecision struct {
	Action Action
	Reason Reason
	// To is the version that the step ends at.
	To string
	// Notice is a line for the user, or "".
	Notice string
	// Unread is the message of the blocker of a Blocked step.
	Unread string
	// Err fails the whole plan.
	Err error
}

// maxListedVersions is how many supported versions an error names.
const maxListedVersions = 5

// decideAddon is the step table of an add-on update. The first row that
// matches decides. "At target" means that the installed version equals
// the target, or is newer than it when the target is the latest version
// and not one that the user named.
//
//  1. --version names a version that the cluster cannot run: the plan
//     fails.
//  2. The version to move to could not be read: Blocked.
//  3. At target and ACTIVE: Skip, AlreadyAtTarget.
//  4. At target and UPDATING or CREATING: Attach, InFlight.
//  5. Newer than the latest, in a status that is not in flight: Skip,
//     NewerThanLatest. A repair would downgrade it.
//  6. At target, in a status that is not in flight: Submit at the same
//     version, Repair.
//  7. UPDATING or CREATING away from the target, or DELETING: Skip,
//     InFlight. The target is busy.
//  8. Otherwise: Submit, VersionBehind.
func decideAddon(a fleet.Addon, t addonTarget, kubernetes string) addonDecision {
	if t.Pinned && t.Known && !slices.Contains(t.Supported, t.Version) {
		return addonDecision{Err: fmt.Errorf("add-on %s version %s is not compatible with Kubernetes %s; supported versions (newest first): %s",
			a.Name, t.Version, kubernetes, versionList(t.Supported))}
	}
	if !t.Known {
		return addonDecision{Action: Blocked, Unread: fmt.Sprintf("could not read the versions of add-on %s that Kubernetes %s can run", a.Name, kubernetes)}
	}
	order := fleet.CompareAddonVersions(a.Version, t.Version)
	newer := order > 0 && !t.Pinned
	atTarget := order == 0 || newer
	changing := a.Status == "UPDATING" || a.Status == "CREATING"
	switch {
	case atTarget && a.Status == "ACTIVE":
		return addonDecision{Action: Skip, Reason: AlreadyAtTarget, To: a.Version}
	case atTarget && changing:
		return addonDecision{Action: Attach, Reason: InFlight, To: a.Version}
	case newer && a.Status != "DELETING":
		return addonDecision{
			Action: Skip, Reason: NewerThanLatest, To: a.Version,
			Notice: fmt.Sprintf("%s is %s at %s, newer than the latest catalogue version %s; not re-applied", a.Name, a.Status, a.Version, t.Version),
		}
	case atTarget && a.Status != "DELETING":
		return addonDecision{Action: Submit, Reason: Repair, To: a.Version}
	case addonInFlight(a.Status):
		return addonDecision{Action: Skip, Reason: InFlight, To: t.Version}
	}
	d := addonDecision{Action: Submit, Reason: VersionBehind, To: t.Version}
	if order > 0 {
		d.Notice = fmt.Sprintf("downgrading %s from %s to %s", a.Name, a.Version, t.Version)
	}
	return d
}

// versionList names the first versions, and how many more there are.
func versionList(versions []string) string {
	if len(versions) == 0 {
		return "none"
	}
	shown := versions[:min(len(versions), maxListedVersions)]
	s := strings.Join(shown, ", ")
	if extra := len(versions) - len(shown); extra > 0 {
		s += fmt.Sprintf(" (and %d more)", extra)
	}
	return s
}

// ClusterReadError is the error of a plan whose cluster could not be
// read at all. A cluster that does not exist is one case.
type ClusterReadError struct {
	Target
	Err error
}

func (e *ClusterReadError) Error() string {
	return "reading cluster " + e.Cluster + ": " + e.Err.Error()
}

func (e *ClusterReadError) Unwrap() error { return e.Err }

// planAddons builds the plan of an add-on update: one read of the whole
// cluster with the latest version of each target, the step of each
// add-on, the busy rule, and the gate over the add-ons that would be
// updated.
func planAddons(ctx context.Context, d Deps, req AddonUpdateRequest) (*Plan, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	clients := d.AWS.Clients(req.Region)
	need := fleet.Need{Nodegroups: true, Addons: true}
	if req.Version == "" {
		need.LatestAddons, need.LatestFor = req.All, req.Addons
	}
	snap, err := d.Fetch.Fetch(ctx, clients, req.Cluster, need)
	if err != nil {
		return nil, &ClusterReadError{Target: Target{Region: clients.Region, Cluster: req.Cluster}, Err: err}
	}
	cp := ClusterPlan{
		Target:       Target{Region: snap.Cluster.Region, Cluster: snap.Cluster.Name},
		Failures:     slices.Clone(snap.Failures),
		snapshot:     snap,
		addonTargets: map[string]addonTarget{},
	}
	names, err := addonNames(snap, req)
	if err != nil {
		return nil, err
	}
	pinned := addonTarget{Version: req.Version, Pinned: true}
	if req.Version != "" && len(names) == 1 {
		versions, err := d.Fetch.AddonVersions(ctx, clients, names[0], snap.Cluster.Version)
		pinned.Known, pinned.Supported = err == nil, versions
		if err != nil {
			f := fleet.FailureFromError(apidoc.FailureAddon, names[0], err)
			f.Cluster, f.Region = cp.Cluster, cp.Region
			cp.Failures = append(cp.Failures, f)
		}
	}
	phase := Phase{Ref: PhaseRef{Target: cp.Target, Kind: PhaseAddons}}
	for _, name := range names {
		step, err := cp.addonStep(phase.Ref, name, req, pinned)
		if err != nil {
			return nil, err
		}
		phase.Steps = append(phase.Steps, step)
	}
	cp.Phases = []Phase{phase}

	refuse, notices := Busy(snap, AddonUpdate, phase.Steps)
	cp.Blockers = append(cp.Blockers, refuse...)
	cp.Notices = append(cp.Notices, notices...)
	cp.gateAddons(ctx, d, req.Policy)
	return &Plan{Kind: AddonUpdate, Request: req, Clusters: []ClusterPlan{cp}, At: d.now()}, nil
}

// addonNames returns the add-ons that the request targets, in the order
// of their steps: the names as given, or with All every installed add-on
// but the skipped ones, in dependency order. A name that is not installed
// is an error that lists the close matches.
func addonNames(snap *fleet.Snapshot, req AddonUpdateRequest) ([]string, error) {
	installed := make([]string, len(snap.Addons))
	for i, a := range snap.Addons {
		installed[i] = a.Name
	}
	// Without the list, nothing shows which names exist. The busy rule
	// blocks the plan for the list that was not read.
	listed := snap.Have.Addons == fleet.Collected || snap.Have.Addons == fleet.Partial
	exists := func(name string) error {
		if !listed || slices.Contains(installed, name) {
			return nil
		}
		return &NotFoundError{Target: Target{Region: snap.Cluster.Region, Cluster: snap.Cluster.Name}, Kind: StepAddon, Name: name, Names: slices.Clone(installed)}
	}
	if !req.All {
		var names []string
		for _, name := range req.Addons {
			if err := exists(name); err != nil {
				return nil, err
			}
			if !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
		return names, nil
	}
	for _, name := range req.Skip {
		if err := exists(name); err != nil {
			return nil, err
		}
	}
	names := slices.DeleteFunc(installed, func(name string) bool { return slices.Contains(req.Skip, name) })
	return sortByDependency(names), nil
}

// addonStep returns the step of one add-on, and records its target, its
// notice, its blocker, and its busy failure in the plan.
func (cp *ClusterPlan) addonStep(phase PhaseRef, name string, req AddonUpdateRequest, pinned addonTarget) (Step, error) {
	snap := cp.snapshot
	step := Step{Ref: StepRef{Phase: phase, Kind: StepAddon, Name: name}}
	i := slices.IndexFunc(snap.Addons, func(a fleet.Addon) bool { return a.Name == name })
	if i < 0 || snap.Addons[i].State != fleet.Collected {
		step.Action = Blocked
		cp.Blockers = append(cp.Blockers, Blocker{Kind: BlockerUnread, Step: &step.Ref, Message: "could not read add-on " + name})
		return step, nil
	}
	a := snap.Addons[i]
	step.From = a.Version
	target := pinned
	if req.Version == "" {
		target = addonTarget{Version: a.Latest, Known: a.LatestState == fleet.Collected}
		if target.Known && target.Version == "" {
			msg := fmt.Sprintf("EKS publishes no version of add-on %s for Kubernetes %s", name, snap.Cluster.Version)
			if !req.All {
				return Step{}, errors.New(msg)
			}
			step.Action = Manual
			cp.Notices = append(cp.Notices, msg+"; not updated")
			return step, nil
		}
	}
	cp.addonTargets[name] = target
	d := decideAddon(a, target, snap.Cluster.Version.String())
	if d.Err != nil {
		return Step{}, d.Err
	}
	step.Action, step.Reason, step.To = d.Action, d.Reason, d.To
	if d.Notice != "" {
		cp.Notices = append(cp.Notices, d.Notice)
	}
	if d.Unread != "" {
		cp.Blockers = append(cp.Blockers, Blocker{Kind: BlockerUnread, Step: &step.Ref, Message: d.Unread})
	}
	if step.busy() {
		cp.Failures = append(cp.Failures, cp.busyFailure(name, a.Status, a.Version, ""))
	}
	return step, nil
}

// busyFailure returns the failure of an add-on target that is already
// changing. inFlight names the update: its type, or unknownUpdate. It is
// "" in a plan, which makes no lookup.
func (cp *ClusterPlan) busyFailure(name, status, version, inFlight string) apidoc.Failure {
	msg := fmt.Sprintf("add-on %s is %s", name, status)
	if version != "" && status != "DELETING" {
		msg += " to " + version
	}
	if inFlight != "" {
		msg += " (" + inFlight + ")"
	}
	f := apidoc.NewFailure(apidoc.FailureAddon, name, apidoc.ReasonBusy, msg+"; it was not changed. Run the command again once that finishes")
	f.Cluster, f.Region = cp.Cluster, cp.Region
	return f
}

// gateAddons runs the health gate over the add-ons that the plan would
// update. A plan that submits nothing runs no gate.
func (cp *ClusterPlan) gateAddons(ctx context.Context, d Deps, policy health.Policy) {
	var targets []string
	for _, s := range cp.Steps() {
		if s.Action == Submit {
			targets = append(targets, s.Ref.Name)
		}
	}
	if len(targets) == 0 {
		return
	}
	if policy.Skip {
		cp.Gate = health.Gate(nil, policy)
		cp.Notices = append(cp.Notices, "the health checks were skipped (--skip-health-check)")
		return
	}
	cp.Checks = health.Run(ctx, []health.Check{health.Addons(targets)}, &health.Input{
		Snapshot: cp.snapshot, Clients: d.AWS.Clients(cp.Region), Now: cp.snapshot.FetchedAt,
	})
	cp.Gate = health.Gate(cp.Checks, policy)
}
