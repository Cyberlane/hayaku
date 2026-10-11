# Go and TypeScript stable release

Priority set by the user on 2026-10-10: finish Go and TypeScript end to end
before expanding to another language. Later releases add one language each.
macOS and Linux are the current platform scope; Windows remains deferred.

## Release outcome

One documented CLI workflow discovers the configured suite, executes affected
groups, reuses authenticated unchanged passing groups, and executes the complete
original scope whenever qualification is unavailable. Unsupported code must
produce a visible full fallback, never a false passing result or silent omission.
Every advertised scenario must pass the release acceptance matrix. Arbitrary
programs cannot be promised exact minimal selection.

The initial installed acceptance line uses Go 1.27.1, Node 22.18.0,
TypeScript 6.0.2 and the locked Vitest 4.1.11 / Vite 8.2.2 fixture.
The release must gate qualified execution to accepted toolchain contexts;
additional versions require their own acceptance and otherwise run in full.
Runtime authority requires its actual platform tests; cross-compilation and
installed runner baselines are insufficient. macOS and Linux support may have
different qualified contexts, which must be explicit in reports and docs.

## Critical path

| Work | Existing cards | Completion evidence |
| --- | --- | --- |
| Close tested addon and external Go integration | K10D, K14A | Source/log hashes, completed normal/race/vet, final review and commit |
| Go local helpers and setup contracts | K14B, K14C, K14 | Compiler-selected typed bindings, dormant-effect rejection and actual original-versus-selected cases |
| TypeScript/Vitest execution and private issuer | K16B, K16C, K16 | Real Vitest cases under the declared runtime; source-only parser demonstrations are insufficient |
| Shared selective execution and complete outcomes | K32, K33 | Same selection/reuse/fallback machinery for both languages, no framework branches in the selector |
| CLI, audits and reproducible acceptance | K35, K36, K37, K38, R06-QA | Actual packaged-CLI matrix, authenticated cache faults and measured fixture end-to-end costs; broader K39 consumer performance remains deferred |
| Release candidate and handoff | R06-HANDOFF | Versioned artifacts, checksums, documentation and explicit remaining external acceptance; broader K40/K39 dependencies stay open |

K14B/C and K16B/C are release blockers. Existing adapters for other languages
remain intact; their new implementation and qualification are deferred.

## Required acceptance

- Cold run: every required case has a real terminal outcome.
- Unchanged run: qualified groups reuse complete authenticated passes.
- Changed independent group: affected groups execute; unchanged qualified groups
  reuse, with exact case identities and no dependency on historical coverage.
- Shared helper, configuration, compiler, dependency, generated input or resource
  changes invalidate every influenced group.
- New/deleted/renamed cases, assertion failures, skips and collection/startup/hook
  errors cannot reuse an incompatible pass or mint incomplete passing receipts.
- Unsupported imports, syntax, hooks, dynamic loading, services and effects
  execute the complete original configured scope, including no-change runs.
- Source/tool/runtime drift, cancellation, overflow and cleanup failure prevent
  successful aggregate outcomes. Corrupt/foreign caches cause execution.
- Forced audits execute original scopes and revoke mismatched authority.
- Actual macOS/Linux CLI controls, full Go tests/races/vet, advisory source review
  and packaged artifact smoke checks pass on the final source revision.
- Report cold/warm end-to-end costs, including capture/build/probes/audits. Record
  negative savings honestly; claim no representative speedup without evidence.

## Working boundaries

Use three worker slots for independent Go, TypeScript and acceptance/review
work, with the coordinator owning common execution, registration and integration.
Claim exact files before edits. Heavy SDK/native acceptance uses one lane. A
separate Docker-free host-core process may run alongside it with private caches
and temporary directories, the same frozen source and complete input guards.
Source work can proceed independently. Reuse completed evidence on unchanged
bytes, and run full repository gates once per meaningful integration batch.

Preserve unfinished other-language work in its existing worktrees. After
retaining required evidence, remove obsolete task-owned downloads, build trees
and temporary environments to reclaim disk. Preserve unrelated host files.
Local commits and release preparation are authorized. Publishing, pushing tags,
deploying or changing external consumers require explicit approval.
