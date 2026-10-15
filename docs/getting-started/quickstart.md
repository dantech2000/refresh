# Quickstart

This page takes one cluster through the four steps. The examples use a cluster
named `prod` in `us-east-1`.

Before you start, log in to AWS. See
[Logging in to AWS](../concepts/logging-in.md).

## 1. Find what is out of date

```bash
refresh status -r us-east-1
```

```text
FLEET  8

CLUSTER                REGION     VERSION  SUPPORT           COMPUTE       STALE AMI                ADDONS                      HEALTH
▲ dev-orders-32        us-east-1  1.34     standard (49d)*   3 nodegroups  1/3 (130d)               1 (vpc-cni)                 ok
▲ dev-orders-56        us-east-1  1.31     extended (43d)*   4 nodegroups  1/4 (130d)               0                           ok
▲ prod-data-48         us-east-1  1.32     extended (160d)*  4 nodegroups  1/4 (130d), 1 behind CP  1 (eks-pod-identity-agent)  ok
▲ staging-checkout-16  us-east-1  1.35     standard (164d)*  2 nodegroups  0                        2 (coredns,vpc-cni)         ok

● 0 current   ▲ 8 need attention
```

`STALE AMI` reads "1/3 (130d)": one of three nodegroups does not run the latest
AMI, and that AMI is 130 days old. Use `-A` to read every region. See
[`refresh status`](../commands/status.md) for the columns.

## 2. Check that the cluster is ready to upgrade

```bash
refresh cluster upgrade-check prod
```

The command reports the EKS upgrade insights, the version skew of the
nodegroups and add-ons, and the support window. Its exit code is the verdict:
`0` ready, `2` review, `3` blocked, `4` incomplete.

## 3. Patch the nodegroups

1. Run a dry run. It prints the plan and the verdict of the health gate, and
   it changes nothing.

    ```bash
    refresh nodegroup update -c prod --dry-run
    ```

    ```text
    Plan   ● Proceed   NodegroupUpdate

    STEPS  3
      NAME  KIND       ACTION  REASON         FROM                                            TO                        NOTE
      ng-a  Nodegroup  Skip    AlreadyLatest  1.34                                            1.34                      -
      ng-b  Nodegroup  Skip    AlreadyLatest  1.34                                            1.34                      -
      ng-c  Nodegroup  Submit  AMIOutdated    1.34 (release 1.34.0-20260101, AMI ami-old134)  1.34 (AMI ami-latest134)  -

    HEALTH GATE
      decision  ● Go
    ```

2. Run the roll. The command asks once, starts the rolls, waits for them, and
   verifies the result.

    ```bash
    refresh nodegroup update -c prod
    ```

If the health gate blocks the roll, the command exits `3` and changes nothing.
The `HEALTH GATE` section names the check that blocked. See
[Health checks](../concepts/health-checks.md).

## 4. Patch the add-ons

```bash
refresh addon update --all -c prod --dry-run
refresh addon update --all -c prod
```

`--all` updates every installed add-on in dependency order.

## 5. Upgrade the cluster

```bash
refresh cluster upgrade prod --to 1.35 --dry-run
refresh cluster upgrade prod --to 1.35
```

The command moves the control plane, then the add-ons that the new version
needs, then each nodegroup, then the other add-ons. On a terminal it asks
before each phase. If the run stops, run the same command again. It goes on
with what is left.

## Save the cluster in a context

A context saves the cluster, the region, and the AWS profile under a name, so
you do not repeat the flags.

```bash
refresh context add prod --cluster prod --region us-east-1 --profile prod --use
refresh nodegroup list
```

See [Contexts](../concepts/contexts.md).

## Next

- [The upgrade lifecycle](../concepts/lifecycle.md): how the steps fit.
- [Cookbook](../usage/cookbook.md): recipes for scripts and CI.
- [Exit codes](../concepts/exit-codes.md): what each code means.
