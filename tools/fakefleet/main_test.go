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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

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
		{[]string{"-situations", "pdb-blocker,stuck-drain"}, false},
		{[]string{"-situations", "none"}, false},
		{[]string{"-situations", "nope"}, true},
		{[]string{"-roll", "0"}, true},
		{[]string{"-addon", "-1s"}, true},
		{[]string{"-kube=false"}, false},
	}
	for _, tt := range tests {
		if _, err := parseFlags(tt.args, io.Discard); (err != nil) != tt.wantErr {
			t.Errorf("parseFlags(%q) error = %v, want an error = %t", tt.args, err, tt.wantErr)
		}
	}
}

// fixedNow is the start of the clock of every world of these tests.
var fixedNow = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

// testOptions returns the options of a generated fleet with every
// situation, no latency, no throttling, and a fixed clock.
func testOptions(clusters, regions int) options {
	o, err := parseFlags([]string{"-latency", "0", "-throttle", "0", "-closed-region", ""}, io.Discard)
	if err != nil {
		panic(err)
	}
	o.clusters, o.regions, o.now = clusters, regions, fixedNow
	return o
}

func TestGenerateIsRepeatable(t *testing.T) {
	build := func(seed uint64) (fleettest.State, []placement) {
		o := testOptions(12, 3)
		o.seed = seed
		w, _, placements, err := newWorld(o)
		if err != nil {
			t.Fatal(err)
		}
		var out fleettest.State
		w.View(func(s *fleettest.State) { out = *s })
		return out, placements
	}
	ignore := cmpopts.IgnoreUnexported(fleettest.Update{})
	a, placements := build(7)
	b, _ := build(7)
	if diff := cmp.Diff(a, b, ignore); diff != "" {
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
	if other, _ := build(8); cmp.Equal(a, other, ignore) {
		t.Error("another seed generated the same fleet")
	}
	// Every situation is placed, one per cluster, in order.
	if len(placements) != len(situations) {
		t.Fatalf("%d placements, want one per situation (%d)", len(placements), len(situations))
	}
	for i, p := range placements {
		if p.situation.name != situations[i].name || p.cluster != a.Clusters[i].Name || p.region != a.Clusters[i].Region {
			t.Errorf("placement %d = %s on %s/%s, want %s on cluster %d", i, p.situation.name, p.region, p.cluster, situations[i].name, i)
		}
	}
	// A fleet smaller than the mix places what it can, and the rest is
	// said.
	o := testOptions(3, 1)
	_, _, few, err := newWorld(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(few) != 3 {
		t.Errorf("a fleet of 3 placed %d situations, want 3", len(few))
	}
	var out strings.Builder
	describe(&out, few, o)
	if !strings.Contains(out.String(), "situations not placed: the fleet has only 3 clusters") {
		t.Errorf("describe does not say what was not placed:\n%s", out.String())
	}
	// Every situation's cluster has an outdated AMI on ng-a, so a roll is
	// offered there.
	for _, p := range placements {
		c := a.Cluster(p.region, p.cluster)
		if c.Nodegroups[0].Name != "ng-a" {
			t.Errorf("%s: the first nodegroup is %s, want ng-a", c.Name, c.Nodegroups[0].Name)
		}
		for _, in := range a.Instances {
			if in.Tags["eks:cluster-name"] == c.Name && in.Tags["eks:nodegroup-name"] == "ng-a" && !strings.HasPrefix(in.ImageID, "ami-old") && p.situation.name != "roll-in-flight" {
				t.Errorf("%s (%s): ng-a runs %s, want an outdated AMI", c.Name, p.situation.name, in.ImageID)
			}
		}
	}
}

// Without Kubernetes the fleet has no nodes, and the environment points at
// a kubeconfig that does not exist.
func TestNoKube(t *testing.T) {
	o := testOptions(4, 1)
	o.kube = false
	w, _, _, err := newWorld(o)
	if err != nil {
		t.Fatal(err)
	}
	w.View(func(s *fleettest.State) {
		for _, c := range s.Clusters {
			if len(c.Nodes) > 0 || len(c.Pods) > 0 || len(c.PDBs) > 0 {
				t.Errorf("%s has Kubernetes objects with -kube=false", c.Name)
			}
		}
	})
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
	w, regions, placements, err := newWorld(options{worldFile: path, regions: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(placements) != 0 {
		t.Errorf("a world file placed %d situations, want none", len(placements))
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
	if _, _, _, err := newWorld(options{worldFile: path, regions: 1}); err == nil {
		t.Error("a file with an unknown field was accepted")
	}
}

// TestFollowClock feeds wall-clock ticks to the clock follower and checks
// the world's time after each. A send on the unbuffered channel returns
// only when the follower is back at its receive, so the earlier tick has
// been applied by then.
func TestFollowClock(t *testing.T) {
	o := testOptions(1, 1)
	o.mix = nil
	w, _, _, err := newWorld(o)
	if err != nil {
		t.Fatal(err)
	}
	w.Edit(func(s *fleettest.State) { s.DefaultUpdate = fleettest.UpdateScript{After: time.Minute} })
	update, err := w.UpdateClusterVersion("us-east-1", clusterName(w), fleettest.ClusterVersionRequest{Version: new("1.99")})
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

// TestAdvanceDoesNotOverflow moves the clock by more than a time.Duration
// holds, as a long gap between ticks at a high speed asks for.
func TestAdvanceDoesNotOverflow(t *testing.T) {
	w := fleettest.New()
	start := w.Now()
	// Twelve hours of wall time at the highest speed: 4.3e19 ns.
	advance(w, float64(12*time.Hour)*maxSpeed)
	got := w.Now().Sub(start) // saturates at the largest Duration
	if !w.Now().After(start) || got != time.Duration(1<<63-1) {
		t.Errorf("the clock moved from %s to %s, want forward by more than a Duration holds", start, w.Now())
	}
	years := w.Now().Year() - start.Year()
	if years < 1360 || years > 1380 { // 4.32e19 ns is about 1369 years
		t.Errorf("the clock moved %d years, want about 1369", years)
	}
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
	// The closed region is swept too, so a sweep meets it.
	if env["REFRESH_EKS_REGIONS"] != "us-east-1,us-west-2,eu-west-1" || env["AWS_REGION"] != "us-east-1" {
		t.Fatalf("environment = %v", env)
	}
	// The kubeconfig has a context per cluster, whose server is the
	// cluster's endpoint as EKS reports it, and a real clientset reaches
	// it.
	kubeconfig, err := clientcmd.LoadFromFile(env["KUBECONFIG"])
	if err != nil {
		t.Fatal(err)
	}
	if len(kubeconfig.Contexts) != 6 {
		t.Fatalf("the kubeconfig has %d contexts, want 6", len(kubeconfig.Contexts))
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
	described, err := client("us-east-1").DescribeCluster(ctx, &eks.DescribeClusterInput{Name: aws.String(listed.Clusters[0])})
	if err != nil {
		t.Fatal(err)
	}
	kc := kubeconfig.Clusters[listed.Clusters[0]]
	if kc == nil || kc.Server != aws.ToString(described.Cluster.Endpoint) {
		t.Fatalf("the kubeconfig server of %s is %v, want the EKS endpoint %s", listed.Clusters[0], kc, aws.ToString(described.Cluster.Endpoint))
	}
	restConfig, err := clientcmd.NewNonInteractiveClientConfig(*kubeconfig, listed.Clusters[0], nil, nil).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes.Items) == 0 {
		t.Error("the cluster has no nodes")
	}
	clientset.CoreV1().RESTClient().(*rest.RESTClient).Client.CloseIdleConnections()
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
