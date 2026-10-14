package fleetdata_test

import (
	"slices"
	"testing"

	"github.com/dantech2000/refresh/internal/fleet/fleetdata"
)

func TestRegions(t *testing.T) {
	tests := []struct {
		home      string
		wantFirst string
		wantCount int
		alwaysOn  string
	}{
		{"", "us-east-1", 17, "us-east-1"},
		{"eu-west-1", "us-east-1", 17, "us-east-1"},
		{"us-gov-east-1", "us-gov-west-1", 2, "us-gov-west-1"},
		{"cn-northwest-1", "cn-north-1", 2, "cn-north-1"},
	}
	for _, tt := range tests {
		got := fleetdata.Regions(tt.home)
		if len(got) != tt.wantCount || got[0] != tt.wantFirst {
			t.Errorf("Regions(%q) = %v, want %d regions that start with %s", tt.home, got, tt.wantCount, tt.wantFirst)
		}
		if on := fleetdata.AlwaysOnRegion(tt.home); on != tt.alwaysOn || !slices.Contains(got, on) {
			t.Errorf("AlwaysOnRegion(%q) = %s, want %s, a region of the sweep", tt.home, on, tt.alwaysOn)
		}
	}
	// The caller may change the list that it gets.
	fleetdata.Regions("")[0] = "changed"
	if got := fleetdata.Regions("")[0]; got != "us-east-1" {
		t.Errorf("a caller changed the built-in list: the first region is now %s", got)
	}
}
