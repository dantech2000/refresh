# AWS operation coverage

The `TestMatrix*` tests call every AWS operation that refresh uses, with a real SDK
client against the fake server. `TestCoverageDoc` generates this file from their
table. To rewrite it, run `go test ./internal/awsx/awsxtest -run TestCoverageDoc -update`.

| Column | What `TestMatrix<Column>` proves |
|---|---|
| success | The SDK decodes the response into the fields refresh reads. |
| denied | The SDK returns a typed API error with the access-denied code of the service, and does not retry. |
| throttled | The SDK returns a typed, retryable throttling error, and a client with retries succeeds. |
| paged | With a forced page size of 1, every item arrives. `-` means the API has no pages. |
| delayed | The response waits for the world's clock, then decodes. |
| cancelled | A call to a hanging handler returns when its context is cancelled, and the handler exits. |

| Operation | Service | Protocol | Mutates | success | denied | throttled | paged | delayed | cancelled |
|---|---|---|---|---|---|---|---|---|---|
| `sts:GetCallerIdentity` | sts | awsQuery | no | yes | yes | yes | - | yes | yes |
| `eks:ListClusters` | eks | restJson1 | no | yes | yes | yes | yes | yes | yes |
| `eks:DescribeCluster` | eks | restJson1 | no | yes | yes | yes | - | yes | yes |
| `eks:ListNodegroups` | eks | restJson1 | no | yes | yes | yes | yes | yes | yes |
| `eks:DescribeNodegroup` | eks | restJson1 | no | yes | yes | yes | - | yes | yes |
| `eks:ListAddons` | eks | restJson1 | no | yes | yes | yes | yes | yes | yes |
| `eks:DescribeAddon` | eks | restJson1 | no | yes | yes | yes | - | yes | yes |
| `eks:DescribeAddonVersions` | eks | restJson1 | no | yes | yes | yes | yes | yes | yes |
| `eks:DescribeClusterVersions` | eks | restJson1 | no | yes | yes | yes | yes | yes | yes |
| `eks:ListInsights` | eks | restJson1 | no | yes | yes | yes | yes | yes | yes |
| `eks:DescribeInsight` | eks | restJson1 | no | yes | yes | yes | - | yes | yes |
| `eks:StartInsightsRefresh` | eks | restJson1 | yes | yes | yes | yes | - | yes | yes |
| `eks:DescribeInsightsRefresh` | eks | restJson1 | no | yes | yes | yes | - | yes | yes |
| `eks:UpdateNodegroupVersion` | eks | restJson1 | yes | yes | yes | yes | - | yes | yes |
| `eks:UpdateNodegroupConfig` | eks | restJson1 | yes | yes | yes | yes | - | yes | yes |
| `eks:UpdateClusterVersion` | eks | restJson1 | yes | yes | yes | yes | - | yes | yes |
| `eks:UpdateAddon` | eks | restJson1 | yes | yes | yes | yes | - | yes | yes |
| `eks:DescribeUpdate` | eks | restJson1 | no | yes | yes | yes | - | yes | yes |
| `eks:ListUpdates` | eks | restJson1 | no | yes | yes | yes | yes | yes | yes |
| `ssm:GetParameter` | ssm | awsJson1_1 | no | yes | yes | yes | - | yes | yes |
| `ec2:DescribeImages` | ec2 | ec2Query | no | yes | yes | yes | yes | yes | yes |
| `ec2:DescribeInstances` | ec2 | ec2Query | no | yes | yes | yes | yes | yes | yes |
| `ec2:DescribeLaunchTemplateVersions` | ec2 | ec2Query | no | yes | yes | yes | yes | yes | yes |
| `ec2:DescribeSubnets` | ec2 | ec2Query | no | yes | yes | yes | yes | yes | yes |
| `ec2:DescribeInstanceTypeOfferings` | ec2 | ec2Query | no | yes | yes | yes | yes | yes | yes |
| `ec2:DescribeVpcs` | ec2 | ec2Query | no | yes | yes | yes | yes | yes | yes |
| `autoscaling:DescribeAutoScalingGroups` | autoscaling | awsQuery | no | yes | yes | yes | yes | yes | yes |
| `autoscaling:DescribeScalingActivities` | autoscaling | awsQuery | no | yes | yes | yes | yes | yes | yes |
| `cloudwatch:GetMetricData` | cloudwatch | rpcv2Cbor | no | yes | yes | yes | yes | yes | yes |
| `servicequotas:GetServiceQuota` | servicequotas | awsJson1_1 | no | yes | yes | yes | - | yes | yes |
