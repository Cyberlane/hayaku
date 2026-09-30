# Implementation architecture

`cmd/hayaku` delegates to `internal/app`, which coordinates packages rather than
placing framework branches in graph logic. `internal/model` owns versioned config,
evidence, unit, gap, reason and command schemas. CLI/config use the standard
library. There is no service, telemetry, AI model, credential requirement or DB.

- `snapshot`: resolves exact commit objects, inventories NUL-delimited changes,
  copies raw blobs, bounds resources and rejects unsupported entry types.
- `config`: strict schema/fields, duplicate-key rejection, context and path checks.
- `adapter`: versioned native providers and honest whole-suite fallbacks. Native
  graph identities include workspace namespaces. Discovery does not certify runtime
  dependencies. Collection side effects are explicit capability gaps.
- `graph` / `planner`: validates endpoints, unions old/new influences, follows
  dependency-to-dependent edges with cycle-safe deterministic closure. New units
  enter proposals, deleted units remain old influence evidence, unknown inputs and
  evidence gaps broaden. Declared cross-language input/consumer contracts add
  influence; declarations do not prove enforced isolation.
- `report`: JSON and human views of the same plan. Required commands and proposed
  commands are separate fields; candidate exclusions are never CI skip authority.
- `runner/golang` and `runner/vitest`: native terminal reconciliation, stable
  scope/test identities, failed/skipped outcomes and missing-result rejection.
- `noderuntime`: bounded Node/dependency content identities and verified copies;
  internal npm links are allowed only within the declared installed tree.
- `adapter/vitest`: an embedded Node bridge uses the installed Vitest 4.1.11 API
  and configured Vite transforms; the Go core does not parse TypeScript imports.
- `process` / `app`: bounded/cancellable structured argv; saved-plan reconstruction,
  source/context revalidation, execution, and disposable Go/Vitest shadow comparisons.
- `evidence`: optional private, atomic JSON advisory cache, keyed by source, context,
  installed executable bytes, policy and installed CLI bytes/implementation version. Integrity checks
  are not a cryptographic signature. Execution does not use cached metadata.
- `qualification`: finite observation validation, net-cost arithmetic and independent
  fault corpus. Nothing in this package grants production omission authority.

Dependency-free initial choices are intentional. Introduce go/packages, parsers,
SQLite or platform SDKs only when an adapter or measured workload requires them.
Mori's normalized fingerprints are not imported or used for source invalidation.

Configuration command cwd is relative to its workspace root. Plan command cwd is
relative to the Git repository. Environment values are private configuration; plans
carry digests, not inherited/configured values. Executable argv is owner-provided
policy and must not contain credentials. A context with a different host OS/arch
requires a separate actual runner; local plans do not qualify an entire CI matrix.

Limits are explicit errors, not lossy truncation: configuration 1 MiB, process output
32 MiB per stream (Go discovery separately 64 MiB), source 50k files /512 MiB total
/64 MiB blob, graph 100k nodes /1m edges. A future plugin API should preserve this
contract and require independently versioned qualification per runtime and runner.

Native Go context captures persisted effective compiler/CGO/architecture settings.
Nonempty hidden GOFLAGS rejects with a request to place scope flags in argv.
A differing PATH override rejects; explicit absolute runner paths are supported.
Discovery checks materialized raw-tree digests before/after tooling.

Declared Node runtimes are separately bounded to 250k dependency entries and
2 GiB including Node bytes. Snapshot source keeps its original strict digest;
explicit module subtrees receive independent complete digests, including modes
and internal symlink targets. Copies cannot overwrite committed inputs. Bound
Vitest runs use private copies, uncached results and private Vite caches; a newly
created empty bundler scratch directory is restored before revalidation. No
arbitrary ignore list is introduced. These checks observe before/after state;
they do not enforce hermetic execution or observe every transient side effect.
