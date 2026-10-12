package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/smithy-go"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"go.uber.org/goleak"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestParseFlags(t *testing.T) {
	tests := []struct {
		args    []string
		wantErr bool
	}{
		{nil, false},
		{[]string{"-regions", "0"}, true},
		{[]string{"-regions", "11"}, true},
		{[]string{"-throttle", "1.5"}, true},
		{[]string{"-nope"}, true},
	}
	for _, tt := range tests {
		if _, err := parseFlags(tt.args, io.Discard); (err != nil) != tt.wantErr {
			t.Errorf("parseFlags(%q) error = %v, want an error = %t", tt.args, err, tt.wantErr)
		}
	}
}

func TestGenerateIsRepeatable(t *testing.T) {
	build := func(seed uint64) fleettest.State {
		w, _, err := newWorld(options{clusters: 12, regions: 3, seed: seed})
		if err != nil {
			t.Fatal(err)
		}
		var out fleettest.State
		w.View(func(s *fleettest.State) { out = *s })
		return out
	}
	a, b := build(7), build(7)
	if diff := cmp.Diff(a, b, cmpopts.IgnoreUnexported(fleettest.Update{})); diff != "" {
		t.Errorf("two fleets from one seed differ (-first +second):\n%s", diff)
	}
	if len(a.Clusters) != 12 {
		t.Fatalf("generated %d clusters, want 12", len(a.Clusters))
	}
	regions := map[string]int{}
	for _, c := range a.Clusters {
		regions[c.Region]++
	}
	if diff := cmp.Diff(map[string]int{"us-east-1": 4, "us-west-2": 4, "eu-west-1": 4}, regions); diff != "" {
		t.Errorf("clusters per region (-want +got):\n%s", diff)
	}
	if other := build(8); cmp.Equal(a, other, cmpopts.IgnoreUnexported(fleettest.Update{})) {
		t.Error("another seed generated the same fleet")
	}
}

func TestWorldFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "world.json")
	const doc = `{
		"Clusters": [
			{"Name": "a", "Version": "1.33", "Region": "eu-north-1", "Nodegroups": [{"Name": "ng", "Version": "1.32", "Status": "DEGRADED"}]},
			{"Name": "b", "Version": "1.32", "Region": "us-west-2"}
		],
		"DefaultUpdate": {"After": 60000000000}
	}`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	w, regions, err := newWorld(options{worldFile: path, regions: 1})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"eu-north-1", "us-west-2"}, regions); diff != "" {
		t.Errorf("regions (-want +got):\n%s", diff)
	}
	w.View(func(s *fleettest.State) {
		ng := s.Cluster("eu-north-1", "a").Nodegroup("ng")
		if ng.Status != fleettest.StatusDegraded || s.DefaultUpdate.After != time.Minute {
			t.Errorf("nodegroup status %s, default update %s; want DEGRADED, 1m0s", ng.Status, s.DefaultUpdate.After)
		}
	})

	if err := os.WriteFile(path, []byte(`{"Clutsers": []}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := newWorld(options{worldFile: path, regions: 1}); err == nil {
		t.Error("a file with an unknown field was accepted")
	}
}

// TestRun starts the command, reads the environment it prints, and calls
// the fleet with a real SDK client built from that environment.
func TestRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	out, in := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := run(ctx, []string{
			"-clusters", "6", "-regions", "2", "-latency", "0", "-throttle", "0",
			"-closed-region", "eu-west-1", "-dir", t.TempDir(),
		}, in, io.Discard)
		_ = in.CloseWithError(err)
		done <- err
	}()

	env := map[string]string{}
	lines := bufio.NewScanner(out)
	for lines.Scan() {
		name, value, ok := strings.Cut(strings.TrimPrefix(lines.Text(), "export "), "=")
		if !ok {
			t.Fatalf("unexpected line %q", lines.Text())
		}
		env[name] = strings.Trim(value, `"`)
		if name == "REFRESH_EKS_REGIONS" { // the last line
			break
		}
	}
	if env["REFRESH_EKS_REGIONS"] != "us-east-1,us-west-2" || env["AWS_REGION"] != "us-east-1" {
		t.Fatalf("environment = %v", env)
	}

	httpClient := &http.Client{Transport: &http.Transport{}}
	defer httpClient.CloseIdleConnections()
	client := func(region string) *eks.Client {
		return eks.NewFromConfig(aws.Config{
			Region:       region,
			Credentials:  credentials.NewStaticCredentialsProvider(env["AWS_ACCESS_KEY_ID"], env["AWS_SECRET_ACCESS_KEY"], ""),
			BaseEndpoint: aws.String(env["AWS_ENDPOINT_URL"]),
			HTTPClient:   httpClient,
			Retryer:      func() aws.Retryer { return aws.NopRetryer{} },
		})
	}
	listed, err := client("us-east-1").ListClusters(ctx, &eks.ListClustersInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Clusters) != 3 {
		t.Errorf("us-east-1 lists %d clusters, want 3 of the 6", len(listed.Clusters))
	}
	_, err = client("eu-west-1").ListClusters(ctx, &eks.ListClustersInput{})
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "UnrecognizedClientException" {
		t.Errorf("the closed region answered %v, want UnrecognizedClientException", err)
	}

	httpClient.CloseIdleConnections()
	cancel()
	if err := <-done; err != nil {
		t.Errorf("run returned %v after its context ended", err)
	}
}
