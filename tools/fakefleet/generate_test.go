package main

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// fleet is a generated fleet served to real clients: the AWS SDK and
// client-go.
type fleet struct {
	world      *fleettest.World
	placements []placement
	aws        *awsxtest.Server
	kube       *kubeServers
}

func newFleet(t *testing.T) *fleet {
	t.Helper()
	o := testOptions(len(situations)+2, 2)
	w, regions, placements, err := newWorld(o)
	if err != nil {
		t.Fatal(err)
	}
	f := &fleet{world: w, placements: placements, aws: awsxtest.NewServer(w)}
	t.Cleanup(f.aws.Close)
	f.kube = serveKube(w, regions[0])
	t.Cleanup(f.kube.Close)
	return f
}

// describeNodegroup reads ng-a of the cluster of p.
func (f *fleet) describeNodegroup(t *testing.T, p placement) *ekstypes.Nodegroup {
	t.Helper()
	ng, err := f.eks(p.region).DescribeNodegroup(context.Background(), &eks.DescribeNodegroupInput{ClusterName: aws.String(p.cluster), NodegroupName: aws.String("ng-a")})
	if err != nil {
		t.Fatal(err)
	}
	return ng.Nodegroup
}

// nodesOf lists the nodes of ng-a of the cluster of p.
func (f *fleet) nodesOf(t *testing.T, p placement) *corev1.NodeList {
	t.Helper()
	nodes, err := f.clientset(t, p).CoreV1().Nodes().List(context.Background(), metav1.ListOptions{LabelSelector: "eks.amazonaws.com/nodegroup=ng-a"})
	if err != nil {
		t.Fatal(err)
	}
	return nodes
}

// place returns where a situation is.
func (f *fleet) place(t *testing.T, name string) placement {
	t.Helper()
	i := slices.IndexFunc(f.placements, func(p placement) bool { return p.situation.name == name })
	if i < 0 {
		t.Fatalf("the situation %s is not placed", name)
	}
	return f.placements[i]
}

func (f *fleet) eks(region string) *eks.Client {
	return eks.NewFromConfig(f.aws.Config(region))
}

// clientset returns a client-go clientset for the cluster of p, from the
// server that the cluster's EKS endpoint names.
func (f *fleet) clientset(t *testing.T, p placement) *kubernetes.Clientset {
	t.Helper()
	i := slices.Index(f.kube.names, p.cluster)
	cs, err := kubernetes.NewForConfig(&rest.Config{Host: f.kube.servers[i].URL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cs.CoreV1().RESTClient().(*rest.RESTClient).Client.CloseIdleConnections)
	return cs
}

// Every situation is where the placements say, as real clients see it.
func TestSituationsAreReachable(t *testing.T) {
	f := newFleet(t)
	for name, check := range map[string]func(t *testing.T, f *fleet){
		"pdb-blocker": testPDBBlocker, "critical-failing": testCriticalFailing, "node-notready": testNodeNotReady,
		"nodegroup-degraded": testNodegroupDegraded, "roll-in-flight": testRollInFlight, "addon-degraded": testAddonDegraded,
		"insight-error": testInsightError, "rollback-window": testRollbackWindow,
	} {
		t.Run(name, func(t *testing.T) { check(t, f) })
	}
	// The situations about the next roll: each runs its script when the
	// roll starts.
	for _, tc := range []nextRoll{
		{"stuck-drain", ekstypes.NodegroupStatusUpdating, func(t *testing.T, nodes *corev1.NodeList, _ *corev1.PodList) {
			t.Helper()
			if !slices.ContainsFunc(nodes.Items, func(n corev1.Node) bool { return n.Spec.Unschedulable }) {
				t.Error("no node is cordoned")
			}
		}},
		{"nodes-never-ready", ekstypes.NodegroupStatusActive, func(t *testing.T, nodes *corev1.NodeList, _ *corev1.PodList) {
			t.Helper()
			if !slices.ContainsFunc(nodes.Items, func(n corev1.Node) bool { return n.Status.Conditions[0].Status == corev1.ConditionFalse }) {
				t.Error("every new node is Ready")
			}
		}},
		{"pending-pods", ekstypes.NodegroupStatusActive, func(t *testing.T, _ *corev1.NodeList, pods *corev1.PodList) {
			t.Helper()
			if !slices.ContainsFunc(pods.Items, func(p corev1.Pod) bool { return p.Status.Phase == corev1.PodPending }) {
				t.Error("no pod is Pending")
			}
		}},
		{"update-fails", ekstypes.NodegroupStatusDegraded, nil},
	} {
		t.Run(tc.name, func(t *testing.T) { testNextRoll(t, f, tc) })
	}
}

func testPDBBlocker(t *testing.T, f *fleet) {
	t.Helper()
	p := f.place(t, "pdb-blocker")
	pdbs, err := f.clientset(t, p).PolicyV1().PodDisruptionBudgets("app").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pdbs.Items) != 1 || pdbs.Items[0].Status.DisruptionsAllowed != 0 {
		t.Fatalf("PDBs = %+v, want one that allows no disruption", pdbs.Items)
	}
}

func testCriticalFailing(t *testing.T, f *fleet) {
	t.Helper()
	p := f.place(t, "critical-failing")
	pods, err := f.clientset(t, p).CoreV1().Pods("kube-system").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	notReady := 0
	for _, pod := range pods.Items {
		if !pod.Status.ContainerStatuses[0].Ready {
			notReady++
		}
	}
	if notReady == 0 || notReady*100 < len(pods.Items)*10 {
		t.Fatalf("%d of %d system pods are not ready; want more than a tenth, so the check fails", notReady, len(pods.Items))
	}
}

func testNodeNotReady(t *testing.T, f *fleet) {
	t.Helper()
	p := f.place(t, "node-notready")
	notReady := 0
	for _, n := range f.nodesOf(t, p).Items {
		if n.Status.Conditions[0].Status == corev1.ConditionFalse {
			notReady++
		}
	}
	if notReady != 1 {
		t.Fatalf("%d NotReady nodes in ng-a, want 1", notReady)
	}
}

func testNodegroupDegraded(t *testing.T, f *fleet) {
	t.Helper()
	ng := f.describeNodegroup(t, f.place(t, "nodegroup-degraded"))
	if ng.Status != ekstypes.NodegroupStatusDegraded || len(ng.Health.Issues) != 1 {
		t.Fatalf("ng-a is %s with %d issues, want DEGRADED with 1", ng.Status, len(ng.Health.Issues))
	}
}

// A roll in flight is half done: the new nodes are there and Ready, the
// old ones are cordoned, and the roll ends on the clock.
func testRollInFlight(t *testing.T, f *fleet) {
	t.Helper()
	p := f.place(t, "roll-in-flight")
	if ng := f.describeNodegroup(t, p); ng.Status != ekstypes.NodegroupStatusUpdating {
		t.Fatalf("ng-a is %s, want UPDATING", ng.Status)
	}
	updates, err := f.eks(p.region).ListUpdates(context.Background(), &eks.ListUpdatesInput{Name: aws.String(p.cluster), NodegroupName: aws.String("ng-a")})
	if err != nil {
		t.Fatal(err)
	}
	if len(updates.UpdateIds) != 1 {
		t.Fatalf("%d updates of ng-a, want 1 in progress", len(updates.UpdateIds))
	}
	cordoned, fresh := 0, 0
	for _, n := range f.nodesOf(t, p).Items {
		if n.Spec.Unschedulable {
			cordoned++
		} else {
			fresh++
		}
	}
	if cordoned == 0 || fresh == 0 || cordoned != fresh {
		t.Fatalf("%d cordoned and %d new nodes, want the same number of each", cordoned, fresh)
	}
	f.world.Advance(2 * time.Minute)
	if ng := f.describeNodegroup(t, p); ng.Status != ekstypes.NodegroupStatusActive {
		t.Fatalf("ng-a is %s after the roll's time, want ACTIVE", ng.Status)
	}
}

func testAddonDegraded(t *testing.T, f *fleet) {
	t.Helper()
	p := f.place(t, "addon-degraded")
	a, err := f.eks(p.region).DescribeAddon(context.Background(), &eks.DescribeAddonInput{ClusterName: aws.String(p.cluster), AddonName: aws.String("coredns")})
	if err != nil {
		t.Fatal(err)
	}
	if a.Addon.Status != ekstypes.AddonStatusDegraded {
		t.Fatalf("coredns is %s, want DEGRADED", a.Addon.Status)
	}
}

func testInsightError(t *testing.T, f *fleet) {
	t.Helper()
	p := f.place(t, "insight-error")
	list, err := f.eks(p.region).ListInsights(context.Background(), &eks.ListInsightsInput{ClusterName: aws.String(p.cluster)})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Insights) != 1 || list.Insights[0].InsightStatus.Status != ekstypes.InsightStatusValueError {
		t.Fatalf("insights = %+v, want one ERROR", list.Insights)
	}
}

func testRollbackWindow(t *testing.T, f *fleet) {
	t.Helper()
	p := f.place(t, "rollback-window")
	ctx := context.Background()
	updates, err := f.eks(p.region).ListUpdates(ctx, &eks.ListUpdatesInput{Name: aws.String(p.cluster)})
	if err != nil {
		t.Fatal(err)
	}
	if len(updates.UpdateIds) != 1 {
		t.Fatalf("%d updates, want the one upgrade", len(updates.UpdateIds))
	}
	u, err := f.eks(p.region).DescribeUpdate(ctx, &eks.DescribeUpdateInput{Name: aws.String(p.cluster), UpdateId: aws.String(updates.UpdateIds[0])})
	if err != nil {
		t.Fatal(err)
	}
	if u.Update.Type != ekstypes.UpdateTypeVersionUpdate || u.Update.Status != ekstypes.UpdateStatusSuccessful || !u.Update.CreatedAt.After(fixedNow.Add(-7*24*time.Hour)) {
		t.Fatalf("update = %+v, want a successful version update inside the window", u.Update)
	}
}

// nextRoll is a situation about the next roll of ng-a: the status the
// nodegroup ends in, and what the nodes and the pods show.
type nextRoll struct {
	name   string
	status ekstypes.NodegroupStatus
	check  func(t *testing.T, nodes *corev1.NodeList, pods *corev1.PodList)
}

func testNextRoll(t *testing.T, f *fleet, tc nextRoll) {
	t.Helper()
	p := f.place(t, tc.name)
	ctx := context.Background()
	client := f.eks(p.region)
	ng := f.describeNodegroup(t, p)
	started, err := client.UpdateNodegroupVersion(ctx, &eks.UpdateNodegroupVersionInput{
		ClusterName: aws.String(p.cluster), NodegroupName: aws.String("ng-a"), Version: ng.Version,
		ClientRequestToken: aws.String("fakefleet-test-" + tc.name + "-0000000000000000000"),
	})
	if err != nil {
		t.Fatal(err)
	}
	f.world.Advance(10 * time.Minute)
	if ng := f.describeNodegroup(t, p); ng.Status != tc.status {
		t.Fatalf("ng-a is %s after the roll's time, want %s", ng.Status, tc.status)
	}
	if tc.check == nil {
		u, err := client.DescribeUpdate(ctx, &eks.DescribeUpdateInput{Name: aws.String(p.cluster), NodegroupName: aws.String("ng-a"), UpdateId: started.Update.Id})
		if err != nil {
			t.Fatal(err)
		}
		if u.Update.Status != ekstypes.UpdateStatusFailed || len(u.Update.Errors) != 1 {
			t.Fatalf("update = %s with %d errors, want Failed with 1", u.Update.Status, len(u.Update.Errors))
		}
		return
	}
	pods, err := f.clientset(t, p).CoreV1().Pods("app").List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tc.check(t, f.nodesOf(t, p), pods)
}

// The default durations give each kind of update its own time on the
// clock.
func TestUpdateDurations(t *testing.T) {
	f := newFleet(t)
	ctx := context.Background()
	// A cluster with no situation: the last one.
	var name, region, version, addonVersion string
	f.world.View(func(s *fleettest.State) {
		c := s.Clusters[len(s.Clusters)-1]
		name, region, version = c.Name, c.Region, c.Version
		addonVersion = c.Addons[0].Version
	})
	client := f.eks(region)
	roll, err := client.UpdateNodegroupVersion(ctx, &eks.UpdateNodegroupVersionInput{ClusterName: aws.String(name), NodegroupName: aws.String("ng-a"), Version: aws.String(version), ClientRequestToken: aws.String("fakefleet-test-durations-roll-00000000000")})
	if err != nil {
		t.Fatal(err)
	}
	addon, err := client.UpdateAddon(ctx, &eks.UpdateAddonInput{ClusterName: aws.String(name), AddonName: aws.String("vpc-cni"), AddonVersion: aws.String(addonVersion), ClientRequestToken: aws.String("fakefleet-test-durations-addon-0000000000")})
	if err != nil {
		t.Fatal(err)
	}
	status := func(id *string, in *eks.DescribeUpdateInput) ekstypes.UpdateStatus {
		in.UpdateId = id
		u, err := client.DescribeUpdate(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		return u.Update.Status
	}
	rollIn := &eks.DescribeUpdateInput{Name: aws.String(name), NodegroupName: aws.String("ng-a")}
	addonIn := &eks.DescribeUpdateInput{Name: aws.String(name), AddonName: aws.String("vpc-cni")}
	f.world.Advance(45 * time.Second)
	if status(roll.Update.Id, rollIn) != ekstypes.UpdateStatusInProgress || status(addon.Update.Id, addonIn) != ekstypes.UpdateStatusSuccessful {
		t.Fatal("after 45s the add-on update (30s) should be done and the roll (2m) in progress")
	}
	f.world.Advance(90 * time.Second)
	if status(roll.Update.Id, rollIn) != ekstypes.UpdateStatusSuccessful {
		t.Fatal("after 2m15s the roll should be done")
	}
}
