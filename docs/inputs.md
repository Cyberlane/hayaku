# Explicit generated and linked input envelopes

`hayaku inputs` captures only explicitly selected inputs and explicitly allowed
source roots. It does not guess ignored/generated files or expand native runner
qualification. Missing inputs, ambiguous collisions, escapes, cycles and
observable mutation fail instead of silently dropping evidence.

## Configuration and capture

```json
{
  "schema": 1,
  "roots": [
    {"name": "project", "path": ".", "mount": "."},
    {"name": "shared", "path": "../shared-package", "mount": "linked/shared"}
  ],
  "inputs": [
    {"root": "project", "path": "generated", "generated": true},
    {"root": "project", "path": "node_modules", "generated": false}
  ]
}
```

Root paths resolve against `--root` (default current directory). Input paths and
mounts are relative, with `.` allowed for an explicitly selected root namespace.
The example captures complete selected trees and follows a workspace link only
if its target is inside an explicitly declared allowed root. Select the linked
target directly as an additional input if it must be captured without a link.
`generated` records provenance; it is not permission to ignore a missing path.

```sh
hayaku inputs --config inputs.json --output .git/inputs-manifest.json
# Destination must be new, with an existing parent, outside every source root.
hayaku inputs --config inputs.json --destination /tmp/hayaku-input-view
```

Source roots cannot physically overlap. Mount overlap/collisions reject, except
that a selected `.` namespace can coexist with disjoint declared mounts when
actual captured entries do not collide. Internal links are rewritten into the
private view; broken/cyclic/escaping links, implicit linked ancestors, special
files and unsupported special modes reject. Capture preserves file/directory
modes, original link identity and complete selected membership.

The emitted manifest includes schema/digest, named mounts, selected provenance,
entries, modes, byte counts, file hashes and link identity. Host source paths,
file contents and absolute link text are not exposed. Absolute original link
text is hashed; relative link text remains manifest data. Timestamps/inodes are
not source semantic identity. Source inventories are checked repeatedly to
detect observable drift; this is not a detector for historical transient writes.

Default hard ceilings are 50k entries, 64 MiB per file, 512 MiB total and depth
128. Every exceeded bound fails. The saved-manifest decoder independently
rejects duplicate/unknown fields, trailing data, invalid canonical identities,
unsafe paths/link chains and manifests larger than 32 MiB. A manifest has no
file bytes or authenticated producer authority; reconstructing a view requires
fresh source capture and identity comparison.

## Verified materialization

Materialization uses private captured bytes, exclusively reserves a new
destination, verifies source before/after, preserves supported modes/links and
checks exact resulting membership/bytes. It cannot overwrite source roots or
existing destinations. Partial failed copies are removed. The caller must wait
for successful completion before using the view; directory reservation is not
atomic publication of a complete tree. Revalidation rejects added, missing,
changed or escaped materialized inputs.

## Capsule bridge

`capsule.json` can replace `inputs` with the same inline envelope:

```json
{
  "schema": 1,
  "name": "linked-input-suite",
  "module": "suite.wasm",
  "input_envelope": {
    "schema": 1,
    "roots": [
      {"name": "project", "path": ".", "mount": "."},
      {"name": "shared", "path": "../shared-package", "mount": "linked/shared"}
    ],
    "inputs": [{"root": "project", "path": "generated", "generated": true}]
  },
  "args": ["suite.wasm"],
  "env": {},
  "producer_identity": "1111111111111111111111111111111111111111111111111111111111111111"
}
```

Use a verified producer digest in place of the illustrative value. The bridge
verifies a private materialization and normalizes its virtual alias paths,
regular-file bytes and full directory membership, including empty directories,
into the capsule's fixed read-only filesystem. The source manifest digest is
bound into the producer identity, so original mode/link/provenance changes
invalidate reuse even though guest metadata is normalized. Flattening aliases
can duplicate bytes and is also bounded by capsule limits. Original symlink,
`readlink` and native filesystem metadata behavior are not qualified.

The CLI revalidates live config/module/envelope after running; drift cannot be
reported as a passing result for current inputs. Standalone manifests are not
pass receipts and cannot authorize native skipping.

## Native monorepo provenance

Native BASE and CANDIDATE remain independently bound snapshots. Never overlay
candidate-generated output or linked target source into BASE as if BASE produced
it. Capture candidate-scoped inputs with candidate provenance; independently
produce/bind BASE inputs if a future native context supports them. An explicit
envelope does not currently broaden the native Vitest runtime-copy boundary,
qualify arbitrary ignored source, isolate services or enable native omission.
