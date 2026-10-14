//go:build livecheck

package fleetdata_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleetdata"
)

// TestLiveRegions compares the built-in list of the default sweep with
// AWS: the regions that every account of the partition has enabled (EC2
// reports them as opt-in-not-required) and where EKS answers. It reads a
// real account, with read-only calls that cost nothing, and runs only with
// the livecheck tag:
//
//   - ec2:DescribeRegions, in the region of the profile
//   - eks:ListClusters with a page of one, in each region
func TestLiveRegions(t *testing.T) {
	cfg := awsxtest.LiveConfig(t, "")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	out, err := ec2.NewFromConfig(cfg).DescribeRegions(ctx, &ec2.DescribeRegionsInput{
		Filters: []ec2types.Filter{{Name: aws.String("opt-in-status"), Values: []string{"opt-in-not-required"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var live []string
	for _, r := range out.Regions {
		region := aws.ToString(r.RegionName)
		regional := cfg.Copy()
		regional.Region = region
		if _, err := eks.NewFromConfig(regional).ListClusters(ctx, &eks.ListClustersInput{MaxResults: aws.Int32(1)}); err != nil {
			t.Logf("%s: EKS does not answer: %v", region, err)
			continue
		}
		live = append(live, region)
	}
	builtIn := fleetdata.Regions(cfg.Region)
	slices.Sort(live)
	slices.Sort(builtIn)
	for _, r := range live {
		if !slices.Contains(builtIn, r) {
			t.Errorf("%s is enabled for every account and runs EKS, and the built-in list (as of %s) does not have it", r, fleetdata.RegionsAsOf)
		}
	}
	for _, r := range builtIn {
		if !slices.Contains(live, r) {
			t.Errorf("%s is in the built-in list (as of %s), and it is not enabled for every account or EKS does not answer there", r, fleetdata.RegionsAsOf)
		}
	}
}
