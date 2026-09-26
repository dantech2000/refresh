package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/tui/state"
)

// The Kube events of a real roll ran their columns together: a reason or an
// object as wide as its column touched the next one ("NodeShutdown" is fine,
// "Pod/eks-pod-identity-agent-fpsb8Pod was rejected…" is not). Each column
// now keeps a gap and cuts with "…", at any pane width.
func TestKubeEventColumnsKeepAGapAtAnyWidth(t *testing.T) {
	at := time.Date(2026, 9, 25, 20, 41, 21, 0, time.UTC)
	ev := func(reason, obj, msg string) state.Event {
		return state.Event{At: at, Source: state.SourceKube, Level: state.LevelWarn, Text: reason, Subject: obj, Detail: msg}
	}
	evs := []state.Event{
		ev("NodeShutdown", "Pod/aws-node-75w97", "Pod was rejected as the node is shutting down."),
		ev("NodeShutdown", "Pod/eks-pod-identity-agent-fpsb8", "Pod was rejected as the node is shutting down."),
		ev("Unhealthy", "Pod/metrics-server-759bb58f87-86g7n", `Readiness probe failed: Get "https://192.168.38.105:10251/readyz"`),
		ev("InvalidDiskCapacity", "Node/ip-192-168-50-75.ec2.internal", "invalid capacity 0 on image filesystem"),
	}
	for _, w := range []int{140, 90, 60} {
		cols := kubeColumns(evs, w)
		for _, e := range evs {
			got := kubeLine(e, cols).Fit(w).Plain()
			if width(got) != w {
				t.Errorf("w=%d: line is %d wide: %q", w, width(got), got)
			}
			// The object column starts one space after the reason column.
			objAt := 1 + 8 + 1 + 7 + 1 + cols.reason + 1
			if got[:objAt] == "" || got[objAt-1] != ' ' {
				t.Errorf("w=%d: no gap before the object: %q", w, got)
			}
			obj := []rune(got)[objAt : objAt+cols.object]
			if s := string(obj); !strings.HasPrefix(e.Subject, strings.TrimRight(strings.TrimSuffix(s, "…"), " ")) {
				t.Errorf("w=%d: object column %q is not a cut of %q", w, s, e.Subject)
			}
			if r := []rune(got); r[objAt+cols.object] != ' ' {
				t.Errorf("w=%d: no gap before the message: %q", w, got)
			}
		}
	}
	if wide, narrow := kubeColumns(evs, 140), kubeColumns(evs, 60); narrow.object >= wide.object {
		t.Errorf("the object column does not shrink with the pane: %d at 140, %d at 60", wide.object, narrow.object)
	}
}
