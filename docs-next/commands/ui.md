# refresh ui (experimental)

`refresh ui` opens a full-screen terminal UI for the fleet. It is experimental:
its screens and keys can change between releases.

```bash
refresh ui [flags]
```

The UI shows the fleet with what is out of date, each cluster with its
nodegroups and add-ons, and the readiness checks of `cluster upgrade-check` on
demand. It reads the fleet with the same code as `refresh status`, every
`--interval` and on ++ctrl+r++.

!!! note "Read-only in this release"
    The UI does not start changes yet. `--allow-changes` and `--wait-timeout`
    are accepted and have no effect. Use the commands to change a cluster.

## Flags

| Flag | Meaning |
|---|---|
| `-A, --all-regions` | Read every region. |
| `-r, --region` | The regions to read. |
| `--interval` | Time between fleet sweeps. Default `1m`, at least `10s`. |
| `--allow-changes` | No effect yet. |
| `--wait-timeout` | No effect yet. |

## Screens

| Key | Screen | Content |
|---|---|---|
| ++1++ | Fleet | The cluster table and the fleet feed. |
| ++2++ | Cluster | The nodegroups and add-ons of the selected cluster, and its readiness. |

## Keys

Press ++question++ in the UI for the keys of the current screen.

| Key | Action |
|---|---|
| ++up++ ++down++ ++j++ ++k++ ++g++ ++shift+g++ | Move in a list. |
| ++enter++ | Open the selected cluster. |
| ++esc++ | Go back to the fleet, or close a dialog. |
| ++r++ | Run the readiness checks of the selected cluster. |
| ++f++ | Fleet screen: show the feed of all clusters, or of the selected one. |
| ++ctrl+r++ | Sweep the fleet now. |
| ++ctrl+l++ | Redraw the screen. |
| ++q++ ++ctrl+c++ | Quit. |

## Terminal

- The UI needs a terminal of at least 100 by 24 cells. In a smaller terminal
  it shows one line that says so, and it comes back when you resize.
- `--no-color`, `NO_COLOR`, and `TERM=dumb` turn the colors off. The marks and
  labels still carry every status.
- While the UI runs, the standard error of `refresh` and of the programs that
  it starts, such as a kubeconfig credential plugin, goes to the null device.
  So their messages cannot write over the screen.
