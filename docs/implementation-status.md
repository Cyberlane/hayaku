# Implementation status

This maps the development backlog to actual capabilities. "Implemented" means
code and its specified bounded behavior exist; it does not grant production
omission, native platform acceptance or qualification for a consumer's CI context.
Every runner remains on full-suite required commands. Hayaku itself is the first
local pilot. A separate private consumer has a locally committed full-suite
baseline integration; additional projects require their own qualification.

| Ticket | Local state | Remaining boundary |
| --- | --- | --- |
| HYK-001 Safety contract | Implemented fail-closed contract | Review/enforce adapter assumptions before production omission |
| HYK-002 Foundation | Go CLI, strict config, AGENTS, standard library | Native support matrix grows through qualification |
| HYK-003 Snapshots | Committed Git pairs, raw diffs/bytes/modes, mutation validation | Staged/worktree modes; symlink/gitlink support intentionally rejected |
| HYK-004 Contexts | Workspace commands, host context, native Go settings/tool-byte binding | Full external input/isolation and CI matrix qualification |
| HYK-005 Contracts | Versioned shared evidence, gap, runner, report contracts | Public plugin API/version qualification |
| HYK-006 Graph | Old/new union, deterministic closure, new/deleted units | Adapter completeness remains independent |
| HYK-007 Uncertainty | Full required runs or explicit errors | Qualified isolation-scoped production narrowing |
| HYK-008 Plans | JSON/human reasons, source/context/tool digests, argv/cwd | Production omission explanations follow a qualified algorithm |
| HYK-009 Go adapter | Native package/test/import/embed/build-input evidence | Arbitrary runtime/cgo inputs unqualified |
| HYK-010 Go runner | Commands and terminal package/test reconciliation | More flag/wrapper/version contexts independently qualify |
| HYK-011 Fixtures | Native Git/Go/Cargo/Vitest, protocol pytest, five fault challenges | Broader framework/dispatch/FFI/service corpus |
| HYK-012 Properties | Independent graph oracle, monotonicity, decoder/snapshot/qualification fuzzing | Extend as new algorithms/adapters arrive |
| HYK-013 Shadow | Separate Go/Vitest source/runtime copies, misses and incomplete/mismatched outcomes | External-state isolation and other native reconcilers |
| HYK-014 Performance | Complete-cost arithmetic, local pilot tooling and two warmed synthetic observations | Representative CI datasets, thresholds and accepted savings |
| HYK-015 Cache | Private atomic advisory JSON, integrity/context/source/tool keys | No cached omission authority; measured storage-scale work |
| HYK-016 Fault challenges | Five independent seeded Go regression oracles | Real incident datasets; native Stryker JS/.NET/Scala integration |
| HYK-017 CI integration | Full-gated reference script, force-full override, private consumer baseline hook | Qualified skip mode and consumer CI acceptance |
| HYK-018 Vitest | Configured Vite import influence, Node/dependency binding, native full results and shadow for 4.1.11 | Runtime completeness, browser/typecheck/other versions and consumer qualification |
| HYK-019 Bazel | Explicit original full-suite integration | Native configured target/action graph and pinned real fixtures |
| HYK-020 Cross-language | Declared producer/input consumers add influence | Enforced generator/service/FFI contract evidence |
| HYK-021 pytest | Native JSON collection bridge, whole files, conservative inputs | Installed native verification; runtime/dependency/fixture qualification |
| HYK-022 Cargo | Native offline/locked workspace graph, package commands; native unit/integration/docs fixture | Build-script/proc-macro/runtime-input and results qualification |
| HYK-023 JVM | Separate original Maven/Gradle/sbt full-suite adapters | Native configured module/suite metadata and result reconciliation |
| HYK-024 .NET | Explicit original full-suite integration | SDK/VSTest/MTP/framework-specific native metadata and filters |
| HYK-025 Apple | Explicit original Xcode/Swift full-suite integration | Native schemes/plans/destinations/XCTest/SwiftTesting result fixtures |
| HYK-026 Fine cases | Qualification criteria and challenge/observation validators | Sound case-level algorithm; no case-level omission enabled |
| HYK-027 Distribution | Six cross-build archives, checksums, source/build provenance, MIT/Go notices and versioned release automation | Native installed-artifact matrix and binary signing/notarization |
| HYK-028 Setup | Init without overwrite, doctor capabilities/runner paths, honest gaps | Root Vitest manifest detection; richer multi-root and framework setup |

No installed pytest runner was available; that native test explicitly skips.
Go/Cargo native fixtures ran locally. Vitest 4.1.11 file/project/filter fixtures
ran using an existing externally installed executable on disposable sources only;
the external project's source, configuration and tests were not run or changed.
Bazel/.NET/Maven/Gradle/sbt tools were unavailable; their entries are full-suite
fallbacks, not completed native adapters. Cross-compilation is not native runtime
qualification. Hayaku does not add telemetry, credentials or automatic consumer
CI enrollment. Public release automation does not enable affected-test omission.

The first [local pilot observations](pilot-results.md) describe an earlier private
prepublication source and raw costs. They do not establish accepted CI savings or
production omission. Later context/path regressions are recorded separately from
that dataset.

A private consumer's prepared integration and native Node 22 workspace observation
are described in [CI integration](ci.md#historical-external-consumer-baseline).
This validates an original full command and adds measurement overhead; it does not establish
Vitest shadow acceptance for that consumer, affected-test savings or live CI acceptance.

## v0.2.0 Vitest implementation

The bound adapter uses native Vitest 4.1.11/Vite transforms for JS/TS proposals,
retains broad config/setup/resource ownership and original full required scopes,
and reconciles project/file/test results including duplicate names. Shadow detects
a deliberately omitted filesystem-dependent failure; unhandled global errors
invalidate comparison. Node and installed module bytes/modes/links are bound and
revalidated, and source/runtime mutation cannot become a successful comparison.
See [setup, qualification commands and limits](vitest.md). Legacy unbound file
discovery remains available, with full proposal gaps and no native execution.
