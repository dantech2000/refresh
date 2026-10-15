# Coming from eksctl or the AWS CLI

This page maps common `eksctl` and `aws eks` commands to `refresh`. `refresh`
does not create or delete clusters. It covers the upgrade work: status,
readiness, patch, upgrade.

## Read

| Job | eksctl or AWS CLI | refresh |
|---|---|---|
| List clusters | `aws eks list-clusters`, once for each region | `refresh cluster list -A` |
| Describe a cluster | `aws eks describe-cluster --name prod` | `refresh cluster describe prod` |
| List nodegroups | `eksctl get nodegroup --cluster prod` | `refresh nodegroup list -c prod` |
| Describe a nodegroup | `aws eks describe-nodegroup --cluster-name prod --nodegroup-name web` | `refresh nodegroup describe web -c prod` |
| List add-ons | `eksctl get addon --cluster prod` | `refresh addon list -c prod` |
| Upgrade insights | `aws eks list-insights --cluster-name prod` | `refresh cluster upgrade-check prod` |
| What is out of date, in every region | No single command | `refresh status -A` |

## Change

| Job | eksctl or AWS CLI | refresh |
|---|---|---|
| Scale a nodegroup | `eksctl scale nodegroup --cluster prod --name web --nodes 4` | `refresh nodegroup scale web -c prod --desired 4` |
| Roll a nodegroup onto the latest AMI | `aws eks update-nodegroup-version --cluster-name prod --nodegroup-name web` | `refresh nodegroup update web -c prod` |
| Update an add-on | `eksctl update addon --cluster prod --name vpc-cni` | `refresh addon update vpc-cni -c prod` |
| Update every add-on | One command for each add-on | `refresh addon update --all -c prod` |
| Upgrade a cluster | `eksctl upgrade cluster`, then each nodegroup, then each add-on | `refresh cluster upgrade prod --to 1.35` |
| Roll an upgrade back | `aws eks update-cluster-version` with a rollback, after the nodegroups | `refresh cluster rollback prod` |

## What refresh adds

- A health gate before every change. See
  [Health checks](concepts/health-checks.md).
- A dry run for every change, with an exit code that says what the real run
  would do.
- A wait for every change, and a verification after a roll.
- One exit-code contract for scripts. See [Exit codes](concepts/exit-codes.md).

## Output formats

| eksctl or AWS CLI | refresh |
|---|---|
| `--output json` | `-o json` |
| `--output yaml` | `-o yaml` |
| `--output text` | `-o plain` (TSV with a header row) |
| `--query` (JMESPath) | `-o json`, then `jq` |

## Contexts

`eksctl` and `kubectl` use the kubeconfig context. `refresh` has its own
contexts, which also hold the region and the AWS profile:

```bash
refresh context add prod --cluster prod-eks --region us-east-1 --profile prod --use
```

A command that only reads also takes the cluster of the current kubeconfig
context. A command that changes something never does. See
[Contexts](concepts/contexts.md).
