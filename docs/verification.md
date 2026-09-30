# Historical local verification, 2026-09-30

These observations concern private prepublication development revisions, rather
than the public v0.1.0 source or downloaded release assets. The private source
history and raw reports are not published; this is a historical evidence summary,
not a reproducible public dataset or release acceptance record. Hayaku itself was
the first pilot. Current capabilities and unfinished work are mapped in
[implementation status](implementation-status.md). Required production commands
always retain the original full suite. Release-specific checks are described in
[distribution](distribution.md) and [release notes](releases/v0.1.0.md).

## Checks and independent challenges

On Darwin arm64 with Go 1.27.1, `go test ./...`, `go test -race ./...` and
`go vet ./...` pass. Bounded fuzz runs exercised the independent graph oracle, planner monotonicity,
snapshot paths and qualification validation; strict configuration fuzz seeds also
ran with the standard suite. Native Go and Cargo fixtures passed. Vitest 4.1.11 file/project/filter
fixtures passed using an existing installed runner on disposable sources;
the external project's sources, configuration and tests were untouched. Native
pytest explicitly skipped because the installed Python did not provide it.

Independent reviews covered snapshots/qualification/distribution, native adapters,
planner/CLI behavior and the research constraints. Independent seeded faults and a
runtime file-read counterexample are retained. The latter makes a graph-only
proposal miss a failure that the original full suite catches; shadow reports the
miss and cannot turn the failing suite green. No clean finite sample is a proof
of complete influence.

Additional regressions cover malformed/tampered plans, a valid saved-plan round
trip and execution, ignored/raw-byte checkout drift, missing terminal outcomes,
compiler failures, bounded-output overflow, inherited/persisted hidden Go flags,
effective compiler settings, changed environment/tool bytes, and physical/relative
repository aliases. The Darwin pilot explicitly pins reviewed launcher environment
inputs; arbitrary environment changes remain privately bound and rejected.

## Local artifact acceptance

Six local archives were built from a private prepublication development revision:
Darwin/Linux/Windows on amd64/arm64. All SHA-256 checksums were independently
verified; embedded source/platform build information was checked by the builder.
Source provenance was recorded in their `manifest.json`. Those temporary artifacts
are unpublished and unsigned; they are separate from the v0.1.0 release assets.

The extracted Darwin arm64 CLI passed version, saved-plan construction, full-suite
execution and shadow comparison against the same unchanged candidate. All 19
required package outcomes completed; the empty change proposal had no commands,
the full audit passed, and no misses or conflicts were reported. This tests the
installed local archive path, not a downloaded public release or another platform.
Other platforms have cross-build evidence only. The separate two-pair synthetic
[pilot observations](pilot-results.md) describe a separate, earlier private source.

## Mori review evidence

The installed Mori executable was version 0.35.0, revision
`65f1dba5fde6`, normalization 14, schema 23. Project `.gitignore` was honored;
no project Mori config, baseline, receipt or feedback policy was introduced.
Source remained local. Complete bounded reports are retained under `.git`.

Final production scan:

```sh
mori scan --profile review --threshold 0.85 --min-tokens 40 \
  --max-groups 250 --max-occurrences 10 --exclude '**/*_test.go' \
  --require-coverage --format agent --output .git/mori-final-context.json .
```

This analyzed 28/28 supported files (27 Go, one shell), with 23 fragment files,
five zero-fragment files, zero warnings/parse diagnostics/generated exclusions,
12 groups and 17 location pairs from 782 compared candidates. Nothing was
truncated. The model file has no function boundaries; four tiny seeded-fault
files were below the token floor. Twenty-three documentation/configuration assets
were unsupported by the comparison parser. Supported coverage is not entire
repository coverage, and test code was explicitly excluded from this scan.

Canonical immutable staged review for the final code fix:

```sh
mori review staged check --policy advisory --require-coverage \
  --format agent --output .git/mori-staged-context.json .
```

Its default threshold was 0.70 with a 12-token floor. The private report bound the
exact development HEAD and index digest; working-tree and untracked inclusion
were false. All 5/5 supported changed files
and 50/50 supported repository files, including tests, were analyzed. There were
45 fragment files, the same five zero-fragment files, zero warnings/parse
errors/generated exclusions and 19 groups/19 location pairs from 477 candidates.
Policy passed, analysis was complete and output was not truncated. No finding was
silently acknowledged or hidden.

Both source ranges and surrounding context were inspected for all 25 distinct
staged identities. Production matches were assessed as follows:

| Candidate family | Source assessment and decision |
| --- | --- |
| Provider `within` guards | Likely small duplication; retained at independent trust boundaries, with differing empty/root handling. A typed shared path utility is a future review, not inferred from scores. |
| Proposal loops | Intentional similarity; Go/Cargo package flags and pytest/Vitest file/project scopes differ. Retain independent adapters. |
| Bounded output buffers | Intentional shared limit check; native metadata and general runner cancellation/completion contracts differ. The promoted `ReadFrom` bypass was fixed and challenged independently. |
| JSON equality/rendering/digests | Structural resemblance; compare, stream and hash different outputs. Retain their different contracts. |
| Workspace identity/build/run/pilot loops | Intentional validation sequence; tool-byte identity, native environment, evidence discovery, execution and measured copies have different effects. |
| Sorting/signal wrappers | Expected small boilerplate; no semantic consolidation justified. |
| Test setup/mutate/assert shapes | Intentional similar scaffolding with independent positive, negative, context, path, cache and native-result oracles. Retain the distinct failure triggers. |

Mori supplied review leads only. Runtime tests and source inspection provided the
behavioral evidence; scores did not establish selection safety or authorize
production omission. Native Bazel/JVM/.NET/Apple graphs, pytest acceptance,
external-state isolation, fine cases, historical CI datasets, agreed performance
thresholds and production skipping remain open. v0.1.0 adds public MIT licensing
and release automation; binary signing and notarization are not provided.


## v0.2.0 local Vitest verification, 2026-10-01

The full Go suite, race suite and vet passed with explicit Node 22.18.0,
Vitest 4.1.11 and Vite 7.3.1 development fixtures on macOS arm64. Eight native
application fixtures cover TS aliases, required full execution, hidden filesystem
failure misses, resource/new/deleted test broadening, multiple projects and
duplicate names, failed hooks, ignored bound dependency installation plus CLI
execution/drift rejection, unhandled errors and setup changes. Protocol tests
challenge malformed/missing/unexpected/nonterminal results and graph gaps;
runtime-copy tests challenge unsafe symlinks, identity drift, mutation and limits.
The actual pinned fixture package lock is committed under `testdata/vitest-runtime`.
Native application tests skip explicitly without their environment variables;
release CI sets them and requires native checks on Linux and macOS.

The final changed-source Mori scan used version 0.35.0, revision `65f1dba5fde6`,
normalization 14/schema 23, profile review, threshold 0.85 and a 40-token floor:

```sh
mori scan --profile review --threshold 0.85 --min-tokens 40 \
  --max-groups 250 --max-occurrences 10 --require-coverage --changed-since HEAD \
  --format agent --output .git/hayaku-release-review/vitest-final.json .
```

It analyzed 63/63 supported files (61 Go, one JS bridge, one shell) and 25/25
supported changed files, with 54 fragment files and nine zero-fragment files
(the type-only model and eight small fault fixtures below the floor). There were
nine focused identities, 24 total groups/32 location pairs, zero warnings/parse
diagnostics/generated exclusions, and no truncation. Forty-four documentation,
configuration and asset files were unsupported by the comparison parser and
reviewed separately. The nine focused identities were inspected on both sides:
sort predicates, independent path guards, adapter ownership walks, normalization
and independent test scaffolds were retained as intentional small similarities.
No findings were suppressed or acknowledged. This review is advisory; native
results and safety regressions provide behavioral evidence.
