# Pilot observations

## Separate WASI self-pilot, 2026-10-03

A clean public-development source commit
`fc1ed887ca456a9e644924e344c591c38084adee` ran `hayaku pilot` for
`./internal/graph` on Darwin arm64 with Go 1.27.1. This is the separate
deterministic Go/WASI contract, with one executed passing baseline and one
actual authenticated reuse. It does not establish equivalent native Go behavior
or affected-only execution for DocPulse. The original full Go test/race/vet gates
also passed independently.

Both legs captured the same committed source, used independent fresh compiler
caches, compiled the same WASI module, and matched every measurement context.
Compiler preflight, source capture, build, capsule execution/reuse, final validation
and cleanup are charged. CLI startup/JSON output and CI artifact upload are outside
these paired timers. No upstream cache hit is credited to Hayaku. The
[raw report](https://github.com/Cyberlane/hayaku/blob/v0.4.0/docs/pilot-wasi-v040.json)
contains only bounded metadata/digests and measurements.

| One paired observation | Baseline seconds | Reuse seconds |
| --- | ---: | ---: |
| Capture and compile | 10.952494 | 10.883942 |
| Capsule phase | 0.286322 | 0.021156 |
| Final validation and cleanup | 0.235876 | 0.245840 |
| Complete measured wall time | 11.474692 | 11.150938 |

The observed net saving was **0.323754 seconds**; gross runner cost avoided was
0.286322 seconds. Compilation dominates this small suite, and overhead variation
contributes to the difference. One shared-desktop pair is not a representative
CI saving, statistical confidence interval or performance threshold. Negative
savings remain valid results; the tool never rounds them into a win.

A second local acceptance pair used the **downloaded public v0.4.0 CLI** against
its clean tagged source `941d99d8ad5b0b31eb04bfcf1f9212966f8f4a62`.
Both functional outcomes passed and reuse was authenticated, but total cost
**increased by 0.178009 seconds**: 8.359275 seconds executed versus 8.537284
seconds reused. Capture/compile alone took 7.931660 versus 8.270836 seconds.
The [downloaded-CLI raw report](https://github.com/Cyberlane/hayaku/blob/main/docs/pilot-downloaded-v040.json)
retains that negative net result. This illustrates why small-suite runner reuse
alone is insufficient to claim a CI win; the two source/artifact contexts must
not be pooled into a matched performance comparison.

DocPulse's required baseline retains the complete original command and Turbo
policy. Advisory diagnostics are opt-in so routine CI avoids planning/provisioning
cost. Its independently forced-fresh local full command passed all 13 Turbo tasks
with zero cache hits in 106.279 seconds; that is local verification, not Hayaku
selection savings or native suite qualification.

## Historical native exploratory pilot, 2026-09-30


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
