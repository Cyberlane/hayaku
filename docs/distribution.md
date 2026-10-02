# Distribution and releases

v0.4.0 adds deterministic WASI suite execution and authenticated whole-suite reuse, explicit generated/linked input envelopes, native runner integrations and cost comparison tools. Native affected-test proposals retain the original full suite; a WASI result does not qualify an equivalent native command. Download binaries from [GitHub releases](https://github.com/Cyberlane/hayaku/releases).

## Local archive builder

From a clean committed checkout, build the archive builder outside the repository, then choose a new output directory whose parent exists:

```sh
go build -o /tmp/hayaku-dist ./cmd/hayaku-dist
/tmp/hayaku-dist --root "$PWD" --output /tmp/hayaku-archives
```

The builder cross-compiles Darwin, Linux and Windows binaries on amd64 and arm64 with CGO disabled. It rejects dirty tracked, untracked and ignored source inputs, binds the exact commit and source timestamp, checks embedded Go build information, and revalidates the checkout after building. Pinned modules must already be available in the trusted build environment: dependency fetching and toolchain installation are disabled during archive construction. Output is built in private staging; an existing destination is not replaced.

Archives include the CLI, README, capability and safety documentation, the [MIT license](../LICENSE) and [third-party notices](../THIRD_PARTY_NOTICES.md), including the Go runtime, wazero and platform support modules. `manifest.json` records tool identity, schema/implementation versions, capabilities, platform and archive sizes/checksums. Verify with `shasum -a 256 -c SHA256SUMS` after downloading all listed archives. Checksums are integrity evidence, not publisher signatures.

Archive ordering, modes and timestamps are fixed. Timestamps derive from the source commit, also passed as `SOURCE_DATE_EPOCH`. Rebuild comparisons require the same pinned Go toolchain, tagged source and module metadata; use a fresh isolated `GOMODCACHE`. Cross-compilation and build-info checks do not prove native execution or downloaded-artifact acceptance on another platform.

## Public release workflow

The workflow accepts strict `vMAJOR.MINOR.PATCH` tags, runs required CI and verifies that the CLI version matches the tag. Matching release notes are required. It builds six archives from the exact clean tag with Go 1.27.1, checks their checksums, and smoke-tests the extracted Linux amd64 binary. The required CI matrix also exercises Linux amd64 and macOS arm64 source fixtures. Publication starts with a draft containing all assets before making the release public. See [release checks](release-checks.md) for actual remote acceptance evidence.

Other advertised targets have cross-compilation evidence unless separately recorded. Binaries are not signed or notarized. Neither the local builder nor publication enrolls a consumer's CI or qualifies native omission. [Capsule reuse](capsules.md) applies only to its own deterministic WASI contract; Windows capsules execute without receipt caching until an independently qualified ACL backend exists.
