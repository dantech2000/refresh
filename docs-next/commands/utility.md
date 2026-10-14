# version and completion

## refresh version

```bash
refresh version [-o table|json|yaml] [--no-update-check]
```

The command prints the version, and the commit and build date of a release
build.

```text
refresh version: v0.16.0-158-gb62e59d
commit: b62e59d
built: 2026-10-14T01:16:43Z
```

`refresh --version` prints the version line alone. `-v` is not a flag.

### The update check

On a terminal, the command also checks GitHub Releases for a newer version. If
there is one, it prints a one-line hint on stderr.

- The check runs at most once a day.
- A failed check prints nothing.
- The check does not run when stdout is not a terminal.
- To turn it off, pass `--no-update-check`, or set `REFRESH_NO_UPDATE_CHECK` to
  any value other than `0`, `false`, or `no`.

## refresh completion

```bash
refresh completion <bash|zsh|fish|powershell>
```

The command prints a completion script for the shell. The script completes
commands, flags, and the names of clusters, nodegroups, and add-ons. See
[Shell completion](../getting-started/installation.md#shell-completion) for
where to install it.

`refresh completion zsh --help` shows the steps for each shell.

## Man pages

`refresh` has no command that installs man pages. The Homebrew cask installs
them, and the release archives have them in `man/man1`. See
[Installation](../getting-started/installation.md#pre-built-binary).

```bash
man refresh
man refresh-nodegroup-update
```
