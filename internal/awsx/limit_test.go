package awsx_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/service/eks"

	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// limited returns a provider that reaches srv with a request limit, and
// with backoff in place of the delay between attempts when it is not nil.
func limited(srv *awsxtest.Server, limit int, backoff func()) *awsx.Provider {
	return awsx.NewProvider(srv.Config(region), func(o *awsx.ProviderOptions) {
		o.MaxInFlight = limit
		o.Backoff = retry.BackoffDelayerFunc(func(int, error) (time.Duration, error) {
			if backoff != nil {
				backoff()
			}
			return 0, nil
		})
	})
}

func describe(ctx context.Context, c *awsx.Clients) error {
	_, err := c.EKS.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String("prod")})
	return err
}

// holdUntil makes the server hold every request until n are in flight, and
// lets everything through from then on. With a limit of n the requests can
// reach n and must not pass it.
func holdUntil(srv *awsxtest.Server, n int) {
	var (
		mu      sync.Mutex
		waiting int
	)
	reached := make(chan struct{})
	srv.OnRequest = func(ctx context.Context, _ awsxtest.Request) {
		mu.Lock()
		waiting++
		if waiting == n {
			close(reached)
		}
		mu.Unlock()
		select {
		case <-reached:
		case <-ctx.Done():
		}
	}
}

// The clients of one region share one limit, whatever their policy, and
// the requests in flight reach the limit and never pass it.
func TestRequestLimit_OneLimitForARegion(t *testing.T) {
	_, srv, _ := newServer(t)
	const limit = 4
	holdUntil(srv, limit)
	provider := limited(srv, limit, nil)
	ctx := testContext(t)

	sets := []*awsx.Clients{
		provider.Clients(region),
		provider.SweepClients(region).Clients,
		provider.PollClients(region).Clients,
	}
	var wg sync.WaitGroup
	for i := range 24 {
		wg.Go(func() {
			if err := describe(ctx, sets[i%len(sets)]); err != nil {
				t.Errorf("DescribeCluster: %v", err)
			}
		})
	}
	wg.Wait()
	if got := srv.PeakConcurrency("", region); got != limit {
		t.Errorf("peak requests in flight = %d, want %d", got, limit)
	}
}

// Another region has its own limit.
func TestRequestLimit_PerRegion(t *testing.T) {
	_, srv, _ := newServer(t)
	const limit = 2
	// Both regions must be at their limit at the same time.
	holdUntil(srv, 2*limit)
	provider := limited(srv, limit, nil)
	ctx := testContext(t)

	var wg sync.WaitGroup
	for _, r := range []string{region, "eu-west-1"} {
		for range 6 {
			wg.Go(func() {
				// The cluster answers in every region.
				if err := describe(ctx, provider.Clients(r)); err != nil {
					t.Errorf("DescribeCluster in %s: %v", r, err)
				}
			})
		}
	}
	wg.Wait()
	for _, r := range []string{region, "eu-west-1"} {
		if got := srv.PeakConcurrency("", r); got != limit {
			t.Errorf("peak requests in flight in %s = %d, want %d", r, got, limit)
		}
	}
}

// occupy sends one request that takes the only slot and holds it until the
// returned function is called. It returns once the request is at the
// server.
func occupy(t *testing.T, srv *awsxtest.Server, provider *awsx.Provider) (release func()) {
	t.Helper()
	atServer := make(chan struct{})
	hold := make(chan struct{})
	var once sync.Once
	srv.OnRequest = func(ctx context.Context, _ awsxtest.Request) {
		once.Do(func() { close(atServer) })
		select {
		case <-hold:
		case <-ctx.Done():
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = describe(testContext(t), provider.Clients(region))
	}()
	<-atServer
	return func() {
		close(hold)
		<-done
	}
}

// A call that gets no slot before its context ends was never sent.
func TestRequestLimit_NotSent(t *testing.T) {
	_, srv, _ := newServer(t)
	provider := limited(srv, 1, nil)
	release := occupy(t, srv, provider)
	defer release()

	ctx, cancel := context.WithTimeout(testContext(t), 50*time.Millisecond)
	defer cancel()
	err := describe(ctx, provider.Clients(region))
	if !errors.Is(err, awsx.ErrNotSent) {
		t.Fatalf("error = %v, want ErrNotSent in its chain", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want the context error in its chain", err)
	}
	if got := awsx.Classify(err); got != awsx.ClassNotAttempted {
		t.Errorf("Classify = %s, want %s", got, awsx.ClassNotAttempted)
	}
	if got := count(srv, fleettest.EKSDescribeCluster); got != 1 {
		t.Errorf("DescribeCluster requests = %d, want 1: only the one that holds the slot", got)
	}
}

// A retry that gets no slot before the context ends reports the context
// error and not "not sent": the first attempt did reach AWS.
func TestRequestLimit_RetryWithoutSlotIsNotUnsent(t *testing.T) {
	world, srv, _ := newServer(t)
	world.SetFault(fleettest.EKSDescribeCluster, fleettest.Fault{Throttle: 1})

	var provider *awsx.Provider
	var release func()
	// The backoff runs after the throttled attempt gave its slot back. It
	// lets another request take the slot before the retry asks for it.
	provider = limited(srv, 1, func() { release = occupy(t, srv, provider) })
	defer func() {
		if release != nil {
			release()
		}
	}()

	ctx, cancel := context.WithTimeout(testContext(t), 200*time.Millisecond)
	defer cancel()
	err := describe(ctx, provider.Clients(region))
	if errors.Is(err, awsx.ErrNotSent) {
		t.Fatalf("error = %v: a call whose first attempt was answered is not \"not sent\"", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want the deadline of the context", err)
	}
	if got := awsx.Classify(err); got != awsx.ClassTimeout {
		t.Errorf("Classify = %s, want %s", got, awsx.ClassTimeout)
	}
}

// A call holds no slot while it backs off: another call runs in between.
func TestRequestLimit_SlotIsFreeDuringBackoff(t *testing.T) {
	world, srv, _ := newServer(t)
	world.SetFault(fleettest.EKSListClusters, fleettest.Fault{Throttle: 1})

	var provider *awsx.Provider
	var between error
	provider = limited(srv, 1, func() {
		ctx, cancel := context.WithTimeout(testContext(t), 5*time.Second)
		defer cancel()
		between = describe(ctx, provider.Clients(region))
	})
	if _, err := provider.Clients(region).EKS.ListClusters(testContext(t), &eks.ListClustersInput{}); err != nil {
		t.Fatalf("ListClusters: %v", err)
	}
	if between != nil {
		t.Errorf("the call during the backoff failed: %v", between)
	}
}
