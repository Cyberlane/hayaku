# Shared analysis, runner integration and group execution

This describes unreleased local implementation. It does not grant omission from ordinary native
plans or establish consumer CI acceptance.

Hayaku shares graph selection and effect handling across languages, and process
completion and normalized outcome validation across runners. Language semantics,
configured module resolution, collection, setup, filters and wire formats remain
integration responsibilities. A parser cannot replace those semantics.

## Integration boundaries

`internal/adapter/runner.go` composes analysis functions and runner bridges in one
closed registration table. A bridge prepares commands, binds its installed entry
point, invokes the provisioned runner and translates results into
`model.Outcomes`. The execution coordinator handles cwd confinement, source/tool
revalidation, prerequisites, cancellation and errors once. Existing Go, Vitest
and native result fields remain for report compatibility; new execution and
shadow comparisons use normalized outcomes.

`internal/runner.Validate` requires the expected terminal unit inventory, unique
test identities within each unit, valid ownership, process completion and
consistent exit status. Complete failures remain failures; unhandled errors,
missing/extra/duplicate units and incomplete results cannot become passes.
Bridges still validate their native protocol before normalization.

`model.Effect` describes additional influence: file reads, directory membership,
existence/metadata probes, or unbounded operations. `internal/influence` implements
matching without language-specific branches. Old and new effects enter the same
graph as old and new source dependencies. Unbounded effects broaden the containing
workspace and its dependents. Unknown effects, owners and invalid paths reject;
bounded analysis overflow stops planning instead of dropping evidence.

The Go frontend supplements compiler metadata with AST observations of recognizable
resource calls, imported effectful APIs, captured references and goroutines. This
is conservative supplementary evidence, not complete Go effect analysis.
Resource paths include possible calling test directories, since a library's
working directory can differ from its source directory.
Type aliasing, interfaces, reflection, native code and library effects remain native
qualification gaps. Configured Vite transforms remain the authority for Vitest
module resolution. Other existing collection integrations retain their declared
coarse dependency fallback; registration cannot claim native skipping.

## Actual independent group reuse

`hayaku suite` executes an explicitly configured inventory of separate WASI
groups. Each group uses the existing enforced capsule contract and private
authenticated receipt authority. It can be produced by any installed toolchain
that emits a compatible WASI Preview 1 command; Hayaku does not install compilers,
convert arbitrary native runners or assert equivalence with native tests.

```json
{
  "schema": 1,
  "groups": [
    {
      "schema": 1,
      "name": "billing",
      "module": "billing.wasm",
      "inputs": ["fixtures/billing.json"],
      "args": ["billing.wasm"],
      "producer_identity": "REPLACE_WITH_VERIFIED_PRODUCER_SHA256"
    },
    {
      "schema": 1,
      "name": "formatting",
      "module": "formatting.wasm",
      "inputs": [],
      "args": ["formatting.wasm"],
      "producer_identity": "REPLACE_WITH_VERIFIED_PRODUCER_SHA256"
    }
  ]
}
```

The producer identity is a verified compilation/source-context digest, as in the
[capsule contract](capsules.md). The example placeholders are intentionally invalid.
Each group also supports the single capsule's environment, seed, limits and
generated/linked input envelope fields.

```sh
hayaku suite --config suites.json --cache-dir .git/hayaku-passes
hayaku suite --config suites.json --cache-dir .git/hayaku-passes --no-reuse
```

The first invocation executes groups without matching receipts. An unchanged
invocation reuses matching complete passes. Changing a group's module, complete
input envelope, producer, arguments, environment, seed or limits executes that
group again. A shared input must be present in every consumer's envelope. There
is no graph-based pruning of a guest's visible files. Guest absence, directory
membership, clocks and entropy remain part of the enforced deterministic contract.

All configuration and input shapes are captured/validated before group execution.
Modules are additionally validated by the actual backend. Names are unique, the
inventory is bounded to 256 groups and captured module/input bytes to 512 MiB.
Single-group limits also apply. The batch visits other groups after a test or
operational failure unless cancelled. Cancellation, failed/unqualified groups or
live configuration/module/input drift prevent a successful aggregate result.
Reports give executed/reused counts, group digests and total coordinator duration
(including capture and final input revalidation for the CLI);
they omit guest output and raw environment/input values. A duration does not by
itself establish net CI savings; capture, compilation and upstream cache costs
still need context-matched measurement.

`--no-reuse` audits each visited group under the existing capsule invalidation
rules. An interrupted batch is incomplete; it does not claim an audit of unvisited
groups. Invalid/unavailable receipts trigger execution. Windows receipt reuse
remains disabled pending ACL qualification.

## Shared scenario coverage

- Core scenarios run with Go, Vitest, Jest, Node test, unittest, pytest, Cargo and
  Swift adapter identities. They cover transitive resource influence, directory
  additions, missing probes, parent deletion, renames, removed effects and
  unbounded operations while retaining full native commands.
- Every registered JSON/stream decoder (Go, Vitest, Node test, unittest, pytest,
  Jest and Playwright) runs a shared normalized-outcome contract for pass, failure,
  skip and truncation. This is protocol coverage, not native runtime acceptance.
- A real-runner scenario harness uses separately authored Go, JavaScript,
  TypeScript/Vitest and Python/unittest/pytest programs. Baselines pass; independent
  faults cover direct code, shared setup, resources, resource deletion and new
  failing tests. Required original scopes and complete failing outcomes remain
  visible. Explicit runtime variables enable installed fixtures; unavailable
  fixtures skip visibly rather than implying acceptance.
- Group/CLI adversaries cover warm reuse, individual invalidation, malformed
  inventory, forged receipts, failed groups, denied imports, cancellation, audits,
  live input drift and rejection of native qualification declarations.

The existing graph oracle, monotonicity fuzzing, fault corpus and capsule capability
adversaries remain required. Finite fixture success does not establish arbitrary
language/runtime soundness. General native omission still needs an independently reviewed
algorithm, enforced native input/isolation boundaries and runner/context acceptance.

The unreleased [restricted native groups](native-groups.md) command adds a
framework-independent Linux enforcement/receipt engine. Its Go boundary owns
source qualification, compilation, inventory and native result decoding. The
platform worker has no runner-specific logic. The private authenticated receipt
store is common to this backend and WASI, with separate domains. This adds actual
native reuse for the supported pure Go subset; it leaves ordinary native plan
full gates and other language qualification unchanged.
