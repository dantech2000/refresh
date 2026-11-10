package fleet_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
)

// A cluster that was read and has nothing in its lists prints [] and {},
// never null: the SDK returns nil for an empty list, and the encoder
// rejects a null.
func TestClusterDoc_CollectedAndEmptyEncodes(t *testing.T) {
	snap := &fleet.Snapshot{
		Cluster: fleet.Cluster{Name: "a", Region: "us-east-1", HealthIssues: []fleet.Issue{{Code: "X"}}},
		Have:    fleet.Have{Cluster: fleet.Collected},
	}
	doc := apidoc.ClusterList{Clusters: []apidoc.Cluster{snap.ClusterDoc()}}
	for name, encode := range map[string]func(*bytes.Buffer) error{
		"json": func(b *bytes.Buffer) error { return apidoc.EncodeJSON(b, doc) },
		"yaml": func(b *bytes.Buffer) error { return apidoc.EncodeYAML(b, doc) },
	} {
		var buf bytes.Buffer
		if err := encode(&buf); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if name != "json" {
			continue
		}
		for _, want := range []string{
			`"tags": {}`, `"subnetIds": []`, `"securityGroupIds": []`, `"publicAccessCidrs": []`,
			`"logTypes": []`, `"resourceIds": []`, `"failures": []`,
		} {
			if !strings.Contains(buf.String(), want) {
				t.Errorf("the document has no %s:\n%s", want, buf.String())
			}
		}
	}
}

// A nodegroup is incomplete when the read of any of its groups failed, and
// sizes that EKS did not return are left out, not printed as 0.
func TestNodegroupDoc(t *testing.T) {
	sizes := &apidoc.Scaling{Desired: 2, Min: 1, Max: 3}
	tests := []struct {
		name           string
		n              fleet.Nodegroup
		wantIncomplete bool
		wantScaling    *apidoc.Scaling
	}{
		{name: "read", n: fleet.Nodegroup{Desired: 2, Min: 1, Max: 3, SizesKnown: true}, wantScaling: sizes},
		{name: "sizes not known", n: fleet.Nodegroup{}},
		{name: "AMI failed", n: fleet.Nodegroup{AMI: fleet.AMI{State: fleet.Failed}}, wantIncomplete: true},
		{name: "instances not attempted", n: fleet.Nodegroup{InstancesState: fleet.NotAttempted}, wantIncomplete: true},
		{name: "ready nodes failed", n: fleet.Nodegroup{ReadyState: fleet.Failed}, wantIncomplete: true},
		{name: "workloads failed", n: fleet.Nodegroup{WorkloadsState: fleet.Failed}, wantIncomplete: true},
		{name: "workloads not requested", n: fleet.Nodegroup{WorkloadsState: fleet.NotRequested, ReadyState: fleet.Collected}},
	}
	snap := &fleet.Snapshot{Have: fleet.Have{Cluster: fleet.Collected}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.n.Name, tt.n.State, tt.n.Status = "web", fleet.Collected, "ACTIVE"
			doc := snap.NodegroupDoc(tt.n)
			if doc.Incomplete != tt.wantIncomplete {
				t.Errorf("incomplete = %v, want %v", doc.Incomplete, tt.wantIncomplete)
			}
			if diff := cmp.Diff(tt.wantScaling, doc.Scaling); diff != "" {
				t.Errorf("scaling (-want +got):\n%s", diff)
			}
		})
	}
}

// A cluster that was not read has its name, its region, and nothing that
// could pass for data.
func TestClusterDoc_NotCollectedOmitsEveryGroup(t *testing.T) {
	for _, state := range []fleet.State{fleet.NotRequested, fleet.Partial, fleet.Failed, fleet.NotAttempted} {
		snap := &fleet.Snapshot{
			Cluster: fleet.Cluster{Name: "a", Region: "us-east-1", Status: "ACTIVE", Version: fleet.Version{Major: 1, Minor: 33}},
			Have:    fleet.Have{Cluster: state},
		}
		want := apidoc.Cluster{Name: "a", Region: "us-east-1"}
		got := snap.ClusterDoc()
		if got.Status != "" || got.Version != "" || got.Network != nil || got.Security != nil || got.Tags != nil || got.HealthIssues != nil {
			t.Errorf("state %s: document = %+v, want %+v", state, got, want)
		}
	}
}
