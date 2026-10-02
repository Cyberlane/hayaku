# Measuring complete CI cost

`hayaku compare` validates matching-context measurements and reports net wall
savings, including negative savings. It does not grant reuse authority. Record
execution evidence from the actual backend; a caller-written measurement is not
an authenticated receipt or proof that a suite ran.

## Measurement schema

This example is illustrative measured data, not an observed benchmark. Both
files use this `Run` shape without a `schema` field:

```json
{
  "context": {
    "source_digest": "exact-source-identity",
    "inputs_digest": "exact-input-identity",
    "runner_digest": "exact-runner-identity",
    "environment_digest": "private-environment-identity",
    "platform": "linux/amd64",
    "suite_set_digest": "complete-suite-set-identity"
  },
  "wall_nanos": 1000000000,
  "phases": [
    {"id": "capture-and-plan", "kind": "hayaku", "start_nanos": 0, "duration_nanos": 100000000},
    {"id": "tests", "kind": "runner", "start_nanos": 100000000, "duration_nanos": 900000000}
  ],
  "suites": [{"id": "portable-suite", "execution": "executed", "outcome": "passed"}]
}
```

Phase kinds are `hayaku`, `runner` or `other`; phases are nonoverlapping measured
wall intervals. Unclassified wall time still counts. Do not sum parallel suite
durations as wall time. Suite execution states are `executed`, `hayaku-reused`
or `upstream-cached`, and outcomes are `passed`, `failed` or `incomplete`.
Hayaku reuse requires a passed outcome. Context identities must all be present;
identities are bounded strings, with producer-defined digest construction.

```sh
hayaku compare --baseline baseline.json --measurement candidate.json \
  --output .git/cost-comparison.json
```

Summary reports total wall cost, Hayaku overhead, runner cost and per-suite
execution/outcome counts. Net savings equal baseline wall time minus candidate
wall time. Gross runner savings are reported separately. Negative savings are
preserved instead of making added planning/reuse overhead disappear.

Comparison requires exact source/input/runner/environment/platform/suite-set
identity, the same complete suite inventory, matching upstream-cache states,
passing complete outcomes and a baseline with no Hayaku reuse. Context, inventory,
upstream-cache or completeness mismatch returns `valid: false`, a fixed reason
and no claimed savings, with an unsuccessful CLI exit. Malformed evidence is an
error. A Turbo/npm/Go cache change cannot be credited to Hayaku.

Limits are 24 hours per measured run, 50k suites and 10k phases. Duplicate IDs,
invalid states, overlaps, out-of-bounds intervals and overflow reject. Capture,
identity checks, planning, compilation, startup, evidence writing and cleanup
belong in measured costs; shadow's additional full run is a separate cost.

## Hayaku self-pilot

```sh
hayaku pilot --package ./internal/graph --cache-dir .git/hayaku-passes \
  --output .git/hayaku-pilot.json
```

The pilot requires a clean exact committed source and one normalized package
path; wildcard/all-package scope is rejected. It explicitly invokes an installed
Go compiler with offline module/toolchain behavior and builds a WASI test artifact
from independent snapshots. Both builds use independent fresh `GOCACHE`
directories; a candidate native compiler cache hit cannot masquerade as capsule
savings. The baseline audits/executes; a matching candidate may reuse its pass.

All timers include compiler identity, source capture, build, capsule execution or
lookup, final validation and cleanup. Output schema 1 has contract
`separate-deterministic-go-wasi-suite-v1`, baseline/candidate measurements,
`compiler_cache: "independent-fresh"`, comparison and both capsule results. Compilation/source context and module/backend
identity must match. The gate requires comparable complete passing results, not
a positive savings threshold.

This measures a small separate WASI graph suite. It does not establish native Go
equivalence, native skip eligibility, representative DocPulse/Kioku savings or a
consumer rollout threshold. Record actual sample conditions and distributions in
[pilot results](pilot-results.md); keep cold/warm comparisons and upstream cache
state explicit before using results to choose a production policy.
