# Parity suite

The suite runs the old binary and the new binary on the same fake fleet and
holds the new one to the old one on safety. It does not compare output: the
two CLIs print different documents and messages by design.

`REWRITE.md` ("Parity suite") says why the suite exists. This page says what
it compares and how.

## What a trace is

The runner answers one request at a time. It gives each request two numbers
in one sequence: one when the request arrives, and one when its answer is
ready. A read counts as known to the binary at a change only if its answer
was ready before the change arrived. Arrival order alone does not show that:
a binary can send a read and a change at the same time.

An answer becomes facts: a subject, an attribute, and a value.

| Request | Facts |
|---|---|
| `DescribeCluster`, `DescribeNodegroup`, `DescribeAddon` | `cluster prod`, `nodegroup prod/ng-a`, `addon prod/vpc-cni`: version, release, status, size, issues |
| `DescribeUpdate` | `update prod/ng-a 3`: status |
| `ListInsights`, `DescribeInsight`, `DescribeInsightsRefresh` | `insight prod/in-1`: status. `insights prod`: refresh |
| Kubernetes lists | `node prod/a-1`: condition. `pod prod/default/web-1`: phase. `pdb prod/default/web`: allowed. `deployment prod/default/web`: replicas. `pods prod`: pending, for a pod list over the whole cluster |
| Any of the above that fails | the subject, or the collection (`pods prod`): `read=error=<code>` |

A read that works replaces an earlier error of the same subject.

These reads carry no facts, so the suite does not hold a binary to them: lists
of names, SSM parameters, EC2, Auto Scaling, CloudWatch, Service Quotas,
watches, and the polls of the live node view. `operations` in a trace still
lists every operation the binary called.

## The rule

Evidence is the last value a binary saw of each fact in one part of its run.
How often it read, in what order, and through which call does not count.

For each mutating request, the window is: the answers that came after the
previous mutating request arrived, and before this one arrived. The evidence
of the change has three kinds.

- `target`: the resource the request changes, and its cluster.
- `prerequisite`: an update, or a resource the run changed before. It shows
  that the earlier change settled.
- `gate`: every other fact of the cluster, Kubernetes included.

Two things move the window.

- A prompt that the user answered splits it. Evidence from before the answer
  is marked `before the prompt`. Evidence from after it is not.
- A binary that decides on several targets and then starts them one by one
  reads the second target before the first change, and not again. That is
  correct. So target evidence from before the previous change is kept, marked
  `carried`, unless the run changed that resource or the user answered a
  prompt since.

After the last mutating request:

- `settlement`: the changed resources and their updates.
- `verification`: every other fact that the binary read after it first saw
  the last update in a final status. A run that ends with `--no-wait`, at its
  deadline, or by a signal never sees that status, so it has no verification.

A run with no mutating request has `decision` evidence: every fact, over the
whole run. It covers a dry run, a refusal, and a run that attaches to an
update someone else started.

## The comparison

Each piece of evidence in the old trace is an obligation. The new trace meets
it when its evidence for the same part of the run has the same fact with the
same value. A `before the prompt` obligation needs evidence from before the
prompt. A `carried` obligation is met by carried or fresh evidence. A fresh
obligation needs fresh evidence.

The baseline rule: the verification baseline (the whole-cluster Pending-pod list before a roll) is owed only when a mutation follows it in the new trace, so a run that both binaries refuse before any mutation owes none, and still owes every gate read.

What the new binary read in addition is not a difference. An obligation that
is not met reads like this:

```
target obligation not met: before eks:UpdateNodegroupVersion prod/ng-a (change 1),
the old binary had seen nodegroup prod/ng-a status=ACTIVE, and the new binary did
not read it there
```

Evidence is compared change by change while the two runs make the same
change. Where they part, the mutations differ, and that difference stands for
the rest.

These are compared for equality: the mutating requests (operation, target,
payload, token identity, outcome, order), whether the run refused before any
mutation, the final state of the fleet, and the exit code through
`ExitCodes`.

## Where the new CLI differs on purpose

`CONTRACT.md` lists the old behaviour that the new CLI does not copy. For such
a scenario the old trace is not the reference.

- `testdata/allowlist.json` has one entry for each field that differs. An
  entry names the row of the contract, and `expect` says what the new trace
  must show in that field. An entry with no `expect` does not load. An entry
  that no difference uses is an error.
- `Scenario.V2Require` lists facts that the new binary must have seen.
- `Scenario.V2Checks` are assertions on the new trace alone.

## Moves and holds

A scenario changes the world at a point that both binaries reach: before or
after a named mutating request, when an update gets a status, or after a
prompt. It never counts reads. `Scenario.Holds` keeps a request unanswered
until another request arrived or was answered.

## Commands

```bash
go test ./test/parity/                                  # legacy traces against the goldens
go test ./test/parity/ -run TestLegacyTraces -update    # record the goldens; each scenario runs 5 times
REFRESH_PARITY_V2=/path/to/refresh go test ./test/parity/ -run TestPaired
REFRESH_PARITY_LEGACY=/path/to/legacy go test ./test/parity/   # skip the build
REFRESH_PARITY_RACE=1 go test ./test/parity/            # build the old binary with the race detector
```

## Limits

- A fault that delays or hangs a request would stop the run, because the
  runner answers one request at a time. No scenario uses one.
- The suite does not check that a binary read a list, an AMI parameter, or a
  metric. A scenario asserts that with `sawRead`.
