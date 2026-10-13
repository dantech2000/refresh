package fleet

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	asgtypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/fanout"
	"github.com/dantech2000/refresh/internal/fleet/fleetdata"
)

// The most items of one batched call. A DescribeAutoScalingGroups call
// names at most 50 groups. A filter of an EC2 call carries at most 100
// values here; EC2 accepts 200 for one filter.
const (
	groupsPerCall       = 50
	filterValuesPerCall = 100
)

// nodegroupsResult is the nodegroup group of a snapshot.
type nodegroupsResult struct {
	nodegroups []Nodegroup
	state      State
	failures   []apidoc.Failure
}

// fetchNodegroups reads the nodegroups of a cluster: the ones in
// need.OnlyNodegroups, or every nodegroup that EKS lists. The describe
// calls run together. With need.AMI or need.Instances, the calls for the
// AMIs and the instances follow, batched for the cluster.
func (f *Fetcher) fetchNodegroups(ctx context.Context, c *awsx.Clients, cluster string, need Need) nodegroupsResult {
	names := need.OnlyNodegroups
	if len(names) == 0 {
		var err error
		if names, err = nodegroupNames(ctx, c, cluster); err != nil {
			// The list is a fact about the cluster, so the failure names
			// the cluster.
			return nodegroupsResult{
				state:    stateOf(err),
				failures: []apidoc.Failure{inRegion(c, FailureFromError(apidoc.FailureCluster, cluster, err))},
			}
		}
	}

	type described struct {
		ran       bool
		nodegroup Nodegroup
		err       error
	}
	results := each(ctx, f.tickets(c), names, func(ctx context.Context, name string) described {
		out, err := c.EKS.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{ClusterName: aws.String(cluster), NodegroupName: aws.String(name)})
		if err == nil && out.Nodegroup == nil {
			err = fmt.Errorf("describing nodegroup %s: %w", name, errEmptyAnswer)
		}
		if err != nil {
			return described{ran: true, nodegroup: Nodegroup{Name: name, State: stateOf(err)}, err: err}
		}
		return described{ran: true, nodegroup: newNodegroup(name, out.Nodegroup)}
	})

	res := nodegroupsResult{nodegroups: make([]Nodegroup, len(names)), state: Collected}
	fail := func(failure apidoc.Failure) {
		failure.Cluster = cluster
		res.failures = append(res.failures, inRegion(c, failure))
		res.state = Partial
	}
	for i, r := range results {
		switch {
		case !r.ran:
			// The context ended before this nodegroup started.
			r.nodegroup = Nodegroup{Name: names[i], State: NotAttempted}
			fail(notAttempted(ctx, apidoc.FailureNodegroup, names[i]))
		case r.err != nil:
			fail(FailureFromError(apidoc.FailureNodegroup, names[i], r.err))
		}
		res.nodegroups[i] = r.nodegroup
	}
	slices.SortFunc(res.nodegroups, func(a, b Nodegroup) int { return cmp.Compare(a.Name, b.Name) })

	if need.AMI || need.AMIAge || need.Instances || need.InstanceIDs {
		need.AMI = need.AMI || need.AMIAge
		for _, failure := range f.fetchAMIs(ctx, c, res.nodegroups, need) {
			fail(failure)
		}
	}
	if need.AMIAge {
		imageAges(ctx, c, res.nodegroups)
	}
	return res
}

// NodegroupNames returns the names of the nodegroups of a cluster, in the
// order that EKS lists them.
func (f *Fetcher) NodegroupNames(ctx context.Context, c *awsx.Clients, cluster string) ([]string, error) {
	return nodegroupNames(ctx, c, cluster)
}

func nodegroupNames(ctx context.Context, c *awsx.Clients, cluster string) ([]string, error) {
	return awsx.ListAll(ctx,
		func(ctx context.Context, token *string) (*eks.ListNodegroupsOutput, error) {
			return c.EKS.ListNodegroups(ctx, &eks.ListNodegroupsInput{ClusterName: aws.String(cluster), NextToken: token})
		},
		func(out *eks.ListNodegroupsOutput) ([]string, *string) { return out.Nodegroups, out.NextToken },
	)
}

// newNodegroup returns the nodegroup that DescribeNodegroup returned.
func newNodegroup(name string, in *ekstypes.Nodegroup) Nodegroup {
	n := Nodegroup{
		Name:           name,
		State:          Collected,
		Status:         string(in.Status),
		AMIType:        string(in.AmiType),
		ReleaseVersion: aws.ToString(in.ReleaseVersion),
		CapacityType:   string(in.CapacityType),
		InstanceTypes:  in.InstanceTypes,
		CreatedAt:      aws.ToTime(in.CreatedAt),
		ModifiedAt:     aws.ToTime(in.ModifiedAt),
	}
	// A version that does not parse stays the zero value: not known.
	n.Version, _ = ParseVersion(aws.ToString(in.Version))
	if sc := in.ScalingConfig; sc != nil {
		n.Desired, n.Min, n.Max = aws.ToInt32(sc.DesiredSize), aws.ToInt32(sc.MinSize), aws.ToInt32(sc.MaxSize)
	}
	if in.Resources != nil {
		for _, g := range in.Resources.AutoScalingGroups {
			if name := aws.ToString(g.Name); name != "" {
				n.AutoScalingGroups = append(n.AutoScalingGroups, name)
			}
		}
	}
	if lt := in.LaunchTemplate; lt != nil {
		n.LaunchTemplate = LaunchTemplate{ID: aws.ToString(lt.Id), Version: aws.ToString(lt.Version)}
	}
	if in.Health != nil {
		for _, issue := range in.Health.Issues {
			n.HealthIssues = append(n.HealthIssues, Issue{Code: string(issue.Code), Message: aws.ToString(issue.Message), ResourceIDs: issue.ResourceIds})
		}
	}
	return n
}

// amiKey is the key of the memo for the latest AMI. The version is the
// nodegroup's own: an AMI update keeps a nodegroup on its minor version, so
// the AMI for the version of the cluster may be one it cannot reach.
type amiKey struct {
	region  string
	version Version
	amiType string
}

// lookup is what one nodegroup learned in the calls after its describe.
type lookup struct {
	latest    string // the latest recommended AMI
	latestErr error
	template  string // the image of the launch template version
	templErr  error
}

// fetchAMIs fills the AMI group and the instances of the nodegroups that
// were described, and returns the failures. The calls are, in two steps:
//
//  1. For each nodegroup the latest AMI from SSM, shared between the
//     nodegroups and the clusters of the run, and the image of its launch
//     template version. For the cluster, the instances of every Auto
//     Scaling group, in one call for each 50 groups.
//  2. For the cluster, the instances that are needed, in one filtered call
//     for each 100 instances: one of each group whose launch template
//     names no image, or all of them for need.Instances.
//
// The current AMI is the image of the launch template when it names one,
// and the image of an instance of the first Auto Scaling group otherwise.
// One source that answers is enough.
func (f *Fetcher) fetchAMIs(ctx context.Context, c *awsx.Clients, nodegroups []Nodegroup, need Need) []apidoc.Failure {
	lookups := make([]lookup, len(nodegroups))
	var (
		wg        sync.WaitGroup
		groups    map[string][]string // the instance IDs of each group
		groupsErr error
	)
	// The lookups of the nodegroups: one task each, started as the region
	// has tickets.
	type task struct {
		index    int
		template bool // the image of the launch template, not the latest AMI
		path     string
	}
	var tasks []task
	var groupNames []string
	for i := range nodegroups {
		n := &nodegroups[i]
		if n.State != Collected {
			continue
		}
		switch {
		case need.InstanceIDs:
			groupNames = append(groupNames, n.AutoScalingGroups...)
		case len(n.AutoScalingGroups) > 0:
			groupNames = append(groupNames, n.AutoScalingGroups[0])
		}
		if !need.AMI {
			continue
		}
		if path, ok := fleetdata.AMIParameter(n.Version.String(), n.AMIType); ok {
			tasks = append(tasks, task{index: i, path: path})
		}
		if n.LaunchTemplate.ID != "" && n.LaunchTemplate.Version != "" {
			tasks = append(tasks, task{index: i, template: true})
		}
	}
	if len(groupNames) > 0 {
		wg.Go(func() { groups, groupsErr = groupInstances(ctx, c, groupNames) })
	}
	ran := each(ctx, f.tickets(c), tasks, func(ctx context.Context, t task) bool {
		// Each task writes its own fields of its own nodegroup.
		n, l := &nodegroups[t.index], &lookups[t.index]
		if t.template {
			l.template, l.templErr = templateImage(ctx, c, n.LaunchTemplate)
		} else {
			l.latest, l.latestErr = f.latestAMI(ctx, c, n, t.path)
		}
		return true
	})
	wg.Wait()
	for i, t := range tasks {
		// A task that never started, because the context ended, did not
		// find "no AMI": it was not sent.
		if err := chunkError(ctx, ran[i], nil); err != nil {
			if t.template {
				lookups[t.index].templErr = err
			} else {
				lookups[t.index].latestErr = err
			}
		}
	}

	instances, instancesErr := instancesByID(ctx, c, instancesToRead(nodegroups, lookups, groups, need))

	var failures []apidoc.Failure
	for i := range nodegroups {
		n := &nodegroups[i]
		if n.State != Collected {
			continue
		}
		var members []string
		if len(n.AutoScalingGroups) > 0 {
			members = groups[n.AutoScalingGroups[0]]
		}
		// The read of the group and of its instances, as one error. A
		// group with no instance had nothing to read in the second call.
		var fromGroupErr error
		switch {
		case len(n.AutoScalingGroups) == 0:
		case groupsErr != nil:
			fromGroupErr = groupsErr
		case len(members) > 0 && (need.Instances || need.AMI):
			fromGroupErr = instancesErr
		}
		failures = append(failures, n.fill(lookups[i], members, instances, groups, fromGroupErr, need)...)
	}
	return failures
}

// instancesToRead returns the IDs of the instances that the second step
// reads: all of a group for the instance list, and one for the AMI of a
// nodegroup whose template names no image.
func instancesToRead(nodegroups []Nodegroup, lookups []lookup, groups map[string][]string, need Need) []string {
	var ids []string
	for i, n := range nodegroups {
		if n.State != Collected || len(n.AutoScalingGroups) == 0 {
			continue
		}
		members := groups[n.AutoScalingGroups[0]]
		switch {
		case need.Instances:
			ids = append(ids, members...)
		case need.AMI && lookups[i].template == "" && len(members) > 0:
			ids = append(ids, members[0])
		}
	}
	return ids
}

// fill sets the instances and the AMI group of n from what the calls
// returned, and returns its failures: at most one, also when the instance
// list and the AMI failed for the same reason.
func (n *Nodegroup) fill(l lookup, members []string, instances map[string]Instance, groups map[string][]string, fromGroupErr error, need Need) []apidoc.Failure {
	var failures []apidoc.Failure
	if need.InstanceIDs {
		n.InstanceIDsState = Collected
		if fromGroupErr != nil {
			n.InstanceIDsState = stateOf(fromGroupErr)
			failures = append(failures, FailureFromError(apidoc.FailureNodegroup, n.Name, fromGroupErr))
		}
		n.InstanceIDs = make([]string, 0, len(members))
		for _, g := range n.AutoScalingGroups {
			n.InstanceIDs = append(n.InstanceIDs, groups[g]...)
		}
	}
	if need.Instances {
		n.InstancesState = Collected
		if fromGroupErr != nil {
			n.InstancesState = stateOf(fromGroupErr)
			failures = append(failures, FailureFromError(apidoc.FailureNodegroup, n.Name, fromGroupErr))
		}
		for _, id := range members {
			if inst, ok := instances[id]; ok {
				n.Instances = append(n.Instances, inst)
			}
		}
	}
	if need.AMI {
		if failure := n.setAMI(l, members, instances, fromGroupErr); failure != nil && len(failures) == 0 {
			failures = append(failures, *failure)
		}
	}
	return failures
}

// setAMI fills the AMI group of n from what the lookups returned, and
// returns the failure when a lookup that matters failed.
func (n *Nodegroup) setAMI(l lookup, members []string, instances map[string]Instance, fromGroupErr error) *apidoc.Failure {
	n.AMI = AMI{State: Collected, Latest: l.latest, Current: l.template}
	var currentErr error
	if n.AMI.Current == "" {
		for _, id := range members {
			if inst, ok := instances[id]; ok && inst.ImageID != "" {
				n.AMI.Current = inst.ImageID
				break
			}
		}
		if n.AMI.Current == "" {
			// The template is asked first, so its error is the one to
			// report when both sources failed.
			currentErr = cmp.Or(l.templErr, fromGroupErr)
		}
	}
	// The status of a nodegroup that is updating, or that has a custom
	// AMI, does not depend on the AMIs. A lookup that failed changes
	// nothing for it.
	if n.Status == string(ekstypes.NodegroupStatusUpdating) || n.AMIType == string(ekstypes.AMITypesCustom) {
		return nil
	}
	err := cmp.Or(l.latestErr, currentErr)
	if err == nil {
		return nil
	}
	n.AMI.State = stateOf(err)
	failure := FailureFromError(apidoc.FailureNodegroup, n.Name, err)
	return &failure
}

// latestAMI returns the latest recommended AMI for the nodegroup, from the
// SSM parameter at path. Every nodegroup of the run with the same region,
// version, and AMI type shares one call.
func (f *Fetcher) latestAMI(ctx context.Context, c *awsx.Clients, n *Nodegroup, path string) (string, error) {
	key := amiKey{region: c.Region, version: n.Version, amiType: n.AMIType}
	return f.latestAMIs.Get(ctx, key, func(ctx context.Context) (string, error) {
		out, err := c.SSM.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(path)})
		if err != nil {
			return "", err
		}
		if out.Parameter == nil || aws.ToString(out.Parameter.Value) == "" {
			return "", fmt.Errorf("reading the SSM parameter %s: %w", path, errEmptyAnswer)
		}
		return aws.ToString(out.Parameter.Value), nil
	})
}

// templateImage returns the image of a launch template version, or "" when
// the version names none: EKS then chooses the AMI.
func templateImage(ctx context.Context, c *awsx.Clients, lt LaunchTemplate) (string, error) {
	out, err := c.EC2.DescribeLaunchTemplateVersions(ctx, &ec2.DescribeLaunchTemplateVersionsInput{
		LaunchTemplateId: aws.String(lt.ID),
		Versions:         []string{lt.Version},
	})
	if err != nil {
		return "", err
	}
	for _, v := range out.LaunchTemplateVersions {
		if v.LaunchTemplateData != nil {
			return aws.ToString(v.LaunchTemplateData.ImageId), nil
		}
	}
	return "", nil
}

// groupInstances returns the instance IDs of each Auto Scaling group, in
// the order of the group. The chunks of names run together.
func groupInstances(ctx context.Context, c *awsx.Clients, names []string) (map[string][]string, error) {
	chunks := slices.Collect(slices.Chunk(names, groupsPerCall))
	type answer struct {
		ran    bool
		groups []asgtypes.AutoScalingGroup
		err    error
	}
	answers := fanout.ForEach(ctx, chunks, len(chunks), func(ctx context.Context, chunk []string) answer {
		groups, err := awsx.ListAll(ctx,
			func(ctx context.Context, token *string) (*autoscaling.DescribeAutoScalingGroupsOutput, error) {
				return c.AutoScaling.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{
					AutoScalingGroupNames: chunk, NextToken: token,
				})
			},
			func(out *autoscaling.DescribeAutoScalingGroupsOutput) ([]asgtypes.AutoScalingGroup, *string) {
				return out.AutoScalingGroups, out.NextToken
			},
		)
		return answer{ran: true, groups: groups, err: err}
	})
	out := map[string][]string{}
	var firstErr error
	for _, a := range answers {
		firstErr = cmp.Or(firstErr, chunkError(ctx, a.ran, a.err))
		for _, g := range a.groups {
			ids := make([]string, 0, len(g.Instances))
			for _, inst := range g.Instances {
				ids = append(ids, aws.ToString(inst.InstanceId))
			}
			out[aws.ToString(g.AutoScalingGroupName)] = ids
		}
	}
	return out, firstErr
}

// chunkError returns the error of one chunk of a batched call. A chunk that
// never ran, because the context ended first, was not sent.
func chunkError(ctx context.Context, ran bool, err error) error {
	if !ran {
		return fmt.Errorf("%w: %w", awsx.ErrNotSent, ctx.Err())
	}
	return err
}

// instancesByID returns the instances with the IDs. It selects them with
// the instance-id filter and not with the InstanceIds list: with the list,
// one ID of an instance that is gone fails the whole call, and during a
// roll instances go all the time. With the filter, an instance that is gone
// is not in the answer.
func instancesByID(ctx context.Context, c *awsx.Clients, ids []string) (map[string]Instance, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	chunks := slices.Collect(slices.Chunk(ids, filterValuesPerCall))
	type answer struct {
		ran          bool
		reservations []ec2types.Reservation
		err          error
	}
	answers := fanout.ForEach(ctx, chunks, len(chunks), func(ctx context.Context, chunk []string) answer {
		reservations, err := awsx.ListAll(ctx,
			func(ctx context.Context, token *string) (*ec2.DescribeInstancesOutput, error) {
				return c.EC2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
					Filters:   []ec2types.Filter{{Name: aws.String("instance-id"), Values: chunk}},
					NextToken: token,
				})
			},
			func(out *ec2.DescribeInstancesOutput) ([]ec2types.Reservation, *string) {
				return out.Reservations, out.NextToken
			},
		)
		return answer{ran: true, reservations: reservations, err: err}
	})
	out := map[string]Instance{}
	var firstErr error
	for _, a := range answers {
		firstErr = cmp.Or(firstErr, chunkError(ctx, a.ran, a.err))
		for _, r := range a.reservations {
			for _, in := range r.Instances {
				inst := Instance{
					ID:         aws.ToString(in.InstanceId),
					ImageID:    aws.ToString(in.ImageId),
					Type:       string(in.InstanceType),
					Lifecycle:  "on-demand",
					LaunchTime: aws.ToTime(in.LaunchTime),
				}
				if in.InstanceLifecycle != "" {
					inst.Lifecycle = string(in.InstanceLifecycle)
				}
				if in.State != nil {
					inst.State = string(in.State.Name)
				}
				if in.Placement != nil {
					inst.AvailabilityZone = aws.ToString(in.Placement.AvailabilityZone)
				}
				out[inst.ID] = inst
			}
		}
	}
	return out, firstErr
}

// imageAges fills when the AMI of each nodegroup with an outdated AMI was
// created, in one filtered call for each 100 AMIs. The image-id filter is
// used and not the ImageIds list: with the list, one AMI that was
// deregistered fails the whole call. The read is advisory: when it fails,
// the output shows the stale AMIs without their age.
func imageAges(ctx context.Context, c *awsx.Clients, nodegroups []Nodegroup) {
	var ids []string
	for _, n := range nodegroups {
		if status, _ := n.AMIStatus(); status == apidoc.AMIOutdated && !slices.Contains(ids, n.AMI.Current) {
			ids = append(ids, n.AMI.Current)
		}
	}
	if len(ids) == 0 {
		return
	}
	created := map[string]time.Time{}
	for chunk := range slices.Chunk(ids, filterValuesPerCall) {
		images, err := awsx.ListAll(ctx,
			func(ctx context.Context, token *string) (*ec2.DescribeImagesOutput, error) {
				return c.EC2.DescribeImages(ctx, &ec2.DescribeImagesInput{
					Filters:   []ec2types.Filter{{Name: aws.String("image-id"), Values: chunk}},
					NextToken: token,
				})
			},
			func(out *ec2.DescribeImagesOutput) ([]ec2types.Image, *string) { return out.Images, out.NextToken },
		)
		if err != nil {
			slog.DebugContext(ctx, "could not read the age of the stale AMIs", "operation", awsx.Operation(err), "class", awsx.Classify(err))
			return
		}
		for _, image := range images {
			if t, err := time.Parse(time.RFC3339, aws.ToString(image.CreationDate)); err == nil {
				created[aws.ToString(image.ImageId)] = t
			}
		}
	}
	for i := range nodegroups {
		if status, _ := nodegroups[i].AMIStatus(); status == apidoc.AMIOutdated {
			nodegroups[i].AMI.CurrentCreatedAt = created[nodegroups[i].AMI.Current]
		}
	}
}
