package awsx

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// identityError is a failure to get the credentials of a call. The call
// itself was never sent.
type identityError struct {
	err error
}

func (e *identityError) Error() string { return e.err.Error() }
func (e *identityError) Unwrap() error { return e.err }

// isIdentityFailure reports whether err is a failure to get credentials,
// from a provider that markIdentityFailures wrapped.
func isIdentityFailure(err error) bool {
	var identity *identityError
	return errors.As(err, &identity)
}

// markIdentityFailures returns provider with its failures marked, so that
// the retryer of an operation can tell them from a failure of the operation.
//
// The client that refreshes the credentials (STS for a role, SSO) has the
// Standard policy and makes its own attempts. When they are used up, the
// error holds the last answer, such as a throttling error. Without the mark
// the retryer of the operation that needed the credentials would read that
// answer as its own and start the whole refresh again for each of its
// attempts.
func markIdentityFailures(provider aws.CredentialsProvider) aws.CredentialsProvider {
	if provider == nil {
		return nil
	}
	return identityProvider{provider}
}

type identityProvider struct {
	provider aws.CredentialsProvider
}

func (p identityProvider) Retrieve(ctx context.Context) (aws.Credentials, error) {
	creds, err := p.provider.Retrieve(ctx)
	if err != nil {
		return creds, &identityError{err: err}
	}
	return creds, nil
}
