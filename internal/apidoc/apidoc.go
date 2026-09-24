// Package apidoc defines the identity of the documents refresh prints with
// -o json and -o yaml: every document starts with an apiVersion and a kind,
// so a consumer can tell which contract (and which JSON Schema) applies.
//
// A document type implements Document. runner.EncodeStdout accepts only a
// Document, and it writes apiVersion and kind as the first two keys, so no
// document can leave without them. A document nested inside another one
// (the plan inside an UpgradeRun, the health verdict inside a
// NodegroupUpdate) gets no apiVersion or kind of its own.
//
// apidoc is a leaf package: it imports only the standard library.
package apidoc

// APIVersion is the version of the document contract. Within v1, changes are
// additive only: new fields, new enum values, and new kinds. A removal, a
// rename, or a type change needs a new version. docs/concepts/output.md has
// the full compatibility policy.
const APIVersion = "refresh.drod.dev/v1"

// SchemaBaseURL is where the JSON Schemas of this APIVersion are published;
// a kind's schema is at SchemaBaseURL + kind + ".json".
const SchemaBaseURL = "https://drod.dev/refresh/schema/v1/"

// Kind names the type of a document, in PascalCase.
type Kind string

// The document kinds. Add a kind for every new document a command prints.
const (
	KindFleetStatus          Kind = "FleetStatus"
	KindClusterList          Kind = "ClusterList"
	KindClusterDescription   Kind = "ClusterDescription"
	KindUpgradeCheck         Kind = "UpgradeCheck"
	KindInsightDescription   Kind = "InsightDescription"
	KindUpgradePlan          Kind = "UpgradePlan"
	KindUpgradeRun           Kind = "UpgradeRun"
	KindNodegroupList        Kind = "NodegroupList"
	KindNodegroupDescription Kind = "NodegroupDescription"
	KindNodegroupUpdate      Kind = "NodegroupUpdate"
	KindNodegroupUpdatePlan  Kind = "NodegroupUpdatePlan"
	KindFleetUpdate          Kind = "FleetUpdate"
	KindFleetUpdatePlan      Kind = "FleetUpdatePlan"
	KindHealthSummary        Kind = "HealthSummary"
	KindAddonList            Kind = "AddonList"
	KindAddonDescription     Kind = "AddonDescription"
	KindAddonUpdate          Kind = "AddonUpdate"
	KindAddonUpdateAll       Kind = "AddonUpdateAll"
)

// commands is the command line (after "refresh") that prints each kind.
var commands = map[Kind]string{
	KindFleetStatus:          "status",
	KindClusterList:          "cluster list",
	KindClusterDescription:   "cluster describe",
	KindUpgradeCheck:         "cluster upgrade-check",
	KindInsightDescription:   "cluster upgrade-check --id",
	KindUpgradePlan:          "cluster upgrade --dry-run",
	KindUpgradeRun:           "cluster upgrade --yes",
	KindNodegroupList:        "nodegroup list",
	KindNodegroupDescription: "nodegroup describe",
	KindNodegroupUpdate:      "nodegroup update",
	KindNodegroupUpdatePlan:  "nodegroup update --dry-run",
	KindFleetUpdate:          "nodegroup update --all-clusters",
	KindFleetUpdatePlan:      "nodegroup update --all-clusters --dry-run",
	KindHealthSummary:        "nodegroup update --health-only",
	KindAddonList:            "addon list",
	KindAddonDescription:     "addon describe",
	KindAddonUpdate:          "addon update",
	KindAddonUpdateAll:       "addon update --all",
}

// Command returns the command line, after "refresh", that prints a document
// of kind k, such as "cluster describe".
func (k Kind) Command() string { return commands[k] }

// Kinds returns every Kind, in documentation order.
func Kinds() []Kind {
	return []Kind{
		KindFleetStatus,
		KindClusterList,
		KindClusterDescription,
		KindUpgradeCheck,
		KindInsightDescription,
		KindUpgradePlan,
		KindUpgradeRun,
		KindNodegroupList,
		KindNodegroupDescription,
		KindNodegroupUpdate,
		KindNodegroupUpdatePlan,
		KindFleetUpdate,
		KindFleetUpdatePlan,
		KindHealthSummary,
		KindAddonList,
		KindAddonDescription,
		KindAddonUpdate,
		KindAddonUpdateAll,
	}
}

// Document is a value refresh prints as one -o json or -o yaml document.
// DocumentKind names the type; it must not depend on the value.
type Document interface {
	DocumentKind() Kind
}

// Enum is a string type with a closed set of values, such as a status. The
// JSON Schema lists EnumValues as the type's enum. A later v1 release may
// add values; consumers must accept a value they do not know.
type Enum interface {
	EnumValues() []string
}
