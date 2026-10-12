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
		{[]string{"-throttle", "-0.1"}, true},
		{[]string{"-throttle", "NaN"}, true},
		{[]string{"-nope"}, true},
		{[]string{"-clusters", "3", "extra"}, true},
		{[]string{"-clusters", "-1"}, true},
		{[]string{"-clusters", "0"}, false},
		{[]string{"-latency", "-1s"}, true},
		{[]string{"-latency", "2562047h"}, true},
		{[]string{"-latency", "1h"}, false},
		{[]string{"-speed", "-1"}, true},
		{[]string{"-speed", "NaN"}, true},
		{[]string{"-speed", "+Inf"}, true},
		{[]string{"-speed", "0"}, false},
		{[]string{"-speed", "600"}, false},
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

// TestFollowClock feeds wall-clock ticks to the clock follower and checks
// the world's time after each. A send on the unbuffered channel returns
// only when the follower is back at its receive, so the earlier tick has
// been applied by then.
func TestFollowClock(t *testing.T) {
	w, _, err := newWorld(options{clusters: 1, regions: 1})
	if err != nil {
		t.Fatal(err)
	}
	w.Edit(func(s *fleettest.State) { s.DefaultUpdate = fleettest.UpdateScript{After: time.Minute} })
	update, err := w.UpdateClusterVersion("us-east-1", clusterName(w), fleettest.ClusterVersionRequest{Version: "1.99"})
	if err != nil {
		t.Fatal(err)
	}
	status := func() fleettest.UpdateStatus {
		var out fleettest.UpdateStatus
		w.View(func(s *fleettest.State) { out = s.Clusters[0].Update(update.ID).Status })
		return out
	}

	ctx, cancel := context.WithCancel(t.Context())
	ticks := make(chan time.Time)
	done := make(chan struct{})
	wall := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	start := w.Now()
	go func() {
		defer close(done)
		followClock(ctx, w, 60, wall, ticks) // one wall second is one world minute
	}()

	ticks <- wall.Add(500 * time.Millisecond)
	ticks <- wall.Add(500 * time.Millisecond) // no time passed: applies the tick before
	if got := w.Now().Sub(start); got != 30*time.Second {
		t.Errorf("after half a wall second the world moved %s, want 30s", got)
	}
	if got := status(); got != fleettest.UpdateInProgress {
		t.Errorf("update = %s after 30s of a 1m update, want InProgress", got)
	}
	ticks <- wall.Add(400 * time.Millisecond) // a wall clock that steps back moves nothing
	ticks <- wall.Add(time.Second)
	ticks <- wall.Add(time.Second)
	if got := w.Now().Sub(start); got != 66*time.Second {
		t.Errorf("the world moved %s in all, want 1m6s", got)
	}
	if got := status(); got != fleettest.UpdateSuccessful {
		t.Errorf("update = %s after its minute passed, want Successful", got)
	}

	cancel()
	<-done
}

func clusterName(w *fleettest.World) string {
	var name string
	w.View(func(s *fleettest.State) { name = s.Clusters[0].Name })
	return name
}

// failAfter is a writer that fails once it has taken n writes.
type failAfter struct{ n int }

func (f *failAfter) Write(p []byte) (int, error) {
	if f.n == 0 {
		return 0, errors.New("the pipe closed")
	}
	f.n--
	return len(p), nil
}

// TestRunStopsWhenItCannotPrint checks that run gives up, and leaves no
// listener or goroutine behind, when its output goes away.
func TestRunStopsWhenItCannotPrint(t *testing.T) {
	err := run(t.Context(), []string{"-clusters", "1", "-regions", "1", "-dir", t.TempDir()}, &failAfter{n: 2}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "the pipe closed") {
		t.Errorf("run returned %v, want the write error", err)
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
