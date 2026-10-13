package fleet

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
)

// Need says what Fetch reads beyond the cluster itself. The zero value
// reads the cluster alone. Later phases of the read side add its fields:
// nodegroups, add-ons, AMIs, and the support window.
type Need struct{}

// Fetcher reads clusters. Use one Fetcher for one run of a command. The
// zero value is ready.
type Fetcher struct {
	// Now returns the time that a snapshot records. Nil means time.Now.
	Now func() time.Time
}

func (f *Fetcher) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

// Clusters returns the names of the clusters in the region of c, in the
// order that EKS lists them.
func (f *Fetcher) Clusters(ctx context.Context, c *awsx.Clients) ([]string, error) {
	return awsx.ListAll(ctx,
		func(ctx context.Context, token *string) (*eks.ListClustersOutput, error) {
			return c.EKS.ListClusters(ctx, &eks.ListClustersInput{NextToken: token})
		},
		func(out *eks.ListClustersOutput) ([]string, *string) { return out.Clusters, out.NextToken },
	)
}

// errEmptyAnswer is the error of a describe call that answered without the
// item.
var errEmptyAnswer = errors.New("empty answer")

// Fetch reads one cluster. The snapshot is never nil. The error is the
// error of DescribeCluster: when it is set, the snapshot holds the name,
// the region, and the failure, which is the row that a sweep prints.
func (f *Fetcher) Fetch(ctx context.Context, c *awsx.Clients, cluster string, _ Need) (*Snapshot, error) {
	snap := &Snapshot{Cluster: Cluster{Name: cluster, Region: c.Region}}
	out, err := c.EKS.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String(cluster)})
	if err == nil && out.Cluster == nil {
		err = fmt.Errorf("describing cluster %s: %w", cluster, errEmptyAnswer)
	}
	snap.FetchedAt = f.now()
	if err != nil {
		failure := FailureFromError(apidoc.FailureCluster, cluster, err)
		failure.Region = c.Region
		snap.Failures = append(snap.Failures, failure)
		snap.Have.Cluster = Failed
		if errors.Is(err, awsx.ErrNotSent) {
			snap.Have.Cluster = NotAttempted
		}
		return snap, err
	}
	fillCluster(&snap.Cluster, out.Cluster)
	snap.Have.Cluster = Collected
	return snap, nil
}

// fillCluster copies what DescribeCluster returned into c.
func fillCluster(c *Cluster, in *ekstypes.Cluster) {
	c.ARN = aws.ToString(in.Arn)
	c.Status = string(in.Status)
	// A version that does not parse stays the zero value: not known.
	c.Version, _ = ParseVersion(aws.ToString(in.Version))
	c.PlatformVersion = aws.ToString(in.PlatformVersion)
	c.Endpoint = aws.ToString(in.Endpoint)
	c.CreatedAt = aws.ToTime(in.CreatedAt)
	c.Tags = in.Tags
	if in.UpgradePolicy != nil {
		c.UpgradePolicy = string(in.UpgradePolicy.SupportType)
	}
	c.AutoMode = in.ComputeConfig != nil && aws.ToBool(in.ComputeConfig.Enabled)
	if in.Health != nil {
		for _, issue := range in.Health.Issues {
			c.HealthIssues = append(c.HealthIssues, Issue{
				Code:        string(issue.Code),
				Message:     aws.ToString(issue.Message),
				ResourceIDs: issue.ResourceIds,
			})
		}
	}
	if vpc := in.ResourcesVpcConfig; vpc != nil {
		c.Network = Network{
			VPCID:             aws.ToString(vpc.VpcId),
			SubnetIDs:         vpc.SubnetIds,
			SecurityGroupIDs:  vpc.SecurityGroupIds,
			PublicAccess:      vpc.EndpointPublicAccess,
			PrivateAccess:     vpc.EndpointPrivateAccess,
			PublicAccessCIDRs: vpc.PublicAccessCidrs,
		}
	}
	c.Security.RoleARN = aws.ToString(in.RoleArn)
	c.Security.DeletionProtection = aws.ToBool(in.DeletionProtection)
	for _, enc := range in.EncryptionConfig {
		if enc.Provider != nil && aws.ToString(enc.Provider.KeyArn) != "" {
			c.Security.KMSKeyARN = aws.ToString(enc.Provider.KeyArn)
			break
		}
	}
	if in.Logging != nil {
		for _, setup := range in.Logging.ClusterLogging {
			if !aws.ToBool(setup.Enabled) {
				continue
			}
			for _, t := range setup.Types {
				c.Security.LogTypes = append(c.Security.LogTypes, string(t))
			}
		}
	}
}
