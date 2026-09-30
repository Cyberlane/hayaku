# Hayaku local pilot, 2026-09-30

Hayaku is the first pilot. Other projects follow after the relevant
adapters and runner contexts qualify. This is a local synthetic observation on
a development machine, not production CI acceptance or permission to omit tests.

Measurements used a private prepublication development revision on Darwin arm64
with Go 1.27.1, rather than the public v0.1.0 source. The private source history
and raw reports are not published, so this summary is not a reproducible public
dataset or a measurement of the released version. The pilot made a benign
reordering of the pure `isFailure` expression in a private clone; the source
checkout and branch were unchanged.
Two warmed pairs compared the original full command with the experimental
qualification-package proposal. Both reconciled successfully, with 19 required
packages and one proposed package, no observed misses and no outcome conflicts.

| Median measurement | Seconds |
| --- | ---: |
| Original full-suite wall time | 15.621 |
| Exploratory selected cost, including charged planning/validation/storage | 11.638 |
| Paired exploratory net saving | 3.983 |
| Full-gated shadow challenge wall time | 26.033 |

Raw paired net savings were 4.201 and 3.765 seconds. Setup took 0.542 seconds,
cleanup 1.724 seconds and the complete pilot process 191.094 seconds, including
warmups and audits. The raw report was retained privately and is not published.
See [methodology](pilot-methodology.md) for all charged components, cache boundaries
and the independently retained full audit.

Two observations of one harmless change cannot establish representative savings,
statistical confidence or selection soundness. Native warm caching was preserved;
the machine was a shared development desktop, not a controlled CI runner. Archive
building finished before these measurements; ordinary metadata inspection still
occurred during the trial. A separate overlapping trial was cancelled and is not
included. Later context/path fixes and regressions are not in this measured source.

Production required commands still execute the original full suite. Shadow mode
adds validation work and currently costs more than the baseline. Representative
historical changes, real faults, high-impact/full-fallback cases, cold and warm
runners, enforced external-input isolation and agreed thresholds remain open in
HYK-014/HYK-016/HYK-017. The independent runtime-file-read counterexample already
demonstrates why native import graphs alone cannot authorize universal skipping.
