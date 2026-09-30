<p align="center"><img src="assets/branding/hayaku-logo.png" alt="Hayaku" width="440"></p>

<p align="center">Understand affected tests. Keep the safety of your full suite.</p>
<p align="center"><a href="https://cyberlane.github.io/hayaku/">Website</a> · <a href="https://github.com/Cyberlane/hayaku/releases/latest">Downloads</a> · <a href="docs/safety.md">Safety contract</a></p>

Hayaku is a Go CLI for planning test runs from exact Git revisions. It discovers
workspace dependencies, explains uncertainty and emits structured commands and
experimental affected-test proposals.

**v0.1.0 is an early release. Required commands always run the original full
suites. Affected-test skipping is disabled.** Go shadow evaluation lets you
challenge proposals against an independent full run. Complete runtime influence
cannot be inferred from imports alone; there is no confidence-based or
skip-anyway switch.

## Install

Download an archive from [Releases](https://github.com/Cyberlane/hayaku/releases/latest).
Each release includes `SHA256SUMS`, a source/build manifest and license notices.

For macOS with Apple Silicon:

```sh
curl -fLO https://github.com/Cyberlane/hayaku/releases/download/v0.1.0/hayaku-darwin-arm64.tar.gz
curl -fLO https://github.com/Cyberlane/hayaku/releases/download/v0.1.0/SHA256SUMS
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
go install github.com/Cyberlane/hayaku/cmd/hayaku@v0.1.0
```

No npm package is required. Hayaku has no third-party Go modules. Git and your
ecosystem tools must already be installed; Hayaku never fetches Git revisions
or installs test runners.

## Start with a plan

In a trusted Git repository:

```sh
hayaku init
# Review hayaku.json: roots, original test commands and host context.
hayaku doctor
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

`shadow` currently supports Go workspaces. Separate source copies do not isolate
services or external state. A passing comparison is finite evidence, not proof
of selection safety. For installed/generated ecosystems such as Node/Turbo,
retain the original full runner alongside observational planning: ordinary
`node_modules` is outside the immutable execution contract.

## Framework status

| Adapter | Discovery and experimental proposals | Required execution |
| --- | --- | --- |
| Go | Native package, import, test and embed graph | Full suite; native JSON reconciliation |
| Cargo | Offline/locked metadata, whole packages | Full suite; result qualification pending |
| pytest | Collection bridge, whole files | Full suite; native acceptance pending |
| Vitest | File/project inventory; 4.1.11 fixture tested | Full suite; dependency/results pending |
| Bazel, Maven, Gradle, sbt, .NET, Xcode, Swift | Explicit fallback; native graphs pending | Full suite |
| command | Language-independent configured command | Full suite |

`hayaku doctor` reports the live adapter registry and installed tools.
No adapter is authorized to omit production tests.

## Documentation

- [Safety](docs/safety.md), [architecture](docs/architecture.md) and [qualification](docs/qualification.md)
- [CI integration](docs/ci.md) and [implementation status](docs/implementation-status.md)
- [Pilot methodology](docs/pilot-methodology.md) and [historical observations](docs/pilot-results.md)
- [Verification](docs/verification.md) and [distribution](docs/distribution.md)
- [First-release checks](docs/release-checks.md)
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
installed automatically. Test an existing Vitest runner with `HAYAKU_VITEST_BINARY`.

See [contributing](CONTRIBUTING.md). Licensed under [MIT](LICENSE);
Go runtime notices are in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
