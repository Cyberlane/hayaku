# Generated input provenance

K30 development candidate in an isolated worktree; it is not yet integrated into
the main CLI. Source review is provisional and targeted macOS normal,
race and vet checks passed. Independent Linux acceptance, full composition checks
and canonical review remain pending in the kanban.

Explicit `producers` configure generation separately from existing workspace
`prerequisites`. Each stage declares a unique ID, a new repository-relative
output directory, a structured command and optional dependencies on earlier
stages. There are no language-specific branches in generation or invalidation.

```json
{
  "producers": [
    {
      "id": "schema",
      "output": "generated",
      "command": {
        "cwd": ".",
        "executable": "/absolute/provisioned/generator",
        "argv": ["{source}/schema.json", "{output}"]
      }
    }
  ]
}
```

Commands run without an added shell. Arguments can reference `{source}`,
`{output}`, and `{producer:ID}` for declared earlier dependencies. Working
directories are relative to the private revision source. Executables must be
external absolute tools; revision-owned generator scripts are passed through
`{source}` arguments. This prevents BASE generation from accidentally executing
a candidate-checkout tool. Output paths must be absent from source, nonoverlapping,
and beneath existing real source directories.

Planning creates separate BASE and candidate sessions. Each captures source,
tool bytes and aliases, the explicit effective environment, invocation and
upstream outputs. Commands get fresh private output directories. Complete,
successful generation is followed by bounded output capture and source/tool/
upstream verification. Partial output, cancellation, drift or exceeded bounds
invalidates the session. The shared input bundle materializes a fresh combined
consumer tree and verifies its complete contents; candidate files never overlay
BASE or the user's checkout.

The aggregate captured source, distinct tool bytes and generated outputs are
bounded to 512 MiB, with at most 50,000 source/output entries and 64 MiB per
source/output file. At most 32 ordered stages run within an aggregate ten-minute
deadline, shortened by caller cancellation. Each command's stdout and stderr
are bounded to 1 MiB. Output-local link roles are normalized only after capture;
the original link identities remain guarded within the session.

Reports retain identities and repository-relative generated paths. They contain
no generator stdout, source bytes, raw environment values or tool paths.
Generated additions, changes and deletions join the existing influence graph
and cross-language contracts. Discovery cache keys include actual producer
records. Execution regenerates candidate output and compares those records
before running consumers, then checks the copied consumer tree around commands.
Owned cleanup errors invalidate successful results.

Host generators still have unclosed native dependencies and effects. Their
records are observed provenance, not isolation or omission authority. Private
paths and rewritten links also define a different observable context. An
explicit producer gap and unknown effects retain all original consumer commands,
including comparisons with no changes. Original argv and prerequisite ordering
are preserved in the declared private generation context. Producer shadow and
observation modes reject until their independent scopes are implemented.

Passing these controls cannot qualify affected execution or cache reuse. Runtime
qualification and end-to-end acceptance remain separate cards.
