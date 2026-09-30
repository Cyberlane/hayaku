# Predictive test selection and the certainty requirement

Research date: 2026-09-30. Scope: the requested Meta publication, its primary paper, and selected primary work on safe regression test selection. Research and design notes only; no implementation or CI change.

## Meta: directly verified evidence

The [requested publication](https://research.facebook.com/publications/predictive-test-selection/) was retrieved with installed Defuddle; the web reader redirected to Facebook login. The full [paper, arXiv v2](https://arxiv.org/pdf/1810.05286v2), is dated 2019-05-29; [v1](https://arxiv.org/abs/1810.05286) was submitted 2018-10-11. These are historical results, not a verified description of Meta's current infrastructure.

- Strict build isolation makes the build dependency graph a conservative candidate set (§I).
- A gradient-boosted classifier scores change–test-target pairs using historical outcomes; selection combines a score threshold with top-ranked targets (§IV).
- Reported recall exceeds 95% for individual failing targets and 99.9% for faulty changes. Selection is below one third of dependency-selected targets; infrastructure cost halves (§VI). Change recall requires finding just one failure on a faulty change, not every affected test (§III).
- Full dependency-selected learning runs on sampled changes support calibration; training uses three months of outcomes, latest-week validation, and weekly refresh (§IV).
- Failed targets receive up to ten attempts; mixed outcomes are classified as flaky (§V).
- All tests still run during periodic stabilization (§II).

Interpretation: the reported “guarantees” are empirically calibrated recall targets. The paper explicitly permits missed failures and limits how long measured performance can be assumed stable (§III, §IV-D). It does not establish universal 100% selection certainty.

The [Meta engineering article](https://engineering.fb.com/2018/11/21/developer-tools/predictive-test-selection/), dated 2018-11-21, provides an accessible primary explanation: probabilistic selection accelerates developer feedback, while exhaustive testing remains before production deployment.

## Safe selection: directly verified evidence

[Rothermel and Harrold, A Safe, Efficient Regression Test Selection Technique](https://www.cs.purdue.edu/homes/xyzhang/fall07/Papers/p173-rothermel.pdf), ACM TOSEM, April 1997, pp. 173–210: §3.1.2 proves safety under controlled regression testing, meaning other behavior-affecting conditions remain equivalent between executions. The algorithm compares old/new control-flow graphs and prior test traversal information. It can include extra tests. The theorem concerns retaining fault-revealing tests from the existing suite; it does not prove adequate testing of new functionality. Its setup assumes reusable, non-obsolete tests that terminated correctly on the old program (§3). The paper says there is no general effective procedure to identify precisely all fault-revealing tests.

[Gligoric, Eloussi, and Marinov, Practical Regression Test Selection with Dynamic File Dependencies](https://users.ece.utexas.edu/~gligoric/papers/GligoricETAL15Ekstazi.pdf), ISSTA 2015: Ekstazi records per-test file dependencies and checksums, including executable classes and external resources. Missing dependency records select the test; unsupported cases run everything (§3). The authors report 32% average end-to-end time reduction across 615 revisions of 32 Java projects (§1). This includes dependency collection, unlike a test-execution-only metric. Crucially, §5 acknowledges that a nondeterministic test can have dependencies absent from its observed run, and such a dependency change may be missed. Its historical broad safety claim should therefore be read with the execution assumptions and later proof work.

[Mansky and Gunter, Safety of a Smart Classes-Used Regression Test Selection Algorithm](https://www.sciencedirect.com/science/article/pii/S1571066120300402), 2020-09-15, DOI [10.1016/j.entcs.2020.08.004](https://doi.org/10.1016/j.entcs.2020.08.004): the primary abstract describes machine-checked safety proofs over a Java/JVM subset with dynamic class initialization. It also identifies historical instrumentation placements in Ekstazi that collected too late, and proposes corrections. This is evidence that collection completeness needs justification, not evidence that the current Ekstazi release has those historical defects. The corresponding [Archive of Formal Proofs entry](https://isa-afp.org/entries/Regression_Test_Selection.html), dated 2021-04-30, provides the proof development and names its JinjaDCI semantics boundary.

Reading limit: the 2020 publisher full-text reader failed; its primary abstract and the associated formal-proof entry were verified. No current Ekstazi implementation audit was performed.

## Proposed Hayaku contract: original design analysis

The useful requirement is **sound selection within an explicit supported model**, not a percentage confidence score. Let `T` be all discovered tests, `A` tests whose relevant behavior may change, and `S` selected tests. Require `A ⊆ S ⊆ T`; optimize the size and execution cost of `S`. Requiring `S = A` for arbitrary repositories would demand exact behavioral knowledge we cannot generally compute. Define what counts as observable behavior before claiming safety: assertions alone, process exit, side effects, ordering, timing, and external interactions are different contracts.

Cross-language support should mean a common decision protocol with separate evidence-producing adapters. A parser that recognizes a language is insufficient to certify its dependency model. An adapter should declare its supported framework versions, discovery mechanism, execution granularity, dependency types, collection semantics, and unsupported features. A shared engine can traverse conservative dependency edges and enforce widening rules. A narrow trusted isolation boundary can contain a fallback; if independence outside that boundary is unestablished, widen to the entire suite.

Each emitted plan should record the exact compared revisions, baseline evidence identity, runner/configuration identity, discovered test universe, selected test IDs, executable plus argument array, working directory, selected-set explanation, exclusions and their evidence, fallback reasons, and the limit of the assurance claim. Printing a plausible command is insufficient: verification must establish that the installed runner interprets it as the intended selection. Keep parameterized cases, setup/teardown, shared fixtures, and test order inside the chosen execution unit when individual-case independence is not established.

Unknowns should enlarge `S`, not lower a confidence threshold. Examples: absent baseline; ambiguous merge base; unsupported adapter; failed parsing/discovery; deleted or added dependency roots; generated-code provenance gaps; lockfile/compiler/runner/configuration changes; dynamic imports or reflection beyond the model; filesystem enumeration including previously missing paths; native/foreign-language calls; process-spawned code; services/database state; and shared test state. Hashing the previously read files alone does not establish that all future inputs are represented.

Past coverage can be valuable evidence when its collection is complete for the claimed semantics and inputs are controlled. It cannot simply replace a sound model with observed line intersections. Conversely, blanket claims that all dynamic selection is inherently unsafe would ignore the conditional proof literature. The practical question is whether the adapter's instrumentation and invalidation rules meet its declared assumptions.

ML can prioritize the already-required tests for faster failure feedback. Using predictions to omit required tests would introduce a separate probabilistic policy incompatible with the stated certainty requirement. Treat that as an explicit future discussion option, not the default.

## Proposed evaluation: original design analysis

Start in shadow mode: generate plans, then run the full comparison suite on the same candidate artifact and controlled environment. Measure omitted failing tests, faulty changes with every failure omitted, selection/discovery overhead, dependency collection/storage cost, build cost, end-to-end wall time, aggregate compute time, and fallback frequency. A test-count reduction alone does not establish CI speedup.

Deliberate fixtures should exercise hidden input boundaries and newly introduced dependencies. Compare mutation results between selected and full suites: a mutant killed only by omitted tests is a counterexample to the selector for that case. Also replay real historical regressions. Neither a finite set of successful shadow runs nor a mutation score proves universal soundness; they establish empirical evidence and locate implementation errors. The safety argument must remain tied to the model and its enforced assumptions.

Discussion choices: define the observable-outcome contract; choose the first language/framework adapter; decide acceptable fallback scope; identify evidence that establishes isolation; and decide whether CI needs a checked executable plan or command output only. The research supports a conservative MVP with transparent widening, then progressively narrower certified adapters.
