package health

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakek8s "k8s.io/client-go/kubernetes/fake"
)

// Selector kinds for a fuzzed PDB.
const (
	selAppA  = iota // app=a
	selAppB         // app=b
	selEmpty        // {}: every pod in the namespace
	selNil          // nil: no pod (policy/v1)
)

type fuzzPod struct {
	node  string // "" when unscheduled
	app   string // "" for no app label
	phase corev1.PodPhase
	ready bool
}

type fuzzPDB struct {
	allowed      int32
	sel          int
	synced       bool
	alwaysAllow  bool
	desired      int32
	matchesLabel func(app string) bool
}

type pdbScenario struct {
	ngNodes, otherNodes int
	pdbs                []fuzzPDB
	pods                []fuzzPod
}

// decodePDBScenario builds a small cluster from fuzz bytes: 1-5 nodes in
// nodegroup "ng", 0-2 nodes elsewhere, up to 3 PDBs and 24 pods in one
// namespace.
func decodePDBScenario(data []byte) pdbScenario {
	next := func() byte {
		if len(data) == 0 {
			return 0
		}
		b := data[0]
		data = data[1:]
		return b
	}
	b := next()
	s := pdbScenario{ngNodes: 1 + int(b%5), otherNodes: int(b>>4) % 3}
	for range int(next() % 4) {
		sel, flags := next(), next()
		p := fuzzPDB{
			allowed:     int32(sel>>2)%6 - 1, // -1..4
			sel:         int(sel % 4),
			synced:      flags&1 == 0,
			alwaysAllow: flags&2 != 0,
			desired:     int32(flags>>2) % 5,
		}
		switch p.sel {
		case selAppA:
			p.matchesLabel = func(app string) bool { return app == "a" }
		case selAppB:
			p.matchesLabel = func(app string) bool { return app == "b" }
		case selEmpty:
			p.matchesLabel = func(string) bool { return true }
		default:
			p.matchesLabel = func(string) bool { return false }
		}
		s.pdbs = append(s.pdbs, p)
	}
	nodes := s.ngNodes + s.otherNodes
	for len(data) >= 2 && len(s.pods) < 24 {
		where, what := next(), next()
		p := fuzzPod{app: []string{"a", "b", ""}[what%3]}
		if i := int(where) % (nodes + 1); i < s.ngNodes {
			p.node = fmt.Sprintf("ng-%d", i)
		} else if i < nodes {
			p.node = fmt.Sprintf("other-%d", i-s.ngNodes)
		}
		switch (what >> 2) % 4 {
		case 0:
			p.phase, p.ready = corev1.PodRunning, true
		case 1:
			p.phase = corev1.PodRunning
		case 2:
			p.phase = corev1.PodPending
		default:
			p.phase = corev1.PodSucceeded
		}
		s.pods = append(s.pods, p)
	}
	return s
}

// objects returns the scenario as API objects. reversed assigns pod names
// and creates PDBs in the opposite order, so a list returns them in a
// different order while the cluster is the same.
func (s pdbScenario) objects(reversed bool) []runtime.Object {
	var objs []runtime.Object
	for i := range s.ngNodes {
		objs = append(objs, ngNode(fmt.Sprintf("ng-%d", i), "ng"))
	}
	for i := range s.otherNodes {
		objs = append(objs, ngNode(fmt.Sprintf("other-%d", i), "other"))
	}
	for i, fp := range s.pods {
		n := i
		if reversed {
			n = len(s.pods) - 1 - i
		}
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: fmt.Sprintf("p%02d", n)},
			Spec:       corev1.PodSpec{NodeName: fp.node},
			Status:     corev1.PodStatus{Phase: fp.phase},
		}
		if fp.app != "" {
			pod.Labels = map[string]string{"app": fp.app}
		}
		if fp.ready {
			pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		}
		objs = append(objs, pod)
	}
	order := make([]int, len(s.pdbs))
	for i := range order {
		order[i] = i
	}
	if reversed {
		slices.Reverse(order)
	}
	for _, i := range order {
		fp := s.pdbs[i]
		var expected, healthy int32
		for _, pod := range s.pods {
			if fp.matchesLabel(pod.app) {
				expected++
				if pod.ready {
					healthy++
				}
			}
		}
		p := &policyv1.PodDisruptionBudget{
			ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: fmt.Sprintf("pdb%d", i), Generation: 2},
			Status: policyv1.PodDisruptionBudgetStatus{
				ObservedGeneration: 2,
				DisruptionsAllowed: fp.allowed,
				CurrentHealthy:     healthy,
				DesiredHealthy:     fp.desired,
				ExpectedPods:       expected,
			},
		}
		switch fp.sel {
		case selAppA:
			p.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": "a"}}
		case selAppB:
			p.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"app": "b"}}
		case selEmpty:
			p.Spec.Selector = &metav1.LabelSelector{}
		}
		if !fp.synced {
			p.Status.ObservedGeneration = 1
		}
		if fp.alwaysAllow {
			policy := policyv1.AlwaysAllow
			p.Spec.UnhealthyPodEvictionPolicy = &policy
		}
		objs = append(objs, p)
	}
	return objs
}

// coverBound is an upper bound on the pods of PDB i that a scale-down of
// "ng" can take: pods it selects on ng nodes whose eviction consults PDBs.
func (s pdbScenario) coverBound(i int) int32 {
	var n int32
	for _, p := range s.pods {
		onNG := len(p.node) > 3 && p.node[:3] == "ng-"
		if onNG && s.pdbs[i].matchesLabel(p.app) && p.phase == corev1.PodRunning {
			n++
		}
	}
	return n
}

// readyOnNG reports whether PDB i selects a Running, Ready pod on an ng
// node: such a pod is always gated by the PDB.
func (s pdbScenario) readyOnNG(i int) bool {
	for _, p := range s.pods {
		onNG := len(p.node) > 3 && p.node[:3] == "ng-"
		if onNG && s.pdbs[i].matchesLabel(p.app) && p.phase == corev1.PodRunning && p.ready {
			return true
		}
	}
	return false
}

type blockerKey struct {
	loss, coveredNodes int32
}

func blockerSet(r DrainBlockerReport) map[string]blockerKey {
	out := make(map[string]blockerKey, len(r.Blockers))
	for _, b := range r.Blockers {
		out[b.Name] = blockerKey{b.ScaleDownLoss, b.CoveredNodes}
	}
	return out
}

// FuzzScaleDownBlockers checks the `nodegroup scale --check-pdbs` gate and
// the drain-blocker report on fuzzed clusters (REF-4):
//
//   - no error and no panic;
//   - removing more nodes never unblocks a PDB, and never lowers its
//     worst-case loss;
//   - a synced PDB that allows at least as many disruptions as it has pods
//     on the nodegroup is never a scale-down blocker;
//   - a nil selector (no pods under policy/v1) never blocks, and an empty
//     selector ({}) that allows 0 blocks as soon as the nodegroup holds a
//     Ready pod;
//   - loss, covered nodes, and scale-down nodes are consistent with the
//     input;
//   - the results do not depend on the order the API lists objects in;
//   - DrainBlockers reports only at-risk PDBs, never a nil-selector one,
//     and reports a pod as multi-PDB exactly when two or more non-nil PDB
//     selectors match a running pod on the nodegroup.
func FuzzScaleDownBlockers(f *testing.F) {
	f.Add([]byte{0x02, 1, 0x00, 0, 0, 0, 1, 0}, uint8(1))                           // 3 ng nodes, app=a allowing -1, two ready pods
	f.Add([]byte{0x01, 1, 0x04, 0, 0, 0, 1, 0}, uint8(2))                           // app=a allowing 0, remove 2
	f.Add([]byte{0x03, 2, 0x02, 0, 0x03, 0, 0, 0, 0, 0}, uint8(1))                  // {} and nil selectors
	f.Add([]byte{0x14, 3, 0x0a, 0, 0x06, 1, 0x03, 2, 0, 0, 1, 1, 2, 4}, uint8(3))   // unsynced, AlwaysAllow, other nodes
	f.Add([]byte{0x04, 2, 0x02, 0, 0x00, 0, 0, 0, 0, 4, 0, 8, 0, 12}, uint8(0))     // remove 0
	f.Add([]byte{0x04, 1, 0x10, 0, 0, 0, 0, 0, 0, 0, 1, 0, 1, 0, 2, 0}, uint8(255)) // remove more than the nodegroup has
	f.Fuzz(func(t *testing.T, data []byte, remove uint8) {
		s := decodePDBScenario(data)
		ctx := context.Background()
		hc := NewChecker(nil, fakek8s.NewSimpleClientset(s.objects(false)...), nil, nil)
		hcRev := NewChecker(nil, fakek8s.NewSimpleClientset(s.objects(true)...), nil, nil)

		r := int32(remove % 8)
		report, err := hc.ScaleDownBlockers(ctx, "", "ng", r)
		if err != nil {
			t.Fatalf("ScaleDownBlockers: %v", err)
		}
		if r <= 0 {
			if len(report.Blockers) != 0 || !report.Scoped {
				t.Fatalf("remove %d: report %+v, want scoped and empty", r, report)
			}
			return
		}
		if !report.Scoped {
			t.Fatalf("report not scoped with %d ng nodes", s.ngNodes)
		}
		got := blockerSet(report)

		revReport, err := hcRev.ScaleDownBlockers(ctx, "", "ng", r)
		if err != nil {
			t.Fatalf("ScaleDownBlockers (reversed): %v", err)
		}
		if rev := blockerSet(revReport); !maps.Equal(got, rev) {
			t.Fatalf("order dependence: %v vs %v", got, rev)
		}
		checkScaleDownReport(t, s, report, r)

		// Monotonicity: one more node never unblocks or lowers the loss.
		more, err := hc.ScaleDownBlockers(ctx, "", "ng", r+1)
		if err != nil {
			t.Fatalf("ScaleDownBlockers(%d): %v", r+1, err)
		}
		moreSet := blockerSet(more)
		for name, k := range got {
			if k2, ok := moreSet[name]; !ok || k2.loss < k.loss {
				t.Fatalf("remove %d blocks %s (loss %d), remove %d gives %v", r, name, k.loss, r+1, moreSet)
			}
		}

		// Drain blockers for a roll of the same nodegroup.
		drain, err := hc.DrainBlockers(ctx, "", []string{"ng"})
		if err != nil {
			t.Fatalf("DrainBlockers: %v", err)
		}
		checkDrainReport(t, s, drain)
	})
}

// pdbIndex returns i for a PDB named "pdb<i>".
func pdbIndex(t *testing.T, name string) int {
	t.Helper()
	var i int
	if _, err := fmt.Sscanf(name, "pdb%d", &i); err != nil {
		t.Fatalf("unexpected PDB %q", name)
	}
	return i
}

// checkScaleDownReport checks each scale-down blocker against the scenario,
// and that an empty-selector PDB allowing 0 blocks when it should.
func checkScaleDownReport(t *testing.T, s pdbScenario, report DrainBlockerReport, r int32) {
	t.Helper()
	for _, b := range report.Blockers {
		i := pdbIndex(t, b.Name)
		p := s.pdbs[i]
		bound := s.coverBound(i)
		switch {
		case p.sel == selNil:
			t.Fatalf("nil-selector %s is a blocker: %+v", b.Name, b)
		case p.synced && p.allowed >= bound:
			t.Fatalf("%s allows %d with at most %d pods on the nodegroup, but blocks: %+v", b.Name, p.allowed, bound, b)
		case b.ScaleDownNodes != r:
			t.Fatalf("%s: ScaleDownNodes = %d, want %d", b.Name, b.ScaleDownNodes, r)
		case b.ScaleDownLoss > bound || b.ScaleDownLoss <= b.allowedDisruptions():
			t.Fatalf("%s: loss %d, want in (%d, %d]", b.Name, b.ScaleDownLoss, b.allowedDisruptions(), bound)
		case b.CoveredNodes < 1 || int(b.CoveredNodes) > s.ngNodes:
			t.Fatalf("%s: CoveredNodes = %d with %d ng nodes", b.Name, b.CoveredNodes, s.ngNodes)
		}
	}
	got := blockerSet(report)
	for i, p := range s.pdbs {
		name := fmt.Sprintf("pdb%d", i)
		if p.sel == selEmpty && p.synced && p.allowed <= 0 && s.readyOnNG(i) {
			if _, ok := got[name]; !ok {
				t.Fatalf("{}-selector %s allows %d and covers a Ready ng pod, but is not a blocker: %v", name, p.allowed, got)
			}
		}
	}
}

// checkDrainReport checks DrainBlockers for a roll of "ng": only at-risk,
// non-nil-selector PDBs block, and a pod is multi-PDB exactly when two or
// more non-nil selectors match a running pod on the nodegroup.
func checkDrainReport(t *testing.T, s pdbScenario, drain DrainBlockerReport) {
	t.Helper()
	for _, b := range drain.Blockers {
		if !b.AtRisk() || s.pdbs[pdbIndex(t, b.Name)].sel == selNil {
			t.Fatalf("drain blocker %+v is not at risk or has a nil selector", b)
		}
	}
	wantMulti := map[string]bool{}
	for i, pod := range s.pods {
		onNG := len(pod.node) > 3 && pod.node[:3] == "ng-"
		if !onNG || pod.phase != corev1.PodRunning {
			continue
		}
		n := 0
		for _, p := range s.pdbs {
			if p.sel != selNil && p.matchesLabel(pod.app) {
				n++
			}
		}
		if n > 1 {
			wantMulti[fmt.Sprintf("p%02d", i)] = true
		}
	}
	gotMulti := map[string]bool{}
	for _, m := range drain.MultiPDBPods {
		if len(m.PDBs) < 2 || !slices.IsSorted(m.PDBs) {
			t.Fatalf("multi-PDB pod %+v: want 2+ sorted PDB names", m)
		}
		gotMulti[m.Name] = true
	}
	if !maps.Equal(gotMulti, wantMulti) {
		t.Fatalf("multi-PDB pods = %v, want %v", gotMulti, wantMulti)
	}
}

// FuzzWorstCaseLoss checks the scale-down arithmetic on its own: the loss
// from the k fullest nodes never exceeds the total, reaches it once k covers
// every node, grows with k by less each step, and does not depend on which
// node holds which count.
func FuzzWorstCaseLoss(f *testing.F) {
	f.Add([]byte{1, 1}, uint8(1))
	f.Add([]byte{3, 0, 2, 5}, uint8(2))
	f.Add([]byte{}, uint8(3))
	f.Add([]byte{255, 255, 255}, uint8(0))
	f.Fuzz(func(t *testing.T, counts []byte, k uint8) {
		perNode := map[string]int32{}
		reversed := map[string]int32{}
		var total int32
		for i, c := range counts {
			perNode[fmt.Sprintf("n%d", i)] = int32(c)
			reversed[fmt.Sprintf("n%d", len(counts)-1-i)] = int32(c)
			total += int32(c)
		}
		kk := int(k) % (len(counts) + 2)
		loss := worstCaseLoss(perNode, kk)
		if loss < 0 || loss > total {
			t.Fatalf("worstCaseLoss(%v, %d) = %d, total %d", counts, kk, loss, total)
		}
		if kk >= len(counts) && loss != total {
			t.Fatalf("worstCaseLoss(%v, %d) = %d, want the total %d", counts, kk, loss, total)
		}
		if kk == 0 && loss != 0 {
			t.Fatalf("worstCaseLoss(%v, 0) = %d, want 0", counts, loss)
		}
		if r := worstCaseLoss(reversed, kk); r != loss {
			t.Fatalf("worstCaseLoss depends on node names: %d vs %d", loss, r)
		}
		if kk > 0 {
			prev := worstCaseLoss(perNode, kk-1)
			if prev > loss {
				t.Fatalf("worstCaseLoss(%v, %d) = %d > worstCaseLoss(k=%d) = %d", counts, kk-1, prev, kk, loss)
			}
			if kk > 1 {
				prev2 := worstCaseLoss(perNode, kk-2)
				if loss-prev > prev-prev2 {
					t.Fatalf("worstCaseLoss(%v): step %d adds %d, more than step %d (%d)", counts, kk, loss-prev, kk-1, prev-prev2)
				}
			}
		}
	})
}
