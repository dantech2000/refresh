package change

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
)

// The parity traces record what the old binary did in each scenario of
// test/parity, and the document that it printed has the plan that it
// derived. The tests below build the world of each scenario by hand, plan
// it, and compare the plan with the one in the trace, step for step. So
// the planners are pinned to what the old binary planned, and not only to
// the old unit tests.
const parityTraces = "../../test/parity/testdata/legacy"

// legacyStep is a step of a plan of the old binary.
type legacyStep struct {
	Type             string `json:"type"`
	Target           string `json:"target"`
	Version          string `json:"version"`
	Status           string `json:"status"`
	BeforeNodegroups bool   `json:"beforeNodegroups"`
}

// legacyPlan is the plan of the old binary: an UpgradePlan with hops, or
// a RollbackPlan with steps.
type legacyPlan struct {
	CurrentVersion string `json:"currentVersion"`
	TargetVersion  string `json:"targetVersion"`
	Hops           []struct {
		From, To string
		Steps    []legacyStep
	} `json:"hops"`
	Steps      []legacyStep `json:"steps"`
	UpgradedAt *string      `json:"upgradedAt"`
}

// legacyTrace is what the tests read of a trace.
type legacyTrace struct {
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
}

// readTrace returns the plan in the trace of a scenario, whether the
// document is the plan itself, and the exit code of the old binary. The
// plan is nil when the old binary printed no document: it stopped at a
// prompt, or refused to run unattended.
func readTrace(t *testing.T, name string) (plan *legacyPlan, isPlanDocument bool, exit int) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(parityTraces, name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var trace legacyTrace
	if err := json.Unmarshal(raw, &trace); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if !strings.HasPrefix(strings.TrimSpace(trace.Stdout), "{") {
		return nil, false, trace.ExitCode
	}
	var doc struct {
		Kind string      `json:"kind"`
		Plan *legacyPlan `json:"plan"`
	}
	if err := json.Unmarshal([]byte(trace.Stdout), &doc); err != nil {
		t.Fatalf("%s: the document: %v", name, err)
	}
	if doc.Plan != nil {
		return doc.Plan, false, trace.ExitCode
	}
	plan = &legacyPlan{}
	if err := json.Unmarshal([]byte(trace.Stdout), plan); err != nil {
		t.Fatalf("%s: the plan: %v", name, err)
	}
	return plan, true, trace.ExitCode
}

// parityRow is one step as both planners describe it: the kind, the
// name, the version that the step ends at, and what a run does with it.
type parityRow struct {
	Kind, Name, To, Status string
	// Before marks an add-on step that goes before the nodegroup rolls.
	Before bool
}

// legacyRows returns the steps of an old plan without the readiness
// steps, which are blockers of the plan now, and whether one of its steps
// is blocked.
func legacyRows(steps []legacyStep) (rows []parityRow, blocked bool) {
	for _, s := range steps {
		blocked = blocked || s.Status == "Blocked"
		if s.Type != "Readiness" {
			rows = append(rows, parityRow{Kind: s.Type, Name: s.Target, To: s.Version, Status: s.Status, Before: s.BeforeNodegroups})
		}
	}
	return rows, blocked
}

// planRows returns the steps of phases as rows, with the status that the
// old plan had for the action of each. want is the action of a pending
// step.
func planRows(t *testing.T, phases []Phase, want func(Step) Action) []parityRow {
	t.Helper()
	var rows []parityRow
	for _, ph := range phases {
		for _, s := range ph.Steps {
			row := parityRow{Kind: string(s.Ref.Kind), Name: s.Ref.Name, To: s.To, Before: ph.Ref.Kind == PhaseAddonsBefore}
			switch s.Action {
			case Skip:
				row.Status = "Completed"
			case Manual, Blocked:
				row.Status = string(s.Action)
			default:
				row.Status = "Pending"
				// The old plan said in words which pending step attaches.
				// The scenario says which one it is.
				if w := want(s); s.Action != w {
					t.Errorf("step %s is %s, want %s", s.Ref, s.Action, w)
				}
			}
			rows = append(rows, row)
		}
	}
	return rows
}

// parityWorld is the fleet of the parity scenarios, as fleet() seeds it
// in test/parity/seeds.go: the cluster prod on 1.32 with the nodegroups
// ng-a and ng-b, vpc-cni one version behind, coredns on the latest, and a
// catalogue with the add-on builds for 1.31 to 1.34.
func parityWorld() *planWorld {
	w := worldOf("1.32", "ng-a", "1.32", "ng-b", "1.32")
	w.name, w.policy = "prod", "EXTENDED"
	w.addons = map[string]string{"vpc-cni": "v1.18.0-eksbuild.1", "coredns": "v1.11.4-eksbuild.2"}
	builds := []struct {
		addon, version string
		clusters       []string
	}{
		{"vpc-cni", "v1.21.0-eksbuild.1", []string{"1.34"}},
		{"vpc-cni", "v1.20.0-eksbuild.1", []string{"1.33"}},
		{"vpc-cni", "v1.19.0-eksbuild.1", []string{"1.31", "1.32", "1.33"}},
		{"vpc-cni", "v1.18.0-eksbuild.1", []string{"1.31", "1.32"}},
		{"coredns", "v1.13.0-eksbuild.1", []string{"1.34"}},
		{"coredns", "v1.12.0-eksbuild.1", []string{"1.33"}},
		{"coredns", "v1.11.4-eksbuild.2", []string{"1.31", "1.32"}},
		{"coredns", "v1.11.3-eksbuild.1", []string{"1.31", "1.32"}},
	}
	w.catalogue = func(addon string, k fleet.Version) ([]string, error) {
		var out []string
		for _, b := range builds {
			if b.addon == addon && slices.Contains(b.clusters, k.String()) {
				out = append(out, b.version)
			}
		}
		slices.SortFunc(out, func(a, b string) int { return fleet.CompareAddonVersions(b, a) })
		return out, nil
	}
	w.offer = func(v fleet.Version) VersionOffer {
		switch v.String() {
		case "1.34", "1.33", "1.32":
			return VersionOffer{Offered: true, Tier: apidoc.SupportStandard}
		case "1.31":
			return VersionOffer{Offered: true, Tier: apidoc.SupportExtended}
		}
		return VersionOffer{}
	}
	return w
}

// The edits of the seed, as in test/parity/seeds.go and scenarios.go.

// upgradedAgo moves the fleet to 1.33, as after an upgrade to 1.33 that
// long ago.
func upgradedAgo(ago time.Duration) func(w *planWorld) {
	return func(w *planWorld) {
		w.version = "1.33"
		for _, n := range w.nodegroups {
			n.version = "1.33"
		}
		w.addons["vpc-cni"], w.addons["coredns"] = "v1.20.0-eksbuild.1", "v1.12.0-eksbuild.1"
		w.updates = []fleet.Update{versionUpdate("11111111-1111-4111-8111-111111111111", "VersionUpdate", "1.33", w.now.Add(-ago))}
	}
}

// rolledBackNodes puts the nodegroups and the add-ons of an upgraded
// fleet back on 1.32.
func rolledBackNodes(w *planWorld) {
	for _, n := range w.nodegroups {
		n.version = "1.32"
	}
	w.addons["vpc-cni"], w.addons["coredns"] = "v1.19.0-eksbuild.1", "v1.11.4-eksbuild.2"
}

// blockingInsight gives the cluster an ERROR insight for the next minor
// version.
func blockingInsight(w *planWorld) {
	w.insights = append(w.insights, insightFor("Deprecated APIs removed in Kubernetes v1.33", insightError, "1.33"))
}

// updatingTo starts an update of the control plane, as Prepare does in
// the attach scenarios: the cluster is UPDATING at its old version, with
// the update in its history.
func updatingTo(typ, to string) func(w *planWorld) {
	return func(w *planWorld) {
		w.status = "UPDATING"
		u := versionUpdate("00000000-0000-4000-8000-000000000001", typ, to, w.now)
		u.Status = "InProgress"
		w.updates = append(w.updates, u)
	}
}

// parityScenario is one scenario of test/parity: the edits of its seed,
// and the request of its command line.
type parityScenario struct {
	edits []func(w *planWorld)
	// to and upgrade are the request of cluster upgrade. to is 1.33 when
	// empty.
	to      string
	upgrade UpgradeRequest
	// preview is a --dry-run.
	preview  bool
	rollback RollbackRequest
	// controlPlane is what the plan does with the control plane: Submit
	// when empty.
	controlPlane Action
	// noDocument: the old binary printed no document.
	noDocument bool
	// verify are the nodegroups whose pending step is Verify.
	verify []string
}

func edits(e ...func(w *planWorld)) []func(w *planWorld) { return e }

// action is the action of a pending step of the scenario's plan.
func (sc parityScenario) action(s Step) Action {
	switch {
	case s.Ref.Kind == StepControlPlane && sc.controlPlane != "":
		return sc.controlPlane
	case s.Ref.Kind == StepNodegroup && slices.Contains(sc.verify, s.Ref.Name):
		return Verify
	}
	return Submit
}

// TestPlanUpgrade_ParityTraces plans every cluster-upgrade scenario and
// compares the plan with the one in its trace.
func TestPlanUpgrade_ParityTraces(t *testing.T) {
	base := parityScenario{}
	attachOnly := parityScenario{
		edits: edits(updatingTo("VersionUpdate", "1.33")), controlPlane: Attach,
		upgrade: UpgradeRequest{Only: []Part{PartControlPlane}},
	}
	silent := parityScenario{noDocument: true}
	scenarios := map[string]parityScenario{
		"yes": base, "addon-failed": base, "addon-updated-elsewhere": base, "deadline": base, "failed": base,
		"interrupt": base, "interrupt-nodegroup": base, "kube-yes": base, "lost-response": base,
		"nodegroup-degrades-mid-run": base, "nodegroup-failed": base, "pdb-blocked": base,
		"pdb-blocks-second-nodegroup": base, "pdb-forced": base, "pdbs-unreadable": base,
		"pdbs-unreadable-forced": base, "skip-health-check": base, "throttled-read": base,
		"dry-run":    {preview: true},
		"prompt-yes": silent, "prompt-no": silent, "prompt-interrupt": silent, "unattended": silent,
		"blocked":                  {edits: edits(blockingInsight)},
		"blocked-on-second-page":   {edits: edits(blockingInsight)},
		"skip-insights":            {edits: edits(blockingInsight), upgrade: UpgradeRequest{SkipInsights: true}},
		"attach":                   attachOnly,
		"attach-failed":            attachOnly,
		"attach-update-not-listed": attachOnly,
		"attach-continue":          {edits: edits(updatingTo("VersionUpdate", "1.33")), controlPlane: Attach},
		"two-hops":                 {to: "1.34"},
		"only-control-plane":       {upgrade: UpgradeRequest{Only: []Part{PartControlPlane}}},
		"skip-addon":               {upgrade: UpgradeRequest{SkipAddons: []string{"coredns"}}},
		"skip-nodegroup":           {upgrade: UpgradeRequest{SkipNodegroups: []string{"ng-b"}}},
		"one-nodegroup":            {upgrade: UpgradeRequest{Nodegroups: []string{"ng-a"}}},
		// Everything is at 1.33, and the roll of ng-a of an earlier run
		// is still in flight.
		"resume-unsettled-nodegroup": {verify: []string{"ng-a"}, edits: edits(upgradedAgo(24*time.Hour), func(w *planWorld) {
			w.nodegroups[0].status = "UPDATING"
		})},
		// The control plane and the add-ons are done, and the nodegroups
		// are not.
		"resume": {edits: edits(func(w *planWorld) {
			w.version = "1.33"
			w.addons["vpc-cni"], w.addons["coredns"] = "v1.20.0-eksbuild.1", "v1.12.0-eksbuild.1"
		})},
	}
	for _, name := range traceNames(t, "cluster-upgrade-", scenarios) {
		sc := scenarios[name]
		t.Run(name, func(t *testing.T) {
			old, isPlan, exit := readTrace(t, "cluster-upgrade-"+name)
			if (old == nil) != sc.noDocument {
				t.Fatalf("the trace has a plan: %v; the scenario says no document: %v", old != nil, sc.noDocument)
			}
			if old == nil {
				return
			}
			w := parityWorld()
			for _, e := range sc.edits {
				e(w)
			}
			w.preview = sc.preview
			to := sc.to
			if to == "" {
				to = "1.33"
			}
			cp := w.upgrade(t, to, sc.upgrade)

			if cp.From.String() != old.CurrentVersion || cp.To.String() != old.TargetVersion {
				t.Errorf("plan from %s to %s, the trace has %s to %s", cp.From, cp.To, old.CurrentVersion, old.TargetVersion)
			}
			var hops []string
			blocked := false
			for _, h := range old.Hops {
				hops = append(hops, h.To)
				want, b := legacyRows(h.Steps)
				blocked = blocked || b
				if diff := cmp.Diff(want, planRows(t, hopOf(cp, h.To), sc.action)); diff != "" {
					t.Errorf("steps of the hop to %s (-trace +plan):\n%s", h.To, diff)
				}
			}
			if got := hopTargets(cp); !slices.Equal(got, hops) {
				t.Errorf("hops = %v, the trace has %v", got, hops)
			}
			if got := len(cp.Blockers) > 0; got != blocked {
				t.Errorf("blockers = %q, the trace is blocked: %v", blockerText(cp), blocked)
			}
			// A trace whose document is the plan is a dry run or a run
			// that was refused before any update: its exit code is the
			// one of the plan.
			if isPlan && cp.ExitCode() != exit {
				t.Errorf("exit code of the plan = %d, the old binary exited %d", cp.ExitCode(), exit)
			}
		})
	}
}

// TestPlanRollback_ParityTraces plans every cluster-rollback scenario
// and compares the plan with the one in its trace.
func TestPlanRollback_ParityTraces(t *testing.T) {
	upgraded := upgradedAgo(24 * time.Hour)
	base := parityScenario{edits: edits(upgraded)}
	attach := parityScenario{edits: edits(upgraded, rolledBackNodes, updatingTo("VersionRollback", "1.32")), controlPlane: Attach}
	silent := parityScenario{noDocument: true}
	scenarios := map[string]parityScenario{
		"yes": base, "dry-run": base, "deadline": base, "failed": base, "interrupt": base,
		"kube-yes": base, "lost-response": base, "nodegroup-failed": base, "pdb-blocked": base,
		"pdb-blocks-second-nodegroup": base, "pdb-forced": base, "pdbs-unreadable": base,
		"pdbs-unreadable-forced": base, "throttled-read": base,
		"prompt-yes": silent, "prompt-no": silent, "prompt-interrupt": silent, "unattended": silent,
		// It runs on a terminal: the signal lands after the answer.
		"interrupt-before-start": silent,
		// With --yes: the signal lands at the second read of the insights.
		"interrupt-before-start-yes": base,
		// A cluster with no in-place upgrade in its history.
		"blocked": {},
		// The upgrade was ten days ago, and EKS allows a rollback for
		// seven.
		"expired":           {edits: edits(upgradedAgo(10 * 24 * time.Hour))},
		"force-and-timeout": {edits: edits(upgraded), rollback: RollbackRequest{SkipInsights: true, RollbackTimeout: 3 * time.Hour}},
		// The nodegroups and the add-ons are back, and the control plane
		// is not.
		"resume":        {edits: edits(upgraded, rolledBackNodes)},
		"attach":        attach,
		"attach-failed": attach,
	}
	for _, name := range traceNames(t, "cluster-rollback-", scenarios) {
		sc := scenarios[name]
		t.Run(name, func(t *testing.T) {
			old, isPlan, exit := readTrace(t, "cluster-rollback-"+name)
			if (old == nil) != sc.noDocument {
				t.Fatalf("the trace has a plan: %v; the scenario says no document: %v", old != nil, sc.noDocument)
			}
			if old == nil {
				return
			}
			w := parityWorld()
			for _, e := range sc.edits {
				e(w)
			}
			cp := w.rollback(t, sc.rollback)

			if cp.From.String() != old.CurrentVersion || cp.To.String() != old.TargetVersion {
				t.Errorf("plan from %s to %s, the trace has %s to %s", cp.From, cp.To, old.CurrentVersion, old.TargetVersion)
			}
			want, blocked := legacyRows(old.Steps)
			if diff := cmp.Diff(want, planRows(t, cp.Phases, sc.action)); diff != "" {
				t.Errorf("steps (-trace +plan):\n%s", diff)
			}
			if got := len(cp.Blockers) > 0; got != blocked {
				t.Errorf("blockers = %q, the trace is blocked: %v", blockerText(cp), blocked)
			}
			// The old plan had the dates of the window when it found the
			// upgrade.
			if got := cp.Rollback != nil; got != (old.UpgradedAt != nil) {
				t.Errorf("the plan has the upgrade: %v; the trace has it: %v", got, old.UpgradedAt != nil)
			}
			if s := findStep(t, cp, "", StepControlPlane, ""); s.Force != sc.rollback.SkipInsights {
				t.Errorf("control-plane step has force %v", s.Force)
			}
			if isPlan && cp.ExitCode() != exit {
				t.Errorf("exit code of the plan = %d, the old binary exited %d", cp.ExitCode(), exit)
			}
		})
	}
}

// traceNames returns the scenarios of the traces with a prefix, without
// the prefix, sorted. It fails for a trace that has no scenario here and
// for a scenario that has no trace, so a new trace needs a row.
func traceNames[V any](t *testing.T, prefix string, scenarios map[string]V) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(parityTraces, prefix+"*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no trace with the prefix %s in %s: %v", prefix, parityTraces, err)
	}
	var names []string
	for _, f := range files {
		name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), prefix), ".json")
		if _, ok := scenarios[name]; !ok {
			t.Errorf("the trace %s%s has no scenario in this test", prefix, name)
			continue
		}
		names = append(names, name)
	}
	for name := range scenarios {
		if !slices.Contains(names, name) {
			t.Errorf("the scenario %s has no trace %s%s.json", name, prefix, name)
		}
	}
	slices.Sort(names)
	return names
}
