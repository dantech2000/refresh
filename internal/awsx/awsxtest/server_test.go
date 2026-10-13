package awsxtest_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	asgtypes "github.com/aws/aws-sdk-go-v2/service/autoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/servicequotas"
	sqtypes "github.com/aws/aws-sdk-go-v2/service/servicequotas/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
	"github.com/aws/smithy-go/encoding/cbor"
	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// TestRequestLog checks that the log keeps the order of the requests,
// decodes the input of each protocol, and cannot be changed by a reader.
func TestRequestLog(t *testing.T) {
	f := newFixture(t)
	ctx := testContext(t)
	c := f.clients

	if _, err := c.eks.ListInsights(ctx, &eks.ListInsightsInput{
		ClusterName: aws.String("prod"), MaxResults: aws.Int32(50),
		Filter: &ekstypes.InsightsFilter{Statuses: []ekstypes.InsightStatusValue{ekstypes.InsightStatusValueError}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.eks.UpdateNodegroupVersion(ctx, &eks.UpdateNodegroupVersionInput{
		ClusterName: aws.String("prod"), NodegroupName: aws.String("ng-a"), Version: aws.String("1.32"),
		Force: true, ClientRequestToken: aws.String(token),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		MaxResults: aws.Int32(5),
		Filters: []ec2types.Filter{
			{Name: aws.String("tag:eks:eks-cluster-name"), Values: []string{"prod"}},
			{Name: aws.String("tag-key"), Values: []string{"karpenter.sh/nodepool", "karpenter.sh/provisioner-name"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.asg.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{AutoScalingGroupNames: []string{"eks-ng-a"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ssm.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(parameter)}); err != nil {
		t.Fatal(err)
	}

	want := []awsxtest.Request{
		{
			ID: 1, Operation: fleettest.EKSListInsights, Service: "eks", Region: region,
			Input: map[string]any{"clusterName": "prod", "maxResults": float64(50), "filter": map[string]any{"statuses": []any{"ERROR"}}},
		},
		{
			ID: 2, Operation: fleettest.EKSUpdateNodegroupVersion, Service: "eks", Region: region, Mutates: true,
			Input: map[string]any{"clusterName": "prod", "nodegroupName": "ng-a", "version": "1.32", "force": true, "clientRequestToken": token},
		},
		{
			ID: 3, Operation: fleettest.EC2DescribeInstances, Service: "ec2", Region: region,
			Input: map[string]any{
				"MaxResults":       "5",
				"Filter.1.Name":    "tag:eks:eks-cluster-name",
				"Filter.1.Value.1": "prod",
				"Filter.2.Name":    "tag-key",
				"Filter.2.Value.1": "karpenter.sh/nodepool",
				"Filter.2.Value.2": "karpenter.sh/provisioner-name",
			},
		},
		{
			ID: 4, Operation: fleettest.AutoScalingDescribeAutoScalingGroups, Service: "autoscaling", Region: region,
			Input: map[string]any{"AutoScalingGroupNames.member.1": "eks-ng-a"},
		},
		{
			ID: 5, Operation: fleettest.SSMGetParameter, Service: "ssm", Region: region,
			Input: map[string]any{"Name": parameter},
		},
	}
	got := f.server.Requests()
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("request log (-want +got):\n%s", diff)
	}

	// A reader that changes what it got does not change the log.
	got[0].Input["clusterName"] = "changed"
	if filter, ok := got[0].Input["filter"].(map[string]any); ok {
		filter["statuses"] = []any{"changed"}
		delete(filter, "categories")
	}
	if diff := cmp.Diff(want, f.server.Requests()); diff != "" {
		t.Errorf("the log changed after a reader wrote to its copy (-want +got):\n%s", diff)
	}
}

// TestUpdateFollowsTheClock drives a nodegroup roll through the SDK: the
// update and the nodegroup change only when the world's clock passes the
// scripted duration.
func TestUpdateFollowsTheClock(t *testing.T) {
	const duration = 10 * time.Minute
	tests := []struct {
		name          string
		script        fleettest.UpdateScript
		wantStatus    ekstypes.UpdateStatus
		wantRelease   string
		wantErrors    int
		wantNodegroup ekstypes.NodegroupStatus
		wantIssue     ekstypes.NodegroupIssueCode
		wantModified  bool
	}{
		{
			name: "successful", script: fleettest.UpdateScript{After: duration},
			wantStatus: ekstypes.UpdateStatusSuccessful, wantRelease: "1.32.0-20260101", wantNodegroup: ekstypes.NodegroupStatusActive,
			wantIssue: "AsgInstanceLaunchFailures", wantModified: true,
		},
		{
			name: "failed", script: fleettest.UpdateScript{After: duration, Result: fleettest.UpdateFailed},
			wantStatus: ekstypes.UpdateStatusFailed, wantRelease: "1.32.0-20250101", wantErrors: 1, wantNodegroup: ekstypes.NodegroupStatusActive,
			wantIssue: "AsgInstanceLaunchFailures",
		},
		{
			name: "failed part way", script: fleettest.UpdateScript{
				After: duration, Result: fleettest.UpdateFailed, ResourceStatus: fleettest.StatusDegraded,
				ResourceIssues: []fleettest.Issue{{Code: "NodeCreationFailure", Message: "nodes did not join"}},
			},
			wantStatus: ekstypes.UpdateStatusFailed, wantRelease: "1.32.0-20250101", wantErrors: 1, wantNodegroup: ekstypes.NodegroupStatusDegraded,
			wantIssue: ekstypes.NodegroupIssueCodeNodeCreationFailure,
		},
		{
			name: "never", script: fleettest.UpdateScript{Never: true},
			wantStatus: ekstypes.UpdateStatusInProgress, wantRelease: "1.32.0-20250101", wantNodegroup: ekstypes.NodegroupStatusUpdating,
			wantIssue: "AsgInstanceLaunchFailures",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			ctx := testContext(t)
			f.world.Edit(func(s *fleettest.State) {
				s.Cluster(region, "prod").Nodegroup("ng-a").UpdateScripts = []fleettest.UpdateScript{tt.script}
			})
			describe := func() (*ekstypes.Update, *ekstypes.Nodegroup) {
				t.Helper()
				u, err := f.clients.eks.DescribeUpdate(ctx, &eks.DescribeUpdateInput{
					Name: aws.String("prod"), NodegroupName: aws.String("ng-a"), UpdateId: aws.String(firstUpdate),
				})
				if err != nil {
					t.Fatal(err)
				}
				ng, err := f.clients.eks.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{ClusterName: aws.String("prod"), NodegroupName: aws.String("ng-a")})
				if err != nil {
					t.Fatal(err)
				}
				return u.Update, ng.Nodegroup
			}

			started, err := f.clients.eks.UpdateNodegroupVersion(ctx, &eks.UpdateNodegroupVersionInput{ClusterName: aws.String("prod"), NodegroupName: aws.String("ng-a")})
			if err != nil {
				t.Fatal(err)
			}
			if got := aws.ToString(started.Update.Id); got != firstUpdate {
				t.Errorf("update ID = %q, want %q", got, firstUpdate)
			}

			f.world.Advance(duration - time.Second)
			u, ng := describe()
			if u.Status != ekstypes.UpdateStatusInProgress || ng.Status != ekstypes.NodegroupStatusUpdating {
				t.Errorf("before the update is due: update %s, nodegroup %s; want InProgress, UPDATING", u.Status, ng.Status)
			}
			before := aws.ToTime(ng.ModifiedAt)

			f.world.Advance(time.Second)
			u, ng = describe()
			if u.Status != tt.wantStatus || len(u.Errors) != tt.wantErrors {
				t.Errorf("update = %s with %d errors, want %s with %d", u.Status, len(u.Errors), tt.wantStatus, tt.wantErrors)
			}
			if got := aws.ToString(ng.ReleaseVersion); got != tt.wantRelease {
				t.Errorf("release version = %q, want %q", got, tt.wantRelease)
			}
			if ng.Status != tt.wantNodegroup {
				t.Errorf("nodegroup status = %s, want %s", ng.Status, tt.wantNodegroup)
			}
			if len(ng.Health.Issues) != 1 || ng.Health.Issues[0].Code != tt.wantIssue {
				t.Errorf("nodegroup health = %+v, want one %s issue", ng.Health.Issues, tt.wantIssue)
			}
			if got := aws.ToTime(ng.ModifiedAt).Sub(before) == duration; got != tt.wantModified {
				t.Errorf("modifiedAt moved by the update's duration = %t, want %t", got, tt.wantModified)
			}
		})
	}
}

// TestAPIErrors checks the errors the server answers on its own, without a
// fault, and the Go type the SDK decodes each into.
func TestAPIErrors(t *testing.T) {
	f := newFixture(t)
	ctx := testContext(t)
	c := f.clients
	f.world.Edit(func(s *fleettest.State) { s.DefaultUpdate = fleettest.UpdateScript{Never: true} })
	roll := func(tok, version string) error {
		_, err := c.eks.UpdateNodegroupVersion(ctx, &eks.UpdateNodegroupVersionInput{
			ClusterName: aws.String("prod"), NodegroupName: aws.String("ng-a"), Version: aws.String(version), ClientRequestToken: aws.String(tok),
		})
		return err
	}
	if err := roll(token, "1.32"); err != nil {
		t.Fatal(err)
	}
	if err := roll(token, "1.32"); err != nil {
		t.Errorf("a retry with the same client request token: %v", err)
	}
	notFound := wireError{"ResourceNotFoundException", is[*ekstypes.ResourceNotFoundException]}
	invalid := wireError{"InvalidParameterException", is[*ekstypes.InvalidParameterException]}

	tests := []struct {
		name string
		call func() error
		want wireError
	}{
		{"cluster in another region", func() error {
			_, err := c.eks.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String("elsewhere")})
			return err
		}, notFound},
		{"unknown nodegroup", func() error {
			_, err := c.eks.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{ClusterName: aws.String("prod"), NodegroupName: aws.String("nope")})
			return err
		}, notFound},
		{"nodegroup update described without its nodegroup", func() error {
			_, err := c.eks.DescribeUpdate(ctx, &eks.DescribeUpdateInput{Name: aws.String("prod"), UpdateId: aws.String(firstUpdate)})
			return err
		}, notFound},
		{"second update of a busy nodegroup", func() error { return roll(token+"x", "1.32") },
			wireError{"ResourceInUseException", is[*ekstypes.ResourceInUseException]}},
		{"client request token reused with another version", func() error { return roll(token, "1.33") }, invalid},
		{"add-on update while the control plane updates", func() error {
			if _, err := c.eks.UpdateClusterVersion(ctx, &eks.UpdateClusterVersionInput{Name: aws.String("staging"), Version: aws.String("1.34")}); err != nil {
				return fmt.Errorf("starting the control-plane update: %w", err)
			}
			_, err := c.eks.UpdateAddon(ctx, &eks.UpdateAddonInput{ClusterName: aws.String("staging"), AddonName: aws.String("kube-proxy")})
			return err
		}, wireError{"ResourceInUseException", is[*ekstypes.ResourceInUseException]}},
		{"short client request token", func() error {
			_, err := c.eks.UpdateClusterVersion(ctx, &eks.UpdateClusterVersionInput{
				Name: aws.String("prod"), Version: aws.String("1.33"), ClientRequestToken: aws.String("0123456789abcdef0123456789abcdef"),
			})
			return err
		}, invalid},
		{"EKS page token the server did not issue", func() error {
			_, err := c.eks.ListClusters(ctx, &eks.ListClustersInput{NextToken: aws.String("bogus")})
			return err
		}, invalid},
		{"unknown instance", func() error {
			_, err := c.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{"i-gone"}})
			return err
		}, wireError{"InvalidInstanceID.NotFound", generic}},
		{"filter the fake does not model", func() error {
			_, err := c.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
				Filters: []ec2types.Filter{{Name: aws.String("vpc-id"), Values: []string{"vpc-1"}}},
			})
			return err
		}, wireError{"InvalidParameterValue", generic}},
		{"unknown SSM parameter", func() error {
			_, err := c.ssm.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String("/nope")})
			return err
		}, wireError{"ParameterNotFound", is[*ssmtypes.ParameterNotFound]}},
		{"unknown quota", func() error {
			_, err := c.sq.GetServiceQuota(ctx, &servicequotas.GetServiceQuotaInput{ServiceCode: aws.String("ec2"), QuotaCode: aws.String("L-NOPE")})
			return err
		}, wireError{"NoSuchResourceException", is[*sqtypes.NoSuchResourceException]}},
		{"Auto Scaling page token the server did not issue", func() error {
			_, err := c.asg.DescribeScalingActivities(ctx, &autoscaling.DescribeScalingActivitiesInput{NextToken: aws.String("bogus")})
			return err
		}, wireError{"InvalidNextToken", is[*asgtypes.InvalidNextToken]}},
		{"CloudWatch page token the server did not issue", func() error {
			_, err := c.cw.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
				StartTime: aws.Time(time.Unix(0, 0)), EndTime: aws.Time(time.Unix(60, 0)), NextToken: aws.String("bogus"),
				MetricDataQueries: []cwtypes.MetricDataQuery{{Id: aws.String("q"), Expression: aws.String("1")}},
			})
			return err
		}, wireError{"InvalidNextToken", is[*cwtypes.InvalidNextToken]}},
		{"operation the fake does not model", func() error {
			_, err := c.eks.ListFargateProfiles(ctx, &eks.ListFargateProfilesInput{ClusterName: aws.String("prod")})
			return err
		}, wireError{"UnsupportedOperation", generic}},
		{"operation of another protocol the fake does not model", func() error {
			_, err := c.ec2.DescribeRegions(ctx, &ec2.DescribeRegionsInput{})
			return err
		}, wireError{"UnsupportedOperation", generic}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkError(t, tt.call(), tt.want)
		})
	}

	updates, err := c.eks.ListUpdates(ctx, &eks.ListUpdatesInput{Name: aws.String("prod"), NodegroupName: aws.String("ng-a")})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{firstUpdate}, updates.UpdateIds); diff != "" {
		t.Errorf("the retried and the refused calls started updates (-want +got):\n%s", diff)
	}
}

// httpStatus returns the HTTP status of the response behind an SDK error.
func httpStatus(err error) int {
	var re interface{ HTTPStatusCode() int }
	if errors.As(err, &re) {
		return re.HTTPStatusCode()
	}
	return 0
}

// TestInjectedCodeStatus checks that an injected error code gets the HTTP
// status the service uses for it, so the SDK classifies it as it does the
// real error.
func TestInjectedCodeStatus(t *testing.T) {
	ctx := context.Background()
	listClusters := func(c *clients) error {
		_, err := c.eks.ListClusters(ctx, &eks.ListClustersInput{})
		return err
	}
	tests := []struct {
		op        fleettest.Operation
		fault     fleettest.Fault
		call      func(c *clients) error
		want      wireError
		status    int
		retryable bool
	}{
		{fleettest.EKSListClusters, fleettest.Fault{Code: "ServerException"}, listClusters,
			wireError{"ServerException", is[*ekstypes.ServerException]}, 500, true},
		{fleettest.EKSListClusters, fleettest.Fault{Code: "ServiceUnavailableException"}, listClusters,
			wireError{"ServiceUnavailableException", is[*ekstypes.ServiceUnavailableException]}, 503, true},
		{fleettest.EKSListClusters, fleettest.Fault{Code: "InvalidRequestException"}, listClusters,
			wireError{"InvalidRequestException", is[*ekstypes.InvalidRequestException]}, 400, false},
		{fleettest.EKSListClusters, fleettest.Fault{Code: "Teapot", Status: 418}, listClusters, wireError{"Teapot", generic}, 418, false},
		{fleettest.EKSListClusters, fleettest.Fault{Code: "Teapot", Status: 502}, listClusters, wireError{"Teapot", generic}, 502, true},
		{fleettest.EC2DescribeVpcs, fleettest.Fault{Code: "InternalError"}, func(c *clients) error {
			_, err := c.ec2.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{})
			return err
		}, wireError{"InternalError", generic}, 500, true},
		{fleettest.EC2DescribeVpcs, fleettest.Fault{Code: "OptInRequired"}, func(c *clients) error {
			_, err := c.ec2.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{})
			return err
		}, wireError{"OptInRequired", generic}, 401, false},
		{fleettest.SSMGetParameter, fleettest.Fault{Code: "InternalServerError"}, func(c *clients) error {
			_, err := c.ssm.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(parameter)})
			return err
		}, wireError{"InternalServerError", is[*ssmtypes.InternalServerError]}, 500, true},
		{fleettest.STSGetCallerIdentity, fleettest.Fault{Code: "ExpiredToken"}, func(c *clients) error {
			_, err := c.sts.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
			return err
		}, wireError{"ExpiredToken", generic}, 403, false},
		{fleettest.STSGetCallerIdentity, fleettest.Fault{Code: "ExpiredTokenException"}, func(c *clients) error {
			_, err := c.sts.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
			return err
		}, wireError{"ExpiredTokenException", generic}, 403, false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s/%s/%d", tt.op, tt.fault.Code, tt.fault.Status), func(t *testing.T) {
			f := newFixture(t)
			f.world.SetFault(tt.op, tt.fault)
			err := tt.call(f.once)
			checkError(t, err, tt.want)
			if got := httpStatus(err); got != tt.status {
				t.Errorf("HTTP status = %d, want %d", got, tt.status)
			}
			if got := retry.NewStandard().IsErrorRetryable(err); got != tt.retryable {
				t.Errorf("the SDK retries it = %t, want %t", got, tt.retryable)
			}
		})
	}
}

// TestErrorEnvelopes checks the bytes of one error response per service,
// below the SDK's decoder, which accepts more than AWS sends.
func TestErrorEnvelopes(t *testing.T) {
	const denied = "User: arn:aws:iam::123456789012:user/fake is not authorized to perform this action"
	const requestID = "00000000-0000-4000-8000-000000000000"
	tests := []struct {
		op     fleettest.Operation
		status int
		// header holds the headers that carry protocol meaning.
		header map[string]string
		// body is the expected body. For CloudWatch it is the decoded CBOR.
		body string
	}{
		{
			op: fleettest.EKSListClusters, status: 403,
			header: map[string]string{"Content-Type": "application/json", "X-Amzn-Errortype": "AccessDeniedException"},
			body:   `{"message":"` + denied + `"}` + "\n",
		},
		{
			op: fleettest.STSGetCallerIdentity, status: 403, header: map[string]string{"Content-Type": "text/xml"},
			body: `<ErrorResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><Error><Type>Sender</Type><Code>AccessDenied</Code><Message>` + denied +
				`</Message></Error><RequestId>` + requestID + `</RequestId></ErrorResponse>`,
		},
		{
			op: fleettest.AutoScalingDescribeScalingActivities, status: 403, header: map[string]string{"Content-Type": "text/xml"},
			body: `<ErrorResponse xmlns="http://autoscaling.amazonaws.com/doc/2011-01-01/"><Error><Type>Sender</Type><Code>AccessDenied</Code><Message>` + denied +
				`</Message></Error><RequestId>` + requestID + `</RequestId></ErrorResponse>`,
		},
		{
			op: fleettest.EC2DescribeVpcs, status: 403, header: map[string]string{"Content-Type": "text/xml"},
			body: `<Response><Errors><Error><Code>UnauthorizedOperation</Code><Message>` + denied +
				`</Message></Error></Errors><RequestID>` + requestID + `</RequestID></Response>`,
		},
		{
			op: fleettest.SSMGetParameter, status: 400, header: map[string]string{"Content-Type": "application/x-amz-json-1.1"},
			body: `{"__type":"AccessDeniedException","message":"` + denied + `"}` + "\n",
		},
		{
			op: fleettest.ServiceQuotasGetServiceQuota, status: 400, header: map[string]string{"Content-Type": "application/x-amz-json-1.1"},
			body: `{"__type":"com.amazonaws.servicequotas#AccessDeniedException","message":"` + denied + `"}` + "\n",
		},
		{
			op: fleettest.CloudWatchGetMetricData, status: 403,
			header: map[string]string{"Content-Type": "application/cbor", "Smithy-Protocol": "rpc-v2-cbor", "X-Amzn-Query-Error": "AccessDenied;Sender"},
			body:   fmt.Sprint(cbor.Map{"__type": cbor.String("com.amazonaws.cloudwatch#AccessDenied"), "message": cbor.String(denied)}),
		},
	}
	cases := map[fleettest.Operation]opCase{}
	for _, tc := range matrix() {
		cases[tc.op] = tc
	}
	for _, tt := range tests {
		t.Run(string(tt.op), func(t *testing.T) {
			f := newFixture(t)
			f.world.SetFault(tt.op, fleettest.Fault{Deny: true})
			if _, _, err := cases[tt.op].run(testContext(t), f.once); err == nil {
				t.Fatal("the denied call succeeded")
			}
			wire := f.wire.all()
			if len(wire) != 1 {
				t.Fatalf("got %d responses, want 1", len(wire))
			}
			got := wire[0]
			if got.Status != tt.status {
				t.Errorf("status = %d, want %d", got.Status, tt.status)
			}
			for name, want := range tt.header {
				if h := got.Header.Get(name); h != want {
					t.Errorf("header %s = %q, want %q", name, h, want)
				}
			}
			body := string(got.Body)
			if tt.header["Smithy-Protocol"] != "" {
				v, err := cbor.Decode(got.Body)
				if err != nil {
					t.Fatalf("the body is not CBOR: %v", err)
				}
				body = fmt.Sprint(v)
			}
			if diff := cmp.Diff(tt.body, body); diff != "" {
				t.Errorf("body (-want +got):\n%s", diff)
			}
		})
	}
}

// TestScopedFaults checks faults that apply to a region, to a resource, and
// to every request, through the SDK.
func TestScopedFaults(t *testing.T) {
	ctx := context.Background()
	listClusters := func(c *clients) error {
		_, err := c.eks.ListClusters(ctx, &eks.ListClustersInput{})
		return err
	}
	describe := func(nodegroup string) func(c *clients) error {
		return func(c *clients) error {
			_, err := c.eks.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{ClusterName: aws.String("prod"), NodegroupName: aws.String(nodegroup)})
			return err
		}
	}
	identity := func(c *clients) error {
		_, err := c.sts.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
		return err
	}
	vpcs := func(c *clients) error {
		_, err := c.ec2.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{})
		return err
	}
	closed := fleettest.Scope{Region: otherRegion}

	tests := []struct {
		name   string
		scope  fleettest.Scope
		fault  fleettest.Fault
		region string
		call   func(c *clients) error
		// want is the error code, "" for success, or "connection" for an
		// error that is not an API error.
		want string
	}{
		{"closed region, EKS", closed, fleettest.Fault{Unauthenticated: true}, otherRegion, listClusters, "UnrecognizedClientException"},
		{"closed region, STS", closed, fleettest.Fault{Unauthenticated: true}, otherRegion, identity, "InvalidClientTokenId"},
		{"closed region, EC2", closed, fleettest.Fault{Unauthenticated: true}, otherRegion, vpcs, "AuthFailure"},
		{"closed region, another region answers", closed, fleettest.Fault{Unauthenticated: true}, region, listClusters, ""},
		{"region that is not opted in", closed, fleettest.Fault{Code: "OptInRequired"}, otherRegion, vpcs, "OptInRequired"},
		{"region that drops connections", closed, fleettest.Fault{Reset: true}, otherRegion, listClusters, "connection"},
		{"region that drops connections, another region answers", closed, fleettest.Fault{Reset: true}, region, listClusters, ""},
		{"credentials rejected everywhere", fleettest.Scope{}, fleettest.Fault{Unauthenticated: true}, region, identity, "InvalidClientTokenId"},
		{"one service", fleettest.Scope{Service: "ec2"}, fleettest.Fault{Deny: true}, region, vpcs, "UnauthorizedOperation"},
		{"one service, another answers", fleettest.Scope{Service: "ec2"}, fleettest.Fault{Deny: true}, region, listClusters, ""},
		{"one nodegroup", fleettest.Scope{Nodegroup: "ng-b"}, fleettest.Fault{Deny: true}, region, describe("ng-b"), "AccessDeniedException"},
		{"one nodegroup, another answers", fleettest.Scope{Nodegroup: "ng-b"}, fleettest.Fault{Deny: true}, region, describe("ng-a"), ""},
		{"one cluster", fleettest.Scope{Cluster: "prod"}, fleettest.Fault{Deny: true}, region, describe("ng-a"), "AccessDeniedException"},
		{"one cluster, a call that names none answers", fleettest.Scope{Cluster: "prod"}, fleettest.Fault{Deny: true}, region, listClusters, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.world.SetScopedFault(tt.scope, tt.fault)
			cfg := f.server.Config(tt.region)
			cfg.Retryer = func() aws.Retryer { return aws.NopRetryer{} }
			err := tt.call(newClients(cfg))

			var apiErr smithy.APIError
			got := ""
			switch {
			case errors.As(err, &apiErr):
				got = apiErr.ErrorCode()
			case err != nil:
				got = "connection"
			}
			if got != tt.want {
				t.Errorf("got %q (%v), want %q", got, err, tt.want)
			}
		})
	}
}

// TestUnmodeledUpdateFields proves that an Update request with a field the
// world does not model fails with an error that names the field, so nothing
// the product starts to send is dropped in silence.
func TestUnmodeledUpdateFields(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		field string
		call  func(c *clients) error
	}{
		{"launchTemplate of UpdateNodegroupVersion", func(c *clients) error {
			_, err := c.eks.UpdateNodegroupVersion(ctx, &eks.UpdateNodegroupVersionInput{
				ClusterName: aws.String("prod"), NodegroupName: aws.String("ng-a"),
				LaunchTemplate: &ekstypes.LaunchTemplateSpecification{Id: aws.String("lt-1"), Version: aws.String("3")},
			})
			return err
		}},
		{"labels of UpdateNodegroupConfig", func(c *clients) error {
			_, err := c.eks.UpdateNodegroupConfig(ctx, &eks.UpdateNodegroupConfigInput{
				ClusterName: aws.String("prod"), NodegroupName: aws.String("ng-a"),
				Labels: &ekstypes.UpdateLabelsPayload{AddOrUpdateLabels: map[string]string{"a": "b"}},
			})
			return err
		}},
		{"updateConfig of UpdateNodegroupConfig", func(c *clients) error {
			_, err := c.eks.UpdateNodegroupConfig(ctx, &eks.UpdateNodegroupConfigInput{
				ClusterName: aws.String("prod"), NodegroupName: aws.String("ng-a"),
				UpdateConfig: &ekstypes.NodegroupUpdateConfig{MaxUnavailable: aws.Int32(1)},
			})
			return err
		}},
		{"resolveConflicts of UpdateAddon", func(c *clients) error {
			_, err := c.eks.UpdateAddon(ctx, &eks.UpdateAddonInput{
				ClusterName: aws.String("prod"), AddonName: aws.String("vpc-cni"), ResolveConflicts: ekstypes.ResolveConflictsOverwrite,
			})
			return err
		}},
		{"serviceAccountRoleArn of UpdateAddon", func(c *clients) error {
			_, err := c.eks.UpdateAddon(ctx, &eks.UpdateAddonInput{
				ClusterName: aws.String("prod"), AddonName: aws.String("vpc-cni"), ServiceAccountRoleArn: aws.String("arn:aws:iam::123456789012:role/r"),
			})
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			f := newFixture(t)
			err := tt.call(f.once)
			checkError(t, err, wireError{"InvalidParameterException", is[*ekstypes.InvalidParameterException]})
			if want := "awsxtest does not model field " + tt.field; err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("error = %v, want it to say %q", err, want)
			}
			var updates int
			f.world.View(func(s *fleettest.State) { updates = len(s.Cluster(region, "prod").Updates) })
			if updates != 2 { // the two seeded ones
				t.Errorf("the refused request started an update: the cluster has %d", updates)
			}
		})
	}
}

// TestLostResponse proves that a call whose response is lost after the
// update was started makes one update when the SDK retries it: the retry
// carries the same client request token.
func TestLostResponse(t *testing.T) {
	f := newFixture(t)
	ctx := testContext(t)
	f.world.SetFault(fleettest.EKSUpdateNodegroupVersion, fleettest.Fault{LoseResponse: 1})

	// No token is set: the SDK generates one for the call.
	out, err := f.clients.eks.UpdateNodegroupVersion(ctx, &eks.UpdateNodegroupVersionInput{ClusterName: aws.String("prod"), NodegroupName: aws.String("ng-a")})
	if err != nil {
		t.Fatalf("the retried call: %v", err)
	}
	if got := aws.ToString(out.Update.Id); got != firstUpdate {
		t.Errorf("update ID = %q, want %q", got, firstUpdate)
	}

	sent := f.requests(fleettest.EKSUpdateNodegroupVersion)
	if len(sent) != 2 {
		t.Fatalf("the client sent %d requests, want 2", len(sent))
	}
	first, second := sent[0].Input["clientRequestToken"], sent[1].Input["clientRequestToken"]
	if first != second || first == nil || first == "" {
		t.Errorf("client request tokens = %v, %v; want the same token twice", first, second)
	}
	var updates int
	f.world.View(func(s *fleettest.State) {
		for _, u := range s.Cluster(region, "prod").Updates {
			if u.Nodegroup == "ng-a" {
				updates++
			}
		}
	})
	if updates != 1 {
		t.Errorf("the world has %d updates of ng-a, want 1", updates)
	}
}

// TestLostResponseNeedsSuccess proves that a request the operation refuses
// does not use up a lost response: the fault waits for a request that
// committed.
func TestLostResponseNeedsSuccess(t *testing.T) {
	f := newFixture(t)
	ctx := testContext(t)
	f.world.SetFault(fleettest.EKSUpdateNodegroupVersion, fleettest.Fault{LoseResponse: 1})
	roll := func(c *clients, nodegroup string) error {
		_, err := c.eks.UpdateNodegroupVersion(ctx, &eks.UpdateNodegroupVersionInput{ClusterName: aws.String("prod"), NodegroupName: aws.String(nodegroup)})
		return err
	}

	checkError(t, roll(f.once, "nope"), wireError{"ResourceNotFoundException", is[*ekstypes.ResourceNotFoundException]})
	var apiErr smithy.APIError
	if err := roll(f.once, "ng-a"); err == nil || errors.As(err, &apiErr) {
		t.Errorf("the first roll that committed returned %v, want a connection error", err)
	}
	if err := roll(f.once, "ng-b"); err != nil {
		t.Errorf("a roll after the lost response: %v", err)
	}
}

// TestUpdatePayload proves that every field the legacy product sends in an
// Update call reaches the world, and that a client request token replays
// the first answer whatever happened to the resource since.
func TestUpdatePayload(t *testing.T) {
	f := newFixture(t)
	ctx := testContext(t)
	c := f.clients
	other := token + "x"
	configure := func(tok, values string) (*eks.UpdateAddonOutput, error) {
		return c.eks.UpdateAddon(ctx, &eks.UpdateAddonInput{
			ClusterName: aws.String("prod"), AddonName: aws.String("vpc-cni"), AddonVersion: aws.String("v1.19.0-eksbuild.1"),
			ConfigurationValues: aws.String(values), ClientRequestToken: aws.String(tok),
		})
	}
	// No version: the roll keeps the nodegroup's own, 1.31.
	rollB := func() (*eks.UpdateNodegroupVersionOutput, error) {
		return c.eks.UpdateNodegroupVersion(ctx, &eks.UpdateNodegroupVersionInput{
			ClusterName: aws.String("prod"), NodegroupName: aws.String("ng-b"), Force: true, ClientRequestToken: aws.String(token),
		})
	}

	if _, err := configure(token, `{"env":{"A":"1"}}`); err != nil {
		t.Fatal(err)
	}
	_, err := configure(token, `{"env":{"A":"2"}}`)
	checkError(t, err, wireError{"InvalidParameterException", is[*ekstypes.InvalidParameterException]})

	first, err := rollB()
	if err != nil {
		t.Fatal(err)
	}
	// Another update moves ng-b to 1.32. The replay must still answer.
	if _, err := c.eks.UpdateNodegroupVersion(ctx, &eks.UpdateNodegroupVersionInput{
		ClusterName: aws.String("prod"), NodegroupName: aws.String("ng-b"), Version: aws.String("1.32"), ClientRequestToken: aws.String(other),
	}); err != nil {
		t.Fatal(err)
	}
	again, err := rollB()
	if err != nil {
		t.Fatalf("the replay after the nodegroup changed: %v", err)
	}
	if diff := cmp.Diff(updateOf(first.Update), updateOf(again.Update)); diff != "" {
		t.Errorf("the replay answered another update (-first +replay):\n%s", diff)
	}

	if _, err := c.eks.UpdateClusterVersion(ctx, &eks.UpdateClusterVersionInput{
		Name: aws.String("prod"), Version: aws.String("1.31"), Force: true, ClientRequestToken: aws.String(token),
		RollbackConfig: &ekstypes.RollbackConfig{TimeoutMinutes: aws.Int32(120)},
	}); err != nil {
		t.Fatal(err)
	}

	type sent struct {
		Type    fleettest.UpdateType
		Force   bool
		Timeout int32
	}
	got := map[string]sent{}
	var configuration string
	f.world.View(func(s *fleettest.State) {
		cl := s.Cluster(region, "prod")
		configuration = cl.Addon("vpc-cni").ConfigurationValues
		for _, u := range cl.Updates[2:] { // after the two seeded ones
			got[u.ID[len(u.ID)-1:]+" "+u.Nodegroup+u.Addon] = sent{u.Type, u.Force, u.RollbackTimeoutMinutes}
		}
	})
	want := map[string]sent{
		"1 vpc-cni": {Type: fleettest.TypeAddonUpdate},
		"2 ng-b":    {Type: fleettest.TypeVersionUpdate, Force: true},
		"3 ng-b":    {Type: fleettest.TypeVersionUpdate},
		"4 ":        {Type: fleettest.TypeVersionRollback, Force: true, Timeout: 120},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("updates in the world (-want +got):\n%s", diff)
	}
	if configuration != `{"env":{"A":"1"}}` {
		t.Errorf("add-on configuration = %s, want the values of the first request", configuration)
	}
}

// TestPeakConcurrency holds requests with a delay to give the counters a
// known peak.
func TestPeakConcurrency(t *testing.T) {
	f := newFixture(t)
	ctx := testContext(t)
	f.world.SetFault(fleettest.EKSListClusters, fleettest.Fault{Delay: time.Second})
	f.world.SetFault(fleettest.EC2DescribeVpcs, fleettest.Fault{Delay: time.Second})

	other := f.in(otherRegion)
	var wg sync.WaitGroup
	list := func(c *clients) {
		wg.Go(func() {
			if _, err := c.eks.ListClusters(ctx, &eks.ListClustersInput{}); err != nil {
				t.Errorf("ListClusters: %v", err)
			}
		})
	}
	for range 3 {
		list(f.clients)
	}
	for range 2 {
		list(other)
	}
	wg.Go(func() {
		if _, err := f.clients.ec2.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{}); err != nil {
			t.Errorf("DescribeVpcs: %v", err)
		}
	})
	if err := f.world.AwaitBlocked(ctx, 6); err != nil {
		t.Fatal(err)
	}
	f.world.Advance(time.Second)
	wg.Wait()
	// One more request, alone: it must not raise any peak.
	f.world.SetFault(fleettest.EKSListClusters, fleettest.Fault{})
	if _, err := f.clients.eks.ListClusters(ctx, &eks.ListClustersInput{}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		service, region string
		want            int
	}{
		{"eks", region, 3},
		{"eks", otherRegion, 2},
		{"eks", "", 5},
		{"ec2", region, 1},
		{"", region, 4},
		{"", "", 6},
		{"ssm", "", 0},
		{"eks", emptyRegion, 0},
	}
	for _, tt := range tests {
		if got := f.server.PeakConcurrency(tt.service, tt.region); got != tt.want {
			t.Errorf("PeakConcurrency(%q, %q) = %d, want %d", tt.service, tt.region, got, tt.want)
		}
	}
}

// TestConcurrentEditAndDescribe changes the maps and slices of a cluster
// while the server encodes them. It fails under the race detector if a
// response shares memory with the world.
func TestConcurrentEditAndDescribe(t *testing.T) {
	f := newFixture(t)
	ctx := testContext(t)
	const rounds = 100

	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range rounds {
			f.world.Edit(func(s *fleettest.State) {
				c := s.Cluster(region, "prod")
				c.Tags["round"] = fmt.Sprint(i)
				c.LogTypes[0] = fmt.Sprint("api", i)
				c.SubnetIDs = append(c.SubnetIDs[:1], fmt.Sprint("subnet-", i))
				c.HealthIssues[0].ResourceIDs[0] = fmt.Sprint("subnet-", i)
				ng := c.Nodegroup("ng-a")
				ng.Labels["round"] = fmt.Sprint(i)
				ng.InstanceTypes[0] = fmt.Sprint("m5.", i)
				ng.Subnets[0] = fmt.Sprint("subnet-", i)
				c.Insights[0].AdditionalInfo["round"] = fmt.Sprint(i)
				c.Updates[1].Errors[0].ResourceIDs[0] = fmt.Sprint("prod-", i)
				s.Instances[0].Tags["round"] = fmt.Sprint(i)
				s.AutoScalingGroups[0].InstanceIDs[0] = fmt.Sprint("i-", i)
				s.Metrics[0].Values[0] = float64(i)
			})
		}
	})
	read := func(name string, call func() error) {
		wg.Go(func() {
			for range rounds {
				if err := call(); err != nil {
					t.Errorf("%s: %v", name, err)
					return
				}
			}
		})
	}
	c := f.clients
	read("DescribeCluster", func() error {
		_, err := c.eks.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String("prod")})
		return err
	})
	read("DescribeNodegroup", func() error {
		_, err := c.eks.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{ClusterName: aws.String("prod"), NodegroupName: aws.String("ng-a")})
		return err
	})
	read("DescribeInsight", func() error {
		_, err := c.eks.DescribeInsight(ctx, &eks.DescribeInsightInput{ClusterName: aws.String("prod"), Id: aws.String("in-1")})
		return err
	})
	read("DescribeUpdate", func() error {
		_, err := c.eks.DescribeUpdate(ctx, &eks.DescribeUpdateInput{Name: aws.String("prod"), UpdateId: aws.String(updateB)})
		return err
	})
	read("DescribeInstances", func() error {
		_, err := c.ec2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{})
		return err
	})
	read("DescribeAutoScalingGroups", func() error {
		_, err := c.asg.DescribeAutoScalingGroups(ctx, &autoscaling.DescribeAutoScalingGroupsInput{})
		return err
	})
	wg.Wait()
}

// TestOnAnswer checks what the answer hook reports for each way a request
// can end.
func TestOnAnswer(t *testing.T) {
	f := newFixture(t)
	ctx := testContext(t)
	type seen struct {
		ID        int
		Operation fleettest.Operation
		Answer    string
	}
	var mu sync.Mutex
	var got []seen
	f.server.OnAnswer = func(r awsxtest.Request, a awsxtest.Answer) {
		mu.Lock()
		defer mu.Unlock()
		answer := fmt.Sprintf("code=%q dropped=%t committed=%t json=%t", a.Code, a.Dropped, a.Committed, a.JSON != nil)
		if r.Operation == fleettest.EKSDescribeCluster {
			var body struct {
				Cluster struct{ Status, Version string }
			}
			if err := json.Unmarshal(a.JSON, &body); a.JSON != nil && err != nil {
				t.Errorf("the JSON of DescribeCluster: %v", err)
			}
			answer += " " + body.Cluster.Status + " " + body.Cluster.Version
		}
		got = append(got, seen{r.ID, r.Operation, answer})
	}
	f.world.SetFault(fleettest.EKSUpdateAddon, fleettest.Fault{LoseResponse: 1})
	f.world.SetFault(fleettest.EKSListAddons, fleettest.Fault{Deny: true})

	c := f.once
	_, _ = c.eks.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String("prod")})
	_, _ = c.eks.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String("nope")})
	_, _ = c.eks.ListAddons(ctx, &eks.ListAddonsInput{ClusterName: aws.String("prod")})
	_, _ = c.eks.UpdateAddon(ctx, &eks.UpdateAddonInput{ClusterName: aws.String("prod"), AddonName: aws.String("vpc-cni")})
	_, _ = c.ec2.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{})
	_, _ = c.ec2.DescribeRegions(ctx, &ec2.DescribeRegionsInput{})

	want := []seen{
		{1, fleettest.EKSDescribeCluster, `code="" dropped=false committed=true json=true ACTIVE 1.32`},
		{2, fleettest.EKSDescribeCluster, `code="ResourceNotFoundException" dropped=false committed=false json=false  `},
		{3, fleettest.EKSListAddons, `code="AccessDeniedException" dropped=false committed=false json=false`},
		{4, fleettest.EKSUpdateAddon, `code="" dropped=true committed=true json=false`},
		{5, fleettest.EC2DescribeVpcs, `code="" dropped=false committed=true json=false`},
		{6, "ec2:DescribeRegions", `code="UnsupportedOperation" dropped=false committed=false json=false`},
	}
	mu.Lock()
	defer mu.Unlock()
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("answers (-want +got):\n%s", diff)
	}
}

// TestHandlerHooks checks the two hooks a serving program sets.
func TestHandlerHooks(t *testing.T) {
	f := newFixture(t)
	ctx := testContext(t)
	var mu sync.Mutex
	var delayed []fleettest.Operation
	f.server.Latency = func(r awsxtest.Request) time.Duration {
		mu.Lock()
		defer mu.Unlock()
		delayed = append(delayed, r.Operation)
		r.Input["written by"] = "the latency hook"
		return time.Nanosecond
	}
	f.server.Throttle = func(r awsxtest.Request) bool {
		r.Input["written by"] = "the throttle hook"
		return r.Service == "ec2"
	}

	if _, err := f.once.eks.ListClusters(ctx, &eks.ListClustersInput{}); err != nil {
		t.Errorf("ListClusters: %v", err)
	}
	_, err := f.once.ec2.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{})
	checkError(t, err, wireErrors["ec2"].throttled)

	mu.Lock()
	defer mu.Unlock()
	if diff := cmp.Diff([]fleettest.Operation{fleettest.EKSListClusters, fleettest.EC2DescribeVpcs}, delayed); diff != "" {
		t.Errorf("requests the latency hook saw (-want +got):\n%s", diff)
	}
	for _, r := range f.server.Requests() {
		if len(r.Input) != 0 {
			t.Errorf("a hook changed the logged input of %s: %v", r.Operation, r.Input)
		}
	}
}

// TestHandlerOnAnotherServer serves the Handler without Server, as a tool
// with its own listener does.
func TestHandlerOnAnotherServer(t *testing.T) {
	h := awsxtest.NewHandler(newWorld())
	defer h.Close()
	ts := httptest.NewServer(h)
	defer ts.Close()
	cfg := aws.Config{
		Region:       region,
		Credentials:  credentials.NewStaticCredentialsProvider("AKIDFAKEFAKEFAKE", "fake-secret", ""),
		BaseEndpoint: aws.String(ts.URL),
		HTTPClient:   ts.Client(),
	}

	out, err := eks.NewFromConfig(cfg).ListClusters(testContext(t), &eks.ListClustersInput{})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"prod", "staging"}, out.Clusters); diff != "" {
		t.Errorf("clusters (-want +got):\n%s", diff)
	}
	if n := len(h.Requests()); n != 1 {
		t.Errorf("the handler logged %d requests, want 1", n)
	}
}

// TestCloseReleasesHeldRequests checks that Close does not wait for a
// request that a fault still holds.
func TestCloseReleasesHeldRequests(t *testing.T) {
	f := newFixture(t)
	ctx := testContext(t)
	f.world.SetFault(fleettest.EKSListClusters, fleettest.Fault{Hang: true})

	done := make(chan error, 1)
	go func() {
		_, err := f.once.eks.ListClusters(ctx, &eks.ListClustersInput{})
		done <- err
	}()
	if err := f.world.AwaitBlocked(ctx, 1); err != nil {
		t.Fatal(err)
	}
	f.server.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Error("the held request succeeded")
		}
	case <-ctx.Done():
		t.Fatal("the held request did not return after Close")
	}
}
