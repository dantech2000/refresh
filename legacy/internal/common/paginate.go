package common

import "context"

// Paginate calls fetch with the previous response's next token until the
// returned token is nil or empty. Returned items from each page are appended
// in order. fetch is responsible for any per-page retry/error formatting.
func Paginate[Item any](
	ctx context.Context,
	fetch func(ctx context.Context, token *string) (items []Item, next *string, err error),
) ([]Item, error) {
	var all []Item
	var token *string
	for {
		// Stop between pages if the caller cancelled. (REF-56)
		if err := ctx.Err(); err != nil {
			return all, err
		}
		items, next, err := fetch(ctx, token)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if next == nil || *next == "" {
			return all, nil
		}
		// Guard against a server echoing a non-advancing token, which would
		// otherwise loop forever. (REF-56)
		if token != nil && *next == *token {
			return all, nil
		}
		token = next
	}
}
