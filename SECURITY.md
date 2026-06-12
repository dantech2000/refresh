# Security Policy

## Supported versions

`refresh` is distributed via Homebrew and GitHub Releases. Security fixes are
applied to the latest released version. Please upgrade to the most recent
release before reporting an issue.

## Reporting a vulnerability

Please **do not** open a public GitHub issue for security vulnerabilities.

Instead, report privately via GitHub's [private vulnerability reporting](https://github.com/dantech2000/refresh/security/advisories/new)
("Report a vulnerability" under the repository's **Security** tab). If that is
unavailable, email the maintainer at the address on their GitHub profile.

Please include:

- A description of the vulnerability and its impact.
- Steps to reproduce (a proof of concept if possible).
- Affected version(s) and platform.

You can expect an acknowledgement within a few days. Once a fix is available we
will coordinate a release and credit you in the advisory unless you prefer to
remain anonymous.

## Scope

`refresh` is a CLI that talks to the AWS EKS / EC2 / Auto Scaling / CloudWatch
APIs using your local AWS credentials and kubeconfig. It does not run a server
or persist credentials. Reports about credential handling, command injection,
supply-chain integrity (release artifacts, the Homebrew cask), or unsafe
defaults on mutating commands are all in scope.

## Supply-chain integrity

Release artifacts are checksummed, accompanied by an SBOM (syft), and the
checksums file is signed with cosign keyless signing. Verify a download with:

```bash
cosign verify-blob \
  --certificate checksums.txt.pem \
  --signature checksums.txt.sig \
  --certificate-identity-regexp 'https://github.com/dantech2000/refresh' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
```
