# Stryker: selection evidence, mutation quality, and Hayaku

Researched 2026-09-30. This is a design note, not an implementation or runtime validation. Primary documentation was read live; source revisions are pinned below. Documentation pages generally do not expose a publication date, so the date above is an access date.

## Main finding

**Inference:** Stryker supplies useful mechanisms and a quality audit strategy, but its coverage optimization and incremental cache do not establish a universally safe regression selector. Its immediate problem is to choose tests for a known synthetic mutation; Hayaku must account for arbitrary changes, including inputs outside source code. Strict Hayaku selection should retain all potentially affected tests and broaden execution whenever independence cannot be established.

The homepage identifies three implementations: JavaScript/TypeScript, C#, and Scala. Their shared product identity does not imply identical runner capabilities. [Stryker homepage](https://stryker-mutator.io/).

## What was directly verified

### JavaScript and TypeScript

**Fact:** `coverageAnalysis: "perTest"` collects which tests exercise each mutant during the initial run, then filters mutation runs to those tests. `off` runs all tests for every mutant; `all` identifies uncovered mutants without per-test filtering. Per-test selection assumes tests can execute independently and in different orders. Startup/static mutants require broad execution. `disableBail` obtains more complete failing-test information; default reporting can identify only the first failure. A generic command runner lacks the hooks needed for per-test analysis. [Configuration](https://stryker-mutator.io/docs/stryker-js/configuration/#coverageanalysis-string).

**Fact:** Incremental mode reuses previously measured mutation results after diffing mutant and test sources. It still performs a dry run for discovery and current coverage. Changes outside mutated and test files can escape invalidation, including dependencies, environment variables, and snapshots. Test-change precision varies: Jest, Vitest, and CucumberJS provide locations; Mocha/Tap report files; Jasmine/Karma report names; command mode reports no test details. `--force` reruns scoped mutants. [Incremental mode](https://stryker-mutator.io/docs/stryker-js/incremental/).

**Fact from source:** At revision `f2a49ff02437e3b7fe2682dba808ac93039895bf` (commit timestamp 2026-09-11), `mutantCanBeReused` returns true when coverage is unavailable. With coverage, unchanged killing tests can justify reusing a killed result, while newly covering tests trigger re-evaluation of a non-killed result. This is explicitly best-effort reuse, not a conservative rejection of incomplete evidence. [IncrementalDiffer, lines 394 onward](https://github.com/stryker-mutator/stryker-js/blob/f2a49ff02437e3b7fe2682dba808ac93039895bf/packages/core/src/mutants/incremental-differ.ts#L394).

Runner APIs distinguish initial collection from mutation execution and carry test filters, runtime/static activation, and environment reload capability. [TestRunner](https://github.com/stryker-mutator/stryker-js/blob/f2a49ff02437e3b7fe2682dba808ac93039895bf/packages/api/src/test-runner/test-runner.ts), [run options](https://github.com/stryker-mutator/stryker-js/blob/f2a49ff02437e3b7fe2682dba808ac93039895bf/packages/api/src/test-runner/run-options.ts), [capability interface](https://github.com/stryker-mutator/stryker-js/blob/f2a49ff02437e3b7fe2682dba808ac93039895bf/packages/api/src/test-runner/test-runner-capabilities.ts).

**Inference:** Hayaku should reverse the no-evidence reuse policy: incomplete instrumentation or invalid fingerprints must widen the selected scope. Borrow the separation of collection, planning, and runner execution, with a richer explicit capability contract.

### C# and .NET

**Fact:** The configuration distinguishes `perTest`, `perTestInIsolation`, `all`, and `off`. Isolated collection trades startup cost for more accurate attribution. Static constructors/initializers otherwise run against all tests. `--since:<committish>` restricts mutation scope using Git changes; changed test files affect their covered mutants. Baseline reuse provides a full report from partial execution, but appears under the documented experimental section, which warns about false positives and negatives. Missing baselines cause complete runs. VSTest is the default; Microsoft Testing Platform support is documented as preview. [Configuration](https://stryker-mutator.io/docs/stryker-net/configuration/).

**Fact:** Stryker's technical notes document practical test-identity hazards: parameterized cases can share identities, dynamic names can change under mutation, discovery can execute application code, and coverage between test events can be attributed to the next test. These notes describe specific VSTest/framework behavior; they are not a guarantee for all current versions. [Testing-framework technical reference](https://stryker-mutator.io/docs/stryker-net/technical-reference/testing-framework/).

**Inference:** A .NET adapter cannot be described merely as “C# supported.” Framework, runner platform, adapter version, parameterization, and effective filter granularity belong in its evidence and compatibility key.

### Scala

**Fact:** Stryker4s supports sbt, Mill, and Maven integrations. `test-filter` selects classes/suites through the build tool; command-runner mode does not support that option. The sbt legacy-runner option exists partly for static-mutant and framework compatibility problems. This prevents an unconditional claim that every Scala runner has the same static-mutant behavior. [Getting started](https://stryker-mutator.io/docs/stryker4s/getting-started/), [configuration](https://stryker-mutator.io/docs/stryker4s/configuration/).

**Fact from source:** At revision `c9093cfcbb959b563daf10076d6c6a7780807677` (commit timestamp 2026-09-23), coverage is represented as `Map[MutantId, Seq[TestFile]]`. The forked runner filters task definitions by fully qualified suite name; individual test definitions in reports do not imply individual-case execution filtering. The initial run executes twice to help distinguish static mutants. [CoverageReport](https://github.com/stryker-mutator/stryker4s/blob/c9093cfcbb959b563daf10076d6c6a7780807677/modules/testRunnerApi/src/main/scala/stryker4s/testrunner/api/CoverageReport.scala), [task filtering](https://github.com/stryker-mutator/stryker4s/blob/c9093cfcbb959b563daf10076d6c6a7780807677/modules/testRunner/src/main/scala/stryker4s/testrunner/TestRunner.scala#L38), [initial runs](https://github.com/stryker-mutator/stryker4s/blob/c9093cfcbb959b563daf10076d6c6a7780807677/modules/core/src/main/scala/stryker4s/run/testrunner/ProcessTestRunner.scala#L72).

**Inference:** Hayaku must record the smallest *executable* selection unit and round its plan outward to that unit. Reporting finer identities than a runner can select must never drop sibling tests.

## Mutation score is useful, but measures a different question

**Fact:** Stryker's score is `detected / valid * 100`, with killed and timed-out mutants counted as detected. Surviving and uncovered mutants are undetected. Compile errors, runtime errors, and ignored mutants do not contribute like valid mutants. Consequently, score alone loses information about excluded scope and failure modes. [Mutant states and metrics](https://stryker-mutator.io/docs/mutation-testing-elements/mutant-states-and-metrics/).

**Fact:** Static mutants can execute before test attribution is available, need fresh environments, and limit filtering. The shared static-mutant page lists different defaults by implementation; the Scala legacy-runner configuration adds a runner-specific exception. Prefer pinned runner evidence over treating that table as a universal capability declaration. [Static mutants](https://stryker-mutator.io/docs/mutation-testing-elements/static-mutants/).

**Inference:** Even a 100% mutation score covers a finite generated fault model, not every possible defect or configuration change. A surviving mutant may expose weak assertions or may be behaviorally equivalent; it needs review. A stable score between two runs also cannot establish that the same mutants were detected. Treat mutation results as diagnostic evidence and selector qualification, never as proof that arbitrary excluded tests are irrelevant.

## Proposed use in Hayaku

The following are design proposals, not claims of Stryker functionality:

1. **Use a conservative dependency envelope.** Combine language/build dependencies, test ownership, configuration/data/resource inputs, and measured test influence. A previous coverage map alone cannot establish that a newly changed input has no influence. Unknown edges widen to a package, target, integration group, or full configured suite.
2. **Declare capability boundaries.** An adapter reports discovery precision, stable identity support, executable selection unit, setup/teardown attribution, subprocess/async coverage, dynamic resource handling, and filter verification. Missing capabilities preclude corresponding reductions.
3. **Fingerprint evidence completely.** Include base/head revisions, source and test contents, shared fixtures/helpers, generated artifacts, lockfiles, compiler/runner/plugin versions, build flags, supported environment inputs, and selected target/runtime. Reject partial or stale maps. Explicitly state which external state remains outside the supported envelope.
4. **Keep collection outside the critical path when possible.** Stryker's dry run already executes the baseline tests; wrapping it around every CI plan would erase much of the intended saving. Populate Hayaku evidence during already-required full runs and qualified collection runs, with exact revision and environment matching.
5. **Use mutation testing to challenge the selector.** For each valid synthetic fault, compare the full configured suite with Hayaku's selected suite in the same deterministic environment. Track mutant identities detected by the full suite but missed by selection; do not merely compare aggregate scores. Include startup faults, fixture/data changes, discovery changes, and cross-module edges in separate adversarial cases, because normal operator mutations do not cover the whole change space.
6. **Separate mutation audit cost from fast regression feedback.** Run targeted mutation checks for changed behavior plus periodic broader audits. Any discovered selector miss invalidates the applicable evidence/capability claim and forces broad execution until repaired. Mutation audits and historical zero misses are empirical validation, not mathematical certainty.
7. **Verify command execution.** Export exact argument arrays and intended IDs/units, then compare actual runner-discovered/executed units. A zero-test successful exit, omitted parameterized case, or unmatched ID must not count as successful validation.

## Meaning of “100% certain”

**Proposed contract:** Hayaku can be deterministic and conservative under an explicit supported execution model. It should promise to omit a test only when that model and complete evidence establish independence; uncertainty produces broader execution. This may occasionally mean everything runs. It should not promise the smallest possible exact set across arbitrary reflective, stateful, external-input programs.

There are two distinct obligations: preserve the outcome of the full configured regression suite, and ensure that suite is strong enough to detect relevant defects. Conservative selection addresses the first; mutation analysis helps evaluate the second. Neither establishes that the full suite detects every real-world defect.

## Coverage gaps

No Stryker package was installed and no workload was executed. Source inspection verifies intended paths, not runtime behavior. Current documentation was not checked against every released runner version. The full dependency invalidation behavior of each implementation was not audited. Stryker4s incremental-result caching was not established in the inspected pages/source; no absence-of-feature claim is made. Initial Hayaku adapters still require adversarial qualification fixtures and actual execution readback.
