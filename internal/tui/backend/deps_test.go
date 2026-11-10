package backend

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/change"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// A command that a notice of a change names reaches the region of the
// cluster. In the home region the words of the command line stay as they
// are; outside it they get --region, which no -r gave.
func TestDepsForRegion(t *testing.T) {
	srv := awsxtest.NewServer(fleettest.NewAt(cmdutiltest.Now()))
	t.Cleanup(srv.Close)
	f, _, _, _ := cmdutiltest.NewFactory()
	cmdutiltest.UseAWS(f, srv.Config("us-east-1"))
	provider, err := f.AWS(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	home := change.FollowUpFor("test", "", "/k", "admin")
	home.Resume, home.Profile = "--wait-timeout 1h0m0s", "test"
	b := New(t.Context(), provider, Options{
		Profile: "test", Kubeconfig: "/k", KubeContext: "admin",
		Deps: change.Deps{FollowUp: home},
	})

	if diff := cmp.Diff(home, b.depsFor("us-east-1").FollowUp); diff != "" {
		t.Errorf("home region: follow-up words changed (-want +got):\n%s", diff)
	}
	want := change.FollowUp{
		Prefix:  "refresh --profile test --region eu-west-1",
		Suffix:  home.Suffix,
		Resume:  home.Resume,
		Profile: "test",
	}
	if diff := cmp.Diff(want, b.depsFor("eu-west-1").FollowUp); diff != "" {
		t.Errorf("another region (-want +got):\n%s", diff)
	}
}
