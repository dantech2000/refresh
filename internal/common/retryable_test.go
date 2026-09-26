package common

import (
	"context"
	"encoding/hex"
	"fmt"
	"syscall"
	"testing"

	"github.com/dantech2000/refresh/internal/mocks"
)

// IsRetryable classifies the typed errors the EKS mock injects exactly as it
// classifies the SDK's, wrapped or not: throttling and server faults are
// transient; permission, not-found and validation errors are permanent.
func TestIsRetryable_TypedMockErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"throttling", mocks.Throttling(), true},
		{"internal failure", mocks.APIError("InternalFailure", "boom"), true},
		{"service unavailable", mocks.APIError("ServiceUnavailableException", "busy"), true},
		{"unknown server fault", mocks.APIError("ServerException", "x"), true},
		{"too many requests", mocks.APIError("TooManyRequestsException", "slow down"), true},
		{"access denied", mocks.AccessDenied(), false},
		{"not found", mocks.NotFound(), false},
		{"invalid parameter", mocks.APIError("InvalidParameterException", "bad"), false},
	}
	for _, tc := range cases {
		for _, err := range []error{tc.err, fmt.Errorf("describing cluster: %w", tc.err)} {
			if got := IsRetryable(err); got != tc.want {
				t.Errorf("%s: IsRetryable(%v) = %v, want %v", tc.name, err, got, tc.want)
			}
		}
	}
}

// IdempotencyToken returns a fresh 32-hex-char token each call, so a caller
// can reuse one token across its own retries without colliding with other
// operations.
func TestIdempotencyToken_UniqueHex(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		tok := IdempotencyToken()
		// EKS rejects a token outside 33..126 characters on
		// UpdateClusterVersion (a real upgrade failed on 32).
		if b, err := hex.DecodeString(tok); err != nil || len(b) != 20 || len(tok) < 33 || len(tok) > 126 {
			t.Fatalf("token %q is not 20 random bytes in hex (33..126 characters)", tok)
		}
		if seen[tok] {
			t.Fatalf("token %q repeated", tok)
		}
		seen[tok] = true
	}
}

func TestIsPermanentAPIError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"access denied", mocks.AccessDenied(), true},
		{"not found", mocks.NotFound(), true},
		{"throttling", mocks.Throttling(), false},
		{"server fault", mocks.APIError("InternalFailure", "boom"), false},
		{"connection reset", fmt.Errorf("dial: %w", syscall.ECONNRESET), false},
		{"deadline", context.DeadlineExceeded, false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		if got := IsPermanentAPIError(tc.err); got != tc.want {
			t.Errorf("%s: IsPermanentAPIError = %v, want %v", tc.name, got, tc.want)
		}
	}
}
