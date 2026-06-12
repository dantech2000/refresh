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
gate. See [`refresh status`](../commands/status.md).

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

See [`cluster upgrade-check`](../commands/cluster.md#upgrade-check).

---

## Patch a single nodegroup

Always preview first. `--changelog` prints the `amazon-eks-ami` release notes
between the current and target AMI so you know what's changing.

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
    Without a terminal and without `--yes`, a run that would otherwise prompt
    fails fast — so CI never hangs waiting on stdin.

---

## Update add-ons safely

Update every add-on in dependency-safe order (`vpc-cni` → `coredns`/`kube-proxy`
→ others), waiting for each to settle before the next.

```bash
refresh addon update prod-east --all --dependency-order --wait

# Skip an add-on you manage via Helm/GitOps
refresh addon update prod-east --all --dependency-order --wait --skip aws-load-balancer-controller
```

See [`addon update`](../commands/addon.md#update).

---

## Safe scale-down

Scaling down can strand pods behind a PodDisruptionBudget. Preview the exact PDBs
that would constrain the operation before touching anything.

```bash
# Preview the constraining PDBs (no changes)
refresh nodegroup scale prod-east -n ng-default --desired 2 --check-pdbs --dry-run

# Scale down for real, gated on PDBs, waiting for it to settle
refresh nodegroup scale prod-east -n ng-default --desired 2 --check-pdbs --wait
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
thing: control plane → add-ons → nodegroups, with a health gate after every
phase. EKS upgrades one minor at a time, so a multi-minor jump expands into
sequential hops. Always dry-run first.

```bash
# Print the ordered plan (exits non-zero if anything blocks)
refresh cluster upgrade -c prod-east --to 1.33 --dry-run

# Execute, confirming each mutating phase
refresh cluster upgrade -c prod-east --to 1.33

# Non-interactive run, skipping a Helm-managed add-on
refresh cluster upgrade -c prod-east --to 1.33 --yes --skip aws-load-balancer-controller
```

The plan is re-derived from live state on every run, so rerunning after a
failure (or Ctrl+C) resumes where it left off. See
[`cluster upgrade`](../commands/cluster.md#upgrade).
