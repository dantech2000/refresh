package fleettest

import "time"

// The types in this file are the data refresh reads outside EKS. Each has a
// Region: "" answers in every region, and any other value only there.

// Image is an AMI.
type Image struct {
	Region  string
	ID      string
	Name    string
	Created time.Time
}

// Instance is an EC2 instance.
type Instance struct {
	Region  string
	ID      string
	ImageID string
	Type    string
	// State is an EC2 instance state name. It defaults to running when read.
	State string
	// Lifecycle is "spot" for a Spot instance and "" for On-Demand.
	Lifecycle        string
	AvailabilityZone string
	LaunchTime       time.Time
	Tags             map[string]string
}

// LaunchTemplateVersion is one version of an EC2 launch template.
type LaunchTemplateVersion struct {
	Region     string
	TemplateID string
	Version    string
	ImageID    string
}

// Subnet is a VPC subnet.
type Subnet struct {
	Region           string
	ID               string
	AvailabilityZone string
	VPCID            string
}

// VPC is a VPC.
type VPC struct {
	Region string
	ID     string
	CIDR   string
}

// Offering says that an instance type is offered in an availability zone.
type Offering struct {
	Region           string
	InstanceType     string
	AvailabilityZone string
}

// AutoScalingGroup is an Auto Scaling group and the instances in it.
type AutoScalingGroup struct {
	Region            string
	Name              string
	InstanceIDs       []string
	AvailabilityZones []string
}

// ScalingActivity is one activity of an Auto Scaling group.
type ScalingActivity struct {
	Region string
	ID     string
	Group  string
	// Status is an Auto Scaling status code, such as Successful or Failed.
	Status      string
	Message     string
	Description string
	Cause       string
	Start       time.Time
}

// Metric is one CloudWatch metric. A GetMetricData query reads it when the
// namespace and name match and the query has exactly these dimensions.
type Metric struct {
	Region     string
	Namespace  string
	Name       string
	Dimensions map[string]string
	// Values are the datapoints, oldest first. A query gets all of them,
	// whatever its time range: the product asks with the real clock, and
	// the world's clock is virtual.
	Values []float64
}

// Quota is a Service Quotas quota value.
type Quota struct {
	Region      string
	ServiceCode string
	QuotaCode   string
	Value       float64
}
