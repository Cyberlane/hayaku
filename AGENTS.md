# Hayaku

Hayaku plans affected test runs. Preserve the fail-closed safety contract in
`docs/safety.md`: incomplete evidence must broaden a run, never silently omit.
Native discovery is not proof of complete runtime influence. Keep experimental
proposals separate from commands authorized for CI execution.

- Go core, standard library first; no automatic network fetch or runner install.
- Use structured process arguments, bounded output and cancellation.
- Run `go test ./...`, `go test -race ./...` and `go vet ./...` for meaningful code changes.
- Review source and tests with Mori; findings are advisory, not semantic evidence.
- Local commits are authorized for implementation tasks. Publishing, releases
  and deployments require explicit task authorization.
- Keep unrelated work intact. Do not add credentials, raw environment values or
  source contents to reports.

Keep statuses honest about native qualification and external acceptance.
Source and tests here are technical truth; the public capability backlog is in
`docs/implementation-status.md`.
