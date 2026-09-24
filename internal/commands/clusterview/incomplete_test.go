package clusterview

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/render"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
)

// An incomplete cluster row never reads as complete: its NODES cell gets the
// unknown glyph, "unknown" when nothing was counted, and the failures are
// listed below the table.
func TestClusterList_IncompleteRows(t *testing.T) {
	th := render.New(render.ColorNone, false)
	rows := []clustersvc.ClusterSummary{
		{Name: "ok", Status: "ACTIVE", Version: "1.32", NodeCount: clustersvc.NodeCountInfo{Total: 3}},
		{Name: "partial", Status: "ACTIVE", Version: "1.32", NodeCount: clustersvc.NodeCountInfo{Total: 2}, Incomplete: true},
		{Name: "ghost", Status: "UNKNOWN", Incomplete: true},
	}
	for _, c := range []struct {
		row        clustersvc.ClusterSummary
		text, cell string
	}{
		{rows[0], "3", "3"},
		{rows[1], "2", "[?] 2"},
		{rows[2], "unknown", "[?] unknown"},
	} {
		if got := nodesText(c.row); got != c.text {
			t.Errorf("%s: nodesText = %q, want %q", c.row.Name, got, c.text)
		}
		if got := nodesCell(th, c.row); got != c.cell {
			t.Errorf("%s: nodesCell = %q, want %q", c.row.Name, got, c.cell)
		}
	}

	failures := []diag.Failure{{Kind: diag.KindCluster, Name: "ghost", Reason: diag.ReasonAccessDenied, Error: "denied"}}
	out, err := captureStdout(t, func() error { return OutputClustersTable(rows, failures, 0, false, false) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "INCOMPLETE DATA") || !strings.Contains(out, "cluster ghost: AccessDenied: denied") {
		t.Errorf("table lacks the INCOMPLETE DATA section:\n%s", out)
	}
	out, err = captureStdout(t, func() error { return OutputClustersTree(rows, failures, 0, false, false) })
	if err != nil {
		t.Fatal(err)
	}
	// The tree itself goes through pterm; the section follows it on stdout.
	if !strings.Contains(out, "INCOMPLETE DATA") {
		t.Errorf("tree lacks the INCOMPLETE DATA section:\n%s", out)
	}
}
