# Mori architecture: what Hayaku can reuse and what it must add

Research date: 2026-09-30. Local source reviewed at Mori commit `3563675191e6b59accf57871df993220bfeece44`; `git status --short` was empty. This is an inspection of source, documentation, and existing test cases, not a new build, benchmark, or test run. No Mori files were changed. Source links below are pinned navigation references assembled from the locally inspected files; remote availability of that commit was not tested.

## Finding

Mori is a useful model for language adapters, deterministic local analysis, explicit incomplete evidence, versioned machine contracts, and snapshot-bound caching. It is a structural similarity tool. Its current output cannot prove which tests a change affects, and its parser support does not establish support for a language's test frameworks.

Hayaku should adopt that discipline while building a different analysis contract: preserve changes and dependencies, enumerate the actual runnable test universe, and omit a test only under a validated dependency model. Where that model is incomplete, expand the selected test set to an affected target, package, project, or the full suite. This can preserve soundness under explicit supported assumptions; it cannot promise universal 100% knowledge of arbitrary program behavior.

## Verified pipeline

Mori's pipeline is bounded source discovery → language registry → native Tree-sitter parsing → comparison fragments → normalized feature bags → candidate pruning → weighted Jaccard → deterministic reports. The repository instructions explicitly say a score is never proof of semantic or behavioral equivalence. [Architecture](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/docs/architecture.md#L1-L20), [repository instructions](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/AGENTS.md#L3-L10).

The language registry associates concrete grammar IDs with review families, comparison domains, file extensions, interpreter names, grammar constructors, and fragment predicates. This is a good separation between discovery and syntax adapters; a family such as TypeScript groups TS and TSX without pretending they use the same parser. [Registry contract](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/internal/language/registry.go#L37-L104).

Tree-sitter gives grammar-specific concrete syntax, not a shared typed AST. Each parse creates and closes its own parser/tree, supports cancellation, and keeps warnings and candidate-boundary coverage distinct from valid retained fragments. Compatibility adaptations repair only bounded known syntax forms and preserve byte positions. An adapter must never silently turn unsupported syntax into evidence that a change has no impact. [Parser](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/internal/parser/parser.go#L31-L139), [parser limits](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/docs/reference/languages-and-parser-limits.md#L77-L118).

## Language coverage and its actual boundary

The registry has 24 concrete grammar IDs: Bash, C, C++, C#, Dart, GDScript, Go, Hack, Java, JavaScript/JSX, Kotlin, Lua, Luau, PHP, PowerShell, Python, Ruby, Rust, Swift, TypeScript, TSX, Zsh, generic SQL, and PostgreSQL. These are grammar entries, not 24 test runner integrations. Extensionless scripts use a bounded shebang read; `.h` defaults to C; SQL dialect selection is explicit; legacy `.php` Hack selection requires a recognized header. [Current capability table](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/docs/reference/languages-and-parser-limits.md#L1-L36), [registered ABI cases](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/internal/language/registry_test.go#L9-L65).

Code comparison generally operates on implemented function-like fragments. Consequential constructs can fall outside those boundaries:

| Existing Mori boundary | Consequence for Hayaku |
| --- | --- |
| Bodyless declarations are not scored; type-only syntax is excluded from normalization. | Signature, interface, type, overload, ABI and schema changes still require dependency and test analysis. |
| Swift computed properties, accessors and subscripts are not independent comparison units. | Do not equate zero comparison fragments with zero behavioral effect. |
| Rust item macro token trees are opaque and emit a coverage warning. | Macro expansion and generated tests need compiler/build evidence or conservative escalation. |
| C macro support recognizes specific syntax without running the preprocessor. | Include/header changes, preprocessing and build configuration require an independent model. |
| SQL extracts top-level DML queries; embedded SQL is a bounded, opt-in direct-Go-string feature. | Migrations, DDL, prepared query variables and database state can affect tests outside that extraction. |
| Vue, Svelte and Zig are outside the registered set; Flow is unsupported. | Supported-file coverage cannot describe total repository coverage. Unknown relevant inputs trigger escalation. |

Sources: [comparison boundaries](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/docs/reference/languages-and-parser-limits.md#L38-L75), [macro and unsupported-source limits](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/docs/reference/languages-and-parser-limits.md#L93-L118), [opaque and unsupported inventory](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/docs/reference/languages-and-parser-limits.md#L157-L164).

Mori's `--fragment-selection tests` classifies paths, conventional test filename suffixes, selected Rust attributes/conditional modules, and positive C/C++ `REDIS_TEST` branches. It includes helpers in conventionally named test files. Stories are not automatically tests; unclassified units remain on the production side. This is a comparison filter, not runtime test discovery, collection, parameter expansion, or executable test identity. [Exact classification contract](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/docs/reference/languages-and-parser-limits.md#L123-L155).

## Why similarity and fingerprints cannot decide omission

Mori intentionally maps identifiers to generic symbols and literal values to kinds. Its normalization also drops types and decorators. Ordered features and semantic operation hints improve structural comparisons but do not resolve arbitrary callee behavior, overloads, receiver types, external effects, or dependency edges. [Normalization](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/internal/normalize/normalize.go#L380-L448), [type/decorator exclusions](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/internal/normalize/normalize.go#L1050-L1082).

A particularly useful existing regression fixture compares `return "jpeg"` with `return "avif"`: their structural fingerprints are deliberately equal. Mori keeps separate literal digests for drift evidence, but that does not turn the structural identity into a behavioral identity or a complete dependency model. [Fixture](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/internal/normalize/normalize_test.go#L151-L174).

The feature-bag identity is a SHA-256 digest truncated to 64 bits, appropriate to Mori's stated bounded scan/display purpose. It is not a full input digest. More fundamentally, the normalized feature bag is deliberately lossy even in the absence of hash collisions. Hayaku must retain original source/build/resource content and complete relevant input digests for invalidation; equal Mori fingerprints must never authorize skipping tests. [Fingerprint implementation](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/internal/fingerprint/fingerprint.go#L15-L49).

An unchanged parent fragment also does not cover its nested functions: Mori replaces a nested body with a coarse nested-function feature and compares the inner fragment independently. A Hayaku change graph must retain containment and calls/dependencies between scopes rather than using a parent's comparison score as impact evidence.

Potential advisory use: highlight parallel implementations or duplicated test patterns across languages for human review. Adding candidates from that evidence can widen a test plan. Removing candidates from it cannot satisfy the requested certainty.

## Snapshot and cache patterns worth copying

Mori's Git resolver records requested base, resolved base commit, HEAD, merge base, changed paths, deleted paths, whole-file focus for untracked files, and changed line intervals. Git is invoked directly with bounded output and timeouts. Delete-only hunks are anchored near surviving lines for similarity focus; that anchor is useful presentation evidence, not a deleted dependency reconstruction. [Change model](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/internal/vcs/git.go#L22-L65).

The immutable index model records stage-zero mode/OID/path entries and hashes a snapshot. Staged source, configuration and ignore data are read from Git blobs rather than unstaged disk bytes. Hayaku needs the same input coherence, plus explicit CI base/head snapshots and both old and new graphs. A deleted or renamed input can invalidate dependents even if it no longer appears in the head graph. [Index model](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/internal/vcs/index.go#L16-L40), [staged architecture](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/docs/architecture.md).

Two caches serve different purposes:

| Cache | Verified binding and behavior | Hayaku application |
| --- | --- | --- |
| Parse cache | Reads current bytes once, hashes content plus path/language/domain/extraction options; cached results include fragments, warnings and coverage. Disk authentication additionally binds executable digest. Misses preserve normal parsing. | Reuse dependency extraction only with all adapter/compiler/configuration inputs bound; retain incomplete evidence on a hit. |
| Staged analysis cache | Binds exact snapshot, effective options, baseline digest, initial warnings, schema/normalization/hook revisions and actual executable digest. Coverage/receipt/exit policy still revalidates. Private HMAC authority is outside the checkout. | Treat cached plans as evidence for one exact environment/snapshot, never as transferable permission to skip tests. Separate analysis reuse from current policy evaluation. |

Sources: [parse cache](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/internal/parser/cache.go#L13-L99), [disk parse cache](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/internal/cli/parse_cache.go#L19-L111), [staged key](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/internal/cli/staged_cache.go#L100-L150), [cache trust and bounds](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/docs/guides/staged-cache.md).

CI reuse will require a deliberate remote-cache trust model; Mori's local per-user HMAC design does not itself define cross-runner provenance. A historical test-to-source map should also bind test enumeration, runner version/configuration, platform, build variant, instrumentation version, fixtures/resources and recorded external inputs. Old execution evidence alone cannot establish that new code takes no new paths.

## Recommended separation in Hayaku

This is a design proposal derived from the inspected boundaries, not an existing Mori feature.

1. **Inventory adapters** identify language/build/test roots and all relevant inputs, including unsupported source, build files, manifests, lockfiles, schemas, resources, fixtures and generators. Source-ignore conventions must not silently exclude behaviorally relevant inputs.
2. **Dependency adapters** expose compiler/build graph facts, module dependencies, test input traces or conservative static edges. Each adapter declares what it can prove, what it overapproximates, and its blind spots. Source syntax is one evidence source among several.
3. **Test runner adapters** enumerate stable executable tests or targets and translate a selection to structured executable/cwd/argv/env operations. A source fragment name is not an executable test ID. Runner version/configuration and filtering semantics belong in the adapter contract.
4. **Planner** takes exact base/head inputs, merges affected closures from old and new dependency information, includes changed/new tests and required shared setup, and widens selection when evidence is absent or unsupported. A sound target-level plan is preferable to an unjustified per-test plan.
5. **Verifier/reporter** records every selection reason, every unsupported input, assumptions, escalation, provenance and final commands. Report `subset` versus `full fallback` explicitly. A valid empty selection needs positive completeness evidence; missing data must not become an empty command.

Language and framework support should be separate capability dimensions. For example, Python syntax support does not establish complete pytest collection/plugin behavior; TypeScript syntax support does not establish Vite/Vitest transform or browser test dependencies. Cross-language boundary adapters should model RPC/API contracts, generated clients, FFI, database schemas and shared resources. Without a complete boundary, widen to all consuming targets.

Avoid a single unqualified `supported: true`. A useful capability report would distinguish source inventory, dependency extraction, runnable test discovery, supported build variants, command filtering granularity, and conservative fallback availability. Define a versioned adapter contract and evidence schema independently from the user-facing command rendering.

## Concrete reuse versus new work

| Reuse concept | New work needed for affected tests |
| --- | --- |
| Language registry and pinned grammar provenance | Build/semantic dependency providers and independently versioned framework adapters |
| Bounded discovery, safe path handling, cancellation | Behaviorally relevant non-source inventory and explicit ownership of all changed inputs |
| Immutable snapshots and NUL-delimited Git changes | Base/head dependency reconstruction; deletion/rename/generator invalidation |
| Deterministic schema-versioned JSON and separate incomplete status | Selection/omission rationale, guarantees under assumptions, structured command plans |
| Full input digests, private authenticated caches, miss fallback | CI evidence exchange, untrusted PR boundaries, environment/build-variant invalidation |
| Positive and nearby-negative fixtures, parser ABI tests | Adversarial selector fixtures, discovered/selected/executed test reconciliation, full-suite shadow comparisons |

Mori keeps most packages under Go's `internal/` tree. Hayaku cannot directly import those packages from a separate module under the ordinary Go internal-package visibility rule. Options to discuss are an audited shared library extraction, selective attributed reuse, or consuming Mori's public CLI JSON for optional advisory evidence. Its current CLI does not provide a dependency graph; using it as Hayaku's sole analyzer would retain the fundamental gap.

## Licensing and distribution boundary

Mori's own source is MIT licensed; copies or substantial portions retain its copyright and permission notice. Runtime/grammar dependencies have separate notices. The pinned dependency inventory lists MIT components plus BSD 3-Clause components, including the Go runtime and PostgreSQL grammar. Reuse of vendored grammar material should retain its provenance, license text, copyrights and checksum records. This section reports the repository's files rather than a separate legal audit. [Mori license](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/LICENSE), [third-party notices](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/THIRD_PARTY_NOTICES.md#L1-L35), [adding a language](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/docs/adding-a-language.md#L7-L25).

The Go/Tree-sitter path uses CGO. Native platform verification and the cost/size of bundled grammars are product decisions, separate from coverage claims. Mori's local-only/source-not-uploaded model is a useful default for Hayaku.

## Existing regression evidence and inspection limits

Inspected tests cover every registry grammar's actual `SetLanguage` ABI compatibility, cache preservation/invalidation, corrupt evidence rejection, snapshot/options binding, presentation-independent cache keys, receipt revalidation, Git old/new local state, deletion hunk handling, stage-zero snapshots, unmerged index rejection, and selected inline-test classification. They are useful patterns for Hayaku's own adversarial fixtures. They do not test affected-test soundness because Mori does not implement that function. [Registry tests](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/internal/language/registry_test.go#L9-L65), [parser cache tests](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/internal/parser/cache_test.go), [staged cache tests](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/internal/cli/staged_cache_test.go), [Git tests](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/internal/vcs/git_test.go).

Current source constants are report schema **23** and normalization **14**. Some overview prose still describes older schema numbers; source constants and the current machine contract take precedence. No installed Mori version, performance gain, remote publication state, or fresh native build was verified here. [Schema constant](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/internal/model/model.go#L6-L7), [normalization constant](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/internal/normalize/normalize.go#L17-L20), [machine contract](https://github.com/Cyberlane/mori/blob/3563675191e6b59accf57871df993220bfeece44/docs/machine-integration.md).

## Questions for the design discussion

- Should the first supported adapter select build/test targets conservatively, before attempting individual test IDs?
- Which small language/build/framework matrix is valuable enough to qualify exhaustively first?
- Is the initial contract limited to hermetic deterministic suites, with stateful/integration suites always selected unless their inputs are explicitly modeled?
- Should dependency evidence come first from native build tooling, with Tree-sitter as conservative supplemental inventory?
- Should shared Mori components become a separate module, or should Hayaku begin independent and reuse architectural patterns only?
- What evidence and supported assumptions must appear in each omission explanation, and which gap codes always require a full fallback?
