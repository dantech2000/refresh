# Cookbook

Recipes for common jobs. Each one names the exit codes that a script must
handle.

## See what is out of date, everywhere

```bash
refresh status -A --sort stale --desc
```

To limit the regions for every sweep, set the list once:

```bash
export REFRESH_EKS_REGIONS=us-east-1,us-west-2,eu-west-1
refresh status -A
```

For a dashboard, read the document:

```bash
refresh status -A -o json > status.json
jq -r '.clusters[] | select(.support.tier != "Standard") | "\(.name) \(.region) \(.support.tier)"' status.json
```

`refresh status` exits `2` when something is out of date and `3` when a cluster
left standard support. In a job that only collects the data, accept both:

```bash
refresh status -A -o json > status.json || [ $? -le 3 ]
```

## Gate a pipeline on upgrade readiness

```bash
refresh cluster upgrade-check prod --show-passing -o json > check.json
```

| Exit code | What the pipeline does |
|---|---|
| `0` | Go on. |
| `2` | Go on, or stop for a review. Your choice. |
| `3` | Stop. |
| `4` | Stop. The report is incomplete. |

To keep the report and never fail the job, add `--exit-zero`.

## Patch one cluster

1. Look at the plan and the AMI release notes.

    ```bash
    refresh nodegroup update -c prod --dry-run --changelog
    ```

2. Roll the nodegroups.

    ```bash
    refresh nodegroup update -c prod
    ```

3. Update the add-ons.

    ```bash
    refresh addon update --all -c prod
    ```

## Patch nodegroups by pattern

```bash
refresh nodegroup update -c prod -f name=web-*
```

The confirmation lists the nodegroups that match.

## Patch every cluster of some regions

1. Look at the plan for every cluster.

    ```bash
    refresh nodegroup update --all-clusters -r us-east-1,us-west-2 --dry-run
    ```

2. Run it. The command asks once, for all the clusters.

    ```bash
    refresh nodegroup update --all-clusters -r us-east-1,us-west-2
    ```

A cluster that is blocked or fails does not stop the next one.

## Patch from a nightly job

A job has no terminal, so it needs `--yes`. `--require-healthy` makes every
health warning a stop, so the job never goes past a warning that nobody saw.

```bash
#!/bin/sh
refresh nodegroup update -c prod --yes --require-healthy -o json > run.json
code=$?
case $code in
  0) echo "patched, or nothing to do" ;;
  3) echo "blocked: nothing changed"; jq -r '.clusters[].gate.reasons[]' run.json ;;
  4) echo "incomplete: check the failures"; jq -r '.failures[] | "\(.kind) \(.name): \(.error)"' run.json ;;
  5) echo "rolled, and the verification found an issue"; jq -r '.clusters[].verification[] | select(.outcome == "Fail") | .message' run.json ;;
  *) echo "failed" ;;
esac
exit $code
```

## Scale a nodegroup down safely

1. Look at the verdict of the health gate.

    ```bash
    refresh nodegroup scale web -c prod --desired 2 --dry-run
    ```

2. If the gate blocks, fix the PodDisruptionBudget that it names, or accept the
   disruption with `--force`.
3. Scale.

    ```bash
    refresh nodegroup scale web -c prod --desired 2
    ```

## Upgrade a cluster

1. Check readiness.

    ```bash
    refresh cluster upgrade-check prod
    ```

2. Look at the plan.

    ```bash
    refresh cluster upgrade prod --to 1.35 --dry-run
    ```

3. Upgrade. On a terminal, the command asks before each phase.

    ```bash
    refresh cluster upgrade prod --to 1.35
    ```

4. If the run stops, run the same command again.

To upgrade in parts, on different days:

```bash
refresh cluster upgrade prod --to 1.35 --only control-plane
refresh cluster upgrade prod --to 1.35 --only addons
refresh cluster upgrade prod --to 1.35 --only nodegroups
```

To leave an add-on alone that Helm or GitOps manages:

```bash
refresh cluster upgrade prod --to 1.35 --skip-addon coredns
```

## Roll an upgrade back

```bash
refresh cluster upgrade-check prod      # the ROLLBACK section shows the window
refresh cluster rollback prod --dry-run
refresh cluster rollback prod
```

## Stop repeating `-c`, `-r`, and `--profile`

```bash
refresh context add prod --cluster prod-eks --region us-east-1 --profile prod
refresh context add stage --cluster stage-eks --region us-east-1 --profile stage
refresh context use prod
refresh nodegroup list
```

For one shell or one job, set the variables:

```bash
export REFRESH_CONTEXT=stage      # a saved context
export REFRESH_CLUSTER=stage-eks  # or only the cluster
```
