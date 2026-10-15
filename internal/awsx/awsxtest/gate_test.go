package awsxtest_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// gateServer starts a fake AWS with one cluster and returns it with an EKS
// client that reaches it.
func gateServer(t *testing.T) (*awsxtest.Server, *eks.Client) {
	t.Helper()
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) {
		s.Clusters = append(s.Clusters, &fleettest.Cluster{Name: "prod", Version: "1.33"})
	})
	srv := awsxtest.NewServer(world)
	t.Cleanup(srv.Close)
	return srv, eks.NewFromConfig(srv.Config("us-east-1"))
}

// gateRecorder is a testing.TB that records a failure in place of stopping the
// test, so that a test can check what the gate reports.
type gateRecorder struct {
	testing.TB
	mu       sync.Mutex
	failures []string
	cleanups []func()
}

func newGateRecorder(t *testing.T) *gateRecorder {
	t.Helper()
	return &gateRecorder{TB: t}
}

func (r *gateRecorder) Helper() {}

func (r *gateRecorder) Cleanup(f func()) { r.cleanups = append(r.cleanups, f) }

func (r *gateRecorder) Errorf(format string, args ...any) { r.record(format, args...) }

// Fatalf records the failure and stops the goroutine, as testing.T does.
func (r *gateRecorder) Fatalf(format string, args ...any) {
	r.record(format, args...)
	panic(r)
}

func (r *gateRecorder) record(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

func (r *gateRecorder) all() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.failures, "\n")
}

// wave runs one Wave and reports whether it failed the test.
func gateWave(g *awsxtest.Gate, ops ...fleettest.Operation) (failed bool) {
	defer func() {
		if recover() != nil {
			failed = true
		}
	}()
	g.Wave(ops...)
	return false
}

// Two calls that start together are one wave, and a call that needs the
// answer of another is the next wave.
func TestGate_Waves(t *testing.T) {
	srv, client := gateServer(t)
	gate := awsxtest.NewGate(t, srv.Handler)
	ctx := t.Context()

	done := make(chan error, 1)
	go func() {
		var wg sync.WaitGroup
		wg.Go(func() { _, _ = client.ListClusters(ctx, &eks.ListClustersInput{}) })
		wg.Go(func() { _, _ = client.ListNodegroups(ctx, &eks.ListNodegroupsInput{ClusterName: aws.String("prod")}) })
		wg.Wait()
		_, err := client.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String("prod")})
		done <- err
	}()

	gate.Wave(fleettest.EKSListNodegroups, fleettest.EKSListClusters)
	gate.Wave(fleettest.EKSDescribeCluster)
	gate.Done()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// A call that the product makes in series is not in the wave: Wave fails
// at its deadline and names what it holds and what it still expects.
func TestGate_SerialCallFailsTheWave(t *testing.T) {
	srv, client := gateServer(t)
	rec := newGateRecorder(t)
	gate := awsxtest.NewGate(rec, srv.Handler)
	gate.Timeout = 100 * time.Millisecond
	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := client.ListClusters(ctx, &eks.ListClustersInput{}); err != nil {
			return
		}
		_, _ = client.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String("prod")})
	}()

	if !gateWave(gate, fleettest.EKSListClusters, fleettest.EKSDescribeCluster) {
		t.Fatal("Wave passed for two calls that run one after the other")
	}
	cancel()
	<-done
	got := rec.all()
	for _, want := range []string{"eks:ListClusters", "eks:DescribeCluster", "still expects"} {
		if !strings.Contains(got, want) {
			t.Errorf("the failure %q does not name %s", got, want)
		}
	}
}

// A request that is not in the wave fails it at once.
func TestGate_RequestOutsideTheWave(t *testing.T) {
	srv, client := gateServer(t)
	rec := newGateRecorder(t)
	gate := awsxtest.NewGate(rec, srv.Handler)
	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = client.ListAddons(ctx, &eks.ListAddonsInput{ClusterName: aws.String("prod")})
	}()
	if !gateWave(gate, fleettest.EKSListClusters) {
		t.Fatal("Wave passed with a request that it did not name")
	}
	cancel()
	<-done
	if got := rec.all(); !strings.Contains(got, "outside the wave") || !strings.Contains(got, "eks:ListAddons") {
		t.Errorf("the failure %q does not name the request outside the wave", got)
	}
}

// A request after Done fails the test when it ends.
func TestGate_RequestAfterDone(t *testing.T) {
	srv, client := gateServer(t)
	rec := newGateRecorder(t)
	gate := awsxtest.NewGate(rec, srv.Handler)
	gate.Done()
	if _, err := client.ListClusters(t.Context(), &eks.ListClustersInput{}); err != nil {
		t.Fatal(err)
	}
	for _, cleanup := range rec.cleanups {
		cleanup()
	}
	if got := rec.all(); !strings.Contains(got, "after the last wave") {
		t.Errorf("failures = %q, want one for the request after the last wave", got)
	}
}
