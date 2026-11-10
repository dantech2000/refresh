package awsxtest

import (
	"fmt"
	"slices"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// The page rules are from the API reference: MaxRecords of
// DescribeAutoScalingGroups "The default value is 50 and the maximum value
// is 100", and of DescribeScalingActivities "The default value is 100 and
// the maximum value is 100"
// (https://docs.aws.amazon.com/autoscaling/ec2/APIReference/API_DescribeAutoScalingGroups.html,
// https://docs.aws.amazon.com/autoscaling/ec2/APIReference/API_DescribeScalingActivities.html).
var autoScalingOperations = map[string]operation{
	"DescribeAutoScalingGroups": {handle: autoScalingDescribeGroups, pages: pageRule{Default: 50, Min: 1, Max: 100}},
	"DescribeScalingActivities": {handle: autoScalingDescribeActivities, pages: pageRule{Default: 100, Min: 1, Max: 100}},
}

// autoScalingDescribeGroups leaves out a name that has no group, as Auto
// Scaling does. It takes at most 50 names, or MaxRecords names when the
// request sets more: "By default, you can only specify up to 50 names.
// You can optionally increase this limit using the MaxRecords property."
func autoScalingDescribeGroups(c *call) (any, error) {
	asked := c.in.list("AutoScalingGroupNames.member")
	limit, ok := c.in.num("MaxRecords")
	if !ok || limit < 50 {
		limit = 50
	}
	if len(asked) > limit {
		return nil, newError(c.svc.invalid, fmt.Sprintf("The number of AutoScalingGroupNames (%d) exceeds the limit of %d.", len(asked), limit))
	}
	var groups []fleettest.AutoScalingGroup
	c.world.View(func(s *fleettest.State) {
		for _, g := range s.AutoScalingGroups {
			if c.sees(g.Region) && (len(asked) == 0 || slices.Contains(asked, g.Name)) {
				g.InstanceIDs = slices.Clone(g.InstanceIDs)
				g.AvailabilityZones = slices.Clone(g.AvailabilityZones)
				groups = append(groups, g)
			}
		}
	})
	groups, next, err := page(c, groups, "MaxRecords", "NextToken")
	if err != nil {
		return nil, err
	}
	members := each(groups, func(g fleettest.AutoScalingGroup) node {
		return tag("member",
			leaf("AutoScalingGroupName", g.Name),
			tag("AvailabilityZones", each(g.AvailabilityZones, func(az string) node { return leaf("member", az) })...),
			tag("Instances", each(g.InstanceIDs, func(id string) node {
				return tag("member", leaf("InstanceId", id), leaf("LifecycleState", "InService"), leaf("HealthStatus", "Healthy"))
			})...),
		)
	})
	return []node{tag("AutoScalingGroups", members...), leaf("NextToken", next)}, nil
}

func autoScalingDescribeActivities(c *call) (any, error) {
	group := c.in.str("AutoScalingGroupName")
	var activities []fleettest.ScalingActivity
	c.world.View(func(s *fleettest.State) {
		for _, a := range s.ScalingActivities {
			if c.sees(a.Region) && (group == "" || a.Group == group) {
				activities = append(activities, a)
			}
		}
	})
	activities, next, err := page(c, activities, "MaxRecords", "NextToken")
	if err != nil {
		return nil, err
	}
	members := each(activities, func(a fleettest.ScalingActivity) node {
		return tag("member",
			leaf("ActivityId", a.ID),
			leaf("AutoScalingGroupName", a.Group),
			leaf("StatusCode", a.Status),
			leaf("StatusMessage", a.Message),
			leaf("Description", a.Description),
			leaf("Cause", a.Cause),
			leaf("StartTime", xmlTime(a.Start)),
		)
	})
	return []node{tag("Activities", members...), leaf("NextToken", next)}, nil
}
