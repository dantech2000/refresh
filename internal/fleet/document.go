package fleet

import "github.com/dantech2000/refresh/internal/apidoc"

// ClusterDoc returns the document form of the cluster. A field group that
// was not read is left out, and every list of a group that was read is not
// nil, so the document prints [] and never null.
func (s *Snapshot) ClusterDoc() apidoc.Cluster {
	c := s.Cluster
	doc := apidoc.Cluster{Name: c.Name, Region: c.Region, Incomplete: s.Incomplete()}
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
