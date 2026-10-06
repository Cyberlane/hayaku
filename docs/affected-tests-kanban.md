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
closes all promised macOS/Linux contexts after their separate acceptance gates.

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
- Preserve unrelated changes; install/fetch no tools automatically outside the
  explicitly approved isolated development scope in [runner provisioning](runner-provisioning.md). External
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
| K34 | CI | K31, K02, K46 | Trusted cache persistence across CI jobs | Authenticate portable receipts with an explicit trusted issuer/key lifecycle and context checks; fork/untrusted jobs cannot poison authority. Cache misses/unavailable storage execute again. |
| K10B | RUNTIME | K10A, K09 | Dynamic loader, interpreter and JIT closure | Enforce bound installed runtime/library/executable-memory provenance and required thread/process scopes; unsupported modes visibly fall back, without disabling enforcement to make runners launch. |
| K19 | WORKER | K15, K07, K08, K09, K02 | Cloudflare worker runtime qualification | Independently qualify worker execution, bindings, storage, requests, isolation and reset state against a defined worker context; ordinary Node/Vitest qualification is insufficient. |
| K30 | CORE | K05, K07, F03 | Monorepo and generated input closure | Cross-language consumers, workspace links and producers invalidate correctly; BASE/candidate generation stays independent, and provenance is enforced rather than declared. |


## DOING

| ID | Lane | Dependencies | Feature | Acceptance criterion |
| --- | --- | --- | --- | --- |
| K25 | APPLE | K04, K02 | SwiftPM/Xcode build and test closure | Bind resources, generated code, build settings, test plans, lifecycle, case inventories and simulator/device identities; unsupported macros/plugins/modes remain explicit. |
| P01 | QA | K29 | Isolated runner and fixture provisioning | Explicitly approved temporary macOS/Linux tools and fixture dependencies have official verified pins, private caches/locks and actual baseline records; installation alone grants no qualification. |

## REVIEW

| ID | Lane | Dependencies | Feature | Acceptance criterion |
| --- | --- | --- | --- | --- |

## DONE

| ID | Lane | Dependencies | Feature | Acceptance criterion |
| --- | --- | --- | --- | --- |
| K47 | RUNTIME | K09, K02 | Linux worker exec transition and payload bounds | Inherited descriptors close atomically at exec without racing the Go worker poller; exact advertised stderr payload fits independent of bounded enforcement framing, with FD, overflow, controlled-child and pure-Go regressions. |
| K11 | RUNTIME | K07, K08, K02 | macOS backend | A native macOS context has actual enforcement and receipt authority tests; unsupported host capabilities execute the original full scope. Docker/Linux success is not macOS qualification. |
| K46 | CORE | F04 | Bounded nonblocking authenticated cache reads | Authority keys and receipts reject preflight-to-open FIFO substitution and file/metadata drift; reads never exceed admitted bytes, failures force execution and never authenticate a pass. |
| K10A | RUNTIME | K07, K08, K02, K47 | Shared static executable capture and transport | Actual non-Go static Linux programs have immutable byte/context capture, enforced resource/process/network controls, drift rejection and bounded cleanup; operational results grant no authority. |
| K23 | RUST | K04, K02, K43 | Cargo/nextest build and test closure | Features, build scripts, proc macros, generated inputs, binaries and case inventory have complete producer/context identities; unsupported build/runtime effects are gaps. |
| K15 | TS | K04, K02 | TypeScript/Vitest complete configured graph and inventory | Extend configured Vite transforms for aliases, setup, mocks, dynamic collection/imports, plugins, generated assets and dependency inputs. Unresolved loaders/effects broaden. |
| K09 | RUNTIME | K07, K08 | Subprocess, native extension and service contracts | Controlled child programs and service state require complete identities and enforced boundaries; unsupported FFI, processes, networks and service state retain full execution. |
| K31 | CORE | K04, K06, K07, F04 | Complete native receipt identity and lifecycle | Bind entire input closure, inventory, group, proof, runner/toolchain/backend and configuration; tampering, drift, skips, failed or incomplete execution cannot mint reusable passes. |
| K45 | CORE | F03 | Bounded nonblocking WASI input capture | Preserve os.Root confinement, reject stale regular-file FIFO substitution and file/metadata drift, bound reads to admitted bytes and honor cancellation. |
| K44 | CORE | F03 | Nonblocking input-envelope file capture | Regular-to-FIFO replacement cannot block capture or source verification indefinitely; actual healthy and no-writer FIFO controls pass on macOS/Linux. |
| K43 | CORE | F01 | Bounded snapshot file hashing and drift checks | A file growing or being replaced after preflight cannot trigger unbounded allocation or yield a valid digest; independently challenge read bounds, identity, permissions and cancellation. |
| K07 | RUNTIME | K04, F03 | Enforced native input envelopes | Bind and enforce files, absent paths, directories, environment, links and write/scratch rules; an outside access cannot leave a qualified pass. Protect capture and live drift boundaries. |
| K08 | RUNTIME | K07 | Metadata, clocks and entropy policy | Each qualified context either enforces defined observable semantics or rejects these effects. Containment and interception are not assumed to establish native equivalence. |
| K29 | OTHER | K04, K02 | Remaining language/runner context contracts | Define exact runner/build/runtime/version scopes for the nine L/R lanes below, with required behavior, exclusions and evidence. Multiple frameworks need explicit subcards before implementation; generic full-command support is not qualified omission. |
| K03 | QA | F01, F02 | Representative baseline fixtures | Independently authored Go, TS, worker, Python, Rust and Apple workloads capture full-command behavior, discovery/startup failures and cold/warm costs; fixtures do not alter consumer repositories. |
| K05 | CORE | K04 | Complete old/new invalidation | All qualified providers handle transitive changes, removed edges, additions/deletions/renames, configuration changes and new/deleted tests without losing influence. |
| K06 | CORE | K04 | Safe group formation | Collection, initialization, hooks, order and mutable shared state determine test/file/package/suite boundaries; unproved independence merges or broadens groups. |
| K13 | GO | K04, K02 | Go inventory and imported dependency closure | Compiler metadata plus typed semantics account for imported packages, build tags, test variants, initialization and embed inputs; unresolved/native/toolchain effects remain explicit gaps. |
| K21 | PYTHON | K04, K02 | Python import, collection and fixture analysis | Interpreter/package/plugin identities, dynamic imports, discovery and pytest/unittest fixtures map to common inputs/groups; unknown influence broadens. |
| K42 | CORE | F01 | Unix process-tree cancellation | Timeouts, cancellation and output overflow kill the invoked process group, including compiler/runner descendants; actual descendant probe passes. Escaping children and Windows cleanup stay explicitly unqualified. |
| K01 | CORE | F01, F02, F05 | Supported-context and qualification contract | Document initial Go and TS/Vitest contexts, selection granularity, trusted components, forbidden effects and the reviewed conditions that permit reuse. Later contexts cannot inherit qualification merely through registration. |
| K02 | QA | F01, F02, F04, F05 | Shared adversarial acceptance harness | Independent inventories and seeded faults exercise cold/warm/change/failure/fallback/audit behavior through reusable runner-neutral scenarios; incomplete installed fixtures are visible. |
| K04 | CORE | K01 | Shared qualified execution model | Versioned inventory/group/input/effect/proof/context contracts compose existing providers; unknown effects and unsupported versions cannot produce skip authority. |

## BLOCKED

| ID | Lane | Dependencies | Feature | Acceptance criterion |
| --- | --- | --- | --- | --- |
| K10 | RUNTIME | K07, K08, K02, K10A, K10B, K10C | Linux backend generalization | Extend the existing worker to supported non-Go artifacts with real file/process/network/metadata bypass probes, cancellation and cleanup; no runner-specific selection logic in the worker. |
| K10C | QA | K10B, K02 | Linux generalization acceptance | Independently challenge the shared backend with actual supported non-Go runtimes, bypasses and complete cleanup; static-only acceptance cannot qualify Node or other dynamic contexts. |
| K14 | GO | K13, K06, K10 | Broader Go qualification | Support documented Go constructs, external test packages, setup and fixture inputs with enforced effect contracts; unsupported interfaces/reflection/cgo/concurrency broaden rather than guess. |
| K16 | TS | K15, K06, K10 | Node runtime qualification for TS/Vitest | Installed Node/module/runner identities and all startup/collection/execution effects fit an enforced context; native addons, JIT/runtime behavior and unsupported loaders cannot bypass qualification. |
| K17 | JS | K16, K02 | Node test affected execution | Account for runner collection, hooks, module loading, concurrency and exact terminal inventory using common qualification/reuse machinery. |
| K18 | JS | K16, K02 | Jest affected execution | Account for configured transforms, resolution, mocks, setup, snapshots and process scopes; reject unsupported plugin behavior and snapshot update modes. |
| K20 | WORKER | K19, K06 | Worker Vitest integration | Collect and execute supported worker-pool groups with complete outcomes and qualified reuse; binding/state changes invalidate consumers and unsupported worker modes fall back. |
| K22 | PYTHON | K21, K06, K10 | Python runtime and pytest/unittest qualification | Actual interpreter, native extension, subprocess and resource effects meet the supported boundary; both runners pass common and native-specific acceptance cases. |
| K24 | RUST | K23, K06, K10 | Rust runtime and Cargo/nextest qualification | Supported actual binaries have enforced input boundaries and reconciled outcomes; filtering and runtime effects preserve full-command semantics or fall back. |
| K26 | APPLE | K25, K06, K11, K09 | Swift/Xcode runtime qualification | Qualify documented host/simulator scopes with reset application/device state; UI/device/external-state tests cannot inherit host-unit qualification. |
| K27 | BROWSER | K04, K06, K09, K10, K02 | Browser/application/service boundary | Bind browser bytes, application build, storage, requests, service fixtures and cleanup/reset semantics; uncontrolled services or device state prevent reuse. |
| K28 | BROWSER | K27 | Playwright affected execution | Actual browser-based workloads reconcile discovery and outcomes and reuse only qualified isolated groups; browser-free reporter tests are insufficient. |
| K32 | CORE | K05, K06, K31, K14, K16 | Selective execution coordinator | For initial Go and TS/Vitest contexts, execute changed groups, validate reused groups, and run complete original scopes on qualification gaps; no unrelated existing native gate is silently replaced. |
| K33 | CORE | K32, K02 | Aggregate outcome reconciliation | Every required case/group has an executed terminal outcome or authenticated pass; startup/build/hook errors, unexpected identities, truncation, cancellation and drift cannot produce green. |
| K35 | CORE | K33, K02 | Full audits, revocation and recovery | Forced-full and scheduled audit paths revoke previous authority before execution, including cancellation/failure; mismatches revoke relevant qualification and retain original full gates. |
| K36 | CLI | K33, K30 | One end-to-end command and explanation | Setup/doctor/plan/run connect qualified paths and original fallbacks; stable reports distinguish executed, reused, affected, broadened and incomplete scopes without exposing source or environment values. |
| K37 | CI | K34, K35, K36 | Reproducible CI workflow | Exact revisions, provisioned tools, matrix context, private cache authority, fallback, artifacts and audits work in disposable CI-equivalent runs. External workflow changes require applicable authorization. |
| K38 | QA | K33, K35, K03 | Independent selection and isolation review | Review the influence algorithm and backend assumptions; run real hidden-dependency, unexecuted-effect, state, bypass, cache and fault cases against original full runs. Passing comparisons alone confer no qualification. |
| K39 | QA | K37, K38 | Compatibility and performance acceptance | Run actual declared versions/platforms and representative consumer workloads; record cold/warm end-to-end costs including capture/build/audits and negative savings. Unsupported modes demonstrably fall back. |
| K40 | COORDINATOR | K39 | Capability documentation and local handoff | Update status/safety/runner/CI docs with exact supported contexts and evidence; initial Go/TS milestone is complete locally, with external acceptance and release status separately recorded. |
| K41 | COORDINATOR | K42, K40, A01, A02, A03, A04, A05, A06, A07, A08, A10 | Whole-backlog local completion | Reconcile every promised context with implemented qualification, actual acceptance evidence, explicit exclusions and accurate docs; no placeholder or untested platform remains claimed as supported. External production acceptance and release remain separate. |

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
| L01 | JVM | K29, LA01A, LA01B, LA01C, LA01D, LA01E, LA01F | JVM analysis and collection | Account for selected build tools, classpaths, generated classes/resources, initialization, reflection and runner discovery; unresolved effects broaden. |
| R01 | JVM | L01, K06, K09, K10, K02, RA01A, RA01B, RA01C, RA01D, RA01E, RA01F | JVM runtime and runner qualification | Review VM/native/agent effects and enforce the selected context; exact runner filters and results preserve required semantics or fall back. |
| L02 | DOTNET | K29, LA02A, LA02B, LA02C | .NET analysis and collection | Account for selected builds, assemblies/resources, generators, startup, reflection and runner discovery; unresolved effects broaden. |
| R02 | DOTNET | L02, K06, K09, K10, K02, RA02A, RA02B, RA02C | .NET runtime and runner qualification | Review managed/native runtime effects and selected runner semantics; unsupported plugins, processes or services retain full execution. |
| L03 | RUBY | K29, LA03A, LA03B | Ruby analysis and collection | Account for installed gems, dynamic loading, hooks, shared helpers, resources and selected runner discovery; unresolved effects broaden. |
| R03 | RUBY | L03, K06, K09, K10, K02, RA03A, RA03B | Ruby runtime and runner qualification | Qualify actual interpreter/extension effects and fixture isolation; exact supported runner outcomes use common validation and reuse. |
| L04 | PHP | K29, LA04A | PHP analysis and collection | Account for Composer/autoload inputs, bootstrap, generated code, fixtures and selected runner discovery; unresolved effects broaden. |
| R04 | PHP | L04, K06, K09, K10, K02, RA04A | PHP runtime and runner qualification | Qualify interpreter/extensions and selected runner isolation; uncontrolled service and bootstrap effects retain full execution. |
| L05 | DART | K29, LA05A, LA05B | Dart analysis and collection | Account for packages, generated code, assets, initialization and chosen Dart/Flutter runner scopes; device/widget contexts are explicit separate scopes. |
| R05 | DART | L05, K06, K09, K10, K02, RA05A, RA05B | Dart runtime and runner qualification | Qualify the specified runtime and resource/extension effects; device/Flutter contexts cannot inherit plain Dart host qualification. |
| L06 | CPP | K29, LA06A, LA06B, LA06C | C/C++ analysis and collection | Account for headers, compiler/linker settings, generated sources, linked libraries, initialization and selected build/runner inventory. |
| R06 | CPP | L06, K06, K09, K10, K02, RA06A, RA06B, RA06C | C/C++ runtime and runner qualification | Review native/FFI/loading effects with an enforced supported contract; unsafe unaccounted behavior broadens, not a user-declared proof. |
| L07 | BUN | K29, LA07A | Bun analysis and collection | Account for configured TS/JS transforms, resolution, packages, setup, assets and native test discovery; Node qualification is not inherited. |
| R07 | BUN | L07, K06, K09, K10, K02, RA07A | Bun runtime and runner qualification | Qualify Bun-specific runtime/extension effects and terminal outcomes against actual supported binaries; unsupported modes run in full. |
| L08 | DENO | K29, LA08A | Deno analysis and collection | Account for module maps, locked imports, local/remote dependency bytes, assets and test discovery without automatic fetching. |
| R08 | DENO | L08, K06, K09, K10, K02, RA08A | Deno runtime and runner qualification | Review permissions and runtime enforcement rather than trusting declarations; actual context, runner outcomes and fallback pass review. |
| L09 | BAZEL | K29, LA09A | Bazel build/action/test closure | Account for configured targets, rules/toolchains, external repositories, action inputs and test inventory; build cache hits are not test receipts. |
| R09 | BAZEL | L09, K06, K09, K10, K02, RA09A | Bazel test execution qualification | Review actual test-language runtimes and sandbox/resource semantics; qualify named combinations only, with common reconciliation and receipt authority. |

## Framework implementation subcards

K29 fixes these named scopes in [language context contracts](language-contexts.md).
Analysis subcards are `READY`, unclaimed after accepted K29; runtime subcards
remain `BLOCKED` on their listed dependencies. Each requires its own
concrete fixture admission record before executable qualification. Exact pins are
unresolved for unprovisioned contexts, not wildcard-approved. Family L/R cards
aggregate these subcards and cannot be closed by one sibling framework.

| Analysis ID | Dependencies | Runtime ID | Dependencies | Named framework scope |
| --- | --- | --- | --- | --- |
| LA01A | K29 | RA01A | LA01A, K06, K09, K10, K02 | Maven, JUnit Jupiter |
| LA01B | K29 | RA01B | LA01B, K06, K09, K10, K02 | Maven, TestNG |
| LA01C | K29 | RA01C | LA01C, K06, K09, K10, K02 | Gradle, JUnit Jupiter |
| LA01D | K29 | RA01D | LA01D, K06, K09, K10, K02 | Gradle, TestNG |
| LA01E | K29 | RA01E | LA01E, K06, K09, K10, K02 | sbt, ScalaTest |
| LA01F | K29 | RA01F | LA01F, K06, K09, K10, K02 | sbt, MUnit |
| LA02A | K29 | RA02A | LA02A, K06, K09, K10, K02 | dotnet test, xUnit |
| LA02B | K29 | RA02B | LA02B, K06, K09, K10, K02 | dotnet test, NUnit |
| LA02C | K29 | RA02C | LA02C, K06, K09, K10, K02 | dotnet test, MSTest |
| LA03A | K29 | RA03A | LA03A, K06, K09, K10, K02 | Ruby/Bundler, RSpec |
| LA03B | K29 | RA03B | LA03B, K06, K09, K10, K02 | Ruby/Bundler, Minitest |
| LA04A | K29 | RA04A | LA04A, K06, K09, K10, K02 | Composer, PHPUnit |
| LA05A | K29 | RA05A | LA05A, K06, K09, K10, K02 | Dart, package:test host VM |
| LA05B | K29 | RA05B | LA05B, K06, K09, K10, K02 | Flutter, flutter_test widget host |
| LA06A | K29 | RA06A | LA06A, K06, K09, K10, K02 | CMake/Ninja, GoogleTest |
| LA06B | K29 | RA06B | LA06B, K06, K09, K10, K02 | CMake/Ninja, Catch2 |
| LA06C | K29 | RA06C | LA06C, K06, K09, K10, K02 | CMake, CTest executable scopes |
| LA07A | K29 | RA07A | LA07A, K06, K09, K10, K02 | Bun, bun:test |
| LA08A | K29 | RA08A | LA08A, K06, K09, K10, K02 | Deno, Deno.test |
| LA09A | K29 | RA09A | LA09A, K06, K09, K10, K02, R06 | Bazel, C/C++ test rules |

Each LA card must resolve its contracted source/build/configuration/resource and
collection closure with explicit unknown-effect gaps. Each RA card must implement
actual reviewed runtime qualification, lifecycle-preserving execution, exact
terminal outcomes and common receipt integration. See the contract matrix for
framework-specific acceptance; common full-run import support is insufficient.

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

All acceptance cards are `BLOCKED`, unclaimed. K29 defines each named L/R scope
and framework subcard; no family can complete before its named subcards.
No unenumerated language scope can be marked complete. External production acceptance, publishing and rollout are
separate authorization gates; they are not implied by a local DONE card.

## DEFERRED — Windows

These cards are outside the current completion gate by user instruction. Actual
Windows evidence is required before either can be completed or claimed supported.

| ID | Dependencies | Feature and acceptance criterion |
| --- | --- | --- |
| K12 | K07, K08, K02 | Windows backend and cache authority: qualify actual enforcement and private ACL semantics; reuse stays disabled until context review and runtime checks pass. |
| A09 | K12, K14, K16, K32, K33, K35, K36, K37, K38 | Separate actual Windows language/runner and cache acceptance; cross-compilation does not qualify it. |

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
| `/root` | ACTIVE | K09/K31/K45 committed13ef109; integrate exact reviewed K23 source and fourth Vitest fixture, K23/K15 committed9201d93 after full repository checks/canonical and integrate reviewed K11+K46; K11+K46 combined normal508.938s/race715.622s/vet0.9s passed; K47v4/K10A integrated; original-deadline actualLinuxGo148.96s and boundaries153.410s passed; final full normal341.257s/race392.732s/vet1.141s passed; canonical staged233/233 and12/12 focus accepted; local commit next, K34 and K10B now READY. |
| `/root/runtime_implementation` | ACTIVE | K09 actual independent corpus passed; K23 seven files frozen/source-reviewed with actual Mac checks; K10A static source and actualRust/legacychild checks accepted/frozen, final independent six-test QA normal/race/vet accepted; implement K25 Appleclosure in isolated checkout; dynamic/JIT K10B and complete generalization K10C remain separate gates. |
| `/root/completion_review` | ACTIVE | Read-only composed K11/K46/K47/K10A source and final QA/Mori accepted; K25 source review and actual controls remain; no implementation ownership. |
| `/root/completion_testing` | ACTIVE | Independent repaired K23 actual Mac/Linux corpus passed; approved P01 provisioning and baselines; independently authored K11 macOS boundary corpus. |

Previous `/root/factory_*` agents are historical owners, not active assignments.
K07 and K08 passed their corrected local card gates after independent macOS/Linux
checks, source review and full normal/race/vet checks. K15 descriptive source and
fourth-pair acceptance are reviewed; older actual context and locked-fixture gates
remain open. K43 passed integrated full normal/race/vet checks and independent macOS/Linux filesystem and Git checks. No new general
native skipping is claimed.

Card claims and accumulated evidence (historical owners retained where useful):

```text
ID | STATE | AGENT/TASK ID | EXACT FILE SCOPE OR WORKTREE | COMMIT/EVIDENCE | NEXT ACTION
K08 | DONE | /root/runtime_implementation | NEW internal/native/observables.go; observables_test.go; internal/native/backend.go; independent tester NEW observable_boundary_test.go | Corrected K07 accepted; actual metadata/clock/entropy and dormant-effect probes independently passed; canonical staged Mori209/209+14/14focus reviewed; full normal/race/vet passed | Fixed reject-only policy/effect veto accepted; empty effects remain no proof, other external contracts K09
K29 | DONE | /root | docs/language-contexts.md; docs/affected-tests-kanban.md | Independent specification review accepted; twenty named profiles and forty analysis/runtime subcards; 124 unique card DAG valid; installed candidate facts verified | Actual per-framework admission/qualification and acceptance remain separate; unresolved versions never wildcard-approved
K47 | DONE | /root | main docs/safety.md restricted native platform scope; internal/native/backend.go protocol budget/marker only and internal/native/worker/main.go version/close_range only; same corrective hunks in /tmp/hayaku-k10-worktree shared files now root-owned; QA retains NEW program_boundary_test.go | Independent actualstaticC found sporadic Go runtime epollfd EBADF pre-exec and exact32MiB stderr wrongly consumed framing budget; failures retained and source review confirms causes | close_range CLOEXEC and bounded marker reserve plus guest-byte cap; newworkeridentity, independent FD/size/stress/cleanup/child acceptance passed; composed LinuxGo originaldeadline PASS148.96s and seven-test Linuxboundary corpus153.410s passed; splitMori88/88+96/96 accepted, final exact main native normal356.001s/race471.325s passed; full repository normal341.257s/race392.732s/vet1.141s passed; canonical staged233/233 and12/12 focus,24 identities/73 pairs accepted; exact source/test hashes retained
K25 | DOING | /root/runtime_implementation | NEW isolated /tmp/hayaku-k25-worktree from9201d93; internal/adapter/swift/swift.go and NEW closure.go/closure_test.go; then internal/adapter/xcode/xcode.go and NEW closure.go/closure_test.go; root owns shared registrations/docs | K04/K02 DONE; installed Swift/Xcode candidates, existing descriptive adapters; K10A DONE, K10B READY; original Swift full/proposal/fault fixture passed166.47s within180s; Xcode generated-root alias fix under actual original4min acceptance | Bind actual compiler/build/source/resource/test-plan and producer/context identity with explicit plugin/macro/generated/device gaps; private offline builds/native inventories, source/tool drift and dormant unknown effects; source review plus actualMac/availableLinux tests and full checks/Mori; no receipt or runtime qualification
K46 | DONE | /root | isolated /tmp/hayaku-cache-worktree from13ef109; internal/passcache/cache.go readPrivate boundary only; NEW internal/passcache/read_boundary_test.go | Live review found cache readPrivate still uses blocking Root.Open after regular-file preflight, independently of corrected input/WASI boundaries | Reuse Root confinement plus nonblocking open, exact size/full metadata checks; actual Macrace1.321s and Linux healthy/stale-FIFO/postread drift controls passed; source/tests/Mori accepted; exacttwo files integrated main; combined normal508.938s/race715.622s/vet0.9s passed, final K47/K10A composition full normal341.257s/race392.732s/vet1.141s passed; canonical staged233/233 and12/12 focus,24 identities/73 pairs accepted; exact source/test hashes retained
K11 | DONE | /root | isolated /tmp/hayaku-k11-worktree from13ef109; NEW internal/native/darwin.go/darwin_test.go; backend.go platform dispatch/context only; go.go explicit platform/build environment only; QA owns NEW darwin_boundary_test.go | Actual fixed-profile enforcement Bind and v2 cold/warm/changed pure-Go lifecycle passed34.94s; independent10-test finalMacnormal148.915s passed actualpositivefile/process/live-network/cwd/output/cancellation/cleanup controls plusaudit/tamper/fail/skip/producer-authority; initial cache andtypeddeadline defects corrected and failures preserved | Independent finalMacrace233.744s/vet0.294s passed; composedK11+K46 exact reviewedsource/QA integratedmain full normal508.938s/race715.622s/vet0.9s passed; final K47/K10A composition actualLinuxGo148.96s and boundaries153.410s passed; full normal341.257s/race392.732s/vet1.141s passed; canonical staged233/233 and12/12 focus,24 identities/73 pairs accepted; exact source/test hashes retained; restricted pure-Go context only, no arbitrary macOS runner qualification
K10A | DONE | /root/runtime_implementation | isolated /tmp/hayaku-k10-worktree from13ef109; NEW internal/native/program.go/program_test.go; child.go shared capture only; backend.go private captured-primary transport only; tester NEW program_boundary_test.go reserved | Final source and independent six-test QA accepted at2c1adc; actual normal/race stress, raw32MiB/+1, guards/cancel/alias and512MiB aggregate passed, controlled-child v4 race249.936s passed; finalQA vet passed | Exact source/QA integratedmain with independent Linux-only platform vetoes reviewed; splitMori source88/88focus7/7 and tests96/96focus5/5 accepted, actualLinuxGo PASS148.96s and combined seven-test Linuxboundary153.410s passed; final full repository normal341.257s/race392.732s/vet1.141s passed; canonical staged233/233 and12/12 focus,24 identities/73 pairs accepted; exact source/test hashes retained; no receipt/decoder/cache, dynamic/JIT remains separate
P01 | DOING | /root/completion_testing | NEW private /tmp/hayaku-language-acceptance-* setup root and candidate manifest; docs runner-provisioning approval record root only; source/fixture integration exact claims later | User explicitly approved isolated provisioning following docs/runner-provisioning.md on2026-10-06; no global host installation/credentials/consumer-repo/service/hosted-CI changes | Verified Nextest146 Mac/Linux and allfourVitest pairs unblock K23/K15; committed checkpoint47records and9/20 macframeworkoriginalbaselines retained in docs/evidence; subsequent C++ Mac three and Linux Deno/Dart baselines bring live named counts12/20 Mac and2/20 Linux, not qualification; continue remaining named framework SDKs/dependencies with official checksums before execution, private lock/cache closure and actual full-command baselines; report pins and paths, not support
K45 | DONE | /root | isolated /tmp/hayaku-k45-worktree branch factory/capsule-input-reader-20261006 fromee9d4a8; internal/capsule/filesystem.go confined input read only; NEW filesystem_adversarial_test.go | Exact two-file source and final canonical213/213+2/2focus accepted; actual Mac normal/race/vet and fresh Linux preflight/FIFO/post-read faults passed | Integrated main full normal/race/vet passed; combined canonical222/222+18/18focus accepted; actual Linux Go lifecycle148.43s and boundary corpus123.712s passed; preserve confinement and bounded reads, no atomic/history claim
K44 | DONE | /root + /root/completion_testing | internal/inputbundle/bundle.go regular-file open/identity only; tester NEW fifo_adversarial_test.go | Opened regular-file identity and nonblocking source-capture correction independently accepted; actual Mac/Linux stale-preflight no-writerFIFO controls pass; full project normal/race/vet pass, canonical Mori212/212+2/2focus reviewed | Final docs-only canonical refresh; fix covers inputbundle capture, not every file-read boundary
K09 | DONE | /root/runtime_implementation | isolated /tmp/hayaku-k09-worktree, branch factory/controlled-child-20261006 from2979914; NEW internal/native/child.go/child_test.go; envelope.go/tests; backend.go controlled-child execution only; worker/main.go; tester child_boundary_test.go reserved | Fixed RUN-only broker source and independent Linux corpus accepted: normal102.590s/race106.323s/vet; exact source/copy drift rejects, protocol/overflow/deadline/cleanup cases pass | Composed with K31/K45 in main; full normal/race/vet and canonical222/222+18/18focus accepted; actual v3 Linux Go lifecycle148.43s and boundary/child corpus123.712s passed. Only initial managed launch is fixed; ignored denials or self-reexec do not establish equivalence; Envelope authority disabled, arbitrary FFI/services unsupported
K31 | DONE | /root | isolated /tmp/hayaku-k31-worktree, branch factory/native-authority-20261006 from2979914; NEW internal/native/authority.go/tests, backend.go authority binding only, go.go Go issuer/tool identity only, observable_boundary_test.go synthetic producer fixture only; snapshot/digest.go shared bounded FileDigest API + NEW file_digest_test.go; NEW capability gate files from K09 extraction; tester NEW authority_adversarial_test.go | Private Go producer/seal source and finalisolatedMori217/217+10/10focus accepted; actualLinux lifecycle150.723s and decoderpass/fail14.996s,100independentrejection/FIFO cases pass; staged K31 fullnormal/race/vet passed and exact canonical218/218+10/10 accepted; newer composed K09/K45 fullnormal/race/vet and canonical222/222+18/18focus accepted; actual v3 Go lifecycle148.43s/boundaries123.712s pass | Implement private in-process artifact seals binding complete reviewed provider/source/tool/program/inventory/context data; JSON and caller strings cannot issue; preserve pureGo reuse and before-cache negative gates
K43 | DONE | /root/runtime_implementation + /root/completion_testing | isolated /tmp/hayaku-k43-worktree, branch factory/bounded-snapshot-20261006 from cca5939; internal/snapshot/digest.go; snapshot.go raw-file validation only; worker NEW digest_bounded_test.go; tester NEW digest_adversarial_test.go | Frozen four-file correction independently source-reviewed; package normal/race/vet pass; actual Mac/Linux64MiB bounded-memory, growth/replacement, earlier-file/directory drift and no-writerFIFO probes pass; Git SHA1/SHA256 healthy/content/mode faults pass on both OSes | Integrated onto2979914; full normal/race/vet passed with bothPython+actualLinux; canonical staged Mori211/211+4/4focus accepted; committed1234d61; entry metadata observations are not atomic/history proof, directory-name enumeration allocation is outside scope
K23 | DONE | /root/runtime_implementation | isolated /tmp/hayaku-k23-worktree branch factory/rust-build-closure-20261006 from1234d61; internal/adapter/cargo/cargo.go and NEW closure.go/tests, then serial nextest.go, existing nextest_test.go and NEW closure.go/closure_test.go; shared registration/API edits root only | Repaired seven files frozen and source/test Mori accepted; compiler/private target library closure fixes original-Cargo/direct-binary loader mismatch; actual Mac normal47.809/43.246s and race188.712/204.769s/vet passed; provisioned Nextest0.9.146 actual Mac/Linux baselines pass | Repaired Linux full Cargo/Nextest and independent Mac normal88.106s/race292.110s/vet passed; exact seven files integrated main, full main normal/race/vet and canonical review passed; custom harness/doctest/proc-macro/scripts/generated/native/ambient effects stay explicit gaps; default source-mutating Nextest reporting store rejects
K07 | DONE | /root/runtime_implementation | internal/native/envelope.go; envelope_test.go; backend.go; worker/main.go; NEW envelope_boundary_test.go | Independent source/test review and canonical staged Mori accepted204/204; installed Linux probes15.240s independently14.196s; legacy43.413s; full normal/race/vet passed with bothPython and actualLinux contexts | Final case/Unicode/source/copied/multi-root alias correction independently accepted: Mac normal0.525/race1.687, fresh Linux filesystem all variants, operational15.527s; full normal/race/vet and canonical Mori209/209+14/14focus passed. Guard still forbids envelope receipts
K01 | DONE | /root | docs/qualified-contexts.md; shared core integration reserved | docs/qualified-contexts.md; read-only factory_review contract review, 2026-10-06 | Contract accepted; no new runtime qualification granted
K04 | DONE | /root | internal/model/execution.go; internal/runner/exact.go and tests; native shared outcome integration | fdc139c | Exact inventory/binding validators and native integration; full Go test/race/vet and actual Linux acceptance pass; Mori canonical core review complete
K42 | DONE | /root | internal/process/**; internal/adapter/golang/golang.go; internal/snapshot/snapshot.go and tests | 088d4a7; independent source/test review; full normal/race/vet and final Python delta checks pass; canonical staged Mori complete | Accepted within descriptive/common contract; runtime qualification and coordinator integration remain separate
K02 | DONE | /root/factory_implementation | internal/testutil/acceptance/** | independent review; installed Linux acceptance 70.170s; focused race/vet; Mori complete | Accepted harness; no new provider qualification
K03 | DONE | /root/factory_testing | testdata/endtoend/**; internal/qualification/endtoend_test.go | 088d4a7; independent source/test review; full normal/race/vet and final Python delta checks pass; canonical staged Mori complete | Accepted within descriptive/common contract; runtime qualification and coordinator integration remain separate
K05 | DONE | /root | internal/planner; internal/influence | 088d4a7; independent source/test review; full normal/race/vet and final Python delta checks pass; canonical staged Mori complete | Accepted within descriptive/common contract; runtime qualification and coordinator integration remain separate
K06 | DONE | /root/factory_implementation | internal/runner/groups.go; groups_test.go | 088d4a7; independent source/test review; full normal/race/vet and final Python delta checks pass; canonical staged Mori complete | Accepted within descriptive/common contract; runtime qualification and coordinator integration remain separate
K13 | DONE | /root/factory_implementation | internal/adapter/golang/closure.go; closure_test.go; internal/native/goclosure.go; goclosure_test.go | 088d4a7; independent source/test review; full normal/race/vet and final Python delta checks pass; canonical staged Mori complete | Accepted within descriptive/common contract; runtime qualification and coordinator integration remain separate
K15 | DONE | /root + /root/completion_testing | internal/adapter/vitest and internal/app/vitest_test.go (root), NEW startup_adversarial_test.go (independent tester); root NEW testdata/vitest-runtime-v4-vite822/package.json/package-lock.json and .github/workflows/ci.yml matrix/setup only | configured startup transitive closure and original/transformed AST import analysis independently reviewed; computed/dormant/escaped/direct SSR loader regressions pass; installed fourth-pair adapter normal42.829s/race47.706s/vet pass | Fresh AST/closure application aggregate712.177s passed all5 shared and11 dedicated Vitest checks with original deadlines; snapshot preceded only legacy-specific deltas. Final typed/context normal3.970/race6.515/vet pass; latest source full normal/race/vet passed. Prior aggregate680.095s covered historical graph. Collection path admitted only on fourth pair; approved P01 exact private four-pair baselines passed; root integrated exact lockedfourth CI definition; allfour actualMacadapter suites pass37.371/36.750/24.751/43.703s; full main normal/race/vet, canonical source review, exact provisioned fixture/lock identity and localworkflow Bash syntax checks passed; hosted CI acceptance remains separate
K21 | DONE | /root/completion_review + /root/completion_testing | internal/adapter/native/pythonclosure.go; pythonclosure.py; pythonclosure_test.go; pythonclosure_acceptance_test.go; unittest.py; pytest.py | Accepted baseline088d4a7; new strict Unicode serialization correction source-reviewed and focused normal/race passes | Strict Unicode correction full native normal/race and full project normal/race/vet pass; canonical staged Mori/source review accepted; runtime qualification separate
REVIEW | DOING | /root/completion_review | read-only; no file ownership | K03/K05/K06/K13/K21/K42 accepted in described scope and committed088d4a7; K07 source and Python surrogate correction accepted | Review K08 and every next submitted wave; require fixes before DONE
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
