package view_test

import (
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/view"
)

func TestCells(t *testing.T) {
	ready, behind := 5, true
	tests := []struct {
		name, got, want string
	}{
		{"unknown when not read", view.OrUnknown(true, ""), "unknown"},
		{"empty when read", view.OrUnknown(false, ""), ""},
		{"a value", view.OrUnknown(true, "1.33"), "1.33"},
		{"a time", view.Time(time.Date(2026, 9, 1, 14, 5, 59, 0, time.FixedZone("x", 3600))), "2026-09-01 13:05 UTC"},
		{"the zero time", view.Time(time.Time{}), ""},
		// A nodegroup whose sizes EKS did not return has an unknown
		// nodes cell, never 0.
		{"nodes, no sizes", view.NodesCell(apidoc.Nodegroup{Name: "web"}), "unknown"},
		{"nodes", view.NodesCell(apidoc.Nodegroup{Scaling: &apidoc.Scaling{Desired: 6}}), "6"},
		{"nodes, ready", view.NodesCell(apidoc.Nodegroup{Scaling: &apidoc.Scaling{Desired: 6}, ReadyNodes: &ready}), "5/6"},
		{"support, not read", view.SupportCell(nil), ""},
		{"support", view.SupportCell(&apidoc.Support{Tier: apidoc.SupportStandard, DaysRemaining: 61, BuiltIn: true}), "standard (61d)*"},
		{"plural, one", view.Plural(1, "issue"), "1 issue"},
		{"plural", view.Plural(3, "issue"), "3 issues"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %q, want %q", tt.name, tt.got, tt.want)
		}
	}
	if got := view.BehindStatus(apidoc.Nodegroup{VersionBehind: &behind}); got != view.Warn {
		t.Errorf("BehindStatus = %v, want Warn", got)
	}
	if got := view.AddonHealthStatus(apidoc.AddonFailed); got != view.Fail {
		t.Errorf("AddonHealthStatus(Failed) = %v, want Fail", got)
	}
}
