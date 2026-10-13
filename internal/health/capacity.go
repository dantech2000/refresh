package health

import (
	"context"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/fanout"
	"github.com/dantech2000/refresh/internal/fleet"
)

// The CPU thresholds. The mean headroom of the cluster decides, and the
// busiest instance can only make the verdict worse: one saturated node is
// a bottleneck for a roll whatever the mean says.
const (
	cpuSafeHeadroomPercent = 30.0
	cpuWarnHeadroomPercent = 15.0
	cpuPeakFailPercent     = 95.0
	cpuPeakWarnPercent     = 85.0
	cpuWindow              = 10 * time.Minute
	cpuPeriod              = 5 * time.Minute
	// metricQueriesPerCall is the most queries CloudWatch takes in one
	// GetMetricData call.
	metricQueriesPerCall = 500
)

// Capacity checks that the nodes have the CPU headroom to take the pods
// of the nodes that a roll drains, from the EC2 CPU of every instance of
// the managed nodegroups over the last 10 minutes.
func Capacity() Check {
	return Check{Name: "Capacity", Blocking: true, Run: runCapacity}
}

func runCapacity(ctx context.Context, in *Input) Result {
	ids, unread := instanceIDs(in.Snapshot)
	if len(unread) > 0 {
		return Result{Outcome: Unreadable, Message: "the instances of " + plural(len(unread), "nodegroup") + " could not be read: " + joinNames(unread)}
	}
	if len(ids) == 0 {
		return Result{Outcome: Skipped, Message: "no managed nodegroup instances"}
	}
	byInstance, err := instanceCPU(ctx, in, ids)
	if err != nil {
		f := fleet.FailureFromError(apidoc.FailureCluster, in.Snapshot.Cluster.Name, err)
		return Result{Outcome: Unreadable, Message: "could not read the CPU metrics: " + f.Error + failureAction(f), Failures: []apidoc.Failure{f}}
	}
	return evaluateCapacity(byInstance, len(ids))
}

// instanceIDs returns the instance IDs of every nodegroup of the
// snapshot, and the names of the nodegroups whose IDs could not be read.
func instanceIDs(snap *fleet.Snapshot) (ids, unread []string) {
	for _, n := range snap.Nodegroups {
		switch {
		case n.State != fleet.Collected:
			unread = append(unread, n.Name)
		case n.InstanceIDsState == fleet.Collected:
			ids = append(ids, n.InstanceIDs...)
		case n.InstanceIDsState != fleet.NotRequested:
			unread = append(unread, n.Name)
		}
	}
	return ids, unread
}

// instanceCPU returns the mean CPU of each instance over the window, from
// one GetMetricData call per 500 instances. An instance with no
// datapoint is left out.
func instanceCPU(ctx context.Context, in *Input, ids []string) (map[string]float64, error) {
	end := in.Now
	start := end.Add(-cpuWindow)
	chunks := slices.Collect(slices.Chunk(ids, metricQueriesPerCall))
	type answer struct {
		ran bool
		cpu map[string]float64
		err error
	}
	answers := fanout.ForEach(ctx, chunks, len(chunks), func(ctx context.Context, chunk []string) answer {
		queries := make([]cwtypes.MetricDataQuery, len(chunk))
		for i, id := range chunk {
			queries[i] = cwtypes.MetricDataQuery{
				Id: aws.String(fmt.Sprintf("cpu%d", i)),
				MetricStat: &cwtypes.MetricStat{
					Metric: &cwtypes.Metric{
						Namespace:  aws.String("AWS/EC2"),
						MetricName: aws.String("CPUUtilization"),
						Dimensions: []cwtypes.Dimension{{Name: aws.String("InstanceId"), Value: aws.String(id)}},
					},
					Period: aws.Int32(int32(cpuPeriod.Seconds())),
					Stat:   aws.String("Average"),
				},
			}
		}
		results, err := metricData(ctx, in.Clients.CloudWatch, &cloudwatch.GetMetricDataInput{
			StartTime: aws.Time(start), EndTime: aws.Time(end), MetricDataQueries: queries,
		})
		if err != nil {
			return answer{ran: true, err: err}
		}
		cpu := map[string]float64{}
		for _, r := range results {
			var i int
			if _, err := fmt.Sscanf(aws.ToString(r.Id), "cpu%d", &i); err != nil || i < 0 || i >= len(chunk) || len(r.Values) == 0 {
				continue
			}
			cpu[chunk[i]] = sumOf(r.Values) / float64(len(r.Values))
		}
		return answer{ran: true, cpu: cpu}
	})
	byInstance := map[string]float64{}
	for _, a := range answers {
		if !a.ran {
			// The context ended before the chunk was sent.
			return nil, fmt.Errorf("%w: %w", awsx.ErrNotSent, ctx.Err())
		}
		if a.err != nil {
			return nil, a.err
		}
		for id, v := range a.cpu {
			byInstance[id] = v
		}
	}
	return byInstance, nil
}

// evaluateCapacity is the rule, apart from the read.
func evaluateCapacity(byInstance map[string]float64, instances int) Result {
	if len(byInstance) == 0 {
		return Result{Outcome: Warn, Message: fmt.Sprintf("no CPU metrics reported for the %s in the last 10 minutes", plural(instances, "instance"))}
	}
	values := slices.Sorted(func(yield func(float64) bool) {
		for _, v := range byInstance {
			if !yield(v) {
				return
			}
		}
	})
	mean := sumOf(values) / float64(len(values))
	peak := values[len(values)-1]
	spread := 0.0
	for _, v := range values {
		spread += (v - mean) * (v - mean)
	}
	spread = math.Sqrt(spread / float64(len(values)))

	r := Result{Details: []string{
		fmt.Sprintf("CPU mean %.1f%%, peak %.1f%%, spread %.1f points, over %d of %s", mean, peak, spread, len(values), plural(instances, "instance")),
	}}
	switch headroom := 100 - mean; {
	case headroom >= cpuSafeHeadroomPercent:
		r.Outcome, r.Message = Pass, fmt.Sprintf("CPU mean %.0f%%, peak %.0f%%, %d instances", mean, peak, len(values))
	case headroom >= cpuWarnHeadroomPercent:
		r.Outcome, r.Message = Warn, fmt.Sprintf("limited CPU headroom (mean %.0f%%, peak %.0f%%)", mean, peak)
	default:
		r.Outcome, r.Message = Fail, fmt.Sprintf("insufficient CPU headroom for a roll (mean %.0f%%, peak %.0f%%)", mean, peak)
	}
	switch {
	case peak >= cpuPeakFailPercent && r.Outcome != Fail:
		r.Outcome, r.Message = Fail, fmt.Sprintf("an instance is near CPU saturation (peak %.0f%%, mean %.0f%%)", peak, mean)
	case peak >= cpuPeakWarnPercent && r.Outcome == Pass:
		r.Outcome, r.Message = Warn, fmt.Sprintf("uneven CPU load (peak %.0f%%, mean %.0f%%)", peak, mean)
	}
	return r
}
