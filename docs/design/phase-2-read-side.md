# Phase 2 design note: the read side

Status: draft 6. Steps 1 to 8 of section 10 are built, and the parts of step 9 that need no AWS account. Sections 13 to 16 list what the code and its reviews changed in this note. Where a section above and those two differ, they are right.

Draft 2: It applies the review of draft 1 and the owner's decisions, which section 11 records. It belongs to `REWRITE.md` and stays inside `CONTRACT.md`.

Scope: `internal/fleet`, `internal/fleet/fleetdata`, `internal/view`, cluster target resolution and the `-f/--filter` helper in `internal/cmdutil`, the v2 read documents in `internal/apidoc`, and seven commands: `status`, `cluster list`, `cluster describe`, `nodegroup list`, `nodegroup describe`, `addon list`, `addon describe`. Watch mode is included.

Out of scope: health checks, every changing command, the TUI. Kubernetes data is named only where it will attach.

## Summary of the decisions

| # | Decision |
|---|---|
| 1 | Provenance is one `State` value for each field group, stored next to the group. Data fields stay plain values. |
| 2 | `Need` is a struct of booleans. `Fetcher.Fetch` reads one cluster and always returns a snapshot, with an error when the cluster itself could not be read. |
| 3 | The concurrency limit moves into the AWS clients: at most `--max-concurrency` requests in flight for each region. A sweep also works on at most that many clusters of a region at once, so rows finish in order. |
| 4 | Documents are separate types in `apidoc`. Three functions in `fleet` map a snapshot to them. Views read documents, never snapshots. |
| 5 | A column has one `Value` function for its text and an optional `Status` function for its glyph and color. Plain output and the table share `Value`, so they cannot differ in text. |
| 6 | Every command starts all the calls that depend on nothing in one wave. The deepest read command is 6 sequential round trips, down from 10. |

## 1. Types

### Version

Five places in the old tree parse a Kubernetes minor version. One type replaces them.

```go
package fleet

// Version is a Kubernetes minor version, such as 1.33. The zero value means
// that the version is not known.
type Version struct {
	Major, Minor int
}

func ParseVersion(s string) (Version, error)
func (v Version) String() string        // "1.33"; "" for the zero value
func (v Version) IsZero() bool
func (v Version) Compare(o Version) int // -1, 0, or +1
```

`ParseVersion` rules: trim spaces, drop one leading `v`, read `major.minor`, and ignore everything after a second `.`, a `-`, or a `+`. Both parts must be plain digits, so `1.-5` is an error. `Version` is comparable, so it is a map key for the support calendar and the memos.

Add-on versions are a different grammar (`v1.18.1-eksbuild.3`). They stay strings. `fleet.CompareAddonVersions(a, b string) int` is a behaviour-frozen port of `legacy/internal/services/addons/version_analyzer.go`, with its fuzz tests.

### Provenance

```go
// State says how much of a field group the fetcher read.
type State uint8

const (
	NotRequested State = iota // the caller did not ask for the group
	Collected                 // every call answered
	Partial                   // part was read; Failures names what is missing
	Failed                    // a call failed and nothing was read
	NotAttempted              // the run ended before the call was sent
)
```

A field group is the set of fields that one call, or one short chain of calls, fills. Each group has one `State`. Cluster-level groups keep their states in `Snapshot.Have`. An item keeps its own `State`, and an item-level group such as `Nodegroup.AMI` keeps a `State` inside the group. Data fields are plain values.

Rule: a field is valid only when the `State` of its group is `Collected`. A zero value in a group that is not `Collected` means nothing.

A value that depends on two groups is not stored. It is a method that returns the value and whether it is known, so no caller can read it against one state and forget the other.

```go
// VersionBehind reports whether n runs an older minor than the control
// plane. It is known when n and the cluster were both read.
func (s *Snapshot) VersionBehind(n Nodegroup) (behind, known bool)

// AMIStatus compares the current AMI with the latest one. UPDATING and
// CUSTOM nodegroups are known from the nodegroup alone. Any other needs
// the AMI group. A nodegroup with no instance to read is Unknown and known.
func (n Nodegroup) AMIStatus() (status apidoc.AMIStatus, known bool)

// Behind reports whether a newer compatible version exists. It is known
// when the add-on and its latest version were both read.
func (a Addon) Behind() (behind, known bool)
```

Two alternatives were rejected.

| Alternative | Why not |
|---|---|
| A pointer or an `Optional[T]` for each field | About 40 fields get wrapped. Every read unwraps, and five states do not fit in nil or not nil. |
| One set of collected field names on the snapshot, such as `snap.Has(FieldAMI)` | The state sits far from the data. One failed nodegroup out of ten needs a second keyed structure. It is a small language that a reader must learn. |

The chosen form has one cost: code can read a field without checking its state. Two things limit that. Only three kinds of code read `State`: the document mappers in section 5, the methods above with the counters built on them, and phase 3 and 4 planners. Views read documents, where "not collected" is "the key is absent". A test sets each group to each state and asserts that the mapper omits the keys.

### Snapshot and its parts

```go
// Snapshot is one cluster as one Fetch read it.
type Snapshot struct {
	Cluster    Cluster
	Nodegroups []Nodegroup // sorted by name
	Addons     []Addon     // sorted by name
	Have       Have
	Failures   []apidoc.Failure // every failed read that counts; see section 4
	FetchedAt  time.Time
}

// Have holds the state of each cluster-level field group.
type Have struct {
	Cluster    State // DescribeCluster
	Support    State // the support window of the cluster's version
	Compute    State // how the cluster runs nodes
	VPC        State // Network.VPCCIDR
	Nodegroups State // the list; each Nodegroup has its own State
	Addons     State // the list; each Addon has its own State
}

type Cluster struct {
	Name, Region string // always set

	// From DescribeCluster. Valid when Have.Cluster is Collected.
	ARN, Status, PlatformVersion, Endpoint string // Status is the EKS value
	Version       Version
	CreatedAt     time.Time
	Tags          map[string]string
	UpgradePolicy string // the EKS value: STANDARD, EXTENDED, or ""
	AutoMode      bool
	HealthIssues  []Issue
	Network       Network
	Security      Security

	Support Support            // valid when Have.Support is Collected
	Compute apidoc.ComputeType // valid when Have.Compute is Collected
}
type Support struct {
	Tier                     apidoc.SupportTier
	StandardEnd, ExtendedEnd time.Time
	AutoUpgrade              bool // the upgrade policy is STANDARD
	BuiltIn                  bool // the dates come from fleetdata, not from EKS
}
type Nodegroup struct {
	Name  string
	State State // DescribeNodegroup

	Status, AMIType, ReleaseVersion, CapacityType string // EKS values
	Version               Version
	InstanceTypes         []string
	Desired, Min, Max     int32
	AutoScalingGroups     []string
	LaunchTemplate        LaunchTemplate // ID and Version; zero when there is none
	HealthIssues          []Issue
	CreatedAt, ModifiedAt time.Time

	AMI            AMI
	Instances      []Instance
	InstancesState State

	// From Kubernetes. Phase 3 fills them; phase 2 leaves NotRequested.
	ReadyNodes int
	ReadyState State
}
type AMI struct {
	State           State
	Current, Latest string // AMI IDs

	// CurrentCreatedAt is the creation date of Current. Fetch reads it only
	// for a stale AMI, with Need.AMIAge. Zero means not read.
	CurrentCreatedAt time.Time
}
type Addon struct {
	Name  string
	State State // DescribeAddon

	Version, Status            string
	Health                     apidoc.AddonHealth
	ARN, ServiceAccountRoleARN string
	Configuration              string // as EKS returns it
	Issues                     []Issue
	CreatedAt, ModifiedAt      time.Time

	Latest      string // "" when EKS publishes no compatible version
	LatestState State
}
```

`Network`, `Security`, `Issue`, `Instance`, and `LaunchTemplate` have the fields of their documents in section 5.

An item that was listed and then could not be described stays in its slice with its `Name` and a `State` of `Failed` or `NotAttempted`. The old tree dropped such nodegroups from `nodegroup list` and counted them in `status`. One rule now applies in every command.

A snapshot's source is AWS for every group in phase 2. The Kubernetes groups carry their own `State`, and phase 3 adds a `KubeAt` time beside `FetchedAt`. Phase 3 fills them with a second function that takes the snapshot and a kube client. `Fetch` never takes a kube client.

### Enums

The enums are document values, so `apidoc` defines them once and `fleet` uses them. Each has a `Values()` method for the schema generator.

```go
package apidoc

type AMIStatus string   // Latest, Outdated, Updating, Custom, Unknown
type AddonHealth string // Healthy, Failed, InProgress, Unknown
type SupportTier string // Standard, Extended, Unsupported, Unknown
type ComputeType string // ManagedNodegroups, AutoMode, Karpenter, None
```

The pure functions that produce them live in `fleet`.

| Function | Rule, first match wins |
|---|---|
| `amiStatus(amiType, status, current, latest)`, behind `Nodegroup.AMIStatus` | `UPDATING` gives `Updating`. `CUSTOM` gives `Custom`. An empty ID gives `Unknown`. Equal IDs give `Latest`. Else `Outdated`. |
| `addonHealth(status)` | `ACTIVE` gives `Healthy`. `DEGRADED` and the three `*_FAILED` values give `Failed`. `CREATING`, `UPDATING`, `DELETING` give `InProgress`. Else `Unknown`. |
| `supportFor(version, calendar, policy, now)` | Before the standard end: `Standard`. Before the extended end: `Extended`. A known date in the past: `Unsupported`. No dates: `Unknown`. |

The AMI status and `AMI.State` are independent. A nodegroup with no instance to read has `AMI.State: Collected` and the status `Unknown`. A failed lookup has `AMI.State: Failed`, and `AMIStatus` returns not known. Fetch makes no latest-AMI call for a `CUSTOM` nodegroup, which has no recommended AMI.

`supportFor` keeps three old rules: a version older than the oldest calendar row is `Unsupported`, a partial day counts as a whole day left, and the `STANDARD` policy sets `AutoUpgrade`.

## 2. Fetching

### API

```go
// Need says what Fetch reads beyond the cluster itself. Fetch sets the
// fields that a set field depends on.
type Need struct {
	Nodegroups   bool // list and describe every nodegroup
	AMI          bool // current and latest AMI of each nodegroup; sets Nodegroups
	AMIAge       bool // the creation date of each stale AMI; sets AMI
	Instances    bool // the EC2 instances of each nodegroup; sets Nodegroups
	Addons       bool // list and describe every add-on
	LatestAddons bool // the latest compatible version of each add-on; sets Addons
	Support      bool // the support window
	Compute      bool // the compute type; sets Nodegroups
	VPC          bool // the VPC CIDR

	// OnlyNodegroups and OnlyAddons name the items to read. Fetch describes
	// these by name and makes no list call for that kind.
	OnlyNodegroups []string
	OnlyAddons     []string
}

// Fetcher reads clusters and shares lookups between them. Use one Fetcher
// for one run of a command. The zero value is ready.
type Fetcher struct {
	Now func() time.Time // nil means time.Now
	// unexported memos
}

// Clusters returns the names of the clusters in the region of c.
func (f *Fetcher) Clusters(ctx context.Context, c *awsx.Clients) ([]string, error)

// Fetch reads one cluster. The snapshot is never nil. The error is the
// error of DescribeCluster: when it is set, the snapshot holds the name,
// the region, and the failure, which is the row a sweep prints. Any other
// read that fails is a Failure in the snapshot and a State on its group,
// and no error.
func (f *Fetcher) Fetch(ctx context.Context, c *awsx.Clients, cluster string, need Need) (*Snapshot, error)
```

`Need` is a struct of booleans because that is how Go libraries pass options, and a call reads as `fleet.Need{Nodegroups: true, AMI: true}`. A bitmask reads no better and cannot hold `OnlyNodegroups`. One function for each use would put seven command names into `fleet`, and phases 3 and 4 would add more.

Draft 1 returned only the snapshot, with an `Err()` method. `errcheck` cannot see a forgotten method call, so the error is a return value. The error wraps the SDK error, so `awsx.IsNotFound` works on it. A single-cluster command returns it. A sweep keeps the snapshot as the row and drops the error, because the same failure is in the snapshot.

`REWRITE.md` writes `fleet.Fetch(ctx, clients, cluster, Need)`. It becomes a method because the memos need an owner.

The retry policy comes with the clients, and `Fetch` takes the plain `*awsx.Clients`. A single-cluster command passes `provider.Clients("")`. A sweep of named regions passes `provider.Clients(region)`. Only a default sweep passes `provider.SweepClients(region).Clients`, which has the short dial timeout and does not retry a dial failure.

### The concurrency limit

`--max-concurrency` is the limit on the requests in flight for one region. The default is 32. `awsx` enforces it with one middleware.

```go
// In awsx.ProviderOptions:
MaxInFlight int // requests in flight for each region; 0 means no limit

// ErrNotSent is in the chain of the error of a call whose first attempt
// never left: the context ended while it waited for a slot.
var ErrNotSent = errors.New("request not sent")
```

The rules of the middleware:

- **One semaphore for each region.** The `Provider` owns it. The three client sets of a region (`Clients`, `SweepClients`, `PollClients`) share it, and so do the seven services.
- **Position.** It sits in the Finalize step of the SDK stack, after `Retry` and before signing. A slot therefore covers one attempt: credential retrieval, signing, the round trip, and the read of the body. The slot is free during a backoff sleep and between the pages of a paged call.
- **`ErrNotSent`.** It is returned only when the first attempt of a call got no slot before the context ended. A later attempt that gets no slot returns the real error of the attempt before it, in a wrapper that `awsx.IsRetryable` refuses, so the retryer stops. A part of the middleware in the Initialize step keeps that state for the call.

The request limit alone is not enough. If a sweep started every cluster of a region at once, the work would run breadth-first, and a deadline would leave every row partly read. The old code left finished rows and rows it never started (`legacy/internal/services/status/service.go`). So a sweep also bounds the clusters it works on:

- In each region, `fanout.ForEach(ctx, names, limit, ...)` runs the clusters with the same number as the limit, `--max-concurrency`. Names are in sorted order, so rows finish roughly in order.
- This number never lowers the request ceiling. Every cluster in work has at least one request in flight or waiting, so 32 clusters can fill 32 slots, also in `cluster list`, where a cluster is one call. A smaller number would leave slots idle there.
- At a deadline, at most 32 rows of a region are partly read. The rows before them are complete. The rows after them were never started, and the sweep marks them `NotAttempted`, as the old code did.
- Inside one cluster, the fan-out over nodegroups and add-ons has no limit of its own. The request limit bounds it.

The old tree nested its limits: 8 clusters at once, each with two fan-outs of 8, and 4 regions at once. `PeakConcurrency` in the fake now measures the same number that the flag sets.

The default of 32 has a condition, which `CONTRACT.md` records. The retryer has no client-side rate limiter and at most about 3 s of backoff over its 5 attempts, and every region now runs at once. A burst that EKS throttles would end as failures and exit 4. The default stays 32 only if the throttle test in section 9 passes and a `task test:live` run of `status -A` completes without a `Throttled` failure. If either fails, the default is 16.

### Calls for each command

A wave is a set of calls that start together. The number of waves is the number of sequential round trips. N is nodegroups, M is add-ons, C is clusters in a region.

| Command | Wave 1 | Wave 2 | Wave 3 | Wave 4 and later |
|---|---|---|---|---|
| `cluster list` | `ListClusters` | `DescribeCluster` × C | | |
| `cluster describe` | `DescribeCluster`, `ListNodegroups`, `ListAddons`, `DescribeClusterVersions` | `DescribeNodegroup` × N, `DescribeAddon` × M, `DescribeVpcs` | | |
| `nodegroup list` | `DescribeCluster`, `ListNodegroups` | `DescribeNodegroup` × N | `GetParameter` for each distinct version and AMI type, one `DescribeAutoScalingGroups` for each 50 groups, `DescribeLaunchTemplateVersions` for each nodegroup with a template | 4: one `DescribeInstances` with an `instance-id` filter |
| `nodegroup describe` | `DescribeCluster`, `DescribeNodegroup` | `GetParameter`, `DescribeAutoScalingGroups`, `DescribeLaunchTemplateVersions` | one `DescribeInstances` with an `instance-id` filter | |
| `addon list` | `ListAddons`, `DescribeCluster` | `DescribeAddon` × M | | |
| `addon describe` | `DescribeAddon`, `DescribeCluster` | | | |
| `status`, each region | `ListClusters`, `DescribeClusterVersions` | for each cluster: `DescribeCluster`, `ListNodegroups`, `ListAddons` | `DescribeNodegroup` × N, `DescribeAddon` × M, `DescribeAddonVersions` for each new add-on and version, two Karpenter `DescribeInstances` probes when N is 0 and Auto Mode is off | 4: as wave 3 of `nodegroup list`. 5: one `DescribeInstances`. 6: one `DescribeImages` with an `image-id` filter, only when an AMI is stale |

Old and new, in sequential round trips. The old numbers come from reading the code path with no concurrency limit.

| Command | Old | New | Calls, new |
|---|---|---|---|
| `cluster list`, one region | 4 | 2 | 1 + C |
| `cluster describe` | about 8, or about 11 with `--detailed` | 2 | 5 + N + M |
| `nodegroup list` | 5 to 8 | 3 or 4 | 2 + N + 2 to 3 |
| `nodegroup describe` | 5 to 8, plus 2 with `--show-instances` | 3 | 5 or 6 |
| `addon list` | 3 | 2 | 2 + M |
| `addon describe` | 3 | 1 | 2 |
| `status`, each region | 8 to 10 | 5, or 6 with a stale AMI | 2 + C × (5 + N + M) |

Where the old counts are uncertain:

- `cluster describe`: I counted `ListClusters`, `DescribeCluster`, `ListAddons`, `DescribeAddon`, and `DescribeClusterVersions`, all in series. The health checker runs in the same series and I did not trace its calls. The measured 394 ms fits about three more.
- `nodegroup list` and `status`: the range is the path to the current AMI. A launch template with an image is 1 call, an Auto Scaling group is 2, and a template without an image is 3.
- The measured "13 calls' worth" for `cluster list` is not depth. The depth is 4. A region has 25 clusters, the old code ran 8 at once, and each took 3 calls in series: 1 + 4 × 3 = 13. Call volume divided by the limit sets the time.

What the new design drops or shares:

| Old call | New design |
|---|---|
| `ListClusters` to resolve a name, in every single-cluster command | Dropped. The name is exact. The list runs only after a not-found answer. |
| `ListAddons` to resolve an add-on name in `addon describe` | Dropped, for the same reason. |
| `ListNodegroups` and `DescribeNodegroup` for the node count in `cluster list` | Dropped with the `NODES` column. `status` shows the nodegroup count, and `cluster describe` shows the nodegroups. |
| `DescribeClusterVersions` for each version, in series before the fan-out | One unfiltered call for each region, in wave 1. |
| `DescribeAutoScalingGroups` for each nodegroup | One call for each cluster, with up to 50 group names. More groups take more calls in the same wave. |
| `DescribeInstances` for each nodegroup | One call for each cluster, which selects one instance of each group with an `instance-id` filter. |
| The second `DescribeAutoScalingGroups` and `DescribeInstances` of `--show-instances` | Shared with the AMI lookup. |
| The second Karpenter probe, in series after the first | Both probes run in one wave. |
| `diag.WithOperation` tags on errors | Dropped. The SDK's `smithy.OperationError` names the service and the operation. |

The current AMI keeps the old precedence: the launch template image first, then an instance of the first Auto Scaling group. The group call starts in the same wave as the template call. That costs at most one unused call for a cluster and keeps the depth at 4.

The batched calls must not let one bad item fail the others. Three rules:

- **Instances are selected with the `instance-id` filter, never with `InstanceIds`.** `DescribeInstances` with `InstanceIds` fails the whole call with `InvalidInstanceID.NotFound` when one ID is gone, and during a roll instances go all the time. The old code made one call for each nodegroup, so it lost one nodegroup (`legacy/internal/aws/ami.go`). With the filter, a missing instance is absent from the answer. Its nodegroup then has no instance to read: the AMI group is `Collected` and the status is `Unknown`, as for an empty group today.
- **`DescribeImages` uses the `image-id` filter** for the same reason: `ImageIds` fails the call with `InvalidAMIID.NotFound` when one AMI is deregistered.
- **Chunks.** A `DescribeAutoScalingGroups` call names at most 50 groups. A filter carries at most 100 values. This relies on EC2 accepting 200 values for one filter, which is to confirm in `task test:live`. A filtered call is paged, so it goes through `awsx.ListAll`.

The fake does not know the `instance-id` and `image-id` filters yet. Step 5 of the build order adds them to `awsxtest`.

`DescribeCluster` runs in `addon list` and `addon describe` so that "cluster not found" and "add-on not found" are told apart by type and not by message text. It adds a call and no round trip.

The unfiltered `DescribeClusterVersions` call must return every version that a cluster can still run. `task test:live` checks that. A version missing from the answer uses the built-in calendar, as today.

### Memos

All three use `fanout.Memo`, which does not keep a failed read. They live in the `Fetcher`, so they end with the run.

| Memo | Key | Value |
|---|---|---|
| Support calendar | region | `map[Version]` of the two end dates |
| Latest AMI | region, the nodegroup's own version, AMI type | AMI ID |
| Latest add-on version | region, add-on name, cluster version | the version, or "none published" |

"None published" is an answer, so it is kept. `REWRITE.md` puts the account in the key. A `Fetcher` reads through one provider, which has one set of credentials, so the account is fixed and the key leaves it out. No STS call is needed to learn it.

The latest AMI is keyed by the nodegroup's version, not the cluster's. An AMI update keeps a nodegroup on its minor.

### Target resolution

```go
package cmdutil

type Target struct {
	Cluster string
	Source  TargetSource // Explicit, Env, Context, Kubeconfig
}

// ResolveCluster returns the cluster a command acts on. It makes no AWS
// call. A changing command passes readOnly false and never gets the
// kubeconfig step.
func ResolveCluster(f *Factory, explicit string, readOnly bool) (Target, error)
```

The order is the one in `CONTRACT.md`: the explicit value, `REFRESH_CLUSTER`, the active context, then the current kubeconfig context for read-only commands. The kubeconfig step reads the cluster name from the context's cluster ARN or from the `--cluster-name` argument of its exec plugin. That code goes in `internal/kube`, which phase 3 extends.

When nothing resolves, the error names the three ways and makes no list call:

```text
no cluster specified
Set one with:
  the argument, or -c/--cluster   for this command
  REFRESH_CLUSTER                 for this shell
  refresh context use <name>      until you change it
```

A name that does not exist is found by the first wave: the error of `Fetch` satisfies `awsx.IsNotFound`. Only then does the command call `Fetcher.Clusters`, which is one more round trip on the failure path.

```go
// Suggest returns up to five of candidates that are close to name: those
// that contain it or that it contains, then those within a small edit
// distance. Both tests ignore case.
func Suggest(name string, candidates []string) []string
```

```text
cluster "prd" not found in us-east-1
Did you mean:
  prod
  prod-eu
List the clusters with: refresh cluster list -r us-east-1
```

A missing nodegroup or add-on gets the same treatment with `ListNodegroups` or `ListAddons`. If the list call fails too, the error is the not-found error with no suggestions. Exit code 1.

## 3. Sweeps

```go
package fleet

// Regions is the region list of a sweep.
type Regions struct {
	Names   []string
	Default bool // nobody named them: the built-in list for the partition
}
type SweepOptions struct {
	Need Need
	// Match, when set, selects clusters by region and name before any
	// describe call.
	Match func(region, name string) bool
	// OnRegion, when set, gets the snapshots of each region that answers.
	// Calls do not overlap. It must not block.
	OnRegion func(region string, snaps []*Snapshot)
}
type SweepResult struct {
	Regions   Regions
	Answered  []string
	Skipped   []string         // sorted; a default sweep only
	Snapshots []*Snapshot      // every cluster of every answered region
	Failures  []apidoc.Failure // one of kind Region for each failed region
	Errs      []error          // the error behind each of Failures
}

func (f *Fetcher) Sweep(ctx context.Context, p *awsx.Provider, r Regions, opts SweepOptions) *SweepResult
```

This is a port of `legacy/internal/regionsweep` with these rules kept:

- A region that returns a list is answered, also when it has no clusters and when some of its rows are incomplete.
- A default sweep moves a region to `Skipped` when its `ListClusters` error satisfies `awsx.IsRegionInaccessible`. A named region is never skipped. It fails.
- A region the sweep did not reach before the context ended is a `Region` failure with reason `NotAttempted`.
- `Answered` and `Failures` keep the order of the region list.

Changes from the old code:

| Old | New | Reason |
|---|---|---|
| `status` sweeps `min(4, --max-concurrency)` regions at once; `cluster list` sweeps `--max-concurrency` | Every region starts at once | The limits are for each region, so a region cap protects nothing. |
| `Run` is generic over the region's value | `Sweep` returns snapshots | Every caller wants clusters. Fleet mode in phase 4 calls it with an empty `Need`. |
| Every sweep fails fast on a dial, through a context wrapper | Only a default sweep does, through `provider.SweepClients(region)`. Named regions use `provider.Clients(region)`. | Phase 1 built the policy for the default sweep. A region the user named gets the full retries. |
| A single-region `cluster list` skips the sweep code | `status` and `cluster list` always go through `Sweep` | One path, also for one named region. |
| Clusters in a region: 8 at once, with nested fan-outs | `--max-concurrency` clusters at once, under the request limit | See "The concurrency limit" in section 2. |

Which regions a command sweeps, in `cmdutil.SweepRegions(global, home, allRegions)`:

1. `-r` given: those regions, named.
2. `-A`, or no home region is configured: `REFRESH_EKS_REGIONS` when set, named. Else the list from `fleetdata.Regions` for the partition of the home region, as a default sweep.
3. Else the home region, named.

`-A` with `-r` is a usage error, from `cmdutil.ValidateSweepFlags` in `validate`. The old CLI let one silently win, depending on where the flag stood.

What is silent and what is reported:

| Case | Output | Exit |
|---|---|---|
| A default sweep skips regions | One stderr notice that lists them, and `skippedRegions` in the document | no effect |
| A region fails and another answers | A `Region` failure | 4 |
| No region answers | An error, and no document. Before the error, `cmdutil.SweepError` runs the old `NoRegionAnswered` check: one STS call in an always-on region tells bad credentials from closed regions. | 1 |
| Every region is skipped | The same error path | 1 |

Deadline and cancellation: `run` wraps the context with `cmdutil.WithTimeout(ctx, f.Global.Timeout)`. One budget covers the whole sweep. When it ends, calls in flight return a timeout, calls that wait for their first slot return `ErrNotSent`, clusters that `ForEach` never started become `NotAttempted` rows, every goroutine returns, and `Sweep` returns what it has. `fleet` gets a goleak check. A deadline prints the partial document and exits 4. Ctrl+C prints the same document and exits 1.

## 4. Failures

`apidoc.Failure` is the old `diag.Failure` with the same keys.

```go
package apidoc

type Failure struct {
	Kind         FailureKind `json:"kind"`
	Name         string      `json:"name"`
	Cluster      string      `json:"cluster,omitzero"`
	Region       string      `json:"region,omitzero"`
	Operation    string      `json:"operation,omitzero"` // IAM action, such as eks:DescribeNodegroup
	Reason       Reason      `json:"reason"`
	Retryable    bool        `json:"retryable"`
	Error        string      `json:"error"` // one line for people
	AWSErrorCode string      `json:"awsErrorCode,omitzero"`
	UpdateID     string      `json:"updateId,omitzero"`
}
type FailureKind string // Region, Cluster, Nodegroup, Addon, Insight, Update, PodDisruptionBudget, Node
type Reason string      // the 15 values of legacy/internal/diag/reason.go, unchanged

// Failures is a list that encodes as [] when it is nil.
type Failures []Failure

func NewFailure(kind FailureKind, name string, reason Reason, msg string) Failure
func SortFailures(fs []Failure) // kind, region, cluster, name, operation, reason, update ID, error
```

`apidoc.NewFailure` is the only constructor with that name. The path from an AWS error to a `Failure` has two parts, because `apidoc` and `awsx` may import no refresh package.

```go
package awsx

// ErrorClass is why an AWS call failed, from a closed set.
type ErrorClass string // AccessDenied, CredentialError, Throttled, NotFound, RegionUnavailable,
// InvalidRequest, ServiceError, NetworkError, Timeout, Interrupted, NotAttempted, Unknown

// Classify returns the class of err. It reads the types in the chain of
// err, never its text.
func Classify(err error) ErrorClass

// Operation returns the IAM action of the call that failed, such as
// "eks:DescribeCluster".
func Operation(err error) string

package fleet

// FailureFromError returns the Failure for an AWS error about the item.
func FailureFromError(kind apidoc.FailureKind, name string, err error) apidoc.Failure
```

`awsx.Classify` holds the ordered checks of `diag.Classify`, beside the typed predicates it calls: interrupt, `ErrNotSent`, dial failure, deadline, credentials, permission, region disabled, throttling, not found, validation, server error, other API code, region error, network error. `ErrNotSent` is new and gives `NotAttempted`. The operation comes from `smithy.OperationError`, with a table from SDK service ID to IAM prefix. A test checks that table against `awsx.RequiredPermissions`.

`fleet.FailureFromError` is a few lines: it calls `Classify`, `Operation`, and the existing `ErrorCode` and `Summary`, and fills the document type. It lives in `fleet` because that is the lowest package that imports both `awsx` and `apidoc`. Every `ErrorClass` value is also a `Reason` value, and a test asserts that.

How each case appears:

| Case | In the snapshot | In the document |
|---|---|---|
| One nodegroup could not be described | The nodegroup has `State: Failed` and its name. `Have.Nodegroups` is `Partial`. One `Nodegroup` failure. | The nodegroup is `{"name": "web", "incomplete": true}`. Its cluster row has `"incomplete": true`. The failure is in the top-level `failures`. |
| The nodegroup list call failed | `Have.Nodegroups` is `Failed`, the slice is empty. One `Cluster` failure with operation `eks:ListNodegroups`. | No `nodegroups` key. The cluster row is incomplete. |
| An AMI lookup failed | `AMI.State` is `Failed`, and `AMIStatus` returns not known. One `Nodegroup` failure. | No `ami` key. The nodegroup is incomplete. |
| A cluster could not be described | `Have.Cluster` is `Failed`. `Fetch` returns the error. | The row is `{"name", "region", "incomplete": true}`. |
| A region failed | `SweepResult.Failures` | A `Region` failure. No rows for the region. |
| The deadline passed | Finished rows are complete. Rows in work have groups that are `Failed` or `NotAttempted`. Rows not started have `Have.Cluster: NotAttempted`. Regions not reached are `Region` failures. | The unfinished rows are incomplete, with reason `Timeout` or `NotAttempted`. |
| Ctrl+C | As the deadline | The same document, with reason `Interrupted` or `NotAttempted`. Exit 1. |

A row carries no error text. Its failures are in the top-level list, where `cluster`, or `name` for the cluster itself, identifies the row.

Reporting follows `docs/concepts/output.md`. The table view lists failures under `INCOMPLETE DATA` and writes nothing to stderr. Machine and plain output write one `warning:` line for each failure to stderr. All of it comes from the one sorted list. The exit code is 4 unless a higher rule in section 7 applies.

### Advisory reads

Not every failed call is a `Failure`. The rule: a failed read that hides a fact the command exists to show is a `Failure` and exits 4. A failed read that only loses an extra, and whose fallback is labelled in the output, is not. Without this rule a user with restricted IAM would get exit 4 from every run. The old code already ignored the errors of the four advisory calls below.

| Call | When it fails | In the document | Exit 4 |
|---|---|---|---|
| Any list or describe call for a cluster, nodegroup, or add-on | The row or the item is incomplete | A `Failure` | yes |
| `GetParameter`, the latest AMI | `AMI.State: Failed` | A `Failure`, no `ami` key | yes |
| `DescribeLaunchTemplateVersions`, `DescribeAutoScalingGroups`, `DescribeInstances` for the current AMI | `AMI.State: Failed`. One readable source is enough, as today. | A `Failure`, no `ami` key | yes |
| `DescribeInstances` for `--show-instances` | `InstancesState: Failed` | A `Failure`, no `instances` key | yes |
| `DescribeAddonVersions`, in `status` | `LatestState: Failed` | A `Failure`, no `latest` key | yes |
| `DescribeClusterVersions` | The dates come from the built-in calendar. `Have.Support` is `Collected`. | `support.builtIn: true`. The table marks the cell with `*`. | no |
| `DescribeClusterVersions`, and the built-in calendar does not know the version | `Have.Support: Failed` | A `Failure`, no `support` key | yes |
| The Karpenter probes | `Have.Compute: Failed` | No `compute` key. The table shows `unknown`, not `none`. | no |
| `DescribeVpcs` | `Have.VPC: Failed` | No `vpcCidr` key | no |
| `DescribeImages`, the age of a stale AMI | `CurrentCreatedAt` stays zero | No `currentCreatedAt` key. The table shows the stale count without an age. | no |

An advisory failure sets the `State` of its group, adds nothing to `Snapshot.Failures`, and is logged at the `debug` level with its operation and reason.

Two differences from the old CLI. A failed AMI lookup was advisory in `nodegroup list` (exit 0, an `amiLookupFailure` object on the row) and a failure in `status`. It is a failure in both now, by the rule: the AMI status is what the command shows. And a failed Karpenter probe showed `none`. It shows `unknown` now, because an unlabelled fallback states something the command did not read.

## 5. Documents

Documents are separate types, and `fleet` maps to them in one file:

```go
package fleet

func (s *Snapshot) ClusterDoc() apidoc.Cluster // with nodegroups and add-ons when collected
func NodegroupDoc(n Nodegroup) apidoc.Nodegroup
func AddonDoc(a Addon) apidoc.Addon
```

Reasons for separate types:

1. The output rule "omit a key whose data was not collected" needs an omittable form for nearly every field. That is the wrapping section 1 keeps out of the domain types.
2. The documents are a frozen contract. The domain types must stay free to change in phases 3 to 5.
3. `apidoc` stays a leaf, and the schema generator reads one package.

The cost is three functions and their test.

The encoder in `apidoc` rejects any `null`. The SDK returns nil for an empty list, so the mappers make every list of a collected group non-nil: `subnetIds`, `securityGroupIds`, `publicAccessCidrs`, `logTypes`, `resourceIds`, and the rest. A test encodes a collected and empty snapshot as each of the seven kinds, in JSON and YAML, and expects `[]` and `{}` and no error.

The document types use `omitzero`, which Go 1.24 added. It omits a nil slice or map and keeps an empty one, and it omits a zero `time.Time`. So `nil` means "not collected" and `[]` means "collected, none", with no pointer. A group whose zero values are real data is a pointer to a small struct. YAML goes through JSON, so the tags cover both. `genschema` must treat an `omitzero` key as optional.

```go
package apidoc

type Cluster struct {
	Name            string            `json:"name"`
	Region          string            `json:"region"`
	ARN             string            `json:"arn,omitzero"`
	Status          string            `json:"status,omitzero"`  // AWS value
	Version         string            `json:"version,omitzero"` // "1.33"
	PlatformVersion string            `json:"platformVersion,omitzero"`
	Endpoint        string            `json:"endpoint,omitzero"`
	CreatedAt       time.Time         `json:"createdAt,omitzero"`
	Tags            map[string]string `json:"tags,omitzero"`
	HealthIssues    []Issue           `json:"healthIssues,omitzero"`
	Support         *Support          `json:"support,omitzero"`
	Compute         ComputeType       `json:"compute,omitzero"`
	Network         *Network          `json:"network,omitzero"`
	Security        *Security         `json:"security,omitzero"`
	Nodegroups      []Nodegroup       `json:"nodegroups,omitzero"`
	Addons          []Addon           `json:"addons,omitzero"`
	Incomplete      bool              `json:"incomplete,omitzero"`
}
type Support struct {
	Tier          SupportTier `json:"tier"`
	StandardUntil time.Time   `json:"standardUntil,omitzero"`
	ExtendedUntil time.Time   `json:"extendedUntil,omitzero"`
	DaysRemaining int         `json:"daysRemaining,omitzero"` // at least 1 while a window is open
	AutoUpgrade   bool        `json:"autoUpgrade"`
	BuiltIn       bool        `json:"builtIn"` // dates from the built-in calendar
}
type Network struct {
	VPCID             string   `json:"vpcId"`
	VPCCIDR           string   `json:"vpcCidr,omitzero"`
	SubnetIDs         []string `json:"subnetIds"`
	SecurityGroupIDs  []string `json:"securityGroupIds"`
	PublicAccess      bool     `json:"publicAccess"`
	PrivateAccess     bool     `json:"privateAccess"`
	PublicAccessCIDRs []string `json:"publicAccessCidrs"`
}
type Security struct {
	RoleARN            string   `json:"roleArn"`
	KMSKeyARN          string   `json:"kmsKeyArn,omitzero"` // absent: secrets are not encrypted
	LogTypes           []string `json:"logTypes"`
	DeletionProtection bool     `json:"deletionProtection"`
}
type Issue struct {
	Code        string   `json:"code"`
	Message     string   `json:"message"`
	ResourceIDs []string `json:"resourceIds"`
}
type Nodegroup struct {
	Name           string        `json:"name"`
	Status         string        `json:"status,omitzero"`
	Version        string        `json:"version,omitzero"`
	VersionBehind  *bool         `json:"versionBehind,omitzero"` // set when known
	AMIType        string        `json:"amiType,omitzero"`
	ReleaseVersion string        `json:"releaseVersion,omitzero"`
	CapacityType   string        `json:"capacityType,omitzero"`
	InstanceTypes  []string      `json:"instanceTypes,omitzero"`
	Scaling        *Scaling      `json:"scaling,omitzero"`
	AMI            *NodegroupAMI `json:"ami,omitzero"`
	HealthIssues   []Issue       `json:"healthIssues,omitzero"`
	Instances      []Instance    `json:"instances,omitzero"`
	ReadyNodes     *int          `json:"readyNodes,omitzero"` // phase 3
	CreatedAt      time.Time     `json:"createdAt,omitzero"`
	ModifiedAt     time.Time     `json:"modifiedAt,omitzero"`
	Incomplete     bool          `json:"incomplete,omitzero"`
}
type Scaling struct {
	Desired int32 `json:"desired"`
	Min     int32 `json:"min"`
	Max     int32 `json:"max"`
}
type NodegroupAMI struct {
	Status           AMIStatus `json:"status"`
	Current          string    `json:"current,omitzero"`
	Latest           string    `json:"latest,omitzero"`
	CurrentCreatedAt time.Time `json:"currentCreatedAt,omitzero"` // read for a stale AMI in status
}
type Instance struct {
	ID               string    `json:"id"`
	Type             string    `json:"type"`
	State            string    `json:"state"`
	Lifecycle        string    `json:"lifecycle"` // AWS value: on-demand or spot
	AvailabilityZone string    `json:"availabilityZone"`
	ImageID          string    `json:"imageId"`
	LaunchTime       time.Time `json:"launchTime,omitzero"`
}
type Addon struct {
	Name                  string         `json:"name"`
	Version               string         `json:"version,omitzero"`
	Status                string         `json:"status,omitzero"`
	Health                AddonHealth    `json:"health,omitzero"`
	Latest                *AddonLatest   `json:"latest,omitzero"`
	ARN                   string         `json:"arn,omitzero"`
	ServiceAccountRoleARN string         `json:"serviceAccountRoleArn,omitzero"`
	Configuration         string         `json:"configuration,omitzero"` // as EKS returns it
	Issues                []Issue        `json:"issues,omitzero"`
	CreatedAt             time.Time      `json:"createdAt,omitzero"`
	ModifiedAt            time.Time      `json:"modifiedAt,omitzero"`
	Incomplete            bool           `json:"incomplete,omitzero"`
}
type AddonLatest struct {
	Version string `json:"version,omitzero"` // absent: none is published
	Behind  *bool  `json:"behind,omitzero"` // absent: no version to compare with
}
```

Two pointers to scalars remain, `versionBehind` and `readyNodes`, because `false` and `0` are real answers there and each is a single value. The mappers fill `versionBehind`, `ami`, and `latest` from the methods of section 1, and leave the key out when the method returns not known.

`configuration` is the string that EKS returns. The old code parsed it into a map. EKS accepts YAML and JSON there, and a `null` that a user wrote would fail the encoder, so the document passes the text through.

The seven documents:

```go
type ClusterList struct {
	Clusters       []Cluster `json:"clusters"`
	SkippedRegions []string  `json:"skippedRegions,omitzero"` // a default sweep only
	Failures       Failures  `json:"failures"`
}

// FleetStatus has the three fields of ClusterList, with the same tags.
type FleetStatus struct{ /* Clusters, SkippedRegions, Failures */ }

type ClusterDescription struct {
	Cluster  Cluster  `json:"cluster"`
	Failures Failures `json:"failures"`
}
type NodegroupList struct {
	Cluster    string      `json:"cluster"`
	Region     string      `json:"region"`
	Nodegroups []Nodegroup `json:"nodegroups"`
	Failures   Failures    `json:"failures"`
}
type NodegroupDescription struct {
	Cluster   string    `json:"cluster"`
	Region    string    `json:"region"`
	Nodegroup Nodegroup `json:"nodegroup"`
	Failures  Failures  `json:"failures"`
}

// AddonList and AddonDescription are the two nodegroup documents with
// Addons []Addon `json:"addons"` and Addon Addon `json:"addon"`.
```

Each has a `Kind()` method and a line in `apidoc.Kinds()`. Phase 3 adds `health` to `ClusterDescription`.

Changes from the v1 documents, beyond the shared shapes:

- A describe document nests its resource under `cluster`, `nodegroup`, or `addon`. v1 put the fields at the top level.
- `count` is gone from the lists. `length` in jq gives it.
- The summary objects of `status` (`staleAmi`, `addonsBehind`, `nodegroupCount`) are gone. A row holds its nodegroups and add-ons, each with its own `ami.status` and `latest.behind`. The age of the oldest stale AMI, `staleAmi.oldestDays` in v1, is now `ami.currentCreatedAt` on each stale nodegroup. The `status` table still shows the age in days.
- `configuration` of an add-on is a string, not a map.
- `instanceType`, the first type only, becomes `instanceTypes`, the full list.
- `extraCostUsdPerHour` is gone. The cost note stays in the table view of `status`.

## 6. Views

`view` imports `apidoc` and nothing else from the project. It holds no package-level state. fatih and pterm are not used.

### Theme

```go
package view

type ColorDepth int // NoColor, Color256, TrueColor

// Theme says how to draw on one stream. It is a value with no state.
type Theme struct {
	Depth   ColorDepth
	Unicode bool
}
type Status int // Neutral, Healthy, Warn, Fail, Progress, Unknown

func (t Theme) Glyph(s Status) string
func (t Theme) Token(s Status, label string) string // glyph, a space, the label
func (t Theme) Heading(title string) string
func (t Theme) Strong(s string) string
func (t Theme) Dim(s string) string
func (t Theme) Caution(s string) string

// StatusOf maps an AWS status string, such as ACTIVE or UPDATE_FAILED, to
// a Status. Values of refresh's own enums are mapped with a switch on the
// type, not through this function.
func StatusOf(awsStatus string) Status
```

The palette, the glyphs, their ASCII forms, the 256-color downgrade, and the status table are ports of `legacy/internal/render` (`theme.go`, `tokens.go`) and `ui.ClassifyStatus`. The palette is not exported. A caller names what a text is (a heading, dim text, a caution) and never a color, so `Paint` and `Bold` of draft 2 are gone.

The decision about color and glyphs is made once for each stream, in `cmdutil`:

```go
// New fields on cmdutil.IOStreams. System() fills them from COLORTERM,
// TERM, and LC_ALL, LC_CTYPE, LANG. A test sets them.
ColorDepth view.ColorDepth
Unicode    bool
TermSize   func() (width, height int) // of the terminal behind Out

func (s *IOStreams) Theme() view.Theme    // NoColor depth unless ColorEnabled()
func (s *IOStreams) ErrTheme() view.Theme // the same, for ErrOut
func (s *IOStreams) Size() (width, height int) // zeros when Out is no terminal
```

Color is always an addition. Every status has a glyph and a label, so output reads the same with `--no-color`.

### Lists

```go
type Column[T any] struct {
	Title    string
	Value    func(row T) string // the text of the cell; "" means empty
	Status   func(row T) Status // optional: the glyph and color in the table
	MinWidth int
	MaxWidth int // 0 means no limit
	Align    Align
	Truncate Truncate // TruncateEnd, TruncateNever
}
type List[T any] struct {
	Title    string // "NODEGROUPS"
	Subtitle string // "prod · 3"
	Columns  []Column[T]
	Rows     []T
	Empty    string           // shown in place of the rows when there are none
	Notes    []string         // lines after the table; table view only
	Failures []apidoc.Failure // the INCOMPLETE DATA section; table view only
}

func (l List[T]) Render(th Theme, width int) []string
func (l List[T]) Plain(w io.Writer) error
```

`REWRITE.md` sketches a column with `Plain(row)` and `Human(row)`. Two text functions can still differ, which is the old bug in a new place. Here `Value` is the only source of text. `Status` can add a glyph and a color and cannot change a word.

- Plain output is the titles joined by tabs, then one line for each row of `Value` results joined by tabs. A tab or newline inside a value becomes a space. An empty value is `-`.
- The table pads and truncates the `Value` text first and paints it after. Width math never sees an escape code. Widths are display cells, counted with `go-runewidth` and `uniseg` as today.
- The columns of a command are fixed. No flag adds or removes one, so a column keeps its position in plain output.
- A table has no column without a title. The old `status` table had one for the row's glyph. The glyph of a row is now the `Status` of the first column: it stands before the cluster name in the table, and plain output has the name alone.
- Truncation happens only when `width` is above zero, which means a terminal. A piped table is never cut. Columns shrink from the widest, down to `MinWidth`, and `TruncateNever` columns do not shrink. A cut cell ends in `…`, or `...` without Unicode.

### Describe views

```go
type Description struct {
	Title    string // the resource name
	Status   Status
	Label    string // the status text beside the title
	Sections []Section
	Failures []apidoc.Failure
}
type Section struct {
	Title  string  // "NETWORK"
	Fields []Field // key and value rows
	Items  []Item  // repeated things, such as one for each nodegroup
}
type Field struct {
	Name, Value string
	Status      Status // optional
}
type Item struct {
	Name   string
	Fields []Field
}

func (d Description) Render(th Theme, width int) []string
func (d Description) Plain(w io.Writer) error
```

A describe command writes one function from its document to a `Description`. The table view draws `Fields` as aligned pairs and `Items` as a small table whose columns are the field names. Plain output is `FIELD` and `VALUE` rows from the same data:

```text
FIELD	VALUE
overview.status	ACTIVE
overview.version	1.33
network.vpc	vpc-0abc
nodegroups.web.status	ACTIVE
nodegroups.web.desired	3
```

The field name is the section title, then the item name when there is one, then the field name, in lower case and joined by dots. A space in a title or a field name becomes `_`. A section with no fields and no items is left out of both forms.

Both `List` and `Description` satisfy the interface that the print helper needs:

```go
package cmdutil

type HumanView interface {
	Render(th view.Theme, width int) []string
	Plain(w io.Writer) error
}

// Print writes doc as JSON or YAML, or v as a table or as plain text.
func Print(ios *IOStreams, format Format, doc apidoc.Document, v HumanView) error
```

### Live output

`view.Live` redraws a block of lines in place. It has a constructor that takes the writer and a size function, and two methods, `Draw(lines []string) bool` and `Clear()`. `Draw` reports whether it drew. It is a port of `render.LiveRegion`, with one rule added: it redraws in place only when the block fits the terminal height. Section 8 says what a sweep draws when it does not fit.

### What phase 2 does not build

A tree view. Spinners. Boxes, callouts, and progress bars, which arrive with phase 4. `-o wide`, custom columns, and `--no-headers`. A pager. Text wrapping inside a cell. A light-terminal palette. User themes. The elapsed-time line of the old tables, which made output differ between runs.

## 7. Commands

Each command lives in its group package (`internal/cmd/cluster`, `nodegroup`, `addon`, `status`) and follows `internal/cmd/contextcmd`: `newCmdXxx(f *cmdutil.Factory)`, an options struct, then `complete`, `validate`, `run`. Every `list` has the alias `ls`. The old `get` alias of `describe` is not kept.

New flag helpers in `cmdutil`, each adding one flag the same way everywhere:

```go
func AddClusterFlag(cmd *cobra.Command, p *string)    // -c/--cluster, with name completion
func AddAllRegionsFlag(cmd *cobra.Command, p *bool)   // -A/--all-regions
func AddFilterFlag(cmd *cobra.Command, p *[]string, keys ...string)
func AddSortFlags(cmd *cobra.Command, key *string, desc *bool, keys ...string)
func AddWatchFlags(cmd *cobra.Command, watch *bool, interval *time.Duration)
func AddKubeFlags(cmd *cobra.Command, kubeconfig, kubeContext *string)
```

### The seven commands

| Command | `Use` | Args | Flags beyond the global set | `Need` |
|---|---|---|---|---|
| `status` | `status` | none | `-A`, `-f`, `--sort`, `--desc`, `-o` | `Nodegroups`, `AMI`, `AMIAge`, `Addons`, `LatestAddons`, `Support`, `Compute` |
| `cluster list` | `list` | none | `-A`, `-f`, `--sort`, `--desc`, `-w`, `--watch-interval`, `-o` | none |
| `cluster describe` | `describe [cluster]` | at most 1 | `--kubeconfig`, `--kube-context`, `-o` | `Nodegroups`, `Addons`, `Support`, `VPC` |
| `nodegroup list` | `list` | none | `-c`, `-f`, `--sort`, `--desc`, `-w`, `--watch-interval`, `--kubeconfig`, `--kube-context`, `-o` | `Nodegroups`, `AMI` |
| `nodegroup describe` | `describe <nodegroup>` | exactly 1 | `-c`, `--show-instances`, `--kubeconfig`, `--kube-context`, `-o` | `OnlyNodegroups`, `AMI`, and `Instances` with the flag |
| `addon list` | `list` | none | `-c`, `-w`, `--watch-interval`, `-o` | `Addons` |
| `addon describe` | `describe <addon>` | exactly 1 | `-c`, `-o` | `OnlyAddons` |

Every `-o` accepts `table`, `json`, `yaml`, and `plain`. Positionals and `-c` complete from live names with `ValidArgsFunction`.

Flags from `CONTRACT.md` that wait for phase 3, because they need the health checks or a kube client: `cluster list --show-health`, `cluster describe --check-readiness`, `nodegroup list --check-readiness`, `nodegroup describe --show-workloads`. Phase 2 does not register them. `--kubeconfig` and `--kube-context` are registered now, because target resolution reads the kubeconfig.

### Options and steps

A sweep command and a single-cluster command differ only in how they find their clusters. Two structs show the pattern. The other five follow one of them.

```go
// internal/cmd/cluster/list.go
type listOptions struct {
	io     *cmdutil.IOStreams
	aws    func(context.Context) (*awsx.Provider, error)
	global *cmdutil.GlobalOptions

	allRegions    bool
	rawFilters    []string
	sortKey       string
	desc          bool
	watch         bool
	watchInterval time.Duration
	output        cmdutil.Format

	filters cmdutil.Filters // set by complete
}

// internal/cmd/nodegroup/describe.go
type describeOptions struct {
	io     *cmdutil.IOStreams
	aws    func(context.Context) (*awsx.Provider, error)
	global *cmdutil.GlobalOptions
	target func(explicit string, readOnly bool) (cmdutil.Target, error)

	cluster       string
	nodegroup     string
	showInstances bool
	kubeconfig    string
	kubeContext   string
	output        cmdutil.Format

	resolved cmdutil.Target // set by complete
}
```

| Step | Sweep command (`status`, `cluster list`) | Single-cluster command (the other five) |
|---|---|---|
| `complete` | Parse the filters. | Take the positional. Resolve the cluster with `cmdutil.ResolveCluster`. No AWS call. |
| `validate` | Reject `-A` with `-r`. Check the watch rules. | Reject more than one `-r`. Check the watch rules. |
| `run` 1 | `cmdutil.WithTimeout`. Get the provider. `cmdutil.SweepRegions`. | `cmdutil.WithTimeout`. Get the provider. Fail when it has no region. |
| `run` 2 | `Fetcher.Sweep`, with `Match` from the `name` and `region` filters. | `Fetcher.Fetch` with `provider.Clients("")`. On a not-found error, return the suggestion error. Any other error goes through `awsx.FormatAWSError`. |
| `run` 3 | Apply the other filters. Sort. | Apply the filters and sort, for a list. A named item with `State: Failed` and reason `NotFound` returns the suggestion error. |
| `run` 4 | Map to the document. Sort the failures. `cmdutil.Print`. | The same. |
| `run` 5 | Report failures. Return the exit error. | The same. |

The sort key and the output format are checked when cobra parses the flag, as `AddOutputFlag` does today. Nothing before `run` touches the network.

### Filters

```go
package cmdutil

type Filter struct{ Key, Pattern string }
type Filters []Filter

func ParseFilters(raw []string, keys ...string) (Filters, error)

// Match reports whether every filter matches. get returns the values of a
// key; one matching value is enough.
func (fs Filters) Match(get func(key string) []string) bool

// Only returns the filters with one of keys. A sweep command uses it to
// apply the name and region filters before the describe calls.
func (fs Filters) Only(keys ...string) Filters
```

Parse rules:

1. Split at the first `=`. A missing `=`, an empty key, or an empty value is an error.
2. The key is matched without regard to case against the keys of the command. An unknown key is an error that lists the keys.
3. `*` matches any run of characters, also an empty one. No other character is special. The matcher splits the pattern at `*` and does not use `path.Match`, which gives `[`, `?`, and `\` a meaning.
4. A pattern without `*` must equal the whole value. The old `name` filter matched a substring. Write `name=*prod*` for that.
5. Values compare without regard to case.
6. Every filter must match. A key may repeat, as in `-f name=web-* -f name=*-spot`.
7. A key with several values matches when one of them matches. `instanceType=m5.*` selects a nodegroup with the types `m5.large` and `c5.large`. The `instanceType` sort key uses the first type.

| Command | Filter keys | Sort keys, default first |
|---|---|---|
| `status` | `name`, `region`, `version`, `support` | `name`, `region`, `version`, `support`, `stale` |
| `cluster list` | `name`, `region`, `status`, `version` | `name`, `region`, `status`, `version` |
| `nodegroup list` | `name`, `status`, `instanceType`, `amiStatus`, `version` | `name`, `status`, `instanceType`, `nodes`, `version`, `amiStatus` |
| `addon list` | none | by name |

A sort is stable, and ties go to name and then region. `version` sorts by `Version.Compare`. The old code compared strings, which puts 1.9 after 1.10. `support` sorts by severity: `Standard`, `Unknown`, `Extended`, `Unsupported`. `stale` is the count of stale AMIs, nodegroups behind the control plane, and add-ons behind. The old `status` sort key `cluster` is now `name`.

### Exit codes

| Result | Code |
|---|---|
| Complete, nothing to report | 0 |
| `status` only: a stale AMI, a nodegroup behind the control plane, an add-on behind, or a control-plane health issue | 2 |
| `status` only: a cluster in extended support or unsupported | 3 |
| Any failure in the document | 4 |
| An error before a result, or an interrupt | 1 |

The highest code wins, as `CONTRACT.md` says, with no exception for `status`. Incomplete data (4) outranks an unsupported cluster (3). The old order was 3, then 2, then 4.

### Watch mode

```go
// Watch calls draw now and then again after each interval, until ctx ends.
// It clears the screen before each call.
func Watch(ctx context.Context, ios *IOStreams, interval time.Duration, draw func(context.Context) error) error
```

- `-w` needs `-o table` and a terminal on stdout. Anything else fails in `validate`. One document, or one TSV table, per run is the rule for the other formats.
- `--watch-interval` defaults to 10s and must be at least 1s.
- Each refresh uses a new `Fetcher`, so a new AMI or add-on version shows up, and gets its own `--timeout` budget. The provider and its connections are reused.
- A refresh with a partial result draws what it has, with the `INCOMPLETE DATA` section, and the watch goes on. The next refresh may be complete.
- A refresh that returns an error, such as expired credentials, ends the watch with that error.
- Ctrl+C ends the watch with exit 0. It is the normal way to stop one, and `CONTRACT.md` names it as the one exception to "an interrupt exits 1".
- The screen is cleared and redrawn whole, as `watch(1)` does. The old code did the same.

## 8. Performance

### Targets

Wall time for a read command is close to `latency × max(waves, calls in the busiest region ÷ limit)`. The design sets the first term. The second term depends on the limit.

| Command | Waves, target | Old, measured at 50 ms mean | Model, limit 8 | Model, limit 32 |
|---|---|---|---|---|
| `cluster list`, one region, 25 clusters | 2 | 677 ms | 250 ms | 100 ms |
| `cluster describe` | 2 | 394 ms | 100 ms | 100 ms |
| `nodegroup list` | 4 | 315 ms | 200 ms | 200 ms |
| `addon list` | 2 | 224 ms | 100 ms | 100 ms |
| `status -A`, 200 clusters, 8 regions | 6 | 1,990 ms | about 1,700 ms | about 450 ms |

The model columns are arithmetic, not measurements. The `status` row assumes 2 nodegroups and 4 add-ons for each cluster, which is about 275 calls for a region.

The `status` row matters. A request limit of 8 does not beat the old binary, because the old binary's real peak for a region was far above 8. The waves are no longer the bound there. The volume is. That is the reason for the default of 32, and the model for 16, the fallback, is about 900 ms.

The cluster-level limit does not change the model: it equals the request limit, so the slots stay full. The `instance-id` filter and the chunks change no wave count.

### Assertions

Each command has one test that asserts three things against `awsxtest`.

1. **The exact calls.** The request log, grouped by operation, equals a fixed table. An extra call fails the test.
2. **The waves.** A small helper on the handler's `OnRequest` hook holds every request. `Wave` waits until the named requests are all held, fails on any other held request, and releases them. A call that the command makes in series never arrives while its wave is held. `Wave` has its own deadline, 5 s by default, and fails with the list of the requests it holds and the ones it still expects. So a serial call fails fast with a message that names it, not at the `--timeout` of the run. No sleep is involved.

```go
gate := awsxtest.NewGate(t, srv.Handler)
go run()
gate.Wave("eks:DescribeCluster", "eks:ListNodegroups")
gate.Wave("eks:DescribeNodegroup", "eks:DescribeNodegroup")
gate.Wave("ssm:GetParameter", "autoscaling:DescribeAutoScalingGroups")
gate.Wave("ec2:DescribeInstances")
gate.Done() // nothing else arrived
```

3. **The limit.** With `--max-concurrency 4` and 30 clusters, the hook holds requests until 4 are in flight. `PeakConcurrency("", region)` must equal 4, and the held requests must belong to at most 4 clusters. For a sweep, the hook holds until every region has a request in flight, which proves that regions run together.

A memo test asserts that 20 clusters on one version make one `DescribeAddonVersions` call for each add-on and one `GetParameter` call for each AMI type.

### Output as regions answer

- On a terminal with `-o table`, `Sweep` calls `OnRegion`, and the command redraws the whole table through `view.Live`, sorted in the final order. Rows are inserted in place, never appended out of order.
- When the table is taller than the terminal, the command draws one status line on stderr, such as `5 of 16 regions, 61 clusters`, and prints the table once at the end.
- Machine and plain output print once, at the end.
- A single-cluster command prints once. With at most four round trips it needs no progress display.

### Benchmarks

| Benchmark | Gate |
|---|---|
| `List.Render` with 200 rows | `testing.AllocsPerRun` on every PR |
| The three document mappers over 200 snapshots | `AllocsPerRun` on every PR |
| `ParseVersion`, `CompareAddonVersions`, `Filters.Match` | nightly, benchstat |
| `status -A` against `awsxtest` with no latency, 200 clusters in 8 regions | nightly. Its first reading sets the "local overhead" target that `REWRITE.md` leaves to phase 2. |
| `status -A` and `cluster list -A` with 0 to 100 ms of latency | nightly, against the old binary's numbers in `REWRITE.md` |

## 9. Test plan

**Table tests for pure functions.** `ParseVersion` and `Compare`, with a fuzz target. `amiStatus`, one row for each outcome. `addonHealth`, one row for each EKS status. `supportFor`, including the day boundary and a version older than the calendar. `CompareAddonVersions`, with the two old fuzz targets. `ParseFilters` and `Match`, including each parse error. Each sort key. `Suggest`. `fleetdata`: the AMI parameter path for every AMI type, and a contiguous calendar. `awsx.Classify`, one row for each class. `VersionBehind`, `AMIStatus`, and `Behind`, one row for each pair of states. The mappers, one row for each `State` of each group, and the encode test over a collected and empty snapshot for all seven kinds.

**View tests.** `List.Plain` and `List.Render` from one fixture, with a test that the plain cells equal the table cells once the glyph and the padding are removed. That is the no-drift test. Width tests: wide runes, combining marks, truncation at each mode, a width of zero. Plain output passes a port of `legacy/internal/ui/plaintest`: a header row, tabs, no empty cell, no escape code.

**Whole-command tests.** `cmdutiltest.NewFactory` with `UseAWS(f, srv.Config(region))`, run through the root command. Each asserts stdout, stderr, the exit code, and the request counts. Cases for each command: complete, empty, one item fails with each of `AccessDenied` and `Throttled`, the list call fails, not found with suggestions, not found when the suggestion list fails, deadline, interrupt. Extra cases for sweeps: a default sweep skips a closed region, a named closed region fails, one region fails and another is empty, no region answers with good credentials and with bad ones, every region skipped. Watch: two refreshes driven by the world's clock, a partial refresh between two complete ones, Ctrl+C with exit 0.

**Batch and limit tests.**

- An instance of one nodegroup is gone between the group call and the instance call. That nodegroup's AMI status is `Unknown` with no failure, every other nodegroup keeps its status, and the exit code is 0.
- A cluster with 120 nodegroups makes three `DescribeAutoScalingGroups` calls in one wave and two filtered `DescribeInstances` calls.
- A stale AMI that is deregistered: the row has no `currentCreatedAt` and no failure.
- Each advisory read of section 4 fails with `AccessDenied`: the document has the labelled fallback, no failure, and exit 0.
- A deadline in the middle of a sweep of 100 clusters with the limit 4: the first rows are complete, at most 4 are partial, and the rest are `NotAttempted`.
- The middleware: a throttled first attempt whose retry gets no slot before the context ends reports `Throttled`, not `NotAttempted`. A slot is free during backoff. `Clients`, `SweepClients`, and `PollClients` of one region share the 4 slots.
- **Throttle at the limit.** The fake throttles a share of the calls whenever 32 are in flight in a region, for 200 clusters in 8 regions. `status -A` must complete with no failure and exit as it does without throttling. This test and the `task test:live` reading decide between 32 and 16.

**Goldens.** For each command, one fixed world and four files: table at 100 columns without color, plain, JSON, YAML. One more table golden at 60 columns for truncation. JSON and YAML goldens validate against `api/schema/v2`, which step 2 of the build order starts to generate.

**Carried over.** These old tests name a past regression. Each is ported or covered by a named case above.

| Old test file | What it protects |
|---|---|
| `services/status/support_test.go`, `support_fuzz_test.go` | A partial day rounds up; a cancelled lookup is not shared; `1.-5` is rejected |
| `services/status/incomplete_test.go` | A cancelled sweep keeps its rows as not attempted; a late cancel is not partial; an add-on with no compatible version; an AMI lookup failure marks the row |
| `services/status/sweep_test.go`, `paging_test.go`, `retry_test.go` | Lookups are shared across clusters and do not outlive a sweep; a failed lookup is not kept; paging; the Karpenter probe is scoped to its cluster |
| `services/status/service_test.go` | A nodegroup behind the control plane; control-plane health issues |
| `services/cluster/filter_test.go`, `commands/cluster/filter_test.go` | Status and version filters apply |
| `services/cluster/service_test.go` | The regressions named in the file: partial describes, node counts that are not invented, failures in the result |
| `services/nodegroup/readiness_test.go`, `commands/nodegroup/list_render_test.go`, `commands/clusterview/detail_render_test.go` | A ready count is never invented. Phase 2 ports the rule "no `readyNodes` key unless measured". |
| `services/nodegroup/service_test.go`, `list_failures_test.go`, `list_options_test.go` | AMI status for updating and custom nodegroups; a filtered row is not a failure; a cancelled list |
| `aws/ami_test.go`, `ami_cache_test.go` | Every AMI type's path; unknown types are not guessed; a failed lookup is an error; the Auto Scaling fallback; the key is the nodegroup's version |
| `services/addons/version_order_test.go`, `version_fuzz_test.go` | A release beats a prerelease; the order is total |
| `regionsweep/regionsweep_test.go` | Default sweeps skip, named regions never skip, a cancelled sweep marks regions |
| `commands/statuscmd/*_test.go` | Exit codes: interrupt is 1, deadline is 4, a failed region with an empty region is 4, cancel does not block |
| `commands/cluster/list_regions_test.go`, `exitcodes_test.go`, `describe_test.go`, `machine_output_test.go` | A hanging endpoint fails; exit codes; failures are in the document |
| `commands/nodegroup/failure_fields_test.go` | The failure fields |
| `commands/clusterview/clusterview_test.go`, `ui/ansi_test.go`, `ui/plain_test.go` | Width of wide runes, malformed escapes, plain cells |
| `commands/runner/runner_test.go`, `exitcodes_test.go`, `deadline_test.go` | The regressions named in the files: target resolution, the exit code table, the deadline |
| `diag/reason_test.go` | The classification table matches the docs |

`legacy/PORTING.md` records the result for each file.

## 10. Build order

Each step ends with `task dev:full` green and is one reviewable change.

1. **`cluster list` in one named region, table and JSON.** `fleet.Version`, `State`, `Snapshot`, `Cluster`, `Fetcher.Clusters`, `Fetch` with an empty `Need`, and `Fetcher.Sweep` called with one named region, with the cluster-level limit. `apidoc.Cluster`, `ClusterList`, `Failure`. `awsx.Classify`, `fleet.FailureFromError`. `view.Theme`, `List`. `cmdutil.Print`. The `awsx` request limit and `ErrNotSent`, with the middleware tests. The `awsxtest` gate. This is the smallest end-to-end slice, and it already runs through the sweep code.
2. **Plain and YAML, filters, sort, schemas.** `List.Plain`, the no-drift test, `cmdutil.Filters`, the sort flags. `tools/genschema` and `api/schema/v2`, which do not exist yet, for the kinds built so far: `ClusterList` and `Version`. The goldens for all four formats, validated against those schemas. Each later step adds the schemas of its kinds.
3. **Many regions.** Step 1's `Sweep` gets the default sweep with `SweepClients`, `fleetdata.Regions`, `cmdutil.SweepRegions` and `SweepError`, `-A`, skipped regions, `view.Live`, and output as regions answer. It extends the sweep and does not rewrite it.
4. **Target resolution and `addon list`, `addon describe`.** `cmdutil.ResolveCluster`, `internal/kube` for the kubeconfig step, `Suggest`, the not-found error, `Need.Addons`, `Need.OnlyAddons`, `view.Description`.
5. **`nodegroup list`, `nodegroup describe`.** `Need.Nodegroups`, `AMI`, `Instances`, the AMI paths in `fleetdata`, the latest-AMI memo, the batched calls with their chunks, and the `instance-id` and `image-id` filters in `awsxtest`.
6. **`cluster describe`.** `Need.Support`, `VPC`, the calendar in `fleetdata`, and its memo.
7. **`status`.** `Need.LatestAddons`, `Compute`, and `AMIAge`, the counters, the advisory reads, the exit rule, and the throttle test.
8. **Watch mode** on the three list commands.
9. **Close-out.** `gen-docs`, the benchmarks and gates, the livecheck tests for the read commands with the two readings this note asks for (the filter value limit and `status -A` at 32), the `PORTING.md` rows.

## 11. Decisions

The owner decided these on draft 1. Nothing in this note is open.

| # | Question | Decision |
|---|---|---|
| 1 | Does `cluster list` keep its `NODES` column? | No. It cost 2 + N calls for each cluster and was the reason for the old 677 ms. |
| 2 | What does `--max-concurrency` limit, and what is its default? | Requests in flight for one region, default 32. The default holds only if the throttle test and the `task test:live` reading pass. Else it is 16. |
| 3 | Is a failed AMI lookup a failure in `nodegroup list`? | Yes, exit 4. The advisory table in section 4 lists the reads that are not failures. |
| 4 | Exit code order for `status`? | The highest code wins, with no exception. |
| 5 | Should `addon list` and `addon describe` show the latest compatible version? | Not in phase 2. `REWRITE.md` rule 1 allows no new feature. The `latest` key exists for `status`, so it is a small change after cutover. |
| 6 | Should the AMI check compare release versions in place of AMI IDs? | Not in phase 2. `DecideAMIUpdate` is frozen behaviour and uses the IDs. Decide in phase 4 with `task test:live` data. Windows publishes no release version. |
| 7 | A disk cache for slow data? | None. The calendar is one call in wave 1, and the memos cover a run. |

## 12. Contract edits

The owner accepted these, and `CONTRACT.md` has them.

1. **`--max-concurrency`** limits AWS requests in flight in one region. The default is 32, with the condition of decision 2.
2. **`addon list --show-health`** is removed. Health comes from the status, so the column is always shown.
3. **`-A` with `-r`** is a usage error.
4. **`-w`** needs `-o table` and a terminal, and `--timeout` applies to each refresh.
5. **Exit codes.** The highest code wins for `status` too.
6. **`status --sort`.** The key `cluster` is now `name`.
7. **Document field lists.** Section 5 is the field list of the seven read kinds, without `count` and without the `status` summary objects.
8. **Stopping `--watch` with Ctrl+C exits 0.** It is the normal way to end a watch, and the one exception to "an interrupt exits 1".

Two points in `REWRITE.md` change in this note and need no contract edit: the column shape in "Views (view)", and `fleet.Fetch` as a method of `Fetcher` that returns a snapshot and an error.

## 13. Changes made while building steps 1 to 3

The code is the reference where it and an earlier draft differ. These are the differences, each applied above.

| Section | Draft 2 | Built |
|---|---|---|
| 2 | `Need` has eight fields | `Need` is an empty struct until the step that needs a field adds it. `Fetch` already takes it, so later steps add fields and change no caller. |
| 2 | The cluster-level limit is `--max-concurrency` | The sweep reads it from `Provider.MaxInFlight()`, so the two numbers cannot drift. |
| 2 | `ErrNotSent` wraps the cause of the context | It wraps `ctx.Err()`. `context.Cause` returns nil for the deadline of `cmdutil.WithTimeout`, which is not a standard library context. |
| 4 | `Classify` returns the class, the code, and the operation | `Classify` returns the class. `Operation` is its own function, and the code was already `ErrorCode`. |
| 4 | The limiter returns the earlier error of a retry that got no slot | It does, inside a wrapper that `IsRetryable` refuses. Without it the retryer would wait for the next backoff and report the cancelled context. |
| 4 | Failure kinds | The constants are `apidoc.FailureRegion`, `FailureCluster`, and so on. `apidoc` also gets `Documents()`, `Kind.Command()`, and the `Enum` interface, which `genschema` walks. |
| 5 | `apidoc.Cluster` has `support`, `compute`, `nodegroups`, `addons`, and `network.vpcCidr` | Those keys arrive with steps 4 to 7. The cluster has `upgradePolicy` now, the EKS support type, which draft 2 left out. |
| 6 | `Theme.Paint` and `Theme.Bold` with exported colors | Methods that name the role of a text. The palette is private. |
| 6 | `TruncateMiddle` | Not built. No column of steps 1 to 3 wants it. |
| 6 | `List` | It has an `Empty` line for a list with no rows. `Live.Draw` returns whether it drew, and the command falls back to a status line when it did not. |
| 7 | `Filters.Match` takes a function that returns one value | The function returns a list, which is rule 7 of the filters. `Filters.Only` splits the filters that apply before the describe calls. |
| 7 | A cell of a row that was not read | It shows `unknown`, in the table and in plain output. |
| 7 | `cluster list --show-health` | Not registered. It needs the health checks of phase 3. `-w` waits for step 8. |
| 8 | `Wave` in `awsxtest` | Built as `awsxtest.Gate`, in its own file, with `Timeout` as a field. |
| 9 | Goldens at 100 and 60 columns through the command | A terminal run also redraws the table as regions answer, so its output holds cursor codes. The two width goldens render the view from the JSON golden, and a terminal test checks that the final table of a run equals the 100-column golden. |
| 10 | Step 2 writes a reference page for the schemas | `tools/genschema` writes the schemas only. The docs pages are phase 6. |

Measured on the fake fleet of 200 clusters in 8 regions, 8 runs after one warm-up, old binary and new binary in the same run:

| Command | Old, no latency | New, no latency | Old, 0 to 100 ms | New, 0 to 100 ms |
|---|---|---|---|---|
| `cluster list -A -o json` | 37 ms | 18 ms | 787 ms | 218 ms |
| `cluster list -r us-east-1 -o json` | 19 ms | 10 ms | 704 ms | 151 ms |

The model of section 8 gave 100 ms for one region at the limit 32. The measured 151 ms is two waves whose slowest call is near 100 ms each: the model used the mean latency, and a wave ends with its slowest call.

## 14. Changes made while building steps 4 and 5

| Section | Before | Built |
|---|---|---|
| 2 | A retry that gets no slot returns the error of the attempt before it | It returns the error of the context. Only a first attempt that got no slot is `ErrNotSent`. The wrapper type and its rule in `IsRetryable` are gone: with the real backoff a deadline lands in the backoff sleep and reports the context error anyway. |
| 2 | `Fetch` starts its calls whatever the context | `Fetch` returns a `NotAttempted` row at once when the context has ended. The SDK returns the bare context error for a call that it did not send, and that would read as a timeout of a call that never left. |
| 2 | The latest AMI falls back to the version of the cluster | A nodegroup whose own version is not known gets no lookup, and its status is `Unknown`. EKS always reports the version, and the fallback would need the cluster before the nodegroups. |
| 2 | The read of the instances fails every nodegroup with a group | It fails the nodegroups that had an instance to read. A group with no instance has nothing in that call. |
| 2 | Target resolution names the three ways | The kubeconfig step is `kube.CurrentCluster`: the cluster ARN, then `--cluster-name` of the exec plugin, then an eksctl name or a plain name. It does not guess from the server URL, as the old code did: the first label of an EKS endpoint is not the cluster name. |
| 2 | `Suggest` uses containment and edit distance | It also compares the start of a longer name, so `prd` finds `prod-eu`. |
| 3 | The sweep policy is in `cmdutil` | It is in `internal/cmd/fleetcmd`: the region choice, the skipped-region notice, `SweepError`, `RegionsClosedError`, and `ClusterError`. `cmdutil` imports neither `fleet` nor an AWS service package, and a test holds that. |
| 3 | `-r` is repeatable | It is a slice flag: repeats and commas both work, and an empty element is an error. |
| 4 | `Classify` tests the interrupt before `ErrNotSent` | `ErrNotSent` first. A request that never left is `NotAttempted`, also after Ctrl+C. |
| 4 | Every enum of a document is closed in its schema | `reason` is an open string with the known values as examples. A consumer must accept a reason that a later release adds. |
| 4 | A credential error of a sweep gets the headline "AWS credential validation failed" | `FormatAWSError` writes the headline. A second one before it said the same thing twice. |
| 5 | `nodegroup describe` and `addon describe` read one item or fail | `nodegroup describe` can be partial: the nodegroup is read and its AMI or its instances are not. It prints the nodegroup, names the failure, and exits 4. |
| 6 | `Status` has `Neutral` as its zero value | The zero value is `NoStatus`, which draws no glyph. A column or a field then marks only the rows that need a mark, such as a version behind the control plane. |
| 7 | Filters keep the pattern as typed | Spaces around the pattern do not count, and a pattern of spaces is an error. |
| 9 | Whole-command tests build their own fake | `internal/cmd/cmdtest` runs a command against a fake AWS, and `cmdutiltest` has `Golden`, `ValidateJSON`, and `Deadline`. `Deadline` is a context that the test expires in a handshake, so no deadline test waits for a clock. |
| 9 | The limit test uses `cluster list` | A cluster of `cluster list` is one call, so the cluster bound and the request limit cannot be told apart there. A sweep whose clusters make five calls each shows that the limit counts requests. |

Open check: the built-in region list has no `ap-northeast-3`, as the old list. `TestLiveRegions` (`-tags livecheck`) compares the list with EC2 and EKS. The list stays as it is until that test has run against a real account.

Measured, same method as section 13, with the 0 to 100 ms latency:

| Command | Old | New |
|---|---|---|
| `cluster list -A` | 757 ms | 210 ms |
| `cluster list`, one region | 726 ms | 168 ms |
| `addon list` | 237 ms | 136 ms |
| `addon describe` | 164 ms | 96 ms |
| `nodegroup list` | 273 ms | 165 ms |
| `nodegroup describe` | 255 ms | 138 ms |

The nodegroups of the fake fleet have no Auto Scaling group, so `nodegroup list` is three waves there and not four.

## 15. Changes made while building steps 6 to 9

| Section | Before | Built |
|---|---|---|
| 2 | `DescribeAddonVersions` runs in wave 3 | It runs next to the `DescribeAddon` calls, as soon as the cluster is described: the add-on branch waits for the cluster version through a function that `Fetch` gives it. |
| 2 | `DescribeVpcs` uses a filter | It names the one VPC with `VpcIds`. A list of one ID has no other ID that can fail the call. |
| 2 | The Karpenter probes run in wave 3 | They run after the nodegroup list is known to be empty, which is wave 2 for such a cluster. |
| 4 | A failed Karpenter probe is advisory | Built so. The compute cell shows `unknown`. A failed calendar is advisory only when the built-in calendar has the version. |
| 5 | `Support` has `daysRemaining` from the clock | The clock is `Factory.Now`, which the fetcher and the `status` view share, so the days and the AMI age are fixed in tests. |
| 5 | A describe reads one item or fails | A describe can be partial and exit 4. `CONTRACT.md` says so now. |
| 7 | `--kubeconfig` and `--kube-context` are flags of some commands | They are global flags of the root, and `cmdutil.ResolveCluster` reads them from the factory. |
| 7 | A cell of `status` shows a count | `STALE AMI` and `ADDONS` show `unknown`, or `N unknown` beside the count, for what could not be read. A `0` there would say that nothing is stale. |
| 7 | Watch returns nil on Ctrl+C | It returns `cmdutil.ErrStopped`, and `main` exits 0 for it. The pause between two reads is `Factory.After`, so a test drives the reads with a handshake. A watch shows no rows before a read is done. |
| 8 | The exact-calls test compares the request log | For a large sweep it counts distinct calls. With some hundred connections to one local server, the HTTP transport sends a request again now and then after the server closed an idle connection, and the log has it twice. |
| 9 | Completion | `fleetcmd.CompleteClusters`, `CompleteNodegroups`, `CompleteAddons`, and the completion of `-c`. Each has a 3 s budget, offers nothing on any failure, writes nothing, and does not read the kubeconfig for a cluster. |

Measured, same method as section 13, in milliseconds. `status -A` makes 2,256 calls for the 200 clusters of the test fleet (two nodegroups and four add-ons each), with 32 requests in flight in each region and 256 in all.

| Command | Old, no latency | New, no latency | Old, 0 to 100 ms | New, 0 to 100 ms |
|---|---|---|---|---|
| `cluster list -A` | 37 | 19 | 794 | 220 |
| `cluster list`, one region | 19 | 11 | 704 | 172 |
| `cluster describe` | 17 | 9 | 444 | 161 |
| `nodegroup list` | 15 | 8 | 304 | 205 |
| `nodegroup describe` | 16 | 9 | 249 | 106 |
| `addon list` | 16 | 8 | 225 | 156 |
| `addon describe` | 15 | 8 | 188 | 100 |
| `status -A` | 66 | 77 to 112 | 1,924 | 651 |

Two findings about `status -A` that are open:

- **Memory.** Its peak is about 125 MB at the limit 32 and about 63 MB at 8. The old binary is at about 75 MB. The memory follows the requests in flight.
- **No latency.** Alone, a run takes 77 to 112 ms. Right after nine runs of the old binary against the same local server, runs took 1 to 4 s, and one had failures. The cause is not confirmed. The likely one is that 256 dials at once to one local listener fail and are retried with backoff. A real sweep has one endpoint for each region.

The default of 32 under throttling, on the fake fleet with 0 to 100 ms of latency, six runs each:

| Share of EKS calls throttled | Limit 8 | Limit 16 | Limit 32 |
|---|---|---|---|
| none | 2,029 ms | 1,102 ms | 672 ms |
| 3 percent | 2,271 ms | 1,286 ms | 938 ms |
| 10 percent | 2,672 ms, one run with one failure | 1,896 ms | 1,256 ms |

No run at 32 had a failure, and 32 stayed the fastest. The fake throttles a fixed share of calls whatever the load, so this does not show how EKS answers a burst. The `task test:live` reading is still needed before the default is final.

## 16. Memory and connections of a large sweep

A profile of `status -A` on 200 clusters showed where the 125 MB went: about 45 MB were goroutine stacks, for about 3,300 goroutines, and about 45 MB were heap. The request limit kept 256 requests in flight, and every other goroutine waited: one for each nodegroup, add-on, and lookup of every cluster in work. A goroutine that has been through the SDK has a stack of about 15 KB.

| Section | Before | Built |
|---|---|---|
| 2 | A fan-out over the items of a cluster starts a goroutine for each item | It starts a goroutine when the region has a ticket: `Fetcher.tickets` has as many as the request limit. The goroutines of the items follow the requests in flight. |
| 2 | The cluster-level limit is the request limit | It is the request limit for a sweep that reads the clusters alone, and half of it for a sweep that reads more. Such a cluster has two or more calls to make at any time, so the slots stay full. |
| 2 | Each cluster starts a goroutine that waits for the support calendar | One read for a region, started by the first cluster. The others wait for it at the end of `Fetch`, with no goroutine. |
| 2 | The newest add-on versions run in a fan-out of their own | They are tasks of the fan-out that describes the add-ons. |
| 2 | A call waits for its slot below the SDK retry step | The first attempt waits at the first step of the call. A retry waits at the attempt, as before. This did not lower the memory by itself: a goroutine that waited at a small stack grows it when its call runs. |
| 2 | The idle connections of all hosts are limited to four times the limit for one host | No total limit. Each region is a host of its own, and 128 idle connections were fewer than 8 regions times 32. |

The slow runs at no latency were the benchmark, not the product. Evidence: the failures of those runs were all `dial tcp 127.0.0.1: connect: can't assign requested address`, which is the loopback out of ephemeral ports. A counting proxy between the binaries and the fake shows that one `status -A` opens 548 connections with the new binary and 710 with the old one at no latency, and 273 against 411 with latency. macOS has 16,384 ephemeral ports and keeps a closed one for 30 s, so about 25 runs in 30 s use them up. The new binary opens more than 256 because the fake is one host for all 8 regions: 256 requests in flight share a pool of 32 idle connections. Against AWS each region is its own host, with at most 32 requests in flight and 32 idle connections kept, and the SDK's transport asks for HTTP/2, which needs one connection where the endpoint offers it. The product did what it should with a dial that fails: it retried, and then it named the read as a failure and exited 4.
