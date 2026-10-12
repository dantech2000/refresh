package common

import (
	"context"
	"testing"
)

func TestPaginate_AggregatesPages(t *testing.T) {
	pages := [][]int{{1, 2}, {3, 4}, {5}}
	tokens := []string{"t1", "t2", ""}
	i := 0
	got, err := Paginate(context.Background(), func(_ context.Context, _ *string) ([]int, *string, error) {
		items := pages[i]
		var next *string
		if tokens[i] != "" {
			tok := tokens[i]
			next = &tok
		}
		i++
		return items, next, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d items, want 5: %v", len(got), got)
	}
}

// REF-56: a server echoing a non-advancing token must not loop forever.
func TestPaginate_StuckTokenBreaks(t *testing.T) {
	calls := 0
	stuck := "same-token"
	got, err := Paginate(context.Background(), func(_ context.Context, _ *string) ([]int, *string, error) {
		calls++
		if calls > 100 {
			t.Fatal("Paginate looped on a non-advancing token")
		}
		return []int{calls}, &stuck, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// First page returns token "same-token"; second page returns the same token
	// → must break. So exactly 2 calls, 2 items.
	if calls != 2 || len(got) != 2 {
		t.Errorf("calls=%d items=%d, want 2/2 (broke on stuck token)", calls, len(got))
	}
}

// REF-56: Paginate bails when the context is cancelled between pages.
func TestPaginate_CtxCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	_, err := Paginate(ctx, func(_ context.Context, _ *string) ([]int, *string, error) {
		calls++
		tok := "next"
		return []int{1}, &tok, nil
	})
	if err == nil {
		t.Fatal("expected context error, got nil")
	}
	if calls != 0 {
		t.Errorf("fetch called %d times after cancel, want 0", calls)
	}
}
