package awsxtest_test

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	sqtypes "github.com/aws/aws-sdk-go-v2/service/servicequotas/types"
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
	// paged is set for an operation whose API pages. wantPages is the
	// number of requests it takes to read want one item per page.
	paged     bool
	wantPages int
	mutates   bool
	// run calls the operation and follows its page tokens to the end. It
	// returns the decoded items, as a []T, and the number of requests.
	run func(ctx context.Context, c *clients) (got any, pages int, err error)
	// want is what run returns in region, as a []T.
	want any
	// elsewhere is what run returns in a region that holds nothing. Nil
	// means an API error or no items.
	elsewhere any
	// state reads what a mutating operation changes, and wantState is its
	// value after the call. The default update script ends at once, so the
	// change is visible when the call returns.
	state     func(s *fleettest.State) string
	wantState string
}

// request is one SDK call: the items read from the output and the token of
// the next page, if any.
type request[T any] func(ctx context.Context, c *clients, token *string) (items []T, next *string, err error)

func newCase[T any](op fleettest.Operation, want []T, call request[T]) opCase {
	return opCase{op: op, want: want, run: func(ctx context.Context, c *clients) (any, int, error) {
		var got []T
		var token *string
		for pages := 1; ; pages++ {
			items, next, err := call(ctx, c, token)
			if err != nil {
				return nil, pages, err
			}
			got = append(got, items...)
			if aws.ToString(next) == "" {
				return got, pages, nil
			}
			token = next
		}
	}}
}

func pagedCase[T any](op fleettest.Operation, want []T, call request[T]) opCase {
	tc := newCase(op, want, call)
	tc.paged, tc.wantPages = true, len(want)
	return tc
}

// mutatingCase is a case for an operation that changes the world. state
// reads the changed part of the prod cluster.
func mutatingCase[T any](op fleettest.Operation, want []T, state func(c *fleettest.Cluster) string, wantState string, call request[T]) opCase {
	tc := newCase(op, want, call)
	tc.mutates = true
	tc.state = func(s *fleettest.State) string { return state(s.Cluster(region, "prod")) }
	tc.wantState = wantState
	return tc
}

// readState returns tc.state of the world, or "" for a case with none.
func (tc opCase) readState(w *fleettest.World) string {
	if tc.state == nil {
		return ""
	}
	var out string
	w.View(func(s *fleettest.State) { out = tc.state(s) })
	return out
}

// is reports whether err is, or wraps, an error of the concrete type T.
func is[T error](err error) bool {
	var target T
	return errors.As(err, &target)
}

// wireError is how the SDK must decode one injected error of a service: the
// code, and the concrete Go type. Where the service's model has a shape for
// the code and lists it for the operation, the type is that shape, which
// shows that the SDK dispatched on the error. Elsewhere it is the SDK's
// generic API error.
type wireError struct {
	code    string
	modeled func(error) bool
}

var generic = is[*smithy.GenericAPIError]

// wireErrors are the denied and the throttled error of each service.
var wireErrors = map[string]struct{ denied, throttled wireError }{
	"eks":           {wireError{"AccessDeniedException", is[*ekstypes.AccessDeniedException]}, wireError{"ThrottlingException", is[*ekstypes.ThrottlingException]}},
	"sts":           {wireError{"AccessDenied", generic}, wireError{"Throttling", generic}},
	"ec2":           {wireError{"UnauthorizedOperation", generic}, wireError{"RequestLimitExceeded", generic}},
	"autoscaling":   {wireError{"AccessDenied", generic}, wireError{"Throttling", generic}},
	"ssm":           {wireError{"AccessDeniedException", generic}, wireError{"ThrottlingException", generic}},
	"cloudwatch":    {wireError{"AccessDenied", generic}, wireError{"Throttling", generic}},
	"servicequotas": {wireError{"AccessDeniedException", is[*sqtypes.AccessDeniedException]}, wireError{"TooManyRequestsException", is[*sqtypes.TooManyRequestsException]}},
}

// checkError fails t unless err is the API error want describes.
func checkError(t *testing.T, err error, want wireError) {
	t.Helper()
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("got %v, want a smithy.APIError", err)
	}
	if apiErr.ErrorCode() != want.code {
		t.Errorf("error code = %q, want %q", apiErr.ErrorCode(), want.code)
	}
	if !want.modeled(err) {
		t.Errorf("the SDK decoded %s as %T, which is not the expected type", want.code, apiErr)
	}
}

// proofs are the columns of the matrix. Each is proven by the test named
// TestMatrix plus the column, for every operation; "paged" only for the
// operations that page.
var proofs = []string{"success", "denied", "throttled", "paged", "delayed", "cancelled", "region"}

// TestMatrixSuccess proves that the SDK decodes each operation's response
// into the fields refresh reads, that the server logs the request, and that
// a mutating operation changed the world.
func TestMatrixSuccess(t *testing.T) {
	for _, tc := range matrix() {
		t.Run(string(tc.op), func(t *testing.T) {
			f := newFixture(t)
			before := tc.readState(f.world)
			got, _, err := tc.run(testContext(t), f.clients)
			if err != nil {
				t.Fatalf("%s: %v", tc.op, err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("%s decoded the wrong output (-want +got):\n%s", tc.op, diff)
			}

			logged := f.server.Requests()
			if len(logged) != 1 {
				t.Fatalf("the server logged %d requests, want 1: %+v", len(logged), logged)
			}
			want := awsxtest.Request{Operation: tc.op, Service: tc.op.Service(), Region: region, Mutates: tc.mutates}
			logged[0].Input = nil // the inputs are checked in TestRequestLog
			if diff := cmp.Diff(want, logged[0]); diff != "" {
				t.Errorf("logged request (-want +got):\n%s", diff)
			}

			if tc.mutates != (tc.state != nil) {
				t.Fatalf("%s: a mutating case, and only a mutating case, needs a state check", tc.op)
			}
			if after := tc.readState(f.world); after != tc.wantState || after == before && tc.mutates {
				t.Errorf("state after %s = %q (before: %q), want %q", tc.op, after, before, tc.wantState)
			}
		})
	}
}

// TestMatrixDenied proves that a denied call returns the access-denied
// error of the service in the Go type the SDK models it with, is not
// retried, and changes nothing.
func TestMatrixDenied(t *testing.T) {
	for _, tc := range matrix() {
		t.Run(string(tc.op), func(t *testing.T) {
			f := newFixture(t)
			before := tc.readState(f.world)
			f.world.SetFault(tc.op, fleettest.Fault{Deny: true})
			_, _, err := tc.run(testContext(t), f.clients)

			checkError(t, err, wireErrors[tc.op.Service()].denied)
			if n := len(f.requests(tc.op)); n != 1 {
				t.Errorf("the client sent %d requests, want 1: a denied call is not retried", n)
			}
			if after := tc.readState(f.world); after != before {
				t.Errorf("a denied %s changed the state from %q to %q", tc.op, before, after)
			}
		})
	}
}

// TestMatrixThrottled proves that a throttled call returns the throttling
// error of the service, that the SDK classifies it as retryable throttling,
// and that a client with retries gets through.
func TestMatrixThrottled(t *testing.T) {
	for _, tc := range matrix() {
		t.Run(string(tc.op), func(t *testing.T) {
			f := newFixture(t)
			f.world.SetFault(tc.op, fleettest.Fault{Throttle: 3})
			ctx := testContext(t)

			// One attempt shows the error the SDK decodes.
			_, _, err := tc.run(ctx, f.once)
			checkError(t, err, wireErrors[tc.op.Service()].throttled)
			if !retry.NewStandard().IsErrorRetryable(err) {
				t.Errorf("the SDK does not retry %v", err)
			}
			if retry.IsErrorThrottles(retry.DefaultThrottles).IsErrorThrottle(err) != aws.TrueTernary {
				t.Errorf("the SDK does not read %v as throttling", err)
			}

			// Two throttles remain. A client with three attempts gets through.
			got, _, err := tc.run(ctx, f.clients)
			if err != nil {
				t.Fatalf("%s with retries: %v", tc.op, err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
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
			got, pages, err := tc.run(testContext(t), f.clients)
			if err != nil {
				t.Fatalf("%s: %v", tc.op, err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("%s lost items over pages of 1 (-want +got):\n%s", tc.op, diff)
			}
			if pages != tc.wantPages || pages < 2 {
				t.Errorf("%s made %d requests, want %d: one per item, and at least 2", tc.op, pages, tc.wantPages)
			}
			if n := len(f.requests(tc.op)); n != pages {
				t.Errorf("the server got %d requests, want %d", n, pages)
			}
		})
	}
}

type result struct {
	got any
	err error
}

// start runs the case in a goroutine and returns the channel its result
// arrives on.
func start(ctx context.Context, tc opCase, c *clients) <-chan result {
	done := make(chan result, 1)
	go func() {
		got, _, err := tc.run(ctx, c)
		done <- result{got, err}
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
			if diff := cmp.Diff(tc.want, r.got); diff != "" {
				t.Errorf("%s decoded the wrong output after the delay (-want +got):\n%s", tc.op, diff)
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
			before := tc.readState(f.world)
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
			if after := tc.readState(f.world); after != before {
				t.Errorf("a cancelled %s changed the state from %q to %q", tc.op, before, after)
			}
		})
	}
}

// TestMatrixRegion proves that an operation answers only from its own
// region. The success test already shows that the decoys of otherRegion do
// not leak into region. This test calls from a region that holds nothing:
// the call must fail with an API error or return no items.
func TestMatrixRegion(t *testing.T) {
	for _, tc := range matrix() {
		t.Run(string(tc.op), func(t *testing.T) {
			f := newFixture(t)
			before := tc.readState(f.world)
			got, _, err := tc.run(testContext(t), f.in(emptyRegion))
			switch {
			case tc.elsewhere != nil:
				if diff := cmp.Diff(tc.elsewhere, got); diff != "" || err != nil {
					t.Errorf("%s in %s: err %v (-want +got):\n%s", tc.op, emptyRegion, err, diff)
				}
			case err != nil:
				var apiErr smithy.APIError
				if !errors.As(err, &apiErr) {
					t.Errorf("%s in %s: %v, want an API error", tc.op, emptyRegion, err)
				}
			case reflect.ValueOf(got).Len() != 0:
				t.Errorf("%s in %s returned items of %s: %+v", tc.op, emptyRegion, region, got)
			}
			if after := tc.readState(f.world); after != before {
				t.Errorf("%s in %s changed %s from %q to %q", tc.op, emptyRegion, region, before, after)
			}
			if logged := f.server.Requests(); len(logged) == 0 || logged[0].Region != emptyRegion {
				t.Errorf("the server did not log the request for %s: %+v", emptyRegion, logged)
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
	b.WriteString("| success | The SDK decodes the response into the fields the legacy product reads (the projections in `operations_test.go`). The request is logged once with its operation, service, region, and mutation flag. A mutating operation changed the world. |\n")
	b.WriteString("| denied | The SDK returns the access-denied code of the service, in the modeled Go type where the SDK dispatches one for the operation (EKS, Service Quotas) and as `smithy.GenericAPIError` elsewhere. The call is not retried and changes nothing. |\n")
	b.WriteString("| throttled | The SDK returns the throttling code of the service, in the same type rule, and classifies it as retryable throttling. A client with 3 attempts succeeds after 2 more throttles. |\n")
	b.WriteString("| paged | With a forced page size of 1, every item arrives, one request per page. `-` means the API has no pages. |\n")
	b.WriteString("| delayed | The response waits for the world's clock, then decodes. |\n")
	b.WriteString("| cancelled | A call to a hanging handler returns `context.Canceled` when its context is cancelled, the handler exits, and nothing changed. |\n")
	b.WriteString("| region | Called from a region that holds nothing, the operation fails with an API error or returns no items, and changes nothing. The success column also runs with a decoy of each kind in a second region. |\n\n")
	b.WriteString("Not in the table, and proven once per service or protocol in `server_test.go`: the raw\n")
	b.WriteString("error envelopes, the HTTP status of injected error codes, modeled not-found and\n")
	b.WriteString("bad-token errors, scoped faults, a lost response after a mutation, peak concurrency,\n")
	b.WriteString("and the request log's inputs.\n\n")
	b.WriteString("## Not verified against real AWS\n\n")
	b.WriteString("The tests prove that the SDK decodes what the fake sends. They do not prove that AWS\n")
	b.WriteString("sends the same. Nothing below was compared with a captured AWS response. A live check\n")
	b.WriteString("must settle each item before a test relies on it.\n\n")
	b.WriteString("- **Every HTTP status in `service.go`.** The codes and statuses are from the AWS API\n")
	b.WriteString("  references as the author knows them. The least certain: `ExpiredTokenException` and\n")
	b.WriteString("  `ExpiredToken` as 403 for the query services and EKS; `OptInRequired` as 401 for EC2 and\n")
	b.WriteString("  403 for the query services; `AuthFailure` as 401; EKS `ThrottlingException` as 429;\n")
	b.WriteString("  `RequestLimitExceeded` as 503; every client error of SSM and Service Quotas as 400,\n")
	b.WriteString("  including `AccessDeniedException` and `UnrecognizedClientException`.\n")
	b.WriteString("- **The bad-credentials code per service** (`Fault.Unauthenticated`): `UnrecognizedClientException`\n")
	b.WriteString("  for EKS, SSM, and Service Quotas; `InvalidClientTokenId` for STS, Auto Scaling, and\n")
	b.WriteString("  CloudWatch; `AuthFailure` for EC2.\n")
	b.WriteString("- **The CloudWatch error shape ID.** The fake sends `com.amazonaws.cloudwatch#<code>` in\n")
	b.WriteString("  `__type` for every code. That is the modeled form for shapes such as `InvalidNextToken`.\n")
	b.WriteString("  For codes with no shape in the model (`AccessDenied`, `Throttling`) the namespace AWS\n")
	b.WriteString("  uses is a guess. The SDK reads the code from `X-Amzn-Query-Error` in both cases.\n")
	b.WriteString("- **The Service Quotas `__type` prefix** `com.amazonaws.servicequotas#`. SSM sends the bare\n")
	b.WriteString("  code in the fake. Both forms decode.\n")
	b.WriteString("- **A reused client request token with a different payload** answers\n")
	b.WriteString("  `InvalidParameterException`. The EKS reference does not say what EKS answers.\n")
	b.WriteString("- **`DescribeUpdate` without `nodegroupName` or `addonName`** for an update of one answers\n")
	b.WriteString("  `ResourceNotFoundException`, from the reference's \"required if\" wording.\n")
	b.WriteString("- **`ResourceInUseException`** for a second update of a resource that has one in progress.\n\n")
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
