# CI integration and rollout boundary

`scripts/ci-shadow.sh` is a local reference integration. Set HAYAKU_BASE to a
locally available comparison commit and HAYAKU_CANDIDATE to the exact tested
commit. Select a fresh artifact directory under Git metadata for each matrix job.
Keep original tests, lint, security, migrations and other gates in their normal
pipeline. The script never makes a skipped/full failing suite green.

Shadow defaults on; HAYAKU_FORCE_FULL=1 suppresses the additional proposal challenge
and still executes the full required suite. It cannot enable omissions. Native
Go shadow reconciliation currently requires an all-Go workspace configuration;
other ecosystems can retain their original full runner alongside observational
plans until reconciliers and installed dependency inputs are qualified. `run`
rejects ignored installed/build inputs, including ordinary `node_modules`; do not
weaken that execution contract to force a checkout through it. A planning/discovery
failure fails the integration rather than asserting that no tests are affected.
An owner may run their original full command to recover
without treating the invalid plan as a successful selector.

Do not infer the base from a branch name, PR title, HEAD~1 or an unavailable remote.
A shallow checkout must provision the desired objects through normal CI checkout
policy; Hayaku performs no fetch. Save plan, shadow/full outcomes, exact source and
runner context with artifacts. Do not save secrets or raw diagnostic/source output.
Artifacts are exclusive-created and should be access controlled.

Saved plans bind all inherited and configured environment inputs privately. Shell
launch bookkeeping can change between processes, particularly SHLVL and
XPC_SERVICE_NAME on macOS. The Hayaku Darwin pilot configuration explicitly pins
those two inputs for discovery and execution. For other runners, establish a
consistent job environment or declare reviewed values in context.env; Hayaku does
not silently exclude arbitrary environment variables from validation. Reports name
changed plan fields without exposing their values.

Production skip rollout is not enabled. Before adding it, review an adapter-specific
sound algorithm and enforced input/isolation boundary, fix all shadow misses, define
workload and net-cost thresholds, qualify native contexts, and retain scheduled
full-suite audits and immediate force-full rollback. A mutation score or a clean
sample cannot replace this boundary. Public release workflows verify and publish
Hayaku itself; they do not enroll consumers or authorize test omission in their CI.

## Historical external consumer baseline

During private prepublication development, a separate consumer received a locally
committed command-adapter configuration, reviewed local binary pin, full-suite
baseline wrapper and prepared CI artifact hook. It retained the exact original
npm/Turbo full test command and all other CI gates, recorded planning/runtime/full
result costs and enabled no omissions. Planning failure still ran the original
suite and failed the added planning gate. The wrapper used an ordinary installed
and generated checkout; it was explicitly an observation outside Hayaku's immutable
execution and native shadow qualification.

The real planner and eleven adversarial hook tests passed. On the pinned Node 22
major, all thirteen Turbo tasks passed with caching forced off; a Node 26 run
failed frontend storage assertions and correctly remained failed. Local observed
planning overhead was 21.206 seconds and full-suite cost 63.920 seconds, so this
baseline added work and claimed no selection savings. These observations used
private development revisions, not the public v0.1.0 source or downloaded release
assets. The consumer source and raw reports are not published; this is a historical
summary, not a reproducible public dataset or live CI acceptance record. No remote
consumer workflow activation or public installation was performed for that trial.
