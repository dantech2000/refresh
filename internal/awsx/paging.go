package awsx

import (
	"context"
	"fmt"
)

// ListAll reads every page of a paged list API and returns the items in
// order. call fetches the page for a token, which is nil for the first page.
// extract returns the items of a page and the token of the next one.
//
// A next token that was already used is an error. Such a server would
// otherwise make the loop endless, and to stop without an error would pass a
// partial list as a complete one.
func ListAll[O, T any](
	ctx context.Context,
	call func(ctx context.Context, token *string) (O, error),
	extract func(O) (items []T, next *string),
) ([]T, error) {
	var all []T
	var token *string
	seen := map[string]bool{}
	for {
		// Stop between pages when the caller gave up.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out, err := call(ctx, token)
		if err != nil {
			return nil, err
		}
		items, next := extract(out)
		all = append(all, items...)
		if next == nil || *next == "" {
			return all, nil
		}
		if seen[*next] {
			return nil, fmt.Errorf("the list did not advance: the next page token %q was already used", *next)
		}
		seen[*next] = true
		token = next
	}
}
