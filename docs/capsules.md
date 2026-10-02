# Deterministic WASI capsules

A capsule is a separate WASI preview1 command with an enforced, closed guest
interface. It can reuse the complete passing result of that same contract.
Compiling a native suite to WASI does not establish native equivalence, and a
capsule pass does not remove any original native CI gate.

## Run and audit

Produce and verify the actual `suite.wasm` through trusted build steps. Hayaku
does not install a compiler or infer a compilation identity. This configuration
shows the schema; replace the illustrative producer digest with the SHA256 of
your independently verified source/compiler/build context:

```json
{
  "schema": 1,
  "name": "portable-suite",
  "module": "suite.wasm",
  "inputs": ["generated/value.json"],
  "args": ["suite.wasm"],
  "env": {},
  "producer_identity": "1111111111111111111111111111111111111111111111111111111111111111",
  "seed": "0000000000000000000000000000000000000000000000000000000000000000",
  "limits": {"timeout_ns": 30000000000, "memory_pages": 4096, "output_bytes": 1048576}
}
```

```sh
hayaku capsule --config capsule.json --cache-dir .git/hayaku-passes
# An unchanged, authenticated complete pass can report mode "reused".
hayaku capsule --config capsule.json --cache-dir .git/hayaku-passes
# Audit executes and invalidates any previous receipt first.
hayaku capsule --config capsule.json --cache-dir .git/hayaku-passes --no-reuse
```

Paths are root-relative. Ordinary `inputs` select regular files only; links and
directories require the explicit [input envelope](inputs.md). `inputs` and
`input_envelope` are mutually exclusive. Unknown/duplicate fields and malformed
configuration reject. Reports may go to stdout or a fresh `--output` file.

The result contains `mode` (`executed` or `reused`), `complete`, `qualified`,
`passed`, context digests, output digests/counts and fixed reasons/cache status.
`qualified` refers only to the enforced WASI contract. Unsupported effects,
nonpassing or incomplete results cause an unsuccessful CLI exit. Reports omit
guest output, source bytes, raw environment values and host paths. A live
module/config/input change during the invocation invalidates its reported pass.

## Guest interface

Only copied files and explicit directory membership are visible. Guest metadata
is fixed: files read-only, directories read-only/traversable and timestamps at
Unix epoch. Missing entries are known absence. The guest receives explicit args
and environment, empty stdin, logical clocks and deterministic seed-based
entropy. That entropy is for reproducibility, not cryptographic randomness.

The pinned interpreter validates imports, a WASI command entrypoint and private
memory/tables. Network, subprocesses, inherited host files/environment, external
memory and unsupported imported capabilities are unavailable. Allowed compiler
imports do not grant effectful operations: denied writes, socket calls, signals
and other unsupported host calls trap before host effects. Stdout/stderr are
bounded and reduced to digests. Native services, browser behavior, threads and
FFI are not qualified by this contract.

Modules and total guest inputs are limited to 64 MiB each, with at most 4096 input
entries. Args/environment have bounded counts and a 1 MiB byte limit. Default
memory is 4096 pages (256 MiB), at most 8192 pages (512 MiB); module declared
memory maxima are clamped to that contract. Tables and host calls are bounded.
Default timeout is 30 seconds, at most 10 minutes; default combined output is
1 MiB, at most 16 MiB. Resource violations, traps and operational
timeout/cancellation never create reusable passes. A hit certifies a functional
outcome, not whether another host could meet a wall-clock deadline.

## Receipt authority

Reuse binds the module, input paths/bytes/membership, producer identity, args,
hashed environment, seed, limits and backend identity. The backend includes the
interpreter contract, Hayaku executable identity, Go runtime and host platform.
Changing any bound context requires fresh execution. The producer digest is an
invalidation input supplied by the caller, not proof of native equivalence.

A private cache uses an invoking-user authority key and authenticated canonical
receipts. Guests cannot access it. Forged, foreign, malformed or unavailable
receipts cause execution; only complete qualified passes are stored. The user,
trusted executable/compiler and interpreter are inside this authority boundary.
Sharing write access or authority keys with untrusted jobs defeats that model.
Windows caching is disabled pending independent ACL qualification; capsules
there execute uncached.

`--no-reuse` removes the previous receipt before starting. Failed/interrupted
audits cannot preserve it; output-digest mismatch against a trusted earlier pass
invalidates qualification and leaves no reusable pass. Failure to store a new
receipt can leave a fresh execution passed, but the next run must execute again.

The [self-pilot](measurements.md) compiles a committed Go package twice into this
separate contract and measures all costs. It does not qualify the native Go
suite or claim general consumer savings.
