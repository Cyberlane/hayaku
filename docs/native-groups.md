# Restricted native Go groups (unreleased)

`hayaku native` can skip execution of an unchanged **qualified pure Go package**
by reusing its authenticated complete pass. It compiles and executes actual
Linux Go test binaries, not WASI modules. Qualification checks all package code,
including branches and helpers that no test has previously covered.

```json
{
  "schema": 1,
  "image": "sha256:<64 lowercase hex characters from a locally installed image>",
  "architecture": "arm64",
  "packages": ["./math", "./validation"]
}
```

```sh
hayaku native --root . --config native.json --cache-dir /private/path/native-passes
hayaku native --root . --config native.json --cache-dir /private/path/native-passes --no-reuse
```

Use an installed Linux image with the matching architecture (`arm64` or
`amd64`). Docker, the Go compiler and its standard library must already be
provisioned and trusted. Hayaku disables toolchain/module downloads and Docker
pulls. It builds the embedded platform worker offline with the same installed
compiler; no supplied helper or `qualified` configuration assertion is accepted.
Cache directories must be private and outside source, or underneath `.git`.
Packages are explicit directories, not patterns, and must produce nonempty test
inventories. Empty/no-test groups are errors. The report covers that list;
it does not assert discovery of every package in a repository.

A cold invocation executes each group. An unchanged qualified group can reuse on
subsequent invocations; changing another package does not invalidate its pass.
The compiler's own build cache can accelerate preparation, but Hayaku compiles
and fingerprints each artifact again before deciding whether to skip its tests.
There is no dependency on historical test coverage. Cold execution, capture,
compilation and platform probes still have a cost; no net CI savings claim has
been established for representative consumers.

## Qualification and fallback

The first certifier accepts a small, deliberately conservative subset:

- One package, including same-package test files; no mutable package globals,
  initialization hooks, methods, external test packages or native object files.
- Direct local function calls, scalar/string/array/slice computation, constants,
  loops and ordinary control flow. The exact syntax is a whitelist in source.
- Only `testing` imports in test files, conventional test signatures and a fixed
  set of assertion/status methods. Testing handles and function/method values
  cannot escape. No subtests, parallel tests, test logging or `TestMain`.

Other imports, clocks, entropy, files/directories/probes, services, subprocesses,
reflection, unsafe code, concurrency, maps, compiler directives and unknown
syntax are unqualified. They run the complete original-host package command
(`go test -json -count=1 <package>`) every time. An unavailable platform also
runs those complete commands; incomplete native execution evidence broadens to
the complete original-host group. Cancellation and live source drift prevent
aggregate success. Build failures remain failures.

Reports distinguish `linux-<architecture>` from `original-host` outcomes, as
well as executed, reused, qualified, complete and passed. A skipped test remains
a successful runner skip, but that group cannot mint an all-cases-passed receipt.
Failed assertions prevent aggregate success and are never cached. No raw source,
process diagnostics or environment values are included in reports.

This command is a separate, restricted execution contract. It does not replace
ordinary `hayaku run` full native gates, certify equivalence with arbitrary
native commands, or qualify other languages/frameworks. An explicit package
list cannot authorize omissions from an existing broader CI suite. Independent
review and consumer CI acceptance remain outstanding.

## Shared boundary and authority

The framework-independent backend mounts private copies of only the program,
its argument/environment contract and the embedded worker. It uses a pinned
image, read-only root/mounts, no network, no capabilities, an unprivileged UID,
no new privileges and fixed memory/process limits. Landlock ABI 3 denies file
access outside the copied input directory, writes and executing other files.
A seccomp filter denies sockets, process creation (runtime threads are allowed),
namespace/privilege bypasses, tracing, handle-based file access and io_uring.
The guest receives empty stdin, explicit environment and no inherited extra
file descriptors. Every invocation performs a real enforcement probe, including
invocations where all test groups reuse. Container lifecycle and resource
termination are reconciled independently of the attach process; interrupted
and overflowing runs are removed with a bounded cleanup context.

**Containment is not determinism.** Native clocks, entropy and filesystem
metadata probes are not virtualized. Landlock is not a complete metadata-probe
filter. Self-exec of the same permitted program is also not a deterministic
computation guarantee. Those behaviors cannot qualify through this backend
alone; the Go certifier excludes them. The trusted Go runtime/testing library
still uses operational clocks, entropy and threads internally. Receipts certify
functional test outcomes within the restricted contract, not wall-clock timing,
host-load behavior, absence of operating-system bugs or arbitrary program safety.

The common receipt store now serves both WASI and native contracts, with
separate authentication domains. Native identities bind program/proof/inventory,
arguments/environment, controller/worker/CLI bytes, immutable image, daemon,
kernel and backend contract. Live identities are rechecked before reuse and after
execution. Only exact, complete, all-cases-passed outcomes can mint native receipts;
coverage, diagnostics and previous unauthenticated result reports have no authority.
The invoking user, executable, compiler/standard library and Docker daemon/kernel
are trusted. Original-host fallback commands retain ordinary native process access
and must also be trusted. Local authority protects bounded guests; it cannot
protect against malicious host code replacing the controller, compiler or key.

`--no-reuse` revokes all authenticated native receipts in that cache before
capture/build, even if the invocation is cancelled before execution. Other
receipt domains remain intact. New complete passes may be written afterward.
Source/policy drift or incomplete aggregate evidence prevents success and revokes
available receipts used/created by that invocation. Missing, invalid or unavailable caches execute again. The shared
store's versioned envelope invalidates older WASI cache files; they execute again
and repopulate, without a migration that grants new authority.

## Verification

Unit tests cover unexecuted effects, testing-handle/function escapes, unknown
syntax, exact terminal inventories, skips, full fallback, private/domain-separated
receipts, tampering, linked inputs, empty groups, policy drift and cancelled audits. The installed Linux
fixtures exercise cold/warm/change/audit/failure behavior and real file, socket,
subprocess, foreign-exec, environment, cancellation and output-overflow boundaries.

```sh
HAYAKU_NATIVE_IMAGE=sha256:<installed image ID> HAYAKU_NATIVE_ARCH=arm64 \
  go test ./internal/native -run TestInstalledLinux -v
```

These fixtures never pull an image or install a runner. Linux arm64 was exercised
locally with the installed OrbStack daemon; amd64 cross-compilation is a build
check, not runtime qualification. Finite adversarial fixtures and local review do
not establish a soundness proof, production approval or published availability.
