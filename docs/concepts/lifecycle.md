# The upgrade lifecycle

`refresh` follows the work of keeping a cluster current: find what is out of
date, check readiness, patch, and upgrade. Each step is one command.

## 1. Status: what is out of date?

`refresh status` lists every cluster with its Kubernetes version, its EKS
support window, the nodegroups that do not run the latest AMI, and the add-ons
that have a newer version.

```bash
refresh status -A
```

The exit code tells a script what the command found: `2` when something is out
of date, `3` when a cluster left standard support. See
[`refresh status`](../commands/status.md).

## 2. Readiness: is the cluster ready to upgrade?

`refresh cluster upgrade-check` reads the EKS upgrade insights, the version
skew of the nodegroups and add-ons, the control-plane metrics, and the support
window. It changes nothing. Its exit code is the verdict, so it works as a CI
gate.

```bash
refresh cluster upgrade-check prod
```

## 3. Patch: roll nodegroups and update add-ons

`refresh nodegroup update` rolls managed nodegroups onto the latest recommended
AMI of their own Kubernetes version. `refresh addon update` moves add-ons to
the latest version that the cluster can run.

Every changing command works the same way:

1. It reads the whole cluster and builds a plan.
2. It refuses if the cluster or anything in it is already changing.
3. It runs the [health gate](health-checks.md).
4. It asks once. `--yes` skips the question.
5. It sends the change and waits for it. `--no-wait` returns once AWS accepts
   the change.
6. After a roll, it verifies that the nodegroups are `ACTIVE`, that the new
   nodes are `Ready`, and that no pod is newly `Pending`.

`--dry-run` stops after step 3 and prints the plan with the verdict of the
health gate.

## 4. Upgrade: move the cluster to a new version

`refresh cluster upgrade` runs the whole upgrade, one minor version at a time.
For each minor version it runs these phases in order:

1. The control plane.
2. The add-ons that the new control plane cannot run at their current version.
3. Each managed nodegroup, one after another.
4. The other add-ons.

The plan comes from the live state of the cluster. There is no state file. If
a run stops, run the same command again, and it goes on with what is left.

## Undo: roll an upgrade back

`refresh cluster rollback` returns a cluster to the version before its last
upgrade, within 7 days of that upgrade. It rolls the nodegroups back first,
then downgrades the add-ons that the older version cannot run, then rolls the
control plane back.

## The other commands

| Command | Use |
|---|---|
| `refresh cluster list`, `describe` | Read clusters. |
| `refresh nodegroup list`, `describe`, `scale` | Read nodegroups, and change their sizes. |
| `refresh addon list`, `describe` | Read add-ons. |
| `refresh context ...` | Save a cluster, region, and profile under a name. |
| `refresh ui` | A full-screen view of the fleet (experimental). |
