# Qualified execution contexts

Working K01 contract, 2026-10-06. This defines implementation and review gates,
not additional current capabilities. [Implementation status](implementation-status.md)
and [safety](safety.md) describe what actually runs today.

## Initial completion targets

The first milestone must support useful dependency-separated Go packages and
ordinary TypeScript/Vitest files/projects. It is not satisfied by renaming the
existing pure-Go subset, running everything through a generic command, or reporting
an affected proposal while still claiming that proposal was executed selectively.

| Context | Required target | Safe execution group |
| --- | --- | --- |
| Go on a qualified Linux backend | Offline compiler-resolved local imported packages, same-package and external tests, setup/initialization, embedded or explicitly captured fixture inputs, changes to shared consumers, exact build/test context | Full package test command by default; smaller groups require independent lifecycle/state proof. |
| TS/Vitest on a qualified Linux backend | Supported installed Vitest/Vite/Node combinations, configured transformation/resolution, aliases, shared setup, imported helpers/assets, multiple projects, complete independent file/case collection | Entire configured project/file lifecycle as the runner requires; merge when setup/state/order can influence several files. |

Exact versions must be tied to actual fixture acceptance, starting from the
existing [Vitest matrix](vitest.md). Compiler/runtime bytes, flags, architecture,
runner configuration and backend identities are bound, not inferred from version
strings. A new framework, pool, operating system or device is a new context.

The target includes supported bounded resource reads and dynamic paths whose
entire influence envelope is established. Arbitrary dynamic dispatch, reflection,
plugins and external state are not assumed safe. They can be supported only by a
reviewed complete boundary; otherwise their containing original scope runs fully.
Unsupported effects remain visible even when no test has executed the branch.

## Shared evidence model

Every context supplies versioned evidence with:

- Original full scope, exact source/context/tool identities and normalized runner
  inventory, including startup/build/collection scopes that can fail before cases.
- Stable unit/case ownership and execution groups containing every affected
  lifecycle/state dependency. Duplicate, missing or empty inventories are errors.
- Complete dependency/input closure, including configuration, initialization,
  setup, producer outputs, absence, directory membership and cross-workspace inputs.
- Explicit gaps and effect policies, with adapter/backend policy identity. Static
  graphs and diagnostic observations add influence; absence of an observation
  does not prove absence of influence.
- Enforced boundary identity and reviewed context algorithm. Serialized evidence
  is data: importing it or setting a `qualified` field cannot create authority.

Common code validates identities, graph closure, groups, complete terminal outcomes,
reuse binding, audits and fallback. Language adapters own semantic resolution;
runner adapters own collection, filters and protocol decoding; backends own actual
resource enforcement. An adapter registration does not inherit another's proof.

## Conditions for omitting execution

All conditions must hold for a group:

1. Independent complete discovery and build/startup context match the required
   original scope. Collection itself stays inside a qualified boundary or is
   treated as unqualified; arbitrary collection code is not passive metadata.
2. The reviewed language/runner algorithm establishes its whole input/effect
   envelope, including code that was not covered by tests, or the backend enforces
   an independently reviewed complete envelope without changing promised semantics.
3. The actual backend enforces every observable input contract. Read-only files,
   no network and process isolation alone do not establish deterministic clocks,
   entropy, metadata, scheduling, services, JIT/runtime or native-extension behavior.
4. The group is unchanged within that full envelope and has an authenticated,
   exact-context, complete all-cases-passed receipt from the trusted authority.
   Runner skips are outcomes but never all-cases-passed receipt authority.
5. Live sources, configuration, tools and platform still match. Drift, incomplete
   results or failed cleanup cannot be hidden by another group's successful reuse.

First invocation and unavailable/invalid caches execute tests. Affected groups
execute even if a different group passed. New tests cannot be omitted; deleted
tests retain old influence but are not executed in the candidate inventory.

Changed effects, missing inputs or unsupported qualification broaden to the
smallest independently justified original containing scope, ultimately the full
suite. Actual assertion/build failures remain failures. Cancellation and live
drift stop successful aggregation; they are not fallback successes.

## Resource and state policy

| Observable capability | Required treatment |
| --- | --- |
| Files, links, directories, missing probes, environment | Capture complete identities and enforce access, absence, membership and declared values. Native link semantics need their own proof. |
| Metadata, clocks, entropy | Enforce documented semantics that preserve the promised context, or reject outcome-observable access. Native behavior cannot be silently replaced by WASI semantics. |
| Writes and scratch state | Isolate and reset complete writable state per group, bind relevant initial state, and account for later consumers. Unsupported transient writes remain unqualified. |
| Ordering, concurrency and shared initialization | Preserve original semantics and qualify complete shared-state influence; otherwise merge groups or run the original scope. |
| Subprocesses, FFI and runtime extensions | Bind complete child/runtime/extension identities and enforce their effects recursively, or leave the context unqualified. |
| Services, databases, worker bindings, browsers, simulators | Require explicit enforced service/state/reset contracts and actual context acceptance. A host unit-test proof does not cover them. |

The compiler, runner/runtime internals, controller, enforcing kernel/daemon and
invoking trusted authority are explicit trusted components. Program-visible effects
inside trusted code still require the same input semantics; trusting a runtime
implementation does not make user calls to its clock deterministic.

## Acceptance and rollout

Each supported context needs independently authored inventories and full-command
fixtures covering cold/warm reuse, changed direct/shared inputs, new/deleted tests,
startup/hook failures, unexecuted effects, denied-capability bypass, tampering,
resource limits, canceled audits and live drift. Use actual installed versions
and platforms. Absent fixtures and cross-compilation are not runtime acceptance.

Review the algorithm and trust assumptions separately from successful comparisons.
Run `go test ./...`, `go test -race ./...`, `go vet ./...` and advisory Mori review
for meaningful core changes. Capture/build/validation/audit costs belong in the
end-to-end performance result; upstream cache savings are separately attributed.

Do not replace ordinary full CI gates until the applicable qualification and
integration cards have completed review and actual acceptance. External rollout,
publishing and production acceptance are separately authorized and recorded.
No CLI force flag, imported report, coverage threshold or configuration declaration
can waive these conditions.
