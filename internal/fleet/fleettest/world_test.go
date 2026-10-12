package fleettest_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

const region = "us-east-1"

// newWorld returns a world with one cluster, one nodegroup, and one add-on.
func newWorld() *fleettest.World {
	w := fleettest.New()
	w.Edit(func(s *fleettest.State) {
		s.Clusters = []*fleettest.Cluster{{
			Name: "prod", Version: "1.32",
			Nodegroups: []*fleettest.Nodegroup{{Name: "ng-a", Version: "1.31"}},
			Addons:     []*fleettest.Addon{{Name: "coredns", Version: "v1"}},
		}}
	})
	return w
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestEditAppliesDefaults(t *testing.T) {
	w := newWorld()
	var got fleettest.Cluster
	var ng fleettest.Nodegroup
	w.View(func(s *fleettest.State) {
		c := s.Cluster(region, "prod")
		got, ng = *c, *c.Nodegroups[0]
	})

	want := fleettest.Cluster{
		Name: "prod", Version: "1.32", Status: "ACTIVE", PlatformVersion: "eks.1", CreatedAt: w.Now(),
		Insights: []fleettest.Insight{{ID: "insight-prod-skew", Name: "Kubelet version skew", Category: "UPGRADE_READINESS", Status: "PASSING"}},
	}
	if diff := cmp.Diff(want, got, cmpopts.IgnoreFields(fleettest.Cluster{}, "Nodegroups", "Addons")); diff != "" {
		t.Errorf("cluster defaults (-want +got):\n%s", diff)
	}
	wantNG := fleettest.Nodegroup{
		Name: "ng-a", Version: "1.31", ReleaseVersion: "1.31.0-20260101", Status: "ACTIVE", CreatedAt: w.Now(), ModifiedAt: w.Now(),
		AMIType: "AL2023_x86_64_STANDARD", CapacityType: "ON_DEMAND", InstanceTypes: []string{"m5.large"},
		Desired: 2, Min: 1, Max: 3,
	}
	if diff := cmp.Diff(wantNG, ng); diff != "" {
		t.Errorf("nodegroup defaults (-want +got):\n%s", diff)
	}
}

func TestClusterRegion(t *testing.T) {
	w := fleettest.New()
	w.Edit(func(s *fleettest.State) {
		s.Clusters = []*fleettest.Cluster{{Name: "everywhere"}, {Name: "west", Region: "us-west-2"}}
	})
	tests := []struct {
		region, name string
		want         bool
	}{
		{"us-east-1", "everywhere", true},
		{"us-west-2", "everywhere", true},
		{"us-west-2", "west", true},
		{"us-east-1", "west", false},
		{"us-east-1", "missing", false},
	}
	for _, tt := range tests {
		w.View(func(s *fleettest.State) {
			if got := s.Cluster(tt.region, tt.name) != nil; got != tt.want {
				t.Errorf("Cluster(%q, %q) found = %t, want %t", tt.region, tt.name, got, tt.want)
			}
		})
	}
}

func TestParameter(t *testing.T) {
	w := fleettest.New()
	w.Edit(func(s *fleettest.State) {
		s.Parameters = []fleettest.Parameter{
			{Name: "/custom/1.30/image_id", Value: "ami-custom"},
			{Region: "us-west-2", Name: "/west", Value: "ami-west"},
			{Region: "us-west-2", Name: "/both", Value: "ami-west"},
			{Region: region, Name: "/both", Value: "ami-east"},
		}
	})
	tests := []struct {
		name   string
		want   string
		wantOK bool
	}{
		{"/custom/1.30/image_id", "ami-custom", true},
		{"/west", "", false},
		{"/both", "ami-east", true},
		{"/aws/service/eks/optimized-ami/1.32/amazon-linux-2023/x86_64/standard/recommended/image_id", "ami-latest132", true},
		{"/aws/service/eks/optimized-ami/1.32/amazon-linux-2023/x86_64/standard/recommended/release_version", "1.32.0-20260101", true},
		{"/aws/service/bottlerocket/aws-k8s-1.34-nvidia/x86_64/latest/image_id", "ami-latest134", true},
		{"/aws/service/bottlerocket/aws-k8s-1.34/x86_64/latest/image_version", "1.34.0-20260101", true},
		{"/aws/service/ami-windows-latest/Windows_Server-2022-English-Core-EKS_Optimized-1.33/image_id", "", false},
		{"/no/version/here", "", false},
	}
	for _, tt := range tests {
		w.View(func(s *fleettest.State) {
			got, ok := s.Parameter(region, tt.name)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("Parameter(%q) = %q, %t; want %q, %t", tt.name, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestEnterFaults(t *testing.T) {
	op := fleettest.Call{Operation: fleettest.EKSListClusters}
	ctx := testContext(t)
	kind := func(err error) fleettest.FaultKind {
		var f *fleettest.FaultError
		if errors.As(err, &f) {
			return f.Kind
		}
		return 0
	}

	t.Run("no fault", func(t *testing.T) {
		w := fleettest.New()
		if a, err := w.Enter(ctx, op); a != (fleettest.Admission{}) || err != nil {
			t.Errorf("Enter = %+v, %v; want a zero admission", a, err)
		}
	})
	t.Run("throttle then page size", func(t *testing.T) {
		w := fleettest.New()
		w.SetFault(op.Operation, fleettest.Fault{Throttle: 2, PageSize: 3})
		for i := range 2 {
			if _, err := w.Enter(ctx, op); kind(err) != fleettest.Throttled {
				t.Errorf("request %d: err = %v, want a throttle", i+1, err)
			}
		}
		if a, err := w.Enter(ctx, op); a.PageSize != 3 || err != nil {
			t.Errorf("after the throttles: Enter = %+v, %v; want page size 3", a, err)
		}
		if _, err := w.Enter(ctx, fleettest.Call{Operation: fleettest.EKSDescribeCluster}); err != nil {
			t.Errorf("another operation: %v", err)
		}
	})
	t.Run("deny", func(t *testing.T) {
		w := fleettest.New()
		w.SetFault(op.Operation, fleettest.Fault{Deny: true})
		for range 2 {
			if _, err := w.Enter(ctx, op); kind(err) != fleettest.Denied {
				t.Errorf("err = %v, want a denial", err)
			}
		}
		w.SetFault(op.Operation, fleettest.Fault{})
		if _, err := w.Enter(ctx, op); err != nil {
			t.Errorf("after the fault was removed: %v", err)
		}
	})
	t.Run("code", func(t *testing.T) {
		w := fleettest.New()
		w.SetFault(op.Operation, fleettest.Fault{Code: "Teapot", Status: 418})
		var f *fleettest.FaultError
		if _, err := w.Enter(ctx, op); !errors.As(err, &f) || *f != (fleettest.FaultError{Kind: fleettest.Coded, Code: "Teapot", Status: 418}) {
			t.Errorf("err = %v, want the coded fault", err)
		}
	})
}

// TestScopedFaults checks which fault applies to a request when several
// match, and that each fault keeps its own counters.
func TestScopedFaults(t *testing.T) {
	ctx := testContext(t)
	kindOf := func(w *fleettest.World, c fleettest.Call) fleettest.FaultKind {
		_, err := w.Enter(ctx, c)
		var f *fleettest.FaultError
		if errors.As(err, &f) {
			return f.Kind
		}
		return 0
	}
	describe := func(region, cluster, nodegroup string) fleettest.Call {
		return fleettest.Call{Operation: fleettest.EKSDescribeNodegroup, Region: region, Cluster: cluster, Nodegroup: nodegroup}
	}

	t.Run("the most specific scope wins", func(t *testing.T) {
		w := fleettest.New()
		// From general to specific. Each rule answers with its own kind.
		w.SetScopedFault(fleettest.Scope{}, fleettest.Fault{Reset: true})
		w.SetScopedFault(fleettest.Scope{Region: "eu-west-1"}, fleettest.Fault{Unauthenticated: true})
		w.SetScopedFault(fleettest.Scope{Service: "eks"}, fleettest.Fault{Deny: true})
		w.SetScopedFault(fleettest.Scope{Operation: fleettest.EKSDescribeNodegroup}, fleettest.Fault{Throttle: 100})
		w.SetScopedFault(fleettest.Scope{Cluster: "prod"}, fleettest.Fault{Code: "ForCluster"})
		w.SetScopedFault(fleettest.Scope{Nodegroup: "ng-a"}, fleettest.Fault{PageSize: 7})

		tests := []struct {
			name string
			call fleettest.Call
			want fleettest.FaultKind
		}{
			{"nothing but the global rule", fleettest.Call{Operation: fleettest.EC2DescribeVpcs, Region: "us-east-1"}, fleettest.ConnectionReset},
			{"region", fleettest.Call{Operation: fleettest.EC2DescribeVpcs, Region: "eu-west-1"}, fleettest.Unauthenticated},
			{"service beats region", fleettest.Call{Operation: fleettest.EKSListClusters, Region: "eu-west-1"}, fleettest.Denied},
			{"operation beats service", describe("eu-west-1", "dev", "ng-z"), fleettest.Throttled},
			{"cluster beats operation", describe("eu-west-1", "prod", "ng-z"), fleettest.Coded},
			{"nodegroup beats cluster", describe("eu-west-1", "prod", "ng-a"), 0},
		}
		for _, tt := range tests {
			if got := kindOf(w, tt.call); got != tt.want {
				t.Errorf("%s: fault kind %d, want %d", tt.name, got, tt.want)
			}
		}
		if a, err := w.Enter(ctx, describe("us-east-1", "prod", "ng-a")); a.PageSize != 7 || err != nil {
			t.Errorf("the nodegroup rule: Enter = %+v, %v; want page size 7", a, err)
		}
	})

	t.Run("each fault counts for itself", func(t *testing.T) {
		w := fleettest.New()
		w.SetFault(fleettest.EKSDescribeNodegroup, fleettest.Fault{Throttle: 1})
		w.SetScopedFault(fleettest.Scope{Operation: fleettest.EKSDescribeNodegroup, Cluster: "prod"}, fleettest.Fault{Throttle: 2})
		w.SetScopedFault(fleettest.Scope{Operation: fleettest.EKSDescribeNodegroup, Region: "eu-west-1"}, fleettest.Fault{Throttle: 1})

		var got []fleettest.FaultKind
		for _, c := range []fleettest.Call{
			describe("us-east-1", "prod", "ng-a"), describe("us-east-1", "prod", "ng-a"), describe("us-east-1", "prod", "ng-a"),
			describe("us-east-1", "dev", "ng-a"), describe("us-east-1", "dev", "ng-a"),
			describe("eu-west-1", "dev", "ng-a"), describe("eu-west-1", "dev", "ng-a"),
		} {
			got = append(got, kindOf(w, c))
		}
		want := []fleettest.FaultKind{fleettest.Throttled, fleettest.Throttled, 0, fleettest.Throttled, 0, fleettest.Throttled, 0}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("fault kinds (-want +got):\n%s", diff)
		}
	})

	t.Run("a zero fault removes only its scope", func(t *testing.T) {
		w := fleettest.New()
		closed := fleettest.Scope{Region: "eu-west-1"}
		w.SetScopedFault(closed, fleettest.Fault{Unauthenticated: true})
		w.SetFault(fleettest.EKSListClusters, fleettest.Fault{Deny: true})
		w.SetScopedFault(closed, fleettest.Fault{})
		if got := kindOf(w, fleettest.Call{Operation: fleettest.EC2DescribeVpcs, Region: "eu-west-1"}); got != 0 {
			t.Errorf("the removed region fault still answers kind %d", got)
		}
		if got := kindOf(w, fleettest.Call{Operation: fleettest.EKSListClusters, Region: "eu-west-1"}); got != fleettest.Denied {
			t.Errorf("the operation fault answers kind %d, want a denial", got)
		}
	})

	t.Run("lost responses are counted", func(t *testing.T) {
		w := fleettest.New()
		w.SetFault(fleettest.EKSUpdateAddon, fleettest.Fault{LoseResponse: 2})
		var got []bool
		for range 3 {
			a, err := w.Enter(ctx, fleettest.Call{Operation: fleettest.EKSUpdateAddon})
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, w.LoseResponse(a))
		}
		// A request that is let through and never asks, as one that the
		// operation refused, uses nothing up.
		if _, err := w.Enter(ctx, fleettest.Call{Operation: fleettest.EKSUpdateAddon}); err != nil {
			t.Fatal(err)
		}
		if w.LoseResponse(fleettest.Admission{}) {
			t.Error("a request with no fault lost its response")
		}
		if diff := cmp.Diff([]bool{true, true, false}, got); diff != "" {
			t.Errorf("LoseResponse per request (-want +got):\n%s", diff)
		}
	})
}

func TestEnterBlocks(t *testing.T) {
	op := fleettest.Call{Operation: fleettest.EKSListClusters}
	ctx := testContext(t)

	t.Run("delay ends when the clock advances", func(t *testing.T) {
		w := fleettest.New()
		w.SetFault(op.Operation, fleettest.Fault{Delay: time.Minute})
		// Three requests, to show that Advance wakes every one of them.
		var wg sync.WaitGroup
		errs := make(chan error, 3)
		for range 3 {
			wg.Go(func() {
				_, err := w.Enter(ctx, op)
				errs <- err
			})
		}
		if err := w.AwaitBlocked(ctx, 3); err != nil {
			t.Fatal(err)
		}
		w.Advance(59 * time.Second)
		if err := w.AwaitBlocked(ctx, 3); err != nil {
			t.Fatalf("a request left before its delay ended: %v", err)
		}
		w.Advance(time.Second)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Errorf("Enter after the delay: %v", err)
			}
		}
	})

	t.Run("hang ends with the context", func(t *testing.T) {
		w := fleettest.New()
		w.SetFault(op.Operation, fleettest.Fault{Hang: true})
		callCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := w.Enter(callCtx, op)
			done <- err
		}()
		if err := w.AwaitBlocked(ctx, 1); err != nil {
			t.Fatal(err)
		}
		w.Advance(24 * time.Hour)
		if err := w.AwaitBlocked(ctx, 1); err != nil {
			t.Fatalf("the clock released a hanging request: %v", err)
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("Enter = %v, want context.Canceled", err)
		}
		if err := w.AwaitBlocked(ctx, 0); err != nil {
			t.Error(err)
		}
	})

	t.Run("AwaitBlocked gives up with its context", func(t *testing.T) {
		w := fleettest.New()
		gone, cancel := context.WithCancel(ctx)
		cancel()
		if err := w.AwaitBlocked(gone, 1); !errors.Is(err, context.Canceled) {
			t.Errorf("AwaitBlocked = %v, want context.Canceled", err)
		}
	})
}
