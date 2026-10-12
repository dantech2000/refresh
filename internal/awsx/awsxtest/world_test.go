package awsxtest_test

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/servicequotas"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

const region = "us-east-1"

// clients are real SDK clients that reach a fake server.
type clients struct {
	eks *eks.Client
	sts *sts.Client
	ec2 *ec2.Client
	asg *autoscaling.Client
	ssm *ssm.Client
	cw  *cloudwatch.Client
	sq  *servicequotas.Client
}

func newClients(cfg aws.Config) *clients {
	return &clients{
		eks: eks.NewFromConfig(cfg),
		sts: sts.NewFromConfig(cfg),
		ec2: ec2.NewFromConfig(cfg),
		asg: autoscaling.NewFromConfig(cfg),
		ssm: ssm.NewFromConfig(cfg),
		cw:  cloudwatch.NewFromConfig(cfg),
		sq:  servicequotas.NewFromConfig(cfg),
	}
}

// fixture is a seeded world, a server for it, and clients that reach it.
type fixture struct {
	world  *fleettest.World
	server *awsxtest.Server
	// clients retry as Server.Config sets up. once makes one attempt.
	clients, once *clients
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	world := newWorld()
	server := awsxtest.NewServer(world)
	t.Cleanup(server.Close)
	cfg := server.Config(region)
	once := cfg.Copy()
	once.Retryer = func() aws.Retryer { return aws.NopRetryer{} }
	return &fixture{world: world, server: server, clients: newClients(cfg), once: newClients(once)}
}

// testContext bounds every wait of a test, so a missing handshake fails the
// test and does not hang it.
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// requests returns the logged requests of one operation.
func (f *fixture) requests(op fleettest.Operation) []awsxtest.Request {
	var out []awsxtest.Request
	for _, r := range f.server.Requests() {
		if r.Operation == op {
			out = append(out, r)
		}
	}
	return out
}

const (
	updateA = "11111111-1111-4111-8111-111111111111"
	updateB = "22222222-2222-4222-8222-222222222222"
	// firstUpdate is the ID of the first update a world starts.
	firstUpdate = "00000000-0000-4000-8000-000000000001"
)

// newWorld returns a world with at least two items behind every paged
// operation.
func newWorld() *fleettest.World {
	w := fleettest.New()
	now := w.Now()
	created := now.Add(-24 * time.Hour)
	w.Edit(func(s *fleettest.State) {
		s.Clusters = []*fleettest.Cluster{
			{
				Name: "prod", Version: "1.32", SupportType: "EXTENDED", AutoMode: true,
				VPCID: "vpc-1", SubnetIDs: []string{"subnet-a", "subnet-b"},
				LogTypes: []string{"api", "audit"}, EncryptionKeyARN: "arn:aws:kms:us-east-1:123456789012:key/k",
				DeletionProtection: true,
				HealthIssues:       []fleettest.Issue{{Code: "Ec2SubnetNotFound", Message: "subnet-z is gone", ResourceIDs: []string{"subnet-z"}}},
				Nodegroups: []*fleettest.Nodegroup{
					{
						Name: "ng-a", Version: "1.32", ReleaseVersion: "1.32.0-20250101",
						Subnets: []string{"subnet-a"}, AutoScalingGroups: []string{"eks-ng-a"},
						LaunchTemplate: &fleettest.LaunchTemplate{ID: "lt-1", Version: "2"},
						HealthIssues:   []fleettest.Issue{{Code: "AsgInstanceLaunchFailures", Message: "no capacity"}},
					},
					{Name: "ng-b", Version: "1.31"},
				},
				Addons: []*fleettest.Addon{
					{Name: "coredns", Version: "v1.11.1-eksbuild.1", HealthIssues: []fleettest.Issue{{Code: "InsufficientNumberOfReplicas", Message: "0 of 2"}}},
					{Name: "vpc-cni", Version: "v1.18.0-eksbuild.1"},
				},
				Insights: []fleettest.Insight{
					{
						ID: "in-1", Name: "Deprecated APIs removed in Kubernetes v1.33", Status: "ERROR", Reason: "Deprecated API usage detected",
						Description: "Checks for deprecated APIs", Recommendation: "Update the manifests",
						Resources: []fleettest.InsightResource{{URI: "/apis/batch/v1beta1/cronjobs/x", Status: "ERROR"}},
						Deprecations: []fleettest.Deprecation{{
							Usage: "/apis/batch/v1beta1/cronjobs", ReplacedWith: "/apis/batch/v1/cronjobs", StopServingVersion: "1.33",
							Clients: []fleettest.ClientStat{{UserAgent: "kubectl", Requests: 7, LastRequest: created}},
						}},
					},
					{ID: "in-2", Name: "Kubelet version skew", Status: "PASSING"},
				},
				Updates: []*fleettest.Update{
					{ID: updateA, Type: fleettest.TypeVersionUpdate, CreatedAt: created, Params: []fleettest.UpdateParam{{Type: "Version", Value: "1.32"}}},
					{
						ID: updateB, Type: fleettest.TypeVersionRollback, Status: fleettest.UpdateFailed, CreatedAt: created,
						Errors: []fleettest.UpdateError{{Code: "Unknown", Message: "boom", ResourceIDs: []string{"prod"}}},
					},
				},
			},
			{Name: "staging", Version: "1.33"},
			{Name: "elsewhere", Version: "1.33", Region: "eu-west-1"},
		}
		s.AddonVersions = []fleettest.AddonVersion{
			{Addon: "vpc-cni", Version: "v1.19.0-eksbuild.1", ClusterVersions: []string{"1.32", "1.33"}, Default: true},
			{Addon: "vpc-cni", Version: "v1.18.0-eksbuild.1", ClusterVersions: []string{"1.32"}},
			{Addon: "coredns", Version: "v1.11.4-eksbuild.2", ClusterVersions: []string{"1.32"}},
		}
		s.ClusterVersions = []fleettest.ClusterVersion{
			{Version: "1.33", EndOfStandardSupport: created.AddDate(1, 0, 0), EndOfExtendedSupport: created.AddDate(2, 0, 0)},
			{Version: "1.32", Status: "EXTENDED_SUPPORT"},
		}
		s.Images = []fleettest.Image{{ID: "ami-1", Name: "old", Created: created}, {ID: "ami-2", Name: "new", Created: now}}
		s.Instances = []fleettest.Instance{
			{
				ID: "i-1", ImageID: "ami-1", Type: "m5.large", AvailabilityZone: "us-east-1a", LaunchTime: created,
				Tags: map[string]string{"eks:eks-cluster-name": "prod", "karpenter.sh/nodepool": "default"},
			},
			{ID: "i-2", ImageID: "ami-2", Type: "m5.xlarge", State: "pending", Lifecycle: "spot", AvailabilityZone: "us-east-1b"},
		}
		s.LaunchTemplateVersions = []fleettest.LaunchTemplateVersion{
			{TemplateID: "lt-1", Version: "1", ImageID: "ami-1"},
			{TemplateID: "lt-1", Version: "2", ImageID: "ami-2"},
		}
		s.Subnets = []fleettest.Subnet{
			{ID: "subnet-a", AvailabilityZone: "us-east-1a", VPCID: "vpc-1"},
			{ID: "subnet-b", AvailabilityZone: "us-east-1b", VPCID: "vpc-1"},
		}
		s.VPCs = []fleettest.VPC{{ID: "vpc-1", CIDR: "10.0.0.0/16"}, {ID: "vpc-2", CIDR: "10.1.0.0/16"}}
		s.Offerings = []fleettest.Offering{
			{InstanceType: "m5.large", AvailabilityZone: "us-east-1a"},
			{InstanceType: "m5.large", AvailabilityZone: "us-east-1b"},
			{InstanceType: "m5.xlarge", AvailabilityZone: "us-east-1a"},
		}
		s.AutoScalingGroups = []fleettest.AutoScalingGroup{
			{Name: "eks-ng-a", InstanceIDs: []string{"i-1", "i-2"}},
			{Name: "eks-ng-b"},
		}
		s.ScalingActivities = []fleettest.ScalingActivity{
			{ID: "act-2", Group: "eks-ng-a", Status: "Failed", Message: "vCPU limit <16> reached", Description: "Launching a new EC2 instance", Start: now},
			{ID: "act-1", Group: "eks-ng-a", Status: "Successful", Description: "Launching a new EC2 instance", Start: created},
		}
		s.Metrics = []fleettest.Metric{
			{Namespace: "AWS/EC2", Name: "CPUUtilization", Dimensions: map[string]string{"InstanceId": "i-1"}, Values: []float64{10, 30}},
			{Namespace: "AWS/EC2", Name: "CPUUtilization", Dimensions: map[string]string{"InstanceId": "i-2"}, Values: []float64{50}},
		}
		s.Quotas = []fleettest.Quota{{ServiceCode: "ec2", QuotaCode: "L-1216C47A", Value: 64}}
	})
	return w
}
