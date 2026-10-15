package change

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/smithy-go"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
)

// planNow is the fixed clock of the planner tests.
var planNow = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

// latestFor returns the one add-on build that the catalogue of a
// planWorld has for a Kubernetes version: 1.32 gives v1.32.0-eksbuild.1.
func latestFor(k8s string) string { return "v" + k8s + ".0-eksbuild.1" }

// accessDenied is the error of a read that IAM refused.
func accessDenied() error {
	return &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "not authorized to perform the call"}
}

// planNodegroup is a nodegroup of a planWorld.
type planNodegroup struct {
	name, version string
	// status is ACTIVE when empty, and amiType AL2023 when empty.
	status, amiType string
	unread          bool
}

// planWorld is a cluster for the planner tests, and the answers to what
// a planner asks beside the snapshot. A test changes it between two
// plans, as a run changes a real cluster, so that the second plan is
// derived from the new state: that is how a run resumes.
type planWorld struct {
	name, version, status string
	policy                string
	autoMode              bool
	nodegroups            []*planNodegroup
	// addons has the installed version of each add-on.
	addons map[string]string
	// keepsCompat names the add-ons whose catalogue for a version also
	// lists the build for the minor version before it, so the new control
	// plane can still run them. The others are compatible with one minor
	// version only.
	keepsCompat map[string]bool
	// catalogue replaces the catalogue above.
	catalogue func(addon string, k fleet.Version) ([]string, error)
	// offer replaces the answer about a version: offered, in standard
	// support, up to 1.35.
	offer func(v fleet.Version) VersionOffer
	// insights are the insights that the cluster has. Without strict, a
	// hop with none gets a passing one, as EKS has after a refresh.
	insights    []apidoc.Insight
	strict      bool
	insightsErr error
	refreshErr  error
	// preview makes the insights the ones of a dry run.
	preview bool
	// updates is the update history, and noHistory makes it unread.
	updates   []fleet.Update
	noHistory bool
	now       time.Time

	// What the planner asked.
	catalogueCalls []string
	insightCalls   int
	offerCalls     []string
}

// newPlanWorld returns the cluster prod-east on 1.31 with the add-on
// vpc-cni and the nodegroup workers-a, both at 1.31.
func newPlanWorld() *planWorld {
	return &planWorld{
		name: "prod-east", version: "1.31",
		addons:     map[string]string{"vpc-cni": latestFor("1.31")},
		nodegroups: []*planNodegroup{{name: "workers-a", version: "1.31"}},
		now:        planNow,
	}
}

// worldOf returns a cluster on a version with the nodegroups, as name
// and version pairs, and no add-on.
func worldOf(clusterVersion string, nodegroups ...string) *planWorld {
	w := newPlanWorld()
	w.version, w.addons, w.nodegroups = clusterVersion, map[string]string{}, nil
	for i := 0; i < len(nodegroups); i += 2 {
		w.nodegroups = append(w.nodegroups, &planNodegroup{name: nodegroups[i], version: nodegroups[i+1]})
	}
	return w
}

// nodegroup returns a nodegroup of the world.
func (w *planWorld) nodegroup(name string) *planNodegroup {
	for _, n := range w.nodegroups {
		if n.name == name {
			return n
		}
	}
	panic("no nodegroup " + name)
}

// anyVersion is a catalogue that answers with the same versions of an
// add-on for every Kubernetes version, and with none for an add-on that
// it does not have.
func anyVersion(byAddon map[string][]string) func(string, fleet.Version) ([]string, error) {
	return func(addon string, _ fleet.Version) ([]string, error) { return byAddon[addon], nil }
}

// perVersion is a catalogue by add-on and Kubernetes version, newest
// first.
func perVersion(byAddon map[string]map[string][]string) func(string, fleet.Version) ([]string, error) {
	return func(addon string, k fleet.Version) ([]string, error) { return byAddon[addon][k.String()], nil }
}

func (w *planWorld) versions(addon string, k fleet.Version) ([]string, error) {
	w.catalogueCalls = append(w.catalogueCalls, addon+"@"+k.String())
	if w.catalogue != nil {
		return w.catalogue(addon, k)
	}
	return w.builds(addon, k), nil
}

// builds is the catalogue of the world itself: one build for each
// Kubernetes version, and for an add-on in keepsCompat also the build of
// the version before.
func (w *planWorld) builds(addon string, k fleet.Version) []string {
	out := []string{latestFor(k.String())}
	if w.keepsCompat[addon] {
		out = append(out, latestFor(fleet.Version{Major: k.Major, Minor: k.Minor - 1}.String()))
	}
	return out
}

func (w *planWorld) offered(v fleet.Version) VersionOffer {
	w.offerCalls = append(w.offerCalls, v.String())
	if w.offer != nil {
		return w.offer(v)
	}
	return VersionOffer{Offered: v.Major == 1 && v.Minor >= 28 && v.Minor <= 35, Tier: apidoc.SupportStandard}
}

func (w *planWorld) upgradeInsights(hop fleet.Version) InsightsRead {
	w.insightCalls++
	read := InsightsRead{Insights: slices.Clone(w.insights), Err: w.insightsErr, RefreshErr: w.refreshErr, Refreshed: !w.preview}
	has := slices.ContainsFunc(w.insights, func(i apidoc.Insight) bool { return i.KubernetesVersion == hop.String() })
	if !w.strict && !has {
		read.Insights = append(read.Insights, insightFor("Kubelet version skew", insightPassing, hop.String()))
	}
	return read
}

// insightFor returns an upgrade insight about a Kubernetes version.
func insightFor(name, status, k8s string) apidoc.Insight {
	return apidoc.Insight{ID: "id-" + name, Name: name, Category: "UPGRADE_READINESS", Status: status, KubernetesVersion: k8s}
}

// snapshot returns the cluster as a fetch with nodegroups, add-ons, and
// updates reads it: both lists sorted by name.
func (w *planWorld) snapshot() *fleet.Snapshot {
	snap := &fleet.Snapshot{
		Cluster: fleet.Cluster{
			Name: w.name, Region: east, Status: cmp.Or(w.status, "ACTIVE"), Version: ver(w.version),
			UpgradePolicy: w.policy, AutoMode: w.autoMode,
		},
		Have:      fleet.Have{Cluster: fleet.Collected, Nodegroups: fleet.Collected, Addons: fleet.Collected, Updates: fleet.Collected},
		Updates:   slices.Clone(w.updates),
		FetchedAt: w.now,
	}
	if w.noHistory {
		snap.Have.Updates, snap.Updates = fleet.Failed, nil
	}
	for _, n := range w.nodegroups {
		if n.unread {
			snap.Nodegroups = append(snap.Nodegroups, fleet.Nodegroup{Name: n.name, State: fleet.Failed})
			snap.Have.Nodegroups = fleet.Partial
			continue
		}
		snap.Nodegroups = append(snap.Nodegroups, fleet.Nodegroup{
			Name: n.name, State: fleet.Collected, Status: cmp.Or(n.status, "ACTIVE"),
			AMIType: cmp.Or(n.amiType, "AL2023_x86_64_STANDARD"), Version: ver(n.version),
		})
	}
	slices.SortFunc(snap.Nodegroups, func(a, b fleet.Nodegroup) int { return strings.Compare(a.Name, b.Name) })
	for name, v := range w.addons {
		snap.Addons = append(snap.Addons, fleet.Addon{Name: name, State: fleet.Collected, Status: "ACTIVE", Version: v})
	}
	slices.SortFunc(snap.Addons, func(a, b fleet.Addon) int { return strings.Compare(a.Name, b.Name) })
	return snap
}

func (w *planWorld) upgradeInputs() UpgradeInputs {
	return UpgradeInputs{Now: w.now, Version: w.offered, AddonVersions: w.versions, Insights: w.upgradeInsights}
}

// upgrade plans an upgrade of the world to a version and returns the
// plan of its cluster.
func (w *planWorld) upgrade(t *testing.T, to string, req UpgradeRequest) *ClusterPlan {
	t.Helper()
	req.Target, req.To = Target{Region: east, Cluster: w.name}, ver(to)
	p, err := PlanUpgrade(w.snapshot(), req, w.upgradeInputs())
	if err != nil {
		t.Fatalf("PlanUpgrade to %s: %v", to, err)
	}
	if p.Kind != ClusterUpgrade || len(p.Clusters) != 1 || !p.At.Equal(w.now) {
		t.Fatalf("plan = kind %s, %d clusters, at %s", p.Kind, len(p.Clusters), p.At)
	}
	checkRefs(t, &p.Clusters[0])
	return &p.Clusters[0]
}

// checkRefs fails when two steps of a plan have the same ref, when a
// step is not in the phase that its ref names, and when a blocker names
// a step that the plan does not have. Diff matches the steps and the
// blockers of two plans by these refs.
func checkRefs(t *testing.T, cp *ClusterPlan) {
	t.Helper()
	seen := map[StepRef]bool{}
	for _, ph := range cp.Phases {
		if ph.Ref.Target != cp.Target {
			t.Errorf("phase %s is not of the cluster of the plan", ph.Ref)
		}
		for _, s := range ph.Steps {
			if s.Ref.Phase != ph.Ref || seen[s.Ref] {
				t.Errorf("step %s is in phase %s, or is in the plan twice", s.Ref, ph.Ref)
			}
			seen[s.Ref] = true
		}
	}
	for _, b := range cp.Blockers {
		if b.Step != nil && !seen[*b.Step] {
			t.Errorf("the blocker %q names the step %s, which the plan does not have", b.Message, b.Step)
		}
	}
}

// upgradeErr plans an upgrade that must be refused, and returns the
// error.
func (w *planWorld) upgradeErr(t *testing.T, to string, req UpgradeRequest) error {
	t.Helper()
	req.Target, req.To = Target{Region: east, Cluster: w.name}, ver(to)
	p, err := PlanUpgrade(w.snapshot(), req, w.upgradeInputs())
	if err == nil {
		t.Fatalf("PlanUpgrade to %s returned a plan: %v", to, stepLines(&p.Clusters[0]))
	}
	return err
}

// apply does what a run of the plan does to the world: each Submit step
// in plan order, until fail reports that a step fails. It returns one
// line for each change, such as "cp→1.33", "addon vpc-cni→v1.33.0", and
// "ng workers-a→1.33", and whether every step went through. A step that
// attaches or verifies changes nothing.
func (w *planWorld) apply(cp *ClusterPlan, fail func(Step) bool) (events []string, ok bool) {
	for _, s := range cp.Steps() {
		if s.Action != Submit {
			continue
		}
		if fail != nil && fail(s) {
			return events, false
		}
		switch s.Ref.Kind {
		case StepControlPlane:
			w.version = s.To
			events = append(events, "cp→"+s.To)
		case StepAddon:
			w.addons[s.Ref.Name] = s.To
			events = append(events, "addon "+s.Ref.Name+"→"+s.To)
		case StepNodegroup:
			w.nodegroup(s.Ref.Name).version = s.To
			events = append(events, "ng "+s.Ref.Name+"→"+s.To)
		}
	}
	return events, true
}

// failAddons fails every add-on step, or the step of one add-on.
func failAddons(name string) func(Step) bool {
	return func(s Step) bool { return s.Ref.Kind == StepAddon && (name == "" || s.Ref.Name == name) }
}

// hopOf returns the phases of one hop of a plan, by the hop target.
func hopOf(cp *ClusterPlan, to string) []Phase {
	var out []Phase
	for _, ph := range cp.Phases {
		if ph.Ref.Hop == ver(to) {
			out = append(out, ph)
		}
	}
	return out
}

// hopTargets returns the hop target of each hop of the plan, in order.
func hopTargets(cp *ClusterPlan) []string {
	var out []string
	for _, ph := range cp.Phases {
		if h := ph.Ref.Hop.String(); len(out) == 0 || out[len(out)-1] != h {
			out = append(out, h)
		}
	}
	return out
}

// stepOrder lists the steps of a hop as "Kind name", in plan order, with
// a "!" on the add-on steps that go before the nodegroup rolls.
func stepOrder(cp *ClusterPlan, hop string) []string {
	var out []string
	for _, ph := range hopOf(cp, hop) {
		for _, s := range ph.Steps {
			e := string(s.Ref.Kind)
			if s.Ref.Name != "" {
				e += " " + s.Ref.Name
			}
			if ph.Ref.Kind == PhaseAddonsBefore {
				e += "!"
			}
			out = append(out, e)
		}
	}
	return out
}

// stepLines lists every step of a plan with its action, for a failure
// message.
func stepLines(cp *ClusterPlan) []string {
	var out []string
	for _, s := range cp.Steps() {
		out = append(out, fmt.Sprintf("%s %s %s %s→%s", s.Ref, s.Action, s.Reason, s.From, s.To))
	}
	return out
}

// planShape summarises a plan as one line for each hop: its target and
// the nodegroups that the hop works on, sorted.
func planShape(cp *ClusterPlan) []string {
	var out []string
	for _, hop := range hopTargets(cp) {
		var names []string
		for _, ph := range hopOf(cp, hop) {
			for _, s := range ph.Steps {
				if s.Ref.Kind == StepNodegroup && s.work() {
					names = append(names, s.Ref.Name)
				}
			}
		}
		slices.Sort(names)
		out = append(out, fmt.Sprintf("%s ng[%s]", hop, strings.Join(names, " ")))
	}
	return out
}

// findStep returns the step of a kind and a name in a hop of the plan.
// An empty hop is the plan of a rollback, whose phases have none.
func findStep(t *testing.T, cp *ClusterPlan, hop string, kind StepKind, name string) Step {
	t.Helper()
	for _, s := range cp.Steps() {
		if s.Ref.Phase.Hop.String() == hop && s.Ref.Kind == kind && s.Ref.Name == name {
			return s
		}
	}
	t.Fatalf("no %s step for %q in hop %q of %v", kind, name, hop, stepLines(cp))
	return Step{}
}

// workSteps counts the steps that a run acts on.
func workSteps(cp *ClusterPlan) int {
	n := 0
	for _, s := range cp.Steps() {
		if s.work() {
			n++
		}
	}
	return n
}

// blockerText joins the messages of the blockers of a plan.
func blockerText(cp *ClusterPlan) string {
	msgs := make([]string, len(cp.Blockers))
	for i, b := range cp.Blockers {
		msgs[i] = b.Message
	}
	return strings.Join(msgs, "\n")
}

// blockerFor returns the blocker of a step, by the kind and the name of
// the step.
func blockerFor(t *testing.T, cp *ClusterPlan, kind StepKind, name string) Blocker {
	t.Helper()
	for _, b := range cp.Blockers {
		if b.Step != nil && b.Step.Kind == kind && b.Step.Name == name {
			return b
		}
	}
	t.Fatalf("no blocker for the %s step %q in %q", kind, name, blockerText(cp))
	return Blocker{}
}
