package aws

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// recordingSSMDoer answers SSM GetParameter from a map of parameter name to
// value (a missing name is ParameterNotFound) and records every name asked
// for, so tests can check which SSM path the code reads.
type recordingSSMDoer struct {
	values map[string]string
	mu     sync.Mutex
	names  []string
}

func (d *recordingSSMDoer) Do(req *http.Request) (*http.Response, error) {
	var in struct{ Name string }
	body, _ := io.ReadAll(req.Body)
	_ = json.Unmarshal(body, &in)
	d.mu.Lock()
	d.names = append(d.names, in.Name)
	d.mu.Unlock()

	status, out := 200, ""
	if v, ok := d.values[in.Name]; ok {
		b, _ := json.Marshal(map[string]any{"Parameter": map[string]string{"Name": in.Name, "Value": v}})
		out = string(b)
	} else {
		status, out = 400, `{"__type":"ParameterNotFound","message":"Parameter not found."}`
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/x-amz-json-1.1"}},
		Body:       io.NopCloser(strings.NewReader(out)),
	}, nil
}

func (d *recordingSSMDoer) asked() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.names...)
}

func recordingSSMClient(d *recordingSSMDoer) *ssm.Client {
	return ssm.New(ssm.Options{
		Region:           "us-east-1",
		Credentials:      aws.AnonymousCredentials{},
		HTTPClient:       d,
		RetryMaxAttempts: 1,
	})
}

// LatestReleaseVersionForType reads the release_version sibling of the
// image_id parameter, and degrades to "" when it can't.
func TestLatestReleaseVersionForType(t *testing.T) {
	imagePath := buildSSMParameterPath("1.31", types.AMITypesAl2023X8664Standard)
	relPath := strings.TrimSuffix(imagePath, "image_id") + "release_version"
	if !strings.HasSuffix(imagePath, "/image_id") {
		t.Fatalf("image path %q does not end in /image_id", imagePath)
	}

	d := &recordingSSMDoer{values: map[string]string{relPath: "1.31.7-20260601"}}
	c := recordingSSMClient(d)
	if got := LatestReleaseVersionForType(context.Background(), c, "1.31", types.AMITypesAl2023X8664Standard); got != "1.31.7-20260601" {
		t.Fatalf("release version = %q, want 1.31.7-20260601", got)
	}
	if asked := d.asked(); len(asked) != 1 || asked[0] != relPath {
		t.Fatalf("SSM names asked = %v, want only %s", asked, relPath)
	}

	// A missing parameter (a new minor not yet published) degrades to "".
	if got := LatestReleaseVersionForType(context.Background(), c, "1.99", types.AMITypesAl2023X8664Standard); got != "" {
		t.Fatalf("missing parameter = %q, want \"\"", got)
	}
	// A present parameter with no value degrades to "" too.
	if got := LatestReleaseVersionForType(context.Background(), stubSSMClient(200, `{"Parameter":{"Name":"p"}}`), "1.31", types.AMITypesAl2023X8664Standard); got != "" {
		t.Fatalf("empty value = %q, want \"\"", got)
	}
	// A custom AMI has no recommended release: no SSM call at all.
	before := len(d.asked())
	if got := LatestReleaseVersionForType(context.Background(), c, "1.31", types.AMITypesCustom); got != "" || len(d.asked()) != before {
		t.Fatalf("custom AMI = %q after %d SSM call(s), want \"\" and none", got, len(d.asked())-before)
	}
}

// NewLatestAMIIDCache looks the image ID up through SSM once per
// (version, AMI type) and serves repeats from the cache.
func TestNewLatestAMIIDCache_RealSSMPath(t *testing.T) {
	al2023 := buildSSMParameterPath("1.31", types.AMITypesAl2023X8664Standard)
	bottlerocket := buildSSMParameterPath("1.31", types.AMITypesBottlerocketX8664)
	d := &recordingSSMDoer{values: map[string]string{al2023: "ami-al2023", bottlerocket: "ami-br"}}
	cache := NewLatestAMIIDCache(recordingSSMClient(d))
	ctx := context.Background()

	for range 3 {
		if got, err := cache.Get(ctx, "1.31", types.AMITypesAl2023X8664Standard); err != nil || got != "ami-al2023" {
			t.Fatalf("AL2023 = %q, %v", got, err)
		}
	}
	if got, err := cache.Get(ctx, "1.31", types.AMITypesBottlerocketX8664); err != nil || got != "ami-br" {
		t.Fatalf("Bottlerocket = %q, %v", got, err)
	}
	if asked := d.asked(); len(asked) != 2 {
		t.Fatalf("SSM calls = %v, want one per distinct key", asked)
	}

	// A failed lookup is an error, never "no newer AMI", and is not cached.
	if got, err := cache.Get(ctx, "1.99", types.AMITypesAl2023X8664Standard); err == nil || got != "" {
		t.Fatalf("missing parameter = %q, %v; want an error", got, err)
	}
	if _, err := cache.Get(ctx, "1.99", types.AMITypesAl2023X8664Standard); err == nil {
		t.Fatal("second lookup of a missing parameter should fail again")
	}
	if n := len(d.asked()); n != 4 {
		t.Fatalf("SSM calls = %d, want 4 (failures are retried, not cached)", n)
	}

	// A custom AMI resolves to "" with no SSM call.
	if got, err := cache.Get(ctx, "1.31", types.AMITypesCustom); err != nil || got != "" || len(d.asked()) != 4 {
		t.Fatalf("custom = %q, %v after %d calls", got, err, len(d.asked()))
	}
}
