// Package fleetdata holds the facts about EKS that refresh ships with: data
// that changes slowly and that no API call of a normal run returns. Each
// table says when it was last checked against AWS.
package fleetdata

import (
	"slices"
	"strings"
)

// RegionsAsOf is the date of the last check of the region lists.
const RegionsAsOf = "2026-10-12"

// Regions returns the regions of a default sweep for the partition of home:
// the regions that every account of the partition has enabled and where EKS
// runs. An empty home means the commercial partition. Opt-in regions are
// not in the list; a user names them with -r or REFRESH_EKS_REGIONS.
func Regions(home string) []string {
	switch {
	case strings.HasPrefix(home, "us-gov-"):
		return []string{"us-gov-west-1", "us-gov-east-1"}
	case strings.HasPrefix(home, "cn-"):
		return []string{"cn-north-1", "cn-northwest-1"}
	}
	return slices.Clone(commercial)
}

var commercial = []string{
	"us-east-1", "us-east-2", "us-west-1", "us-west-2",
	"eu-west-1", "eu-west-2", "eu-west-3", "eu-central-1", "eu-north-1",
	"ap-southeast-1", "ap-southeast-2", "ap-northeast-1", "ap-northeast-2", "ap-south-1",
	"ca-central-1", "sa-east-1",
}

// AlwaysOnRegion returns a region in the partition of home that every
// account has enabled. A credential check runs there: STS in a region that
// the account has not enabled rejects valid keys with the same code as bad
// keys.
func AlwaysOnRegion(home string) string {
	switch {
	case strings.HasPrefix(home, "us-gov-"):
		return "us-gov-west-1"
	case strings.HasPrefix(home, "cn-"):
		return "cn-north-1"
	}
	return "us-east-1"
}
