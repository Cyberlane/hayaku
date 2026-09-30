# Qualification and regression challenges

Hayaku currently authorizes full suites and emits affected-test proposals for
shadow evaluation. No finite observation, mutation score or configuration field
changes that authorization. `internal/qualification` validates observations;
its `valid` field means that two observations can be compared.

## Independent observations

Record schema, exact candidate source identity, execution-context digest, an
independently collected expected test inventory, complete terminal outcomes and
elapsed duration for both proposal and full runs. Build/startup failures need
explicit scope IDs because individual tests may never be discovered. Run each
side in separate disposable state. Preserve framework versions, flags, resource
inputs, service contracts and OS/architecture matrix context. A single context
does not qualify another context.

`Compare` rejects missing or duplicate inventory/outcome IDs, unsupported schema,
nonterminal states, partial runs and mismatched source/context identities. An
omitted full-run failure is an observed miss. Different outcomes for shared IDs
are unresolved mismatches: investigate order effects, retries, shared state and
flakiness with clean reruns. They are never silently removed from metrics. A
passing omitted test is one observation, not evidence that it can never fail.

## Hand-authored fault corpus

`testdata/faults/manifest.json` describes five portable Go challenges over the
dependency-free fixture module in `testdata/faults/go`. Each mutation is applied
to a fresh copy; run the baseline and mutated candidate independently with
`go test -json -count=1 ./...`. Exact replacement tokens avoid silently applying
a mutation to the wrong source. The corpus covers literal changes, downstream
consumers, embedded resource changes/deletion, initialization and new tests.
Expected failures are hand-authored before executing the fault. These are seeded
regression challenges, not claimed historical incidents or a complete mutation
operator set. Stryker is not a Go mutation runner.

## Measuring net performance

Use comparable cold and warmed native-cache baselines. `Measure` includes
snapshot copying, discovery, planning, evidence storage, startup and execution.
Report absolute durations and net savings, including negative savings. Report
the additional full-suite shadow cost separately; it cannot be advertised as a
production speedup. Archive dataset/context versions and measurement methodology
before setting a pilot threshold. Define rollback and full-audit policy before
enabling any future qualified selector. No pilot threshold or measured speedup
is established by these local fixtures.

## Finer test-case selection

The current implementation does not authorize narrowing at test-case granularity.
Future qualification requires an adapter-specific sound algorithm accounting for
collection/setup, static initialization, fixtures, dispatch, overrides, order,
parameterized cases, runtime resources, subprocesses and services. Unknown
influence rounds out to the established containing suite. Historical coverage can
add candidate tests but cannot prune the required envelope. Independently review
the enforced input/isolation boundary, adversarial fixtures and shadow misses
before any production omission. Native platform qualification and public rollout
remain explicit work; these comparison utilities cannot grant either.
