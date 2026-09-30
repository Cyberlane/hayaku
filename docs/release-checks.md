# v0.1.0 release checks

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
