# v0.5.0 release checks

## Local source acceptance, 2026-10-05

Full `go test -count=1 ./...`, `go test -race -count=1 ./...` and
`go vet ./...` passed on Darwin arm64 with Go 1.27.1. Installed Apple
fixture checks use Xcode 27.0 / Apple Swift 6.4 and explicitly enabled native
SwiftPM/Xcode tests. Independent cases cover target impact, full commands,
shadow runtime misses, new/deleted resources, skipped cases, assertion failures,
source immutability and malformed/conflicting native results. Generic Node
checks preserve full execution and reject interpreter/dependency drift.

Release CI explicitly selects installed Xcode 26.3 for Apple fixtures, matching
Neiro's intended CI line. Its workflow definition is not remote acceptance.
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

Publication and downloaded-artifact acceptance will be recorded after release.
Neiro consumer activation and CI acceptance remain separate checks.

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
