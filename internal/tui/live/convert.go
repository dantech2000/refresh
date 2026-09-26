package live

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/dantech2000/refresh/internal/health"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	statussvc "github.com/dantech2000/refresh/internal/services/status"
	"github.com/dantech2000/refresh/internal/services/upgrade"
	"github.com/dantech2000/refresh/internal/tui/state"
	"github.com/dantech2000/refresh/internal/types"
)

// toClusters turns status rows into fleet rows. A name that appears in more
// than one region gets its region in the key ("prod@eu-west-1"), so every
// row can be acted on.
func toClusters(rows []statussvc.ClusterStatus, latest string) ([]state.Cluster, map[string]target) {
	count := map[string]int{}
	for _, r := range rows {
		count[r.Name]++
	}
	targets := make(map[string]target, len(rows))
	out := make([]state.Cluster, 0, len(rows))
	for _, r := range rows {
		key := r.Name
		if count[r.Name] > 1 {
			key = r.Name + "@" + r.Region
		}
		targets[key] = target{name: r.Name, region: r.Region}
		c := state.Cluster{
			Name:            key,
			Region:          r.Region,
			Version:         r.Version,
			Latest:          latest,
			ExtendedSupport: r.Support.Tier == statussvc.SupportExtended || r.Support.Tier == statussvc.SupportUnsupported,
			Incomplete:      r.Incomplete,
		}
		if state.Minor(c.Latest) < state.Minor(c.Version) {
			c.Latest = "" // unknown or stale: never claim the cluster is behind
		}
		if r.State == "UPDATING" {
			c.Busy = "upgrading" // an update runs outside this TUI
		}
		if r.Support.StandardUntil != nil {
			c.SupportEnds = *r.Support.StandardUntil
		}
		for _, ng := range r.Nodegroups {
			c.Nodegroups = append(c.Nodegroups, state.Nodegroup{
				Name:       ng.Name,
				Version:    ng.Version,
				AMI:        ng.CurrentAMI,
				AMIStale:   ng.AMIStatus == types.AMIOutdated,
				AMIUnknown: ng.AMIStatus == types.AMIUnknown,
				Nodes:      int(ng.DesiredSize),
				Status:     ng.Status,
			})
			if ng.Status == "UPDATING" && c.Busy == "" {
				c.Busy = "updating " + ng.Name
			}
		}
		for _, a := range r.Addons {
			latest := a.Latest
			if !a.Behind {
				latest = a.Version // unknown or current: not stale
			}
			c.Addons = append(c.Addons, state.Addon{Name: a.Name, Version: a.Version, Latest: latest, Status: a.Status})
			if a.Status == "UPDATING" && c.Busy == "" {
				c.Busy = "updating add-ons"
			}
		}
		out = append(out, c)
	}
	sortClusters(out)
	return out, targets
}

// readinessChecks turns an upgrade-check report into the readiness list.
// cli is the command prefix that points at the cluster's region.
func readinessChecks(r *clustersvc.UpgradeReport, to, cli string) []state.Check {
	var out []state.Check
	add := func(c state.Check) { out = append(out, c) }

	if r.Support != nil {
		c := state.Check{Group: "CONTROL PLANE", Name: "support", Status: state.CheckPass, Summary: string(r.Support.Tier)}
		switch r.Support.Tier {
		case statussvc.SupportExtended:
			c.Status, c.Summary = state.CheckWarn, "in extended support"
			c.Detail = []string{"Extended support costs more per cluster hour."}
		case statussvc.SupportUnsupported:
			c.Status, c.Summary = state.CheckFail, "unsupported version"
		case statussvc.SupportStandard:
			if r.Support.StandardUntil != nil {
				c.Summary = "standard support until " + r.Support.StandardUntil.Format("2006-01-02")
			}
			if d := r.Support.DaysRemaining; d != nil && *d < 90 {
				c.Status = state.CheckWarn
				c.Summary += " (" + strconv.Itoa(*d) + " days)"
			}
		}
		add(c)
	}
	if hr := r.ControlPlane; hr != nil {
		add(state.Check{Group: "CONTROL PLANE", Name: "health", Status: healthStatus(*hr), Summary: hr.Message, Detail: hr.Details})
	}
	skew := state.Check{Group: "CONTROL PLANE", Name: "version skew", Status: state.CheckPass,
		Summary: r.Skew.ControlPlaneVersion + " → " + to, Detail: r.Skew.Findings}
	add(skew)

	for _, in := range r.Insights {
		c := state.Check{Group: "UPGRADE INSIGHTS", Name: in.Name, Summary: in.StatusReason, Source: "EKS upgrade insights · " + in.ID}
		switch strings.ToUpper(in.Status) {
		case "PASSING":
			c.Status = state.CheckPass
		case "ERROR":
			c.Status = state.CheckFail
		default: // WARNING, UNKNOWN
			c.Status = state.CheckWarn
		}
		if c.Summary == "" {
			c.Summary = strings.ToLower(in.Status)
		}
		if in.Description != "" {
			c.Detail = []string{in.Description}
		}
		if c.Status != state.CheckPass {
			c.Fix = []string{"Run " + cli + "cluster upgrade-check -c " + r.Cluster + " --id " + in.ID + " for the resources involved and the recommended fix."}
		}
		add(c)
	}

	for _, ng := range r.Skew.Nodegroups {
		c := state.Check{Group: "NODEGROUPS", Name: ng.Name, Status: state.CheckPass, Summary: "on " + ng.Version}
		switch {
		case ng.Blocking:
			c.Status, c.Summary = state.CheckFail, fmt.Sprintf("on %s · %d minors behind; patch before the upgrade", ng.Version, ng.MinorsBehind)
		case ng.MinorsBehind > 0:
			c.Status, c.Summary = state.CheckWarn, fmt.Sprintf("on %s · %d minor(s) behind", ng.Version, ng.MinorsBehind)
		}
		add(c)
	}
	for _, a := range r.Skew.Addons {
		c := state.Check{Group: "ADD-ONS", Name: a.Name, Status: state.CheckPass, Summary: a.Installed + " · current"}
		switch {
		case a.Incomplete:
			c.Status, c.Summary = state.CheckWarn, a.Installed+" · newest version unknown"
		case a.Behind:
			c.Status, c.Summary = state.CheckWarn, a.Installed+" · "+a.Latest+" available"
		}
		add(c)
	}
	if a := r.Rollback; a != nil {
		// Information, not a verdict on the upgrade: it never warns.
		c := state.Check{Group: "ROLLBACK", Name: "rollback window", Status: state.CheckPass,
			Summary: "to " + a.PreviousVersion + " until about " + a.AvailableUntil.UTC().Format("2006-01-02 15:04 MST"),
			Detail:  []string{"EKS can roll the control plane back one minor for about 7 days after an in-place upgrade. B dry-runs the rollback."},
			Source:  "EKS update history"}
		if n := a.Insights; n != nil {
			c.Detail = append(c.Detail, fmt.Sprintf("Rollback readiness insights: %d error, %d unknown, %d warning, %d passing. ERROR and UNKNOWN block the rollback.", n.Error, n.Unknown, n.Warning, n.Passing))
		}
		add(c)
	}
	for _, f := range r.Failures {
		add(state.Check{Group: "COULD NOT READ", Name: f.Name, Status: state.CheckWarn, Summary: f.Error})
	}
	return out
}

func healthStatus(hr health.HealthResult) state.CheckStatus {
	switch {
	case hr.Skipped:
		return state.CheckPending
	case hr.Status == health.StatusFail:
		return state.CheckFail
	case hr.Status == health.StatusWarn:
		return state.CheckWarn
	default:
		return state.CheckPass
	}
}

// regionFlag is the global flag that points a CLI command at t's region.
func regionFlag(t target) string { return "refresh --region " + t.region + " " }

func planRoll(c state.Cluster, t target, ngName string) (state.Plan, error) {
	var ng *state.Nodegroup
	for i := range c.Nodegroups {
		if c.Nodegroups[i].Name == ngName {
			ng = &c.Nodegroups[i]
		}
	}
	if ng == nil {
		return state.Plan{}, fmt.Errorf("nodegroup %q not found in %s", ngName, c.Name)
	}
	p := state.Plan{
		Title:   "Patch nodegroup · " + c.Name + " / " + ngName,
		Command: regionFlag(t) + "nodegroup update -c " + t.name + " -n " + ngName,
	}
	// An AMI patch keeps the nodegroup's own version, as `nodegroup
	// update` does.
	version := ng.Version
	if version == "" {
		version = "unknown" // the sweep could not read it
	}
	p.Changes = append(p.Changes, state.Change{Field: "AMI", From: ng.AMI, To: "latest recommended for " + version})
	p.Facts = []state.Fact{
		{Key: "nodes", Value: strconv.Itoa(ng.Nodes) + " replaced", Note: "the nodegroup's update config sets how many at a time"},
		{Key: "version", Value: version + " (unchanged)"},
		{Key: "region", Value: t.region},
	}
	if ng.Version != "" && ng.Version != c.Version {
		p.Facts[1].Note = "the control plane runs " + c.Version + "; `cluster upgrade` moves nodegroups"
	}
	p.Gates = []state.PlanGate{{Status: state.CheckPending, Text: "pre-flight health checks", Note: "run by the CLI command before it changes anything"}}
	switch {
	case ng.AMIUnknown:
		p.Facts = append(p.Facts, state.Fact{Key: "AMI status", Value: "unknown", Note: "the recommended-AMI lookup failed; the roll moves to the latest recommended AMI"})
	case !ng.AMIStale:
		p.Blocked = ngName + " already runs the latest AMI for " + ng.Version
	}
	return p, nil
}

func planAddons(c state.Cluster, t target) state.Plan {
	p := state.Plan{
		Title:   "Update add-ons · " + c.Name,
		Command: regionFlag(t) + "addon update --all -c " + t.name,
	}
	for _, a := range c.StaleAddons() {
		p.Changes = append(p.Changes, state.Change{Field: a.Name, From: a.Version, To: a.Latest})
	}
	p.Facts = []state.Fact{{Key: "order", Value: "one at a time", Note: "each waits for ACTIVE"}, {Key: "region", Value: t.region}}
	if len(p.Changes) == 0 {
		p.Blocked = "every add-on is on its newest compatible version"
	}
	return p
}

func planUpgrade(c state.Cluster, t target, plan *upgrade.Plan) state.Plan {
	p := state.Plan{
		Title:   fmt.Sprintf("Upgrade cluster · %s %s → %s", c.Name, plan.CurrentVersion, plan.TargetVersion),
		Command: regionFlag(t) + "cluster upgrade -c " + t.name + " --to " + plan.TargetVersion,
	}
	var blockers []string
	for _, hop := range plan.Hops {
		for _, s := range hop.Steps {
			key := string(s.Type)
			if s.Target != "" {
				key = s.Target
			}
			switch s.Status {
			case upgrade.StatusBlocked:
				blockers = append(blockers, key)
				p.Gates = append(p.Gates, state.PlanGate{Status: state.CheckFail, Text: key, Note: s.Reason})
			case upgrade.StatusManual:
				p.Gates = append(p.Gates, state.PlanGate{Status: state.CheckWarn, Text: key, Note: "manual: " + s.Reason})
			case upgrade.StatusCompleted:
				continue
			}
			switch s.Type {
			case upgrade.StepReadiness:
				// What the gate found in the preview, or when it runs.
				if s.Status != upgrade.StatusBlocked {
					p.Gates = append(p.Gates, state.PlanGate{Status: state.CheckPass, Text: "readiness for " + hop.To, Note: s.Reason})
				}
				continue
			case upgrade.StepControlPlane:
				continue // the control plane change is the plan's first line
			case upgrade.StepAddon, upgrade.StepNodegroup:
				v := "→ " + s.Version
				switch {
				case s.Status == upgrade.StatusManual || s.Version == "":
					v = s.Description
				case s.Type == upgrade.StepNodegroup:
					v = "roll to " + s.Version
				}
				p.Facts = append(p.Facts, state.Fact{Key: key, Value: v})
			default:
				p.Facts = append(p.Facts, state.Fact{Key: key, Value: s.Description})
			}
		}
	}
	for _, n := range plan.Notices {
		p.Gates = append(p.Gates, state.PlanGate{Status: state.CheckWarn, Text: n})
	}
	for _, f := range plan.Failures {
		p.Gates = append(p.Gates, state.PlanGate{Status: state.CheckWarn, Text: "could not read " + f.Name, Note: f.Error})
	}
	p.Gates = append(p.Gates,
		state.PlanGate{Status: state.CheckPending, Text: "nodegroup health gate", Note: "checked before each roll; warnings ask y/n"},
		state.PlanGate{Status: state.CheckPending, Text: "PDB drain blockers", Note: "checked before each roll; a blocker stops the run"})
	p.Changes = []state.Change{{Field: "control plane", From: plan.CurrentVersion, To: plan.TargetVersion}}
	if len(blockers) > 0 {
		p.Blocked = fmt.Sprintf("%d blocker(s): %s", len(blockers), strings.Join(blockers, ", "))
	}
	return p
}

// plural is n and noun, with an "s" unless n is 1.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
