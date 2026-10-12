package awsx_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/servicequotas"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/smithy-go"

	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

const region = "us-east-1"

// noBackoff makes a retry follow its failed attempt at once, so a test of
// the attempt count does not wait.
func noBackoff(o *awsx.ProviderOptions) {
	o.Backoff = retry.BackoffDelayerFunc(func(int, error) (time.Duration, error) { return 0, nil })
}

// newServer starts a fake AWS with one cluster and returns it with a
// provider that reaches it.
func newServer(t *testing.T) (*fleettest.World, *awsxtest.Server, *awsx.Provider) {
	t.Helper()
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) {
		s.Clusters = append(s.Clusters, &fleettest.Cluster{
			Name:       "prod",
			Version:    "1.32",
			Nodegroups: []*fleettest.Nodegroup{{Name: "ng-a", Version: "1.32"}},
		})
	})
	srv := awsxtest.NewServer(world)
	t.Cleanup(srv.Close)
	return world, srv, awsx.NewProvider(srv.Config(region), noBackoff)
}

// count returns how many requests for op the server got.
func count(srv *awsxtest.Server, op fleettest.Operation) int {
	n := 0
	for _, r := range srv.Requests() {
		if r.Operation == op {
			n++
		}
	}
	return n
}

// testContext bounds every wait of a test, so that a missing handshake
// fails the test and does not hang it.
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestPolicyRequestCounts pins, for each retry policy, how many HTTP
// requests one call makes. The server's request log counts them, so a
// second retry layer anywhere would show.
func TestPolicyRequestCounts(t *testing.T) {
	policies := []awsx.Policy{awsx.Read, awsx.Mutate, awsx.Sweep, awsx.Poll}
	tests := []struct {
		name  string
		fault fleettest.Fault
		// want is the request count for Read, Mutate, Sweep, and Poll.
		want    [4]int
		wantErr func(error) bool // nil means success with a retrying policy
		pollErr bool             // Poll fails although the others succeed
	}{
		{name: "success", want: [4]int{1, 1, 1, 1}},
		{
			name:  "throttled twice, then success",
			fault: fleettest.Fault{Throttle: 2},
			want:  [4]int{3, 3, 3, 1}, pollErr: true,
		},
		{
			name:  "throttled four times, then success",
			fault: fleettest.Fault{Throttle: 4},
			want:  [4]int{5, 5, 5, 1}, pollErr: true,
		},
		{
			name:    "throttled forever",
			fault:   fleettest.Fault{Throttle: 1000},
			want:    [4]int{5, 5, 5, 1},
			wantErr: awsx.IsThrottling,
		},
		{
			name:    "access denied",
			fault:   fleettest.Fault{Deny: true},
			want:    [4]int{1, 1, 1, 1},
			wantErr: awsx.IsPermissionError,
		},
		{
			name:    "server error",
			fault:   fleettest.Fault{Code: "InternalServerException", Status: 500},
			want:    [4]int{5, 5, 5, 1},
			wantErr: awsx.IsServerError,
		},
		{
			name:    "validation error",
			fault:   fleettest.Fault{Code: "InvalidParameterException", Status: 400},
			want:    [4]int{1, 1, 1, 1},
			wantErr: awsx.IsValidation,
		},
		{
			name:    "connection reset",
			fault:   fleettest.Fault{Reset: true},
			want:    [4]int{5, 5, 5, 1},
			wantErr: awsx.IsNetworkError,
		},
	}
	for _, tt := range tests {
		for i, policy := range policies {
			t.Run(tt.name+"/"+policy.String(), func(t *testing.T) {
				world, srv, provider := newServer(t)
				world.SetFault(fleettest.EKSListClusters, tt.fault)

				_, err := provider.Clients(region, policy).EKS.ListClusters(testContext(t), &eks.ListClustersInput{})

				if got := count(srv, fleettest.EKSListClusters); got != tt.want[i] {
					t.Errorf("requests = %d, want %d", got, tt.want[i])
				}
				wantErr := tt.wantErr != nil || (tt.pollErr && policy == awsx.Poll)
				switch {
				case !wantErr && err != nil:
					t.Errorf("error = %v, want success", err)
				case wantErr && err == nil:
					t.Error("error = nil, want a failure")
				case tt.wantErr != nil && !tt.wantErr(err):
					t.Errorf("error = %v, which is not the kind this fault gives", err)
				}
			})
		}
	}
}

// A request that hangs is sent once: when the context ends, the call ends,
// and an ended context is never retried.
func TestHangIsNotRetried(t *testing.T) {
	for _, policy := range []awsx.Policy{awsx.Read, awsx.Mutate, awsx.Sweep, awsx.Poll} {
		t.Run(policy.String(), func(t *testing.T) {
			world, srv, provider := newServer(t)
			world.SetFault(fleettest.EKSListClusters, fleettest.Fault{Hang: true})
			ctx, cancel := context.WithCancel(testContext(t))
			defer cancel()

			done := make(chan error, 1)
			go func() {
				_, err := provider.Clients(region, policy).EKS.ListClusters(ctx, &eks.ListClustersInput{})
				done <- err
			}()
			if err := world.AwaitBlocked(testContext(t), 1); err != nil {
				t.Fatalf("the request did not reach the server: %v", err)
			}
			cancel()
			err := <-done

			if !errors.Is(err, context.Canceled) {
				t.Errorf("error = %v, want context.Canceled", err)
			}
			if got := count(srv, fleettest.EKSListClusters); got != 1 {
				t.Errorf("requests = %d, want 1", got)
			}
			// The server lets the request go before the test ends.
			if err := world.AwaitBlocked(testContext(t), 0); err != nil {
				t.Errorf("the cancelled request is still held: %v", err)
			}
		})
	}
}

// TestMutateRetryStartsOneUpdate: the answer to an update call is lost after
// the update started. The retry carries the same client request token, so
// the world has one update.
func TestMutateRetryStartsOneUpdate(t *testing.T) {
	tests := []struct {
		name  string
		token *string // nil lets the SDK fill the token
	}{
		{name: "token from the SDK"},
		{name: "token from the caller", token: aws.String(awsx.IdempotencyToken())},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			world, srv, provider := newServer(t)
			world.SetFault(fleettest.EKSUpdateNodegroupVersion, fleettest.Fault{LoseResponse: 1})

			out, err := provider.Clients(region, awsx.Mutate).EKS.UpdateNodegroupVersion(testContext(t), &eks.UpdateNodegroupVersionInput{
				ClusterName:        aws.String("prod"),
				NodegroupName:      aws.String("ng-a"),
				ClientRequestToken: tt.token,
			})
			if err != nil {
				t.Fatalf("UpdateNodegroupVersion: %v", err)
			}

			var sent []awsxtest.Request
			for _, r := range srv.Requests() {
				if r.Operation == fleettest.EKSUpdateNodegroupVersion {
					sent = append(sent, r)
				}
			}
			if len(sent) != 2 {
				t.Fatalf("requests = %d, want 2: the lost one and its retry", len(sent))
			}
			first, second := sent[0].Input["clientRequestToken"], sent[1].Input["clientRequestToken"]
			if first == nil || first == "" || first != second {
				t.Errorf("client request tokens = %v, %v; want one token twice", first, second)
			}
			if tt.token != nil && first != *tt.token {
				t.Errorf("client request token = %v, want the caller's %q", first, *tt.token)
			}
			var ids []string
			world.View(func(s *fleettest.State) {
				for _, u := range s.Cluster(region, "prod").Updates {
					ids = append(ids, u.ID)
				}
			})
			if len(ids) != 1 || ids[0] != aws.ToString(out.Update.Id) {
				t.Errorf("updates in the world = %v, want only %q", ids, aws.ToString(out.Update.Id))
			}
		})
	}
}

// Under Poll the lost answer is an error, and the caller's loop decides.
func TestPollDoesNotRetryALostResponse(t *testing.T) {
	world, srv, provider := newServer(t)
	world.SetFault(fleettest.EKSListClusters, fleettest.Fault{Reset: true})

	_, err := provider.Clients(region, awsx.Poll).EKS.ListClusters(testContext(t), &eks.ListClustersInput{})

	if err == nil || awsx.IsPermanentAPIError(err) || !awsx.IsRetryable(err) {
		t.Errorf("error = %v, want a transient error that a wait loop polls through", err)
	}
	if got := count(srv, fleettest.EKSListClusters); got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}
}

func TestProviderCachesClients(t *testing.T) {
	_, _, provider := newServer(t)

	read := provider.Clients(region, awsx.Read)
	if read.Region != region || read.Policy != awsx.Read {
		t.Errorf("clients are for %s with %v, want %s with Read", read.Region, read.Policy, region)
	}
	if provider.Clients(region, awsx.Read) != read {
		t.Error("a second call built the clients again")
	}
	if provider.Clients("", awsx.Read) != read {
		t.Error("the empty region is not the region of the configuration")
	}
	if provider.Region() != region {
		t.Errorf("Region() = %q, want %q", provider.Region(), region)
	}
	poll := read.With(awsx.Poll)
	if poll == read || poll.Policy != awsx.Poll || poll.Region != region {
		t.Errorf("With(Poll) = clients for %s with %v", poll.Region, poll.Policy)
	}
	if poll.With(awsx.Read) != read {
		t.Error("With(Read) is not the cached Read clients")
	}
	other := provider.Clients("eu-west-1", awsx.Read)
	if other == read || other.Region != "eu-west-1" {
		t.Errorf("clients for eu-west-1 are for %s", other.Region)
	}
}

// Every client reaches its service in the region of its Clients value.
func TestClientsReachEveryService(t *testing.T) {
	_, srv, provider := newServer(t)
	ctx := testContext(t)
	c := provider.Clients("eu-west-1", awsx.Read)

	// The fake answers some of these calls with an error for the empty
	// input. The request log is what this test reads.
	_, _ = c.EKS.ListClusters(ctx, nil)
	_, _ = c.EC2.DescribeVpcs(ctx, nil)
	_, _ = c.AutoScaling.DescribeAutoScalingGroups(ctx, nil)
	_, _ = c.SSM.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String("/none")})
	_, _ = c.CloudWatch.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
		StartTime: aws.Time(time.Unix(0, 0)), EndTime: aws.Time(time.Unix(60, 0)),
		MetricDataQueries: []cwtypes.MetricDataQuery{{Id: aws.String("q"), Expression: aws.String("1")}},
	})
	_, _ = c.ServiceQuotas.GetServiceQuota(ctx, &servicequotas.GetServiceQuotaInput{
		ServiceCode: aws.String("ec2"), QuotaCode: aws.String("L-1216C47A"),
	})
	if _, err := c.STS.GetCallerIdentity(ctx, nil); err != nil {
		t.Errorf("sts:GetCallerIdentity: %v", err)
	}

	seen := map[string]bool{}
	for _, r := range srv.Requests() {
		if r.Region != "eu-west-1" {
			t.Errorf("%s was signed for %s, want eu-west-1", r.Operation, r.Region)
		}
		seen[r.Service] = true
	}
	for _, service := range []string{"eks", "ec2", "autoscaling", "ssm", "cloudwatch", "servicequotas", "sts"} {
		if !seen[service] {
			t.Errorf("no request reached %s", service)
		}
	}
}

func TestListAll(t *testing.T) {
	world, srv, provider := newServer(t)
	world.Edit(func(s *fleettest.State) {
		for _, name := range []string{"a", "b", "c", "d"} {
			s.Clusters = append(s.Clusters, &fleettest.Cluster{Name: name, Version: "1.32"})
		}
	})
	world.SetFault(fleettest.EKSListClusters, fleettest.Fault{PageSize: 2})
	client := provider.Clients(region, awsx.Read).EKS

	got, err := awsx.ListAll(testContext(t),
		func(ctx context.Context, token *string) (*eks.ListClustersOutput, error) {
			return client.ListClusters(ctx, &eks.ListClustersInput{NextToken: token})
		},
		func(out *eks.ListClustersOutput) ([]string, *string) { return out.Clusters, out.NextToken })

	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(got) != 5 {
		t.Errorf("ListAll returned %v, want 5 clusters", got)
	}
	if n := count(srv, fleettest.EKSListClusters); n != 3 {
		t.Errorf("requests = %d, want 3 pages", n)
	}
}

func TestListAllErrors(t *testing.T) {
	denied := &ekstypes.AccessDeniedException{Message: aws.String("no")}
	tests := []struct {
		name    string
		pages   []page
		wantErr string
		wantIs  error
	}{
		{
			name:    "error on the second page",
			pages:   []page{{items: []string{"a"}, next: "t1"}, {err: denied}},
			wantIs:  denied,
			wantErr: "AccessDeniedException: no",
		},
		{
			name:    "token that does not advance",
			pages:   []page{{items: []string{"a"}, next: "t1"}, {items: []string{"b"}, next: "t1"}},
			wantErr: `the list did not advance: the next page token "t1" is the one just used`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			got, err := awsx.ListAll(context.Background(),
				func(context.Context, *string) (page, error) {
					p := tt.pages[calls]
					calls++
					return p, p.err
				},
				func(p page) ([]string, *string) { return p.items, aws.String(p.next) })

			if err == nil || err.Error() != tt.wantErr {
				t.Errorf("error = %v, want %q", err, tt.wantErr)
			}
			if tt.wantIs != nil && !errors.Is(err, tt.wantIs) {
				t.Errorf("error does not wrap %v", tt.wantIs)
			}
			// A partial list never passes as a complete one.
			if got != nil {
				t.Errorf("items = %v with an error, want none", got)
			}
		})
	}
}

type page struct {
	items []string
	next  string
	err   error
}

func TestListAllStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	_, err := awsx.ListAll(ctx,
		func(context.Context, *string) (page, error) {
			calls++
			cancel() // the caller gives up while the first page is read
			return page{items: []string{"a"}, next: "t1"}, nil
		},
		func(p page) ([]string, *string) { return p.items, aws.String(p.next) })

	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Errorf("error = %v after %d calls, want context.Canceled after 1", err, calls)
	}
}

// The classification that decides a retry also decides whether a wait loop
// stops: the two cannot disagree.
func TestIsPermanentAPIError(t *testing.T) {
	api := func(code string, fault smithy.ErrorFault) error {
		return &smithy.GenericAPIError{Code: code, Message: "x", Fault: fault}
	}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "access denied", err: api("AccessDeniedException", smithy.FaultClient), want: true},
		{name: "not found", err: api("ResourceNotFoundException", smithy.FaultClient), want: true},
		{name: "validation", err: api("InvalidParameterException", smithy.FaultClient), want: true},
		{name: "throttling", err: api("ThrottlingException", smithy.FaultClient)},
		{name: "server fault", err: api("SomethingNew", smithy.FaultServer)},
		{name: "internal failure", err: api("InternalFailure", smithy.FaultUnknown)},
		{name: "deadline", err: context.DeadlineExceeded},
		{name: "nil"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := awsx.IsPermanentAPIError(tt.err); got != tt.want {
				t.Errorf("IsPermanentAPIError() = %t, want %t", got, tt.want)
			}
			if tt.err != nil && tt.want == awsx.IsRetryable(tt.err) && !errors.Is(tt.err, context.DeadlineExceeded) {
				t.Errorf("IsRetryable() = %t for an API error that is permanent = %t", !tt.want, tt.want)
			}
		})
	}
}
