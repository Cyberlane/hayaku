# Distribution and releases

v0.1.0 is the first public release of the conservative planning and shadow
foundation. Production affected-test omission remains disabled. Download binaries
from [GitHub releases](https://github.com/Cyberlane/hayaku/releases); release notes
describe features and maturity boundaries for each version.

## Local archive builder

From a clean committed checkout, build the local archive builder outside the
repository, then choose a nonexisting output directory whose parent exists:

```sh
go build -o /tmp/hayaku-dist ./cmd/hayaku-dist
/tmp/hayaku-dist --root "$PWD" --output /tmp/hayaku-archives
```

The builder cross-compiles dependency-free Hayaku binaries for Darwin, Linux and
Windows on amd64 and arm64 with CGO disabled. It rejects dirty tracked, untracked
and ignored source inputs, records the exact commit and source timestamp, checks
the embedded Go build information for source/platform identity, and revalidates
the checkout after building. Go dependency fetching and toolchain installation
are disabled. It creates archives in a private staging directory, then moves the
completed output to the requested destination. Existing output is not replaced.

Archives include the CLI, README, safety/qualification/distribution documentation,
the project's [MIT license](../LICENSE) and [Go runtime notices](../THIRD_PARTY_NOTICES.md).
`manifest.json` records Go tool identity, plan schema, implementation versions,
capabilities, platform and archive sizes/checksums. `SHA256SUMS` can be checked
locally with `shasum -a 256 -c SHA256SUMS`. Archive ordering, modes and timestamps
are fixed; timestamps derive from the source commit, also passed as
`SOURCE_DATE_EPOCH`. Reproducibility assumes the same pinned Go toolchain and
source. Checksums are integrity evidence, not signatures or trust attestations.

Local cross-compilation and embedded build-info checks do not prove native
execution on another platform or acceptance of a downloaded artifact. Verify
the archive checksum, extraction, CLI version and representative native fixture
commands on each advertised platform before calling it qualified. No signatures,
public upload, release, deployment or CI rollout are performed by this command.

## Public release workflow

The prepared GitHub workflow accepts strict `vMAJOR.MINOR.PATCH` tags, runs the
required CI checks and verifies that the CLI version matches the tag. A matching
release note file is required. It builds all six archives from the exact clean
committed tag with Go 1.27.1, checks every archive checksum, and smoke-tests the
extracted Linux amd64 CLI version and bundled license notices. Publishing starts
with a draft containing the complete assets, then makes that release public.

Release verification runs on Linux amd64 and macOS arm64; other platforms have
cross-compilation evidence only. This does not establish native downloaded-artifact
acceptance for every target. Neither the local builder nor the public workflow
signs or notarizes binaries. Checksums and the manifest provide integrity and
source/build provenance; they are not publisher signatures. Release publication
never enables production affected-test omission or enrolls a consumer's CI.
