# Implementation status

The source and tests define current behavior. “Native” below means installed-runner evidence and result handling, not complete runtime influence or permission to skip a native suite. Original native plan commands remain full. The restricted native group command has its own contract, described below. v0.4.0 permits whole-suite reuse only within the separately enforced [deterministic WASI contract](capsules.md).

## Go and TypeScript release priority

The local restricted Go and TypeScript/Vitest milestone is complete. v0.6.2
publication preparation is authorized; the failed signed v0.6.0 and v0.6.1 tags
are preserved, and neither produced a release. Corrected hosted core gates,
four Vitest compatibility pairs and WASI pilots remain pending on Linux and macOS.
Published asset verification and external consumer acceptance remain pending.
Other-language expansion and Windows qualification are deferred; subsequent
releases add one language each. Ordinary Apple/Rust CI lanes remain visible and
are deferred from this scoped publication profile.

- Restricted Go helpers, external tests and string fixtures, the private Vitest
  issuer, complete inventories and transactional `native-init` share the same
  execution, receipt, audit and fallback coordinator. Independent source review
  and actual macOS/Linux normal/race CLI controls passed.
- The final matrix has 21 obligations: three current repository gates, eight
  current native gates and ten explicitly impact-qualified historical behavior
  gates. It records 304 mandatory native acceptance terminals and 164 measured
  CLI steps, with no required skips. Core normal/race each passed 2,030 terminals
  and record 204 optional skips; vet passed. Input guards match and owned
  processes and containers drained. See [release checks](release-checks.md).
- Qualified runtime contexts are macOS/Linux ARM64 with Go 1.27.1 and the fixed
  Linux ARM64 Node 22.18.0/TypeScript 6.0.2/Vitest 4.1.11/Vite 8.2.2 runtime from
  either controller. Unsupported syntax, effects, setup and invocation settings
  run the complete configured original scope. Historical coverage is unnecessary.
- Tiny acceptance fixtures run faster in full; measured qualification and reuse
  costs establish no representative speedup. Final staged Mori, signed local
  runtime source and independent matrix review passed. Four macOS/Linux archives,
  extracted ARM64 checks and independent artifact review passed. All 139 obsolete
  task payloads were removed, totalling 10,115,891,346 logical bytes (9.42 GiB),
  with release files and evidence retained. The ordinary archive builder excludes
  deferred Windows targets. AMD64 archives have build-only checks. Hosted CI and consumer acceptance are
  separate. This profile does not qualify arbitrary native Go/TypeScript or
  an AST/source-only proposal for CI omission.

See the [release plan](go-typescript-release-plan.md),
[supported-profile setup guide](go-typescript-native.md) and
[dependency kanban](affected-tests-kanban.md) for the exact completion scope.

## Shared execution foundations

- Shared analysis/runner composition, entrypoint/environment binding and normalized
  outcome validation are integrated into native execution and shadow comparison.
- Shared exact unit/case inventories now guard native passing receipts. Common
  grouping preserves original workspace scopes and only merges lifecycle/shared
  state couplings; its identities do not establish independence or qualification.
- Old/new invalidation also tracks changed unit metadata, dependency edges,
  input ownership and effect policies without requiring raw file changes. Reads
  retain influence when a changed ancestor alters path resolution.
- Normalized file/directory/probe effects supplement the common old/new graph;
  Go AST resource observations broaden proposals without qualifying native omission.
- `hayaku suite` can independently execute/reuse configured WASI groups, with
  complete per-group context binding and live invocation revalidation.
- Shared core/decoder scenarios and real Go/Node/Vitest/unittest/pytest fault fixtures
  exercise the integration boundaries. See [shared execution](shared-execution.md).
- A runner-neutral adversarial acceptance harness and independent Go, Vitest,
  worker, unittest, pytest, Cargo and SwiftPM full-command fixtures cover passing
  baselines and seeded failures. These fixtures do not qualify native omission.
- Internal Go closure APIs bind compiler-resolved imports/test variants, typed
  case identities, build/embed inputs and private source copies. Runtime,
  generated startup, dynamic cases and unsupported effects remain explicit gaps.
- Internal Vitest collection gathers cases separately from execution, retaining
  duplicate-name occurrences, parameterized cases, skips and todo cases. Import,
  setup, global setup, teardown and source-drift failures reject inventories.
- Vitest startup files and tests share configured transitive dependency capture.
  Installed Vite AST parsing checks both original and transformed code, resolves
  literal imports through the configured resolver, and broadens unresolved,
  computed, dormant or escaped loader influence explicitly. Parser availability
  and original TypeScript support are not assumed for older accepted pairs.
- An internal Node/TypeScript source candidate checks every declared module and
  lexical binding in a restricted synchronous numeric/Boolean subset, rejects
  dormant unsupported effects and bounds the direct-call graph. Pinned
  TypeScript 6.0.2 original/emitted structural comparison rejects emitter
  contradictions; source, parser/runtime tool bytes and private copies are
  rechecked. Mandatory installed macOS normal/race and Linux tests passed;
  combined repository normal/race/vet checks passed. Selected tool trust remains explicitly
  unqualified. This candidate is not a complete Vitest inventory, runtime proof
  or issuer, and cannot authorize cache reuse or omission.
- Native input envelopes operationally capture and enforce declared resources,
  absence, exact environment and bounded scratch. They cannot mint or reuse
  receipts: complete provider equivalence remains unqualified. Source and copied
  case/Unicode alias routing has actual macOS/Linux tests, including complete
  ownership across overlapping roots; unsupported copied views reject before use.
- A fixed observable policy rejects reported metadata, clocks, entropy and
  unknown effects before native cache access. Empty effect data
  cannot establish completeness; actual containment probes do not prove native
  equivalence. Independent native probes and full normal/race/vet checks passed.
- Native artifact authority is private to reviewed in-process producers.
  Public proof strings and inventories cannot issue it. Its binding includes
  captured source, compiler tree, artifact, inventory and backend context;
  caller-supplied result decoders cannot replace the producer's decoder.
- A controlled Linux child broker admits one fixed static helper launch with
  bound arguments/environment, copied resources, empty stdin and bounded output
  and cleanup. Actual protocol and capability probes passed. This operational
  envelope cannot issue receipts; arbitrary FFI and services remain unsupported.
- Internal Python collection binds installed interpreter/runner identities and
  independent pytest/unittest cases. Bounded captured source bytes supply import,
  fixture and dormant-effect evidence; unsupported runtime effects retain the
  original full scope. Source/tool/context drift rejects the evidence.
- Internal Cargo/Nextest closure APIs share configured metadata, producer
  records, private build artifacts and observed native cases. Compiler/runtime
  library trees and per-binary loader context are bound and rechecked. Build
  scripts, proc macros, custom harnesses, doctests and ambient/native effects
  remain explicit gaps; these descriptive APIs grant no omission authority.
- Internal CMake/Ninja closure APIs share a runner-neutral private build session,
  producer records, guarded source/tool/build identities and bounded native
  collection. Fresh CMake File API and Ninja dependency observations bind
  targets, settings and consumed headers; GoogleTest, Catch2 and CTest inventories
  retain configured lifecycle scopes. Actual macOS/Linux controls passed,
  including generated/header/compiler-alias drift and original fault outcomes.
  Full repository normal/race/vet integration checks passed. Unresolved SDK, initializer,
  linker, macro and runtime effects remain explicit gaps; these APIs grant no
  runtime qualification or receipt authority.
- Authenticated cache key/receipt reads now use confined nonblocking opens,
  admitted-size bounds and pre/post inode/full metadata checks. Actual macOS
  race and Linux stale-FIFO and post-read drift tests passed. Combined normal,
  race and vet checks passed for the final shared-worker composition. This does not add portable CI authority.
- Shared input-envelope capture rejects regular-file replacement by a FIFO
  through nonblocking opens. Independent macOS/Linux tests verify rejection
  without a writer and preserve captured byte accounting.
- WASI input capture also uses confined nonblocking opens, admitted-size read
  bounds and cancellation checks. Independent macOS/Linux tests challenge FIFO
  substitution and detectable preflight and post-read file/metadata drift.
- Shared snapshot source reads now use nonblocking opens, an admitted-size
  buffer bounded to 64 MiB, growth detection and descriptor/name metadata guards.
  The Go collector delegates its source reads to this common reader. Independent
  macOS/Linux whole-package and collector controls passed; combined repository
  normal/race/vet checks passed. Hashing retains its separate streaming bounds.
- The shared private build session can observe a completed native nonzero exit
  while sealing its generated reports, preserving exact native status and output
  limits. Cancellation, overflow and source/tool/build drift remain unusable.
  Existing successful-build calls retain their strict behavior. Independent
  macOS/Linux controls and combined repository normal/race/vet checks passed.
  These APIs grant no inventory, runtime qualification or receipt authority.
- Snapshot file hashing streams through a fixed buffer with bounded growth
  detection, regular-file identity checks and final file/directory drift checks.
  Independent macOS/Linux tests cover allocation bounds, FIFO replacement and
  Git SHA-1/SHA-256 validation. These observations do not provide an atomic
  filesystem history; directory-name enumeration allocation is outside this fix.
- Node runtime dependency hashing and copying now share nonblocking regular-file
  opens, admitted-size streaming and descriptor/name metadata guards. Ten actual
  macOS/Linux controls passed, including stale FIFO substitution, growth,
  same-byte inode replacement, typed cancellation, a public fingerprint golden
  and cleanup after actual staged module bytes. Combined repository normal/race/vet
  checks passed. These per-file observations do not provide an atomic tree history or
  qualify any Node runtime for skipping.
- Unix commands now cancel their process groups, including descendants holding
  pipes after their parent exits. Windows and children that leave the group need
  separate enforcement; process cleanup is not a runtime isolation proof.
- `hayaku native` adds actual Linux Go package execution/reuse for a restricted,
  statically checked pure subset, with Docker/Landlock/seccomp enforcement,
  shared authenticated receipts and full original-host fallback. See
  [native groups](native-groups.md).
- An explicit macOS pure-Go candidate uses a fixed installed system sandbox
  profile, bound kernel/OS/boot/loader context and a fresh enforcement probe.
  Independent actual boundary, authority, cache and audit controls passed normal
  and race tests and vet. Final combined repository checks and the Linux
  worker regressions passed.
  Resource envelopes and additional macOS language runtimes remain unqualified.
- Shared immutable static Linux program capture now uses the same executable
  validation and resource transport as controlled helpers. Independent actual
  C and Rust probes cover file/process/network enforcement, exact output limits,
  aggregate inputs, cancellation and source/copy drift. Raw operational results
  have no decoder, cache, proof or receipt authority. Final composition checks
  passed.
- Restricted dynamic Linux transport captures the installed ELF loader and
  library closure, enforces executable mapping provenance, and rejects JIT and
  unclosed loader modes. Independent actual C and configured jitless Node
  boundary tests passed normal/race/vet, including typed cancellation, drift,
  output limits and cleanup. Whole repository normal/race/vet composition passed.
  Manual-loader startup differs from ordinary Node startup; these raw results
  grant no runtime equivalence, provider qualification or receipt authority.
  See [operational dynamic transport](native-groups.md#operational-dynamic-program-transport).
- Captured Linux native addons use a separate executable role with closed
  loader/library dependencies and live source/copy/context checks. Actual NAPI
  and Rolldown controls and full normal/race/vet composition passed. Resource
  no-executable restrictions remain; this adds no user FFI or Node/Vitest receipt
  authority.
- Restricted pure-Go qualification accepts complete internal/external test
  namespace maps with exact combined cases and unchanged private issuer guards.
  Actual macOS/Linux normal/race source, failure, skip, drift, audit and cache
  controls passed, followed by full repository normal/race/vet composition.
  The earlier external-test corpus did not qualify helpers/setup; the separate
  Go/TypeScript candidate above adds its reviewed restricted helper/string-input
  contracts and still requires final composed release acceptance.
- SwiftPM/Xcode closure APIs now bind compiler bytes, source/resources, build
  producers and private test products with fresh pre/post invocation checks.
  Actual installed normal/race tests passed within original fixture deadlines.
  SDK metadata observations, unsupported macros/plugins and device effects
  remain gaps; these APIs grant no runtime qualification or receipt authority.
- Shared portable Ed25519 receipt authentication supports explicit protected
  issuer/verifier roles, namespace/epoch/key rotation and receipt-only transfer.
  Independent macOS/Linux tests and full composition checks passed. Hosted CI
  trust and distributed freshness remain separate gates. See [portable receipts](portable-cache.md).
- Shared generated-input stages now capture independent BASE/candidate source,
  tool, invocation and bounded output provenance, regenerate candidate products
  before consumers and join generated changes to common invalidation. Independent
  macOS/Linux fault controls and full composition checks passed. Unclosed host
  generator effects retain complete original consumer scopes, including no-change
  comparisons. See [generated inputs](generated-inputs.md).
- Failed configured audits retain their admitted cache root/key and observe
  actual new cache creation. Bounded cleanup revokes authenticated copies in the
  provider domain on early validation, cancellation or later group/output failure,
  preserving foreign domains and typed primary errors. Independent macOS/Linux
  controls and full composition checks passed. Undecodable arguments or missing
  trusted policy fail before safe cleanup admission.
- General native affected-only qualification across imported packages,
  additional language certifiers and representative consumer CI savings
  remain unfinished. Original native plan gates remain full.

The scoped local release milestone above does not establish production consumer
acceptance or qualification for the additional contexts described here.

The dependency-linked [affected-test execution kanban](affected-tests-kanban.md)
tracks the remaining shared engine, language/runtime qualification and end-to-end
acceptance work, with agent ownership rules and completion gates. Its planned
cards do not authorize native omission or change the capabilities listed here.

## Five improvement areas

| Area | Implemented scope | Remaining boundary |
| --- | --- | --- |
| Enforced execution | WASI deterministic interface; restricted native Linux Docker/Landlock/seccomp and explicit macOS system-sandbox pure-Go contexts, exact args/env and real enforcement probes | General native equivalence, metadata/clocks/entropy virtualization, arbitrary services/FFI/subprocesses and Windows remain unqualified |
| Actual reuse | Locally authenticated passing receipts bound to module, normalized inputs, producer, context, limits and backend; forced audits invalidate old receipts first | Windows ACL backend, distributed authority and general native affected-only execution |
| Generated and linked inputs | Explicit multi-root envelope capture, bounded verified materialization and independent BASE/candidate producer provenance; generated changes join common invalidation | No candidate-generated overlay into BASE; no broader native generated/linked runtime qualification |
| Independent challenge/evidence | Existing fault corpus plus capsule capability/cache/audit adversaries, native inventory/result reconciliation and strict JUnit/TRX/Swift/libtest imports | Finite fixtures are not a soundness proof; more real incident and external-state workloads remain |
| Honest performance | Per-suite executed/Hayaku-reused/upstream-cached evidence, phase costs, exact-context net comparisons and a self-pilot including capture/compile | Representative consumer savings, cold/warm workload policy and production acceptance |

## Runner capabilities

| Integration | Implemented evidence | Assurance |
| --- | --- | --- |
| Go | Native package/import/embed/build inputs; execution and shadow challenges; separate restricted package reuse on macOS/Linux, including compiler-selected pure local helpers, external tests and string fixtures | Ordinary native plans remain full; cgo, concurrency, dynamic setup and unknown external influence run original scopes. Local composed native gates passed; corrected v0.6.2 hosted gates remain pending |
| Vitest | Four exact Vitest/Vite pairs for ordinary evidence; fixed 4.1.11/8.2.2 private file reuse with Node 22.18.0 / TypeScript 6.0.2 and complete runtime closure | Custom Cloudflare/VM/browser/typecheck contexts remain unqualified. Local private issuer/CLI acceptance is carried forward with unchanged qualified contexts; v0.6.2 hosted gates remain pending |
| Node test, unittest, pytest, Jest, Playwright | Version-gated native collection/execution, whole-file/module proposals, strict terminal outcomes | Every workspace input conservatively owns every unit; browser/services/runtime completeness unqualified |
| Cargo | Offline, locked package graph and package commands; Rust result imports | Build scripts, proc macros and external runtime influence unqualified |
| cargo-nextest | Pinned 0.9.146 configured binary/case inventory, Cargo package proposals and real JUnit fixture | Full original execution retained; automatic native shadow/case-reconciliation integration remains future work |
| SwiftPM XCTest | Installed Darwin Swift 6.0–6.4 target graph, source/resource/tool and observed private build-product closure, conservative proposals, independent cases and serial outcomes; shadow comparisons | XCTest only; full suite required, no parallel/plugin/macro/binary support |
| Xcode XCTest | Observed private build/test-product and tool closure, independent full enumeration and installed Xcode 26.3/27.0 xcresulttool case/summary reconciliation | Full single-plan/device/configuration scope; no filtering, repetition or Swift Testing |
| Internal CMake API: GoogleTest, Catch2, CTest | Internal observed build/header/configuration closure, bounded native inventory and private producer binding with actual macOS/Linux controls | Full original configured scopes; SDK/linker/runtime gaps remain, no omission authority |
| JVM, .NET, Ruby, PHP, Dart, C/C++, Bun, Deno, Bazel | Explicit original full-scope command entries and applicable strict report imports | No claim of native framework discovery, filtering or runtime qualification for these entries |
| Generic command | Preserved original structured command and exit status; optional verified installed Node dependency tree | Full scope only; custom Cloudflare pools stay generic |

`hayaku catalog` lists registered integrations; the internal closure APIs above
are not registered CLI adapters. Report formats may require a runner-specific reporter/export step; an entry does not imply that every runner emits that dialect directly. [Runner support](runner-support.md) records exact fixture versions separately from accepted protocol ranges.

The new independent Vitest collection path has actual installed-runner evidence
for 4.1.11/8.2.2 only; other pairs are rejected by this new collection decoder.
The newer AST/transitive-closure application aggregate passed in 712.177s on
2026-10-06 with original per-case limits and no concurrent heavy builds: five
shared Vitest scenarios and eleven dedicated application checks. Its source
snapshot preceded only subsequent legacy SSR routing and older-pair assertion
changes; fourth-pair behavior stayed unchanged. A historical graph aggregate
passed in 680.095s. A prior run
timed out one resource scenario under concurrent checks; it is retained as
historical failed evidence.
Renewed macOS configured-graph adapter acceptance passed on all older pairs;
a linked dependency tree outside the captured workspace is rejected rather than
treated as complete evidence.

## Existing foundation

Exact local Git revisions, old/new graph union, deleted/renamed/new input handling, structured argv/cwd, bounded process output/cancellation, strict schemas, immutable source copies, context/tool digests, full fallback reasons, setup without overwrite and portable archives remain supported. Native Vitest traces broaden proposals monotonically and cover observed dynamic reads/probes/directories. Unobserved influence remains a gap and cannot authorize skipping.

The source includes pinned development fixtures and CI matrices. Local fixture success, cross-compilation, remote release acceptance and consumer CI qualification are separate claims; see [verification](verification.md), [release checks](release-checks.md) and [pilot results](pilot-results.md). Published release notes describe bounded implemented capabilities, not completion of every language/framework on the catalog.

## Isolated development acceptance progress

The approved [runner provisioning](runner-provisioning.md) checkpoint records
40 of 40 original framework/platform healthy and runtime-fault baselines: twenty
on each of macOS and Linux. Linux Flutter uses a derived ARM candidate built from
verified official framework, engine and package components, rather than an official
prepacked ARM SDK. PHP PHAR controls do not cover
Composer autoload. These records qualify no discovery, affected execution or
cache reuse; the corresponding language/runtime/acceptance cards remain open.

Apple closure, portable receipts, generated-input provenance and failed-audit
cleanup passed independent review and full repository normal/race/vet composition
checks. Generated producer influence remains unqualified and retains original full
consumer commands. The [kanban](affected-tests-kanban.md) records file ownership,
dependencies and outstanding runtime/acceptance work.
