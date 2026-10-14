# refresh ui (experimental)

`refresh ui` opens a full-screen terminal UI for the fleet. It is experimental:
its screens and keys can change between releases.

```bash
refresh ui [flags]
```

The UI shows the fleet with what is out of date, each cluster with its
nodegroups and add-ons, the readiness checks of `cluster upgrade-check` on
demand, and the changes of the cluster commands: their dry runs, their health
gates, and their progress while they run. It reads the fleet with the same
code as `refresh status`, every `--interval` and on ++ctrl+r++.

!!! note "Read-only unless you say otherwise"
    Without `--allow-changes` the UI starts nothing. The change keys still
    run the dry run and show the plan with the command that makes the same
    change, so you can copy it and run it in a shell. With the flag, ++y++ in
    a dry run starts that plan on a real cluster.

## Flags

| Flag | Meaning |
|---|---|
| `-A, --all-regions` | Read every region. |
| `-r, --region` | The regions to read. |
| `--interval` | Time between fleet sweeps. Default `1m`, at least `10s`. |
| `--allow-changes` | Let ++y++ in a dry run start the change. Without it the UI is read-only. |
| `--wait-timeout` | How long the UI follows a change it starts or watches. Unset means the default of the command that makes the change (`nodegroup update`, `addon update`, `cluster upgrade`, `cluster rollback`); `0` means no limit. |
| `--kubeconfig`, `--kube-context` | The Kubernetes access of a roll's health gate and node view, as for the commands. The copied command carries them. |

## Screens

| Key | Screen | Content |
|---|---|---|
| ++1++ | Fleet | The cluster table and the fleet feed. |
| ++2++ | Cluster | The nodegroups and add-ons of the selected cluster, and its readiness. |
| ++3++ | Change | The change on screen: its phases and steps, the nodes of a roll, and its log. ++bracket-left++ ++bracket-right++ move between the changes of the session. |

## Keys

Press ++question++ in the UI for the keys of the current screen.

| Key | Action |
|---|---|
| ++up++ ++down++ ++j++ ++k++ ++g++ ++shift+g++ | Move in a list. |
| ++enter++ | Open the selected cluster. |
| ++esc++ | Go back to the fleet, close a dialog, or cancel a dry run on its way. |
| ++r++ | Run the readiness checks of the selected cluster. |
| ++f++ | Fleet screen: show the feed of all clusters, or of the selected one. |
| ++p++ | Dry-run a roll of a stale nodegroup of the selected cluster (`nodegroup update`). With several stale nodegroups, a picker asks which. A nodegroup behind the control plane rolls to its version (`cluster upgrade --only nodegroups -n`). |
| ++a++ | Dry-run the update of the stale add-ons (`addon update --all`). |
| ++shift+u++ | Dry-run an upgrade: the control plane alone, with its add-ons, or everything, to the next minor; or catch up the nodegroups or the add-ons to the version the control plane runs (`cluster upgrade`). |
| ++shift+b++ | Dry-run the rollback of the control plane (`cluster rollback`). Offered after a readiness run found a rollback window. |
| ++ctrl+r++ | Sweep the fleet now. |
| ++ctrl+l++ | Redraw the screen. |
| ++q++ ++ctrl+c++ | Quit. |

In a dry run:

| Key | Action |
|---|---|
| ++y++ | Start the change. Needs `--allow-changes`. |
| ++c++ | Send the command to the terminal clipboard (OSC 52; the terminal decides whether it takes it). |
| ++esc++ ++n++ | Close. Not while the change is starting. |

On the Change screen, while the change offers them:

| Key | Action |
|---|---|
| ++y++ ++n++ | Answer the open question: go on, or stop. |
| ++shift+p++ | Pause before the next phase; again to resume. |
| ++shift+s++ | Stop before the next phase. What has started finishes. |
| ++space++ | Freeze the log; again to follow. |
| ++w++ | Show only the warnings and the failures of the log. |

## What a change from the UI does

A change from the UI is the same change as from the command line, with the
same plan, the same health gate, and the same waits. What differs is how
consent is given.

- The dry run is the plan the change runs. ++y++ starts that plan and no
  other: the run reads the cluster again, and asks when the live plan differs
  from the one you saw, or when the health gate warns. A blocked dry run has
  no start key.
- One dry run can start one change. A second ++y++, or a ++y++ on an older
  dry run, starts nothing.
- One change per cluster. A cluster that has a change in this UI, or that is
  changing in AWS, gets no dry run until it is done.
- A pause or a stop takes effect at the next point between phases of an
  upgrade or a rollback. A roll or an add-on update has no such point: once
  started, it runs to its end. A stop requested while a step runs does not
  turn a failure into a stop.
- A change started elsewhere, from the command line or the console, shows on
  the Change screen once the sweep sees its nodegroup or its control plane
  `UPDATING`. It is marked "started elsewhere · watch only": the UI follows
  it and never stops, pauses, or answers it. A control-plane update that is
  not a version change (logging, networking) is not watched.
- Quitting the UI does not stop a change. On the way out the UI names the
  changes still running, with their update IDs. The commands can watch them:
  `refresh nodegroup update` and `refresh cluster upgrade` attach to an
  update in flight.

## Terminal

- The UI needs a terminal of at least 100 by 24 cells. In a smaller terminal
  it shows one line that says so, and it comes back when you resize.
- `--no-color`, `NO_COLOR`, and `TERM=dumb` turn the colors off. The marks and
  labels still carry every status.
- While the UI runs, the standard error of `refresh` and of the programs that
  it starts, such as a kubeconfig credential plugin, goes to the null device.
  So their messages cannot write over the screen.

## Trying the UI without an account

`tools/fakefleet` serves a fake fleet, with its own clock and a Kubernetes API
per cluster, and the unchanged binary runs against it. From a checkout:

```bash
task run:tui:sim                      # refresh ui -A --allow-changes on the fake fleet
FAKEFLEET_FLAGS="-speed 10" task run:tui:sim
task run:sim -- status -A             # any command against the same fleet
```

The fake fleet places scripted situations on its first clusters (a blocked
drain, a degraded add-on, a roll in flight, a rollback window, a stuck
drain), and prints which cluster has which on start. `-speed` runs its clock
faster than the wall clock; `-roll`, `-addon`, and `-upgrade` set how long
each change takes on that clock. A change started with `task run:sim --
nodegroup update ...` in another terminal shows up in the UI as a change
started elsewhere.
