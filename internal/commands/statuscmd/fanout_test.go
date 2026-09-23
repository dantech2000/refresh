package statuscmd

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	statussvc "github.com/dantech2000/refresh/internal/services/status"
)

// blockingRegion counts concurrent sweeps and records the per-region cluster
// concurrency it was given, then waits for release or ctx.
type blockingRegion struct {
	region  string
	live    *atomic.Int32
	peak    *atomic.Int32
	gotConc *sync.Map
	release <-chan struct{}
}

func (b blockingRegion) ListClusterStatuses(ctx context.Context, opts statussvc.ListOptions) ([]statussvc.ClusterStatus, error) {
	b.gotConc.Store(b.region, opts.MaxConcurrency)
	n := b.live.Add(1)
	defer b.live.Add(-1)
	for {
		p := b.peak.Load()
		if n <= p || b.peak.CompareAndSwap(p, n) {
			break
		}
	}
	select {
	case <-b.release:
		return []statussvc.ClusterStatus{{Name: "c-" + b.region, Region: b.region}}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func manyRegions(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("region-%02d", i)
	}
	return out
}

// Regions are capped at regionConcurrency no matter what --max-concurrency
// is; --max-concurrency goes to each region's cluster fan-out unchanged, and
// rows come back in region order.
func TestGatherFleet_CapsRegionsAndPassesClusterConcurrency(t *testing.T) {
	var live, peak atomic.Int32
	var gotConc sync.Map
	release := make(chan struct{})
	stubRegionService(t, func(cfg aws.Config) regionLister {
		return blockingRegion{region: cfg.Region, live: &live, peak: &peak, gotConc: &gotConc, release: release}
	})
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(release)
	}()

	regions := manyRegions(12)
	sweep := gatherFleet(context.Background(), aws.Config{}, regions, statussvc.ListOptions{MaxConcurrency: 32}, false)

	if p := peak.Load(); p > regionConcurrency {
		t.Errorf("peak concurrent regions = %d, want <= %d", p, regionConcurrency)
	}
	if len(sweep.errs) != 0 || len(sweep.statuses) != len(regions) {
		t.Fatalf("got %d statuses / %v errors, want %d / none", len(sweep.statuses), sweep.errs, len(regions))
	}
	for i, st := range sweep.statuses {
		if st.Region != regions[i] {
			t.Errorf("statuses[%d].Region = %s, want %s (region order)", i, st.Region, regions[i])
		}
	}
	gotConc.Range(func(k, v any) bool {
		if v.(int) != 32 {
			t.Errorf("region %v got cluster concurrency %v, want 32", k, v)
		}
		return true
	})
}

// -C 1 (set to avoid throttling) sweeps one region at a time.
func TestGatherFleet_MaxConcurrencyOneIsSerial(t *testing.T) {
	var live, peak atomic.Int32
	var gotConc sync.Map
	release := make(chan struct{})
	close(release)
	stubRegionService(t, func(cfg aws.Config) regionLister {
		return blockingRegion{region: cfg.Region, live: &live, peak: &peak, gotConc: &gotConc, release: release}
	})

	regions := manyRegions(6)
	sweep := gatherFleet(context.Background(), aws.Config{}, regions, statussvc.ListOptions{MaxConcurrency: 1}, false)
	if p := peak.Load(); p != 1 {
		t.Errorf("peak concurrent regions = %d, want 1", p)
	}
	if len(sweep.statuses) != len(regions) || len(sweep.errs) != 0 {
		t.Fatalf("got %d statuses / %v errors", len(sweep.statuses), sweep.errs)
	}
}

func TestRegionFanout(t *testing.T) {
	for in, want := range map[int]int{0: 4, -1: 4, 1: 1, 3: 3, 4: 4, 8: 4, 64: 4} {
		if got := regionFanout(in); got != want {
			t.Errorf("regionFanout(%d) = %d, want %d", in, got, want)
		}
	}
}

// A cancelled context must not leave gatherFleet blocked on the semaphore, and
// every region it never reached counts as failed.
func TestGatherFleet_CancelDoesNotBlock(t *testing.T) {
	var live, peak atomic.Int32
	var gotConc sync.Map
	stubRegionService(t, func(cfg aws.Config) regionLister {
		return blockingRegion{region: cfg.Region, live: &live, peak: &peak, gotConc: &gotConc, release: make(chan struct{})}
	})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)

	regions := manyRegions(10)
	done := make(chan fleetSweep, 1)
	go func() { done <- gatherFleet(ctx, aws.Config{}, regions, statussvc.ListOptions{}, false) }()

	select {
	case sweep := <-done:
		if len(sweep.errs) != len(regions) {
			t.Fatalf("got %d region errors, want %d (every region failed or not queried)", len(sweep.errs), len(regions))
		}
		for _, e := range sweep.errs {
			if !errors.Is(e, context.Canceled) {
				t.Errorf("region error %v should wrap context.Canceled", e)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("gatherFleet did not return after cancel")
	}
}
