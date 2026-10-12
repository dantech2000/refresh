package awsxtest_test

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/smithy-go"
	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

var update = flag.Bool("update", false, "rewrite COVERAGE.md from the matrix")

// opCase is one row of the coverage matrix: an operation, how a real SDK
// client calls it, and what the client must decode from newWorld.
type opCase struct {
	op fleettest.Operation
	// paged is set for an operation whose API pages.
	paged   bool
	mutates bool
	// run calls the operation and follows its page tokens to the end. It
	// returns the number of requests it made and the difference between the
	// decoded items and the expected ones.
	run func(ctx context.Context, c *clients) (pages int, diff string, err error)
}

// request is one SDK call: the items read from the output and the token of
// the next page, if any.
type request[T any] func(ctx context.Context, c *clients, token *string) (items []T, next *string, err error)

func newCase[T any](op fleettest.Operation, want []T, call request[T]) opCase {
	return opCase{op: op, run: func(ctx context.Context, c *clients) (int, string, error) {
		var got []T
		var token *string
		for pages := 1; ; pages++ {
			items, next, err := call(ctx, c, token)
			if err != nil {
				return pages, "", err
			}
			got = append(got, items...)
			if aws.ToString(next) == "" {
				return pages, cmp.Diff(want, got), nil
			}
			token = next
		}
	}}
}

func pagedCase[T any](op fleettest.Operation, want []T, call request[T]) opCase {
	tc := newCase(op, want, call)
	tc.paged = true
	return tc
}

func mutatingCase[T any](op fleettest.Operation, want []T, call request[T]) opCase {
	tc := newCase(op, want, call)
	tc.mutates = true
	return tc
}

// wireErrors are the codes each service answers for a denied and for a
// throttled request.
var wireErrors = map[string]struct{ denied, throttled string }{
	"eks":           {"AccessDeniedException", "ThrottlingException"},
	"sts":           {"AccessDenied", "Throttling"},
	"ec2":           {"UnauthorizedOperation", "RequestLimitExceeded"},
	"autoscaling":   {"AccessDenied", "Throttling"},
	"ssm":           {"AccessDeniedException", "ThrottlingException"},
	"cloudwatch":    {"AccessDenied", "Throttling"},
	"servicequotas": {"AccessDeniedException", "TooManyRequestsException"},
}

func serviceOf(op fleettest.Operation) string {
	service, _, _ := strings.Cut(string(op), ":")
	return service
}

// proofs are the columns of the matrix. Each is proven by the test named
// TestMatrix plus the column, for every operation; "paged" only for the
// operations that page.
var proofs = []string{"success", "denied", "throttled", "paged", "delayed", "cancelled"}

// TestMatrixSuccess proves that the SDK decodes each operation's response
// into the fields refresh reads, and that the server logs the request.
func TestMatrixSuccess(t *testing.T) {
	for _, tc := range matrix() {
		t.Run(string(tc.op), func(t *testing.T) {
			f := newFixture(t)
			_, diff, err := tc.run(testContext(t), f.clients)
			if err != nil {
				t.Fatalf("%s: %v", tc.op, err)
			}
			if diff != "" {
				t.Errorf("%s decoded the wrong output (-want +got):\n%s", tc.op, diff)
			}

			got := f.server.Requests()
			if len(got) != 1 {
				t.Fatalf("the server logged %d requests, want 1: %+v", len(got), got)
			}
			want := awsxtest.Request{Operation: tc.op, Service: serviceOf(tc.op), Region: region, Mutates: tc.mutates}
			got[0].Input = nil // the inputs are checked in TestRequestLog
			if diff := cmp.Diff(want, got[0]); diff != "" {
				t.Errorf("logged request (-want +got):\n%s", diff)
			}
		})
	}
}

// TestMatrixDenied proves that a denied call returns a typed API error with
// the service's access-denied code, and is not retried.
func TestMatrixDenied(t *testing.T) {
	for _, tc := range matrix() {
		t.Run(string(tc.op), func(t *testing.T) {
			f := newFixture(t)
			f.world.SetFault(tc.op, fleettest.Fault{Deny: true})
			_, _, err := tc.run(testContext(t), f.clients)

			var apiErr smithy.APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("%s returned %v, want a smithy.APIError", tc.op, err)
			}
			if want := wireErrors[serviceOf(tc.op)].denied; apiErr.ErrorCode() != want {
				t.Errorf("error code = %q, want %q", apiErr.ErrorCode(), want)
			}
			if n := len(f.requests(tc.op)); n != 1 {
				t.Errorf("the client sent %d requests, want 1: a denied call is not retried", n)
			}
		})
	}
}

// TestMatrixThrottled proves that a throttled call returns a typed error the
// SDK retries, and that a client with retries gets through.
func TestMatrixThrottled(t *testing.T) {
	for _, tc := range matrix() {
		t.Run(string(tc.op), func(t *testing.T) {
			f := newFixture(t)
			f.world.SetFault(tc.op, fleettest.Fault{Throttle: 3})
			ctx := testContext(t)

			// One attempt shows the error the SDK decodes.
			_, _, err := tc.run(ctx, f.once)
			var apiErr smithy.APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("%s returned %v, want a smithy.APIError", tc.op, err)
			}
			if want := wireErrors[serviceOf(tc.op)].throttled; apiErr.ErrorCode() != want {
				t.Errorf("error code = %q, want %q", apiErr.ErrorCode(), want)
			}
			if !retry.NewStandard().IsErrorRetryable(err) {
				t.Errorf("the SDK does not retry %v", err)
			}
			if retry.IsErrorThrottles(retry.DefaultThrottles).IsErrorThrottle(err) != aws.TrueTernary {
				t.Errorf("the SDK does not read %v as throttling", err)
			}

			// Two throttles remain. A client with three attempts gets through.
			_, diff, err := tc.run(ctx, f.clients)
			if err != nil {
				t.Fatalf("%s with retries: %v", tc.op, err)
			}
			if diff != "" {
				t.Errorf("%s decoded the wrong output after retries (-want +got):\n%s", tc.op, diff)
			}
			if n := len(f.requests(tc.op)); n != 4 {
				t.Errorf("the server got %d requests, want 4: 3 throttled and 1 answered", n)
			}
		})
	}
}

// TestMatrixPaged proves that every item arrives when the server answers
// one item per page.
func TestMatrixPaged(t *testing.T) {
	for _, tc := range matrix() {
		if !tc.paged {
			continue
		}
		t.Run(string(tc.op), func(t *testing.T) {
			f := newFixture(t)
			f.world.SetFault(tc.op, fleettest.Fault{PageSize: 1})
			pages, diff, err := tc.run(testContext(t), f.clients)
			if err != nil {
				t.Fatalf("%s: %v", tc.op, err)
			}
			if diff != "" {
				t.Errorf("%s lost items over pages of 1 (-want +got):\n%s", tc.op, diff)
			}
			if pages < 2 {
				t.Errorf("%s made %d request, want one per item", tc.op, pages)
			}
			if n := len(f.requests(tc.op)); n != pages {
				t.Errorf("the server got %d requests, want %d", n, pages)
			}
		})
	}
}

type result struct {
	diff string
	err  error
}

// start runs the case in a goroutine and returns the channel its result
// arrives on.
func start(ctx context.Context, tc opCase, c *clients) <-chan result {
	done := make(chan result, 1)
	go func() {
		_, diff, err := tc.run(ctx, c)
		done <- result{diff, err}
	}()
	return done
}

func await(ctx context.Context, t *testing.T, done <-chan result) result {
	t.Helper()
	select {
	case r := <-done:
		return r
	case <-ctx.Done():
		t.Fatal("the call did not return")
		return result{}
	}
}

// TestMatrixDelayed proves that a delayed response waits for the world's
// clock and then decodes.
func TestMatrixDelayed(t *testing.T) {
	for _, tc := range matrix() {
		t.Run(string(tc.op), func(t *testing.T) {
			const delay = 5 * time.Second
			f := newFixture(t)
			f.world.SetFault(tc.op, fleettest.Fault{Delay: delay})
			ctx := testContext(t)

			done := start(ctx, tc, f.clients)
			if err := f.world.AwaitBlocked(ctx, 1); err != nil {
				t.Fatal(err)
			}
			select {
			case r := <-done:
				t.Fatalf("%s answered before the clock advanced: %+v", tc.op, r)
			default:
			}
			f.world.Advance(delay - time.Second)
			if err := f.world.AwaitBlocked(ctx, 1); err != nil {
				t.Fatalf("the request left before its delay ended: %v", err)
			}
			f.world.Advance(time.Second)

			r := await(ctx, t, done)
			if r.err != nil {
				t.Fatalf("%s after the delay: %v", tc.op, r.err)
			}
			if r.diff != "" {
				t.Errorf("%s decoded the wrong output after the delay (-want +got):\n%s", tc.op, r.diff)
			}
		})
	}
}

// TestMatrixCancelled proves that a call to a hanging handler returns when
// its context is cancelled, and that the handler exits.
func TestMatrixCancelled(t *testing.T) {
	for _, tc := range matrix() {
		t.Run(string(tc.op), func(t *testing.T) {
			f := newFixture(t)
			f.world.SetFault(tc.op, fleettest.Fault{Hang: true})
			ctx := testContext(t)
			callCtx, cancel := context.WithCancel(ctx)
			defer cancel()

			done := start(callCtx, tc, f.clients)
			if err := f.world.AwaitBlocked(ctx, 1); err != nil {
				t.Fatal(err)
			}
			cancel()
			if r := await(ctx, t, done); !errors.Is(r.err, context.Canceled) {
				t.Errorf("%s returned %v, want context.Canceled", tc.op, r.err)
			}
			// The handler leaves once the server sees the client go.
			if err := f.world.AwaitBlocked(ctx, 0); err != nil {
				t.Errorf("the handler did not return: %v", err)
			}
			if n := len(f.requests(tc.op)); n != 1 {
				t.Errorf("the client sent %d requests, want 1", n)
			}
		})
	}
}

// permissionAction matches an IAM action in the legacy permission table.
var permissionAction = regexp.MustCompile(`"([a-z0-9]+:[A-Za-z0-9]+)"`)

// TestMatrixCoversEveryOperation holds the matrix and the server to the
// list of AWS operations the product calls. Until cutover that list is the
// legacy IAM table, which Go's internal rule keeps this package from
// importing, so the test reads the source file.
func TestMatrixCoversEveryOperation(t *testing.T) {
	const table = "../../../legacy/internal/aws/awserr/permissions.go"
	src, err := os.ReadFile(table)
	if err != nil {
		t.Fatalf("reading the IAM table (point this test at its new home if it moved): %v", err)
	}
	var want []string
	for _, m := range permissionAction.FindAllStringSubmatch(string(src), -1) {
		want = append(want, m[1])
	}
	slices.Sort(want)
	if len(want) == 0 {
		t.Fatalf("found no IAM actions in %s", table)
	}

	var cases, served []string
	for _, tc := range matrix() {
		cases = append(cases, string(tc.op))
	}
	slices.Sort(cases)
	for _, info := range awsxtest.Operations() {
		served = append(served, string(info.Operation))
	}
	if diff := cmp.Diff(want, cases); diff != "" {
		t.Errorf("the matrix and the IAM table differ (-table +matrix):\n%s", diff)
	}
	if diff := cmp.Diff(want, served); diff != "" {
		t.Errorf("the server and the IAM table differ (-table +server):\n%s", diff)
	}
}

// TestCoverageDoc keeps COVERAGE.md equal to the matrix. Run the test with
// -update to rewrite the file.
func TestCoverageDoc(t *testing.T) {
	info := map[fleettest.Operation]awsxtest.OperationInfo{}
	for _, i := range awsxtest.Operations() {
		info[i.Operation] = i
	}
	var b strings.Builder
	b.WriteString("# AWS operation coverage\n\n")
	b.WriteString("The `TestMatrix*` tests call every AWS operation that refresh uses, with a real SDK\n")
	b.WriteString("client against the fake server. `TestCoverageDoc` generates this file from their\n")
	b.WriteString("table. To rewrite it, run `go test ./internal/awsx/awsxtest -run TestCoverageDoc -update`.\n\n")
	b.WriteString("| Column | What `TestMatrix<Column>` proves |\n|---|---|\n")
	b.WriteString("| success | The SDK decodes the response into the fields refresh reads. |\n")
	b.WriteString("| denied | The SDK returns a typed API error with the access-denied code of the service, and does not retry. |\n")
	b.WriteString("| throttled | The SDK returns a typed, retryable throttling error, and a client with retries succeeds. |\n")
	b.WriteString("| paged | With a forced page size of 1, every item arrives. `-` means the API has no pages. |\n")
	b.WriteString("| delayed | The response waits for the world's clock, then decodes. |\n")
	b.WriteString("| cancelled | A call to a hanging handler returns when its context is cancelled, and the handler exits. |\n\n")
	b.WriteString("| Operation | Service | Protocol | Mutates |")
	for _, p := range proofs {
		b.WriteString(" " + p + " |")
	}
	b.WriteString("\n|---|---|---|---|" + strings.Repeat("---|", len(proofs)) + "\n")
	for _, tc := range matrix() {
		mutates := "no"
		if tc.mutates {
			mutates = "yes"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s |", tc.op, info[tc.op].Service, info[tc.op].Protocol, mutates)
		for _, p := range proofs {
			cell := "yes"
			if p == "paged" && !tc.paged {
				cell = "-"
			}
			b.WriteString(" " + cell + " |")
		}
		b.WriteString("\n")
	}

	const path = "COVERAGE.md"
	if *update {
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(b.String(), string(got)); diff != "" {
		t.Errorf("COVERAGE.md is stale; run this test with -update (-want +got):\n%s", diff)
	}
}
