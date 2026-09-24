package cluster

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/services/upgrade"
	"github.com/dantech2000/refresh/internal/ui/plaintest"
)

func TestWriteUpgradePlanPlain(t *testing.T) {
	plan := &upgrade.Plan{
		ClusterName:    "prod",
		CurrentVersion: "1.30",
		TargetVersion:  "1.31",
		Notices:        []string{"custom AMI nodegroup"},
		Hops: []upgrade.Hop{{
			From: "1.30", To: "1.31",
			Steps: []upgrade.Step{
				{Type: upgrade.StepControlPlane, Description: "Upgrade control plane", Version: "1.31", Status: upgrade.StatusPending},
				{Type: upgrade.StepNodegroup, Description: "Roll ng-a", Target: "ng-a", Status: upgrade.StatusBlocked, Reason: "PDB\tblocks\ndrain"},
			},
		}},
	}
	var out, info bytes.Buffer
	writeUpgradePlanPlain(&out, &info, plan)

	rows := plaintest.Check(t, out.String(), "HOP", "STEP", "TYPE", "TARGET", "VERSION", "STATUS", "DESCRIPTION", "REASON")
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2:\n%s", len(rows), out.String())
	}
	if got := strings.Join(rows[1], "|"); got != "1.30->1.31|2|nodegroup|ng-a|-|blocked|Roll ng-a|PDB blocks drain" {
		t.Errorf("row = %q", got)
	}
	for _, want := range []string{"upgrade plan: prod 1.30 -> 1.31", "notice: custom AMI nodegroup"} {
		if !strings.Contains(info.String(), want) {
			t.Errorf("info missing %q:\n%s", want, info.String())
		}
	}
}
