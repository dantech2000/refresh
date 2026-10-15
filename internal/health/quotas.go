package health

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/servicequotas"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/fleet"
)

// The EC2 On-Demand Standard vCPU quota, which a roll surges against, and
// the usage thresholds.
const (
	quotaServiceCode = "ec2"
	quotaCode        = "L-1216C47A"
	quotaWarnPercent = 85.0
	quotaFailPercent = 95.0
	quotaUsageWindow = 10 * time.Minute
	// quotaStaleAfter is the age of the newest AWS/Usage data point after
	// which the check counts the instance list too: the metric lags, and
	// right after a roll it still counts the surge nodes that have ended.
	quotaStaleAfter = 2 * time.Minute
)

// Quotas checks the headroom of the EC2 On-Demand vCPU quota: the limit
// from Service Quotas, and the use of the newest minute from the AWS/Usage
// metric. When the metric has no data points, as on a new account, the use
// is the sum of the vCPUs of the instance list. When its newest data point
// is older than quotaStaleAfter, the check sums the instance list too, and
// the smaller of the two is the use: the metric lags, and never counts
// fewer vCPUs than the instances that run. It is advisory.
//
// With targets it also counts the nodes that their roll launches, by the
// rule of an EKS managed node update: the scale-up phase raises the Auto
// Scaling group by the larger of the number of its Availability Zones and
// maxUnavailable, and by up to twice the number of zones. Current use and
// the likely surge over the quota fail; current use and the documented
// maximum over it warn. A nodegroup with the MINIMAL update strategy
// launches no extra node. See rollLaunches for what counts.
//
// A likely surge over the quota blocks, and --force goes past it: the
// roll would stall on VcpuLimitExceeded. The rules of current use only
// ask.
func Quotas() Check {
	return Check{Name: "Quotas", Blocking: true, Forceable: true, Run: func(ctx context.Context, in *Input) Result {
		return runQuotas(ctx, in, rollLaunches(in))
	}}
}

// ScaleUpQuotas is the quota check of a scale of nodegroup that adds add
// nodes: the launch is known, and counts as both the likely and the most.
// A scale that adds no node has the rule of Quotas with no targets.
func ScaleUpQuotas(nodegroup string, add int32) Check {
	return Check{Name: "Quotas", Blocking: true, Forceable: true, Run: func(ctx context.Context, in *Input) Result {
		return runQuotas(ctx, in, scaleLaunches(in, nodegroup, add))
	}}
}

// launch is the nodes that the change of one nodegroup may launch at once.
type launch struct {
	nodegroup string
	// likely and most are node counts: the surge that EKS adds for a roll,
	// and the most that it documents. A scale-up has one number for both.
	likely, most int32
	// scale says that the change is a scale-up, not a roll.
	scale bool
	// types are the instance types of the nodegroup that the quota counts.
	// largest is the one with the most vCPUs, and vcpus its count: every
	// node counts as one of it. Both are set after the read.
	types   []string
	largest string
	vcpus   int32
}

// launches is what the quota check counts: the launches, and a line for
// every target that is not counted, and why.
type launches struct {
	list     []launch
	together bool
	notes    []string
}

// rollLaunches returns the nodes that the roll of each target may launch.
// The count of a nodegroup:
//
//   - zones are the Availability Zones of its Auto Scaling groups, or, when
//     they were not read, one per distinct subnet: each subnet is in one
//     zone.
//   - maxUnavailable is the count of its update config, or its percentage
//     of the desired size rounded up, and 1 when EKS returned neither, as
//     EKS sets it.
//   - likely is the larger of zones and maxUnavailable, and most the larger
//     of twice the zones and maxUnavailable.
//
// A nodegroup that was not read, that runs Spot capacity, that uses the
// MINIMAL update strategy, or whose instance types are not On-Demand
// Standard types is not counted, with a note.
func rollLaunches(in *Input) launches {
	out := launches{together: in.RollTogether}
	for _, name := range in.Targets {
		n, ok := targetNodegroup(in, name, &out)
		if !ok {
			continue
		}
		if n.UpdateStrategy == string(ekstypes.NodegroupUpdateStrategiesMinimal) {
			out.notes = append(out.notes, "nodegroup "+name+" uses the MINIMAL update strategy: its roll launches no extra node")
			continue
		}
		zones := int32(len(n.Zones)) //nolint:gosec // a count of Availability Zones
		if n.InstanceIDsState != fleet.Collected || zones == 0 {
			zones = int32(len(sortedSet(n.Subnets))) //nolint:gosec // a count of subnets
			note := fmt.Sprintf("the Availability Zones of nodegroup %s are not known: each of its subnets counts as one zone (%d)", name, zones)
			if zones == 0 {
				note = "the Availability Zones of nodegroup " + name + " are not known, nor its subnets: its maxUnavailable alone counts"
			}
			out.notes = append(out.notes, note)
		}
		unavailable := maxUnavailable(n)
		out.list = append(out.list, launch{
			nodegroup: name, types: standardTypes(n.InstanceTypes),
			likely: max(zones, unavailable), most: max(2*zones, unavailable),
		})
	}
	return out
}

// scaleLaunches returns the launch of a scale of nodegroup by add nodes.
func scaleLaunches(in *Input, nodegroup string, add int32) launches {
	var out launches
	if add <= 0 {
		return out
	}
	if n, ok := targetNodegroup(in, nodegroup, &out); ok {
		out.list = append(out.list, launch{nodegroup: nodegroup, likely: add, most: add, scale: true, types: standardTypes(n.InstanceTypes)})
	}
	return out
}

// targetNodegroup returns the nodegroup name of the snapshot when the
// quota counts its nodes, and adds a note to out when it does not.
func targetNodegroup(in *Input, name string, out *launches) (fleet.Nodegroup, bool) {
	i := slices.IndexFunc(in.Snapshot.Nodegroups, func(n fleet.Nodegroup) bool { return n.Name == name })
	var note string
	switch {
	case i < 0 || in.Snapshot.Nodegroups[i].State != fleet.Collected:
		note = "nodegroup " + name + " was not read: the nodes that it launches are not counted"
	case in.Snapshot.Nodegroups[i].CapacityType == string(ekstypes.CapacityTypesSpot):
		note = "nodegroup " + name + " runs Spot capacity, which the Spot quota counts"
	case len(in.Snapshot.Nodegroups[i].InstanceTypes) == 0:
		note = "nodegroup " + name + " names no instance type (its launch template sets it): the nodes that it launches are not counted"
	case len(standardTypes(in.Snapshot.Nodegroups[i].InstanceTypes)) == 0:
		note = "the instance types of nodegroup " + name + " (" + strings.Join(in.Snapshot.Nodegroups[i].InstanceTypes, ", ") +
			") count against another On-Demand quota than the Standard one"
	default:
		return in.Snapshot.Nodegroups[i], true
	}
	out.notes = append(out.notes, note)
	return fleet.Nodegroup{}, false
}

// maxUnavailable returns the nodes that a roll of n takes down at once.
func maxUnavailable(n fleet.Nodegroup) int32 {
	switch {
	case n.MaxUnavailable > 0:
		return n.MaxUnavailable
	case n.MaxUnavailablePercentage > 0:
		return max(1, (n.Desired*n.MaxUnavailablePercentage+99)/100)
	}
	return 1
}

// standardTypes returns the types that the On-Demand Standard quota
// counts: the A, C, D, H, I, M, R, T, and Z families. The others, such as
// the G and P GPU families, have quotas of their own.
func standardTypes(types []string) []string {
	var out []string
	for _, t := range types {
		// The family is the letters before the first digit: t3.medium is
		// t, trn1.2xlarge is trn, and is4gen.large is is.
		i := strings.IndexAny(t, "0123456789")
		if i > 0 && slices.Contains([]string{"a", "c", "d", "h", "i", "im", "is", "m", "r", "t", "z"}, t[:i]) {
			out = append(out, t)
		}
	}
	return out
}

func runQuotas(ctx context.Context, in *Input, ls launches) Result {
	var types []string
	for _, l := range ls.list {
		types = append(types, l.types...)
	}
	types = sortedSet(types)
	var (
		wg                        sync.WaitGroup
		limit, usage              float64
		haveLimit, used           bool
		newest                    time.Time
		vcpus                     map[string]int32
		limitErr, useErr, typeErr error
	)
	wg.Go(func() { limit, haveLimit, limitErr = quotaLimit(ctx, in) })
	wg.Go(func() { usage, newest, used, useErr = quotaUsage(ctx, in) })
	if len(types) > 0 {
		wg.Go(func() { vcpus, typeErr = typeVCPUs(ctx, in, types) })
	}
	wg.Wait()
	cluster := in.Snapshot.Cluster.Name
	for _, read := range []struct {
		what string
		err  error
	}{{"the vCPU quota", limitErr}, {"the vCPU quota", useErr}, {"the vCPUs of the instance types", typeErr}} {
		if read.err == nil {
			continue
		}
		f := fleet.FailureFromError(apidoc.FailureCluster, cluster, read.err)
		return Result{Outcome: Unreadable, Message: "could not read " + read.what + ": " + f.Error + failureAction(f), Failures: []apidoc.Failure{f}}
	}
	if !haveLimit || limit <= 0 {
		return Result{Outcome: Skipped, Message: "the EC2 vCPU quota is not reported"}
	}
	var source string
	if age := in.Now.Sub(newest).Round(time.Second); used && age > quotaStaleAfter {
		usage, vcpus, source = staleUsage(ctx, in, usage, age, vcpus)
	}
	if !used {
		var complete bool
		usage, complete, vcpus, useErr = instanceUsage(ctx, in, limit, vcpus)
		if useErr != nil {
			f := fleet.FailureFromError(apidoc.FailureCluster, cluster, useErr)
			return Result{Outcome: Skipped, Message: "the AWS/Usage vCPU metric is not reported, and the instance list could not be read: " + f.Error + failureAction(f)}
		}
		source = "the vCPUs in use come from the instance list, not from CloudWatch: the AWS/Usage metric has no data points, so the check sums the vCPUs of the running On-Demand instances of the Standard families"
		if !complete {
			source += fmt.Sprintf("; at least %.0f vCPUs are in use, and the list stopped once the sum passed the quota", usage)
		}
	}
	for i := range ls.list {
		l := &ls.list[i]
		for _, t := range l.types {
			if v := vcpus[t]; v > l.vcpus {
				l.largest, l.vcpus = t, v
			}
		}
		if l.vcpus == 0 {
			ls.notes = append(ls.notes, "EC2 reported no vCPU count for the instance types of nodegroup "+l.nodegroup+": the nodes that it launches are not counted")
		}
	}
	r := evaluateQuota(usage, limit, ls)
	if source != "" {
		r.Details = slices.Insert(r.Details, 1, source)
	}
	return r
}

// quotaLimit reads the quota. A quota that Service Quotas does not know
// is not an error: there is nothing to compare with.
func quotaLimit(ctx context.Context, in *Input) (float64, bool, error) {
	out, err := in.Clients.ServiceQuotas.GetServiceQuota(ctx, &servicequotas.GetServiceQuotaInput{
		ServiceCode: aws.String(quotaServiceCode), QuotaCode: aws.String(quotaCode),
	})
	if err != nil {
		if awsx.IsNotFound(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if out.Quota == nil || out.Quota.Value == nil {
		return 0, false, nil
	}
	return *out.Quota.Value, true, nil
}

// quotaUsage reads the vCPUs in use in the newest minute of the window,
// and the time of that data point. The maximum over the window counted a
// surge that had already ended.
func quotaUsage(ctx context.Context, in *Input) (float64, time.Time, bool, error) {
	end := in.Now
	results, err := metricData(ctx, in.Clients.CloudWatch, &cloudwatch.GetMetricDataInput{
		StartTime: aws.Time(end.Add(-quotaUsageWindow)), EndTime: aws.Time(end),
		ScanBy: cwtypes.ScanByTimestampDescending,
		MetricDataQueries: []cwtypes.MetricDataQuery{{
			Id: aws.String("vcpu"),
			MetricStat: &cwtypes.MetricStat{
				Metric: &cwtypes.Metric{
					Namespace:  aws.String("AWS/Usage"),
					MetricName: aws.String("ResourceCount"),
					Dimensions: []cwtypes.Dimension{
						{Name: aws.String("Service"), Value: aws.String("EC2")},
						{Name: aws.String("Type"), Value: aws.String("Resource")},
						{Name: aws.String("Resource"), Value: aws.String("vCPU")},
						{Name: aws.String("Class"), Value: aws.String("Standard/OnDemand")},
					},
				},
				Period: aws.Int32(60),
				Stat:   aws.String("Maximum"),
			},
		}},
	})
	if err != nil {
		return 0, time.Time{}, false, err
	}
	for _, r := range results {
		if aws.ToString(r.Id) == "vcpu" && len(r.Values) > 0 {
			var at time.Time
			if len(r.Timestamps) > 0 {
				at = r.Timestamps[0]
			}
			return r.Values[0], at, true, nil
		}
	}
	return 0, time.Time{}, false, nil
}

// staleUsage is the use when the newest data point of the AWS/Usage
// metric, metric vCPUs, is age old: the smaller of the metric and the sum
// of the instance list. The list stops once its sum passes the metric,
// which then decides. It returns the use, the vCPU counts of the types
// read, and a detail line that names the source that decided. When the
// list cannot be read, the metric decides.
func staleUsage(ctx context.Context, in *Input, metric float64, age time.Duration, vcpus map[string]int32) (float64, map[string]int32, string) {
	counted, complete, known, err := instanceUsage(ctx, in, metric, vcpus)
	if err != nil {
		f := fleet.FailureFromError(apidoc.FailureCluster, in.Snapshot.Cluster.Name, err)
		return metric, vcpus, fmt.Sprintf("the newest data point of the AWS/Usage metric is %s old, and the instance list could not be read: %s%s; the metric decides",
			age, f.Error, failureAction(f))
	}
	if counted < metric {
		return counted, known, fmt.Sprintf("the vCPUs in use come from the instance list (%.0f), not from the AWS/Usage metric (%.0f): "+
			"the newest data point of the metric is %s old, and may still count instances that have stopped", counted, metric, age)
	}
	listed := fmt.Sprintf("counts %.0f", counted)
	if !complete {
		listed = fmt.Sprintf("counts at least %.0f", counted)
	}
	return metric, known, fmt.Sprintf("the vCPUs in use come from the AWS/Usage metric (%.0f): its newest data point is %s old, and the instance list %s",
		metric, age, listed)
}

// instanceUsage sums the vCPUs of the pending and running instances that
// the quota counts, On-Demand of the Standard families. It is the use when
// the AWS/Usage metric has no data points, as for a while on a new account
// or in a region where the account has run few instances, and a second
// count when the metric is stale. vcpus holds the types already read, and
// the types of each page that it lacks are read with that page; the map
// with them is returned. The list stops once the sum passes limit, since
// the sum then no longer decides, and complete says whether it was read to
// the end.
func instanceUsage(ctx context.Context, in *Input, limit float64, vcpus map[string]int32) (usage float64, complete bool, known map[string]int32, err error) {
	known = maps.Clone(vcpus)
	if known == nil {
		known = map[string]int32{}
	}
	_, err = awsx.ListAll(ctx,
		func(ctx context.Context, token *string) (*ec2.DescribeInstancesOutput, error) {
			out, err := in.Clients.EC2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
				Filters:    []ec2types.Filter{{Name: aws.String("instance-state-name"), Values: []string{"pending", "running"}}},
				MaxResults: aws.Int32(1000),
				NextToken:  token,
			})
			if err != nil {
				return nil, err
			}
			var types []string
			for _, r := range out.Reservations {
				for _, i := range r.Instances {
					if i.InstanceLifecycle != ec2types.InstanceLifecycleTypeSpot {
						types = append(types, string(i.InstanceType))
					}
				}
			}
			types = standardTypes(types)
			missing := slices.DeleteFunc(sortedSet(types), func(t string) bool { _, ok := known[t]; return ok })
			if len(missing) > 0 {
				read, err := typeVCPUs(ctx, in, missing)
				if err != nil {
					return nil, err
				}
				maps.Copy(known, read)
			}
			for _, t := range types {
				usage += float64(known[t])
			}
			return out, nil
		},
		// The sum is the result, so no page is kept.
		func(out *ec2.DescribeInstancesOutput) ([]ec2types.Reservation, *string) {
			if usage > limit {
				return nil, nil
			}
			return nil, out.NextToken
		},
	)
	if err != nil {
		return 0, false, vcpus, err
	}
	return usage, usage <= limit, known, nil
}

// typeVCPUs reads the default vCPU count of each of types, in paged
// DescribeInstanceTypes calls of up to 100 types, the most that one call
// takes.
func typeVCPUs(ctx context.Context, in *Input, types []string) (map[string]int32, error) {
	out := map[string]int32{}
	for batch := range slices.Chunk(types, 100) {
		names := make([]ec2types.InstanceType, len(batch))
		for i, t := range batch {
			names[i] = ec2types.InstanceType(t)
		}
		infos, err := awsx.ListAll(ctx,
			func(ctx context.Context, token *string) (*ec2.DescribeInstanceTypesOutput, error) {
				return in.Clients.EC2.DescribeInstanceTypes(ctx, &ec2.DescribeInstanceTypesInput{InstanceTypes: names, NextToken: token})
			},
			func(out *ec2.DescribeInstanceTypesOutput) ([]ec2types.InstanceTypeInfo, *string) {
				return out.InstanceTypes, out.NextToken
			},
		)
		if err != nil {
			return nil, err
		}
		for _, info := range infos {
			if info.VCpuInfo != nil {
				out[string(info.InstanceType)] = aws.ToInt32(info.VCpuInfo.DefaultVCpus)
			}
		}
	}
	return out, nil
}

// evaluateQuota is the rule, apart from the reads: the thresholds of
// current use, and then the launches. The worse of the two decides, and
// the launch names the fix of a tie. A Fail of current use alone is
// advisory; a Fail of a launch blocks.
func evaluateQuota(usage, limit float64, ls launches) Result {
	pct := usage / limit * 100
	r := Result{Details: []string{fmt.Sprintf("EC2 On-Demand Standard vCPUs: %.0f of %.0f in use (%.1f%%, %.0f free)", usage, limit, pct, limit-usage)}}
	switch {
	case pct >= quotaFailPercent:
		r.Outcome, r.Message = Fail, fmt.Sprintf("EC2 vCPU quota nearly exhausted (%.0f%% in use): a roll may fail to launch nodes", pct)
	case pct >= quotaWarnPercent:
		r.Outcome, r.Message = Warn, fmt.Sprintf("%.0f%% of the vCPU quota in use", pct)
	default:
		r.Outcome, r.Message = Pass, fmt.Sprintf("%.0f%% of the vCPU quota in use (%.0f of %.0f)", pct, usage, limit)
	}
	for _, l := range ls.list {
		if l.vcpus > 0 {
			r.Details = append(r.Details, launchDetail(l))
		}
	}
	r.Details = append(r.Details, ls.notes...)
	outcome, message := evaluateLaunches(usage, limit, ls)
	if outcome != Pass && severity(outcome) >= severity(r.Outcome) {
		r.Outcome, r.Message = outcome, message
	}
	r.advisory = r.Outcome == Fail && outcome != Fail
	return r
}

// evaluateLaunches returns Fail when current use and the likely launch
// exceed the quota, Warn when current use and the most exceed it, and Pass
// otherwise. The launch is the sum of the targets when they roll
// together, and the largest one when they roll one after another.
func evaluateLaunches(usage, limit float64, ls launches) (Outcome, string) {
	var counted []launch
	for _, l := range ls.list {
		if l.vcpus > 0 && l.most > 0 {
			counted = append(counted, l)
		}
	}
	if len(counted) == 0 {
		return Pass, ""
	}
	vcpus := func(l launch, nodes int32) float64 { return float64(nodes * l.vcpus) }
	if ls.together && len(counted) > 1 {
		var likely, most float64
		var nodesLikely, nodesMost int32
		var names []string
		for _, l := range counted {
			likely, most = likely+vcpus(l, l.likely), most+vcpus(l, l.most)
			nodesLikely, nodesMost = nodesLikely+l.likely, nodesMost+l.most
			names = append(names, l.nodegroup)
		}
		outcome := launchOutcome(usage, limit, likely, most)
		slices.Sort(names)
		what := fmt.Sprintf("rolling %s at once may launch %s nodes (%s vCPU)", joinNames(names), span(nodesLikely, nodesMost), spanF(likely, most))
		return outcome, launchMessage(what, usage, limit, false)
	}
	worst, worstOutcome := counted[0], Pass
	for _, l := range counted {
		o := launchOutcome(usage, limit, vcpus(l, l.likely), vcpus(l, l.most))
		if severity(o) > severity(worstOutcome) || severity(o) == severity(worstOutcome) && vcpus(l, l.most) > vcpus(worst, worst.most) {
			worst, worstOutcome = l, o
		}
	}
	if worstOutcome == Pass {
		return Pass, ""
	}
	verb := "rolling " + worst.nodegroup + " may launch"
	if worst.scale {
		verb = fmt.Sprintf("scaling %s up by %d launches", worst.nodegroup, worst.likely)
	}
	what := fmt.Sprintf("%s %s (%s vCPU)", verb, nodes(worst), spanF(vcpus(worst, worst.likely), vcpus(worst, worst.most)))
	return worstOutcome, launchMessage(what, usage, limit, worst.scale)
}

// launchOutcome is the rule for one launch of likely to most vCPUs.
func launchOutcome(usage, limit, likely, most float64) Outcome {
	switch {
	case usage+likely > limit:
		return Fail
	case usage+most > limit:
		return Warn
	}
	return Pass
}

// launchMessage is the message of a launch that does not fit: what it
// launches, the use, and the fix.
func launchMessage(what string, usage, limit float64, scale bool) string {
	fix := "request a higher EC2 On-Demand vCPU quota, or set the update strategy to MINIMAL"
	if scale {
		fix = "request a higher EC2 On-Demand vCPU quota, or scale up by fewer nodes"
	}
	return fmt.Sprintf("%s and %.0f of %.0f vCPU are in use: %s", what, usage, limit, fix)
}

// launchDetail is the detail line of one launch: the count and its
// reason.
func launchDetail(l launch) string {
	line := fmt.Sprintf("rolling %s may launch %s, %d vCPU each (%s vCPU)", l.nodegroup, nodes(l), l.vcpus,
		spanF(float64(l.likely*l.vcpus), float64(l.most*l.vcpus)))
	if l.scale {
		line = fmt.Sprintf("scaling %s up by %d launches %s, %d vCPU each (%d vCPU)", l.nodegroup, l.likely, nodes(l), l.vcpus, l.likely*l.vcpus)
	}
	if len(l.types) > 1 {
		line += fmt.Sprintf("; of its instance types (%s), each node counts as the largest, %s", strings.Join(l.types, ", "), l.largest)
	}
	return line
}

// nodes names the nodes of l: "3 to 6 t3.medium nodes", or "3 to 6 nodes
// of up to m5.xlarge" for a nodegroup with several types.
func nodes(l launch) string {
	noun := "nodes"
	if l.likely == 1 && l.most == 1 {
		noun = "node"
	}
	if len(l.types) > 1 {
		return span(l.likely, l.most) + " " + noun + " of up to " + l.largest
	}
	return span(l.likely, l.most) + " " + l.largest + " " + noun
}

// span is "3 to 6", or "3" when both are equal.
func span(low, high int32) string {
	if low == high {
		return fmt.Sprint(low)
	}
	return fmt.Sprintf("%d to %d", low, high)
}

func spanF(low, high float64) string {
	return span(int32(low), int32(high))
}
