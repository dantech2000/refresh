package nodegroup

import "testing"

func TestRenameSkipKeepsItSkipped(t *testing.T) {
	var v PostRollVerification
	v.Checks = append(v.Checks, "nodegroup ng-a is ACTIVE")
	v.Skip(SkipNoPreroll)
	v.RenameSkip(SkipNoPreroll, "started elsewhere")
	if len(v.Checks) != 2 || v.Checks[1] != "started elsewhere" || !v.Skipped("started elsewhere") || v.Skipped(SkipNoPreroll) {
		t.Fatalf("checks = %v", v.Checks)
	}
	v.RenameSkip("nodegroup ng-a is ACTIVE", "x") // not a skipped check
	if v.Checks[0] != "nodegroup ng-a is ACTIVE" {
		t.Fatalf("renamed a check that ran: %v", v.Checks)
	}
}
