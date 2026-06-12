package nodegroup

import (
	"testing"

	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
)

func TestSortNodegroupSummaries_ByName(t *testing.T) {
	items := []nodegroupsvc.NodegroupSummary{{Name: "zebra"}, {Name: "alpha"}, {Name: "mango"}}
	got := sortNodegroupSummaries(items, "name", false)
	if got[0].Name != "alpha" {
		t.Errorf("first = %q, want alpha", got[0].Name)
	}
}

func TestSortNodegroupSummaries_ByNameDesc(t *testing.T) {
	items := []nodegroupsvc.NodegroupSummary{{Name: "alpha"}, {Name: "zebra"}}
	got := sortNodegroupSummaries(items, "name", true)
	if got[0].Name != "zebra" {
		t.Errorf("desc first = %q, want zebra", got[0].Name)
	}
}

func TestSortNodegroupSummaries_ByStatus(t *testing.T) {
	items := []nodegroupsvc.NodegroupSummary{{Name: "b", Status: "UPDATING"}, {Name: "a", Status: "ACTIVE"}}
	got := sortNodegroupSummaries(items, "status", false)
	if got[0].Status != "ACTIVE" {
		t.Errorf("status asc: first = %q, want ACTIVE", got[0].Status)
	}
}

func TestSortNodegroupSummaries_ByInstance(t *testing.T) {
	items := []nodegroupsvc.NodegroupSummary{{Name: "b", InstanceType: "t3.xlarge"}, {Name: "a", InstanceType: "m5.large"}}
	got := sortNodegroupSummaries(items, "instance", false)
	if got[0].InstanceType != "m5.large" {
		t.Errorf("instance asc: first = %q, want m5.large", got[0].InstanceType)
	}
}

func TestSortNodegroupSummaries_ByNodes(t *testing.T) {
	items := []nodegroupsvc.NodegroupSummary{{Name: "b", ReadyNodes: 5}, {Name: "a", ReadyNodes: 1}}
	got := sortNodegroupSummaries(items, "nodes", false)
	if got[0].ReadyNodes != 1 {
		t.Errorf("nodes asc: first ReadyNodes = %d, want 1", got[0].ReadyNodes)
	}
}

func TestSortNodegroupSummaries_UnknownKeyByName(t *testing.T) {
	items := []nodegroupsvc.NodegroupSummary{{Name: "z"}, {Name: "a"}}
	got := sortNodegroupSummaries(items, "bogus", false)
	if got[0].Name != "a" {
		t.Errorf("unknown key should sort by name, got %q", got[0].Name)
	}
}
