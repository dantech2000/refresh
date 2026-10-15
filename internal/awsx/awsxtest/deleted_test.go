package awsxtest_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// The update of a nodegroup or an add-on that was deleted is gone with
// it: DescribeUpdate answers ResourceNotFoundException, as EKS does.
func TestDescribeUpdateOfADeletedResource(t *testing.T) {
	w := fleettest.New()
	w.Edit(func(s *fleettest.State) {
		s.Clusters = []*fleettest.Cluster{{
			Name: "prod", Region: region, Version: "1.33",
			Nodegroups: []*fleettest.Nodegroup{{Name: "web", Version: "1.33"}},
			Addons:     []*fleettest.Addon{{Name: "vpc-cni", Version: "v1.0.0-eksbuild.1"}},
		}}
		s.DefaultUpdate = fleettest.UpdateScript{Never: true}
	})
	v, av := "1.33", "v1.0.0-eksbuild.1"
	ng, err := w.UpdateNodegroupVersion(region, "prod", "web", fleettest.NodegroupVersionRequest{Version: &v})
	if err != nil {
		t.Fatal(err)
	}
	addon, err := w.UpdateAddon(region, "prod", "vpc-cni", fleettest.AddonRequest{Version: &av})
	if err != nil {
		t.Fatal(err)
	}
	srv := awsxtest.NewServer(w)
	t.Cleanup(srv.Close)
	c := eks.NewFromConfig(srv.Config(region))
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	describe := func(in *eks.DescribeUpdateInput) error {
		in.Name = aws.String("prod")
		_, err := c.DescribeUpdate(ctx, in)
		return err
	}
	ngIn := func() *eks.DescribeUpdateInput {
		return &eks.DescribeUpdateInput{UpdateId: aws.String(ng.ID), NodegroupName: aws.String("web")}
	}
	addonIn := func() *eks.DescribeUpdateInput {
		return &eks.DescribeUpdateInput{UpdateId: aws.String(addon.ID), AddonName: aws.String("vpc-cni")}
	}
	for _, in := range []*eks.DescribeUpdateInput{ngIn(), addonIn()} {
		if err := describe(in); err != nil {
			t.Fatalf("before the delete: %v", err)
		}
	}
	w.Edit(func(s *fleettest.State) {
		cl := s.Cluster(region, "prod")
		cl.Nodegroups = slices.DeleteFunc(cl.Nodegroups, func(n *fleettest.Nodegroup) bool { return n.Name == "web" })
		cl.Addons = nil
	})
	for _, in := range []*eks.DescribeUpdateInput{ngIn(), addonIn()} {
		var nf *ekstypes.ResourceNotFoundException
		if err := describe(in); !errors.As(err, &nf) {
			t.Errorf("after the delete: %v, want ResourceNotFoundException", err)
		}
	}
}
