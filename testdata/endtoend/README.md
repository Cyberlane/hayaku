# Independent full-command workloads

These small repositories provide independently authored inventories and fault
oracles for Go, TypeScript/Vitest, Cloudflare worker Vitest, unittest, pytest,
Cargo and SwiftPM XCTest. They exercise imports, setup, resources, new tests,
pre-test failures and dormant external-input effects. Passing these tests does
not establish runtime qualification or permission to omit tests.

`manifest.json` is fixture-author evidence, not an inventory extracted from the
runner under test. Each mutation has exact expected assertion failures or an
independent pre-test diagnostic sentinel plus cases that must never reach a
terminal assertion. `failure_exit` preserves the original runner's assertion
failure status (Cargo uses 101). A dormant effect deliberately passes the full
suite and remains marked unqualified.

Run static contract checks with:

```sh
go test ./internal/qualification -run TestEndToEndFixtureContracts
```

Actual workloads require `HAYAKU_ENDTOEND=1`. Node contexts additionally require
absolute `HAYAKU_VITEST_NODE` and `HAYAKU_VITEST_MODULES` paths; worker tests use
`HAYAKU_WORKER_MODULES` containing Vitest and `@cloudflare/vitest-plugin`.
Python uses `HAYAKU_NATIVE_PYTHON` for pytest and
`HAYAKU_NATIVE_UNITTEST_PYTHON` for unittest, or the installed `python3`.
Missing installed contexts skip explicitly. No dependency/tool installation is
performed. Go and Cargo execution prohibit automatic dependency fetching.

```sh
go test ./internal/qualification -run TestEndToEndProvisionedFullCommandOracles -v -count=1
```

Cold/warm records measure the complete original invocation including build and
startup. Cold means a fresh fixture workspace, with provisioned global toolchain
caches retained; warm means a second full invocation in that workspace. Swift
uses an initially empty shared SDK-module cache for its fixture cases and eight
build jobs. These observations do not represent cleared machine-wide caches,
Hayaku selective execution, total capture/planning costs, or production savings.
Exact installed versions/platforms must be recorded with acceptance evidence;
a successful full Vitest command does not expand Hayaku's qualified version
matrix or qualify the worker runtime.
