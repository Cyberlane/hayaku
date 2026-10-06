# Additional language context contracts

K29 specification, 2026-10-06. These are implementation targets, not executable
qualification records. Existing catalog entries preserve original full commands.
The framework cards in [the kanban](affected-tests-kanban.md) must be implemented
and accepted separately before their entries can use affected execution or reuse.

## Admission record

One admission record describes one concrete build, runner, runtime and platform
combination. It must contain all of the following before runtime qualification:

| Field | Required identity or evidence |
| --- | --- |
| Profile | Named framework subcard, language, schema and reviewed provider policy version. |
| Platform | Actual OS, architecture and backend identity; host, container, simulator and device are different contexts. |
| Tools | Exact observed compiler/build/runtime/runner versions and immutable executable/module/library digests, including agents, extensions and transitive installed plugins. |
| Fixture | Exact source revision/input digest and dependency lock; independent original full-command inventory and assertion/startup/fault oracles. |
| Invocation | Structured original argv/cwd, build options, selected projects/targets, complete effective environment identity and preserved collection/execution modes. |
| Inputs | Resolved source/configuration/resources, absent paths and directory memberships, dependency libraries and generated producer/output identities. |
| Lifecycle | Startup, collection, hooks, ordering, shared state, concurrency and complete execution-group boundaries. |
| Enforcement | Reviewed complete user-observable input/effect contract, actual bypass/reset/cleanup tests and trusted component identities. |
| Results | Exact independent case inventory and complete terminal result translation, including startup/build/hook failures and disabled/ignored cases. |
| Acceptance | Common independent fault, reuse, tampering, drift, cancellation, audit and CI-equivalent tests; end-to-end cold/warm costs. |

Version ranges, protocol compatibility, a configured path and a serialized
`qualified` assertion do not satisfy this record. A missing installed version is
**unresolved**, not a wildcard. Populate exact pins from installed tools and
reviewed fixtures; do not invent versions or download tools during discovery.
Every change to a pin, platform, invocation mode or policy requires revalidation.
Descriptions and imported records never create in-process qualification authority.

The initial specification recorded locally installed Ruby, Bun and CMake
candidates and unresolved additional runner fixtures. Approved isolated
[runner provisioning](runner-provisioning.md) now records exact candidate
identities and original healthy/fault baselines for sixteen named macOS and sixteen Linux profiles,
plus Rust and Vitest compatibility fixtures. SDK-only records and unresolved
framework baselines remain explicit. None of these setup records is the complete
admitted context above; missing qualification retains an available original
full command.

## Named framework scopes

Each row has an analysis card (`LA…`) and runtime/integration card (`RA…`). They
share the family aggregate (`L…`/`R…`); one framework cannot complete its sibling.
Each admitted framework/version uses its own manifest. All profiles initially
use complete package/project/binary lifecycles; case filters need additional
independence proof. Unsupported behaviors broaden to the original full scope.

| Suffix | Family | Build/runner scope | Analysis and collection requirements | Runtime and result requirements |
| --- | --- | --- | --- | --- |
| 01A | JVM | Maven, JUnit Jupiter | Effective POM, reactor, compiler/classpaths, Surefire/Failsafe provider, selected engines, resources/generators and native engine inventory. | Preserve unit/integration-test phases, dynamic/parameterized identities, extensions and before/after lifecycle; complete installed engine outcomes. |
| 01B | JVM | Maven, TestNG | Same Maven build closure; TestNG suites, factories, data providers, groups and listeners own collection influence. | Preserve suite ordering/dependencies/listeners, disabled cases and startup/configuration failures. |
| 01C | JVM | Gradle, JUnit Jupiter | Resolved project/task/configuration graph, toolchains, test engines, generated sources/resources and complete selected test tasks. | Preserve task startup, worker JVMs, hooks, filters and engine lifecycle; no test-cache hit is a receipt. |
| 01D | JVM | Gradle, TestNG | Same Gradle closure with configured TestNG suite/data-provider/listener inventory. | Preserve configured suite and task semantics; no inherited JUnit qualification. |
| 01E | JVM | sbt, ScalaTest | Effective build/settings, Scala compiler/plugins, project/classpath/resource closure and suite/style-specific discovery. | Preserve selected suite styles, fixtures, tags and asynchronous lifecycle; unknown styles broaden. |
| 01F | JVM | sbt, MUnit | Same sbt closure with MUnit-specific suite/fixture collection. | Preserve generated test identities, fixtures and async outcomes; no inherited ScalaTest qualification. |
| 02A | DOTNET | dotnet test, xUnit | Evaluated MSBuild projects, NuGet lock/assets, SDK/runtime, generated assemblies/resources and runner-specific discovery. | Preserve actual installed xUnit version's fixtures, theories and parallel collection scopes; complete runner outcomes. |
| 02B | DOTNET | dotnet test, NUnit | Same build closure with NUnit attributes, parameterized sources and discovery plugins. | Preserve setup/teardown, ordering, parallelism, ignored and inconclusive outcomes. |
| 02C | DOTNET | dotnet test, MSTest | Same build closure with MSTest discovery, data rows/sources and runsettings. | Preserve class/assembly lifecycle, data-driven identities and deployment/resource semantics. |
| 03A | RUBY | Ruby/Bundler, RSpec | Installed interpreter/gem lock and extension bytes; requires/autoload, shared examples, helpers, hooks and native example inventory. | Preserve suite/context/example hooks, ordering, mocks and shared state; exact installed formatter result protocol. |
| 03B | RUBY | Ruby/Bundler, Minitest | Same Ruby build/runtime closure; native classes/methods/spec discovery, plugins and helper/setup influence. | Preserve autorun, seed/order, setup/teardown and plugin behavior; independent terminal identities. |
| 04A | PHP | Composer, PHPUnit | PHP/extension bytes, Composer lock/autoload, bootstrap, attributes/providers, resources and native inventory. | Preserve suite/data-provider lifecycle, skipped/incomplete/risky outcomes and bootstrap failures. |
| 05A | DART | Dart, package:test host VM | SDK, pub lock/package configuration, generated inputs/assets and native host-VM test discovery. | Preserve zones, setup, async completion and isolates; browser/Flutter/device semantics cannot inherit host qualification. |
| 05B | DART | Flutter, flutter_test widget host | Flutter engine/SDK/package bytes and build assets; widget-specific collection and generated registrants. | Preserve fake-clock/pump/framework lifecycle and reset engine state; real-device/integration-test contexts remain separate. |
| 06A | CPP | CMake/Ninja, GoogleTest | Configured compiler/linker/header/depfile/build graph, linked libraries, generated sources/resources and native typed/parameterized case list. | Bind actual binaries; preserve environments/fixtures and exact ignored/disabled/failure outcomes. |
| 06B | CPP | CMake/Ninja, Catch2 | Same native build closure with installed Catch2's case/tag/generator inventory and configuration. | Preserve sections/generators, ordering and reporting semantics; no inherited GoogleTest qualification. |
| 06C | CPP | CMake, CTest executable scopes | Configured targets, dependency/fixture graph, test commands/properties, working directories, labels and resource specifications. | Execute whole declared binaries by default; bind their separate reviewed runtime proofs; arbitrary executable commands remain full. |
| 07A | BUN | Bun, bun:test | Bun binary, lock/package/transformation/resolution closure, preload/setup and independently collected cases. | Preserve Bun-specific mocks/hooks/concurrency and terminal results; Node/Vitest qualification is not inherited. |
| 08A | DENO | Deno, Deno.test | Deno binary, import maps/locks, complete already-installed local/remote module bytes, assets and native discovery. | Review actual permissions/enforcement, workers and lifecycle; missing remote bytes broaden without fetching. |
| 09A | BAZEL | Bazel, C/C++ test rules | Exact configured rule/toolchain/action/target graph, repository bytes and test inventory; select named C/C++ rule combinations. | Bind actual test binaries and reviewed C/C++ runtime proofs, sandbox/resource/reset semantics; action-cache hits are not test receipts. |

The named targets do not promise all framework versions, every test engine/style,
custom build rules or every host/device mode. JUnit Vintage, arbitrary JVM engines,
real Flutter devices, browser Dart tests and other Bazel test-language rules require
new named cards and independent admission. Their existing full-command entries
continue to work where their tools are installed. An unsupported plugin, generator,
native library, reflection target, service or runtime effect remains an explicit
gap even when its branch was not covered by tests.

## Shared implementation boundary

Common code owns bounded identity capture/revalidation, old/new invalidation,
input/effect validation, group formation, authenticated receipt lifecycle,
aggregate reconciliation, audits, fallback and report redaction. The model is
`model.ExecutionInventory`, `model.Effect` and `model.ExecutionBinding`; use the
existing common runner and backend rather than another selection/cache engine.

Language adapters resolve their compiler/build/import semantics into that model.
Framework bridges supply native discovery, lifecycle couplings, structured runner
arguments and strict terminal result translation. Backends enforce the reviewed
capabilities. No framework-specific branch belongs in common influence traversal
or receipt verification. Discovery is trusted executable project code; it also
needs enforced qualification before it can authorize omission.

For each analysis card, independently test direct/transitive inputs, build flags,
setup/hooks, dormant dynamic effects, additions/deletions, generated producers and
collection failures. For each runtime card, use the common acceptance harness plus
actual framework/platform probes for input bypass, state, unknown effects, skipped
or missing cases, tampering, drift, cancellation and audits. Normal/race/vet/Mori
checks do not substitute for these native acceptance tests.
