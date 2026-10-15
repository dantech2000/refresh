package change

import (
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
)

// The command that a stopped run prints repeats every flag that decides
// what the run changes and what stops it. A resume never widens the run:
// it keeps the parts and the exclusions. It never meets a gate that the
// first run was told to pass: it keeps --force and --skip-health-check.
// The resume of an unattended run is unattended: it keeps --yes.
func TestEngineCommand(t *testing.T) {
	to := fleet.Version{Major: 1, Minor: 33}
	upgrade := func(edit func(*UpgradeRequest)) Request {
		r := UpgradeRequest{Target: Target{Cluster: "prod"}, To: to}
		if edit != nil {
			edit(&r)
		}
		return r
	}
	tests := []struct {
		name string
		f    FollowUp
		req  Request
		want string
	}{
		{name: "minimal", req: upgrade(nil), want: "refresh cluster upgrade prod --to 1.33"},
		{
			name: "the parts and the exclusions",
			req: upgrade(func(r *UpgradeRequest) {
				r.Only, r.Nodegroups = []Part{PartNodegroups}, []string{"web"}
				r.SkipAddons, r.SkipNodegroups, r.SkipInsights = []string{"vpc-cni"}, []string{"batch jobs"}, true
			}),
			want: "refresh cluster upgrade prod --to 1.33 --only nodegroups -n web --skip-addon vpc-cni --skip-nodegroup 'batch jobs' --skip-insights-check",
		},
		{
			name: "the flags of the gate",
			req: upgrade(func(r *UpgradeRequest) {
				r.Policy = health.Policy{Force: true, Skip: true}
				r.SkipVerify = true
			}),
			want: "refresh cluster upgrade prod --to 1.33 --force --skip-health-check --skip-verify",
		},
		{
			name: "require-healthy",
			req:  upgrade(func(r *UpgradeRequest) { r.Policy.RequireHealthy = true }),
			want: "refresh cluster upgrade prod --to 1.33 --require-healthy",
		},
		{
			name: "an unattended run stays unattended",
			req:  upgrade(func(r *UpgradeRequest) { r.Policy.Yes = true }),
			want: "refresh cluster upgrade prod --to 1.33 --yes",
		},
		{
			name: "the words of the command around it",
			f:    FollowUp{Prefix: "refresh --region us-west-2", Suffix: "--kube-context admin", Resume: "--wait-timeout 1h0m0s"},
			req:  upgrade(func(r *UpgradeRequest) { r.Policy = health.Policy{Force: true, Yes: true} }),
			want: "refresh --region us-west-2 cluster upgrade prod --to 1.33 --force --wait-timeout 1h0m0s --yes --kube-context admin",
		},
		{
			name: "rollback",
			f:    FollowUp{Resume: "--wait-timeout 1h0m0s"},
			req: RollbackRequest{
				Common:          Common{Policy: health.Policy{Force: true, Skip: true, Yes: true}, SkipVerify: true},
				Target:          Target{Cluster: "prod"},
				SkipNodegroups:  []string{"gpu"},
				RollbackTimeout: 30 * time.Minute,
			},
			want: "refresh cluster rollback prod --skip-nodegroup gpu --rollback-timeout 30m0s --force --skip-health-check --skip-verify --wait-timeout 1h0m0s --yes",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := engineCommand(tt.f, tt.req); got != tt.want {
				t.Errorf("engineCommand:\n got %s\nwant %s", got, tt.want)
			}
		})
	}
}
