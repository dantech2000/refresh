package aws

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
)

func TestNodegroupK8sVersion(t *testing.T) {
	if got := NodegroupK8sVersion(&types.Nodegroup{Version: aws.String("1.31")}, "1.32"); got != "1.31" {
		t.Errorf("nodegroup version: got %q, want 1.31", got)
	}
	if got := NodegroupK8sVersion(&types.Nodegroup{}, "1.32"); got != "1.32" {
		t.Errorf("empty nodegroup version: got %q, want cluster fallback 1.32", got)
	}
	if got := NodegroupK8sVersion(nil, "1.32"); got != "1.32" {
		t.Errorf("nil nodegroup: got %q, want cluster fallback 1.32", got)
	}
}

func TestLatestAMICache_KeysByVersionAndTypeAndDedupesConcurrentLookups(t *testing.T) {
	var calls atomic.Int32
	cache := NewLatestAMICache(func(_ context.Context, v string, at types.AMITypes) string {
		calls.Add(1)
		return "ami-" + v + "-" + string(at)
	})

	ctx := context.Background()
	const want131 = "ami-1.31-AL2023_x86_64_STANDARD"
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := cache.Get(ctx, "1.31", types.AMITypesAl2023X8664Standard); got != want131 {
				t.Errorf("Get = %q, want %q", got, want131)
			}
		}()
	}
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("lookups for one key = %d, want 1", n)
	}

	ng131 := &types.Nodegroup{Version: aws.String("1.31"), AmiType: types.AMITypesAl2023X8664Standard}
	ng132 := &types.Nodegroup{Version: aws.String("1.32"), AmiType: types.AMITypesAl2023X8664Standard}
	if got := cache.ForNodegroup(ctx, ng131, "1.32"); got != want131 {
		t.Errorf("ForNodegroup(1.31) = %q, want %q", got, want131)
	}
	if got := cache.ForNodegroup(ctx, ng132, "1.32"); got != "ami-1.32-AL2023_x86_64_STANDARD" {
		t.Errorf("ForNodegroup(1.32) = %q", got)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("total lookups = %d, want 2 (one per distinct version)", n)
	}
}
