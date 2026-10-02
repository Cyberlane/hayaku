# Qualification and regression challenges

Native affected-test plans authorize original full suites. No finite trace, mutation score or configuration declaration authorizes native omission. The separate [WASI capsule contract](capsules.md) permits whole-suite pass reuse because guest capabilities and inputs are enforced, not because native observations happened to pass.

`internal/qualification.Compare` validates observations; its `valid` field means comparable observations. `internal/metrics.Compare` similarly validates cost comparability. Neither utility grants execution authority or native qualification.

## Independent observations

Record exact candidate source/context identity, independently collected expected inventory, complete terminal outcomes and measured duration for proposal and full runs. Build/startup failures need explicit scope IDs because individual cases may never be discovered. Use separate disposable state and preserve versions, flags, resources, services and OS/architecture context.

Reject duplicate/missing identities, unsupported schemas, nonterminal/partial runs and mismatched contexts. An omitted full-run failure is an observed miss. Different outcomes for shared IDs are unresolved mismatches; investigate retries, ordering, shared state and flakiness with clean reruns. Passing omitted tests are observations, not proof that they cannot fail.

Strict imported report reconciliation requires independent expected identities and successful original process completion. A report cannot prove its own inventory is complete. JUnit/TRX/Swift/libtest dialect support is not a native framework adapter or authority to prune tests.

## Adversarial boundaries

| Boundary | Regression challenges | Interpretation |
| --- | --- | --- |
| Native selection | Hidden dynamic dependency, old/new influence, new/deleted tests, setup/hooks, aliases and conflicting/missing results | Proposals broaden or fail; original full gates remain |
| Input capture | New/deleted/missing inputs, byte/mode/link drift, linked targets, escapes/cycles, collisions, size/depth limits and cancellation | Incomplete capture/materialization never succeeds |
| WASI capabilities | Forbidden imports/host calls, outside/missing file access, attempted writes, entropy/clocks, memory/table/output bounds and cancellation | Unsupported effects trap; no reusable pass |
| Receipt authority | Forged/foreign/tampered/duplicate metadata, changed backend/producer/input context, unsafe cache paths and unavailable cache | Invalid/unavailable cache executes again |
| Audit | Previous receipt invalidated before run, failed/interrupted audit and output-digest mismatch | Earlier pass is not retained as authority |
| Measurement | Different source/context/inventory, upstream cache mismatch, nonpassing/incomplete outcomes, overlapping intervals and negative savings | Invalid comparison has no claimed benefit; negative cost remains visible |

Enforcement establishes the narrow guest contract; tests challenge implementation mistakes within it. Finite fixture success does not prove every native language/runtime or eliminate the trusted interpreter/compiler/user authority. Original symlink semantics and native Go equivalence are not part of normalized capsule qualification. Windows reuse remains disabled pending ACL qualification.

## Hand-authored fault corpus

`testdata/faults/manifest.json` describes five portable Go challenges over `testdata/faults/go`. Each mutation is applied to a fresh copy; baseline and mutated candidates run independently with `go test -json -count=1 ./...`. Exact replacement tokens prevent silently applying a mutation to the wrong source. Cases cover literals, downstream consumers, embedded resource changes/deletion, initialization and new tests. Expected failures are authored before execution. These are seeded challenges, not historical incidents or a complete mutation set. Stryker is not a Go mutation runner.

## Performance and future narrowing

[Measurements](measurements.md) count capture, identity validation, planning, compilation, evidence and startup costs. Match source/input/runner/environment/platform/suite identities and upstream cache state. Keep full shadow cost separate from reuse savings; a Turbo hit is not a Hayaku gain. Record cold/warm conditions and negative savings. The self-pilot measures a separate WASI graph suite and does not establish representative consumer savings.

Native case-level narrowing still requires a sound adapter-specific algorithm accounting for collection, initialization, fixtures, dispatch, order, parameterization, resources, subprocesses and services, plus enforced input/isolation boundaries. Unknown influence rounds out to the containing suite. Historical coverage can add candidates but cannot prune the required envelope. Native context acceptance, rollback/full-audit policy and consumer rollout remain explicit future work.
