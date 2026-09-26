# Cookbook

Task-oriented, copy-pasteable workflows that string the commands together. Each
recipe follows the `refresh` loop: **status → readiness → patch → upgrade**. See
the [upgrade lifecycle](../concepts/lifecycle.md) for the why.

---

## Fleet posture: what's stale, everywhere

Start every session here. One table, all clusters, all regions — Kubernetes
version, support window, stale AMIs, and add-ons behind latest.

```bash
# The front door
refresh status -A

# Narrow to "prod" clusters in two regions
refresh status prod -r us-east-1 -r us-west-2

# Machine-readable for a dashboard
refresh status -A -o json
```

`status` exits non-zero when the fleet needs attention, so it doubles as a CI
gate:

```bash
refresh status -A -o json > status.json
case $? in
  0) echo "fleet current" ;;
  2) echo "stale AMIs, add-ons behind, or health issues" ;;
  3) echo "a cluster is on extended support or unsupported" ;;
  4) echo "incomplete data: check the errors in status.json" ;;
esac
```

See [`refresh status`](../commands/status.md).

---

## Readiness: am I safe to upgrade?

A read-only pre-flight that surfaces AWS Cluster Insights plus control-plane vs.
nodegroup/add-on version skew. Nothing is mutated.

```bash
refresh cluster upgrade-check -c prod-east

# Include passing checks, as JSON for a gate
refresh cluster upgrade-check -c prod-east --show-passing -o json

# Drill into a flagged insight
refresh cluster upgrade-check -c prod-east --id <insight-id>
```

In CI, the exit code is the gate: `0` ready, `2` warnings, `3` blocked.

```bash
refresh cluster upgrade-check -c prod-east -o json > readiness.json
case $? in
  0) echo "ready" ;;
  2) echo "warnings: review readiness.json" ;;
  3) echo "blocked"; exit 1 ;;
  *) echo "check failed"; exit 1 ;;
esac
```

Add `--exit-zero` to keep the report and always exit `0`.

See [`cluster upgrade-check`](../commands/cluster.md#upgrade-check).

---

## Patch a single nodegroup

Always preview first. `--changelog` prints the `amazon-eks-ami` release notes
between the current and target AMI so you know what's changing. Bottlerocket
and Windows nodegroups get a link to their own release notes instead.

```bash
# Preview the roll + read the AMI changelog
refresh nodegroup update prod-east ng-default --dry-run --changelog

# Roll it, requiring a clean health gate
refresh nodegroup update prod-east ng-default --require-healthy
```

See [`nodegroup update`](../commands/nodegroup.md#update).

---

## Patch the whole fleet

Fleet mode discovers clusters across regions and rolls them serially with one
batch confirmation and a worst-outcome exit code. Dry-run, eyeball, then commit.

```bash
# Fleet-wide plan
refresh nodegroup update --all-clusters -r us-east-1 -r us-west-2 --dry-run

# Execute once the plan looks right
refresh nodegroup update --all-clusters -r us-east-1 -r us-west-2 --yes
```

Each cluster gets its own `--wait-timeout`. A region that can't be listed makes
the run exit `4`; see [fleet mode](../commands/nodegroup.md#fleet-mode).

---

## Unattended / CI patch

For cron, suppress prompts with `--yes`, treat health warnings as a hard stop
with `--require-healthy`, and emit a JSON summary with `-o json`. Branch on the
[exit code](../concepts/exit-codes.md).

```bash
refresh nodegroup update -c prod --yes --require-healthy -o json
case $? in
  0) echo "patched cleanly" ;;
  2) echo "health warnings — review" ;;
  3) echo "blocked by health — do not proceed" ;;
  4) echo "some updates failed to start" ;;
  5) echo "rolled, but verification flagged issues" ;;
esac
```

!!! warning "TTY-less runs need `--yes`"
    Without a terminal, with `--quiet`, or with `-o json`, a run that would
    otherwise prompt fails fast unless you pass `--yes` — so CI never hangs
    waiting on stdin. A nodegroup pattern that is not an exact name also
    needs `--yes`. Pass exact cluster names in scripts: `--yes` does not
    accept a partial cluster name. Since 0.11, `addon update`,
    `nodegroup scale`, and `cluster upgrade` also need `--yes` in a script,
    because they ask before they change anything.

---

## Update add-ons safely

Update every add-on in dependency-safe order (`vpc-cni` → `coredns`/`kube-proxy`
→ others), waiting for each to settle before the next.

```bash
# Lists the add-ons that would change and asks once
refresh addon update prod-east --all --dependency-order --wait

# Skip an add-on you manage via Helm/GitOps
refresh addon update prod-east --all --dependency-order --wait --skip aws-load-balancer-controller

# In a script: no prompt
refresh addon update prod-east --all --dependency-order --wait --yes -o json
```

See [`addon update`](../commands/addon.md#update).

---

## Safe scale-down

EKS does not honor PodDisruptionBudgets when a scaling change removes nodes: it
terminates them and their pods go down. `--check-pdbs` refuses the scale-down
(exit `3`) if it could remove more of a PDB's pods than the PDB allows. The
gate assumes the worst case: the removed nodes are the ones that hold the most
of the PDB's pods. If it can't read the PDBs, it refuses too.

```bash
# Preview the gate's verdict and the blocking PDBs (no changes)
refresh nodegroup scale prod-east -n ng-default --desired 2 --check-pdbs --dry-run

# Scale down for real, refused if a PDB blocks it, waiting for it to settle
refresh nodegroup scale prod-east -n ng-default --desired 2 --check-pdbs --wait

# Accept the disruption and scale down anyway (no prompt with --yes)
refresh nodegroup scale prod-east -n ng-default --desired 2 --check-pdbs --force --yes
```

See [`nodegroup scale`](../commands/nodegroup.md#scale).

---

## Stop repeating `--region` / `--profile`

Save your environments once as [contexts](../commands/contexts.md), then switch
by name. The active context fills in cluster/region/profile defaults.

```bash
refresh context add prod  --cluster prod-eks  --region us-east-1 --profile prod
refresh context add stage --cluster stage-eks --region us-west-2 --profile stage

refresh use prod
refresh nodegroup list           # targets prod-eks / us-east-1 / prod
refresh cluster upgrade-check    # same context, no flags

refresh use stage                # flip the whole environment
```

---

## Orchestrate a full cluster upgrade

When you're ready to move a minor version, let `refresh` sequence the whole
thing: control plane → nodegroups → add-ons, with a health gate after every
phase. Add-ons the new control plane cannot run update before the rolls. EKS upgrades one minor at a time, so a multi-minor jump expands into
sequential hops. Always dry-run first.

```bash
# Print the ordered plan (exits 3 if anything blocks)
refresh cluster upgrade -c prod-east --to 1.33 --dry-run

# Execute, confirming each mutating phase
refresh cluster upgrade -c prod-east --to 1.33

# Non-interactive run, skipping a Helm-managed add-on
refresh cluster upgrade -c prod-east --to 1.33 --yes --skip aws-load-balancer-controller
```

The plan is re-derived from live state on every run, so rerunning after a
failure (or Ctrl+C) resumes where it left off. `refresh` prints the exact
command to rerun. For CI, get one JSON document with the plan and what the
run did:

```bash
refresh cluster upgrade -c prod-east --to 1.33 --yes -o json 2>upgrade.log | jq '.report'
```

The run needs `eks:StartInsightsRefresh` and `eks:DescribeInsightsRefresh`
for the readiness gate. See [`cluster upgrade`](../commands/cluster.md#upgrade).
