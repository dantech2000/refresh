package sim

import (
	"strconv"
	"time"

	"github.com/dantech2000/refresh/internal/tui/state"
)

// latestVersion is the newest Kubernetes version the simulated EKS offers.
const latestVersion = "1.33"

// cluster is a simulated cluster: the state the TUI sees plus the scenario
// knobs that decide how its checks and rolls behave.
type cluster struct {
	state.Cluster
	scenario scenario
}

// scenario holds the per-cluster traits that make checks and rolls go wrong
// in interesting ways.
type scenario struct {
	// deprecatedAPI makes the upgrade insights report a removed API in use,
	// which blocks the upgrade.
	deprecatedAPI bool
	// tightPDB puts a PodDisruptionBudget at its limit, so one pod refuses
	// eviction for a while during every drain.
	tightPDB bool
	// orphanPods adds pods with no controller to the roll pre-flight.
	orphanPods int
	// quotaVCPU is the free EC2 on-demand vCPU quota.
	quotaVCPU int
	// budgets is the number of PodDisruptionBudgets.
	budgets int
	// workloads names the Deployments whose pods run on the nodes.
	workloads []string
}

// amiRelease builds an EKS AMI release version for a Kubernetes minor. A
// release older than the current one carries an older patch version.
func amiRelease(version, date string) string {
	patch := map[string]int{"1.30": 14, "1.31": 12, "1.32": 9, "1.33": 4}[version]
	if date < "20260918" {
		patch = max(1, patch-3)
	}
	return version + "." + strconv.Itoa(patch) + "-" + date
}

// latestAMI is the newest AMI release for a Kubernetes minor.
func latestAMI(version string) string { return amiRelease(version, "20260918") }

// addonCatalog is the newest version of each add-on per Kubernetes minor.
var addonCatalog = map[string]map[string]string{
	"vpc-cni":    {"1.30": "v1.19.2", "1.31": "v1.19.2", "1.32": "v1.19.2", "1.33": "v1.19.5"},
	"coredns":    {"1.30": "v1.11.3", "1.31": "v1.11.4", "1.32": "v1.11.4", "1.33": "v1.12.1"},
	"kube-proxy": {"1.30": "v1.30.9", "1.31": "v1.31.7", "1.32": "v1.32.3", "1.33": "v1.33.3"},
}

// addonLatest is the newest version of an add-on for a Kubernetes minor.
func addonLatest(name, version string) string { return addonCatalog[name][version] }

// supportEnds is when standard support ends for a Kubernetes minor. The
// dates are relative to the simulation's epoch, so the scenario reads the
// same whatever the date: the newest minor has most of a year left, two
// behind ends soon, and three behind is already in extended support.
func supportEnds(epoch time.Time, version string) time.Time {
	days := map[int]int{0: 300, 1: 150, 2: 45}
	d, ok := days[state.Minor(latestVersion)-state.Minor(version)]
	if !ok {
		d = -30
	}
	return epoch.AddDate(0, 0, d)
}

func addons(version string, stale map[string]string) []state.Addon {
	var out []state.Addon
	for _, name := range []string{"vpc-cni", "coredns", "kube-proxy"} {
		v := addonLatest(name, version)
		if s, ok := stale[name]; ok {
			v = s
		}
		out = append(out, state.Addon{Name: name, Version: v, Latest: addonLatest(name, version), Status: "ACTIVE"})
	}
	return out
}

func nodegroup(name, version, ami string, nodes int) state.Nodegroup {
	return state.Nodegroup{Name: name, Version: version, AMI: ami, LatestAMI: latestAMI(version), Nodes: nodes, Status: "ACTIVE"}
}

func newCluster(name, region, version string, sc scenario, ngs []state.Nodegroup, ads []state.Addon) *cluster {
	c := &cluster{scenario: sc}
	c.Name = name
	c.Region = region
	c.ARN = "arn:aws:eks:" + region + ":123456789012:cluster/" + name
	c.Version = version
	c.Latest = latestVersion
	c.Nodegroups = ngs
	c.Addons = ads
	return c
}

// refreshSupport recomputes the support fields from the version and now.
func (c *cluster) refreshSupport(epoch, now time.Time) {
	c.SupportEnds = supportEnds(epoch, c.Version)
	c.ExtendedSupport = now.After(c.SupportEnds)
}

var defaultWorkloads = []string{"checkout", "search", "worker", "api-gateway", "payments", "cart", "inventory", "notifier", "auth", "ledger"}

// fleet builds the simulated fleet. It mirrors the TUI design renders.
func fleet(now time.Time) []*cluster {
	old := "20260704"
	cs := []*cluster{
		newCluster("prod-api", "us-east-1", "1.31",
			scenario{deprecatedAPI: true, tightPDB: true, orphanPods: 3, quotaVCPU: 38, budgets: 19, workloads: defaultWorkloads},
			[]state.Nodegroup{
				nodegroup("ng-system", "1.31", latestAMI("1.31"), 3),
				nodegroup("ng-general", "1.31", amiRelease("1.31", old), 6),
				nodegroup("ng-batch", "1.31", latestAMI("1.31"), 2),
				nodegroup("ng-spot", "1.31", latestAMI("1.31"), 4),
			},
			addons("1.31", map[string]string{"vpc-cni": "v1.18.3", "coredns": "v1.11.1"})),
		newCluster("prod-batch", "us-east-1", "1.32",
			scenario{quotaVCPU: 120, budgets: 6, workloads: []string{"etl", "spark-driver", "scheduler", "exporter"}},
			[]state.Nodegroup{
				nodegroup("ng-system", "1.32", latestAMI("1.32"), 2),
				nodegroup("ng-batch", "1.32", latestAMI("1.32"), 5),
			},
			addons("1.32", nil)),
		newCluster("prod-eu", "eu-west-1", "1.32",
			scenario{tightPDB: true, quotaVCPU: 64, budgets: 14, workloads: defaultWorkloads},
			[]state.Nodegroup{
				nodegroup("ng-system", "1.32", latestAMI("1.32"), 3),
				nodegroup("ng-general", "1.32", latestAMI("1.32"), 5),
				nodegroup("ng-spot", "1.32", latestAMI("1.32"), 3),
			},
			addons("1.32", nil)),
		newCluster("stage-api", "us-west-2", "1.33",
			scenario{quotaVCPU: 200, budgets: 8, workloads: defaultWorkloads},
			[]state.Nodegroup{
				nodegroup("ng-system", "1.33", latestAMI("1.33"), 2),
				nodegroup("ng-general", "1.33", latestAMI("1.33"), 3),
			},
			addons("1.33", nil)),
		newCluster("stage-data", "us-west-2", "1.33",
			scenario{quotaVCPU: 90, budgets: 4, workloads: []string{"kafka-connect", "flink", "loader", "exporter"}},
			[]state.Nodegroup{
				nodegroup("ng-system", "1.33", latestAMI("1.33"), 2),
				nodegroup("ng-stream", "1.33", amiRelease("1.33", old), 4),
				nodegroup("ng-spot", "1.33", amiRelease("1.33", old), 3),
			},
			addons("1.33", nil)),
		newCluster("dev-sandbox", "us-east-2", "1.30",
			scenario{quotaVCPU: 16, budgets: 2, workloads: []string{"playground", "preview", "worker"}},
			[]state.Nodegroup{
				nodegroup("ng-default", "1.30", amiRelease("1.30", old), 2),
			},
			addons("1.30", map[string]string{"coredns": "v1.11.1"})),
	}
	for _, c := range cs {
		c.refreshSupport(now, now)
	}
	return cs
}

// nodeName returns an EC2-style private DNS node name.
func (w *World) nodeName(region string) string {
	second := "2"
	switch region {
	case "us-east-1":
		second = "0"
	case "us-west-2":
		second = "4"
	case "us-east-2":
		second = "6"
	}
	return "ip-10-" + second + "-" + strconv.Itoa(1+w.rng.IntN(60)) + "-" + strconv.Itoa(2+w.rng.IntN(250))
}

// podName returns a Deployment-style pod name.
func (w *World) podName(workload string) string {
	const alnum = "bcdfghjklmnpqrstvwxz2456789"
	suffix := make([]byte, 5)
	for i := range suffix {
		suffix[i] = alnum[w.rng.IntN(len(alnum))]
	}
	return workload + "-" + strconv.FormatInt(int64(0x1000+w.rng.IntN(0xefff)), 16) + "-" + string(suffix)
}

// plural formats a count and a noun: "1 warning", "3 warnings".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}
