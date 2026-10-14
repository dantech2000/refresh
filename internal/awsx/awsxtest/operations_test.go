package awsxtest_test

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/servicequotas"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// The types below hold the output fields that refresh reads, so a case
// compares exactly those.

type clusterOut struct {
	Name, ARN, Version, Status, Platform, Endpoint string
	VPC                                            string
	Subnets                                        []string
	Support                                        string
	AutoMode, DeletionProtection                   bool
	LogTypes                                       []string
	KeyARN                                         string
	Issues                                         []string
	Created                                        time.Time
	Tags                                           map[string]string
}

type nodegroupOut struct {
	Name, Cluster, Version, Release, Status, AMIType, Capacity string
	InstanceTypes, Subnets, Groups                             []string
	Desired, Min, Max                                          int32
	MaxUnavailable                                             int32
	UpdateStrategy                                             string
	TemplateID, TemplateVersion                                string
	Issues                                                     []string
	Labels                                                     map[string]string
	Created, Modified                                          time.Time
}

type addonOut struct {
	Name, Version, Status string
	Issues                []string
	Created, Modified     time.Time
}

type addonVersionOut struct {
	Addon, Version string
	Compatible     []string
	Default        bool
}

type clusterVersionOut struct {
	Version, Status    string
	Standard, Extended time.Time
}

type insightOut struct {
	ID, Name, Category, Version, Status, Reason string
}

type insightDetail struct {
	Summary        insightOut
	Recommendation string
	Info           map[string]string
	Resources      []string
	Usage          string
	ReplacedWith   string
	Clients        []string
	Requests       int32
}

type updateOut struct {
	ID     string
	Type   ekstypes.UpdateType
	Status ekstypes.UpdateStatus
	Params []string
	Errors []string
}

type instanceOut struct {
	ID, Image, Type, State, Lifecycle, Zone string
	Launched                                time.Time
}

type activityOut struct {
	ID, Group, Status, Message, Description string
	Start                                   time.Time
}

type metricOut struct {
	ID     string
	Values []float64
	Stamps int
}

func issueCodes[T any](issues []T, code func(T) string) []string {
	var out []string
	for _, i := range issues {
		out = append(out, code(i))
	}
	return out
}

func updateOf(u *ekstypes.Update) updateOut {
	out := updateOut{ID: aws.ToString(u.Id), Type: u.Type, Status: u.Status}
	for _, p := range u.Params {
		out.Params = append(out.Params, string(p.Type)+"="+aws.ToString(p.Value))
	}
	for _, e := range u.Errors {
		out.Errors = append(out.Errors, string(e.ErrorCode)+": "+aws.ToString(e.ErrorMessage))
	}
	return out
}

func insightOf(id, name *string, category ekstypes.Category, version *string, status *ekstypes.InsightStatus) insightOut {
	return insightOut{
		ID: aws.ToString(id), Name: aws.ToString(name), Category: string(category), Version: aws.ToString(version),
		Status: string(status.Status), Reason: aws.ToString(status.Reason),
	}
}

// token is a client request token of a length EKS accepts.
const token = "0123456789abcdef0123456789abcdef-1"

// matrix returns one case per AWS operation refresh calls. The expected
// values are what newWorld seeds.
func matrix() []opCase {
	return slices.Concat(
		identityCases(),
		eksReadCases(),
		eksInsightCases(),
		eksUpdateCases(),
		parameterCases(),
		ec2Cases(),
		autoScalingCases(),
		metricCases(),
	)
}

// everywhere marks a case whose operation answers the same in every region.
func everywhere(tc opCase) opCase {
	tc.elsewhere = tc.want
	return tc
}

// inEmptyRegion sets what a case decodes in a region that holds nothing,
// for an operation that answers there with something other than no items.
func inEmptyRegion(want any, tc opCase) opCase {
	tc.elsewhere = want
	return tc
}

// inPages sets the number of pages of a paged case whose items are not one
// per page.
func inPages(n int, tc opCase) opCase {
	tc.wantPages = n
	return tc
}

// seedTimes returns the two times newWorld uses: the world's start, and the
// day before it.
func seedTimes() (now, created time.Time) {
	now = fleettest.New().Now()
	return now, now.Add(-24 * time.Hour)
}

// startedUpdate is the update the first Update call of a world answers.
func startedUpdate(kind ekstypes.UpdateType, params ...string) []updateOut {
	return []updateOut{{ID: firstUpdate, Type: kind, Status: ekstypes.UpdateStatusInProgress, Params: params}}
}

func identityCases() []opCase {
	return []opCase{
		everywhere(newCase(fleettest.STSGetCallerIdentity, []string{"123456789012", "arn:aws:iam::123456789012:user/fake", "AIDAFAKE"},
			func(ctx context.Context, c *clients, _ *string) ([]string, *string, error) {
				out, err := c.sts.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
				if err != nil {
					return nil, nil, err
				}
				return []string{aws.ToString(out.Account), aws.ToString(out.Arn), aws.ToString(out.UserId)}, nil, nil
			})),
	}
}

func eksReadCases() []opCase {
	worldStart, created := seedTimes()
	return []opCase{
		pagedCase(fleettest.EKSListClusters, []string{"prod", "staging"},
			func(ctx context.Context, c *clients, token *string) ([]string, *string, error) {
				out, err := c.eks.ListClusters(ctx, &eks.ListClustersInput{NextToken: token})
				if err != nil {
					return nil, nil, err
				}
				return out.Clusters, out.NextToken, nil
			}),

		newCase(fleettest.EKSDescribeCluster, []clusterOut{{
			Name: "prod", ARN: "arn:aws:eks:us-east-1:123456789012:cluster/prod", Version: "1.32", Status: "ACTIVE",
			Platform: "eks.1", Endpoint: "https://prod.eks.fake.invalid", VPC: "vpc-1", Subnets: []string{"subnet-a", "subnet-b"},
			Support: "EXTENDED", AutoMode: true, DeletionProtection: true, LogTypes: []string{"api", "audit"},
			KeyARN: "arn:aws:kms:us-east-1:123456789012:key/k", Issues: []string{"Ec2SubnetNotFound: subnet-z is gone"},
			Created: worldStart, Tags: map[string]string{"team": "platform"},
		}}, func(ctx context.Context, c *clients, _ *string) ([]clusterOut, *string, error) {
			out, err := c.eks.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String("prod")})
			if err != nil {
				return nil, nil, err
			}
			cl := out.Cluster
			got := clusterOut{
				Name: aws.ToString(cl.Name), ARN: aws.ToString(cl.Arn), Version: aws.ToString(cl.Version), Status: string(cl.Status),
				Platform: aws.ToString(cl.PlatformVersion), Endpoint: aws.ToString(cl.Endpoint),
				VPC: aws.ToString(cl.ResourcesVpcConfig.VpcId), Subnets: cl.ResourcesVpcConfig.SubnetIds,
				Support: string(cl.UpgradePolicy.SupportType), AutoMode: aws.ToBool(cl.ComputeConfig.Enabled),
				DeletionProtection: aws.ToBool(cl.DeletionProtection), KeyARN: aws.ToString(cl.EncryptionConfig[0].Provider.KeyArn),
				Created: aws.ToTime(cl.CreatedAt).UTC(), Tags: cl.Tags,
				Issues: issueCodes(cl.Health.Issues, func(i ekstypes.ClusterIssue) string {
					return string(i.Code) + ": " + aws.ToString(i.Message)
				}),
			}
			for _, l := range cl.Logging.ClusterLogging[0].Types {
				got.LogTypes = append(got.LogTypes, string(l))
			}
			return []clusterOut{got}, nil, nil
		}),

		pagedCase(fleettest.EKSListNodegroups, []string{"ng-a", "ng-b"},
			func(ctx context.Context, c *clients, token *string) ([]string, *string, error) {
				out, err := c.eks.ListNodegroups(ctx, &eks.ListNodegroupsInput{ClusterName: aws.String("prod"), NextToken: token})
				if err != nil {
					return nil, nil, err
				}
				return out.Nodegroups, out.NextToken, nil
			}),

		newCase(fleettest.EKSDescribeNodegroup, []nodegroupOut{{
			Name: "ng-a", Cluster: "prod", Version: "1.32", Release: "1.32.0-20250101", Status: "ACTIVE",
			AMIType: "AL2023_x86_64_STANDARD", Capacity: "ON_DEMAND", InstanceTypes: []string{"m5.large"},
			Subnets: []string{"subnet-a"}, Groups: []string{"eks-ng-a"}, Desired: 2, Min: 1, Max: 3,
			MaxUnavailable: 2, UpdateStrategy: "MINIMAL", TemplateID: "lt-1", TemplateVersion: "2", Issues: []string{"AsgInstanceLaunchFailures: no capacity"},
			Labels: map[string]string{"pool": "general"}, Created: created, Modified: worldStart,
		}}, func(ctx context.Context, c *clients, _ *string) ([]nodegroupOut, *string, error) {
			out, err := c.eks.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{ClusterName: aws.String("prod"), NodegroupName: aws.String("ng-a")})
			if err != nil {
				return nil, nil, err
			}
			ng := out.Nodegroup
			got := nodegroupOut{
				Name: aws.ToString(ng.NodegroupName), Cluster: aws.ToString(ng.ClusterName), Version: aws.ToString(ng.Version),
				Release: aws.ToString(ng.ReleaseVersion), Status: string(ng.Status), AMIType: string(ng.AmiType),
				Capacity: string(ng.CapacityType), InstanceTypes: ng.InstanceTypes, Subnets: ng.Subnets,
				Desired: aws.ToInt32(ng.ScalingConfig.DesiredSize), Min: aws.ToInt32(ng.ScalingConfig.MinSize), Max: aws.ToInt32(ng.ScalingConfig.MaxSize),
				MaxUnavailable: aws.ToInt32(ng.UpdateConfig.MaxUnavailable), UpdateStrategy: string(ng.UpdateConfig.UpdateStrategy),
				TemplateID: aws.ToString(ng.LaunchTemplate.Id), TemplateVersion: aws.ToString(ng.LaunchTemplate.Version),
				Labels: ng.Labels, Created: aws.ToTime(ng.CreatedAt).UTC(), Modified: aws.ToTime(ng.ModifiedAt).UTC(),
				Issues: issueCodes(ng.Health.Issues, func(i ekstypes.Issue) string {
					return string(i.Code) + ": " + aws.ToString(i.Message)
				}),
			}
			for _, g := range ng.Resources.AutoScalingGroups {
				got.Groups = append(got.Groups, aws.ToString(g.Name))
			}
			return []nodegroupOut{got}, nil, nil
		}),

		pagedCase(fleettest.EKSListAddons, []string{"coredns", "vpc-cni"},
			func(ctx context.Context, c *clients, token *string) ([]string, *string, error) {
				out, err := c.eks.ListAddons(ctx, &eks.ListAddonsInput{ClusterName: aws.String("prod"), NextToken: token})
				if err != nil {
					return nil, nil, err
				}
				return out.Addons, out.NextToken, nil
			}),

		newCase(fleettest.EKSDescribeAddon, []addonOut{{
			Name: "coredns", Version: "v1.11.1-eksbuild.1", Status: "ACTIVE", Issues: []string{"InsufficientNumberOfReplicas: 0 of 2"},
			Created: created, Modified: worldStart,
		}}, func(ctx context.Context, c *clients, _ *string) ([]addonOut, *string, error) {
			out, err := c.eks.DescribeAddon(ctx, &eks.DescribeAddonInput{ClusterName: aws.String("prod"), AddonName: aws.String("coredns")})
			if err != nil {
				return nil, nil, err
			}
			a := out.Addon
			return []addonOut{{
				Name: aws.ToString(a.AddonName), Version: aws.ToString(a.AddonVersion), Status: string(a.Status),
				Created: aws.ToTime(a.CreatedAt).UTC(), Modified: aws.ToTime(a.ModifiedAt).UTC(),
				Issues: issueCodes(a.Health.Issues, func(i ekstypes.AddonIssue) string {
					return string(i.Code) + ": " + aws.ToString(i.Message)
				}),
			}}, nil, nil
		}),

		// The API pages by add-on: two add-ons, three versions.
		inPages(2, pagedCase(fleettest.EKSDescribeAddonVersions, []addonVersionOut{
			{Addon: "vpc-cni", Version: "v1.19.0-eksbuild.1", Compatible: []string{"1.32"}, Default: true},
			{Addon: "vpc-cni", Version: "v1.18.0-eksbuild.1", Compatible: []string{"1.32"}},
			{Addon: "coredns", Version: "v1.11.4-eksbuild.2", Compatible: []string{"1.32"}},
		}, func(ctx context.Context, c *clients, token *string) ([]addonVersionOut, *string, error) {
			out, err := c.eks.DescribeAddonVersions(ctx, &eks.DescribeAddonVersionsInput{KubernetesVersion: aws.String("1.32"), NextToken: token})
			if err != nil {
				return nil, nil, err
			}
			var got []addonVersionOut
			for _, a := range out.Addons {
				for _, v := range a.AddonVersions {
					o := addonVersionOut{Addon: aws.ToString(a.AddonName), Version: aws.ToString(v.AddonVersion)}
					for _, compat := range v.Compatibilities {
						o.Compatible = append(o.Compatible, aws.ToString(compat.ClusterVersion))
						o.Default = o.Default || compat.DefaultVersion
					}
					got = append(got, o)
				}
			}
			return got, out.NextToken, nil
		})),

		pagedCase(fleettest.EKSDescribeClusterVersions, []clusterVersionOut{
			{Version: "1.33", Status: "STANDARD_SUPPORT", Standard: created.AddDate(1, 0, 0), Extended: created.AddDate(2, 0, 0)},
			{Version: "1.32", Status: "EXTENDED_SUPPORT"},
		}, func(ctx context.Context, c *clients, token *string) ([]clusterVersionOut, *string, error) {
			out, err := c.eks.DescribeClusterVersions(ctx, &eks.DescribeClusterVersionsInput{ClusterVersions: []string{"1.33", "1.32", "1.20"}, NextToken: token})
			if err != nil {
				return nil, nil, err
			}
			var got []clusterVersionOut
			for _, v := range out.ClusterVersions {
				got = append(got, clusterVersionOut{
					Version: aws.ToString(v.ClusterVersion), Status: string(v.VersionStatus),
					Standard: aws.ToTime(v.EndOfStandardSupportDate).UTC(), Extended: aws.ToTime(v.EndOfExtendedSupportDate).UTC(),
				})
			}
			return got, out.NextToken, nil
		}),
	}
}

func eksInsightCases() []opCase {
	return []opCase{
		pagedCase(fleettest.EKSListInsights, []insightOut{
			{ID: "in-1", Name: "Deprecated APIs removed in Kubernetes v1.33", Category: "UPGRADE_READINESS", Version: "1.33", Status: "ERROR", Reason: "Deprecated API usage detected"},
			{ID: "in-2", Name: "Kubelet version skew", Category: "UPGRADE_READINESS", Version: "1.33", Status: "PASSING"},
		}, func(ctx context.Context, c *clients, token *string) ([]insightOut, *string, error) {
			out, err := c.eks.ListInsights(ctx, &eks.ListInsightsInput{
				ClusterName: aws.String("prod"), NextToken: token,
				Filter: &ekstypes.InsightsFilter{Categories: []ekstypes.Category{ekstypes.CategoryUpgradeReadiness}, KubernetesVersions: []string{"1.33"}},
			})
			if err != nil {
				return nil, nil, err
			}
			var got []insightOut
			for _, in := range out.Insights {
				got = append(got, insightOf(in.Id, in.Name, in.Category, in.KubernetesVersion, in.InsightStatus))
			}
			return got, out.NextToken, nil
		}),

		newCase(fleettest.EKSDescribeInsight, []insightDetail{{
			Summary: insightOut{
				ID: "in-1", Name: "Deprecated APIs removed in Kubernetes v1.33", Category: "UPGRADE_READINESS", Version: "1.33",
				Status: "ERROR", Reason: "Deprecated API usage detected",
			},
			Recommendation: "Update the manifests", Resources: []string{"/apis/batch/v1beta1/cronjobs/x"},
			Info:  map[string]string{"EKS update cluster documentation": "https://docs.aws.amazon.com/eks/"},
			Usage: "/apis/batch/v1beta1/cronjobs", ReplacedWith: "/apis/batch/v1/cronjobs", Clients: []string{"kubectl"}, Requests: 7,
		}}, func(ctx context.Context, c *clients, _ *string) ([]insightDetail, *string, error) {
			out, err := c.eks.DescribeInsight(ctx, &eks.DescribeInsightInput{ClusterName: aws.String("prod"), Id: aws.String("in-1")})
			if err != nil {
				return nil, nil, err
			}
			in := out.Insight
			got := insightDetail{
				Summary:        insightOf(in.Id, in.Name, in.Category, in.KubernetesVersion, in.InsightStatus),
				Recommendation: aws.ToString(in.Recommendation),
				Info:           in.AdditionalInfo,
			}
			for _, r := range in.Resources {
				got.Resources = append(got.Resources, aws.ToString(r.KubernetesResourceUri))
			}
			d := in.CategorySpecificSummary.DeprecationDetails[0]
			got.Usage, got.ReplacedWith = aws.ToString(d.Usage), aws.ToString(d.ReplacedWith)
			for _, cs := range d.ClientStats {
				got.Clients = append(got.Clients, aws.ToString(cs.UserAgent))
				got.Requests += cs.NumberOfRequestsLast30Days
			}
			return []insightDetail{got}, nil, nil
		}),

		mutatingCase(fleettest.EKSStartInsightsRefresh, []string{"IN_PROGRESS"},
			func(c *fleettest.Cluster) string { return string(c.InsightsRefresh.Status) }, "COMPLETED",
			func(ctx context.Context, c *clients, _ *string) ([]string, *string, error) {
				out, err := c.eks.StartInsightsRefresh(ctx, &eks.StartInsightsRefreshInput{ClusterName: aws.String("prod")})
				if err != nil {
					return nil, nil, err
				}
				return []string{string(out.Status)}, nil, nil
			}),

		newCase(fleettest.EKSDescribeInsightsRefresh, []string{"COMPLETED"},
			func(ctx context.Context, c *clients, _ *string) ([]string, *string, error) {
				out, err := c.eks.DescribeInsightsRefresh(ctx, &eks.DescribeInsightsRefreshInput{ClusterName: aws.String("prod")})
				if err != nil {
					return nil, nil, err
				}
				return []string{string(out.Status)}, nil, nil
			}),
	}
}

func eksUpdateCases() []opCase {
	return []opCase{
		mutatingCase(fleettest.EKSUpdateNodegroupVersion, startedUpdate(ekstypes.UpdateTypeVersionUpdate, "Version=1.32", "ReleaseVersion=1.32.0-20260101"),
			func(c *fleettest.Cluster) string {
				ng := c.Nodegroup("ng-a")
				return ng.Version + " " + ng.ReleaseVersion + " " + string(ng.Status)
			}, "1.32 1.32.0-20260101 ACTIVE",
			func(ctx context.Context, c *clients, _ *string) ([]updateOut, *string, error) {
				out, err := c.eks.UpdateNodegroupVersion(ctx, &eks.UpdateNodegroupVersionInput{
					ClusterName: aws.String("prod"), NodegroupName: aws.String("ng-a"), ClientRequestToken: aws.String(token),
				})
				if err != nil {
					return nil, nil, err
				}
				return []updateOut{updateOf(out.Update)}, nil, nil
			}),

		mutatingCase(fleettest.EKSUpdateNodegroupConfig, startedUpdate(ekstypes.UpdateTypeConfigUpdate, "DesiredSize=3"),
			func(c *fleettest.Cluster) string {
				ng := c.Nodegroup("ng-a")
				return fmt.Sprintf("%d/%d/%d", ng.Desired, ng.Min, ng.Max)
			}, "3/1/3",
			func(ctx context.Context, c *clients, _ *string) ([]updateOut, *string, error) {
				out, err := c.eks.UpdateNodegroupConfig(ctx, &eks.UpdateNodegroupConfigInput{
					ClusterName: aws.String("prod"), NodegroupName: aws.String("ng-a"), ClientRequestToken: aws.String(token),
					ScalingConfig: &ekstypes.NodegroupScalingConfig{DesiredSize: aws.Int32(3)},
				})
				if err != nil {
					return nil, nil, err
				}
				return []updateOut{updateOf(out.Update)}, nil, nil
			}),

		mutatingCase(fleettest.EKSUpdateClusterVersion, startedUpdate(ekstypes.UpdateTypeVersionUpdate, "Version=1.33"),
			func(c *fleettest.Cluster) string { return c.Version + " " + string(c.Status) }, "1.33 ACTIVE",
			func(ctx context.Context, c *clients, _ *string) ([]updateOut, *string, error) {
				out, err := c.eks.UpdateClusterVersion(ctx, &eks.UpdateClusterVersionInput{
					Name: aws.String("prod"), Version: aws.String("1.33"), ClientRequestToken: aws.String(token),
				})
				if err != nil {
					return nil, nil, err
				}
				return []updateOut{updateOf(out.Update)}, nil, nil
			}),

		mutatingCase(fleettest.EKSUpdateAddon, startedUpdate(ekstypes.UpdateTypeAddonUpdate, "AddonVersion=v1.19.0-eksbuild.1"),
			func(c *fleettest.Cluster) string { return c.Addon("vpc-cni").Version }, "v1.19.0-eksbuild.1",
			func(ctx context.Context, c *clients, _ *string) ([]updateOut, *string, error) {
				out, err := c.eks.UpdateAddon(ctx, &eks.UpdateAddonInput{
					ClusterName: aws.String("prod"), AddonName: aws.String("vpc-cni"), AddonVersion: aws.String("v1.19.0-eksbuild.1"),
					ClientRequestToken: aws.String(token),
				})
				if err != nil {
					return nil, nil, err
				}
				return []updateOut{updateOf(out.Update)}, nil, nil
			}),

		newCase(fleettest.EKSDescribeUpdate, []updateOut{{
			ID: updateB, Type: ekstypes.UpdateTypeVersionRollback, Status: ekstypes.UpdateStatusFailed, Errors: []string{"Unknown: boom"},
		}}, func(ctx context.Context, c *clients, _ *string) ([]updateOut, *string, error) {
			out, err := c.eks.DescribeUpdate(ctx, &eks.DescribeUpdateInput{Name: aws.String("prod"), UpdateId: aws.String(updateB)})
			if err != nil {
				return nil, nil, err
			}
			return []updateOut{updateOf(out.Update)}, nil, nil
		}),

		pagedCase(fleettest.EKSListUpdates, []string{updateA, updateB},
			func(ctx context.Context, c *clients, token *string) ([]string, *string, error) {
				out, err := c.eks.ListUpdates(ctx, &eks.ListUpdatesInput{Name: aws.String("prod"), NextToken: token})
				if err != nil {
					return nil, nil, err
				}
				return out.UpdateIds, out.NextToken, nil
			}),
	}
}

func parameterCases() []opCase {
	return []opCase{
		newCase(fleettest.SSMGetParameter, []string{"ami-here"},
			func(ctx context.Context, c *clients, _ *string) ([]string, *string, error) {
				out, err := c.ssm.GetParameter(ctx, &ssm.GetParameterInput{
					Name: aws.String(parameter),
				})
				if err != nil {
					return nil, nil, err
				}
				return []string{aws.ToString(out.Parameter.Value)}, nil, nil
			}),
	}
}

func ec2Cases() []opCase {
	worldStart, created := seedTimes()
	return []opCase{
		pagedCase(fleettest.EC2DescribeImages, []string{"ami-1 " + created.Format(time.RFC3339), "ami-2 " + worldStart.Format(time.RFC3339)},
			func(ctx context.Context, c *clients, token *string) ([]string, *string, error) {
				out, err := c.ec2.DescribeImages(ctx, &ec2.DescribeImagesInput{ImageIds: []string{"ami-1", "ami-2"}, NextToken: token})
				if err != nil {
					return nil, nil, err
				}
				var got []string
				for _, i := range out.Images {
					got = append(got, aws.ToString(i.ImageId)+" "+aws.ToString(i.CreationDate))
				}
				return got, out.NextToken, nil
			}),

		pagedCase(fleettest.EC2DescribeInstances, []instanceOut{
			{ID: "i-1", Image: "ami-1", Type: "m5.large", State: "running", Zone: "us-east-1a", Launched: created},
			{ID: "i-2", Image: "ami-2", Type: "m5.xlarge", State: "pending", Lifecycle: "spot", Zone: "us-east-1b"},
		}, func(ctx context.Context, c *clients, token *string) ([]instanceOut, *string, error) {
			out, err := c.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
				InstanceIds: []string{"i-1", "i-2"}, NextToken: token,
				Filters: []ec2types.Filter{{Name: aws.String("instance-state-name"), Values: []string{"pending", "running"}}},
			})
			if err != nil {
				return nil, nil, err
			}
			var got []instanceOut
			for _, r := range out.Reservations {
				for _, i := range r.Instances {
					got = append(got, instanceOut{
						ID: aws.ToString(i.InstanceId), Image: aws.ToString(i.ImageId), Type: string(i.InstanceType),
						State: string(i.State.Name), Lifecycle: string(i.InstanceLifecycle),
						Zone: aws.ToString(i.Placement.AvailabilityZone), Launched: aws.ToTime(i.LaunchTime).UTC(),
					})
				}
			}
			return got, out.NextToken, nil
		}),

		pagedCase(fleettest.EC2DescribeLaunchTemplateVersions, []string{"lt-1/1 ami-1", "lt-1/2 ami-2"},
			func(ctx context.Context, c *clients, token *string) ([]string, *string, error) {
				out, err := c.ec2.DescribeLaunchTemplateVersions(ctx, &ec2.DescribeLaunchTemplateVersionsInput{
					LaunchTemplateId: aws.String("lt-1"), Versions: []string{"1", "2"}, NextToken: token,
				})
				if err != nil {
					return nil, nil, err
				}
				var got []string
				for _, v := range out.LaunchTemplateVersions {
					got = append(got, aws.ToString(v.LaunchTemplateId)+"/"+fmt.Sprint(aws.ToInt64(v.VersionNumber))+" "+aws.ToString(v.LaunchTemplateData.ImageId))
				}
				return got, out.NextToken, nil
			}),

		pagedCase(fleettest.EC2DescribeSubnets, []string{"subnet-a us-east-1a", "subnet-b us-east-1b"},
			func(ctx context.Context, c *clients, token *string) ([]string, *string, error) {
				out, err := c.ec2.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{SubnetIds: []string{"subnet-a", "subnet-b"}, NextToken: token})
				if err != nil {
					return nil, nil, err
				}
				var got []string
				for _, s := range out.Subnets {
					got = append(got, aws.ToString(s.SubnetId)+" "+aws.ToString(s.AvailabilityZone))
				}
				return got, out.NextToken, nil
			}),

		pagedCase(fleettest.EC2DescribeInstanceTypeOfferings, []string{"m5.large us-east-1a", "m5.large us-east-1b"},
			func(ctx context.Context, c *clients, token *string) ([]string, *string, error) {
				out, err := c.ec2.DescribeInstanceTypeOfferings(ctx, &ec2.DescribeInstanceTypeOfferingsInput{
					LocationType: ec2types.LocationTypeAvailabilityZone, NextToken: token,
					Filters: []ec2types.Filter{{Name: aws.String("instance-type"), Values: []string{"m5.large"}}},
				})
				if err != nil {
					return nil, nil, err
				}
				var got []string
				for _, o := range out.InstanceTypeOfferings {
					got = append(got, string(o.InstanceType)+" "+aws.ToString(o.Location))
				}
				return got, out.NextToken, nil
			}),

		pagedCase(fleettest.EC2DescribeInstanceTypes, []string{"m5.large 2", "t3.medium 2"},
			func(ctx context.Context, c *clients, token *string) ([]string, *string, error) {
				out, err := c.ec2.DescribeInstanceTypes(ctx, &ec2.DescribeInstanceTypesInput{
					InstanceTypes: []ec2types.InstanceType{"m5.large", "t3.medium"}, NextToken: token,
				})
				if err != nil {
					return nil, nil, err
				}
				var got []string
				for _, t := range out.InstanceTypes {
					got = append(got, string(t.InstanceType)+" "+fmt.Sprint(aws.ToInt32(t.VCpuInfo.DefaultVCpus)))
				}
				return got, out.NextToken, nil
			}),

		pagedCase(fleettest.EC2DescribeVpcs, []string{"vpc-1 10.0.0.0/16", "vpc-2 10.1.0.0/16"},
			func(ctx context.Context, c *clients, token *string) ([]string, *string, error) {
				out, err := c.ec2.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{VpcIds: []string{"vpc-1", "vpc-2"}, NextToken: token})
				if err != nil {
					return nil, nil, err
				}
				var got []string
				for _, v := range out.Vpcs {
					got = append(got, aws.ToString(v.VpcId)+" "+aws.ToString(v.CidrBlock))
				}
				return got, out.NextToken, nil
			}),
	}
}

func autoScalingCases() []opCase {
	worldStart, created := seedTimes()
	return []opCase{
		pagedCase(fleettest.AutoScalingDescribeAutoScalingGroups, []string{"eks-ng-a: i-1 i-2 in us-east-1a us-east-1b", "eks-ng-b: in"},
			func(ctx context.Context, c *clients, token *string) ([]string, *string, error) {
				out, err := c.asg.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{
					AutoScalingGroupNames: []string{"eks-ng-a", "eks-ng-b", "eks-gone"}, NextToken: token,
				})
				if err != nil {
					return nil, nil, err
				}
				var got []string
				for _, g := range out.AutoScalingGroups {
					line := aws.ToString(g.AutoScalingGroupName) + ":"
					for _, i := range g.Instances {
						line += " " + aws.ToString(i.InstanceId)
					}
					line += " in"
					for _, az := range g.AvailabilityZones {
						line += " " + az
					}
					got = append(got, line)
				}
				return got, out.NextToken, nil
			}),

		pagedCase(fleettest.AutoScalingDescribeScalingActivities, []activityOut{
			{ID: "act-2", Group: "eks-ng-a", Status: "Failed", Message: "vCPU limit <16> reached", Description: "Launching a new EC2 instance", Start: worldStart},
			{ID: "act-1", Group: "eks-ng-a", Status: "Successful", Description: "Launching a new EC2 instance", Start: created},
		}, func(ctx context.Context, c *clients, token *string) ([]activityOut, *string, error) {
			out, err := c.asg.DescribeScalingActivities(ctx, &autoscaling.DescribeScalingActivitiesInput{
				AutoScalingGroupName: aws.String("eks-ng-a"), NextToken: token,
			})
			if err != nil {
				return nil, nil, err
			}
			var got []activityOut
			for _, a := range out.Activities {
				got = append(got, activityOut{
					ID: aws.ToString(a.ActivityId), Group: aws.ToString(a.AutoScalingGroupName), Status: string(a.StatusCode),
					Message: aws.ToString(a.StatusMessage), Description: aws.ToString(a.Description), Start: aws.ToTime(a.StartTime).UTC(),
				})
			}
			return got, out.NextToken, nil
		}),
	}
}

func metricCases() []opCase {
	worldStart, _ := seedTimes()
	return []opCase{
		inEmptyRegion([]metricOut{{ID: "cpu0", Values: []float64{}}, {ID: "cpu1", Values: []float64{}}, {ID: "cpu2", Values: []float64{}}}, pagedCase(fleettest.CloudWatchGetMetricData, []metricOut{
			{ID: "cpu0", Values: []float64{30, 10}, Stamps: 2},
			{ID: "cpu1", Values: []float64{50}, Stamps: 1},
			{ID: "cpu2", Values: []float64{}},
		}, func(ctx context.Context, c *clients, token *string) ([]metricOut, *string, error) {
			query := func(id, instance string) cwtypes.MetricDataQuery {
				return cwtypes.MetricDataQuery{Id: aws.String(id), MetricStat: &cwtypes.MetricStat{
					Metric: &cwtypes.Metric{
						Namespace: aws.String("AWS/EC2"), MetricName: aws.String("CPUUtilization"),
						Dimensions: []cwtypes.Dimension{{Name: aws.String("InstanceId"), Value: aws.String(instance)}},
					},
					Period: aws.Int32(300), Stat: aws.String("Average"),
				}}
			}
			out, err := c.cw.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
				StartTime: aws.Time(worldStart.Add(-time.Hour)), EndTime: aws.Time(worldStart), NextToken: token,
				MetricDataQueries: []cwtypes.MetricDataQuery{query("cpu0", "i-1"), query("cpu1", "i-2"), query("cpu2", "i-none")},
			})
			if err != nil {
				return nil, nil, err
			}
			var got []metricOut
			for _, r := range out.MetricDataResults {
				got = append(got, metricOut{ID: aws.ToString(r.Id), Values: r.Values, Stamps: len(r.Timestamps)})
			}
			return got, out.NextToken, nil
		})),

		newCase(fleettest.ServiceQuotasGetServiceQuota, []float64{64},
			func(ctx context.Context, c *clients, _ *string) ([]float64, *string, error) {
				out, err := c.sq.GetServiceQuota(ctx, &servicequotas.GetServiceQuotaInput{ServiceCode: aws.String("ec2"), QuotaCode: aws.String("L-1216C47A")})
				if err != nil {
					return nil, nil, err
				}
				return []float64{aws.ToFloat64(out.Quota.Value)}, nil, nil
			}),
	}
}
