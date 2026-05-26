package addon

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
)

// stubListAddons is a minimal listAddonsAPI for resolveAddonName tests.
type stubListAddons struct {
	out *eks.ListAddonsOutput
	err error
}

func (s *stubListAddons) ListAddons(_ context.Context, _ *eks.ListAddonsInput, _ ...func(*eks.Options)) (*eks.ListAddonsOutput, error) {
	return s.out, s.err
}

// Regression: when ListAddons fails (e.g. AccessDeniedException on
// eks:ListAddons) the SDK returns (nil, err). The old resolver did
// `list, _ := ...; for _, n := range list.Addons` which panics with nil
// pointer dereference. The fix surfaces the API error to the caller.
func TestResolveAddonName_ListAddonsErrorReturnsFormattedError(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("resolveAddonName panicked on ListAddons error: %v", r)
		}
	}()

	stub := &stubListAddons{
		out: nil,
		err: errors.New("AccessDeniedException: user is not authorized to perform: eks:ListAddons"),
	}
	// Substring name forces the fuzzy-resolve path that calls ListAddons.
	got, err := resolveAddonName(context.Background(), stub, "my-cluster", "vpc cni")
	if got != "" {
		t.Errorf("expected empty name on error, got %q", got)
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "AccessDenied") && !strings.Contains(err.Error(), "listing add-ons") {
		t.Errorf("error should surface the AWS failure, got %v", err)
	}
}

func TestResolveAddonName_ValidRegexBypassesLookup(t *testing.T) {
	// stub.out is nil; the lookup must NOT be invoked for valid addon names.
	stub := &stubListAddons{}
	got, err := resolveAddonName(context.Background(), stub, "my-cluster", "vpc-cni")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "vpc-cni" {
		t.Errorf("expected pass-through, got %q", got)
	}
}

func TestResolveAddonName_NoMatchReturnsError(t *testing.T) {
	stub := &stubListAddons{
		out: &eks.ListAddonsOutput{Addons: []string{"coredns"}},
	}
	_, err := resolveAddonName(context.Background(), stub, "my-cluster", "totally bogus")
	if err == nil {
		t.Fatal("expected error for unknown addon name, got nil")
	}
}

// Avoid unused-import noise when this file is the only one referencing aws.
var _ = aws.String
