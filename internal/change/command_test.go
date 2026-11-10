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
func TestCommand_Resume(t *testing.T) {
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
			if got := Command(tt.f, tt.req); got != tt.want {
				t.Errorf("Command:\n got %s\nwant %s", got, tt.want)
			}
		})
	}
}

// Command builds the command of every kind of change, with the words of
// the run around it: the dry run that "refresh ui" shows, and a notice.
// The Kubernetes words go only on a command that reads Kubernetes.
func TestCommand_EveryKind(t *testing.T) {
	ui := FollowUpFor("prod admin", "us-east-1", "/home/me/.kube/config", "admin")
	prod := Target{Region: "us-east-1", Cluster: "prod"}
	three := int32(3)
	tests := []struct {
		name string
		f    FollowUp
		req  Request
		want string
	}{
		{
			name: "every add-on",
			f:    ui,
			req:  AddonUpdateRequest{Target: prod, All: true},
			want: "refresh --profile 'prod admin' --region us-east-1 addon update --all -c prod",
		},
		{
			name: "one add-on to a version, no wait",
			req:  AddonUpdateRequest{Common: Common{NoWait: true}, Target: prod, Addons: []string{"coredns"}, Version: "v1.11.3-eksbuild.2"},
			want: "refresh addon update coredns -c prod --version v1.11.3-eksbuild.2 --no-wait",
		},
		{
			name: "every add-on but one",
			req:  AddonUpdateRequest{Target: prod, All: true, Skip: []string{"vpc-cni"}},
			want: "refresh addon update --all -c prod --skip-addon vpc-cni",
		},
		{
			name: "a scale",
			f:    ui,
			req:  ScaleRequest{Target: prod, Nodegroup: "web", Desired: &three, Max: &three},
			want: "refresh --profile 'prod admin' --region us-east-1 nodegroup scale web -c prod --desired 3 --max 3",
		},
		{
			name: "a roll of one nodegroup",
			f:    ui,
			req:  RollRequest{Target: prod, Nodegroups: []string{"web"}},
			want: "refresh --profile 'prod admin' --region us-east-1 nodegroup update web -c prod --kubeconfig /home/me/.kube/config --kube-context admin",
		},
		{
			name: "a roll by pattern, again",
			req:  RollRequest{Target: prod, Select: &Selector{Text: "-f name=web-*"}, Reroll: true, Common: Common{Policy: health.Policy{Force: true}}},
			want: "refresh nodegroup update -c prod -f name=web-* --reroll --force",
		},
		{
			name: "a fleet roll",
			f:    FollowUp{Prefix: "refresh --region us-west-2"},
			req:  RollRequest{Fleet: &FleetScope{}, Common: Common{Policy: health.Policy{Yes: true}}},
			want: "refresh --region us-west-2 nodegroup update --all-clusters --yes",
		},
		{
			name: "a catch-up of one nodegroup",
			f:    ui,
			req:  UpgradeRequest{Target: prod, To: fleet.Version{Major: 1, Minor: 33}, Only: []Part{PartNodegroups}, Nodegroups: []string{"api"}},
			want: "refresh --profile 'prod admin' --region us-east-1 cluster upgrade prod --to 1.33 --only nodegroups -n api --kubeconfig /home/me/.kube/config --kube-context admin",
		},
		{
			name: "a rollback",
			f:    ui,
			req:  RollbackRequest{Target: prod},
			want: "refresh --profile 'prod admin' --region us-east-1 cluster rollback prod --kubeconfig /home/me/.kube/config --kube-context admin",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Command(tt.f, tt.req); got != tt.want {
				t.Errorf("Command:\n got %s\nwant %s", got, tt.want)
			}
		})
	}
}

// FollowUpFor quotes each value that a shell would split or expand, and
// leaves out each empty one.
func TestFollowUpFor(t *testing.T) {
	tests := []struct {
		name                                     string
		profile, region, kubeconfig, kubeContext string
		want                                     FollowUp
	}{
		{name: "nothing", want: FollowUp{Prefix: "refresh"}},
		{name: "plain words", profile: "prod", region: "eu-west-1", kubeContext: "arn:aws:eks:eu-west-1:1:cluster/prod",
			want: FollowUp{Prefix: "refresh --profile prod --region eu-west-1", Suffix: "--kube-context arn:aws:eks:eu-west-1:1:cluster/prod"}},
		{name: "words to quote", profile: "it's me", kubeconfig: "/tmp/my kube",
			want: FollowUp{Prefix: `refresh --profile 'it'\''s me'`, Suffix: "--kubeconfig '/tmp/my kube'"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FollowUpFor(tt.profile, tt.region, tt.kubeconfig, tt.kubeContext); got != tt.want {
				t.Errorf("FollowUpFor = %+v, want %+v", got, tt.want)
			}
		})
	}
}
