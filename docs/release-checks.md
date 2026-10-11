# v0.6.2 publication preparation

Publication preparation is authorized. The failed signed v0.6.0 and v0.6.1 tags
remain immutable; neither produced a release. Corrected hosted core gates, four
Vitest compatibility pairs and WASI pilots are pending on Linux and macOS.
Ordinary Apple/Rust lanes remain visible in CI and are deferred from this scoped
release. Published asset verification is pending publication and download checks.
Windows, additional native qualification and external consumer acceptance remain
deferred.

The completed local restricted native milestone below is carried forward with
v0.6.2's narrow Vitest inventory-validation ordering correction; accepted runtime
contexts do not change. Its historical sources, counts and 139-payload cleanup
proof remain separate from hosted gates and any later download scratch cleanup.

# v0.6.0 historical local release candidate

All finite local Go/TypeScript release gates passed. The failed signed v0.6.0
tag is preserved; no v0.6.0 release was published.
The restricted Go profile uses Go 1.27.1 on macOS ARM64 and Linux ARM64.
TypeScript/Vitest uses the fixed Linux ARM64 runtime from either controller.
The two AMD64 archive targets are build-only; Windows packaging and runtime
qualification are not part of this release.

Runtime acceptance at source commit `13084875` used the 48-file projection
`a6cfa9bd`, freezing 312 code files and 519 donor entries. The later packaging
declaration delta is attributed separately below. The shared coordinator, private producers, complete inventories,
authenticated reuse, full original fallback and transactional enrollment passed
independent source review. See [supported profiles](go-typescript-native.md)
for the exact syntax, runner, toolchain and platform limits.

The current full repository gates passed and were independently reviewed:

| Gate | Seconds | Result |
| --- | ---: | --- |
| `go test -count=1 -p=1 ./...` | 853.753 | 38 passing packages, three packages without tests; 2,030 passing test terminals |
| `go test -race -count=1 -p=1 ./...` | 1508.262 | Same package/test counts; no race reports |
| `go vet ./...` | 2.272 | Exit zero, empty output |

Each repository test run also records 204 optional test skips. The mandatory
installed-runner phases require every specified terminal to pass without skips;
repository skips do not count as native acceptance. The core source, SDK, tools
and exact two offline module guards matched before/after, and all four owned
process groups drained. Core aggregate: `83252cb5`.

Current-source TypeScript normal acceptance passed on both controllers:
Linux in 1339.400 seconds and macOS in 1163.187 seconds. Each has eight mandatory
acceptance terminals, 32 measured CLI steps and three empty joined container
inventories. Both results are independently reviewed. The controller stopped
before the first race result; its cause is unknown. Its aggregate `cfb641e9`
remains interrupted, and no missing result is inferred from an empty log.
A separate finite drain closure confirms its process group is absent, no task
containers remain and unrelated containers are preserved.

A reviewed continuation completed the six missing phases in a new owner:

| Gate | Seconds | Mandatory terminals / measured CLI steps |
| --- | ---: | ---: |
| Linux TypeScript race | 1161.659 | 8 / 32 |
| macOS TypeScript race | 1057.985 | 8 / 32 |
| Linux source certifier normal | 79.904 | 61 / 0 |
| Linux source certifier race | 194.073 | 61 / 0 |
| Linux targeted vet | 8.081 | 0 / 0 |
| macOS targeted vet | 5.315 | 0 / 0 |

Every required terminal passed without skips, failures or race reports. Each
phase joined its command and three empty post-run container inventories. Current
source, SDK, tool and exact offline module guards matched the completed normal
results before carrying them and matched again after execution. All 33 helper
commands joined with exit zero; final inventories are empty and detached
PID/PGID 95373 is absent. Independent review `43be6f27` accepts the separate
logical eight-phase proof `9c17b938`; fresh-six aggregate `883ae328` passed with
no post failures. Interrupted aggregate `cfb641e9` remains unchanged.

The whole matrix is 21 completed obligations: three current core gates, eight
current native checks and ten explicitly impact-qualified historical behavior
checks. The ten are eight Go CLI normal/race checks across both hosts and two
macOS source-certifier checks. The changed symlink-copy branch is unreachable
with their authored inputs; independent source review and matching current
relevant inputs support this reuse. Root composition `8e312b1f` records exactly 304 mandatory native
acceptance terminals and 164 measured typed Go/TypeScript CLI steps. Terminal
counts include parent and nested acceptance tests; they are not 304 separate
public CLI scenarios. Race runs instrument the harness/core/providers while the
CLI under test is compiled normally.

Measured fixture costs do not establish representative speedup. The current
TypeScript lifecycle cold/warm costs are 52.285/26.192 seconds on Linux and
58.725/30.042 seconds from macOS. The Linux forced full audit costs 1.271 seconds;
these tiny fixtures run faster in full than through qualification and reuse.
All capture/build/collection/audit costs are included in CLI step timings.

Earlier failed attempts and their immutable logs are retained as history.
They exposed an acceptance-helper scope error, a read-only Linux module cache,
a missing task-owned Linux client, and copied symlink permission differences.
The portable copy fix preserves source manifests and exact file/directory
permissions while checking link type, target and containment. Its normal/race
regressions and vet passed. A separate core-driver environment error was repaired
without changing product behavior. Failed and interrupted aggregates remain
unaccepted even when individual completed results are relevant.

Canonical immutable staged Mori passed before signed local source commit
`13084875a529da4fdbf7fe984b90bbdc060630ed`: production and test scans analyzed
310/310 supported files and 49/49 changed supported files, with no warnings,
parse diagnostics or truncation. Similarities are advisory; they do not qualify
omission. Independent root-composition review `935c1d21` accepts all 21 gates.

The first archive attempt failed while compiling Windows because existing native
enforcement uses Unix-only APIs. Its failed aggregate `eb09a8a9` and all joined
command logs remain unaccepted history. The ordinary committed archive builder
now lists only the four macOS/Linux targets. This packaging declaration is the
sole Go source change after runtime acceptance; every function body, import and
type is unchanged. Focused packager normal/race/vet passed in 1.883/3.607/0.303
seconds with unchanged runtime source pins and drained command groups. The
release workflow uses this ordinary builder; no overlay is required.

The ordinary offline builder at signed source commit
`ad6c4ced88c29e89b37f25587be88ecaf2ed03d0` produced four macOS/Linux archives.
Build aggregate `7550e7a4` and receipt `4bcd8d6b` passed. Each archive contains
43 members: the binary and 42 committed documents. Checksums, target headers,
archive metadata and all four embedded build records passed. Extracted macOS
ARM64 and Linux ARM64 version/help/catalog smokes passed; Linux kernel and
non-root UID controls passed. AMD64 remains build-only. Verifier `d1eeea2f`
records 36 joined successful commands, matching source/artifact/tool guards,
five removed owned containers and an empty final inventory. Independent
artifact review `478231e4` accepts the build, smokes and read-only drain.

Private release-helper failures remain immutable unaccepted history: a build-info
check initially rejected the correct commit-derived Go module version, and the
Docker launcher initially used OrbStack's internal binary name and a private
configuration missing its context metadata. Exact module/time checks, the
selected Docker alias and a credential-free context for the existing local daemon
corrected those checks. Product runtime code and accepted runtime gates were
unchanged; the passed build was reused.

All 139 obsolete task-owned payloads were removed after eight complete preflights
and a cross-scope uniqueness/overlap check. Cleanup removed 10,115,891,346 logical
bytes (9.42 GiB); directory allocation totals are 10,882,138,112 bytes and do not
establish an equivalent APFS free-space gain. All eight cleanup helpers joined
successfully, original paths are absent and no quarantine remains. Release
archives, proof metadata, source worktrees and deferred language inputs remain.
Independent cleanup review `8880e921` rechecked all removals, helper joins,
retained copies and unchanged final assets.
The retained index before deletion contains 921 byte-pinned records; original
mandatory provenance copy fields are preserved. Earlier refused preflights are
retained, including a strict file-mode metadata mismatch corrected before deletion.

Runtime source is `13084875`; archive source is `ad6c4ced`. This later documentation
closeout does not change the archived runtime bytes or rebuild the already
accepted artifacts. The v0.6.2 publication preparation status above supersedes
this candidate's external handoff status; the records here remain local historical
evidence. Tiny fixtures establish no representative speedup. Published v0.5.1
evidence below does not qualify this candidate.

# v0.5.1 release checks

## Local source acceptance, 2026-10-05

Full `go test -count=1 ./...`, `go test -race -count=1 ./...` and
`go vet ./...` passed on Darwin arm64 with Go 1.27.1. Installed Apple
fixture checks use Xcode 27.0 / Apple Swift 6.4 and explicitly enabled native
SwiftPM/Xcode tests. Independent cases cover target impact, full commands,
shadow runtime misses, new/deleted resources, skipped cases, assertion failures,
source immutability and malformed/conflicting native results. Generic Node
checks preserve full execution and reject interpreter/dependency drift.

Release CI explicitly selects installed Xcode 26.3 for Apple fixtures, matching
Neiro's intended CI line. Published workflow results are recorded below.
Actionlint and diff/format checks pass. Native omission remains disabled.

Mori 0.35.0 (65f1dba5fde6, schema23/normalization14) supplied advisory review:
`mori scan --profile review --threshold 0.85 --min-tokens 40 --max-groups 250
--max-occurrences 10 --require-coverage --changed-since HEAD --format agent
--output REPORT.json .`. `.gitignore` was honored with no project config,
baseline or suppression. All 119 supported files and 22 changed supported files
were analyzed: 12 focused groups, 90 total location pairs, no warnings or parse
diagnostics, and no truncation. Seventeen distinct source ranges were inspected;
fixture/Git helpers are intentional small similarities, and Swift/Cargo walks
have independent ownership and containment contracts. Thirteen files have no
retained fragments (one type-only file, twelve below the token floor). Unsupported
Markdown, workflows and Xcode project assets were checked separately.

The canonical immutable staged Mori check used the same bounds with
`review staged check --policy advisory`: all 119 supported files and 24 changed
files were analyzed, with 12 focused groups / 15 location pairs, no warnings,
complete analysis and passed advisory policy. Working-tree and untracked
inclusion were false. No finding was suppressed or acknowledged.

The initial v0.5.0 tag did not publish: the new Xcode 26.3 job rejected a newer
xcresulttool schema and a compiler above the original executable-size limit.
v0.5.1 corrects both, prepares bound empty Xcode package/workspace metadata
directories and keeps that failed tag immutable.
The v0.5.1 compatibility patch again passed full tests/races/vet and the native
Xcode race fixture locally. Its Mori changed-code scan covered 8/8 supported
changed files and 119/119 supported files with zero focused groups, no warnings
or parsing diagnostics, and no truncation.

The final metadata correction again passed full tests/races/vet and installed
Apple fixtures. Mori covered 3/3 changed supported files and 119/119 supported
files with zero focused groups, no warnings or parsing diagnostics, complete
analysis and passed immutable staged advisory policy.

## Published acceptance, 2026-10-05

The signed `v0.5.1` tag binds source
`2e1eca85736eda5d55f7a50bd08a200aa0b829f3`.
[Main CI](https://github.com/Cyberlane/hayaku/actions/runs/37275495026) passed
all 13 jobs. The [release workflow](https://github.com/Cyberlane/hayaku/actions/runs/37276129391)
independently passed all 13 verification jobs and publication, including installed
Xcode 26.3 / Swift 6.2 fixtures, full tests/races/vet and the existing native
runner/WASI checks. The [public release](https://github.com/Cyberlane/hayaku/releases/tag/v0.5.1)
contains six archives, `SHA256SUMS` and `manifest.json`.

All six downloaded public archives matched their checksum/size/source manifest
and contained Apple documentation and license notices. Neiro's checksum-pinned
installer extracted the public Darwin arm64 CLI; its embedded Go build information
reports module v0.5.1, the exact tagged source and `vcs.modified=false`. Version and
catalog checks passed. Publication smoke-tested the extracted Linux amd64 CLI;
other platforms have cross-compilation coverage rather than native acceptance.

Local committed-Neiro source acceptance on Darwin arm64 / Xcode 27.0 reconciled
253 Swift XCTest cases across 11 test targets, all 34 app/UI cases (20 app, 14 UI)
on iPhone 16 Pro Max / iOS 26.3, and four full service/protocol commands with bound
Node 26.5.0 inputs. These source-CLI checks retain full suites and do not establish
native omission, representative savings or hosted Neiro CI acceptance. Neiro's
public-binary wrapper acceptance is a separate consumer check.

# Historical v0.4.0 release checks

## Local source acceptance, 2026-10-03

On macOS arm64 with Go 1.27.1, the final `go test -count=1 ./...`,
`go test -race -count=1 ./...` and `go vet ./...` passed. Native variables supplied
installed Node 22.18.0, Python 3.14.7, pytest 9.0.2, Jest 30.5.2,
Playwright 1.63.0 and cargo-nextest 0.9.146. Node 25.2.1 additionally passed its
native fixture checks. Vitest adapter/application checks passed against all three
pinned Vitest/Vite pairs: 3.2.7/6.4.3, 4.1.11/7.3.1 and 4.1.11/8.1.5.

Coverage includes native source/TS changes, dynamic-resource failures, repeated
case identities, terminal errors, unchanged full commands, original Python
bytecode behavior, real nextest inventory/JUnit reconciliation, generated/linked
input mutation, rejected effects, receipt forgery, forced audits, resource limits
and cancellation. The final inventory and generated-link CLI regressions also
cover macOS physical/alias paths. These fixtures are independent challenges, not
proof that arbitrary native dependencies are complete.

Actionlint validated CI/release workflows, Go formatting and diff checks passed,
and local documentation links/JSON examples were checked. The existing site was
rendered at desktop and 390px mobile widths with its branding preserved and no
page overflow.

Mori 0.35.0 supplied advisory source/test review. The v0.3.0-to-development scan
analyzed all 49 changed supported files and 107/107 supported files, with 27
focused groups, no warnings/parse diagnostics and no truncation. Both source
ranges and context were inspected: independent runner scope/path contracts,
standard sorting/encoding, and distinct test oracles explain the retained matches.
The final immutable CLI/pilot staged check bound HEAD/index with working-tree and
untracked inclusion false: 9/9 changed supported files and 107/107 supported files,
10 focused groups/13 location pairs, complete analysis and passed advisory policy.
Threshold 0.85, 40-token floor and bounded 250-group/50-occurrence retention were
explicit. Ten zero-fragment files were either the data-only model or tiny helpers
and fault fixtures below that floor. Earlier commit slices had their own complete
reviews. No baseline, receipt, suppression or weakened coverage policy was added.

## Published acceptance, 2026-10-03

The signed `v0.4.0` tag binds source
`941d99d8ad5b0b31eb04bfcf1f9212966f8f4a62`.
[Main CI](https://github.com/Cyberlane/hayaku/actions/runs/37036798768) passed
all 12 jobs. The [release workflow](https://github.com/Cyberlane/hayaku/actions/runs/37037220777)
independently passed its 12 verification jobs plus publication, including the
three-pair Vitest matrix, installed Node/Python/Jest/Playwright/nextest fixtures,
full Linux/macOS tests/races/vet and actual WASI execute/reuse pilots.
The [public release](https://github.com/Cyberlane/hayaku/releases/tag/v0.4.0)
is published with six archives, `SHA256SUMS` and `manifest.json`.

All six downloaded public archives matched their checksum/size/source manifest
and contained the expected documentation and Go/wazero/platform license notices.
The extracted Darwin arm64 CLI reports 0.4.0, module version v0.4.0 and the exact
unchanged tagged source in its embedded Go build information. Native downloaded
checks passed catalog, real capsule execution/authenticated reuse/forced audit,
changed-input invalidation and denied caught writes with unchanged host inputs.
Its clean tagged-source self-pilot independently passed matching executed/reused
outcomes with fresh compiler caches. Private raw reports are retained; these are
functional acceptance samples, not representative savings.

The publication job smoke-tested the extracted Linux amd64 CLI. Other targets
have cross-compilation coverage; Windows WASI receipt reuse remains disabled.
[Pages deployment](https://github.com/Cyberlane/hayaku/actions/runs/37036798657)
passed, and the live website rendered v0.4.0 with its logo loaded, resolved local
anchors and no mobile page overflow. Native affected-only gates remain disabled.
Consumer activation and consumer CI acceptance are separate from this release.

## Historical v0.1.0 release checks


Local source checks used Go 1.27.1 on macOS arm64. The complete test suite,
race suite and `go vet ./...` passed. A packaging regression checks exact document
contents/modes and rejects archives missing either licensing file. Windows amd64
and arm64 cross-builds passed; these are compilation checks, not runtime acceptance.

The static website was rendered in a browser at desktop and 320/390px mobile
sizes. Its logo loaded, local anchors resolved, and document width did not overflow
after a decorative-background fix. Local Markdown links/assets and actionlint
workflow validation passed. The site needs no Node packages or build step.

Mori 0.35.0 (`65f1dba5fde6`, schema 23, normalization 14) supplied advisory source
and test similarity review. The changed-code scan used:

```sh
mori scan --profile review --threshold 0.85 --min-tokens 40 \
  --max-groups 250 --max-occurrences 10 --require-coverage \
  --changed-since HEAD --format agent --output REPORT.json .
```

It analyzed all 50 supported files and all three changed supported files, with
zero focused groups, no warnings/parse diagnostics and no truncation. Nine
supported files lacked retained fragments at this floor. Documentation, workflow,
image and website assets are unsupported comparison inputs and were checked
separately. `.gitignore` was honored; no config, suppression or receipt was added.

The immutable staged check used:

```sh
mori review staged check --policy advisory --require-coverage \
  --format agent --output REPORT.json .
```

With its default 0.70 threshold and 12-token floor, all 50 supported files were
analyzed; 45 contributed fragments and five had no boundaries or were below the
floor. The final expanded-document bundle retained zero focused groups or pairs,
no warnings/parse diagnostics, no truncation, and complete analysis with a passed
advisory policy. Working-tree and untracked inclusion were false. An earlier
packaging snapshot retained six test scaffolding pairs: both ranges and context
of all seven distinct identities were inspected. Those matches exercised
independent distribution, adapter, clone, seeded-fault and digest oracles.
No finding was suppressed or acknowledged.

GitHub release publication separately requires the full Linux amd64 and macOS
arm64 CI test/race/vet checks and exact tag/version agreement. The archive pipeline
checks all six checksums, build provenance, extracted Linux CLI version and license
notices before publishing. Live workflow runs and release assets are the source
of truth for remote success. These checks never authorize production test omission.
