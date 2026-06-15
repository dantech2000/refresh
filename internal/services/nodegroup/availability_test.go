package nodegroup

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// fakeOfferingsAPI serves subnet→AZ mappings and per-instance-type offered AZs.
type fakeOfferingsAPI struct {
	subnetAZ map[string]string   // subnetID → AZ
	offered  map[string][]string // instanceType → AZs offering it
}

func (f *fakeOfferingsAPI) DescribeSubnets(_ context.Context, in *ec2.DescribeSubnetsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error) {
	out := &ec2.DescribeSubnetsOutput{}
	for _, id := range in.SubnetIds {
		if az, ok := f.subnetAZ[id]; ok {
			out.Subnets = append(out.Subnets, ec2types.Subnet{SubnetId: aws.String(id), AvailabilityZone: aws.String(az)})
		}
	}
	return out, nil
}

func (f *fakeOfferingsAPI) DescribeInstanceTypeOfferings(_ context.Context, in *ec2.DescribeInstanceTypeOfferingsInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstanceTypeOfferingsOutput, error) {
	// The pre-flight filters by instance-type; echo back that type's offered AZs.
	var it string
	for _, fl := range in.Filters {
		if aws.ToString(fl.Name) == "instance-type" && len(fl.Values) > 0 {
			it = fl.Values[0]
		}
	}
	out := &ec2.DescribeInstanceTypeOfferingsOutput{}
	for _, az := range f.offered[it] {
		out.InstanceTypeOfferings = append(out.InstanceTypeOfferings, ec2types.InstanceTypeOffering{
			InstanceType: ec2types.InstanceType(it),
			Location:     aws.String(az),
			LocationType: ec2types.LocationTypeAvailabilityZone,
		})
	}
	return out, nil
}

func TestCheckInstanceTypeAvailability(t *testing.T) {
	api := &fakeOfferingsAPI{
		subnetAZ: map[string]string{"subnet-a": "us-east-1a", "subnet-b": "us-east-1b"},
		offered: map[string][]string{
			"m6i.large": {"us-east-1a", "us-east-1b"}, // available everywhere
			"m7g.large": {"us-east-1a"},               // not offered in 1b
		},
	}

	got, err := checkInstanceTypeAvailability(context.Background(), api,
		[]string{"m6i.large", "m7g.large"}, []string{"subnet-a", "subnet-b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d unavailable, want 1: %+v", len(got), got)
	}
	if got[0].InstanceType != "m7g.large" || got[0].AvailabilityZone != "us-east-1b" {
		t.Errorf("unavailable = %+v, want m7g.large/us-east-1b", got[0])
	}
}

func TestCheckInstanceTypeAvailability_AllAvailable(t *testing.T) {
	api := &fakeOfferingsAPI{
		subnetAZ: map[string]string{"subnet-a": "us-east-1a"},
		offered:  map[string][]string{"m6i.large": {"us-east-1a"}},
	}
	got, err := checkInstanceTypeAvailability(context.Background(), api, []string{"m6i.large"}, []string{"subnet-a"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no unavailable offerings, got %+v", got)
	}
}

func TestCheckInstanceTypeAvailability_NoInstanceTypes(t *testing.T) {
	// Custom-launch-template nodegroups report no InstanceTypes — nothing to check.
	api := &fakeOfferingsAPI{}
	got, err := checkInstanceTypeAvailability(context.Background(), api, nil, []string{"subnet-a"})
	if err != nil || got != nil {
		t.Errorf("custom-LT nodegroup should be a no-op, got %v / %v", got, err)
	}
}
