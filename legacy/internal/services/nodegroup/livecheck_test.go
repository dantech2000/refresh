//go:build livecheck

package nodegroup

import (
	"context"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// TestLiveInstanceOfferings runs the roll's instance-type check against the
// default VPC's subnets.
func TestLiveInstanceOfferings(t *testing.T) {
	ctx := context.Background()
	tr := http.DefaultTransport.(*http.Transport).Clone()
	t.Cleanup(tr.CloseIdleConnections) // keep-alive connections are not a leak
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion("us-east-1"), config.WithHTTPClient(&http.Client{Transport: tr}))
	if err != nil {
		t.Fatal(err)
	}
	api := ec2.NewFromConfig(cfg)
	subs, err := api.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{Filters: []ec2types.Filter{{Name: aws.String("default-for-az"), Values: []string{"true"}}}})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range subs.Subnets {
		ids = append(ids, aws.ToString(s.SubnetId))
		t.Logf("subnet %s in %s", aws.ToString(s.SubnetId), aws.ToString(s.AvailabilityZone))
	}
	if len(ids) == 0 {
		t.Skip("no default VPC subnets")
	}
	un, err := checkInstanceTypeAvailability(ctx, api, []string{"m5.large", "t3.medium", "m7g.large", "p5.48xlarge", "not-a-type.large"}, ids)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range un {
		t.Logf("not offered: %s in %s", u.InstanceType, u.AvailabilityZone)
	}
}
