package view

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dantech2000/refresh/internal/noderoll"
)

// midRoll is a roll of three nodes with one replaced: one old node
// drains, one new node is Ready, and one joins.
func midRoll() RollProgress {
	return RollProgress{
		Nodegroup: "spot-burst", OldVersion: "1.33", NewVersion: "1.34", Watching: true,
		Snapshot: noderoll.Snapshot{
			Total: 4, ReadyTarget: 1, Draining: 1, Joining: 1,
			Nodes: []noderoll.NodeView{
				{Name: "ip-1", Ready: true, Phase: noderoll.PhaseDraining, Pods: 1, PodsTotal: 4},
				{Name: "ip-2", Ready: true, Phase: noderoll.PhaseReady},
				{Name: "ip-3", OnTarget: true, Ready: true, Phase: noderoll.PhaseReady},
				{Name: "ip-4", OnTarget: true, Phase: noderoll.PhaseJoining},
			},
		},
		Recent: []noderoll.Event{
			{Node: "ip-3", Kind: noderoll.EvtOnline},
			{Node: "ip-1", Kind: noderoll.EvtDraining},
		},
	}
}

func TestRollPanel_MidRoll(t *testing.T) {
	want := []string{
		"* rolling spot-burst   1.33 -> 1.34   2m30s",
		"[#####-----------]  1/3 replaced, 1 new ready",
		"",
		"       NODE            OLD/NEW  STATE",
		"  [!]  ip-1            old      draining, evicting [###--] 3/4 pods",
		"  [OK] ip-2            old      Ready",
		"  [OK] ip-3            new      Ready",
		"  [~]  ip-4            new      joining (NotReady)",
		"",
		"events",
		"    [OK] ip-3  came online (Ready)",
		"    [!] ip-1  cordoned, draining",
	}
	if diff := cmp.Diff(want, RollPanel(Theme{}, midRoll(), 3, 150*time.Second)); diff != "" {
		t.Errorf("RollPanel (-want +got):\n%s", diff)
	}
	unicode := []string{
		"⠋ rolling spot-burst   1.33 → 1.34   2m30s",
		"▕█████░░░░░░░░░░░▏  1/3 replaced, 1 new ready",
		"",
		"    NODE            OLD/NEW  STATE",
		"  ▲ ip-1            old      draining, evicting ▕███░░▏ 3/4 pods",
		"  ● ip-2            old      Ready",
		"  ● ip-3            new      Ready",
		"  ◷ ip-4            new      joining (NotReady)",
		"",
		"events",
		"    ● ip-3  came online (Ready)",
		"    ▲ ip-1  cordoned, draining",
	}
	if diff := cmp.Diff(unicode, RollPanel(Theme{Unicode: true}, midRoll(), 3, 150*time.Second)); diff != "" {
		t.Errorf("RollPanel with Unicode (-want +got):\n%s", diff)
	}
	// Color is an addition: without the escape codes the lines are the
	// same.
	colored := RollPanel(Theme{Depth: TrueColor, Unicode: true}, midRoll(), 3, 150*time.Second)
	for i, line := range colored {
		if !strings.Contains(line, "\x1b[") && unicode[i] != "" {
			t.Errorf("line %d has no color: %q", i, line)
		}
		if got := visibleWidth(line); got != Width(unicode[i]) {
			t.Errorf("line %d is %d cells wide with color and %d without", i, got, Width(unicode[i]))
		}
	}
}

// The header of the panel: the mark turns with the time, a roll with no
// time has neither time nor motion, a panel of polled nodes says so, and
// a roll that is done has the mark of a healthy thing.
func TestRollPanel_Header(t *testing.T) {
	p := midRoll()
	tests := []struct {
		name    string
		edit    func(p *RollProgress)
		theme   Theme
		desired int
		elapsed time.Duration
		want    []string
	}{
		{
			name: "no time: for a frame that is appended", desired: 3,
			want: []string{"* rolling spot-burst   1.33 -> 1.34", "[#####-----------]  1/3 replaced, 1 new ready"},
		},
		{
			name: "the mark turns each second", theme: Theme{Unicode: true}, desired: 3, elapsed: 13*time.Second + 400*time.Millisecond,
			want: []string{"⠸ rolling spot-burst   1.33 → 1.34   13s", "▕█████░░░░░░░░░░░▏  1/3 replaced, 1 new ready"},
		},
		{
			name: "polled", desired: 3, edit: func(p *RollProgress) { p.Watching = false },
			want: []string{"* rolling spot-burst   1.33 -> 1.34", "[#####-----------]  1/3 replaced, 1 new ready, polled"},
		},
		{
			name: "no old version", desired: 3, edit: func(p *RollProgress) { p.OldVersion = "" },
			want: []string{"* rolling spot-burst   -> 1.34", "[#####-----------]  1/3 replaced, 1 new ready"},
		},
		{
			name: "no versions", desired: 3, edit: func(p *RollProgress) { p.OldVersion, p.NewVersion = "", "" },
			want: []string{"* rolling spot-burst", "[#####-----------]  1/3 replaced, 1 new ready"},
		},
		{
			name: "done", desired: 2, elapsed: time.Minute,
			edit: func(p *RollProgress) {
				p.Snapshot = noderoll.Snapshot{Total: 2, ReadyTarget: 2, Nodes: []noderoll.NodeView{
					{Name: "ip-3", OnTarget: true, Ready: true, Phase: noderoll.PhaseReady},
					{Name: "ip-4", OnTarget: true, Ready: true, Phase: noderoll.PhaseReady},
				}}
			},
			want: []string{"[OK] rolling spot-burst   1.33 -> 1.34   1m0s", "[################]  2/2 replaced, 2 new ready"},
		},
		{
			name: "more old nodes than the roll replaces", desired: 1,
			want: []string{"* rolling spot-burst   1.33 -> 1.34", "[----------------]  0/1 replaced, 1 new ready"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			progress := p
			if tt.edit != nil {
				tt.edit(&progress)
			}
			got := RollPanel(tt.theme, progress, tt.desired, tt.elapsed)
			if diff := cmp.Diff(tt.want, got[:2]); diff != "" {
				t.Errorf("header (-want +got):\n%s", diff)
			}
		})
	}
	// A roll that did not change draws the same lines, so a caller that
	// appends frames can leave the second one out.
	if diff := cmp.Diff(RollPanel(Theme{}, p, 3, 0), RollPanel(Theme{}, p, 3, 0)); diff != "" {
		t.Errorf("two panels of the same roll differ:\n%s", diff)
	}
}

// The Warning events about the nodes have a section of their own, with
// the reason, the node, and the message.
func TestRollPanel_Warnings(t *testing.T) {
	p := RollProgress{
		Nodegroup: "spot-burst", OldVersion: "ami-old", NewVersion: "ami-new", Watching: true,
		Snapshot: noderoll.Snapshot{
			Total: 1, Draining: 1,
			Nodes: []noderoll.NodeView{{Name: "ip-1", Ready: true, Phase: noderoll.PhaseDraining}},
			Warnings: []noderoll.WarnEvent{
				{Node: "ip-1", Object: "Node/ip-1", Reason: "FailedDraining", Message: "Cannot evict pod as it would violate the pod's disruption budget"},
				{Node: "ip-1", Object: "Pod/web", Reason: "FailedMount", Message: "  first line\n\tsecond   line " + strings.Repeat("x", 90)},
				{Node: "ip-1", Object: "Node/ip-1", Reason: "NodeNotReady"},
			},
		},
	}
	want := []string{
		"* rolling spot-burst   ami-old -> ami-new",
		"[----------------]  0/1 replaced, 0 new ready",
		"",
		"      NODE            OLD/NEW  STATE",
		"  [!] ip-1            old      draining",
		"",
		"events",
		"",
		"warnings",
		"    [!] FailedDraining ip-1  Cannot evict pod as it would violate the pod's disruption budget",
		"    [!] FailedMount ip-1  first line second line " + strings.Repeat("x", 54) + "...",
		"    [!] NodeNotReady ip-1",
	}
	if diff := cmp.Diff(want, RollPanel(Theme{}, p, 1, 0)); diff != "" {
		t.Errorf("RollPanel (-want +got):\n%s", diff)
	}
}

// A Ready node under pressure still reads Ready, with the pressure beside
// it.
func TestRollPanel_PressureAdvisory(t *testing.T) {
	p := RollProgress{
		Nodegroup: "spot-burst", OldVersion: "ami-old", NewVersion: "ami-new", Watching: true,
		Snapshot: noderoll.Snapshot{
			Total: 2, ReadyTarget: 1, Draining: 1,
			Nodes: []noderoll.NodeView{
				{Name: "ip-hot", OnTarget: true, Ready: true, Phase: noderoll.PhaseReady, Pressure: []string{"MemoryPressure", "DiskPressure"}},
				{Name: "ip-old", Ready: true, Phase: noderoll.PhaseDraining, Pods: 9, PodsTotal: 4, Pressure: []string{"PIDPressure"}},
				{Name: "ip-odd", Phase: noderoll.Phase("Later")},
			},
		},
	}
	want := []string{
		"       NODE            OLD/NEW  STATE",
		"  [OK] ip-hot          new      Ready, [!] MemoryPressure+DiskPressure",
		"  [!]  ip-old          old      draining, evicting [-----] 0/4 pods, [!] PIDPressure",
		"  [?]  ip-odd          old      unknown",
	}
	if diff := cmp.Diff(want, RollPanel(Theme{}, p, 1, 0)[3:7]); diff != "" {
		t.Errorf("the node table (-want +got):\n%s", diff)
	}
}

// The panel shows the last six events, and each kind in its words.
func TestRollPanel_Events(t *testing.T) {
	p := RollProgress{Nodegroup: "ng", Watching: true, Snapshot: noderoll.Snapshot{Total: 1, Nodes: []noderoll.NodeView{{Name: "ip-1", Phase: noderoll.PhaseReady}}}}
	p.Recent = []noderoll.Event{
		{Node: "n-0", Kind: noderoll.EvtJoining}, {Node: "n-1", Kind: noderoll.EvtJoining},
		{Node: "n-2", Kind: noderoll.EvtJoining}, {Node: "n-3", Kind: noderoll.EvtOnline},
		{Node: "n-4", Kind: noderoll.EvtDraining}, {Node: "n-5", Kind: noderoll.EvtTerminated},
		{Node: "n-6", Kind: noderoll.EventKind("later")}, {Node: "n-7", Kind: noderoll.EvtOnline},
	}
	want := []string{
		"events",
		"    [~] n-2  scaling up, joining",
		"    [OK] n-3  came online (Ready)",
		"    [!] n-4  cordoned, draining",
		"    - n-5  drained and terminated",
		"    - n-6  later",
		"    [OK] n-7  came online (Ready)",
	}
	if diff := cmp.Diff(want, RollPanel(Theme{}, p, 1, 0)[6:]); diff != "" {
		t.Errorf("the events (-want +got):\n%s", diff)
	}
}

// A read of the Warning events that stopped at its cap is noted once, in
// the events section.
func TestRollPanel_WarningsCappedNote(t *testing.T) {
	p := RollProgress{Nodegroup: "ng", Watching: true, Snapshot: noderoll.Snapshot{Total: 1, Nodes: []noderoll.NodeView{{Name: "ip-1", Phase: noderoll.PhaseReady}}}}
	if joined := strings.Join(RollPanel(Theme{}, p, 1, 0), "\n"); strings.Contains(joined, "showing first") {
		t.Errorf("a read below the cap has a note about the cap:\n%s", joined)
	}
	p.Snapshot.WarningsCapped = 2000
	want := []string{"events", "    (showing first 2000 warning events)"}
	if diff := cmp.Diff(want, RollPanel(Theme{}, p, 1, 0)[6:]); diff != "" {
		t.Errorf("the events section (-want +got):\n%s", diff)
	}
}

func TestOneLineWarning(t *testing.T) {
	if got := oneLineWarning("  evict\n\tfailed   now ", true); got != "evict failed now" {
		t.Errorf("oneLineWarning = %q, want the white space collapsed", got)
	}
	// A rune of several bytes near the cut stays whole, and the result
	// fits.
	got := oneLineWarning(strings.Repeat("é", warningWidth+10), true)
	if !utf8.ValidString(got) {
		t.Errorf("the cut split a rune: %q", got)
	}
	if w := Width(got); w != warningWidth {
		t.Errorf("width = %d, want %d", w, warningWidth)
	}
	// A wide rune counts two cells.
	if w := Width(oneLineWarning(strings.Repeat("日", warningWidth), true)); w > warningWidth {
		t.Errorf("width of wide runes = %d, want at most %d", w, warningWidth)
	}
	if got := oneLineWarning("short", false); got != "short" {
		t.Errorf("oneLineWarning(short) = %q", got)
	}
}

// The real observer over a fake cluster feeds the panel: a cluster in the
// middle of a roll draws its draining, joining, and Ready nodes.
func TestRollPanel_FromObserver(t *testing.T) {
	node := func(name string, ready, cordoned bool) *corev1.Node {
		cond := corev1.ConditionFalse
		if ready {
			cond = corev1.ConditionTrue
		}
		return &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{noderoll.LabelNodegroup: "spot-burst"}},
			Spec:       corev1.NodeSpec{Unschedulable: cordoned},
			Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: cond}}},
		}
	}
	ctx := t.Context()
	client := fake.NewClientset(node("ip-1", true, false), node("ip-2", true, false), node("ip-3", true, false))
	obs, err := noderoll.Attach(ctx, client, "spot-burst", noderoll.Identity{}) // ip-1 to ip-3 are old
	if err != nil {
		t.Fatal(err)
	}
	// The panel reads the cluster as it is: lists, and no cache that lags.
	obs.Close()
	tracker := noderoll.NewTracker()
	first, err := obs.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tracker.Observe(first)

	// A new node joins, and an old one is cordoned.
	if _, err := client.CoreV1().Nodes().Create(ctx, node("ip-4", false, false), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CoreV1().Nodes().Update(ctx, node("ip-1", true, true), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	snap, err := obs.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tracker.Observe(snap)

	p := RollProgress{Nodegroup: "spot-burst", OldVersion: "ami-old", NewVersion: "ami-new", Snapshot: snap, Recent: tracker.Recent(0), Watching: obs.Watching()}
	want := []string{
		"* rolling spot-burst   ami-old -> ami-new",
		"[----------------]  0/3 replaced, 0 new ready, polled",
		"",
		"       NODE            OLD/NEW  STATE",
		"  [!]  ip-1            old      draining",
		"  [OK] ip-2            old      Ready",
		"  [OK] ip-3            old      Ready",
		"  [~]  ip-4            new      joining (NotReady)",
		"",
		"events",
		"    [!] ip-1  cordoned, draining",
		"    [~] ip-4  scaling up, joining",
	}
	if diff := cmp.Diff(want, RollPanel(Theme{}, p, 3, 0)); diff != "" {
		t.Errorf("RollPanel (-want +got):\n%s", diff)
	}
}
