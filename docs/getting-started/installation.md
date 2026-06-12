# Installation

`refresh` ships as a single static binary (no runtime dependencies) for Linux,
macOS, and Windows on amd64 and arm64.

## Homebrew (macOS / Linux)

```bash
brew install dantech2000/tap/refresh
# upgrade later with:
brew upgrade dantech2000/tap/refresh
```

## go install

Requires Go **1.26+**:

```bash
go install github.com/dantech2000/refresh@latest
```

The binary lands in `$(go env GOPATH)/bin` — make sure that's on your `PATH`.

## Pre-built binary (with signature verification)

Download the archive for your platform from the
[releases page](https://github.com/dantech2000/refresh/releases/latest), then
verify it before use.

Release artifacts are checksummed, and the checksums file is signed with
[cosign](https://github.com/sigstore/cosign) using keyless (OIDC) signing — no
long-lived keys. Verify the checksum signature, then the archive:

```bash
# 1) verify the signed checksums file
cosign verify-blob \
  --certificate checksums.txt.pem \
  --signature checksums.txt.sig \
  --certificate-identity-regexp 'https://github.com/dantech2000/refresh' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt

# 2) verify your downloaded archive against the (now-trusted) checksums
sha256sum --check --ignore-missing checksums.txt

# 3) extract and install
tar -xzf refresh_*_$(uname -s)_$(uname -m).tar.gz
sudo mv refresh /usr/local/bin/
```

!!! tip "macOS Gatekeeper"
    The Homebrew cask clears the quarantine attribute automatically. For a
    manually-downloaded binary you may need:
    `xattr -dr com.apple.quarantine /usr/local/bin/refresh`.

## Shell completion

`refresh` generates completion scripts for bash, zsh, and fish:

=== "zsh"

    ```bash
    refresh completion zsh > "${fpath[1]}/_refresh"
    # then restart your shell
    ```

=== "bash"

    ```bash
    refresh completion bash | sudo tee /etc/bash_completion.d/refresh > /dev/null
    ```

=== "fish"

    ```bash
    refresh completion fish > ~/.config/fish/completions/refresh.fish
    ```

## Man page

```bash
refresh install-man   # installs to your man path (no sudo required)
man refresh
```

## Prerequisites

- **AWS credentials** via the standard chain (env vars, shared config/profiles,
  SSO, or an instance/role profile). See
  [Configuration & AWS auth](../concepts/configuration.md).
- **A kubeconfig** *(optional)* — only needed for the workload/PDB pre-flight
  health checks (`nodegroup update`, `nodegroup scale --check-pdbs`). Without
  it, kube-dependent checks degrade gracefully to "skipped".

## Verify the install

```bash
refresh version
```
