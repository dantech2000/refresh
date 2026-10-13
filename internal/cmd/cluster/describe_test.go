package cluster_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/cmd/cmdtest"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// newDescribeFake returns a fake AWS with the cluster of the describe
// golden files, and a second cluster with a name close to it.
func newDescribeFake(t *testing.T) *cmdtest.Fake {
	t.Helper()
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) {
		s.Clusters = append(s.Clusters,
			&fleettest.Cluster{
				Name: "prod", Region: east, Version: "1.33", SupportType: "STANDARD",
				VPCID: "vpc-0a1", SubnetIDs: []string{"subnet-1", "subnet-2"}, SecurityGroupIDs: []string{"sg-1"},
				EndpointPublicAccess: true, EndpointPrivateAccess: true, PublicAccessCIDRs: []string{"203.0.113.0/24"},
				RoleARN:  "arn:aws:iam::123456789012:role/eks-prod",
				LogTypes: []string{"api", "audit"}, EncryptionKeyARN: "arn:aws:kms:us-east-1:123456789012:key/k1",
				DeletionProtection: true, Tags: map[string]string{"team": "payments", "env": "prod"},
				HealthIssues: []fleettest.Issue{{Code: "Ec2SubnetInvalidConfiguration", Message: "A subnet is gone", ResourceIDs: []string{"subnet-9"}}},
				Nodegroups: []*fleettest.Nodegroup{
					{Name: "web", Version: "1.33", InstanceTypes: []string{"m5.large", "m5a.large"}, Desired: 6, Min: 2, Max: 12},
					{Name: "old", Version: "1.32", Status: fleettest.StatusUpdating, Desired: 3, Min: 3, Max: 3},
				},
				Addons: []*fleettest.Addon{
					{Name: "vpc-cni", Version: "v1.19.0-eksbuild.1"},
					{Name: "coredns", Version: "v1.11.3-eksbuild.2", Status: fleettest.StatusDegraded},
				},
			},
			&fleettest.Cluster{Name: "prod-eu", Region: east, Version: "1.34"},
		)
		s.VPCs = []fleettest.VPC{{ID: "vpc-0a1", CIDR: "10.0.0.0/16"}}
		s.ClusterVersions = []fleettest.ClusterVersion{
			{Version: "1.33", EndOfStandardSupport: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), EndOfExtendedSupport: time.Date(2027, 12, 1, 0, 0, 0, 0, time.UTC)},
		}
	})
	return cmdtest.NewFake(t, world, east)
}

func TestDescribe_Formats(t *testing.T) {
	for _, tt := range []struct{ format, file string }{
		{"table", "describe.txt"}, {"plain", "describe.tsv"}, {"json", "describe.json"}, {"yaml", "describe.yaml"},
	} {
		t.Run(tt.format, func(t *testing.T) {
			e := newDescribeFake(t)
			if err := e.Run(cmdutiltest.Context(t), "cluster", "describe", "prod", "-o", tt.format); err != nil {
				t.Fatalf("run: %v", err)
			}
			if e.Err.Len() != 0 {
				t.Errorf("stderr = %q, want nothing", e.Err.String())
			}
			cmdutiltest.Golden(t, tt.file, e.Out.String(), *update)
			switch tt.format {
			case "json":
				cmdutiltest.ValidateJSON(t, apidoc.KindClusterDescription, e.Out.Bytes())
			case "yaml":
				cmdutiltest.ValidateYAML(t, apidoc.KindClusterDescription, e.Out.Bytes())
			}
		})
	}
}

// "cluster describe" is two round trips, and no call lists clusters.
func TestDescribe_Waves(t *testing.T) {
	e := newDescribeFake(t)
	gate := awsxtest.NewGate(t, e.Server.Handler)
	done := make(chan error, 1)
	go func() { done <- e.Run(cmdutiltest.Context(t), "cluster", "describe", "prod", "-o", "json") }()
	gate.Wave(fleettest.EKSDescribeCluster, fleettest.EKSListNodegroups, fleettest.EKSListAddons, fleettest.EKSDescribeClusterVersions)
	gate.Wave(fleettest.EKSDescribeNodegroup, fleettest.EKSDescribeNodegroup, fleettest.EKSDescribeAddon, fleettest.EKSDescribeAddon, fleettest.EC2DescribeVpcs)
	gate.Done()
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	if n := len(e.Server.Requests()); n != 9 {
		t.Errorf("requests = %v, want 9", e.Operations())
	}
}

// A part that cannot be read: the command prints the rest, names the part,
// and exits 4.
func TestDescribe_Partial(t *testing.T) {
	for _, tt := range []struct{ format, file string }{{"table", "describe-partial.txt"}, {"json", "describe-partial.json"}} {
		t.Run(tt.format, func(t *testing.T) {
			e := newDescribeFake(t)
			e.World.SetFault(fleettest.EKSListAddons, fleettest.Fault{Deny: true})
			e.World.SetScopedFault(fleettest.Scope{Operation: fleettest.EKSDescribeNodegroup, Nodegroup: "old"}, fleettest.Fault{Deny: true})
			err := e.Run(cmdutiltest.Context(t), "cluster", "describe", "prod", "-o", tt.format)
			if got := cmdutil.ExitCode(err); got != cmdutil.ExitIncomplete {
				t.Errorf("exit code = %d (%v), want %d", got, err, cmdutil.ExitIncomplete)
			}
			cmdutiltest.Golden(t, tt.file, e.Out.String(), *update)
			if tt.format == "json" {
				cmdutiltest.ValidateJSON(t, apidoc.KindClusterDescription, e.Out.Bytes())
			}
		})
	}
}

// The support calendar and the VPC are extras: without them the command
// uses the dates that refresh ships with, leaves the CIDR out, and exits 0.
func TestDescribe_AdvisoryReads(t *testing.T) {
	e := newDescribeFake(t)
	e.World.SetFault(fleettest.EKSDescribeClusterVersions, fleettest.Fault{Deny: true})
	e.World.SetFault(fleettest.EC2DescribeVpcs, fleettest.Fault{Deny: true})
	if err := e.Run(cmdutiltest.Context(t), "cluster", "describe", "prod", "-o", "plain"); err != nil {
		t.Fatalf("run: %v", err)
	}
	out := e.Out.String()
	for _, want := range []string{
		"overview.support\textended until 2027-07-29 (301 days), at a higher price; dates from refresh, not from EKS\n",
		"network.vpc\tvpc-0a1\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout has no %q:\n%s", want, out)
		}
	}
	if e.Err.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", e.Err.String())
	}
}

func TestDescribe_NotFoundAndTarget(t *testing.T) {
	e := newDescribeFake(t)
	err := e.Run(cmdutiltest.Context(t), "cluster", "describe", "prd")
	want := "cluster \"prd\" not found in us-east-1\nDid you mean:\n  prod\n  prod-eu\nList the clusters with: refresh cluster list -r us-east-1"
	if err == nil {
		t.Fatal("no error")
	}
	if diff := cmp.Diff(want, err.Error()); diff != "" {
		t.Errorf("error (-want +got):\n%s", diff)
	}

	// Without an argument the cluster comes from the environment.
	e = newDescribeFake(t)
	t.Setenv(cmdutil.EnvCluster, "prod-eu")
	if err := e.Run(cmdutiltest.Context(t), "cluster", "describe", "-o", "plain"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(e.Out.String(), "overview.version\t1.34\n") {
		t.Errorf("stdout is not the cluster of %s:\n%s", cmdutil.EnvCluster, e.Out.String())
	}

	e = newDescribeFake(t)
	if err := e.Run(cmdutiltest.Context(t), "cluster", "describe", "a", "b"); err == nil || len(e.Server.Requests()) != 0 {
		t.Errorf("two arguments: error = %v, requests = %v", err, e.Operations())
	}
}

// A deadline while the nodegroups are read is a partial result, exit 4.
func TestDescribe_Deadline(t *testing.T) {
	e := newDescribeFake(t)
	ctx, expire := cmdutiltest.Deadline(cmdutiltest.Context(t))
	e.Server.OnRequest = func(hctx context.Context, r awsxtest.Request) {
		if r.Operation == fleettest.EKSDescribeNodegroup {
			expire()
			<-hctx.Done()
		}
	}
	// One request slot: the calls go out one at a time, in the order that
	// they asked for the slot. The cluster asked before any call of the
	// second wave, so it is answered before the run is cut short.
	err := e.Run(ctx, "cluster", "describe", "prod", "-o", "json", "--max-concurrency", "1")
	if got := cmdutil.ExitCode(err); got != cmdutil.ExitIncomplete {
		t.Fatalf("exit code = %d (%v), want %d", got, err, cmdutil.ExitIncomplete)
	}
	cmdutiltest.ValidateJSON(t, apidoc.KindClusterDescription, e.Out.Bytes())
}
