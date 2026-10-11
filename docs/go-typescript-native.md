# Go and TypeScript native execution

This guide describes the completed local restricted Go/TypeScript profiles
carried forward into authorized v0.6.2 publication preparation. Corrected hosted
gates and published asset verification remain pending; failed signed v0.6.0/v0.6.1
tags are preserved without releases. Current status is recorded in
[implementation status](implementation-status.md) and the
[release plan](go-typescript-release-plan.md).

`hayaku native` executes configured test workspaces. A cold run executes every
qualified group. A later run can reuse a complete authenticated passing result
when that group's code, reachable dependencies, cases and execution context match.
Go groups are test packages; Vitest groups are test files. Individual cases still
need exact terminal outcomes before a group can pass or be reused.

The shared engine owns receipts, audits, cancellation, output bounds and aggregate
completion. Ecosystem providers supply compiler/framework facts, complete discovery
and an owned decoder. Historical code coverage is not needed to authorize reuse.

## Enroll Go

Install Go separately. The qualified compiler profile is Go 1.27.1,
and Hayaku itself must be built with Go 1.27.1 for its typed checker to qualify
that compiler. Release archives use that build toolchain. The actual installed
acceptance matrix is macOS ARM64 and Linux ARM64. The two AMD64 targets are
build-only archive targets; historical local archive headers, checksums and embedded
build information passed verification. Extracted ARM64 CLI smokes passed on both platforms. The release
packages four macOS/Linux archives; Windows is deferred.
Choose every explicit test package required by the original suite:

```sh
hayaku native-init --root /absolute/project --language go \
  --package ./math --package ./validation
hayaku native --root /absolute/project --config native.json \
  --cache-dir /absolute/private-hayaku-cache
```

Enrollment writes a new `native.json` and preserves existing files. Its original
command is the installed Go executable with `test -count=1` and those selectors.
An incomplete list of packages defines an incomplete configured suite; Hayaku
does not infer additional required packages from a user's configuration.

The native target must match the original Go command's host OS and architecture.
macOS uses the system sandbox. Linux requires the already installed immutable
Docker image and supported Landlock/seccomp boundary; specify `--platform linux`,
`--architecture arm64` and `--image sha256:<installed-image-id>` at enrollment.
Docker, SDKs and images are never fetched automatically.

The restricted profile admits bounded pure tests and compiler-selected local
helpers, including supported external test packages. A compiler-selected literal
string `go:embed` fixture can be read when its exact input and all uses are
accounted for. Interfaces, reflection, cgo, assembly, goroutines, mutable package
state, dynamic initialization, `TestMain`, general filesystem access and unknown
setup broaden to the original command. Custom command flags, environment,
prerequisites and recursive `./...` selectors also retain the original scope.

## Enroll TypeScript with Vitest

The initial profile uses Node 22.18.0, TypeScript 6.0.2, Vitest 4.1.11,
Vite 8.2.2 and Rolldown 1.2.12. Framework, compiler, Node, native addon, ELF loader
and shared-library bytes must match the admitted publisher/runtime profiles.
See [runner provisioning](runner-provisioning.md) for isolated development input
provenance. Install these inputs separately; enrollment does not qualify them.

The configured original Vitest command runs in the fixed Linux ARM64 image
`sha256:0ea54978ec8888bd907c0b0d552b4ac12dd5951ddc37fead577d17d7252e2f91`.
Both macOS ARM64 and Linux ARM64 controllers use that original runtime context. A macOS host
Vitest command is a different context and retains full original execution.

```sh
hayaku native-init --root /absolute/tests --language vitest \
  --image sha256:0ea54978ec8888bd907c0b0d552b4ac12dd5951ddc37fead577d17d7252e2f91 \
  --node /absolute/host-node/bin/node \
  --runtime-node /absolute/linux-node/bin/node \
  --modules /absolute/fixture/node_modules \
  --typescript-sdk /absolute/fixture/node_modules/typescript \
  --loader /absolute/runtime/ld-linux-aarch64.so.1 \
  --library-dir /absolute/runtime \
  --addon /absolute/rolldown-binding.linux-arm64-gnu.node
hayaku native --root /absolute/tests --config native.json \
  --cache-dir /absolute/private-hayaku-cache
```

The runtime directory supplies `libdl.so.2`, `libstdc++.so.6`, `libm.so.6`,
`libgcc_s.so.1`, `libpthread.so.0` and `libc.so.6`. Source directories need read
and search permission for the original container user; source files need read
permission. The installed Node executable and ELF loader need executable
permission. Enrollment preserves input permissions.

Enrollment creates the fixed `hayaku.vitest.config.mjs`, minimal `package.json`
and native configuration. Existing conflicting files are preserved and cause
enrollment to fail. This profile uses one isolated thread, Node environment,
native configuration loading and complete `**/*.test.ts` / `**/*.test.js`
discovery. Ambient configuration, projects, custom transforms, plugins, hooks,
browser/VM modes, retries and timers are outside its qualified contract.

All `.ts` and `.js` modules in the configured root are checked, including dormant
modules. The bounded grammar admits primitive pure expressions, local functions,
resolved acyclic named imports and the supported Vitest `test`/`expect` assertions.
Unknown globals, dynamic imports, mutable state, effects and unsupported syntax
broaden to the complete original command. Arbitrary application TypeScript is
not promised minimal selection. `.git` and provisioned `node_modules` directories
are handled separately from user source; the complete fixed module tree stays
live-verified.

## Read results and audit

v0.6.2 validates supplied Vitest inventories before controller admission.
Malformed inventories are rejected on every architecture; unsupported controllers
cannot parse or mint authority. Valid inventories retain the same source,
controller, tool and runtime checks, with no additional qualified context.

Reports expose `executed`, `reused`, `qualified`, `complete` and `passed` for each
group, plus exact native case outcomes and fallback reasons. A workspace fallback
is reported as `<workspace>::suite`, with the original command's terminal status,
no omission authority and no reusable key. It executes on every invocation.

Failed, skipped, missing or contradictory native cases cannot create passing
receipts. New/deleted/renamed cases change inventories. Reachable helper changes
invalidate their consumers; unrelated pure source changes can preserve other
groups' keys. Configuration, compiler, framework and runtime context changes
invalidate affected authority. Source or owner drift after a later workspace,
cancellation and cleanup errors invalidate aggregate completion and revoke the
invocation's attempted receipts.

The cache must be private and outside the source tree (or in the supported `.git`
location). Portable CI authority uses the existing
[cache authority workflow](portable-cache.md); cache JSON alone grants no trust.

```sh
hayaku native --root /absolute/project --config native.json \
  --cache-dir /absolute/private-hayaku-cache --no-reuse
```

An audit revokes that authority's prior history before executing complete original
workspace scopes. Foreign authority data is preserved. A mixed configuration
places unique Go and Vitest workspace IDs in the same `workspaces` array; all
workspaces share cache/audit/aggregate handling and one global platform target.
Both providers qualify together on a Linux ARM64 host. On macOS, the Linux
target needed by Vitest leaves typed Go on its complete original-host fallback;
use separate Darwin Go and Linux Vitest configurations to let both reuse there.
A completed assertion failure does not prevent later workspaces from running;
cancellation does.

Capture, compiler checks, collection and live verification still cost time on a
warm run. Tiny suites may run faster in full. Release acceptance records measured
cold/warm costs; this profile makes no universal speedup claim. Windows and new
language qualification remain deferred. Each later release adds one language.
