package awsxtest

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

var ec2Operations = map[string]operation{
	"DescribeImages":                 {handle: ec2DescribeImages},
	"DescribeInstances":              {handle: ec2DescribeInstances},
	"DescribeLaunchTemplateVersions": {handle: ec2DescribeLaunchTemplateVersions},
	"DescribeSubnets":                {handle: ec2DescribeSubnets},
	"DescribeInstanceTypeOfferings":  {handle: ec2DescribeInstanceTypeOfferings},
	"DescribeInstanceTypes":          {handle: ec2DescribeInstanceTypes},
	"DescribeVpcs":                   {handle: ec2DescribeVpcs},
}

// ec2Filter is one Filter.N of a request.
type ec2Filter struct {
	name   string
	values []string
}

func (c *call) ec2Filters() []ec2Filter {
	var out []ec2Filter
	for i := 1; ; i++ {
		prefix := "Filter." + strconv.Itoa(i)
		name := c.in.str(prefix + ".Name")
		if name == "" {
			return out
		}
		out = append(out, ec2Filter{name: name, values: c.in.list(prefix + ".Value")})
	}
}

// ec2Select returns the items that the request's IDs and filters keep. The
// IDs are the list at idKey, or none when idKey is "". An asked ID with no
// item is the notFound error, as in EC2. match reports whether an item
// passes a filter, and ok=false for a filter name the fake does not model,
// which fails the request so a test never reads a filter that was ignored.
func ec2Select[T any](c *call, items []T, idKey, notFound string, id func(T) string, match func(T, ec2Filter) (keep, ok bool)) ([]T, error) {
	var asked []string
	if idKey != "" {
		asked = c.in.list(idKey)
	}
	for _, want := range asked {
		if !slices.ContainsFunc(items, func(it T) bool { return id(it) == want }) {
			return nil, newError(notFound, "The ID '"+want+"' does not exist")
		}
	}
	filters := c.ec2Filters()
	var out []T
	for _, it := range items {
		if len(asked) > 0 && !slices.Contains(asked, id(it)) {
			continue
		}
		keep := true
		for _, f := range filters {
			pass, ok := match(it, f)
			if !ok {
				return nil, newError("InvalidParameterValue", "awsxtest does not model the filter '"+f.name+"'")
			}
			keep = keep && pass
		}
		if keep {
			out = append(out, it)
		}
	}
	return out, nil
}

// inRegion returns a copy of the items that answer in the request's region.
func inRegion[T any](c *call, items []T, region func(T) string) []T {
	var out []T
	for _, it := range items {
		if c.sees(region(it)) {
			out = append(out, it)
		}
	}
	return out
}

func noFilters[T any](T, ec2Filter) (keep, ok bool) { return false, false }

// ec2Paged answers a list in the element set, one item element each.
func ec2Paged[T any](c *call, set string, items []T, item func(T) []node) (any, error) {
	items, next, err := page(c, items, "MaxResults", "NextToken")
	if err != nil {
		return nil, err
	}
	members := each(items, func(it T) node { return tag("item", item(it)...) })
	return []node{tag(set, members...), leaf("nextToken", next)}, nil
}

func ec2DescribeImages(c *call) (any, error) {
	var images []fleettest.Image
	c.world.View(func(s *fleettest.State) {
		images = inRegion(c, s.Images, func(i fleettest.Image) string { return i.Region })
	})
	images, err := ec2Select(c, images, "ImageId", "InvalidAMIID.NotFound", func(i fleettest.Image) string { return i.ID },
		func(i fleettest.Image, f ec2Filter) (keep, ok bool) {
			// As with instances, the filter leaves out an ID with no image.
			return slices.Contains(f.values, i.ID), f.name == "image-id"
		})
	if err != nil {
		return nil, err
	}
	return ec2Paged(c, "imagesSet", images, func(i fleettest.Image) []node {
		return []node{leaf("imageId", i.ID), leaf("name", i.Name), leaf("creationDate", xmlTime(i.Created))}
	})
}

func ec2DescribeInstances(c *call) (any, error) {
	var instances []fleettest.Instance
	c.world.View(func(s *fleettest.State) {
		for _, i := range s.Instances {
			if !c.sees(i.Region) {
				continue
			}
			i.Tags = cloneMap(i.Tags)
			instances = append(instances, i)
		}
	})
	instances, err := ec2Select(c, instances, "InstanceId", "InvalidInstanceID.NotFound", func(i fleettest.Instance) string { return i.ID }, matchInstance)
	if err != nil {
		return nil, err
	}
	// One reservation per instance.
	return ec2Paged(c, "reservationSet", instances, func(i fleettest.Instance) []node {
		keys := make([]string, 0, len(i.Tags))
		for k := range i.Tags {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		return []node{
			leaf("reservationId", "r-"+strings.TrimPrefix(i.ID, "i-")),
			tag("instancesSet", tag("item",
				leaf("instanceId", i.ID),
				leaf("imageId", i.ImageID),
				leaf("instanceType", i.Type),
				tag("instanceState", leaf("name", instanceState(i))),
				leaf("launchTime", xmlTime(i.LaunchTime)),
				tag("placement", leaf("availabilityZone", i.AvailabilityZone)),
				leaf("instanceLifecycle", i.Lifecycle),
				tag("tagSet", each(keys, func(k string) node {
					return tag("item", leaf("key", k), leaf("value", i.Tags[k]))
				})...),
			)),
		}
	})
}

func instanceState(i fleettest.Instance) string { return cmp.Or(i.State, "running") }

func matchInstance(i fleettest.Instance, f ec2Filter) (keep, ok bool) {
	if key, isTag := strings.CutPrefix(f.name, "tag:"); isTag {
		value, has := i.Tags[key]
		return has && slices.Contains(f.values, value), true
	}
	switch f.name {
	case "instance-id":
		// Unlike the InstanceId list, the filter leaves out an ID that
		// has no instance and fails nothing.
		return slices.Contains(f.values, i.ID), true
	case "tag-key":
		return slices.ContainsFunc(f.values, func(k string) bool { _, has := i.Tags[k]; return has }), true
	case "instance-state-name":
		return slices.Contains(f.values, instanceState(i)), true
	}
	return false, false
}

func cloneMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func ec2DescribeLaunchTemplateVersions(c *call) (any, error) {
	template, asked := c.in.str("LaunchTemplateId"), c.in.list("LaunchTemplateVersion")
	var versions []fleettest.LaunchTemplateVersion
	known := false
	c.world.View(func(s *fleettest.State) {
		for _, v := range s.LaunchTemplateVersions {
			if !c.sees(v.Region) || v.TemplateID != template {
				continue
			}
			known = true
			if len(asked) == 0 || slices.Contains(asked, v.Version) {
				versions = append(versions, v)
			}
		}
	})
	if !known {
		return nil, newError("InvalidLaunchTemplateId.NotFound", "The specified launch template, with template ID "+template+", does not exist.")
	}
	return ec2Paged(c, "launchTemplateVersionSet", versions, func(v fleettest.LaunchTemplateVersion) []node {
		number := ""
		if _, err := strconv.Atoi(v.Version); err == nil {
			number = v.Version
		}
		return []node{
			leaf("launchTemplateId", v.TemplateID),
			leaf("versionNumber", number),
			tag("launchTemplateData", leaf("imageId", v.ImageID)),
		}
	})
}

func ec2DescribeSubnets(c *call) (any, error) {
	var subnets []fleettest.Subnet
	c.world.View(func(s *fleettest.State) {
		subnets = inRegion(c, s.Subnets, func(s fleettest.Subnet) string { return s.Region })
	})
	subnets, err := ec2Select(c, subnets, "SubnetId", "InvalidSubnetID.NotFound", func(s fleettest.Subnet) string { return s.ID }, noFilters)
	if err != nil {
		return nil, err
	}
	return ec2Paged(c, "subnetSet", subnets, func(s fleettest.Subnet) []node {
		return []node{leaf("subnetId", s.ID), leaf("availabilityZone", s.AvailabilityZone), leaf("vpcId", s.VPCID)}
	})
}

// ec2DescribeInstanceTypeOfferings answers only for the availability-zone
// location type, which is all the world holds.
func ec2DescribeInstanceTypeOfferings(c *call) (any, error) {
	if lt := c.in.str("LocationType"); lt != "availability-zone" {
		return nil, newError("InvalidParameterValue", "awsxtest models LocationType availability-zone only, got '"+lt+"'")
	}
	var offerings []fleettest.Offering
	c.world.View(func(s *fleettest.State) {
		offerings = inRegion(c, s.Offerings, func(o fleettest.Offering) string { return o.Region })
	})
	offerings, err := ec2Select(c, offerings, "", "", func(fleettest.Offering) string { return "" },
		func(o fleettest.Offering, f ec2Filter) (keep, ok bool) {
			switch f.name {
			case "instance-type":
				return slices.Contains(f.values, o.InstanceType), true
			case "location":
				return slices.Contains(f.values, o.AvailabilityZone), true
			}
			return false, false
		})
	if err != nil {
		return nil, err
	}
	return ec2Paged(c, "instanceTypeOfferingSet", offerings, func(o fleettest.Offering) []node {
		return []node{leaf("instanceType", o.InstanceType), leaf("locationType", "availability-zone"), leaf("location", o.AvailabilityZone)}
	})
}

// ec2DescribeInstanceTypes answers the vCPU count of each type, from the
// world's catalog. An asked type that the catalog does not hold fails the
// request, as in EC2.
func ec2DescribeInstanceTypes(c *call) (any, error) {
	var types []fleettest.InstanceType
	c.world.View(func(s *fleettest.State) {
		types = inRegion(c, s.InstanceTypes, func(t fleettest.InstanceType) string { return t.Region })
	})
	types, err := ec2Select(c, types, "InstanceType", "InvalidInstanceType", func(t fleettest.InstanceType) string { return t.Name }, noFilters)
	if err != nil {
		return nil, err
	}
	return ec2Paged(c, "instanceTypeSet", types, func(t fleettest.InstanceType) []node {
		return []node{leaf("instanceType", t.Name), tag("vCpuInfo", leaf("defaultVCpus", strconv.Itoa(int(t.VCPUs))))}
	})
}

func ec2DescribeVpcs(c *call) (any, error) {
	var vpcs []fleettest.VPC
	c.world.View(func(s *fleettest.State) { vpcs = inRegion(c, s.VPCs, func(v fleettest.VPC) string { return v.Region }) })
	vpcs, err := ec2Select(c, vpcs, "VpcId", "InvalidVpcID.NotFound", func(v fleettest.VPC) string { return v.ID }, noFilters)
	if err != nil {
		return nil, err
	}
	return ec2Paged(c, "vpcSet", vpcs, func(v fleettest.VPC) []node {
		return []node{leaf("vpcId", v.ID), leaf("cidrBlock", v.CIDR)}
	})
}
