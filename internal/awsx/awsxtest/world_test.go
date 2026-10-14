package awsxtest_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sync"
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

const (
	// region is where newWorld puts everything the matrix reads.
	region = "us-east-1"
	// otherRegion holds one decoy of each kind, which a call in region
	// must never return.
	otherRegion = "eu-west-1"
	// emptyRegion holds nothing.
	emptyRegion = "ap-south-1"
)

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

// response is one HTTP response as it came off the wire.
type response struct {
	Status int
	Header http.Header
	Body   []byte
}

// recorder is an SDK HTTP client that keeps every response it passes on.
type recorder struct {
	next aws.HTTPClient

	mu        sync.Mutex
	responses []response
}

func (r *recorder) Do(req *http.Request) (*http.Response, error) {
	resp, err := r.next.Do(req)
	if err != nil {
		return nil, err
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	r.mu.Lock()
	defer r.mu.Unlock()
	r.responses = append(r.responses, response{Status: resp.StatusCode, Header: resp.Header.Clone(), Body: body})
	return resp, nil
}

func (r *recorder) all() []response {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]response(nil), r.responses...)
}

// fixture is a seeded world, a server for it, and clients that reach it.
type fixture struct {
	world  *fleettest.World
	server *awsxtest.Server
	// clients retry as Server.Config sets up. once makes one attempt.
	clients, once *clients
	// wire holds the responses that clients and once received.
	wire *recorder
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	world := newWorld()
	server := awsxtest.NewServer(world)
	t.Cleanup(server.Close)
	f := &fixture{world: world, server: server}
	cfg := server.Config(region)
	f.wire = &recorder{next: cfg.HTTPClient}
	cfg.HTTPClient = f.wire
	once := cfg.Copy()
	once.Retryer = func() aws.Retryer { return aws.NopRetryer{} }
	f.clients, f.once = newClients(cfg), newClients(once)
	return f
}

// in returns retrying clients for another region.
func (f *fixture) in(region string) *clients {
	return newClients(f.server.Config(region))
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
	// parameter is the SSM parameter newWorld seeds.
	parameter = "/refresh/test/image_id"
)

// newWorld returns a world with at least two items behind every paged
// operation, all in region, and decoys in otherRegion.
func newWorld() *fleettest.World {
	w := fleettest.New()
	now := w.Now()
	created := now.Add(-24 * time.Hour)
	w.Edit(func(s *fleettest.State) {
		s.Clusters = []*fleettest.Cluster{
			{
				Name: "prod", Version: "1.32", Region: region, SupportType: "EXTENDED", AutoMode: true,
				VPCID: "vpc-1", SubnetIDs: []string{"subnet-a", "subnet-b"},
				LogTypes: []string{"api", "audit"}, EncryptionKeyARN: "arn:aws:kms:us-east-1:123456789012:key/k",
				DeletionProtection: true, Tags: map[string]string{"team": "platform"},
				HealthIssues: []fleettest.Issue{{Code: "Ec2SubnetNotFound", Message: "subnet-z is gone", ResourceIDs: []string{"subnet-z"}}},
				Nodegroups: []*fleettest.Nodegroup{
					{
						Name: "ng-a", Version: "1.32", ReleaseVersion: "1.32.0-20250101", CreatedAt: created,
						Subnets: []string{"subnet-a"}, AutoScalingGroups: []string{"eks-ng-a"},
						MaxUnavailable: 2, UpdateStrategy: "MINIMAL",
						LaunchTemplate: &fleettest.LaunchTemplate{ID: "lt-1", Version: "2"},
						Labels:         map[string]string{"pool": "general"},
						HealthIssues:   []fleettest.Issue{{Code: "AsgInstanceLaunchFailures", Message: "no capacity"}},
					},
					{Name: "ng-b", Version: "1.31"},
				},
				Addons: []*fleettest.Addon{
					{
						Name: "coredns", Version: "v1.11.1-eksbuild.1", CreatedAt: created,
						HealthIssues: []fleettest.Issue{{Code: "InsufficientNumberOfReplicas", Message: "0 of 2"}},
					},
					{Name: "vpc-cni", Version: "v1.18.0-eksbuild.1"},
				},
				Insights: []fleettest.Insight{
					{
						ID: "in-1", Name: "Deprecated APIs removed in Kubernetes v1.33", Status: fleettest.InsightError, Reason: "Deprecated API usage detected",
						Description: "Checks for deprecated APIs", Recommendation: "Update the manifests",
						AdditionalInfo: map[string]string{"EKS update cluster documentation": "https://docs.aws.amazon.com/eks/"},
						Resources:      []fleettest.InsightResource{{URI: "/apis/batch/v1beta1/cronjobs/x", Status: fleettest.InsightError}},
						Deprecations: []fleettest.Deprecation{{
							Usage: "/apis/batch/v1beta1/cronjobs", ReplacedWith: "/apis/batch/v1/cronjobs", StopServingVersion: "1.33",
							Clients: []fleettest.ClientStat{{UserAgent: "kubectl", Requests: 7, LastRequest: created}},
						}},
					},
					{ID: "in-2", Name: "Kubelet version skew", Status: fleettest.InsightPassing},
				},
				Updates: []*fleettest.Update{
					{ID: updateA, Type: fleettest.TypeVersionUpdate, CreatedAt: created, Params: []fleettest.UpdateParam{{Type: "Version", Value: "1.32"}}},
					{
						ID: updateB, Type: fleettest.TypeVersionRollback, Status: fleettest.UpdateFailed, CreatedAt: created,
						Errors: []fleettest.UpdateError{{Code: "Unknown", Message: "boom", ResourceIDs: []string{"prod"}}},
					},
				},
			},
			{Name: "staging", Version: "1.33", Region: region, Addons: []*fleettest.Addon{{Name: "kube-proxy", Version: "v1.33.0-eksbuild.1"}}},
			{Name: "elsewhere", Version: "1.33", Region: otherRegion},
		}
		s.AddonVersions = []fleettest.AddonVersion{
			{Region: region, Addon: "vpc-cni", Version: "v1.19.0-eksbuild.1", ClusterVersions: []string{"1.32", "1.33"}, Default: true},
			{Region: region, Addon: "vpc-cni", Version: "v1.18.0-eksbuild.1", ClusterVersions: []string{"1.32"}},
			{Region: region, Addon: "coredns", Version: "v1.11.4-eksbuild.2", ClusterVersions: []string{"1.32"}},
			{Region: otherRegion, Addon: "decoy", Version: "v9", ClusterVersions: []string{"1.32"}},
		}
		s.ClusterVersions = []fleettest.ClusterVersion{
			{Region: region, Version: "1.33", EndOfStandardSupport: created.AddDate(1, 0, 0), EndOfExtendedSupport: created.AddDate(2, 0, 0)},
			{Region: region, Version: "1.32", Status: fleettest.ExtendedSupport},
			{Region: otherRegion, Version: "1.20", Status: fleettest.Unsupported},
		}
		s.Parameters = []fleettest.Parameter{
			{Region: region, Name: parameter, Value: "ami-here"},
			{Region: otherRegion, Name: parameter, Value: "ami-decoy"},
		}
		s.Images = []fleettest.Image{
			{Region: region, ID: "ami-1", Name: "old", Created: created},
			{Region: region, ID: "ami-2", Name: "new", Created: now},
			{Region: otherRegion, ID: "ami-decoy"},
		}
		s.Instances = []fleettest.Instance{
			{
				Region: region, ID: "i-1", ImageID: "ami-1", Type: "m5.large", AvailabilityZone: "us-east-1a", LaunchTime: created,
				Tags: map[string]string{"eks:eks-cluster-name": "prod", "karpenter.sh/nodepool": "default"},
			},
			{Region: region, ID: "i-2", ImageID: "ami-2", Type: "m5.xlarge", State: "pending", Lifecycle: "spot", AvailabilityZone: "us-east-1b"},
			{Region: otherRegion, ID: "i-decoy", Tags: map[string]string{"eks:eks-cluster-name": "prod", "karpenter.sh/nodepool": "default"}},
		}
		s.LaunchTemplateVersions = []fleettest.LaunchTemplateVersion{
			{Region: region, TemplateID: "lt-1", Version: "1", ImageID: "ami-1"},
			{Region: region, TemplateID: "lt-1", Version: "2", ImageID: "ami-2"},
			{Region: otherRegion, TemplateID: "lt-1", Version: "3", ImageID: "ami-decoy"},
		}
		s.Subnets = []fleettest.Subnet{
			{Region: region, ID: "subnet-a", AvailabilityZone: "us-east-1a", VPCID: "vpc-1"},
			{Region: region, ID: "subnet-b", AvailabilityZone: "us-east-1b", VPCID: "vpc-1"},
			{Region: otherRegion, ID: "subnet-decoy", AvailabilityZone: "eu-west-1a"},
		}
		s.VPCs = []fleettest.VPC{
			{Region: region, ID: "vpc-1", CIDR: "10.0.0.0/16"},
			{Region: region, ID: "vpc-2", CIDR: "10.1.0.0/16"},
			{Region: otherRegion, ID: "vpc-decoy", CIDR: "10.9.0.0/16"},
		}
		s.Offerings = []fleettest.Offering{
			{Region: region, InstanceType: "m5.large", AvailabilityZone: "us-east-1a"},
			{Region: region, InstanceType: "m5.large", AvailabilityZone: "us-east-1b"},
			{Region: region, InstanceType: "m5.xlarge", AvailabilityZone: "us-east-1a"},
			{Region: otherRegion, InstanceType: "m5.large", AvailabilityZone: "eu-west-1a"},
		}
		s.AutoScalingGroups = []fleettest.AutoScalingGroup{
			{Region: region, Name: "eks-ng-a", InstanceIDs: []string{"i-1", "i-2"}, AvailabilityZones: []string{"us-east-1a", "us-east-1b"}},
			{Region: region, Name: "eks-ng-b"},
			{Region: otherRegion, Name: "eks-ng-a", InstanceIDs: []string{"i-decoy"}},
		}
		s.ScalingActivities = []fleettest.ScalingActivity{
			{Region: region, ID: "act-2", Group: "eks-ng-a", Status: "Failed", Message: "vCPU limit <16> reached", Description: "Launching a new EC2 instance", Start: now},
			{Region: region, ID: "act-1", Group: "eks-ng-a", Status: "Successful", Description: "Launching a new EC2 instance", Start: created},
			{Region: otherRegion, ID: "act-decoy", Group: "eks-ng-a", Status: "Failed", Start: now},
		}
		s.Metrics = []fleettest.Metric{
			{Region: region, Namespace: "AWS/EC2", Name: "CPUUtilization", Dimensions: map[string]string{"InstanceId": "i-1"}, Values: []float64{10, 30}},
			{Region: region, Namespace: "AWS/EC2", Name: "CPUUtilization", Dimensions: map[string]string{"InstanceId": "i-2"}, Values: []float64{50}},
			{Region: otherRegion, Namespace: "AWS/EC2", Name: "CPUUtilization", Dimensions: map[string]string{"InstanceId": "i-1"}, Values: []float64{99}},
		}
		s.Quotas = []fleettest.Quota{
			{Region: region, ServiceCode: "ec2", QuotaCode: "L-1216C47A", Value: 64},
			{Region: otherRegion, ServiceCode: "ec2", QuotaCode: "L-1216C47A", Value: 1},
		}
	})
	return w
}
