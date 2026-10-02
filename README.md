<p align="center"><img src="assets/branding/hayaku-logo.png" alt="Hayaku" width="440"></p>

<p align="center">Understand affected tests. Reuse results within an enforced boundary.</p>
<p align="center"><a href="https://cyberlane.github.io/hayaku/">Website</a> · <a href="https://github.com/Cyberlane/hayaku/releases/latest">Downloads</a> · <a href="docs/safety.md">Safety contract</a></p>

Hayaku is a Go CLI for planning test runs from exact Git revisions. It discovers
workspace dependencies, explains uncertainty and emits structured commands and
experimental affected-test proposals.

**v0.4.0 adds actual whole-suite reuse for a separate deterministic WASI contract.
Native Go, Vitest, Python, browser and service suites retain full fallback.** A WASI
pass does not establish equivalence with a native command. Native shadow evaluation
challenges proposals against independent full runs; imports and traces cannot
establish complete runtime influence. There is no confidence-based skip switch.

## Install

Download an archive from [Releases](https://github.com/Cyberlane/hayaku/releases/latest).
Each release includes `SHA256SUMS`, a source/build manifest and license notices.

For macOS with Apple Silicon:

```sh
curl -fLO https://github.com/Cyberlane/hayaku/releases/download/v0.4.0/hayaku-darwin-arm64.tar.gz
curl -fLO https://github.com/Cyberlane/hayaku/releases/download/v0.4.0/SHA256SUMS
grep '  hayaku-darwin-arm64.tar.gz$' SHA256SUMS | shasum -a 256 -c -
tar -xzf hayaku-darwin-arm64.tar.gz
mkdir -p "$HOME/.local/bin"
install -m 755 hayaku "$HOME/.local/bin/hayaku"
# Add $HOME/.local/bin to PATH if it isn't there already.
hayaku version
```

Linux uses `hayaku-linux-*.tar.gz`; Windows uses `hayaku-windows-*.zip`.
Six cross-built targets are available: macOS, Linux and Windows on amd64 and
arm64. Cross-compilation is not native acceptance: Windows and contexts beyond
the tested matrix are experimental. macOS binaries are not Apple-signed or
notarized. Checksums establish file integrity, not publisher identity.
See [distribution](docs/distribution.md) for validation and provenance.

With Go 1.26 or newer installed:

```sh
go install github.com/Cyberlane/hayaku/cmd/hayaku@v0.4.0
```

No npm package or daemon is required. The core uses the Go standard library,
with pinned wazero/platform dependencies for the WASI boundary. Git and ecosystem
tools must already be installed; Hayaku never fetches revisions or installs runners.
Windows capsule receipts are not ACL-qualified and execute without reuse.

## Start with a plan

In a trusted Git repository:

```sh
hayaku init
# Review hayaku.json: roots, original test commands and host context.
hayaku doctor
hayaku catalog
# Commit the reviewed configuration before using run or shadow.
git add hayaku.json
git commit -m "chore: configure Hayaku"
# Replace BASE_COMMIT with an explicit, locally available comparison commit.
hayaku plan --base BASE_COMMIT --candidate HEAD --output .git/hayaku-plan.json
hayaku explain --plan .git/hayaku-plan.json
```

Output files must not already exist. Use the exact candidate tested by CI,
including merge commits. Both Git objects must exist locally; missing or shallow
bases fail without fetching. Planning reads committed source, not unstaged edits.
Staged/worktree planning, symlinks and submodules are unsupported.

Native discovery can execute project/plugin code. Hayaku is not a sandbox;
inspect trusted projects only. Missing discovery never means no tests are affected.

## Validate and run

For a clean, configured Go repository:

```sh
hayaku shadow --plan .git/hayaku-plan.json --output .git/hayaku-shadow.json
hayaku run --plan .git/hayaku-plan.json --output .git/hayaku-result.json
```

`run` reconstructs the saved plan and validates exact source, configuration,
installed tool bytes and environment before executing full required suites.
Edited plans cannot authorize commands. The checkout must match the candidate,
with no untracked or ignored source inputs. Store reports under Git metadata;
write test/build outputs outside the source tree. Native Go JSON reconciliation
makes missing outcomes, failures and cancellation return nonzero.

`shadow` supports Go, bound Vitest and the implemented native collection runners.
[Vitest setup](docs/vitest.md) binds Node and installed dependency bytes, copies
them into disposable source snapshots, and reconciles native file/project/test
outcomes. Only the declared, ignored dependency tree is permitted in a checkout;
other generated inputs remain unsupported. Separate copies do not isolate services
or external state. A passing comparison is finite evidence, not selection proof.
Every native integration retains its original full suite as the required gate.

## Reuse a bounded WASI suite

A [capsule](docs/capsules.md) runs an explicitly supplied WASI Preview 1 module
with copied read-only inputs, explicit arguments/environment, logical time and
seeded entropy. Denied capabilities, writes, traps, incomplete results and
cancellation cannot create reusable passes.

```sh
hayaku capsule --config capsule.json --cache-dir .git/hayaku-passes
# The same complete context can reuse its authenticated passing result.
hayaku capsule --config capsule.json --cache-dir .git/hayaku-passes
# Scheduled audit removes the previous receipt before executing again.
hayaku capsule --config capsule.json --cache-dir .git/hayaku-passes --no-reuse
```

Module/input bytes, producer identity, arguments, environment, seed, limits and
backend context invalidate reuse. Cache authentication trusts the invoking user
and executable. Invalid or unavailable caches execute again; result reports omit
guest logs, source bytes and raw environment values.

Hayaku's first pilot compiles its graph tests from committed snapshots into this
separate contract and measures execution against actual reuse:

```sh
hayaku pilot --package ./internal/graph --cache-dir .git/hayaku-passes \
  --output .git/hayaku-pilot.json
```

Independent fresh compiler caches are used on both sides. Compiler identity,
snapshotting, compilation, final validation and cleanup are counted. This pilot does not replace
`go test ./...` or DocPulse's native suites. See [measurements](docs/measurements.md).

## Framework status

| Adapter | Discovery and experimental proposals | Required execution |
| --- | --- | --- |
| Go | Native package, import, test and embed graph | Full suite; native JSON reconciliation |
| Cargo | Offline/locked metadata, packages, Rust report ingestion | Full suite |
| Node test runner, unittest, pytest, Jest, Playwright | Native collection/results; conservative whole-workspace ownership | Full suite |
| Vitest 3.2.7 / 4.1.11 | Three pinned Vitest/Vite pairs; configured TS/JS graph, results and observations | Full suite |
| cargo-nextest 0.9.146 | Native binary/case inventory and Cargo package proposals | Original full suite; no automatic shadow reconciliation |
| JVM, .NET, Apple, Ruby, PHP, Dart/Flutter, C/C++, Bun, Deno, Bazel | Named full-command integrations and applicable report imports | Full suite |
| command | Language-independent configured command | Full suite |
| WASI capsule | Separate enforced deterministic guest contract | Whole-suite reuse within that contract |

`hayaku doctor` reports the live adapter registry and installed tools.
No native adapter is authorized to omit production tests. [Runner support](docs/runner-support.md)
separates installed fixture versions, protocol ranges and full-scope integrations.
[Input envelopes](docs/inputs.md) capture explicit generated/linked inputs without
overwriting source. [Measurements](docs/measurements.md) report net cost and preserve
upstream cache states; Turbo hits are never credited as Hayaku reuse.

## Documentation

- [Safety](docs/safety.md), [architecture](docs/architecture.md) and [qualification](docs/qualification.md)
- [Vitest setup and limits](docs/vitest.md), [CI integration](docs/ci.md) and [implementation status](docs/implementation-status.md)
- [Capsules](docs/capsules.md), [input envelopes](docs/inputs.md), [runner support](docs/runner-support.md) and [measurements](docs/measurements.md)
- [Pilot methodology](docs/pilot-methodology.md) and [historical observations](docs/pilot-results.md)
- [Verification](docs/verification.md) and [distribution](docs/distribution.md)
- [Release checks](docs/release-checks.md)
- [Research](docs/research/README.md)

No representative CI savings are claimed. Full-gated shadow evaluation adds work.

## Development

```sh
go test ./...
go test -race ./...
go vet ./...
go build -o /tmp/hayaku ./cmd/hayaku
```

Release verification pins Go 1.27.1. Tests include native Git/Go/Cargo fixtures,
independent graph oracles, seeded faults and a runtime-dependency counterexample.
Optional native fixtures skip explicitly when tools are unavailable; nothing is
installed automatically. Native Vitest CI installs each pinned development fixture
outside source. Local native fixture verification uses `HAYAKU_VITEST_NODE`,
`HAYAKU_VITEST_MODULES` and `HAYAKU_VITEST_BINARY`; see [Vitest](docs/vitest.md).

See [contributing](CONTRIBUTING.md). Licensed under [MIT](LICENSE);
Bundled dependency notices are in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

Runtime filesystem observations are available for bound Vitest installations.
See [runtime setup and strict assurance](docs/runtime-observations.md). This
diagnostic feature cannot authorize native affected-only execution. The separate
capsule boundary has its own enforced contract and authenticated receipts.
