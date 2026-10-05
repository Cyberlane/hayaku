# Runner and language support

`hayaku catalog` separates language/framework, native collection, report format,
selection granularity and assurance. The catalog is a capability map, not a
claim that every framework/version/platform is natively implemented or qualified
to skip. Original native full commands remain required.

## Native evidence integrations

| Runner | Evidence and proposal scope | Version/fixture boundary |
| --- | --- | --- |
| Go `testing` | Package/import/embed/build graph, package proposal, native execution/shadow | Installed Go context/tool bytes; cgo/runtime influence remains unqualified |
| Vitest | Configured Vite graph, file/project proposal, native results/shadow, additive runtime observations | Exact 3.2.7/6.4.3, 4.1.11/7.3.1 and 4.1.11/8.1.5 Vitest/Vite pairs; Node 22.18.0 fixture |
| Node `node:test` | Explicit-file collection with bodies suppressed; native whole-file execution | Exact Node 22.18.0 and 25.2.1 gates/fixtures |
| Python unittest | Native `TestLoader`, including `load_tests`, module scope and terminal results | Stable APIs accepted for Python 3.11–3.14; actual fixture 3.14.7 |
| pytest | Collection and fixture-aware terminal execution, whole-file scope | Hook protocol 7–9 accepted; actual fixture 9.0.2 |
| Jest | Installed `--listTests --json`, whole-file scope and JSON outcomes | Jest 30 CLI protocol accepted; actual fixture 30.5.2 |
| Playwright Test | Native list/reporter, file/project scope, retries and terminal outcomes | Reporter protocol 1.51–1.63 accepted; browser-free fixture 1.63.0 |
| Cargo | Offline, locked workspace graph and package commands | Native Rust fixture; build scripts/proc macros/runtime influence unqualified |
| cargo-nextest | Native configured binary/case inventory rounded to Cargo packages; whole-package proposals | Exact 0.9.146 list dialect and real JUnit fixture; original full execution retained |

Protocol acceptance ranges are broader than recorded fixture versions; they are
not a claim that each version has passed the native matrix. Node/unittest/pytest/
Jest/Playwright input ownership deliberately gives every collected unit every
workspace input. No fine dependency narrowing is claimed. Collection imports
trusted configuration/modules and can have external effects. Flags and modes
outside each bridge's supported contract reject instead of losing scope.

The ordinary native run/shadow paths preserve the original full gate. Nextest's
new inventory/package proposals do not yet integrate automatic case reconciliation
or shadow execution; manually imported reports need an independent inventory.
Playwright browser journeys, services and downloads are outside browser-free
fixture acceptance. Vitest custom Cloudflare/workerd, VM, browser and typecheck
contexts reject even with an accepted Vite pair.

## Other language/build-system entries

| Entries | Frameworks | Current integration |
| --- | --- | --- |
| Maven, Gradle, sbt | Java/Kotlin JUnit/TestNG; Scala ScalaTest/MUnit | Preserved original full-scope command; applicable JUnit import |
| .NET | C#/F# xUnit/NUnit/MSTest | Preserved full command; supported TRX/JUnit import |
| SwiftPM | XCTest on Darwin Swift 6.0–6.4 | Target proposals; independent case list and serial XCTest terminal outcomes; full run required |
| Xcode | Swift/Objective-C XCTest | Full enumeration and installed Xcode 26.3/27.0 xcresulttool exports reconciliation; one plan/device/configuration |
| Ruby, PHP | RSpec, Minitest, PHPUnit | Preserved full command; applicable JUnit import |
| Dart, Flutter | `package:test`, `flutter_test` | Preserved full command; applicable JUnit import |
| C/C++ | GoogleTest, Catch2, CTest | Preserved full command; applicable JUnit import |
| Bun, Deno, Bazel | Native test APIs and configured test rules | Preserved full command; applicable JUnit import |
| Generic command | Any original trusted runner | Structured full command and exit status |

These entries do not provide framework-specific native discovery or affected
filters. Some need an explicit reporter/export step to produce the supported
report dialect. Hayaku installs neither runtimes, reporters nor browsers.
`command` remains an escape hatch for preserving an original full runner, with
no omission authority.

## Strict result imports

Create an independently complete expected case inventory. For common JUnit,
the ID is a JSON-encoded `[classname, name]` pair; TRX uses its native test ID,
Swift its function ID and libtest its native test name. IDs are unique across
the supplied inventory and are owned by explicit containing units.

```json
{
  "schema": 1,
  "cases": [{"unit": "jvm:example", "id": "[\"ExampleTests\",\"addsValues\"]"}]
}
```

```sh
hayaku results --format junit --inventory inventory.json --report results.xml \
  --output .git/reconciled-results.json
```

Formats are `junit`, `trx`, `swift` and `libtest`. Reports are bounded to 16 MiB
and diagnostic bodies are discarded. Import verifies expected terminal identities,
duplicates, conflicting counts/statuses and completeness. Failure, missing cases,
unhandled errors or unsupported dialects cannot be reported as a successful gate.
Independently check the original process completion/exit status: a report alone
does not prove that the runner completed or that the inventory is complete.

Supported JUnit is the common unnamespaced testsuites/testsuite/testcase dialect;
retry/rerun extensions, ambiguous outcomes, XML directives/external entities and
unexpected case IDs reject. TRX follows its validated native identity/result
dialect. Swift Testing accepts ABI 0 or 6.3 JSONL, including function declarations
and run terminals; cancellation/repetition reject. Rust libtest/nextest format
0.1 JSON streams require a single complete binary inventory; reconcile distinct
binaries separately. Upstream Rust JSON support is experimental.

Native imported outcomes, catalogs and observations are not authenticated capsule
receipts. See [qualification](qualification.md) for future native boundaries and
[capsules](capsules.md) for the separate enforced contract that can reuse passes.

## Native nextest inventory

For a clean configured nextest workspace, capture its committed native binary and
case inventory before importing the matching stable JUnit report:

```sh
hayaku inventory --config hayaku.json --workspace rust --output .git/cases.json
hayaku results --format junit --inventory .git/cases.json --report junit.xml
```

Collection runs trusted Rust build scripts and test binaries in private build
folders. The result binds the committed source/config identities and uses native
binary IDs and test names. It remains diagnostic; report ingestion does not
authorize omission. Nextest's experimental libtest JSON is not treated as a
complete native run protocol. Full original nextest execution remains required.

For Python source-envelope execution/shadow, set the original reviewed CI context
explicitly to `context.env.PYTHONDONTWRITEBYTECODE="1"` if bytecode writes should be
disabled. Discovery alone suppresses bytecode. Original full runs preserve their
bytecode behavior; added `__pycache__` files invalidate a supposedly unchanged
source envelope. Actual native fixtures test both behaviors. This is separate
from normal full-command fallback integrations.
