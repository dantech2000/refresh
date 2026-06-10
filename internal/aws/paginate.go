package aws

import (
	"context"

	"github.com/dantech2000/refresh/internal/services/common"
)

// ListAllPages pages through an AWS list API, retrying each page call with
// common.DefaultRetryConfig and formatting failures with FormatAWSError.
// call fetches one page for the given token; extract pulls the items and the
// next token out of the page output.
//
// It replaces the Paginate(WithRetry(...)) sandwich previously duplicated by
// every list-style service method, so the retry and error-formatting policy
// lives in one place.
func ListAllPages[O, T any](
	ctx context.Context,
	operation string,
	call func(ctx context.Context, token *string) (O, error),
	extract func(O) (items []T, next *string),
) ([]T, error) {
	return common.Paginate(ctx, func(rc context.Context, token *string) ([]T, *string, error) {
		out, err := common.WithRetry(rc, common.DefaultRetryConfig, func(rrc context.Context) (O, error) {
			return call(rrc, token)
		})
		if err != nil {
			return nil, nil, FormatAWSError(err, operation)
		}
		items, next := extract(out)
		return items, next, nil
	})
}
