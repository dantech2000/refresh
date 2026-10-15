package addon_test

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// An add-on with configuration values and a service account role keeps
// both through an update: the request sends neither the values nor a
// conflict mode, so EKS keeps what is there and refuses a conflict, and
// the add-on still has them after the update.
func TestUpdate_KeepsTheConfiguration(t *testing.T) {
	const values = `{"env":{"ENABLE_PREFIX_DELEGATION":"true"}}`
	const role = "arn:aws:iam::123456789012:role/vpc-cni"
	seed := func(c *fleettest.Cluster) {
		a := addon(c, "vpc-cni")
		a.ConfigurationValues, a.ServiceAccountRoleARN = values, role
	}
	e, out := runUpdate(t, updateCase{seed: seed, args: []string{"vpc-cni", "--yes"}, updates: []string{"vpc-cni"}})
	for _, r := range e.Server.Requests() {
		if r.Operation != fleettest.EKSUpdateAddon {
			continue
		}
		for _, key := range []string{"configurationValues", "resolveConflicts", "serviceAccountRoleArn", "podIdentityAssociations"} {
			if _, ok := r.Input[key]; ok {
				t.Errorf("the update sent %s: %v", key, r.Input)
			}
		}
	}
	e.World.View(func(s *fleettest.State) {
		a := s.Cluster(east, "prod").Addon("vpc-cni")
		if a.Version != cniNew || a.ConfigurationValues != values || a.ServiceAccountRoleARN != role {
			t.Errorf("after the update: %s, values %q, role %q\n%s", a.Version, a.ConfigurationValues, a.ServiceAccountRoleARN, out)
		}
	})
}

// coredns that runs as a plain Deployment, with no EKS add-on: refresh
// manages EKS add-ons only, so "addon update coredns" fails before any
// change and says how to list the add-ons that are installed.
func TestUpdate_SelfManagedAddon(t *testing.T) {
	seed := func(c *fleettest.Cluster) {
		c.Addons = c.Addons[1:] // coredns goes
		dns := map[string]string{"k8s-app": "kube-dns"}
		c.Deployments = append(c.Deployments, fleettest.Deployment{Namespace: "kube-system", Name: "coredns", Replicas: 2, Selector: dns})
	}
	_, out := runUpdate(t, updateCase{seed: seed, args: []string{"coredns", "--yes"}, exit: cmdutil.ExitFailure})
	if !strings.Contains(out, `add-on "coredns" not found in cluster prod`) || !strings.Contains(out, "refresh addon list -c prod") {
		t.Errorf("the error does not name the add-on and the list command:\n%s", out)
	}
}
