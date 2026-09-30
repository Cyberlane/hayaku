# Hayaku: affected-test selection research

Research date: 2026-09-30. These are historical design proposals written before
implementation. See the [current implementation status](../implementation-status.md)
and [v0.1.0 release notes](../releases/v0.1.0.md) for what now exists.

Hayaku should reduce CI work without introducing test-selection blind spots. The strongest defensible direction is **conservative affected-test selection under explicit, enforced assumptions**, with broader runs whenever those assumptions cannot be established. Universal automatic selection of exactly all and only affected tests across arbitrary programs is not a promise we can honestly make.

Start with [the proposed safety contract and architecture](2026-09-30-design-and-safety.md), then [the framework and command adapter survey](2026-09-30-framework-adapters.md).

The supporting investigations are:

- [Meta's predictive test selection](2026-09-30-meta-predictive-test-selection.md): what the paper actually guarantees and what Hayaku can borrow.
- [Stryker and mutation testing](2026-09-30-stryker-mutation-testing.md): quality measurement, mutation-specific selection, and incremental-mode limits.
- [Mori's architecture](2026-09-30-mori-architecture.md): current source evidence, reusable design ideas, and capabilities Hayaku still needs.

The main decisions for discussion are:

1. Interpret “100% certain” as never omitting a potentially affected test within supported, enforced execution conditions, accepting conservative extra tests and full-suite fallbacks.
2. Begin at build-target/package/test-file granularity; make individual-case selection a later adapter capability with a separate soundness argument.
3. Choose the first pilot: Go packages for a Mori-shaped repository, or a hermetic build graph for the strongest initial assurance boundary.
4. Decide whether Hayaku initially outputs plans only, or also executes and checks that the runner actually honored them. Output alone cannot establish execution success.

All source inspection was read-only. Only these research notes were created. Hayaku was an empty Git repository with no commits at the start of the investigation; no project runtime or CI existed to validate. No performance or universal-safety claim has been demonstrated.
