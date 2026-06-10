package aws

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakePage struct {
	items []string
	next  *string
}

func TestListAllPages_FollowsTokens(t *testing.T) {
	token2 := "page2"
	pages := map[string]fakePage{
		"":      {items: []string{"a", "b"}, next: &token2},
		"page2": {items: []string{"c"}, next: nil},
	}

	got, err := ListAllPages(context.Background(), "listing things",
		func(_ context.Context, token *string) (fakePage, error) {
			key := ""
			if token != nil {
				key = *token
			}
			return pages[key], nil
		},
		func(p fakePage) ([]string, *string) { return p.items, p.next },
	)
	if err != nil {
		t.Fatalf("ListAllPages() = %v", err)
	}
	if len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Errorf("items = %v, want [a b c]", got)
	}
}

func TestListAllPages_FormatsErrorWithOperation(t *testing.T) {
	_, err := ListAllPages(context.Background(), "listing widgets",
		func(_ context.Context, _ *string) (fakePage, error) {
			return fakePage{}, errors.New("boom")
		},
		func(p fakePage) ([]string, *string) { return p.items, p.next },
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "listing widgets") {
		t.Errorf("error %q should mention the operation", err)
	}
}
