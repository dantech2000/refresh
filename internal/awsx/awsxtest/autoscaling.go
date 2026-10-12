package awsxtest

import (
	"slices"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

var autoScalingOperations = map[string]operation{
	"DescribeAutoScalingGroups": {handle: autoScalingDescribeGroups},
	"DescribeScalingActivities": {handle: autoScalingDescribeActivities},
}

// autoScalingDescribeGroups leaves out a name that has no group, as Auto
// Scaling does.
func autoScalingDescribeGroups(c *call) (any, error) {
	asked := c.in.list("AutoScalingGroupNames.member")
	var groups []fleettest.AutoScalingGroup
	c.world.View(func(s *fleettest.State) {
		for _, g := range s.AutoScalingGroups {
			if len(asked) == 0 || slices.Contains(asked, g.Name) {
				g.InstanceIDs = slices.Clone(g.InstanceIDs)
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
			if group == "" || a.Group == group {
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
