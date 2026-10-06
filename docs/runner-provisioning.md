# Isolated acceptance runner provisioning

Prepared 2026-10-06 for the macOS/Linux implementation fleet. The user confirmed
there are no existing environments for the missing language contexts. The user
explicitly approved this development-only provisioning scope on 2026-10-06.
It does not instruct Hayaku to install runners during discovery or
execution. Candidate identities and actual setup results are recorded separately
from runtime qualification and acceptance.

## Scope and locations

Use a newly created `/tmp/hayaku-language-acceptance-*` directory with private
permissions for SDK archives, unpacked tools, authored fixtures and private
package caches. Linux fixtures use disposable containers with explicitly named
temporary volumes; macOS fixtures use absolute tool paths and command-local
environment overrides. Reuse already installed immutable images where possible.
Do not install global packages, change shell startup files, mount credentials or
consumer repositories, modify existing services, or launch hosted CI jobs.

Provision only the frameworks already contracted in
[language-contexts.md](language-contexts.md):

| Context | Development tools and dependencies | Trusted provisioning sources |
| --- | --- | --- |
| JVM | JDK, Maven, Gradle, sbt; JUnit Jupiter, TestNG, ScalaTest, MUnit | Official JDK distribution, [Apache Maven](https://maven.apache.org/download.cgi), [Gradle releases/checksums](https://gradle.org/release-checksums/), [sbt downloads](https://www.scala-sbt.org/download/), Maven Central |
| .NET | Existing SDK image where viable; xUnit, NUnit, MSTest and their configured test SDK/adapters | [Microsoft SDK images](https://learn.microsoft.com/en-us/dotnet/core/docker/build-container), NuGet |
| Ruby | Existing Ruby/Minitest; isolated Bundler/RSpec fixture dependencies | RubyGems |
| PHP | PHP, Composer, PHPUnit and required extensions | Official PHP distribution, Composer, Packagist; [PHPUnit distribution](https://docs.phpunit.de/en/12.5/installation.html) |
| Dart/Flutter | SDK archive; package:test and flutter_test fixtures | [Dart SDK](https://dart.dev/get-dart), [Flutter stable archive](https://docs.flutter.dev/install/archive), pub.dev |
| C/C++ | Existing CMake/compiler where viable; Ninja, GoogleTest, Catch2 | Official tool/framework release repositories |
| Bun | Existing Bun; isolated fixture dependencies if required | Bun's official package/release sources and npm |
| Deno | Deno release binary or official container and already resolved fixture dependencies | [Deno distribution](https://docs.deno.com/runtime/getting_started/installation/), official module/package sources |
| Bazel | Bazel binary, named C/C++ rules/toolchains and fixture dependencies | [Bazel distribution](https://bazel.build/install), Bazel Central Registry and official rule repositories |
| Rust | Existing Cargo/rustc; cargo-nextest | Official cargo-nextest release repository |
| Vitest compatibility | Isolated lockfile installations for the three older exact pairs and a locked 4.1.11/8.2.2 fixture | npm, using the existing fixture manifests; no consumer package scripts |

## Provisioning and acceptance sequence

1. Select explicit compatible stable releases from the listed official sources.
   Record version, platform, archive/image identity and published checksum or
   signature metadata before unpacking or execution. Missing provenance is a
   setup failure. Preserve the selected record; never silently select a newer
   release on a later run.
2. Download only into the dedicated private setup directory. Verify archive
   checksums before unpacking and use immutable image IDs for container runs.
   An SDK or runner is still an unqualified development candidate at this point.
3. Author small framework fixtures, resolve their dependencies in private caches,
   and save exact lockfiles plus content identities. Provisioning may use network
   access; subsequent Hayaku discovery, build and acceptance invocations must use
   the bound installed closure offline. Missing bytes fail rather than fetch.
4. Observe actual installed versions, hash executable/library/plugin bytes and
   execute independent full-command baseline, discovery and seeded-failure tests.
   Populate each named profile's admission record with those observed facts.
5. Run implementation-specific adversaries and the shared acceptance harness.
   Neither installation success nor a passing baseline qualifies skipping.
   Unsupported scopes and incomplete tool or runtime closure retain full runs.

The request is authorization for this bounded setup process and its necessary
official dependency downloads, rather than permission for unspecified global
installation. Exact acceptance pins cannot be invented before the tools and
resolved fixture bytes are inspected. Windows, device installation, production
repositories, publishing, deployments and external messages are outside scope.

Remove only directories, containers and volumes created by this provisioning
task after their evidence is captured. Retain pinned fixture manifests and
redacted version/digest/results evidence in the project; retain no credentials,
raw environment values or source contents in reports.
