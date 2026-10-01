# Runtime observations and strict assurance

v0.3.0 adds a separate **diagnostic** Vitest run that records supported runtime
filesystem APIs before configuration loading and before user setup in workers.
It captures dynamic read paths, metadata, missing-file/existence probes and
readdir/opendir directory observations. It records relative paths, operations and
whole-file owners, never file contents, raw environment values or console logs.

This release does **not** implement a qualified OS isolation backend or enable
production skips. JavaScript instrumentation cannot cover native/internal accesses,
previously captured references, every worker/subprocess or arbitrary external
state. Even a successful trace is historical observation, not proof of all future
paths. Node permissions alone are not a complete boundary. Strict assurance is
preserved: every required command remains full, and observations only broaden
experimental proposals. No observed passing result becomes omission authority.

## Use an existing qualified native installation

Start with [Vitest setup](vitest.md): existing Node 22.18.0, Vitest 4.1.11 and
Vite 7.3.1 are the native fixture matrix; no automatic dependency installation.
Use a reviewed configuration and effective CI=true. On a clean checkout at BASE:

```sh
hayaku plan --base BASE --candidate BASE --config .git/hayaku-vitest.json \
  --output .git/base-plan.json
hayaku observe --plan .git/base-plan.json --config .git/hayaku-vitest.json \
  --output .git/base-observations.json
```

After checking out the clean candidate, with unchanged reviewed configuration,
Node/dependencies and effective environment:

```sh
hayaku plan --base BASE --candidate HEAD --config .git/hayaku-vitest.json \
  --observations .git/base-observations.json --output .git/runtime-plan.json
hayaku shadow --plan .git/runtime-plan.json --config .git/hayaku-vitest.json \
  --observations .git/base-observations.json --output .git/runtime-shadow.json
hayaku run --plan .git/runtime-plan.json --config .git/hayaku-vitest.json \
  --observations .git/base-observations.json --output .git/full-results.json
```

Saved-plan reconstruction requires the same observation file. Reports must have
fresh filenames. Cross-checkout Git metadata/worktree locations may change the
context/tool identity; such reports reject rather than silently transplant.
Observations bind the base source, configuration, installed tools and effective
context. Unknown/forged identities, malformed fields and oversized reports reject.
Failed/incomplete captures, absent workspace or worker coverage and unsupported
API gaps broaden the proposal. Original static influences are never removed.
Directory and metadata/existence envelopes conservatively invalidate descendants,
including additions/deletions;
missing-path probes invalidate when that exact path changes.

## Scope and limits

The observe command accepts bound Vitest workspaces only. Other suites retain
normal full-run commands. Diagnostic runs preserve file/project inventory but
prepend instrumentation and serialize file execution; they are separate from the
original required run and cannot replace its timing/concurrency behavior.
`runtime_trace.complete` describes validated transport, not complete influence.
Unsupported effects include external/ambiguous paths, file descriptors, writes,
subprocesses, network, native addons, extra workers, time and randomness. Framework
internals may themselves generate these gaps, legitimately broadening every file.
The stream is bounded to 32 MiB and 100k records; cancellation, source/runtime
mutation and incomplete terminal results remain failures. Copies are not a sandbox.

## Remaining work for real skipping

A qualified execution backend must enforce an immutable input envelope across
configuration, transforms, setup, discovery and tests; account for native code and
child processes; restrict external services; and control nondeterminism and shared
state. Workspace-level reuse is the first target, before per-test omission. That
backend and representative savings are still unimplemented/unmeasured.

Mixed monorepos with generated ignored inputs, linked workspace dependencies and
workerd/service suites remain unqualified. Preserve their original gates rather
than replacing them with a Vitest-only command.
