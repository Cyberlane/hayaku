# Hayaku first pilot

The first pilot uses Hayaku's own real package graph and full test suite. Other
projects follow after reviewing raw observations and input/service boundaries.
This local pilot is one controlled change, not a workload dataset or production
qualification. Full-suite authorization stays enabled.

Build the helper outside the repository after committing the implementation:

```sh
go build -o /tmp/hayaku-pilot ./cmd/hayaku-pilot
/tmp/hayaku-pilot --root "$PWD" --output /tmp/hayaku-pilot-warm --iterations 2 --cache-mode warm
/tmp/hayaku-pilot --root "$PWD" --output /tmp/hayaku-pilot-fresh --iterations 2 --cache-mode fresh
```

Outputs must be new directories outside the checkout with an existing parent.
The helper requires a clean exact HEAD and never edits the source checkout,
changes its branches, fetches a remote, or writes global Git settings. It clones
using a local transport without hard-linked objects, checks out the exact source
commit, and creates a candidate commit only inside that disposable clone. The
candidate reorders pure string comparisons in `qualification.isFailure`; the
boolean expression is equivalent for every input. Exact replacement tokens stop
the pilot if that source fixture changes. The helper is intentionally Hayaku-only.

## Execution and cache controls

Each pair uses an independent baseline clone and separate private native Go build
caches for baseline and proposal. The full baseline executes `go test -count=1
./...` with JSON reconciliation. Planning captures raw base/candidate trees and
discovers both dependency graphs. `app.Shadow` then executes the proposed packages
and full suite in separate raw-tree copies, using fresh test execution and keeping
the full audit gate. Missing outcomes, failures, context/source drift or observed
misses make the helper exit unsuccessfully; failure observations are retained.

`warm` primes each arm's private build cache with an untimed full suite before
measuring. Warmup time is recorded separately. `fresh` starts each arm with an
empty Go build cache; planning may populate the proposal cache, and that cost is
included. Neither mode promises cold filesystem, compiler, OS or hardware state.
Private cache paths give the two measurement arms different context digests;
their cache condition is explicitly controlled for performance comparison.
Shadow comparison itself uses the same fixed proposal context on both sides.

The measured order is planning, baseline, then shadow proposal/full. Default two
pairs are a small reproducibility check, not a statistical confidence interval.
Rerun on a quiet CI runner, alternate independent run order for a wider study and
collect cold/warm data over representative changes before setting pilot targets.
External tool installations and remote services are not provisioned. The Go core
disables dependency fetching and toolchain installation; native fixture tests
retain their own declared local execution boundaries.

## Recorded costs and review

Each output directory contains `report.json`, plus separate plan, baseline and
shadow JSON artifacts for each completed pair. Reports contain identities,
terminal outcomes, unit counts and timings; they do not retain raw test output,
source contents or environment values. Raw measurements cover clone/setup,
warmup, native baseline execution, complete plan construction, shadow command
execution, copy/validation overhead, artifact storage and cleanup. The total
observed process duration excludes final report serialization and terminal output.

The exploratory selected cost charges all planning, proposal command execution,
combined shadow copy/validation overhead and artifact storage. This deliberately
includes overhead associated with preparing both shadow copies, rather than
claiming an exact future production-selector cost. Full-audit command time is
reported separately. Common pilot setup/cleanup is reported, not attributed as
an asymmetric CI saving. Net savings may be negative. Medians summarize only
successful pairs; incomplete pairs and their errors remain visible.

Review the full and proposal terminal outcomes, package counts, gaps, context
identities and every overhead component before interpreting the median. A benign
change with zero misses does not establish selector soundness. Add real historical
changes, seeded failures, external input changes, startup/collection challenges
and multiple runner contexts before qualification. Each consumer pilot must first
identify its actual commands, languages, generated consumers, services and resource
contracts. No public CI rollout or production omission is enabled by this helper.
