package fleet

import "github.com/dantech2000/refresh/internal/apidoc"

// ClusterDoc returns the document form of the cluster. A field group that
// was not read is left out, and every list of a group that was read is not
// nil, so the document prints [] and never null.
func (s *Snapshot) ClusterDoc() apidoc.Cluster {
	c := s.Cluster
	doc := apidoc.Cluster{Name: c.Name, Region: c.Region, Incomplete: s.Incomplete()}
	if s.Have.Nodegroups == Collected || s.Have.Nodegroups == Partial {
		doc.Nodegroups = make([]apidoc.Nodegroup, len(s.Nodegroups))
		for i, n := range s.Nodegroups {
			doc.Nodegroups[i] = s.NodegroupDoc(n)
		}
	}
	if s.Have.Addons == Collected || s.Have.Addons == Partial {
		doc.Addons = make([]apidoc.Addon, len(s.Addons))
		for i, a := range s.Addons {
			doc.Addons[i] = AddonDoc(a)
		}
	}
	if s.Have.Cluster != Collected {
		return doc
	}
	doc.ARN = c.ARN
	doc.Status = c.Status
	doc.Version = c.Version.String()
	doc.PlatformVersion = c.PlatformVersion
	doc.Endpoint = c.Endpoint
	doc.CreatedAt = c.CreatedAt
	doc.UpgradePolicy = c.UpgradePolicy
	doc.Tags = c.Tags
	if doc.Tags == nil {
		doc.Tags = map[string]string{}
	}
	doc.HealthIssues = make([]apidoc.Issue, 0, len(c.HealthIssues))
	for _, issue := range c.HealthIssues {
		doc.HealthIssues = append(doc.HealthIssues, apidoc.Issue{
			Code:        issue.Code,
			Message:     issue.Message,
			ResourceIDs: list(issue.ResourceIDs),
		})
	}
	doc.Network = &apidoc.Network{
		VPCID:             c.Network.VPCID,
		SubnetIDs:         list(c.Network.SubnetIDs),
		SecurityGroupIDs:  list(c.Network.SecurityGroupIDs),
		PublicAccess:      c.Network.PublicAccess,
		PrivateAccess:     c.Network.PrivateAccess,
		PublicAccessCIDRs: list(c.Network.PublicAccessCIDRs),
	}
	if s.Have.VPC == Collected {
		doc.Network.VPCCIDR = c.Network.VPCCIDR
	}
	if s.Have.Support == Collected {
		doc.Support = &apidoc.Support{
			Tier:          c.Support.Tier,
			StandardUntil: c.Support.StandardEnd,
			ExtendedUntil: c.Support.ExtendedEnd,
			DaysRemaining: c.Support.DaysRemaining,
			AutoUpgrade:   c.Support.AutoUpgrade,
			BuiltIn:       c.Support.BuiltIn,
		}
	}
	if s.Have.Compute == Collected {
		doc.Compute = c.Compute
	}
	doc.Security = &apidoc.Security{
		RoleARN:            c.Security.RoleARN,
		KMSKeyARN:          c.Security.KMSKeyARN,
		LogTypes:           list(c.Security.LogTypes),
		DeletionProtection: c.Security.DeletionProtection,
	}
	return doc
}

// list returns s, or an empty list when s is nil. The SDK returns nil for
// an empty list, and a document must not print null.
func list[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// AddonDoc returns the document form of an add-on. One that was not read
// has its name and incomplete.
func AddonDoc(a Addon) apidoc.Addon {
	if a.State != Collected {
		return apidoc.Addon{Name: a.Name, Incomplete: true}
	}
	issues := make([]apidoc.Issue, len(a.Issues))
	for i, issue := range a.Issues {
		issues[i] = apidoc.Issue{Code: issue.Code, Message: issue.Message, ResourceIDs: list(issue.ResourceIDs)}
	}
	doc := apidoc.Addon{
		Name:                  a.Name,
		Version:               a.Version,
		Status:                a.Status,
		Health:                a.Health(),
		ARN:                   a.ARN,
		ServiceAccountRoleARN: a.ServiceAccountRoleARN,
		Configuration:         a.Configuration,
		Issues:                issues,
		CreatedAt:             a.CreatedAt,
		ModifiedAt:            a.ModifiedAt,
		Incomplete:            failed(a.LatestState),
	}
	if a.LatestState == Collected {
		doc.Latest = &apidoc.AddonLatest{Version: a.Latest}
		if behind, known := a.Behind(); known {
			doc.Latest.Behind = &behind
		}
	}
	return doc
}

// NodegroupDoc returns the document form of a nodegroup of the snapshot.
// One that was not described has its name and incomplete. A group that was
// not read is left out: the AMI, the instances. A group whose read failed
// also marks the nodegroup incomplete.
func (s *Snapshot) NodegroupDoc(n Nodegroup) apidoc.Nodegroup {
	if n.State != Collected {
		return apidoc.Nodegroup{Name: n.Name, Incomplete: true}
	}
	doc := apidoc.Nodegroup{
		Name:           n.Name,
		Status:         n.Status,
		Version:        n.Version.String(),
		AMIType:        n.AMIType,
		ReleaseVersion: n.ReleaseVersion,
		CapacityType:   n.CapacityType,
		InstanceTypes:  list(n.InstanceTypes),
		Scaling:        &apidoc.Scaling{Desired: n.Desired, Min: n.Min, Max: n.Max},
		HealthIssues:   make([]apidoc.Issue, len(n.HealthIssues)),
		CreatedAt:      n.CreatedAt,
		ModifiedAt:     n.ModifiedAt,
	}
	for i, issue := range n.HealthIssues {
		doc.HealthIssues[i] = apidoc.Issue{Code: issue.Code, Message: issue.Message, ResourceIDs: list(issue.ResourceIDs)}
	}
	if behind, known := s.VersionBehind(n); known {
		doc.VersionBehind = &behind
	}
	if status, known := n.AMIStatus(); known && n.AMI.State != NotRequested {
		doc.AMI = &apidoc.NodegroupAMI{Status: status, Current: n.AMI.Current, Latest: n.AMI.Latest, CurrentCreatedAt: n.AMI.CurrentCreatedAt}
	}
	if n.ReadyState == Collected {
		ready := n.ReadyNodes
		doc.ReadyNodes = &ready
	}
	if n.WorkloadsState == Collected {
		doc.Workloads = &apidoc.Workloads{Pods: n.Workloads.Pods, SystemPods: n.Workloads.SystemPods}
	}
	if n.InstancesState == Collected {
		doc.Instances = make([]apidoc.Instance, len(n.Instances))
		for i, inst := range n.Instances {
			doc.Instances[i] = apidoc.Instance{
				ID: inst.ID, Type: inst.Type, State: inst.State, Lifecycle: inst.Lifecycle,
				AvailabilityZone: inst.AvailabilityZone, ImageID: inst.ImageID, LaunchTime: inst.LaunchTime,
			}
		}
	}
	doc.Incomplete = failed(n.AMI.State) || failed(n.InstancesState)
	return doc
}

// failed reports whether the read of a group failed or never started.
func failed(s State) bool {
	return s == Failed || s == NotAttempted
}
