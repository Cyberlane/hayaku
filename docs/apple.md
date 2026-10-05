# Apple XCTest integration

Hayaku 0.5.1 supports installed Darwin Swift 6.0–6.4 XCTest and Xcode's installed Xcode 26.3/27.0 xcresulttool exports. It never installs a toolchain or fetches dependencies. Native full suites remain required; target proposals and shadow results are diagnostic.

## SwiftPM

Use the native `swift` adapter for an explicit XCTest-only command:

```json
{
  "schema": 1,
  "context": {"id": "apple-installed", "os": "darwin", "arch": "arm64"},
  "workspaces": [{
    "id": "swift", "root": "Packages/NeiroKit", "adapter": "swift",
    "command": {"cwd": ".", "executable": "/usr/bin/swift", "argv": ["test", "--disable-swift-testing"]},
    "swift_runtime": {"dependencies": "/absolute/provisioned/swift-inputs"}
  }]
}
```

Omit `swift_runtime` for packages with no external dependencies. Otherwise explicitly resolve/install dependencies outside Hayaku, then copy only `checkouts`, `repositories` and `workspace-state.json` into a separate directory. Registry packages and downloaded binary artifacts are rejected. Dependency bytes, compiler/tool bytes, SDK context, argv and environment are bound privately. Internal dependency links are preserved without recursively following directory links. Build products and shared compiler caches are excluded from that provisioned input directory.

Package description and native test listing happen in disposable build state. Source and resources own their targets; target dependency edges propagate changes to test targets. Manifest/lockfile changes broaden all targets. Base/candidate graph union covers new and deleted resources. `run` executes the complete XCTest command; `shadow` also executes the escaped target filter proposal and checks omitted failures. Skipped outcomes remain skipped. Enumeration, terminal outcomes, process completion and source/runtime identity must all agree.

Serial XCTest is required. Flags for configuration, jobs, sanitizers, coverage and an explicit `--build-system native` or `swiftbuild` are preserved. Caller filters, parallel execution, skip-build, custom scratch paths, plugins, macros and binary targets are rejected. An XCTest-only command does not cover Swift Testing: retain the original mixed-framework full gate separately. Do not silently change the build system to make a test pass; select and record the intended installed context.

## Xcode

Use adapter `xcode` with the original full `xcodebuild ... test` command, an existing committed project and an external `-derivedDataPath`. Preserve scheme, test plan, build settings and a single concrete destination. Hayaku adds a private result bundle and performs an independent `-enumerate-tests` collection before running. Automatic package resolution/updates are disabled; provision required dependencies beforehand.

Only enabled XCTest cases in the single selected plan are expected. xcresulttool exports are reconciled against that inventory; case outcomes and summary counts must agree. Missing/extra/duplicate/nonterminal cases and unsupported repetition, parameterization, plans, devices or configurations fail closed. Diagnostic text, device identifiers and source locations are discarded before reporting. Xcode remains a full-suite unit and has no shadow filtering or omission authority.

## Generic Node workspaces

A `command` workspace may declare the same `node_runtime` object as native Node integrations. Its executable must resolve to the bound installed Node binary; installed `node_modules` bytes are verified and copied into the execution snapshot. The original argv/cwd remain full. For example, run `node node_modules/vitest/vitest.mjs run` for a custom Cloudflare pool. This does not activate native Vitest graph/filter/result qualification. Provision dependencies before Hayaku, and configure runner output/cache paths so source and provisioned dependencies remain unchanged.

Planning and execution use exact committed revisions. Dirty, staged, untracked and ignored source changes remain rejected except explicitly bound ignored installed Node dependencies. Reports belong outside the checkout.
