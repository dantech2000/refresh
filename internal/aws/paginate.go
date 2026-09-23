package aws

import (
	"context"

	"github.com/dantech2000/refresh/internal/aws/awserr"
)

// ListAllPages pages through an AWS list API with the shared retry and
// error-formatting policy. It forwards to awserr.ListAllPages, which holds the
// implementation so leaf packages can use it without importing this one.
func ListAllPages[O, T any](
	ctx context.Context,
	operation string,
	call func(ctx context.Context, token *string) (O, error),
	extract func(O) (items []T, next *string),
) ([]T, error) {
	return awserr.ListAllPages(ctx, operation, call, extract)
}
