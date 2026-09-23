package aws

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"

	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
)

// The classification and formatting tests live in internal/aws/awserr; this
// only checks that the forwarder keeps the wrap chain.
func TestFormatAWSError_ForwardsAndWraps(t *testing.T) {
	if FormatAWSError(nil, "op") != nil {
		t.Error("nil error should return nil")
	}
	err := FormatAWSError(fmt.Errorf("operation error EKS: %w", context.Canceled), "listing clusters")
	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(context.Canceled) = false through FormatAWSError: %v", err)
	}
}

const credentialHelp = "AWS credentials not configured or invalid"

func loadFakeConfig(t *testing.T) aws.Config {
	t.Helper()
	cfg, err := config.LoadDefaultConfig(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// CheckAWSCredentials prints nothing (stdout stays clean for -o json/yaml)
// and returns the setup help exactly once, for a credential error.
func TestCheckAWSCredentials_CredentialErrorCarriesHelpOnce(t *testing.T) {
	srv := fakeaws.New(t)
	srv.FailCredentials("InvalidClientTokenId")
	cfg := loadFakeConfig(t)

	var err error
	stdout, stderr := captureOutput(t, func() { err = CheckAWSCredentials(t.Context(), cfg) })
	if err == nil {
		t.Fatal("CheckAWSCredentials passed with bad credentials")
	}
	if n := strings.Count(err.Error(), credentialHelp); n != 1 {
		t.Errorf("credential help appears %d times in the error, want 1:\n%v", n, err)
	}
	if stdout != "" || stderr != "" {
		t.Errorf("CheckAWSCredentials printed output: stdout=%q stderr=%q", stdout, stderr)
	}
}

// A cancelled check is not a credential problem: no credential help.
func TestCheckAWSCredentials_CancelGetsNoCredentialHelp(t *testing.T) {
	fakeaws.New(t)
	cfg := loadFakeConfig(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := CheckAWSCredentials(ctx, cfg)
	if err == nil {
		t.Fatal("CheckAWSCredentials passed with a cancelled context")
	}
	if strings.Contains(err.Error(), credentialHelp) {
		t.Errorf("a cancel must not show credential help:\n%v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) = false: %v", err)
	}
}

func TestCheckAWSCredentials_Valid(t *testing.T) {
	fakeaws.New(t)
	if err := CheckAWSCredentials(t.Context(), loadFakeConfig(t)); err != nil {
		t.Fatalf("CheckAWSCredentials = %v, want nil", err)
	}
}

// captureOutput runs fn with os.Stdout and os.Stderr redirected.
func captureOutput(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	func() {
		defer func() { os.Stdout, os.Stderr = origOut, origErr }()
		fn()
	}()
	_ = outW.Close()
	_ = errW.Close()
	o, _ := io.ReadAll(outR)
	e, _ := io.ReadAll(errR)
	return string(o), string(e)
}
