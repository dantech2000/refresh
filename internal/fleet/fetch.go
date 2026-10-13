package fleet

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/fanout"
)

// Need says what Fetch reads beyond the cluster itself. The zero value
// reads the cluster alone. Later steps of the read side add fields for the
// nodegroups, the AMIs, and the support window.
type Need struct {
	// Nodegroups reads the nodegroups: the list, and every nodegroup of
	// it.
	Nodegroups bool
	// AMI reads the AMI that each nodegroup runs and the latest
	// recommended one. It implies Nodegroups.
	AMI bool
	// Instances reads the EC2 instances of each nodegroup. It implies
	// Nodegroups.
	Instances bool
	// OnlyNodegroups names the nodegroups to read. Fetch describes these
	// by name and makes no list call. It implies Nodegroups.
	OnlyNodegroups []string
	// Addons reads the add-ons: the list, and every add-on of it.
	Addons bool
	// OnlyAddons names the add-ons to read. Fetch describes these by name
	// and makes no list call. It implies Addons.
	OnlyAddons []string
}

// Fetcher reads clusters. Use one Fetcher for one run of a command. The
// zero value is ready.
type Fetcher struct {
	// Now returns the time that a snapshot records. Nil means time.Now.
	Now func() time.Time

	// latestAMIs shares the latest recommended AMI between the nodegroups
	// and the clusters of the run. A lookup that failed is not kept.
	latestAMIs fanout.Memo[amiKey, string]
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
// the region, and the failure, which is the row that a sweep prints. Any
// other read that fails is a failure in the snapshot and a state on its
// group, and no error.
//
// The calls that need nothing from one another start together: the cluster
// and each list.
func (f *Fetcher) Fetch(ctx context.Context, c *awsx.Clients, cluster string, need Need) (*Snapshot, error) {
	snap := &Snapshot{Cluster: Cluster{Name: cluster, Region: c.Region}}
	if err := ctx.Err(); err != nil {
		// The SDK returns the bare context error for a call that it did
		// not send. Nothing was attempted, and the row says so.
		err = fmt.Errorf("%w: %w", awsx.ErrNotSent, err)
		snap.Failures = []apidoc.Failure{inRegion(c, FailureFromError(apidoc.FailureCluster, cluster, err))}
		snap.Have.Cluster = NotAttempted
		return snap, err
	}
	var (
		wg         sync.WaitGroup
		addons     addonsResult
		nodegroups nodegroupsResult
	)
	if need.Nodegroups || need.AMI || need.Instances || len(need.OnlyNodegroups) > 0 {
		wg.Go(func() { nodegroups = f.fetchNodegroups(ctx, c, cluster, need) })
	}
	if need.Addons || len(need.OnlyAddons) > 0 {
		wg.Go(func() { addons = fetchAddons(ctx, c, cluster, need.OnlyAddons) })
	}
	out, err := c.EKS.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String(cluster)})
	if err == nil && out.Cluster == nil {
		err = fmt.Errorf("describing cluster %s: %w", cluster, errEmptyAnswer)
	}
	wg.Wait()
	snap.FetchedAt = f.now()
	snap.Addons, snap.Have.Addons = addons.addons, addons.state
	snap.Nodegroups, snap.Have.Nodegroups = nodegroups.nodegroups, nodegroups.state
	rest := slices.Concat(nodegroups.failures, addons.failures)

	if err != nil {
		snap.Failures = append(snap.Failures, inRegion(c, FailureFromError(apidoc.FailureCluster, cluster, err)))
		snap.Have.Cluster = stateOf(err)
		// A cluster that does not exist fails every call the same way.
		// One failure says it.
		if !awsx.IsNotFound(err) {
			snap.Failures = append(snap.Failures, rest...)
		}
		return snap, err
	}
	fillCluster(&snap.Cluster, out.Cluster)
	snap.Have.Cluster = Collected
	snap.Failures = append(snap.Failures, rest...)
	return snap, nil
}

// stateOf returns the state of a group whose read failed with err.
func stateOf(err error) State {
	if errors.Is(err, awsx.ErrNotSent) {
		return NotAttempted
	}
	return Failed
}

// inRegion sets the region of a failure to the region of c.
func inRegion(c *awsx.Clients, f apidoc.Failure) apidoc.Failure {
	f.Region = c.Region
	return f
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
