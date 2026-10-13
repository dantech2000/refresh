package awsxtest_test

import (
	"strconv"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// TestScriptedReads checks, with a real client, what an eventually
// consistent EKS shows: a describe that still says ACTIVE after an update
// started, and a ListUpdates that does not list the update yet.
func TestScriptedReads(t *testing.T) {
	t.Parallel()
	w := fleettest.New()
	w.Edit(func(s *fleettest.State) {
		s.DefaultUpdate = fleettest.UpdateScript{Never: true}
		s.Clusters = []*fleettest.Cluster{{
			Name: "prod", Version: "1.32", Region: "us-east-1",
			Nodegroups: []*fleettest.Nodegroup{{Name: "ng-a", Version: "1.32"}},
			Addons:     []*fleettest.Addon{{Name: "vpc-cni", Version: "v1.18.0-eksbuild.1"}},
		}}
	})
	srv := awsxtest.NewServer(w)
	t.Cleanup(srv.Close)
	client := eks.NewFromConfig(srv.Config("us-east-1"))
	ctx := t.Context()

	update, err := client.UpdateNodegroupVersion(ctx, &eks.UpdateNodegroupVersionInput{ClusterName: aws.String("prod"), NodegroupName: aws.String("ng-a")})
	if err != nil {
		t.Fatal(err)
	}
	w.ScriptStatus("us-east-1", "prod", "ng-a", fleettest.StatusActive, fleettest.StatusDegraded)
	w.ScriptStatus("us-east-1", "prod", "", fleettest.StatusUpdating)
	w.ScriptStatus("us-east-1", "prod", "vpc-cni", fleettest.StatusUpdating)
	w.HideUpdates("us-east-1", "prod", 1)

	var got []string
	for range 3 {
		out, err := client.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{ClusterName: aws.String("prod"), NodegroupName: aws.String("ng-a")})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, "nodegroup "+string(out.Nodegroup.Status))
	}
	for range 2 {
		out, err := client.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String("prod")})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, "cluster "+string(out.Cluster.Status))
	}
	for range 2 {
		out, err := client.DescribeAddon(ctx, &eks.DescribeAddonInput{ClusterName: aws.String("prod"), AddonName: aws.String("vpc-cni")})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, "addon "+string(out.Addon.Status))
	}
	for range 2 {
		out, err := client.ListUpdates(ctx, &eks.ListUpdatesInput{Name: aws.String("prod"), NodegroupName: aws.String("ng-a")})
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, "updates", strconv.FormatBool(len(out.UpdateIds) == 1 && out.UpdateIds[0] == *update.Update.Id))
	}
	want := []string{
		"nodegroup ACTIVE", "nodegroup DEGRADED", "nodegroup UPDATING",
		"cluster UPDATING", "cluster ACTIVE", "addon UPDATING", "addon ACTIVE",
		"updates", "false", "updates", "true",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("what the reads showed (-want +got):\n%s", diff)
	}

	// The real status is what EKS acts on: the nodegroup is still updating.
	if _, err := client.UpdateNodegroupVersion(ctx, &eks.UpdateNodegroupVersionInput{ClusterName: aws.String("prod"), NodegroupName: aws.String("ng-a")}); err == nil {
		t.Error("a second update of a nodegroup that is updating was accepted")
	}
}
