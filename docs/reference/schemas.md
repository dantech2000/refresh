# JSON schemas

Every `-o json` and `-o yaml` document starts with
`apiVersion: refresh.drod.dev/v2` and a `kind`. Each kind has a JSON Schema
(draft 2020-12), generated from the Go types of `refresh`. The schema of a
kind is at `https://drod.dev/refresh/schema/v2/<Kind>.json`.

| Kind | Printed by | Schema |
|---|---|---|
| `FleetStatus` | `refresh status` | [FleetStatus.json](https://drod.dev/refresh/schema/v2/FleetStatus.json) |
| `ClusterList` | `refresh cluster list` | [ClusterList.json](https://drod.dev/refresh/schema/v2/ClusterList.json) |
| `ClusterDescription` | `refresh cluster describe` | [ClusterDescription.json](https://drod.dev/refresh/schema/v2/ClusterDescription.json) |
| `UpgradeCheck` | `refresh cluster upgrade-check` | [UpgradeCheck.json](https://drod.dev/refresh/schema/v2/UpgradeCheck.json) |
| `InsightDescription` | `refresh cluster upgrade-check --insight` | [InsightDescription.json](https://drod.dev/refresh/schema/v2/InsightDescription.json) |
| `NodegroupList` | `refresh nodegroup list` | [NodegroupList.json](https://drod.dev/refresh/schema/v2/NodegroupList.json) |
| `NodegroupDescription` | `refresh nodegroup describe` | [NodegroupDescription.json](https://drod.dev/refresh/schema/v2/NodegroupDescription.json) |
| `AddonList` | `refresh addon list` | [AddonList.json](https://drod.dev/refresh/schema/v2/AddonList.json) |
| `AddonDescription` | `refresh addon describe` | [AddonDescription.json](https://drod.dev/refresh/schema/v2/AddonDescription.json) |
| `Plan` | Every `--dry-run`, and a run that was blocked before any change | [Plan.json](https://drod.dev/refresh/schema/v2/Plan.json) |
| `Run` | Every other run of a changing command | [Run.json](https://drod.dev/refresh/schema/v2/Run.json) |
| `Version` | `refresh version` | [Version.json](https://drod.dev/refresh/schema/v2/Version.json) |

[Output formats](../concepts/output.md) explains the documents, and says what
can change within `v2`.

## Validate a document

Use any validator for draft 2020-12. For example:

```bash
refresh cluster list -o json > clusters.json
check-jsonschema --schemafile https://drod.dev/refresh/schema/v2/ClusterList.json clusters.json
```

## The `v1` schemas

The documents of releases up to 0.16 were `refresh.drod.dev/v1`. Their schemas
stay at `https://drod.dev/refresh/schema/v1/<Kind>.json`.
