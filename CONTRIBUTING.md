# Contributing

Hayaku is early software. Start with a focused issue describing the runner,
workspace, context and observable behavior without credentials, raw environment
values or private source. [Implementation status](docs/implementation-status.md)
lists current work and qualification boundaries.

Preserve the [safety contract](docs/safety.md): incomplete evidence broadens a
run or returns an error. Native discovery is not proof of complete runtime
influence. Experimental proposals never become required CI commands.

Prefer the Go standard library, structured process arguments, bounded output
and explicit cancellation. Do not automatically install runners or fetch Git
history. Use independent positive and negative regression oracles.

Before submitting, run `go test ./...`, `go test -race ./...` and `go vet ./...`.
Explain the behavior change, validation and remaining platform/runtime limits.
Source similarity reviews are advisory. For Vitest changes, also run the
[explicit pinned native fixture checks](docs/vitest.md#native-development-checks);
protocol-only tests do not establish native compatibility.

Releases use strict `vMAJOR.MINOR.PATCH` tags matching `internal/app.Version`,
reviewed release notes and the automated verification/archive pipeline.
See [distribution](docs/distribution.md).
