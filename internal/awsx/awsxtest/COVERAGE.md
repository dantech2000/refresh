# AWS operation coverage

The `TestMatrix*` tests call every AWS operation that refresh uses, with a real SDK
client against the fake server. `TestCoverageDoc` generates this file from their
table. To rewrite it, run `go test ./internal/awsx/awsxtest -run TestCoverageDoc -update`.

| Column | What `TestMatrix<Column>` proves |
|---|---|
| success | The SDK decodes the response into the fields the legacy product reads (the projections in `operations_test.go`). The request is logged once with its operation, service, region, and mutation flag. A mutating operation changed the world. |
| denied | The SDK returns the access-denied code of the service, in the modeled Go type where the SDK dispatches one for the operation (EKS, Service Quotas) and as `smithy.GenericAPIError` elsewhere. The call is not retried and changes nothing. |
| throttled | The SDK returns the throttling code of the service, in the same type rule, and classifies it as retryable throttling. A client with 3 attempts succeeds after 2 more throttles. |
| paged | With a forced page size of 1, every item arrives, one request per page. `-` means the API has no pages. |
| delayed | The response waits for the world's clock, then decodes. |
| cancelled | A call to a hanging handler returns `context.Canceled` when its context is cancelled, the handler exits, and nothing changed. |
| region | Called from a region that holds nothing, the operation fails with an API error or returns no items, and changes nothing. The success column also runs with a decoy of each kind in a second region. |

Not in the table, and proven once per service or protocol in `server_test.go`: the raw
error envelopes, the HTTP status of injected error codes, modeled not-found and
bad-token errors, scoped faults, a lost response after a mutation, peak concurrency,
and the request log's inputs.

## Modeled Update fields

The four Update operations model the fields the legacy product sends, listed here. A
request with any other field fails with `InvalidParameterException` and the message
"awsxtest does not model field X of Y" (`TestUnmodeledUpdateFields`). Every modeled
field, and whether the request had it, is part of what a client request token must match.

| Operation | Modeled fields | What the world does with them |
|---|---|---|
| `eks:UpdateClusterVersion` | `version`, `force`, `rollbackConfig.timeoutMinutes`, `clientRequestToken` | Sets the version on success. Records `force` and the timeout on the update. |
| `eks:UpdateNodegroupVersion` | `version`, `releaseVersion`, `force`, `clientRequestToken` | Sets the version and the release on success. Records `force` on the update. |
| `eks:UpdateNodegroupConfig` | `scalingConfig.desiredSize`, `.minSize`, `.maxSize`, `clientRequestToken` | Sets the sizes that are present on success. |
| `eks:UpdateAddon` | `addonVersion`, `configurationValues`, `clientRequestToken` | Sets the version, and the configuration when present, on success. |

## Not verified against real AWS

The tests prove that the SDK decodes what the fake sends. They do not prove that AWS
sends the same. Nothing below was compared with a captured AWS response. A live check
must settle each item before a test relies on it.

- **Every HTTP status in `service.go`.** The codes and statuses are from the AWS API
  references as the author knows them. The least certain: `ExpiredTokenException` and
  `ExpiredToken` as 403 for the query services and EKS; `OptInRequired` as 401 for EC2 and
  403 for the query services; `AuthFailure` as 401; EKS `ThrottlingException` as 429;
  `RequestLimitExceeded` as 503; every client error of SSM and Service Quotas as 400,
  including `AccessDeniedException` and `UnrecognizedClientException`.
- **The bad-credentials code per service** (`Fault.Unauthenticated`): `UnrecognizedClientException`
  for EKS, SSM, and Service Quotas; `InvalidClientTokenId` for STS, Auto Scaling, and
  CloudWatch; `AuthFailure` for EC2.
- **The CloudWatch error shape ID.** The fake sends `com.amazonaws.cloudwatch#<code>` in
  `__type` for every code. That is the modeled form for shapes such as `InvalidNextToken`.
  For codes with no shape in the model (`AccessDenied`, `Throttling`) the namespace AWS
  uses is a guess. The SDK reads the code from `X-Amzn-Query-Error` in both cases.
- **The Service Quotas `__type` prefix** `com.amazonaws.servicequotas#`. SSM sends the bare
  code in the fake. Both forms decode.
- **A reused client request token with a different payload** answers
  `InvalidParameterException`. The EKS reference does not say what EKS answers.
- **`DescribeUpdate` without `nodegroupName` or `addonName`** for an update of one answers
  `ResourceNotFoundException`, from the reference's "required if" wording.
- **`ResourceInUseException`** for a second update of a resource that has one in progress,
  and for an update of a nodegroup or an add-on while the control plane updates. The
  reverse, a control-plane update while a nodegroup or an add-on updates, is allowed.
- **An add-on shows its target version while it is UPDATING**, and its old version again
  after an update that failed. The legacy code attaches to an add-on that is UPDATING at
  the target version, which is where the first half comes from.
- **The size checks of `UpdateNodegroupConfig`** (min, desired, max in order) and their
  messages.

| Operation | Service | Protocol | Mutates | success | denied | throttled | paged | delayed | cancelled | region |
|---|---|---|---|---|---|---|---|---|---|---|
| `sts:GetCallerIdentity` | sts | awsQuery | no | yes | yes | yes | - | yes | yes | yes |
| `eks:ListClusters` | eks | restJson1 | no | yes | yes | yes | yes | yes | yes | yes |
| `eks:DescribeCluster` | eks | restJson1 | no | yes | yes | yes | - | yes | yes | yes |
| `eks:ListNodegroups` | eks | restJson1 | no | yes | yes | yes | yes | yes | yes | yes |
| `eks:DescribeNodegroup` | eks | restJson1 | no | yes | yes | yes | - | yes | yes | yes |
| `eks:ListAddons` | eks | restJson1 | no | yes | yes | yes | yes | yes | yes | yes |
| `eks:DescribeAddon` | eks | restJson1 | no | yes | yes | yes | - | yes | yes | yes |
| `eks:DescribeAddonVersions` | eks | restJson1 | no | yes | yes | yes | yes | yes | yes | yes |
| `eks:DescribeClusterVersions` | eks | restJson1 | no | yes | yes | yes | yes | yes | yes | yes |
| `eks:ListInsights` | eks | restJson1 | no | yes | yes | yes | yes | yes | yes | yes |
| `eks:DescribeInsight` | eks | restJson1 | no | yes | yes | yes | - | yes | yes | yes |
| `eks:StartInsightsRefresh` | eks | restJson1 | yes | yes | yes | yes | - | yes | yes | yes |
| `eks:DescribeInsightsRefresh` | eks | restJson1 | no | yes | yes | yes | - | yes | yes | yes |
| `eks:UpdateNodegroupVersion` | eks | restJson1 | yes | yes | yes | yes | - | yes | yes | yes |
| `eks:UpdateNodegroupConfig` | eks | restJson1 | yes | yes | yes | yes | - | yes | yes | yes |
| `eks:UpdateClusterVersion` | eks | restJson1 | yes | yes | yes | yes | - | yes | yes | yes |
| `eks:UpdateAddon` | eks | restJson1 | yes | yes | yes | yes | - | yes | yes | yes |
| `eks:DescribeUpdate` | eks | restJson1 | no | yes | yes | yes | - | yes | yes | yes |
| `eks:ListUpdates` | eks | restJson1 | no | yes | yes | yes | yes | yes | yes | yes |
| `ssm:GetParameter` | ssm | awsJson1_1 | no | yes | yes | yes | - | yes | yes | yes |
| `ec2:DescribeImages` | ec2 | ec2Query | no | yes | yes | yes | yes | yes | yes | yes |
| `ec2:DescribeInstances` | ec2 | ec2Query | no | yes | yes | yes | yes | yes | yes | yes |
| `ec2:DescribeLaunchTemplateVersions` | ec2 | ec2Query | no | yes | yes | yes | yes | yes | yes | yes |
| `ec2:DescribeSubnets` | ec2 | ec2Query | no | yes | yes | yes | yes | yes | yes | yes |
| `ec2:DescribeInstanceTypeOfferings` | ec2 | ec2Query | no | yes | yes | yes | yes | yes | yes | yes |
| `ec2:DescribeInstanceTypes` | ec2 | ec2Query | no | yes | yes | yes | yes | yes | yes | yes |
| `ec2:DescribeVpcs` | ec2 | ec2Query | no | yes | yes | yes | yes | yes | yes | yes |
| `autoscaling:DescribeAutoScalingGroups` | autoscaling | awsQuery | no | yes | yes | yes | yes | yes | yes | yes |
| `autoscaling:DescribeScalingActivities` | autoscaling | awsQuery | no | yes | yes | yes | yes | yes | yes | yes |
| `cloudwatch:GetMetricData` | cloudwatch | rpcv2Cbor | no | yes | yes | yes | yes | yes | yes | yes |
| `servicequotas:GetServiceQuota` | servicequotas | awsJson1_1 | no | yes | yes | yes | - | yes | yes | yes |
