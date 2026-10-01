# refresh ui (experimental)

A full-screen terminal UI for the fleet: what is stale, readiness checks,
live nodegroup rolls, and cluster upgrades and rollbacks, with live event
and log streams.

```bash
refresh ui [flags]
```

!!! warning "Experimental"
    The UI is new, and its keys and screens can change between releases. It
    starts read-only: pass `--allow-changes`, or press `ctrl+u` in the UI, to
    allow changes. Try changes on a non-production cluster first. It is
    tested on macOS and Linux.

The UI runs the same code as the CLI. The fleet comes from `refresh status`,
readiness from `refresh cluster upgrade-check`, and every change from the
command that makes it: `nodegroup update`, `addon update --all`, `cluster
upgrade`, and `cluster rollback`. Each dry run shows the CLI command that makes
the same change, and `c` copies it.

## Flags

| Flag | Description |
|---|---|
| `--all-regions, -A` | Sweep all EKS-supported regions (or the `REFRESH_EKS_REGIONS` list) |
| `--region, -r` | Region(s) to sweep (repeatable). Without `-A` or `-r`, the UI sweeps the configured region and the region of the kubectl cluster (see [Start on the kubectl cluster](#start-on-the-kubectl-cluster)); with no region configured, the regions of the partition |
| `--interval` | Time between fleet sweeps (default `1m`). `ctrl+r` sweeps now |
| `--allow-changes` | Let the UI start changes after their dry runs and gates. Without it, the UI starts read-only, and `ctrl+u` allows changes later |
| `--wait-timeout` | How long the UI watches a roll it started (default `40m`, `0` = no limit). The EKS update continues either way |
| `--kubeconfig`, `--kube-context` | The Kubernetes access for the live node view and the health gate's workload and PDB checks, as in `nodegroup update` |

## Start on the kubectl cluster

When the kubectl context (or `--kube-context`) points at an EKS cluster, the
fleet cursor moves to that cluster when the sweep finds it, and the feed says
`kubectl cluster <name>`. If you use a key that acts first (move the cursor,
open a screen, or change a pane), the cursor stays where it is. A key that
does nothing yet, such as `enter` before the first sweep, does not count. The
cursor never moves while a dialog is open.

The UI reads the cluster's name and region from the kubeconfig: a cluster ARN
(as `aws eks update-kubeconfig` writes it), an eksctl cluster name, or the API
server address and the `aws eks get-token` arguments. Without `-r` or `-A`,
the UI adds the cluster's region to the sweep, when the region is in the same
AWS partition as the configured region.

A cluster with the same name and region is the kubectl cluster only when the
UI can check it:

- If the kubeconfig has a cluster ARN, the account must match.
- If the kubeconfig's server is an EKS endpoint, the endpoint must match.
- If the kubeconfig has neither (a proxied API server, for example), the name
  and region decide.

If the UI cannot read the cluster's ARN or endpoint, it waits for the next
sweep. The feed tells you when the cluster does not match:

- `kubectl cluster <name> is another cluster`: the sweep found a cluster with
  that name in a different account, or behind a different endpoint.
- `kubectl cluster <name> not found`: the sweep read the region and did not
  find the cluster.

Both usually mean that the AWS credentials are for a different account than
the kubectl context. Select the profile for that account with `--profile` or a
refresh context.

## Screens

| Key | Screen | Shows |
|---|---|---|
| `1` | Fleet | Every cluster: version, stale nodegroups and add-ons, and status. A card for the selected cluster, and a live event feed |
| `2` | Cluster | Readiness for the next version (`r` runs `cluster upgrade-check`) |
| `3` | Rolls | Each nodegroup roll: nodes draining, joining, and terminated, the pods left on a draining node, and Kubernetes events |
| `4` | Upgrade | A cluster upgrade or rollback: its phases, the current step, and its timeline |

`esc` goes back to the fleet. A change in flight keeps running. In a dialog
(a dry run, the key list, the nodegroup picker), `esc` and `q` close the
dialog instead, except in a dry run while its change is starting.

## Keys

The key bar at the bottom shows the keys that work on the current screen. `?`
lists them all. While a dialog is open (a dry run, the key list, the
nodegroup picker), only the dialog's own keys work.

| Key | Where | Action |
|---|---|---|
| `↑` `↓`, `g` `G` | Lists | Move through a list, or jump to the first or last item (`g` `G` not in the nodegroup picker) |
| `enter` | Fleet | Open the selected cluster |
| `r` | Fleet, Cluster | Run readiness for the next version |
| `p` | Fleet, Cluster | Dry-run a nodegroup patch (pick the nodegroup, then `enter`). A nodegroup on the control plane's version gets the newest AMI (`nodegroup update`); a nodegroup behind it rolls to the control plane's version (`cluster upgrade --only nodegroups -n`) |
| `a` | Fleet, Cluster | Dry-run the add-on updates |
| `U` | Fleet, Cluster | Choose what to upgrade, then dry-run it: the control plane only, the control plane and add-ons, or everything, to the next version. With the control plane on the newest version, catch the nodegroups (and add-ons) up to it. A single choice dry-runs at once |
| `B` | Fleet, Cluster | Dry-run a rollback one minor version back (only after readiness found a rollback window) |
| `y` | Dry run | Start the change (once changes are allowed: `--allow-changes` or `ctrl+u`) |
| `c` | Dry run | Copy the CLI command |
| `ctrl+u` | Screens, and a read-only dry run | Allow changes for this session (asks first). In a read-only dry run, the dry run then runs again with its live gates. Not in the key list or the nodegroup picker |
| `S` | Upgrade | Stop an upgrade or rollback this UI started, before its next phase or its next nodegroup roll. An EKS update in flight is never cancelled. Press `S` again to cancel the stop |
| `P` | Upgrade | Pause an upgrade or rollback this UI started, before its next phase. Press `P` again to go on |
| `y` / `n` | Upgrade | When the upgrade asks a question: go on, or stop after this step |
| `f` | Fleet | Feed: all clusters, or the selected one |
| `w` | Any | Feed: warnings and errors only |
| `space` | Any | Freeze the live panes, or follow them again |
| `tab` `←` `→` | Rolls, Upgrade | Switch the log source of a live pane |
| `[` `]` | Rolls, Upgrade | Page through rolls or upgrades |
| `ctrl+r` | Any | Sweep the fleet now |
| `q` | Any | Quit (in a dialog, close it; a dry run cannot close while its change is starting). Changes in flight keep running in EKS |

## Read-only and allowing changes

Without `--allow-changes`, the UI starts read-only. It reads the fleet and
dry-runs changes, and each dry run names the CLI command to run. The top bar
says `READ-ONLY`.

To change a cluster from the UI, allow changes in one of two ways:

- Start the UI with `refresh ui --allow-changes`.
- Press `ctrl+u` in the UI, then `y`. Changes are allowed until you quit.
  If a read-only dry run is open, it runs again with its live gates, and
  `y` then starts the change.

When changes are allowed, the top bar says `CHANGES ON`, and `y` in a dry run
starts the change. Before the UI starts anything, it checks again:

- The cluster is not changing. The UI reads the control plane, each
  nodegroup, and each add-on again, and refuses while one is changing, or
  when it cannot read one of them.
- **A nodegroup roll** runs the health gate of `nodegroup update` again, and
  refuses when the gate finds something the dry run did not show. It also
  refuses when a PodDisruptionBudget would block the drain or cannot be read
  (the UI has no `--force`), or when the nodegroup's version changed since
  the dry run. Without Kubernetes access (no kubeconfig context for the
  cluster), the PDB check is skipped, as in `nodegroup update`, and the dry
  run says so.
- **An add-on update** previews again, and refuses when the plan changed.
- **A cluster upgrade** builds its plan again, for the same parts as the dry
  run (as `cluster upgrade --only`), and asks (`y`/`n`) when the steps
  changed or a nodegroup's health gate warns. A PDB drain blocker before a
  roll stops the run.
- **A rollback** follows the rules of [`cluster rollback`](cluster.md): within
  the rollback window, nodegroups first, then add-ons, then the control plane.

The UI runs one change at a time on a cluster.

## Changes started elsewhere

A nodegroup roll or a control-plane update (an upgrade or a rollback) started
with the CLI or the AWS console shows up on the next sweep. The UI watches
it, including the live node view of a roll, and marks it `started elsewhere ·
watch only`. It cannot stop or pause it. An add-on update started elsewhere
marks the cluster busy, but the UI does not watch it. The UI shows changes
in flight, not past ones.

If you quit the UI (or it stops) during a change it started, the EKS update
in flight keeps running, and the next `refresh ui` watches it the same way.
The later phases of an upgrade or rollback do not start. To finish them, run
the CLI command the dry run named (`c` copies it) again: `cluster upgrade`
with the same `--to`, or the same `cluster rollback`. Each continues from
the cluster's live state. `U` in the UI plans the next
version from the version the cluster is on, so after the control plane
moved it offers the version after that.

## Terminal

- **Size:** at least 100 × 24. A smaller terminal shows the size it needs.
- **UTF-8:** the UI draws its borders, bars, and status marks in Unicode.
- **Color:** truecolor, 256 colors, or 16 colors, from `COLORTERM` and
  `TERM`. `NO_COLOR`, `--no-color`, and `TERM=dumb` turn color off. The
  status marks and the selection marker work without color.
- **Platforms:** tested on macOS and Linux (including tmux). Windows is not
  tested.

## Exit codes

`0` when you quit. `1` on an error, with no interactive terminal, or without
AWS credentials. A change's own result shows in the UI, not in the exit code.
