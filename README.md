# Refresh

A Go-based CLI tool to manage and monitor AWS EKS node groups using your local kubeconfig and AWS credentials.

## Features

-   List all managed node groups in your EKS cluster
-   See AMI status for each nodegroup (✅ Latest, ❌ Outdated, ⚠️ Updating)
-   Detect and show nodegroups that are currently being updated
-   Update the AMI for all or specific nodegroups (rolling by default, with optional force)
-   Color-coded, readable CLI output

## Requirements

-   Go 1.23+
-   AWS credentials (`~/.aws/credentials` or environment variables)
-   kubeconfig (`~/.kube/config`)

## Usage

### List Nodegroups

List all managed nodegroups in a cluster, showing their status and AMI state:

```sh
go run main.go list --cluster <cluster-name>
```

**Example output:**

```
Nodegroup        Status     InstanceType  Desired  Current AMI         AMI Status
ng-frontend      ACTIVE     m5.large      3       ami-0abcd1234ef5678 ✅ Latest
ng-backend       ⚠️ UPDATING t3.medium    2       ami-0abcd1234ef5678 ⚠️ Updating
ng-legacy        ACTIVE     t3.medium     2       ami-0deadbeef123456 ❌ Outdated
```

-   `✅ Latest`: Nodegroup is using the latest recommended AMI for the cluster
-   `❌ Outdated`: Nodegroup AMI is not the latest
-   `⚠️ Updating`: Nodegroup is currently being updated (status and AMI status both show this)

### Update AMI for Nodegroups

Trigger a rolling update to the latest AMI for all or a specific nodegroup:

```sh
# Update all nodegroups
go run main.go update-ami --cluster <cluster-name>

# Update a specific nodegroup
go run main.go update-ami --cluster <cluster-name> --nodegroup <nodegroup-name>

# Force update (replace all nodes, even if already latest)
go run main.go update-ami --cluster <cluster-name> --force
```

**Example output:**

```
Updating nodegroup ng-frontend...
Update started for nodegroup ng-frontend
Updating nodegroup ng-backend...
Update started for nodegroup ng-backend
```

## Security

-   Does not log or store credentials
-   Sanitizes input parameters

---

This is a work in progress...
