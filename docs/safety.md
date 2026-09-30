# Selection contract

For a fixed source snapshot, test inventory and execution context, Hayaku's
required set must contain every test whose observable outcome may change.
Observable outcomes include discovery, build/startup, assertions, failures,
skips and termination. This is a conservative influence model, not a promise
of a minimal set or of software correctness.

Current native metadata does not establish complete runtime influence.
Programs can read files, use services, invoke subprocesses or share state.
Consequently this version produces **shadow proposals** and executable
**full-run commands**. There is no skip-anyway switch, confidence threshold,
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
