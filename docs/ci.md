# CI integration and rollout boundary

Keep native tests, lint, security, migrations and other original gates. Native graph or collection evidence produces proposals for shadow comparison; it never grants an affected-only native CI run. Deterministic [WASI capsules](capsules.md) are a separate suite contract and can reuse a matching authenticated passing receipt.

## Native planning and shadow checks

`scripts/ci-shadow.sh` is a reference integration. Set `HAYAKU_BASE` to a locally available comparison commit and `HAYAKU_CANDIDATE` to the exact tested commit. Use a fresh artifact directory under Git metadata for each matrix job. Shadow defaults on; `HAYAKU_FORCE_FULL=1` suppresses the additional proposal challenge and still executes the original full required suite. It cannot enable omissions.

Native execution/collection support includes Go, the pinned [Vitest pairs](vitest.md) and supported [additional runners](runner-support.md). Native contexts remain unqualified for omission. Runner prerequisites, generated source and services that lack a supported immutable native boundary stay in the original pipeline. Planning failure must not become “no tests affected”; an owner can recover by running the original full command while retaining a failed planning gate.

Do not infer the base from a branch name, PR title, `HEAD~1` or an unavailable remote. Provision required Git objects through normal checkout policy; Hayaku does no fetch. Reports use exclusive-created files and should be access controlled. Save exact source/context identity, native full/shadow outcomes and costs without raw environment values, source contents or diagnostic output.

Saved plans privately bind inherited and declared environment inputs. Shell launch bookkeeping can vary between processes, particularly `SHLVL` and `XPC_SERVICE_NAME` on macOS. Establish a consistent job environment or explicitly pin reviewed values in `context.env`; Hayaku does not silently ignore arbitrary variables.

## Deterministic capsule gate

Provision and verify the actual WASI module through trusted build steps. Bind its compilation/source context in `producer_identity`, declare all guest inputs and use a private receipt directory. Invoke `hayaku capsule --config capsule.json --cache-dir PRIVATE_CACHE`; a failure, incomplete execution or unsupported capability fails this gate. Reuse does not replace any native gate.

Schedule `--no-reuse` audits. An audit invalidates the previous receipt before execution, so cancellation or failure cannot leave its earlier pass authorized. Changed audit output invalidates reuse. Retain the original native gates while evaluating the separate WASI suite and record matching-context [net costs](measurements.md).

The receipt authority key belongs to the invoking user. Do not publish it or share writable cache authority with untrusted jobs, forked pull requests or guests. Cross-user/cache provenance and Windows ACL qualification are outside the current contract; unavailable caching causes fresh execution. Diagnostic plans, imported reports and manifests are not pass receipts.

## Required development fixtures

The reusable CI workflow defines Linux amd64 and macOS arm64 full Go test/race/vet jobs, three exact Vitest/Vite fixture pairs, installed Node/Python/Jest/Playwright checks, and capsule/input-envelope/metrics/pilot checks. Development dependencies are explicitly provisioned outside the checkout; Hayaku itself installs no runners. Browser-free Playwright fixtures do not establish browser or service isolation. Native application fixtures that require explicit runtime variables skip without them; release CI must supply them and exercise the required cases.

The capsule pilot captures and compiles the same committed package independently for an uncached baseline and a reusable candidate. CI requires complete passing results and comparable metrics, not a minimum speedup. Remote job acceptance is recorded separately in [release checks](release-checks.md).

## Historical external consumer baseline

A private prepublication consumer trial kept its original npm/Turbo full command and all gates. It used an ordinary installed/generated checkout outside immutable native qualification. Eleven adversarial hook tests passed; on Node 22 all thirteen Turbo tasks passed with caching forced off. A Node 26 trial remained failed after frontend storage assertions failed.

Observed planning overhead was 21.206 seconds and full-suite cost 63.920 seconds. That trial added work and claimed no selection savings. These private development observations are a historical summary, not a reproducible public dataset, live consumer CI acceptance or a result for v0.4.0 release artifacts. The [current pilot](pilot-results.md) measures a different, separate WASI contract.
