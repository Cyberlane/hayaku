# Affected-test execution kanban

Updated: 2026-10-06. This is the working backlog for end-to-end affected-test
execution across supported languages and runners. It is a plan, not qualification
evidence or authorization to publish, deploy, or omit existing CI gates.

Goal: discover the complete suite, execute affected groups, reuse authenticated
unchanged passing groups, and execute broader original scopes whenever evidence
is incomplete. Exact minimal selection for arbitrary programs is not promised.
No historical coverage threshold, successful trace, configuration assertion or
finite test score can qualify skipping by itself. See [safety](safety.md).

Current implementation milestone: macOS and Linux, as explicitly requested on
2026-10-06. Windows support is deferred; its qualification and acceptance cards
are outside the current milestone and cannot block its handoff. The Windows
cards remain unqualified future work. K40 is the initial local Go/TS handoff; K41
tracks the larger future backlog and its separately promised context gates.

## Board rules

- States: `READY -> DOING -> REVIEW -> DONE`; `BLOCKED` means a listed dependency
  is unfinished. A ready card has every dependency in `DONE`.
- Card state records unfinished work; it does not imply a live agent. The
  activity table below records verified live assignments separately.
- `DONE` means the card's acceptance evidence exists, not that a feature is
  published or accepted in a consumer's production CI.
- Dependencies are hard gates for completing implementation. Read-only design
  and inspection can proceed before they complete. Fixture edits need a separately
  claimed card/file scope and must not assume unfinished upstream interfaces.
- Record a claimed card, agent, file scope and evidence below before editing.
  One agent owns a card; one writer owns each file. The coordinator owns this
  board, shared registration, final integration and capability documentation.
- Use up to three worker agents plus the coordinator. Prefer independent cards;
  use isolated worktrees for overlapping changes, otherwise assign disjoint files.
  Do not delegate one dependency and its dependent as concurrent implementation.
- Reuse existing graph, influence, process, outcome, input and receipt machinery.
  Language adapters supply language semantics, runners supply collection and
  execution semantics, backends supply enforced runtime boundaries. Add no
  framework branches to the common selection engine.
- Every implementation card includes relevant tests and fallback behavior. The
  shared harness supplements language-specific cases, not replaces them.
- Meaningful code changes require `go test ./...`, `go test -race ./...`,
  `go vet ./...` and advisory Mori source/test review. Actual runner/backend checks
  must run for the configuration being qualified; absent fixtures are not passes.
- Move `REVIEW` to `DONE` only after coordinator review of the implementation,
  tests, safety boundaries and recorded evidence. Test success alone is not a
  soundness argument. Keep unsupported effects explicitly unqualified.
- Agents report assumptions, evidence, risks and remaining gaps. Only the
  coordinator updates cross-lane dependencies and shared capability claims.
- Preserve unrelated changes; install/fetch no tools automatically. External
  consumer changes, pushes, releases, publishing and deployments require their
  own applicable authorization.

## Existing foundations

These are locally implemented foundations to extend, not general native skipping
or production acceptance. Source/tests and [implementation status](implementation-status.md)
remain authoritative if this board drifts.

| ID | State | Existing foundation |
| --- | --- | --- |
| F01 | DONE | Exact snapshots, conservative old/new graph union, bounded processes and identity revalidation. |
| F02 | DONE | Shared analysis/runner composition and normalized terminal outcome validation. |
| F03 | DONE | Explicit generated/linked input capture and bounded verified materialization. |
| F04 | DONE | Shared authenticated receipt store, deterministic WASI reuse and audits. |
| F05 | DONE | Restricted pure-Go Linux native group reuse and original-host fallback. |

## READY

| ID | Lane | Dependencies | Feature | Acceptance criterion |
| --- | --- | --- | --- | --- |

| K23 | RUST | K04, K02 | Cargo/nextest build and test closure | Features, build scripts, proc macros, generated inputs, binaries and case inventory have complete producer/context identities; unsupported build/runtime effects are gaps. |

| K25 | APPLE | K04, K02 | SwiftPM/Xcode build and test closure | Bind resources, generated code, build settings, test plans, lifecycle, case inventories and simulator/device identities; unsupported macros/plugins/modes remain explicit. |

| K29 | OTHER | K04, K02 | Remaining language/runner context contracts | Define exact runner/build/runtime/version scopes for the nine L/R lanes below, with required behavior, exclusions and evidence. Multiple frameworks need explicit subcards before implementation; generic full-command support is not qualified omission. |

## DOING

| ID | Lane | Dependencies | Feature | Acceptance criterion |
| --- | --- | --- | --- | --- |
| K07 | RUNTIME | K04, F03 | Enforced native input envelopes | Bind and enforce files, absent paths, directories, environment, links and write/scratch rules; an outside access cannot leave a qualified pass. Protect capture and live drift boundaries. |

| K15 | TS | K04, K02 | TypeScript/Vitest complete configured graph and inventory | Extend configured Vite transforms for aliases, setup, mocks, dynamic collection/imports, plugins, generated assets and dependency inputs. Unresolved loaders/effects broaden. |

## REVIEW

| ID | Lane | Dependencies | Feature | Acceptance criterion |
| --- | --- | --- | --- | --- |
| K21 | PYTHON | K04, K02 | Python import, collection and fixture analysis | Interpreter/package/plugin identities, dynamic imports, discovery and pytest/unittest fixtures map to common inputs/groups; unknown influence broadens. |
| K03 | QA | F01, F02 | Representative baseline fixtures | Independently authored Go, TS, worker, Python, Rust and Apple workloads capture full-command behavior, discovery/startup failures and cold/warm costs; fixtures do not alter consumer repositories. |
| K05 | CORE | K04 | Complete old/new invalidation | All qualified providers handle transitive changes, removed edges, additions/deletions/renames, configuration changes and new/deleted tests without losing influence. |
| K06 | CORE | K04 | Safe group formation | Collection, initialization, hooks, order and mutable shared state determine test/file/package/suite boundaries; unproved independence merges or broadens groups. |
| K13 | GO | K04, K02 | Go inventory and imported dependency closure | Compiler metadata plus typed semantics account for imported packages, build tags, test variants, initialization and embed inputs; unresolved/native/toolchain effects remain explicit gaps. |
| K42 | CORE | F01 | Unix process-tree cancellation | Timeouts, cancellation and output overflow kill the invoked process group, including compiler/runner descendants; actual descendant probe passes. Escaping children and Windows cleanup stay explicitly unqualified. |

## DONE

| ID | Lane | Dependencies | Feature | Acceptance criterion |
| --- | --- | --- | --- | --- |
| K01 | CORE | F01, F02, F05 | Supported-context and qualification contract | Document initial Go and TS/Vitest contexts, selection granularity, trusted components, forbidden effects and the reviewed conditions that permit reuse. Later contexts cannot inherit qualification merely through registration. |

| K02 | QA | F01, F02, F04, F05 | Shared adversarial acceptance harness | Independent inventories and seeded faults exercise cold/warm/change/failure/fallback/audit behavior through reusable runner-neutral scenarios; incomplete installed fixtures are visible. |

| K04 | CORE | K01 | Shared qualified execution model | Versioned inventory/group/input/effect/proof/context contracts compose existing providers; unknown effects and unsupported versions cannot produce skip authority. |

## BLOCKED

| ID | Lane | Dependencies | Feature | Acceptance criterion |
| --- | --- | --- | --- | --- |
| K08 | RUNTIME | K07 | Metadata, clocks and entropy policy | Each qualified context either enforces defined observable semantics or rejects these effects. Containment and interception are not assumed to establish native equivalence. |
| K09 | RUNTIME | K07, K08 | Subprocess, native extension and service contracts | Controlled child programs and service state require complete identities and enforced boundaries; unsupported FFI, processes, networks and service state retain full execution. |
| K10 | RUNTIME | K07, K08, K02 | Linux backend generalization | Extend the existing worker to supported non-Go artifacts with real file/process/network/metadata bypass probes, cancellation and cleanup; no runner-specific selection logic in the worker. |
| K11 | RUNTIME | K07, K08, K02 | macOS backend | A native macOS context has actual enforcement and receipt authority tests; unsupported host capabilities execute the original full scope. Docker/Linux success is not macOS qualification. |
| K12 | RUNTIME | K07, K08, K02 | Windows backend and cache authority | Qualify actual Windows enforcement and private ACL semantics; reuse stays disabled until the complete context passes review and runtime checks. |
| K14 | GO | K13, K06, K10 | Broader Go qualification | Support documented Go constructs, external test packages, setup and fixture inputs with enforced effect contracts; unsupported interfaces/reflection/cgo/concurrency broaden rather than guess. |
| K16 | TS | K15, K06, K10 | Node runtime qualification for TS/Vitest | Installed Node/module/runner identities and all startup/collection/execution effects fit an enforced context; native addons, JIT/runtime behavior and unsupported loaders cannot bypass qualification. |
| K17 | JS | K16, K02 | Node test affected execution | Account for runner collection, hooks, module loading, concurrency and exact terminal inventory using common qualification/reuse machinery. |
| K18 | JS | K16, K02 | Jest affected execution | Account for configured transforms, resolution, mocks, setup, snapshots and process scopes; reject unsupported plugin behavior and snapshot update modes. |
| K19 | WORKER | K15, K07, K08, K09, K02 | Cloudflare worker runtime qualification | Independently qualify worker execution, bindings, storage, requests, isolation and reset state against a defined worker context; ordinary Node/Vitest qualification is insufficient. |
| K20 | WORKER | K19, K06 | Worker Vitest integration | Collect and execute supported worker-pool groups with complete outcomes and qualified reuse; binding/state changes invalidate consumers and unsupported worker modes fall back. |
| K22 | PYTHON | K21, K06, K10 | Python runtime and pytest/unittest qualification | Actual interpreter, native extension, subprocess and resource effects meet the supported boundary; both runners pass common and native-specific acceptance cases. |
| K24 | RUST | K23, K06, K10 | Rust runtime and Cargo/nextest qualification | Supported actual binaries have enforced input boundaries and reconciled outcomes; filtering and runtime effects preserve full-command semantics or fall back. |
| K26 | APPLE | K25, K06, K11, K09 | Swift/Xcode runtime qualification | Qualify documented host/simulator scopes with reset application/device state; UI/device/external-state tests cannot inherit host-unit qualification. |
| K27 | BROWSER | K04, K06, K09, K10, K02 | Browser/application/service boundary | Bind browser bytes, application build, storage, requests, service fixtures and cleanup/reset semantics; uncontrolled services or device state prevent reuse. |
| K28 | BROWSER | K27 | Playwright affected execution | Actual browser-based workloads reconcile discovery and outcomes and reuse only qualified isolated groups; browser-free reporter tests are insufficient. |
| K30 | CORE | K05, K07, F03 | Monorepo and generated input closure | Cross-language consumers, workspace links and producers invalidate correctly; BASE/candidate generation stays independent, and provenance is enforced rather than declared. |
| K31 | CORE | K04, K06, K07, F04 | Complete native receipt identity and lifecycle | Bind entire input closure, inventory, group, proof, runner/toolchain/backend and configuration; tampering, drift, skips, failed or incomplete execution cannot mint reusable passes. |
| K32 | CORE | K05, K06, K31, K14, K16 | Selective execution coordinator | For initial Go and TS/Vitest contexts, execute changed groups, validate reused groups, and run complete original scopes on qualification gaps; no unrelated existing native gate is silently replaced. |
| K33 | CORE | K32, K02 | Aggregate outcome reconciliation | Every required case/group has an executed terminal outcome or authenticated pass; startup/build/hook errors, unexpected identities, truncation, cancellation and drift cannot produce green. |
| K34 | CI | K31, K02 | Trusted cache persistence across CI jobs | Authenticate portable receipts with an explicit trusted issuer/key lifecycle and context checks; fork/untrusted jobs cannot poison authority. Cache misses/unavailable storage execute again. |
| K35 | CORE | K33, K02 | Full audits, revocation and recovery | Forced-full and scheduled audit paths revoke previous authority before execution, including cancellation/failure; mismatches revoke relevant qualification and retain original full gates. |
| K36 | CLI | K33, K30 | One end-to-end command and explanation | Setup/doctor/plan/run connect qualified paths and original fallbacks; stable reports distinguish executed, reused, affected, broadened and incomplete scopes without exposing source or environment values. |
| K37 | CI | K34, K35, K36 | Reproducible CI workflow | Exact revisions, provisioned tools, matrix context, private cache authority, fallback, artifacts and audits work in disposable CI-equivalent runs. External workflow changes require applicable authorization. |
| K38 | QA | K33, K35, K03 | Independent selection and isolation review | Review the influence algorithm and backend assumptions; run real hidden-dependency, unexecuted-effect, state, bypass, cache and fault cases against original full runs. Passing comparisons alone confer no qualification. |
| K39 | QA | K37, K38 | Compatibility and performance acceptance | Run actual declared versions/platforms and representative consumer workloads; record cold/warm end-to-end costs including capture/build/audits and negative savings. Unsupported modes demonstrably fall back. |
| K40 | COORDINATOR | K39 | Capability documentation and local handoff | Update status/safety/runner/CI docs with exact supported contexts and evidence; initial Go/TS milestone is complete locally, with external acceptance and release status separately recorded. |
| K41 | COORDINATOR | K42, K40, A01, A02, A03, A04, A05, A06, A07, A08, A09, A10 | Whole-backlog local completion | Reconcile every promised context with implemented qualification, actual acceptance evidence, explicit exclusions and accurate docs; no placeholder or untested platform remains claimed as supported. External production acceptance and release remain separate. |

## Remaining language implementation lanes

These are named work cards, all `BLOCKED` and unclaimed. K29 specifies exact
framework scopes before dispatch. Every L-card covers context-specific inventory,
transitive build/code/configuration/resource dependencies, collection/setup and
unknown-effect fallback. Every R-card implements reviewed runtime qualification,
runner translation, complete outcomes and common execution/reuse integration.
Use the shared engine and backend; no duplicate selection/cache implementation.
Split a card before claiming it if its contracted scope needs multiple writers.

| ID | Lane | Dependencies | Feature | Acceptance criterion |
| --- | --- | --- | --- | --- |
| L01 | JVM | K29 | JVM analysis and collection | Account for selected build tools, classpaths, generated classes/resources, initialization, reflection and runner discovery; unresolved effects broaden. |
| R01 | JVM | L01, K06, K09, K10, K02 | JVM runtime and runner qualification | Review VM/native/agent effects and enforce the selected context; exact runner filters and results preserve required semantics or fall back. |
| L02 | DOTNET | K29 | .NET analysis and collection | Account for selected builds, assemblies/resources, generators, startup, reflection and runner discovery; unresolved effects broaden. |
| R02 | DOTNET | L02, K06, K09, K10, K02 | .NET runtime and runner qualification | Review managed/native runtime effects and selected runner semantics; unsupported plugins, processes or services retain full execution. |
| L03 | RUBY | K29 | Ruby analysis and collection | Account for installed gems, dynamic loading, hooks, shared helpers, resources and selected runner discovery; unresolved effects broaden. |
| R03 | RUBY | L03, K06, K09, K10, K02 | Ruby runtime and runner qualification | Qualify actual interpreter/extension effects and fixture isolation; exact supported runner outcomes use common validation and reuse. |
| L04 | PHP | K29 | PHP analysis and collection | Account for Composer/autoload inputs, bootstrap, generated code, fixtures and selected runner discovery; unresolved effects broaden. |
| R04 | PHP | L04, K06, K09, K10, K02 | PHP runtime and runner qualification | Qualify interpreter/extensions and selected runner isolation; uncontrolled service and bootstrap effects retain full execution. |
| L05 | DART | K29 | Dart analysis and collection | Account for packages, generated code, assets, initialization and chosen Dart/Flutter runner scopes; device/widget contexts are explicit separate scopes. |
| R05 | DART | L05, K06, K09, K10, K02 | Dart runtime and runner qualification | Qualify the specified runtime and resource/extension effects; device/Flutter contexts cannot inherit plain Dart host qualification. |
| L06 | CPP | K29 | C/C++ analysis and collection | Account for headers, compiler/linker settings, generated sources, linked libraries, initialization and selected build/runner inventory. |
| R06 | CPP | L06, K06, K09, K10, K02 | C/C++ runtime and runner qualification | Review native/FFI/loading effects with an enforced supported contract; unsafe unaccounted behavior broadens, not a user-declared proof. |
| L07 | BUN | K29 | Bun analysis and collection | Account for configured TS/JS transforms, resolution, packages, setup, assets and native test discovery; Node qualification is not inherited. |
| R07 | BUN | L07, K06, K09, K10, K02 | Bun runtime and runner qualification | Qualify Bun-specific runtime/extension effects and terminal outcomes against actual supported binaries; unsupported modes run in full. |
| L08 | DENO | K29 | Deno analysis and collection | Account for module maps, locked imports, local/remote dependency bytes, assets and test discovery without automatic fetching. |
| R08 | DENO | L08, K06, K09, K10, K02 | Deno runtime and runner qualification | Review permissions and runtime enforcement rather than trusting declarations; actual context, runner outcomes and fallback pass review. |
| L09 | BAZEL | K29 | Bazel build/action/test closure | Account for configured targets, rules/toolchains, external repositories, action inputs and test inventory; build cache hits are not test receipts. |
| R09 | BAZEL | L09, K06, K09, K10, K02 | Bazel test execution qualification | Review actual test-language runtimes and sandbox/resource semantics; qualify named combinations only, with common reconciliation and receipt authority. |

## Context acceptance gates

K40 completes the initial Go and ordinary TS/Vitest milestone only. It is not
completion of all languages. Each additional promised context needs the same
integration, reconciliation, audits, CI and independent acceptance gates:

| ID | Context | Dependencies | Acceptance criterion |
| --- | --- | --- | --- |
| A01 | Node test | K17, K32, K33, K35, K36, K37, K38 | Add runner to common execution; actual supported context passes independent scenarios and cost measurement; document accurate local capability status. |
| A02 | Jest | K18, K32, K33, K35, K36, K37, K38 | Same gate, with configured Jest semantics and actual runner fixtures. |
| A03 | Cloudflare worker Vitest | K20, K30, K32, K33, K35, K36, K37, K38 | Same gate, with actual worker binding/storage/reset and cross-language fixtures. |
| A04 | pytest and unittest | K22, K32, K33, K35, K36, K37, K38 | Same gate, separately accepted for each runner/interpreter context. |
| A05 | Cargo and nextest | K24, K32, K33, K35, K36, K37, K38 | Same gate, separately accepted for each runner/build configuration. |
| A06 | SwiftPM and Xcode XCTest | K26, K32, K33, K35, K36, K37, K38 | Same gate, separately accepted for documented host/simulator contexts. |
| A07 | Playwright | K28, K32, K33, K35, K36, K37, K38 | Same gate, with real browser/application/service workloads. |
| A08 | macOS | K11, K14, K16, K32, K33, K35, K36, K37, K38 | Separate actual macOS acceptance per promised language/runner pair; review OS-specific proof and enforcement, not cross-compilation or Linux inheritance. |
| A09 | Windows | K12, K14, K16, K32, K33, K35, K36, K37, K38 | Separate actual Windows acceptance per promised language/runner pair and cache authority; no macOS completion dependency or cross-compilation-only qualification. |
| A10 | Remaining language aggregate | A11, A12, A13, A14, A15, A16, A17, A18, A19 | Reconcile all nine contracted context families, including any required framework subcards and exclusions; generic command entries cannot satisfy this gate. |
| A11 | JVM | R01, K32, K33, K35, K36, K37, K38 | Actual contracted JVM contexts pass independent fault/isolation/CI/cost acceptance; unsupported frameworks remain explicit. |
| A12 | .NET | R02, K32, K33, K35, K36, K37, K38 | Same gate for each contracted .NET runner/runtime combination. |
| A13 | Ruby | R03, K32, K33, K35, K36, K37, K38 | Same gate for each contracted Ruby runner/interpreter combination. |
| A14 | PHP | R04, K32, K33, K35, K36, K37, K38 | Same gate for each contracted PHP runner/interpreter combination. |
| A15 | Dart | R05, K32, K33, K35, K36, K37, K38 | Same gate for each contracted host/device context; actual device evidence required if promised. |
| A16 | C/C++ | R06, K32, K33, K35, K36, K37, K38 | Same gate for each contracted compiler/build/runner combination. |
| A17 | Bun | R07, K32, K33, K35, K36, K37, K38 | Same gate for actual contracted Bun versions and test modes. |
| A18 | Deno | R08, K32, K33, K35, K36, K37, K38 | Same gate for actual contracted Deno versions, imports and permission contexts. |
| A19 | Bazel | R09, K32, K33, K35, K36, K37, K38 | Same gate for each contracted Bazel rule/toolchain/test-runtime combination. |

All acceptance cards are `BLOCKED`, unclaimed. K29 must define each named L/R
scope and add dependencies for any framework subcards before they can be claimed.
No unenumerated language scope can be marked complete. External production acceptance, publishing and rollout are
separate authorization gates; they are not implied by a local DONE card.

## Fleet ownership

| Lane | Default file scope | Coordination boundary |
| --- | --- | --- |
| CORE / COORDINATOR | `internal/model`, `graph`, `planner`, `influence`, `app`, `passcache`; shared registration and docs | Serialize shared schema, registration and CLI integration edits. |
| RUNTIME | Shared native backend/worker files; platform enforcement | Do not edit language certifiers concurrently; agree exact file ownership before claims. |
| GO | Go analysis and native Go qualification files/tests | Shared backend/model changes go through CORE/RUNTIME. |
| TS / JS / WORKER | Vitest/Node/Jest/worker bridges and dedicated fixtures | Separate bridge/file ownership; shared provider registration goes through CORE. |
| PYTHON / RUST / APPLE / BROWSER / OTHER | Corresponding adapter/runner files and dedicated fixtures | Claim one adapter/context at a time; no shared engine edits without reassignment. |
| QA | Shared acceptance harness and independently authored fault fixtures | New tests may expose unfinished features; do not weaken assertions to make a card pass. |
| CLI / CI | Commands, reports, scripts and workflow definitions | Assign exact files to avoid coordinator edits; external actions remain separately authorized. |

## Claims and evidence

Live fleet activity verified 2026-10-06 after the explicit restart request:

| Agent/task | Activity | Assignment |
| --- | --- | --- |
| `/root` | ACTIVE | Correct records; integrate accepted wave; coordinate dependency-ready macOS/Linux completion. |
| `/root/runtime_implementation` | ACTIVE | K07; only new `internal/native/envelope.go` and `envelope_test.go` initially; existing backend integration reserved until prior wave freezes. |
| `/root/completion_review` | ACTIVE | Read-only K21 and frozen-wave review, then K07 contract/source review. |
| `/root/completion_testing` | ACTIVE | Full normal/race/vet with explicit Python fixtures, then provisioned Vitest aggregate with no concurrent heavy builds. |

Previous `/root/factory_*` agents are historical owners, not active assignments.
Completion gates remain open until reviewed integration and actual checks pass.

Card claims and accumulated evidence (historical owners retained where useful):

```text
ID | STATE | AGENT/TASK ID | EXACT FILE SCOPE OR WORKTREE | COMMIT/EVIDENCE | NEXT ACTION
K07 | DOING | /root/runtime_implementation | NEW internal/native/envelope.go; envelope_test.go only initially | minimal common envelope design reviewed; runtime integration reserved | Implement bounded immutable resources, explicit absence/env/scratch contracts; no authority from declarations or ignored denials
K01 | DONE | /root | docs/qualified-contexts.md; shared core integration reserved | docs/qualified-contexts.md; read-only factory_review contract review, 2026-10-06 | Contract accepted; no new runtime qualification granted
K04 | DONE | /root | internal/model/execution.go; internal/runner/exact.go and tests; native shared outcome integration | fdc139c | Exact inventory/binding validators and native integration; full Go test/race/vet and actual Linux acceptance pass; Mori canonical core review complete
K42 | REVIEW | /root | internal/process/**; internal/adapter/golang/golang.go; internal/snapshot/snapshot.go and tests | eight actual descendant cancellation/overflow/orphan probes; independent review accepted | Final integrated race/vet and canonical staged Mori review; escaped children remain unqualified
K02 | DONE | /root/factory_implementation | internal/testutil/acceptance/** | independent review; installed Linux acceptance 70.170s; focused race/vet; Mori complete | Accepted harness; no new provider qualification
K03 | REVIEW | /root/factory_testing | testdata/endtoend/**; internal/qualification/endtoend_test.go | seven actual full-command baselines, 24 cases and 51 seeded mutations; cold/warm invocation costs; independent review accepted | Final integrated checks and canonical staged Mori review; no selective savings or consumer acceptance claimed
K05 | REVIEW | /root | internal/planner; internal/influence | semantic metadata, ancestor invalidation and latest association bounds independently reviewed; focused tests pass | Final integrated checks; broader provider integration remains separate and no omission authority added
K06 | REVIEW | /root/factory_implementation | internal/runner/groups.go; groups_test.go | focused normal/race/vet; independent review/testing; complete advisory Mori coverage | Final integrated checks and canonical staged review; conservative whole-workspace merge groups only
K13 | REVIEW | /root/factory_implementation | internal/adapter/golang/closure.go; closure_test.go; internal/native/goclosure.go; goclosure_test.go | compiler and independent native test oracles; normal/race/vet; independent review and advisory Mori accepted | Final integrated checks and canonical staged review; runtime/generated/dynamic effects remain gaps
K15 | DOING | /root + /root/completion_testing | internal/adapter/vitest | independent case collection reviewed; installed fourth-pair adapter/stage tests pass; aggregate application acceptance failed one scenario, isolated unchanged rerun passed | Rerun complete installed aggregate without build contention, then complete configured closure/runtime boundaries and older-pair collection evidence; aggregate acceptance not yet claimed
K21 | REVIEW | /root/completion_review + /root/completion_testing | internal/adapter/native/pythonclosure.go; pythonclosure.py; pythonclosure_test.go; pythonclosure_acceptance_test.go | independently authored native baseline/fault oracles passed normal23.458s and race26.642s; prior compile error corrected | Final source review, full repo normal/race/vet with both Python runtimes and staged Mori; runtime influence remains unqualified
REVIEW | DOING | /root/completion_review | read-only; no file ownership | frozen K03/K05/K06/K13/K42 source review accepted in described scope; K21 checks in progress | Review K07 contract and every next submitted wave; require fixes before DONE
```

Evidence must include actual commands/context and results, known exclusions and
review outcome. Do not record credentials, raw environment values or source
contents. Keep external acceptance and published availability separate.

## Dispatch order

1. First wave: K01 (coordinator/core), K02 (QA worker), K03 (fixture worker).
2. After K01: K04. After K04 and K02: Go K13 and TS K15 can run; runtime K07
   needs K04 only. These cards can run in parallel,
   with the coordinator handling core K05/K06 and shared interfaces sequentially.
3. K14 needs K13, K06 and K10; K16 needs K15, K06 and K10. K10 first needs
   K07, K08 and K02. Use the next free slots for language or
   runtime cards whose dependencies are complete. Keep K02's harness in use.
4. Integrate K30-K40 for the initial Go/TS milestone while independently ready
   additional-language analysis cards progress in disjoint files/worktrees.
5. Integrate every promised additional context through its A-card. K29 fixes
   named L/R scopes and any framework subcards; K41 closes the whole backlog
   after all initial and additional-context acceptance gates complete.

Before every dispatch, recompute readiness from the dependency tables, check
claims/file overlap, and include the card's acceptance criterion and safety
contract in the agent brief. One finished lane cannot authorize another's skips.
