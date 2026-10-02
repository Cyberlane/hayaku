# Selection contract

For a fixed source snapshot, test inventory and execution context, Hayaku's
required set must contain every test whose observable outcome may change.
Observable outcomes include discovery, build/startup, assertions, failures,
skips and termination. This is a conservative influence model, not a promise
of a minimal set or of software correctness.

Current native metadata does not establish complete runtime influence.
Programs can read files, use services, invoke subprocesses or share state.
Consequently native planning produces **shadow proposals** and executable
**full-run commands**. There is no native skip-anyway switch, confidence threshold,
configuration declaration or mutation score that enables production omission.
Unknown paths, old/new graph gaps, discovery errors and unqualified adapters
retain the original suite. Empty discovery is an error, not a successful skip.

Future narrowing requires an independently reviewed adapter-specific algorithm,
enforced input/isolation boundaries, version/context qualification, adversarial
fixtures and shadow comparison. Assertions in configuration describe inputs;
they do not establish enforcement. Historical coverage may augment evidence
but cannot remove the conservative envelope. Exact all-and-only selection for
arbitrary programs is not the product's established guarantee.

Plans bind raw source trees, configuration and context, and preserve cwd,
executable and argv as data. Execution revalidates these identities. A plan
file is not a trusted executable: the CLI reconstructs the plan before running
its commands. Failed or incomplete execution cannot become a green result.
Source/config changes during execution invalidate the result. External state
cannot be inferred from Git and remains an explicit qualification gap.

Runtime observations can add influence to static proposals but cannot remove it.
Trace transport completeness, a previously passing result and user-supplied JSON
are not proof of an enforced input boundary. Observe runs are instrumented/serial
diagnostics, separate from original required execution. Missing/failed captures
and unsupported effects broaden proposals. They do not qualify native isolation.

## Separate deterministic WASI contract

The capsule backend can reuse an authenticated complete passing result when its
entire WASI context is unchanged. This is actual whole-suite reuse within an
enforced guest interface; it does not establish equivalence with a native Go,
Vitest, Python, browser or service command. Required native suites remain full
fallback even when a separate capsule passes or reuses.

Guests receive copied in-memory regular files, fixed directory inventory and
metadata, explicit arguments/environment, empty stdin, logical clocks and seeded
entropy. There is no ambient host filesystem, inherited environment, network or
subprocess capability. Unsupported imports, writes and denied host calls trap.
Missing files are known absence in this closed guest filesystem. Traps, operational
timeouts/cancellation, incomplete results and resource violations never mint passes.

Keys bind module/input bytes, producer identity, arguments, environment, seed,
limits, interpreter contract, executable identity, Go runtime and host platform.
Only complete qualified passes are stored, authenticated under a private authority
key inaccessible to guests. The invoking user, executable/compiler and interpreter
are trusted; replacing that authority is outside receipt authentication's threat
model. Windows cache ACL semantics remain unqualified, so reuse is disabled there.
Missing, unavailable or invalid caches execute again.

Audit mode removes the previous receipt before execution. Failed/interrupted
audits cannot leave the prior pass authorized; output-digest mismatch invalidates
qualification. A cache hit certifies the functional outcome of this bounded
contract, not host-load timing or another command. See [capsules](capsules.md).

## Monorepo provenance and report evidence

Explicit generated/linked input envelopes bind bytes, modes, membership, link
identity and selection provenance. Manifests and imported native reports are
identity/observation evidence, not capsule receipts or runtime qualification.
Candidate-generated outputs must never be overlaid onto BASE as if BASE produced
them. Input capture alone does not qualify native monorepo or service skipping.
The capsule bridge verifies and flattens a captured envelope into regular-file
aliases and directory membership with fixed guest metadata. Original link/mode/
provenance identity still binds the producer digest; native symlink/readlink
semantics are not part of this normalized contract.
Broader native narrowing still needs a reviewed algorithm, enforced boundaries,
version/context acceptance and adversarial validation.
