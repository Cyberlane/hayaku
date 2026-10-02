# Vitest support

Hayaku uses installed Vitest/Vite APIs for configured JS/TS import influence, file/project proposals, native outcomes and shadow comparison. Required native execution retains the original full scope. Runtime observations broaden proposals; they do not authorize omission.

## Exact compatibility matrix

| Vitest | Vite | Development fixture |
| --- | --- | --- |
| 3.2.7 | 6.4.3 | `testdata/vitest-runtime-v3` |
| 4.1.11 | 7.3.1 | `testdata/vitest-runtime` |
| 4.1.11 | 8.1.5 | `testdata/vitest-runtime-v4-vite8` |

These exact pairs are accepted; missing, mismatched, prerelease or other versions are rejected. The bridge resolves Vite from the installed Vitest package, binds both versions into its protocol, and verifies the returned pair in Go. v3 uses its `init`/vite-node SSR APIs; v4 uses standalone SSR environments. Current upstream documentation can describe a different version: the pinned installed API and fixture tests are the compatibility evidence.

Node 22.18.0 is the pinned native fixture context. CI defines checks on Linux amd64 and macOS arm64 for all three pairs. This establishes tested protocol/discovery/run/shadow behavior, not complete runtime influence or acceptance of every consumer context. Windows Node execution remains unqualified.

## Configure an existing installation

Install locked dependencies through your normal trusted setup. Hayaku does not install packages, run npm scripts or fetch revisions. Use a plain `vitest run` command and an explicit Node executable; review roots, flags and project selection. `init` recognizes root `package.json` Vitest dependencies without interpreting shell scripts.

```sh
hayaku init --config .git/hayaku-vitest.json
# Review generated commands, runtime paths and context before continuing.
hayaku doctor --config .git/hayaku-vitest.json
hayaku plan --base BASE_COMMIT --candidate HEAD \
  --config .git/hayaku-vitest.json --output .git/vitest-plan.json
hayaku shadow --plan .git/vitest-plan.json \
  --config .git/hayaku-vitest.json --output .git/vitest-shadow.json
hayaku run --plan .git/vitest-plan.json \
  --config .git/hayaku-vitest.json --output .git/vitest-full.json
```

A root workspace uses this shape; substitute real absolute paths and host values:

```json
{
  "schema": 1,
  "context": {"id": "node22-linux", "os": "linux", "arch": "amd64", "env": {"CI": "true"}},
  "workspaces": [{
    "id": "js", "root": ".", "adapter": "vitest",
    "command": {"cwd": ".", "executable": "node_modules/.bin/vitest", "argv": ["run", "--config", "vitest.config.ts"]},
    "node_runtime": {"node": "/absolute/path/to/node", "modules": "/absolute/path/to/repository/node_modules"}
  }]
}
```

Effective `CI=true` is required; missing or contradictory values reject. The configured executable must resolve to that installation's `vitest/vitest.mjs`; wrappers or a different global installation reject. Node is invoked explicitly. Modules can be external or the exact ignored `node_modules` directory at the command cwd. All other dirty/untracked/ignored source inputs remain rejected, and committed module directories cannot be overwritten.

Host-specific configuration under Git metadata is still privately digest-bound and reconstructed before execution. Plans cannot move between different Node/module/environment contexts. Use fresh report filenames. Establish consistent process environment or explicitly pin reviewed values such as macOS launch bookkeeping in `context.env`.

## Evidence and execution

Native transforms preserve configured roots, aliases and TypeScript resolution. Config/setup/global-setup dependencies, resources and unmapped paths retain broad ownership. Incomplete transforms broaden proposals. Old/new influence is unioned, new tests enter proposals and deleted units are not executed. Arbitrary computed imports, filesystem reads, services, subprocesses, plugin effects and shared state remain explicit gaps.

Node bytes and the installed module tree are privately hashed, including file modes and internal link targets, then copied into separate disposable sources. The native runtime-copy boundary supports internal npm links; escaping workspace/pnpm-store links, special files and broken links fail. Its limits are 250k entries /2 GiB. The separate [input envelope](inputs.md) supports explicitly declared linked roots for capture and normalized capsule inputs; it does not silently expand this native boundary or overlay candidate inputs into BASE.

Shadow compares exact native project/file specifications against the independent full scope. Stable identities include project, file, full name and duplicate-name occurrence. Missing/unexpected/nonterminal/malformed outcomes invalidate execution. Failures, skips and hooks retain native outcomes; unhandled global errors invalidate comparison. Emitted CLI filters can broaden matches, which does not grant native CI omission.

Runs disable Vitest result caching and use private Vite caches. A newly created empty bundler scratch directory may be restored before revalidation; unexpected/preexisting entries remain mutation evidence. Source/runtime are checked before and after commands. This is not a sandbox or transient-write detector.

## Supported contexts

Only ordinary whole-file `run` scopes with `forks` or `threads` pools are supported. Custom pools, Cloudflare/workerd worker contexts, VM pools, browser mode, typechecking and `poolMatchGlobs` reject. Vite 8 compatibility does not qualify the Cloudflare worker pool. Coverage output, snapshot updating, changed/related scopes, shards, bail, test-name/tag selection and ignored-unhandled-error modes reject.

Prerequisites that generate source/build inputs remain outside bound native execution. Keep service-dependent or unsupported suites in their original pipeline. Unbound legacy file discovery retains full proposal gaps and cannot use native execution/shadow reconciliation.

[Runtime observations](runtime-observations.md) record supported dynamic file reads, missing probes and directory membership. For v3, the private trace setup file is explicitly allowed by vite-node's loader; this diagnostic allowance is not an enforced runtime boundary. Observation runs are instrumented/serial and separate from the original required command.

## Native development checks

The locked npm packages are development fixtures, not bundled Hayaku runner dependencies. Select one matrix directory and prepare it outside the checkout with Node 22.18.0:

```sh
mkdir /tmp/hayaku-vitest-runtime
cp testdata/vitest-runtime-v3/package*.json /tmp/hayaku-vitest-runtime/
npm ci --prefix /tmp/hayaku-vitest-runtime --ignore-scripts --no-audit --no-fund
export HAYAKU_VITEST_NODE="$(command -v node)"
export HAYAKU_VITEST_MODULES=/tmp/hayaku-vitest-runtime/node_modules
export HAYAKU_VITEST_BINARY=/tmp/hayaku-vitest-runtime/node_modules/.bin/vitest
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
```

Repeat with a fresh directory for each other pair. Use canonical absolute paths; macOS `/tmp` resolves to `/private/tmp`. Without explicit runtime variables, installed native application fixtures skip visibly; protocol/copy/snapshot tests still run. Required CI supplies the variables. Fixtures challenge aliases, setup/resources, multiple projects, duplicate names, pass/fail/skip/hooks, new/deleted tests, hidden dependencies, dynamic observations, version/pool rejection, input drift and cancellation.

Primary references: [Vitest advanced API](https://vitest.dev/api/advanced/vitest.html), [CLI](https://vitest.dev/guide/cli.html) and [test modules](https://vitest.dev/api/advanced/test-module.html). Version claims above are deliberately narrower than upstream ranges.
