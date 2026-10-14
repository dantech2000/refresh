# Installation

`refresh` is one static binary for Linux, macOS, and Windows, on amd64 and
arm64.

## Homebrew (macOS and Linux)

```bash
brew install dantech2000/tap/refresh
```

To upgrade later:

```bash
brew upgrade dantech2000/tap/refresh
```

The cask installs the man pages too.

## go install

This needs Go 1.26 or later.

```bash
go install github.com/dantech2000/refresh/cmd/refresh@latest
```

The binary goes to `$(go env GOPATH)/bin`. Make sure that directory is on your
`PATH`.

!!! note "The path changed after 0.16"
    Releases up to 0.16 installed from `github.com/dantech2000/refresh@latest`.
    That path no longer builds a binary.

## Pre-built binary

The release archives are checksummed. The checksums file is signed with
[cosign](https://github.com/sigstore/cosign) through GitHub's OIDC provider,
so there is no long-lived key.

1. Download the archive for your platform, `checksums.txt`,
   `checksums.txt.sig`, and `checksums.txt.pem` from the
   [releases page](https://github.com/dantech2000/refresh/releases/latest).
2. Verify the signature of the checksums file.

    ```bash
    cosign verify-blob \
      --certificate checksums.txt.pem \
      --signature checksums.txt.sig \
      --certificate-identity-regexp 'https://github.com/dantech2000/refresh' \
      --certificate-oidc-issuer https://token.actions.githubusercontent.com \
      checksums.txt
    ```

3. Verify the archive against the checksums file.

    ```bash
    sha256sum --check --ignore-missing checksums.txt
    ```

4. Extract the archive and install the binary. An archive is named
   `refresh_<version>_<os>_<arch>.tar.gz`.

    ```bash
    tar -xzf refresh_*_linux_amd64.tar.gz
    sudo mv refresh /usr/local/bin/
    ```

5. Optional: install the man pages. They are in the `man/man1` directory of
   the archive.

    ```bash
    sudo mkdir -p /usr/local/share/man/man1
    sudo cp man/man1/*.1 /usr/local/share/man/man1/
    ```

On macOS, a binary that you download in a browser is quarantined. If macOS
refuses to run it, clear the attribute:

```bash
xattr -dr com.apple.quarantine /usr/local/bin/refresh
```

The Homebrew cask does this for you.

## Shell completion

`refresh completion` prints a completion script for bash, zsh, fish, or
PowerShell. The scripts complete cluster, nodegroup, and add-on names from
your account.

=== "zsh"

    ```bash
    refresh completion zsh > "${fpath[1]}/_refresh"
    ```

=== "bash"

    ```bash
    refresh completion bash | sudo tee /etc/bash_completion.d/refresh > /dev/null
    ```

=== "fish"

    ```bash
    refresh completion fish > ~/.config/fish/completions/refresh.fish
    ```

=== "PowerShell"

    ```powershell
    refresh completion powershell | Out-String | Invoke-Expression
    ```

Start a new shell after you install a script.

## What refresh needs

- **AWS credentials** from the standard chain. See
  [Logging in to AWS](../concepts/logging-in.md).
- **IAM permissions.** See
  [Required IAM permissions](../concepts/configuration.md#required-iam-permissions).
- **A kubeconfig for the cluster** (optional). The health checks that read the
  Kubernetes API need it, and so do `--check-readiness`, `--show-workloads`,
  and the live view of a roll. Without it, a changing command asks before it
  goes on. See [Kubernetes access](../concepts/health-checks.md#kubernetes-access).

## Check the install

```bash
refresh version
```

```text
refresh version: v0.16.0-158-gb62e59d
commit: b62e59d
built: 2026-10-14T01:16:43Z
```
