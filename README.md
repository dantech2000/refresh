# refresh

A Go-based CLI tool to manage and monitor AWS EKS node groups using your local kubeconfig and AWS credentials.

## Features
- List all managed node groups in the current EKS cluster
- Color-coded output for AMI status (✅ Latest, ❌ Outdated)
- Built with [urfave/cli/v2](https://github.com/urfave/cli) and [fatih/color](https://github.com/fatih/color)

## Usage

```
go run main.go list
```

Example output:

```
Nodegroup        Status     InstanceType  Desired  AMI
ng-frontend      ACTIVE     m5.large      3        ✅ Latest
ng-backend       UPDATING   t3.medium     2        ❌ Outdated
```

## Requirements
- Go 1.18+
- AWS credentials (~/.aws/credentials or env vars)
- kubeconfig (~/.kube/config)

## Roadmap
- Rotate nodegroups
- Monitor rotation
- Update nodegroup version
- --json output for automation

## Security
- Does not log or store credentials
- Sanitizes input parameters

---

This is a work in progress. See the PRD for full requirements and future features.
