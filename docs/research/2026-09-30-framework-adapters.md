# Hayaku: framework adapters and existing tooling

Date: 2026-09-30. Documentation survey, not implemented or runtime-qualified support. Commands below illustrate documented runner capabilities; a real plan must preserve the project's pinned executable, wrapper, configuration, prerequisites and CI flags.

## Separate impact analysis from command rendering

The same source language can use several runners and build systems. A monorepository can contain several configurations of the same runner. Detection must inspect actual manifests, build/task configuration and existing CI commands; extensions are only hints. Ambiguous detection needs an explicit configured command or full fallback.

An adapter should provide a versioned contract for inventory, impact evidence, supported selection granularity, filter rendering, result validation and original full-run fallback. It must round a selected identity outward to a runnable container when exact filtering is unavailable. It must also preserve each lane: selecting a test does not authorize removing its other browser, architecture, feature or device configurations.

## Candidate adapter matrix

| Ecosystem | Documented command capability | Proposed initial unit and qualification concern |
| --- | --- | --- |
| Go | `go list -deps -test -json ./...`; `go test ./pkg/a ./pkg/b` | Whole packages plus reverse dependencies. Include external test imports, embedded resources, build tags and cgo. Import graphs alone do not capture arbitrary runtime inputs. [Go command reference](https://pkg.go.dev/cmd/go) |
| JS/TS, Jest | `jest --listTests --json`; `jest --runTestsByPath tests/a.test.ts tests/b.test.ts`; `--findRelatedTests` also exists | Whole test files/projects first. Static dependency selectors require additional handling for dynamic loading and non-module inputs. [Jest CLI](https://jestjs.io/docs/cli) |
| JS/TS, Vitest | `vitest list --filesOnly`; `vitest run tests/a.test.ts`; `vitest related --run src/a.ts` | Whole files/projects first. `related` misses computed dynamic imports; subprocess dependencies need explicit edges or broader runs. Positional path filters can overselect; validate discovered selections. [CLI](https://vitest.dev/guide/cli), [full-run triggers](https://vitest.dev/config/forcereruntriggers) |
| Python, pytest | `python -m pytest --collect-only -q`; `python -m pytest tests/test_a.py`; exact `file::class::test[param]` IDs are supported | Whole files/fixture-connected scopes first. Dynamic collection, autouse/session fixtures, plugins, resources and runtime imports need modeling. Argument files are version-specific (8.2+). [pytest invocation](https://docs.pytest.org/en/stable/how-to/usage.html) |
| Rust, Cargo | `cargo test -p crate_a -p crate_b`; `--test name` selects integration targets | Whole packages first. Keep unit, integration and documentation tests; explicit target flags can omit doc tests. Features, proc macros, build scripts, cfg and generated resources require context-specific dependencies. [Cargo test](https://doc.rust-lang.org/cargo/commands/cargo-test.html) |
| JVM, Maven Surefire | `mvn -Dtest=TestA,TestB test` | Module/suite scopes first. Provider-specific method syntax is not universal; include build/reactor prerequisites and separately preserve integration-test execution. [Surefire selectors](https://maven.apache.org/surefire/maven-surefire-plugin/examples/single-test.html) |
| C#, .NET | VSTest mode supports `dotnet test Tests.csproj --filter FullyQualifiedName=Namespace.Class.Method`; list-test options exist | Projects/classes first. Detect VSTest versus Microsoft.Testing.Platform and framework/version. MTP xUnit filters differ from MSTest/NUnit; project invocation also varies. No single universal `.NET` filter template. [Platform migration reference](https://learn.microsoft.com/en-us/dotnet/core/testing/migrating-vstest-microsoft-testing-platform) |
| Swift/ObjC, Xcode | Append `-only-testing:TestTarget/TestClass/testMethod` to the original `xcodebuild test` command | Test targets/suites first. Preserve scheme, test plan, destination, build configuration and resources; qualify Swift Testing/XCTest identities separately. [Apple command-line reference](https://developer.apple.com/library/archive/technotes/tn2339/_index.html) is archived; validate against the pinned current toolchain before support |
| Browser integration, Playwright | `playwright test --list`; current docs describe `--test-list file` | Whole test files/projects first. Backend and deployed artifact changes can affect tests with no JS import relationship. Preserve project dependencies and setup. Feature availability must match the installed version. [Playwright CLI](https://playwright.dev/docs/test-cli) |
| Multiple languages, Bazel | Graph queries identify reverse dependencies; `bazel test //a:test //b:test` executes targets | Configured build/test targets. Include rule/build/toolchain changes and external dependencies; graph access alone does not establish test hermeticity. [Query reference](https://bazel.build/query/language), [test contract](https://bazel.build/reference/test-encyclopedia) |

The “qualification concern” column is our design analysis, not a claim that the linked runner implements those protections. This list is an initial survey, not a complete support roadmap. Other Mori languages can initially be recognized while retaining their configured full-run commands.

Go already caches successful package tests under specific conditions, including some file/environment inputs; its build cache has documented cgo library limits. Benchmark Hayaku against warmed native caching as well as cold CI. Removing test invocations is not automatically a net improvement. [Go caching reference](https://pkg.go.dev/cmd/go).

## Existing systems worth integrating

| System | Verified capability | Proposed Hayaku relationship |
| --- | --- | --- |
| Nx | Affected tasks derive from Git changes and the project graph; lockfile impact can be mapped to consumers. [Affected CI docs](https://nx.dev/docs/features/ci-features/affected) | Import qualified project/task boundaries rather than rebuild them; independently examine completeness, configured inputs and runner behavior |
| Turborepo | `--affected` selects changed packages and their dependents using a Git comparison; current docs also describe optional task-input refinement. [Run reference](https://turborepo.com/docs/reference/run) | Preserve package-manager task execution; do not confuse package/task impact with individual-test soundness |
| bazel-diff | Compares target hashes across revisions to identify direct and indirect impact. Optimizations based on supplied changed paths require that list to cover all changes. [Project source/README](https://github.com/Tinder/bazel-diff) | Potential backend for a Bazel adapter after version qualification; never truncate dependency distance in a strict selection policy |
| pytest-testmon | Uses executed-code dependencies and persistent data; its docs explicitly exclude static assets and external services from tracked categories. [Project documentation](https://www.testmon.org/) | Useful behavioral evidence and comparison baseline; it cannot alone satisfy the proposed unrestricted input contract |

These capabilities were checked in project-owned documentation on the research date. No installations, runtime comparisons, exhaustive source audits or universal safety certification were performed for these four systems. Version pinning and conformance testing remain future work.

Hayaku's potential value is one consistent assurance/reporting contract and runner-specific plans across existing repositories, with explicit cross-language/resource edges and honest fallbacks. This is a product hypothesis to validate, not proof that existing tools cannot already meet a particular repository's needs.

## Command correctness checklist for adapter development

1. Detect the exact runner/version and execution context; preserve the full original command as the fallback.
2. Obtain a current inventory, including dynamic/parameterized identities when needed; inability to enumerate safely requires a broader container.
3. Render arguments as structured argv, escaping filter syntax separately from shell syntax. Never interpolate repository-controlled paths into executable shell text.
4. Retain wrappers, setup/build prerequisites, environment contract, coverage/race flags and matrix dimensions.
5. Compare the runner's discovery/result identities with the intended runnable scope. Over-selection is safe but should be reported; under-selection or zero-match success is an error or full rerun.
6. Preserve fixture/setup and ordering semantics, and ensure prerequisite steps do not mutate the candidate after the plan was bound.

No emitted command has been tested against a Hayaku fixture repository yet. The survey establishes that ecosystem-specific selection mechanisms exist; implementation must separately establish that Hayaku chooses and invokes them correctly.
