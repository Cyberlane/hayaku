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

## Recorded development progress

The [2026-10-06 provisioning checkpoint](evidence/runner-provisioning-2026-10-06.json)
retains official archive/package identities, verified digests, private fixture
lock identities and baseline results. It is a dated setup record, not a portable
installed tool closure or qualification credential. Temporary paths are locations
used for those observations; future invocations must verify their actual bytes.

At this checkpoint, macOS original full-command healthy and seeded runtime-fault
baselines passed for Maven/JUnit Jupiter, Maven/TestNG, Gradle/JUnit Jupiter,
Gradle/TestNG, xUnit, NUnit, MSTest, RSpec, Minitest, PHPUnit, Bun, Deno, Dart/package:test,
GoogleTest, Catch2, CTest, Flutter widget tests, ScalaTest, MUnit and Bazel C++
(20 of 20 named profiles). Linux Maven/JUnit Jupiter, Maven/TestNG,
Gradle/JUnit Jupiter, Gradle/TestNG, xUnit, NUnit, MSTest, RSpec, Minitest,
PHPUnit, Bun, Deno, Dart/package:test, GoogleTest, Catch2, CTest, Flutter widget
tests, ScalaTest, MUnit and Bazel C++ baselines also passed (20 of 20).
Rust/Nextest baselines passed on actual macOS and Linux, and all four exact
Vitest/Vite pairs passed macOS baselines. The Deno record distinguishes an earlier type-check
failure from the corrected runtime assertion failure. The initial Catch2 exit
oracle, signed NuGet archive/content-hash distinction, PHP extraction/configuration
setup failures, Bundler version-display/frozen-option setup failures and Linux Dart cache-write setup failures are retained separately from
the corrected passing baselines. Other selected SDKs and
frameworks remain at the explicit setup or baseline stage recorded in the file.
No candidate in this checkpoint gains permission to omit tests.

The original PHP baselines cover the bundled PHPUnit PHAR. Separate macOS/Linux
Composer 2.10.3 PSR4, zero-package lock and bootstrap variants passed healthy and
seeded-failure controls on 2026-10-07 with PHP 8.4.24 and PHPUnit 12.5.38. The
private macOS PHP/OpenSSL 3.5.9 construction and bounded driver controls also
passed; Linux used its separately recorded OpenSSL 3.0.20. These two additional
variants retain exact native exits and case outcomes, stable lock/tool identities
and joined cleanup. They leave the original 40 named framework contexts unchanged,
cover no third-party Composer packages and grant no runtime qualification. The
checkpoint records their supplementary evidence separately.

Separate Maven metadata helper controls passed offline on macOS and Linux on
2026-10-07, using pinned maven-help-plugin 3.5.2 and maven-dependency-plugin 3.11.0.
The final private repository contains 206 coordinates and 311 artifacts verified
against official Maven Central SHA1 sidecars and additionally bound by local
SHA256 digests. Both platforms produced the same ordered eight-entry native
classpath. Two earlier offline resolution failures and all three immutable
repository generations remain recorded. The original repository and SDK bytes
were unchanged; processes joined and task-created guests were removed. These
checks establish metadata helper setup, leave the original 40 named framework
contexts unchanged and grant no source, runtime, provider or omission authority.

A separate parser SDK observation added on 2026-10-07 records TypeScript 6.0.2,
its official npm metadata, publisher SHA512/SHA1 archive identities and all 140
privately extracted file digests. The explicit legacy compiler API profile is
used with pinned Node 22.18.0 for source certification; newer compiler APIs are
separate unsupported contexts. All ten required source-candidate cases passed
macOS normal/race checks and Linux checks, with no skips and verified cleanup.
This observation leaves the original 20 macOS and 20 Linux framework baseline
counts unchanged and grants no framework/runtime or receipt authority.

Linux Flutter is a derived ARM development candidate assembled from the pinned
official framework and engine components. The official SDK archive supplies the
unchanged Flutter tool lock and platform-neutral Gradle wrapper prerequisites;
the record does not claim an official prepacked ARM SDK or Gradle execution.
All 111 distinct tool/widget package archives were publisher-verified before
private extraction and checked again after the offline healthy/fault controls.
The missing shipped lock, cache sidecar serialization, universal wrapper setup
and private preflight comparison failures are retained as setup failures.
