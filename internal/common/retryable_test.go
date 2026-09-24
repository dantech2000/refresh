package common

import (
	"encoding/hex"
	"fmt"
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
		if b, err := hex.DecodeString(tok); err != nil || len(b) != 16 {
			t.Fatalf("token %q is not 16 random bytes in hex", tok)
		}
		if seen[tok] {
			t.Fatalf("token %q repeated", tok)
		}
		seen[tok] = true
	}
}
