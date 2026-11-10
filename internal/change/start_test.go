package change

import (
	"context"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// Start, the entry of the TUI: a pause and a stop have somewhere to take
// effect only for a kind with checkpoints (an upgrade, a rollback, and a
// roll of the fleet), and a run with a reviewed plan shows that plan from
// its first view. The view is read while the first request of the run is
// held, so the run has not moved yet.
func TestStart(t *testing.T) {
	fleetRoll := RollRequest{Fleet: &FleetScope{Regions: fleet.Regions{Names: []string{east}}}}
	fleetRoll.Policy.Yes = true
	oneRoll := RollRequest{Target: Target{Cluster: "prod"}, Nodegroups: []string{"ng-a"}}
	oneRoll.Policy.Yes = true
	addons := AddonUpdateRequest{Target: Target{Cluster: "prod"}, Addons: []string{"vpc-cni"}}
	addons.Policy.Yes = true
	tests := []struct {
		name        string
		world       func() *fleettest.World
		req         Request
		checkpoints bool
	}{
		{name: "an upgrade", world: func() *fleettest.World { return engineWorld(nil) }, req: upgradeTo("1.33"), checkpoints: true},
		{name: "a rollback", world: func() *fleettest.World { return upgradedWorld(nil) }, req: rollbackOf(), checkpoints: true},
		{name: "a roll of the fleet", world: func() *fleettest.World { return engineWorld(nil) }, req: fleetRoll, checkpoints: true},
		{name: "a roll of one cluster", world: func() *fleettest.World { return engineWorld(nil) }, req: oneRoll},
		{name: "an add-on update", world: func() *fleettest.World { return engineWorld(nil) }, req: addons},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, tt.world())
			release := make(chan struct{})
			var once sync.Once
			e.srv.OnRequest = func(ctx context.Context, _ awsxtest.Request) {
				once.Do(func() {
					select {
					case <-release:
					case <-ctx.Done():
					}
				})
			}
			h := Start(testContext(t), e.deps, tt.req)
			v := h.View()
			close(release)
			<-h.Done()
			if v.Kind != tt.req.kind() || v.Controls.Pause != tt.checkpoints || v.Controls.Stop != tt.checkpoints {
				t.Errorf("first view: kind %s, controls %+v; want %s with pause and stop %t", v.Kind, v.Controls, tt.req.kind(), tt.checkpoints)
			}
			if v.Plan != nil {
				t.Errorf("a run with no reviewed plan shows the plan %+v before it read anything", v.Plan)
			}
		})
	}
	t.Run("a reviewed plan", func(t *testing.T) {
		e := newEnv(t, engineWorld(nil))
		req := upgradeTo("1.33")
		plan, err := NewPlan(testContext(t), e.deps, req)
		if err != nil {
			t.Fatal(err)
		}
		req.Expect = plan
		release := make(chan struct{})
		var once sync.Once
		e.srv.OnRequest = func(ctx context.Context, _ awsxtest.Request) {
			once.Do(func() {
				select {
				case <-release:
				case <-ctx.Done():
				}
			})
		}
		h := Start(testContext(t), e.deps, req)
		v := h.View()
		close(release)
		<-h.Done()
		steps := func(p *Plan) []string {
			var out []string
			for _, cp := range p.Clusters {
				for _, ph := range cp.Phases {
					for _, s := range ph.Steps {
						out = append(out, s.Ref.String()+" "+string(s.Action)+" "+s.To)
					}
				}
			}
			return out
		}
		if v.Plan == nil {
			t.Fatal("the first view shows no plan; want the reviewed plan")
		}
		if diff := cmp.Diff(steps(plan), steps(v.Plan)); diff != "" {
			t.Errorf("the plan of the first view (-reviewed +shown):\n%s", diff)
		}
		res := h.View().Result
		if res == nil {
			t.Fatal("the run ended with no result")
		}
		if res.Status != Succeeded {
			t.Errorf("result = %s, %v; want Succeeded with the reviewed plan", res.Status, res.Err)
		}
	})
}
