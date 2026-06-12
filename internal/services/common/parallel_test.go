package common

import (
	"context"
	"sync/atomic"
	"testing"
)

func TestForEachParallel_ReturnsInputOrder(t *testing.T) {
	items := []int{1, 2, 3, 4, 5}
	got := ForEachParallel(context.Background(), items, 2, func(_ context.Context, n int) int {
		return n * 10
	})
	for i, n := range items {
		if got[i] != n*10 {
			t.Errorf("result[%d] = %d, want %d", i, got[i], n*10)
		}
	}
}

// REF-56: a cancelled context must stop dispatching new work; unstarted items
// keep their zero value rather than the fn being invoked for all of them.
func TestForEachParallel_StopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before dispatch

	var calls int32
	items := make([]int, 100)
	got := ForEachParallel(ctx, items, 1, func(_ context.Context, _ int) int {
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
