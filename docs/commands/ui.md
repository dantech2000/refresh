# refresh ui (experimental)

A full-screen terminal UI for the fleet: what is stale, readiness checks,
live nodegroup rolls, and cluster upgrades and rollbacks, with live event
and log streams.

```bash
refresh ui [flags]
```

!!! warning "Experimental"
    The UI is new, and its keys and screens can change between releases. It
    is read-only unless you pass `--allow-changes`. Try changes on a
    non-production cluster first. It is tested on macOS and Linux.

The UI runs the same code as the CLI. The fleet comes from `refresh status`,
readiness from `refresh cluster upgrade-check`, and every change from the
command that makes it: `nodegroup update`, `addon update --all`, `cluster
upgrade`, and `cluster rollback`. Each dry run shows the CLI command that makes
the same change, and `c` copies it.

## Flags

| Flag | Description |
|---|---|
| `--all-regions, -A` | Sweep all EKS-supported regions (or the `REFRESH_EKS_REGIONS` list) |
| `--region, -r` | Region(s) to sweep (repeatable). Without `-A` or `-r`, the UI sweeps the configured region |
| `--interval` | Time between fleet sweeps (default `1m`). `ctrl+r` sweeps now |
| `--allow-changes` | Let the UI start changes after their dry runs and gates. Without it, the UI is read-only |
| `--wait-timeout` | How long the UI watches a roll it started (default `40m`, `0` = no limit). The EKS update continues either way |
| `--kubeconfig`, `--kube-context` | The Kubernetes access for the live node view and the health gate's workload and PDB checks, as in `nodegroup update` |

## Screens

| Key | Screen | Shows |
|---|---|---|
| `1` | Fleet | Every cluster: version, stale nodegroups and add-ons, and status. A card for the selected cluster, and a live event feed |
| `2` | Cluster | Readiness for the next version (`r` runs `cluster upgrade-check`) |
| `3` | Rolls | Each nodegroup roll: nodes draining, joining, and terminated, the pods left on a draining node, and Kubernetes events |
| `4` | Upgrade | A cluster upgrade or rollback: its phases, the current step, and its timeline |

`esc` goes back to the fleet. A change in flight keeps running.

## Keys

The key bar at the bottom shows the keys that work on the current screen. `?`
lists them all.

| Key | Action |
|---|---|
| `↑` `↓`, `g` `G` | Move through a list, or jump to the first or last item |
| `enter` | Open the selected cluster |
| `r` | Run readiness for the next version |
| `p` | Dry-run a nodegroup patch (pick the nodegroup, then `enter`) |
| `a` | Dry-run the add-on updates |
| `U` | Dry-run a cluster upgrade to the next version |
| `B` | Dry-run a rollback one minor version back (only after readiness found a rollback window) |
| `y` | In a dry run: start the change (`--allow-changes` only) |
| `c` | In a dry run: copy the CLI command |
| `S` | Stop the upgrade after the current step. An EKS update in flight finishes |
| `P` | Pause the upgrade before its next phase |
| `y` / `n` | Answer the upgrade's question: go on, or stop after this step |
| `f` | Feed: all clusters, or the selected one |
| `w` | Feed: warnings and errors only |
| `space` | Freeze the live panes, or follow them again |
| `tab` `←` `→` | Switch the log source of a live pane |
| `[` `]` | Page through rolls or upgrades |
| `ctrl+r` | Sweep the fleet now |
| `q` | Quit. Changes in flight keep running in EKS |

## Read-only and --allow-changes

Without `--allow-changes`, the UI reads the fleet and dry-runs changes, and
each dry run names the CLI command to run. The top bar says `READ-ONLY`.

With `--allow-changes`, `y` in a dry run starts the change, and the top bar
says `CHANGES ON`. Before the UI starts anything, it checks again:

- The cluster is not changing. The UI reads the control plane, each
  nodegroup, and each add-on again, and refuses while one is changing, or
  when it cannot read one of them.
- **A nodegroup roll** runs the health gate of `nodegroup update` again, and
  refuses when the gate finds something the dry run did not show. It also
  refuses when a PodDisruptionBudget would block the drain or cannot be read
  (the UI has no `--force`), or when the nodegroup's version changed since
  the dry run.
- **An add-on update** previews again, and refuses when the plan changed.
- **A cluster upgrade** builds its plan again and asks (`y`/`n`) when the steps
  changed or a nodegroup's health gate warns. A PDB drain blocker before a
  roll stops the run.
- **A rollback** follows the rules of [`cluster rollback`](cluster.md): within
  the rollback window, nodegroups first, then add-ons, then the control plane.

The UI runs one change at a time on a cluster.

## Changes started elsewhere

A roll, upgrade, or rollback started with the CLI or the AWS console shows up
on the next sweep. The UI watches it, including the live node view, and marks it
`started elsewhere · watch only`. It cannot stop or pause it.

If you quit the UI (or it stops) during a change it started, the EKS update
in flight keeps running, and the next `refresh ui` watches it the same way.
The later phases of an upgrade or rollback do not start: run the same
upgrade or rollback again (in the UI, or with the CLI command the dry run
names), and it continues from the cluster's live state.

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
