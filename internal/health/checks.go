package health

// Preflight returns the checks that a change runs before it starts, in
// display order. A describe command runs the same list with no targets.
func Preflight() []Check {
	return []Check{Nodes(), Capacity(), ControlPlane(), Workloads(), DrainBlockers(), PDBCoverage(), Utilization(), Quotas()}
}

// AWSOnly returns the checks of Preflight that need no Kubernetes client,
// for a command that reads many clusters and connects to none.
func AWSOnly() []Check {
	var out []Check
	for _, c := range Preflight() {
		if !c.Needs.Kube && !c.Needs.Metrics {
			out = append(out, c)
		}
	}
	return out
}
