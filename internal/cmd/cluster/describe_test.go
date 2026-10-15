package cluster_test

import (
	"context"
	"strings"
	"sync"
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
		s.Metrics = controlPlaneMetrics("prod", 1<<30, 48210, 5, 0)
		s.ClusterVersions = []fleettest.ClusterVersion{
			{Version: "1.33", EndOfStandardSupport: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), EndOfExtendedSupport: time.Date(2027, 12, 1, 0, 0, 0, 0, time.UTC)},
		}
	})
	return cmdtest.NewFake(t, world, east)
}

// controlPlaneMetrics returns the AWS/EKS metrics of a cluster: the etcd
// size in bytes, and the request, 5xx, and 429 counts of the window.
func controlPlaneMetrics(cluster string, etcd, requests, errors5xx, throttled float64) []fleettest.Metric {
	var out []fleettest.Metric
	for name, value := range map[string]float64{
		"etcd_mvcc_db_total_size_in_use_in_bytes": etcd, "apiserver_request_total": requests,
		"apiserver_request_total_5XX": errors5xx, "apiserver_request_total_429": throttled,
	} {
		out = append(out, fleettest.Metric{
			Region: east, Namespace: "AWS/EKS", Name: name, Dimensions: map[string]string{"ClusterName": cluster}, Values: []float64{value},
		})
	}
	return out
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

// "cluster describe" is three round trips, the third for the health
// checks, and no call lists clusters.
func TestDescribe_Waves(t *testing.T) {
	e := newDescribeFake(t)
	gate := awsxtest.NewGate(t, e.Server.Handler)
	done := make(chan error, 1)
	go func() { done <- e.Run(cmdutiltest.Context(t), "cluster", "describe", "prod", "-o", "json") }()
	gate.Wave(fleettest.EKSDescribeCluster, fleettest.EKSListNodegroups, fleettest.EKSListAddons, fleettest.EKSDescribeClusterVersions)
	gate.Wave(fleettest.EKSDescribeNodegroup, fleettest.EKSDescribeNodegroup, fleettest.EKSDescribeAddon, fleettest.EKSDescribeAddon, fleettest.EC2DescribeVpcs)
	// The control-plane metrics, the quota, and its usage. The nodegroups
	// of this cluster have no Auto Scaling group, so the capacity check
	// has nothing to read.
	gate.Wave(fleettest.CloudWatchGetMetricData, fleettest.CloudWatchGetMetricData, fleettest.ServiceQuotasGetServiceQuota)
	gate.Done()
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	if n := len(e.Server.Requests()); n != 12 {
		t.Errorf("requests = %v, want 12", e.Operations())
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
// It passes when the nodegroups are at the server and the cluster is read.
// The VPC call says that the cluster is read: the command sends it only
// after the answer of the cluster call. The nodegroup calls wait for it.
func TestDescribe_Deadline(t *testing.T) {
	e := newDescribeFake(t)
	ctx, expire := awsxtest.Deadline(cmdutiltest.Context(t))
	vpcSent := make(chan struct{})
	var once sync.Once
	e.Server.OnRequest = func(hctx context.Context, r awsxtest.Request) {
		switch r.Operation {
		case fleettest.EC2DescribeVpcs:
			once.Do(func() { close(vpcSent) })
		case fleettest.EKSDescribeNodegroup:
			select {
			case <-vpcSent:
				expire()
			case <-hctx.Done():
			}
			<-hctx.Done()
		}
	}
	err := e.Run(ctx, "cluster", "describe", "prod", "-o", "json")
	if got := cmdutil.ExitCode(err); got != cmdutil.ExitIncomplete {
		t.Fatalf("exit code = %d (%v), want %d", got, err, cmdutil.ExitIncomplete)
	}
	cmdutiltest.ValidateJSON(t, apidoc.KindClusterDescription, e.Out.Bytes())
}

// The help of "cluster describe" says what its HEALTH section does not
// show: upgrade readiness is the job of "cluster upgrade-check".
func TestDescribe_HelpNamesUpgradeCheckForInsights(t *testing.T) {
	e := cmdtest.NewFake(t, fleettest.New(), east)
	if err := e.Run(cmdutiltest.Context(t), "cluster", "describe", "--help"); err != nil {
		t.Fatal(err)
	}
	help := strings.Join(strings.Fields(e.Out.String()), " ")
	if want := `it reads no upgrade insight and no version skew, so an insight in ERROR shows in "cluster upgrade-check" and not here`; !strings.Contains(help, want) {
		t.Errorf("the help lacks %q:\n%s", want, e.Out)
	}
}
