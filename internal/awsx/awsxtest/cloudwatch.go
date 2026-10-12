package awsxtest

import (
	"maps"
	"slices"
	"time"

	"github.com/aws/smithy-go/encoding/cbor"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

var cloudWatchOperations = map[string]operation{
	"GetMetricData": {handle: cloudWatchGetMetricData},
}

// cloudWatchGetMetricData answers one result per query, with no values for
// a query that matches no metric, as CloudWatch does. Datapoints are one
// period apart and end at the request's EndTime. Pages hold whole results:
// a forced page size of 1 answers one query per page.
func cloudWatchGetMetricData(c *call) (any, error) {
	end, _ := c.in["EndTime"].(time.Time)
	ascending := c.in.str("ScanBy") == "TimestampAscending"

	var results []cbor.Value
	c.world.View(func(s *fleettest.State) {
		for _, q := range c.in.objs("MetricDataQueries") {
			stat := q.obj("MetricStat")
			metric := stat.obj("Metric")
			dimensions := map[string]string{}
			for _, d := range metric.objs("Dimensions") {
				dimensions[d.str("Name")] = d.str("Value")
			}
			seconds, _ := stat.num("Period")
			period := time.Duration(seconds) * time.Second

			var values []float64
			for _, m := range s.Metrics {
				if m.Namespace == metric.str("Namespace") && m.Name == metric.str("MetricName") && maps.Equal(m.Dimensions, dimensions) {
					values = m.Values
				}
			}
			var timestamps, points cbor.List
			for i, v := range values {
				age := time.Duration(len(values)-1-i) * period
				timestamps = append(timestamps, cborTime(end.Add(-age)))
				points = append(points, cbor.Float64(v))
			}
			if !ascending {
				slices.Reverse(timestamps)
				slices.Reverse(points)
			}
			results = append(results, cbor.Map{
				"Id":         cbor.String(q.str("Id")),
				"Label":      cbor.String(metric.str("MetricName")),
				"StatusCode": cbor.String("Complete"),
				"Timestamps": timestamps,
				"Values":     points,
			})
		}
	})

	results, next, err := page(c, results, "", "NextToken")
	if err != nil {
		return nil, err
	}
	out := cbor.Map{"MetricDataResults": cbor.List(results)}
	if next != "" {
		out["NextToken"] = cbor.String(next)
	}
	return out, nil
}
