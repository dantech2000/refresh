package cmdutil

import (
	"context"
	"time"
)

// WithTimeout returns a context for the AWS calls of a command. It expires
// after timeout, and a timeout of zero or less means no deadline: only
// parent, the signal context, then ends it.
//
// No prompt runs under it. A read command and a dry run never ask, and a
// changing command runs under the signal context: change stops its own
// budgets while a question is open.
func WithTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}
