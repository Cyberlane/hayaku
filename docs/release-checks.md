# v0.4.0 release checks

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

## External acceptance

Publication requires the new Linux/macOS full test/race/vet, three-pair Vitest,
installed native-runner and WASI pilot matrix; the actual run is separate from its
workflow definition. The release then builds six archives, validates source/tag
identity, checksums and license notices, and smoke-tests the extracted Linux CLI.
Downloaded public archive acceptance and consumer version pins are recorded
separately once publication completes. Windows has cross-compilation coverage;
WASI receipt reuse remains disabled there. No native affected-only gate is enabled.

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
