<p align="center"><img src="assets/branding/hayaku-logo.png" alt="Hayaku" width="440"></p>

<p align="center">See how your changes affect your tests.</p>
<p align="center"><a href="https://cyberlane.github.io/hayaku/">Website</a> · <a href="https://github.com/Cyberlane/hayaku/releases/latest">Downloads</a> · <a href="docs/safety.md">How test decisions work</a></p>

Hayaku is a local command-line tool that compares Git commits, identifies tests
that may be affected, and explains its recommendations. Review the test commands,
see what Hayaku could not establish, and compare experimental recommendations
against a full test run.

**Your normal test suite still runs in full.** Hayaku does not skip native Go,
JavaScript, Python, browser, or service tests based on a recommendation, a trace,
or a confidence score. When information is incomplete, it keeps the full run or
stops planning. See [how test decisions work](docs/safety.md).

**v0.5.0 adds SwiftPM XCTest target proposals and full Xcode result reconciliation.** Installed Node dependencies can also be bound to an unchanged generic full command, including custom Cloudflare test pools. See [Apple integration](docs/apple.md) and the [release notes](docs/releases/v0.5.0.md). Native full suites remain required.

**v0.4.0 can reuse passing results for separate WASI suites.** These WebAssembly
suites run with controlled inputs and restricted access to your computer. Reuse
requires unchanged code, inputs, and execution settings; it does not replace or
prove the result of your normal test suite.

## Install

Download an archive from [Releases](https://github.com/Cyberlane/hayaku/releases/latest).
Each release includes `SHA256SUMS` to check the downloaded files, a record of
the source and build, and license notices.

For macOS with Apple Silicon:

```sh
curl -fLO https://github.com/Cyberlane/hayaku/releases/download/v0.5.0/hayaku-darwin-arm64.tar.gz
curl -fLO https://github.com/Cyberlane/hayaku/releases/download/v0.5.0/SHA256SUMS
grep '  hayaku-darwin-arm64.tar.gz$' SHA256SUMS | shasum -a 256 -c -
tar -xzf hayaku-darwin-arm64.tar.gz
mkdir -p "$HOME/.local/bin"
install -m 755 hayaku "$HOME/.local/bin/hayaku"
# Add $HOME/.local/bin to PATH if it isn't there already.
hayaku version
```

Linux uses `hayaku-linux-*.tar.gz`; Windows uses `hayaku-windows-*.zip`.
Builds are available for macOS, Linux, and Windows on amd64 and arm64. A build
being available does not mean it has been tested on that platform; Windows and
setups outside the tested matrix remain experimental. macOS binaries are not
Apple-signed or notarized. Checksums verify the file contents, not the publisher's
identity. See [download verification and build details](docs/distribution.md).

With Go 1.26 or newer installed:

```sh
go install github.com/Cyberlane/hayaku/cmd/hayaku@v0.5.0
```

No npm package, account, or background service is needed. Git and your test tools
must already be installed; Hayaku does not download commits or install runners.
The core uses the Go standard library, with pinned wazero and platform dependencies
for WASI execution. WASI suites run on Windows, but result reuse is disabled until
cache permissions have been verified there.

## Start with a plan

In a trusted Git repository:

```sh
hayaku init
# Review hayaku.json: project roots, test commands, and execution settings.
hayaku doctor
hayaku catalog
# Commit the reviewed configuration before using run or shadow.
git add hayaku.json
git commit -m "chore: configure Hayaku"
# Replace BASE_COMMIT with a commit already available on your computer.
hayaku plan --base BASE_COMMIT --candidate HEAD --output .git/hayaku-plan.json
hayaku explain --plan .git/hayaku-plan.json
```

Hayaku reads committed code. Both commits must exist locally; if a shallow clone
is missing the comparison commit, planning stops without downloading it. In CI,
use the exact commit being tested, including a merge commit when applicable.

Output files must not already exist. Planning from staged or uncommitted changes,
symlinks, and submodules is unsupported. Inspecting tests can execute project or
plugin code, so use a repository you trust. This workflow is not sandboxed, and
failure to find tests never means there are no affected tests.

## Run tests and check the recommendation

`shadow` compares the experimental recommendation with an independent full run.
`run` executes the full required test suites. For a clean, configured Go repository:

```sh
hayaku shadow --plan .git/hayaku-plan.json --output .git/hayaku-shadow.json
hayaku run --plan .git/hayaku-plan.json --output .git/hayaku-result.json
```

Before running commands, Hayaku reconstructs the plan and checks the source,
configuration, installed tools, and environment. Editing a plan file cannot
authorize a different command. The checkout must match the candidate commit,
with no untracked or ignored source inputs. Keep reports under `.git` and write
test or build outputs outside the source tree. For Go, failures, missing test
outcomes, and cancellation all produce a nonzero exit status.

Shadow mode supports Go, configured Vitest installations, and the implemented
native collection runners. The [Vitest setup guide](docs/vitest.md) explains how
Hayaku verifies Node and dependency contents, copies them into temporary source
snapshots, and checks file, project, and test outcomes. Only the configured ignored
dependency directory is allowed in the checkout; other generated inputs remain
unsupported. Separate source copies still share access to services and external
state. A successful comparison helps evaluate a recommendation, but does not prove
that omitted tests would always be unaffected. The original full suite stays required.

## Reuse results from a separate WASI suite

A [capsule](docs/capsules.md) is Hayaku's controlled environment for an explicitly
supplied WASI Preview 1 WebAssembly module. It gets copied read-only files,
specified arguments and environment values, controlled time, and seeded random
values. It has no access to the host filesystem, network, or subprocesses.
Writes, denied operations, traps, incomplete results, and cancellation cannot
produce a result that Hayaku will reuse.

```sh
hayaku capsule --config capsule.json --cache-dir .git/hayaku-passes
# Unchanged code, inputs, and settings can reuse the verified passing result.
hayaku capsule --config capsule.json --cache-dir .git/hayaku-passes
# Force a fresh run, removing the saved pass before the audit starts.
hayaku capsule --config capsule.json --cache-dir .git/hayaku-passes --no-reuse
```

Changes to the module, input files, producer identity, arguments, environment,
random seed, resource limits, or runtime context prevent reuse. Hayaku verifies
saved passing results with a private authentication key; this trusts the user
running Hayaku and the executable. If the cache is invalid or unavailable, the
suite runs again. Reports omit guest logs, source contents, and raw environment values.

You can try Hayaku's own pilot, which compiles its graph tests from committed
code into a separate WASI suite and compares a fresh run with result reuse:

```sh
hayaku pilot --package ./internal/graph --cache-dir .git/hayaku-passes \
  --output .git/hayaku-pilot.json
```

Both sides use separate fresh compiler caches. The measurement includes checking
the compiler, copying source, compiling, validating the final result, and cleaning
up. This pilot does not replace `go test ./...` or DocPulse's normal suites.
See [how performance is measured](docs/measurements.md).

## Supported test tools

| Test tool | What Hayaku supports | What runs today |
| --- | --- | --- |
| Go | Package, import, test, and embedded-file dependencies; full-run comparisons and JSON result checks | Full test suite |
| Cargo | Offline, locked package dependencies and Rust result reports | Full test suite |
| Node test runner, unittest, pytest, Jest, Playwright | Test lists and completed results; every workspace input is treated as potentially affecting every test | Full test suite |
| Vitest 3.2.7 / 4.1.11 | Three pinned Vitest/Vite pairs; configured JS/TS dependencies, results, full-run comparisons, and observed file reads | Full test suite |
| cargo-nextest 0.9.146 | Test binary and case lists, Cargo package recommendations; automatic shadow result checks are not yet supported | Full test suite |
| JVM, .NET, Apple, Ruby, PHP, Dart/Flutter, C/C++, Bun, Deno, Bazel | Configured full test commands and applicable result reports; no claim of native discovery or filtering | Full test suite |
| Custom command | Your configured command, regardless of language | Full test suite |
| WASI capsule | Separate suite with controlled inputs and execution | Reuse a verified whole-suite pass when unchanged |

Use `hayaku catalog` to list integrations and `hayaku doctor` to check installed
tools. None of the native integrations allow production tests to be skipped.
See [supported versions and limitations](docs/runner-support.md) for the difference
between tested versions, accepted report formats, and full-command integrations.

[Input bundles](docs/inputs.md) record specified generated files and linked inputs
without overwriting source. [Performance reports](docs/measurements.md) count the
complete cost and keep other tools' cache hits separate; a Turbo cache hit is
never counted as Hayaku result reuse.

## Documentation

- [How test decisions work](docs/safety.md), [architecture](docs/architecture.md) and [how support is qualified](docs/qualification.md)
- [Vitest setup and limits](docs/vitest.md), [CI integration](docs/ci.md) and [implementation status](docs/implementation-status.md)
- [WASI result reuse](docs/capsules.md), [input bundles](docs/inputs.md), [supported runners](docs/runner-support.md) and [performance measurement](docs/measurements.md)
- [Pilot methodology](docs/pilot-methodology.md) and [historical observations](docs/pilot-results.md)
- [Verification](docs/verification.md) and [distribution](docs/distribution.md)
- [Release checks](docs/release-checks.md)
- [Research](docs/research/README.md)

CI savings have not yet been established on representative projects. Shadow
comparisons add work because they keep the full test runs.

## Development

```sh
go test ./...
go test -race ./...
go vet ./...
go build -o /tmp/hayaku ./cmd/hayaku
```

Release checks use Go 1.27.1. Tests include real Git, Go, and Cargo fixtures,
independent dependency-graph checks, deliberately introduced faults, and a case
where runtime inputs are missing from the dependency graph.
Optional native fixtures skip explicitly when tools are unavailable; nothing is
installed automatically. Native Vitest CI installs each pinned development fixture
outside source. Local native fixture verification uses `HAYAKU_VITEST_NODE`,
`HAYAKU_VITEST_MODULES` and `HAYAKU_VITEST_BINARY`; see [Vitest](docs/vitest.md).

See [contributing](CONTRIBUTING.md). Licensed under [MIT](LICENSE).
Bundled dependency notices are in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

For configured Vitest installations, Hayaku can observe filesystem activity to
help explain runtime dependencies. See [runtime observation setup and limits](docs/runtime-observations.md).
These observations can add tests to a recommendation, but cannot authorize
skipping native tests. WASI result reuse has its own separate execution rules
and authenticated passing results.
