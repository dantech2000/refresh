package fleet

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
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
	// InstanceIDs reads the IDs of the instances of every Auto Scaling
	// group of each nodegroup, for the health checks. It implies
	// Nodegroups, and costs no call when AMI is set too: the groups are
	// read for the AMI already.
	InstanceIDs bool
	// OnlyNodegroups names the nodegroups to read. Fetch describes these
	// by name and makes no list call. It implies Nodegroups.
	OnlyNodegroups []string
	// Addons reads the add-ons: the list, and every add-on of it.
	Addons bool
	// OnlyAddons names the add-ons to read. Fetch describes these by name
	// and makes no list call. It implies Addons.
	OnlyAddons []string
	// LatestAddons reads the newest compatible version of each add-on. It
	// implies Addons.
	LatestAddons bool
	// LatestFor names the add-ons whose newest compatible version is read,
	// when not every add-on needs it: a change reads the status of every
	// add-on and the newest version of its targets only. It implies
	// Addons. LatestAddons wins over it.
	LatestFor []string
	// AMIAge reads when the AMI of each nodegroup with an outdated AMI was
	// created. It implies AMI.
	AMIAge bool
	// Support reads the support window of the Kubernetes version.
	Support bool
	// Compute finds how the cluster runs its nodes: managed nodegroups,
	// EKS Auto Mode, or Karpenter. It implies Nodegroups.
	Compute bool
	// VPC reads the CIDR block of the VPC of the cluster.
	VPC bool
	// Updates reads the update history of the cluster itself, for the
	// rollback window. The read is advisory.
	Updates bool
}

// wide reports whether a cluster makes several calls at once for this
// need: anything but the cluster alone.
func (n Need) wide() bool {
	return n.Nodegroups || n.AMI || n.AMIAge || n.Instances || n.InstanceIDs || n.Addons || n.LatestAddons || n.Compute || n.Updates ||
		len(n.OnlyNodegroups) > 0 || len(n.OnlyAddons) > 0 || len(n.LatestFor) > 0
}

// The three reads below are advisory. One that fails loses an extra that
// the output marks as missing, so it sets the state of its group and adds
// no failure: a user with a narrow IAM policy still gets exit 0 for a
// fleet that was read.
//
//   - the support calendar: the dates then come from the calendar that
//     refresh ships with, and the output says so
//   - the Karpenter probe: the compute type is then not known
//   - the VPC: the CIDR block is then left out
//
// The fourth is the age of a stale AMI, in fetchAMIs.

// Fetcher reads clusters. Use one Fetcher for one run of a command. The
// zero value is ready.
type Fetcher struct {
	// Now returns the time that a snapshot records. Nil means time.Now.
	Now func() time.Time

	// latestAMIs shares the latest recommended AMI between the nodegroups
	// and the clusters of the run. A lookup that failed is not kept.
	latestAMIs fanout.Memo[amiKey, string]
	// latestAddons shares the newest version of an add-on for a
	// Kubernetes version. "" is an answer: EKS publishes none.
	latestAddons fanout.Memo[addonKey, string]

	// regionTickets bounds the goroutines of the item fan-outs of each
	// region. See tickets.
	mu            sync.Mutex
	regionTickets map[string]chan struct{}
	// calendars has the read of the support calendar of each region.
	calendars map[string]*calendarRead
}

// Fresh returns a fetcher with the clock of f and nothing remembered. A
// change that reads the cluster again after a person answered a question
// uses it: the newest version of an add-on may have changed since.
func (f *Fetcher) Fresh() *Fetcher {
	return &Fetcher{Now: f.Now}
}

// Time returns the time of the fetcher's clock.
func (f *Fetcher) Time() time.Time { return f.now() }

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
	// version is closed when the cluster is described. The add-on branch
	// waits for it: the newest version of an add-on depends on the
	// Kubernetes version of the cluster.
	described := make(chan struct{})
	var (
		wg         sync.WaitGroup
		addons     addonsResult
		nodegroups nodegroupsResult
		calendar   *calendarRead
	)
	if need.Nodegroups || need.AMI || need.AMIAge || need.Instances || need.InstanceIDs || need.Compute || len(need.OnlyNodegroups) > 0 {
		wg.Go(func() { nodegroups = f.fetchNodegroups(ctx, c, cluster, need) })
	}
	if need.Addons || need.LatestAddons || len(need.OnlyAddons) > 0 || len(need.LatestFor) > 0 {
		wg.Go(func() {
			addons = f.fetchAddons(ctx, c, cluster, need, func() Version {
				<-described
				return snap.Cluster.Version
			})
		})
	}
	if need.Support {
		calendar = f.startCalendar(ctx, c)
	}
	if need.Updates {
		wg.Go(func() { snap.Updates, snap.Have.Updates, snap.UpdatesErr = f.fetchUpdates(ctx, c, cluster) })
	}
	out, err := c.EKS.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String(cluster)})
	if err == nil && out.Cluster == nil {
		err = fmt.Errorf("describing cluster %s: %w", cluster, errEmptyAnswer)
	}
	if err == nil {
		fillCluster(&snap.Cluster, out.Cluster)
		snap.Have.Cluster = Collected
	}
	close(described)
	if err == nil && need.VPC {
		snap.Cluster.Network.VPCCIDR, snap.Have.VPC = vpcCIDR(ctx, c, snap.Cluster.Network.VPCID)
	}
	wg.Wait()
	// The calendar is waited for on every path, so that Fetch leaves no
	// read behind it.
	var (
		live        map[Version]window
		calendarErr error
	)
	if calendar != nil {
		live, calendarErr = calendar.wait()
	}
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
	snap.Failures = append(snap.Failures, rest...)
	if need.Support {
		f.setSupport(c, snap, live, calendarErr)
	}
	if need.Compute {
		snap.Cluster.Compute, snap.Have.Compute = compute(ctx, c, snap)
	}
	return snap, nil
}

// setSupport fills the support of the cluster. When EKS did not answer for
// the calendar, the dates are the ones that refresh ships with, and that is
// not a failure. A version that neither calendar has is one: the command
// cannot show the support of the cluster.
func (f *Fetcher) setSupport(c *awsx.Clients, snap *Snapshot, live map[Version]window, calendarErr error) {
	support, ok := supportFor(snap.Cluster.Version, live, snap.Cluster.UpgradePolicy, snap.FetchedAt)
	snap.Cluster.Support, snap.Have.Support = support, Collected
	if ok {
		return
	}
	snap.Have.Support = Failed
	failure := apidoc.NewFailure(apidoc.FailureCluster, snap.Cluster.Name, apidoc.ReasonUnknown,
		"no support dates for Kubernetes "+snap.Cluster.Version.String()+": EKS did not report them, and the built-in calendar ends before this version")
	if calendarErr != nil {
		failure = FailureFromError(apidoc.FailureCluster, snap.Cluster.Name, calendarErr)
		snap.Have.Support = stateOf(calendarErr)
	}
	snap.Failures = append(snap.Failures, inRegion(c, failure))
}

// vpcCIDR returns the CIDR block of a VPC. The read is advisory.
func vpcCIDR(ctx context.Context, c *awsx.Clients, id string) (string, State) {
	if id == "" {
		return "", Collected
	}
	out, err := c.EC2.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{
		VpcIds: []string{id},
	})
	if err != nil {
		slog.DebugContext(ctx, "could not read the VPC", "vpc", id, "operation", awsx.Operation(err), "class", awsx.Classify(err))
		return "", stateOf(err)
	}
	for _, vpc := range out.Vpcs {
		return aws.ToString(vpc.CidrBlock), Collected
	}
	return "", Collected
}

// The EC2 tags that Karpenter sets on the instances that it starts, now and
// in its older releases.
var karpenterTagKeys = []string{"karpenter.sh/nodepool", "karpenter.sh/provisioner-name"}

// compute returns how the cluster runs its nodes. A cluster with no managed
// nodegroup and without Auto Mode is asked for Karpenter instances, so that
// it does not read as a cluster with nothing to keep up to date. The probe
// is advisory: when it fails, the compute type is not known.
func compute(ctx context.Context, c *awsx.Clients, snap *Snapshot) (apidoc.ComputeType, State) {
	switch {
	case snap.Cluster.AutoMode:
		return apidoc.ComputeAutoMode, Collected
	case snap.Have.Nodegroups != Collected && snap.Have.Nodegroups != Partial:
		return "", snap.Have.Nodegroups
	case len(snap.Nodegroups) > 0:
		return apidoc.ComputeManagedNodegroups, Collected
	}
	// Karpenter tags an instance of a cluster in one of two ways. EC2
	// combines the filters of one call with "and", so each way is a call
	// of its own, and the two run together.
	owners := []ec2types.Filter{
		{Name: aws.String("tag:kubernetes.io/cluster/" + snap.Cluster.Name), Values: []string{"owned"}},
		{Name: aws.String("tag:eks:eks-cluster-name"), Values: []string{snap.Cluster.Name}},
	}
	type answer struct {
		ran   bool
		found bool
		err   error
	}
	answers := fanout.ForEach(ctx, owners, len(owners), func(ctx context.Context, owner ec2types.Filter) answer {
		out, err := c.EC2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
			MaxResults: aws.Int32(5),
			Filters: []ec2types.Filter{
				owner,
				{Name: aws.String("tag-key"), Values: karpenterTagKeys},
				{Name: aws.String("instance-state-name"), Values: []string{"pending", "running"}},
			},
		})
		if err != nil {
			return answer{ran: true, err: err}
		}
		for _, r := range out.Reservations {
			if len(r.Instances) > 0 {
				return answer{ran: true, found: true}
			}
		}
		return answer{ran: true}
	})
	var firstErr error
	for _, a := range answers {
		if a.found {
			return apidoc.ComputeKarpenter, Collected
		}
		firstErr = cmp.Or(firstErr, chunkError(ctx, a.ran, a.err))
	}
	if firstErr != nil {
		slog.DebugContext(ctx, "could not look for Karpenter instances", "cluster", snap.Cluster.Name, "operation", awsx.Operation(firstErr), "class", awsx.Classify(firstErr))
		return "", stateOf(firstErr)
	}
	return apidoc.ComputeNone, Collected
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
