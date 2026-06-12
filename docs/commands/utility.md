# Utility (version / install-man / completion)

Housekeeping commands: print the version, install the man page, and generate
shell completion scripts.

---

## refresh version

Print the running version (and, for release builds, the commit and build date).

```bash
refresh version [flags]
```

| Flag | Description |
|---|---|
| `--no-update-check` | Skip the check for a newer release (env `REFRESH_NO_UPDATE_CHECK`) |

!!! note "Opt-in update hint"
    On an interactive terminal, `version` performs a throttled, fail-silent
    check against GitHub Releases and prints a one-line hint to **stderr** when a
    newer release is available. The check runs at most once per day (cached
    under the user config dir), adds no measurable latency, and is skipped when
    stdout is piped/redirected or the build is `dev`. Disable it entirely with
    `--no-update-check` or `REFRESH_NO_UPDATE_CHECK=1`.

```bash
refresh version
refresh version --no-update-check
```

The global `--version` flag prints the same details.

---

## refresh install-man

Generate the man page from the CLI definition and install it to a
user-accessible directory — no `sudo` required. Works across macOS, Linux, and
Unix.

```bash
refresh install-man
```

Aliased as `install-manpage`. By default the page is written to
`$HOME/.local/share/man/man1/refresh.1`. If that man directory isn't on your
`MANPATH`, the command prints the `export MANPATH=...` line to add to your shell
profile. Once installed:

```bash
man refresh
```

---

## refresh completion

Output a shell completion script for `bash`, `zsh`, or `fish`.

```bash
refresh completion <bash|zsh|fish>
```

=== "bash"

    ```bash
    # Load for the current shell
    source <(refresh completion bash)

    # Or install persistently
    refresh completion bash > /usr/local/etc/bash_completion.d/refresh
    ```

=== "zsh"

    ```bash
    # Write somewhere on your $fpath
    refresh completion zsh > "${fpath[1]}/_refresh"
    ```

=== "fish"

    ```bash
    refresh completion fish > ~/.config/fish/completions/refresh.fish
    ```

!!! tip "Context-name completion"
    With completion installed, `refresh use <TAB>` completes saved
    [context](contexts.md) names straight from your local
    `context.yaml` — no AWS calls.
