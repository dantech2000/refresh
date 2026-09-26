package sim

import (
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/dantech2000/refresh/internal/tui/state"
)

// checkParallel is how many readiness checks run at once, as the real
// upgrade-check fans out its reads.
const checkParallel = 3

// checkDef is one simulated readiness check: the AWS call it makes, how long
// it takes, and its verdict.
type checkDef struct {
	group, name string
	api         string
	dur         time.Duration
	eval        func() state.Check
}

type readiness struct {
	w       *World
	c       *cluster
	st      state.Readiness
	defs    []checkDef
	due     []time.Time
	started []bool
	done    func(blockers, warnings int)
}

// newReadiness starts a readiness run for c. done, when set, runs once when
// every check has finished. The caller holds w.mu.
func (w *World) newReadiness(c *cluster, done func(blockers, warnings int)) *readiness {
	from := c.Version
	to := state.NextMinor(from)
	if from == c.Latest {
		to = from
	}
	r := &readiness{w: w, c: c, done: done, defs: w.checkDefs(c, to)}
	r.st = state.Readiness{Cluster: c.Name, From: from, To: to, StartedAt: w.now, Running: true}
	for _, d := range r.defs {
		r.st.Checks = append(r.st.Checks, state.Check{Group: d.group, Name: d.name, Status: state.CheckPending})
	}
	r.due = make([]time.Time, len(r.defs))
	r.started = make([]bool, len(r.defs))
	w.readiness[c.Name] = r
	w.checkRuns = append(w.checkRuns, r)
	return r
}

func (r *readiness) log(lvl state.Level, api, text string) {
	e := r.w.stamp(state.Event{Cluster: r.c.Name, Source: state.SourceCheck, Level: lvl, Subject: api, Text: text})
	r.st.Log = appendCapped(r.st.Log, e, logCap)
}

func (r *readiness) step() {
	if !r.st.Running {
		return
	}
	now := r.w.now
	running := 0
	for i := range r.defs {
		if !r.started[i] || r.st.Checks[i].Status != state.CheckRunning {
			continue
		}
		if now.Before(r.due[i]) {
			running++
			continue
		}
		res := r.defs[i].eval()
		res.Group, res.Name = r.defs[i].group, r.defs[i].name
		r.st.Checks[i] = res
		r.log(levelOf(res.Status), r.defs[i].api, res.Summary)
	}
	for i := range r.defs {
		if running >= checkParallel {
			break
		}
		if r.started[i] {
			continue
		}
		r.started[i] = true
		r.st.Checks[i].Status = state.CheckRunning
		r.due[i] = now.Add(r.defs[i].dur)
		r.log(state.LevelProgress, r.defs[i].api, "…")
		r.w.api(r.c.Name, r.defs[i].api, r.defs[i].name)
		running++
	}
	if slices.ContainsFunc(r.st.Checks, func(c state.Check) bool {
		return c.Status == state.CheckPending || c.Status == state.CheckRunning
	}) {
		return
	}
	r.st.Running = false
	b, wn := r.st.Blockers(), r.st.Warnings()
	lvl, text := state.LevelOK, "ready to upgrade"
	switch {
	case b > 0:
		lvl, text = state.LevelError, "readiness: "+plural(b, "blocker")
	case wn > 0:
		lvl, text = state.LevelWarn, "readiness: "+plural(wn, "warning")
	}
	r.w.emit(state.Event{Cluster: r.c.Name, Source: state.SourceCheck, Level: lvl, Subject: r.st.From + " → " + r.st.To, Text: text})
	if r.done != nil {
		r.done(b, wn)
	}
}

// snapshot deep-copies the run, so a caller that changes the copy cannot
// reach the world's checks.
func (r *readiness) snapshot() state.Readiness {
	st := r.st
	st.Checks = make([]state.Check, len(r.st.Checks))
	for i, c := range r.st.Checks {
		c.Detail = slices.Clone(c.Detail)
		c.Fix = slices.Clone(c.Fix)
		if c.Table != nil {
			t := state.Table{Header: slices.Clone(c.Table.Header)}
			for _, row := range c.Table.Rows {
				t.Rows = append(t.Rows, slices.Clone(row))
			}
			c.Table = &t
		}
		st.Checks[i] = c
	}
	st.Log = slices.Clone(r.st.Log)
	return st
}

func levelOf(s state.CheckStatus) state.Level {
	switch s {
	case state.CheckPass:
		return state.LevelOK
	case state.CheckWarn:
		return state.LevelWarn
	case state.CheckFail:
		return state.LevelError
	case state.CheckRunning:
		return state.LevelProgress
	default:
		return state.LevelInfo
	}
}

// evaluate runs every check at once, for a dry run.
func (w *World) evaluate(c *cluster) []state.Check {
	to := state.NextMinor(c.Version)
	if c.Version == c.Latest {
		to = c.Version
	}
	var out []state.Check
	for _, d := range w.checkDefs(c, to) {
		res := d.eval()
		res.Group, res.Name = d.group, d.name
		out = append(out, res)
	}
	return out
}

func pass(summary string) state.Check { return state.Check{Status: state.CheckPass, Summary: summary} }
func warn(summary string) state.Check { return state.Check{Status: state.CheckWarn, Summary: summary} }

// checkDefs lists the readiness checks for an upgrade of c to version to.
// The durations are fixed per check, so dry runs and real runs agree.
func (w *World) checkDefs(c *cluster, to string) []checkDef {
	now := w.now
	sc := c.scenario
	defs := []checkDef{
		{group: "CONTROL PLANE", name: "version skew", api: "DescribeCluster", dur: 1 * time.Second, eval: func() state.Check {
			if c.Version == c.Latest {
				return pass("already on " + c.Latest + ", the newest version")
			}
			return pass(c.Version + " → " + to + " is one minor")
		}},
		{group: "CONTROL PLANE", name: "platform version", api: "DescribeCluster", dur: 1 * time.Second, eval: func() state.Check {
			return pass("platform version eks.14")
		}},
		{group: "CONTROL PLANE", name: "standard support", api: "DescribeClusterVersions", dur: 2 * time.Second, eval: func() state.Check {
			days := int(c.SupportEnds.Sub(now).Hours() / 24)
			switch {
			case c.ExtendedSupport:
				ch := warn("in extended support since " + c.SupportEnds.Format("2006-01-02"))
				ch.Detail = []string{"Extended support costs more per cluster hour.", "Upgrade to a version in standard support to stop the extra cost."}
				return ch
			case days < 90:
				return warn("standard support ends " + c.SupportEnds.Format("2006-01-02") + " (" + strconv.Itoa(days) + " days)")
			default:
				return pass("standard support until " + c.SupportEnds.Format("2006-01-02"))
			}
		}},
		{group: "UPGRADE INSIGHTS", name: "deprecated APIs", api: "ListInsights", dur: 3 * time.Second, eval: func() state.Check {
			if !sc.deprecatedAPI || c.Version == c.Latest {
				return pass("no removed APIs in use")
			}
			return state.Check{
				Status:  state.CheckFail,
				Summary: "flowcontrol v1beta3 still in use",
				Detail: []string{
					"The API server received calls to flowcontrol.apiserver.k8s.io/v1beta3 in the last 30 days.",
					"Kubernetes " + to + " removes this version.",
				},
				Table: &state.Table{
					Header: []string{"USER AGENT", "REQUESTS", "LAST SEEN"},
					Rows:   [][]string{{"helm/v3.14.2", "412", "6h ago"}, {"kube-controller-manager", "38", "2d ago"}},
				},
				Fix: []string{
					"Move FlowSchema and PriorityLevelConfiguration manifests to flowcontrol v1.",
					"Upgrade the Helm charts that ship these objects.",
					"Run the check again. Insights refresh about once a day.",
				},
				Source: "EKS upgrade insights",
			}
		}},
		{group: "UPGRADE INSIGHTS", name: "kubelet version skew", api: "ListInsights", dur: 2 * time.Second, eval: func() state.Check {
			for _, ng := range c.Nodegroups {
				if state.Minor(c.Version)-state.Minor(ng.Version) >= 2 {
					return warn(ng.Name + " kubelet is two minors behind")
				}
			}
			return pass("kubelets within one minor")
		}},
		{group: "UPGRADE INSIGHTS", name: "cluster health", api: "DescribeInsight", dur: 2 * time.Second, eval: func() state.Check {
			return pass("no health issues")
		}},
	}
	for _, a := range c.Addons {
		defs = append(defs, checkDef{group: "ADD-ONS", name: a.Name, api: "DescribeAddonVersions", dur: 2 * time.Second, eval: func() state.Check {
			target := addonLatest(a.Name, to)
			switch {
			case a.Version != a.Latest:
				ch := warn(a.Version + " · " + a.Latest + " available")
				ch.Fix = []string{"Update the add-on before the upgrade, or let the upgrade update it."}
				return ch
			case target != "" && target != a.Version:
				return pass(a.Version + " · " + target + " after the upgrade")
			default:
				return pass(a.Version + " · compatible with " + to)
			}
		}})
	}
	for _, ng := range c.Nodegroups {
		defs = append(defs, checkDef{group: "NODEGROUPS", name: ng.Name, api: "DescribeNodegroup", dur: 2 * time.Second, eval: func() state.Check {
			switch {
			case ng.Version != c.Version:
				return warn("on " + ng.Version + " · patch before the next hop")
			case ng.LatestAMI != "" && ng.AMI != ng.LatestAMI:
				return warn("AMI " + ng.AMI + " · " + ng.LatestAMI + " available")
			default:
				return pass(strconv.Itoa(ng.Nodes) + " nodes Ready · AMI current")
			}
		}})
	}
	defs = append(defs,
		checkDef{group: "WORKLOADS", name: "PodDisruptionBudgets", api: "ListPodDisruptionBudgets", dur: 3 * time.Second, eval: func() state.Check {
			if sc.tightPDB {
				ch := warn(fmt.Sprintf("%d/%d allow disruption · checkout at its limit", sc.budgets-1, sc.budgets))
				ch.Detail = []string{"pdb/checkout allows 0 disruptions.", "A drain waits on it until a replica is spare."}
				ch.Fix = []string{"Scale checkout up by one replica for the roll, or relax the budget."}
				return ch
			}
			return pass(fmt.Sprintf("%d/%d allow disruption", sc.budgets, sc.budgets))
		}},
		checkDef{group: "WORKLOADS", name: "EC2 quota headroom", api: "GetServiceQuota", dur: 4 * time.Second, eval: func() state.Check {
			if sc.quotaVCPU < 24 {
				return warn(fmt.Sprintf("%d vCPU free · tight for surge nodes", sc.quotaVCPU))
			}
			return pass(fmt.Sprintf("%d vCPU free", sc.quotaVCPU))
		}},
	)
	return defs
}
