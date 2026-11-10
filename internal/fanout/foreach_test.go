package fanout

import (
	"context"
	"sync/atomic"
	"testing"

	"go.uber.org/goleak"
)

func TestForEach_ReturnsInputOrder(t *testing.T) {
	items := []int{1, 2, 3, 4, 5}
	got := ForEach(context.Background(), items, 2, func(_ context.Context, n int) int {
		return n * 10
	})
	for i, n := range items {
		if got[i] != n*10 {
			t.Errorf("result[%d] = %d, want %d", i, got[i], n*10)
		}
	}
}

// A cancelled context must stop dispatching new work; unstarted items
// keep their zero value rather than the fn being invoked for all of them.
func TestForEach_StopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before dispatch

	var calls int32
	items := make([]int, 100)
	got := ForEach(ctx, items, 1, func(_ context.Context, _ int) int {
		atomic.AddInt32(&calls, 1)
		return 1
	})

	if len(got) != len(items) {
		t.Fatalf("result length = %d, want %d", len(got), len(items))
	}
	// With maxConcurrency=1 and a pre-cancelled ctx, the dispatch loop should
	// bail almost immediately rather than invoking fn for all 100 items.
	if n := atomic.LoadInt32(&calls); n >= int32(len(items)) {
		t.Errorf("fn called %d times; expected the cancel to stop dispatch early", n)
	}
}

// With ctx already cancelled, a free slot and ctx.Done are both ready, and
// select picks at random. ForEach must never start an item anyway.
func TestForEach_NeverDispatchesAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for range 200 {
		var started atomic.Int32
		ForEach(ctx, []int{1, 2, 3, 4, 5, 6, 7, 8}, 8, func(context.Context, int) int {
			started.Add(1)
			return 0
		})
		if n := started.Load(); n != 0 {
			t.Fatalf("started %d items with a cancelled context, want 0", n)
		}
	}
}

// Cancelling mid-run must not leave a worker behind: ForEach returns
// only after every started item has returned, even when the rest are never
// dispatched.
func TestForEach_CancelMidRunJoinsWorkers(t *testing.T) {
	defer goleak.VerifyNone(t)
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{}, 2)
	items := make([]int, 10)
	done := make(chan []int, 1)
	go func() {
		done <- ForEach(ctx, items, 2, func(ctx context.Context, _ int) int {
			started <- struct{}{}
			<-ctx.Done()
			return 1
		})
	}()
	<-started
	<-started
	cancel()
	got := <-done
	ran := 0
	for _, r := range got {
		ran += r
	}
	if ran != 2 {
		t.Errorf("items run = %d, want 2 (only the in-flight pair)", ran)
	}
}

// ForEachIn reports how many items it started, and the items that it did
// not start are the suffix of the list, with the zero value.
func TestForEachIn_Started(t *testing.T) {
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	tests := []struct {
		name        string
		ctx         context.Context
		sem         chan struct{}
		wantStarted int
	}{
		{name: "no limit", ctx: t.Context(), sem: nil, wantStarted: 5},
		{name: "a shared limit", ctx: t.Context(), sem: make(chan struct{}, 2), wantStarted: 5},
		{name: "cancelled before the start", ctx: cancelled, sem: nil, wantStarted: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, started := ForEachIn(tt.ctx, tt.sem, []int{1, 2, 3, 4, 5}, func(_ context.Context, n int) int { return n })
			if started != tt.wantStarted {
				t.Fatalf("started = %d, want %d", started, tt.wantStarted)
			}
			for i, r := range got {
				want := 0
				if i < started {
					want = i + 1
				}
				if r != want {
					t.Errorf("result[%d] = %d, want %d", i, r, want)
				}
			}
		})
	}
}

// When ctx ends while the first items hold every slot, the count is the
// number of items in flight, and nothing after them started.
func TestForEachIn_StartedAfterCancelMidRun(t *testing.T) {
	defer goleak.VerifyNone(t)
	ctx, cancel := context.WithCancel(t.Context())
	inFlight := make(chan struct{}, 2)
	type out struct {
		results []int
		started int
	}
	done := make(chan out, 1)
	go func() {
		r, n := ForEachIn(ctx, make(chan struct{}, 2), make([]int, 10), func(ctx context.Context, _ int) int {
			inFlight <- struct{}{}
			<-ctx.Done()
			return 1
		})
		done <- out{r, n}
	}()
	<-inFlight
	<-inFlight
	cancel()
	got := <-done
	if got.started != 2 {
		t.Fatalf("started = %d, want 2", got.started)
	}
	for i, r := range got.results {
		want := 0
		if i < 2 {
			want = 1
		}
		if r != want {
			t.Errorf("result[%d] = %d, want %d", i, r, want)
		}
	}
}
