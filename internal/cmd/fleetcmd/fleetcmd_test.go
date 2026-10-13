package fleetcmd_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/cmd/fleetcmd"
	"github.com/dantech2000/refresh/internal/cmdutil"
)

func TestSweepRegions(t *testing.T) {
	tests := []struct {
		name        string
		flag        []string
		env         string
		home        string
		all         bool
		wantNames   []string
		wantDefault bool
		wantCount   int // when wantNames is nil
	}{
		{name: "the home region", home: "eu-west-1", wantNames: []string{"eu-west-1"}},
		{name: "-r wins over the home region", flag: []string{"us-east-1", " us-west-2 ", "us-east-1", ""}, home: "eu-west-1", wantNames: []string{"us-east-1", "us-west-2"}},
		{name: "-r wins over the environment", flag: []string{"us-east-1"}, env: "eu-west-1", wantNames: []string{"us-east-1"}},
		{name: "the environment does not apply without -A", env: "us-east-1,us-west-2", home: "eu-west-1", wantNames: []string{"eu-west-1"}},
		{name: "-A reads the environment, as named regions", env: "us-east-1, us-west-2,", home: "eu-west-1", all: true, wantNames: []string{"us-east-1", "us-west-2"}},
		{name: "-A alone is a default sweep", home: "eu-west-1", all: true, wantDefault: true, wantCount: 16},
		{name: "no home region is a sweep", wantDefault: true, wantCount: 16},
		{name: "no home region reads the environment", env: "us-east-1", wantNames: []string{"us-east-1"}},
		{name: "the partition of the home region", home: "us-gov-east-1", all: true, wantDefault: true, wantCount: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(fleetcmd.EnvRegions, tt.env)
			got := fleetcmd.SweepRegions(&cmdutil.GlobalOptions{Regions: tt.flag}, tt.home, tt.all)
			if got.Default != tt.wantDefault {
				t.Errorf("Default = %v, want %v", got.Default, tt.wantDefault)
			}
			if tt.wantNames == nil {
				if len(got.Names) != tt.wantCount {
					t.Errorf("regions = %v, want %d of them", got.Names, tt.wantCount)
				}
				return
			}
			if diff := cmp.Diff(tt.wantNames, got.Names); diff != "" {
				t.Errorf("regions (-want +got):\n%s", diff)
			}
		})
	}

	if err := fleetcmd.ValidateSweepFlags(&cmdutil.GlobalOptions{Regions: []string{"us-east-1"}}, true); err == nil {
		t.Error("-A with -r: no error")
	}
	if err := fleetcmd.ValidateSweepFlags(&cmdutil.GlobalOptions{Regions: []string{"us-east-1"}}, false); err != nil {
		t.Errorf("-r alone: %v", err)
	}
	if err := fleetcmd.ValidateSweepFlags(&cmdutil.GlobalOptions{}, true); err != nil {
		t.Errorf("-A alone: %v", err)
	}
}
