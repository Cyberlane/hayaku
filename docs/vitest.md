# Vitest support

Hayaku supports the **Vitest 4.1.11 native API** for configured Vite import
influence, experimental JS/TS file/project proposals, full execution and shadow
comparison. Production test omission remains disabled. Jest, Mocha, Node's test
runner, Playwright and Cypress still use configured full-suite fallback commands.

## Configure an existing installation

Install your project's locked dependencies through your normal trusted setup;
Hayaku does not install packages, run npm scripts or fetch revisions. Use a plain
`vitest run` command and an explicit Node executable. Root `package.json` Vitest
(development or runtime) dependencies are recognized by `init`; it never interprets
shell scripts. Review all original flags, roots and project selection.

```sh
# Keeps host-specific absolute runtime paths under Git metadata.
hayaku init --config .git/hayaku-vitest.json
# Review/edit the generated configuration before continuing.
hayaku doctor --config .git/hayaku-vitest.json
hayaku plan --base BASE_COMMIT --candidate HEAD \
  --config .git/hayaku-vitest.json --output .git/vitest-plan.json
hayaku shadow --plan .git/vitest-plan.json \
  --config .git/hayaku-vitest.json --output .git/vitest-shadow.json
hayaku run --plan .git/vitest-plan.json \
  --config .git/hayaku-vitest.json --output .git/vitest-full.json
```

For an explicit root workspace, the configuration shape is:

```json
{
  "schema": 1,
  "context": { "id": "node22-linux", "os": "linux", "arch": "amd64", "env": { "CI": "true" } },
  "workspaces": [{
    "id": "js", "root": ".", "adapter": "vitest",
    "command": {
      "cwd": ".", "executable": "node_modules/.bin/vitest",
      "argv": ["run", "--config", "vitest.config.ts"]
    },
    "node_runtime": {
      "node": "/absolute/path/to/node",
      "modules": "/absolute/path/to/repository/node_modules"
    }
  }]
}
```

Bound Vitest requires effective `CI=true`, supplied by the job environment or
explicitly reviewed in `context.env`; missing or contradictory values are rejected.
`init` declares this value for review. Configured Vite roots are preserved; discovered
files must remain within the configured command workspace.

Replace the absolute paths and host context with actual runner values. The
configured executable must resolve to that installation's `vitest/vitest.mjs`;
a wrapper or different global installation is rejected. Node is invoked explicitly
rather than through an ambient shebang. Modules can be external, or the exact
`node_modules` directory at the configured command cwd. An in-checkout installation
must be ignored by Git. All other dirty/untracked/ignored inputs remain rejected;
committed module directories cannot be overwritten by an injected runtime.

Configuration under Git metadata is still privately digest-bound and reconstructed
before execution. Keep portable policy in version control and generate/review these
host paths per CI matrix job. Saved plans cannot be transferred between differing
Node/dependency/environment contexts. Reports must use fresh filenames. On macOS,
launch bookkeeping such as SHLVL can differ between commands; establish consistent
job context or explicitly pin reviewed values in `context.env`.

## Evidence and execution

The embedded bridge loads installed Vitest and asks each configured Vite SSR
environment to transform imports. It uses native resolution, including aliases
and TypeScript transformations. It does not parse source imports with regexes or
use `vitest related` as proof. Dependency ownership is an **experimental proposal**.

Configuration files and their captured dependencies, setup/global setup, resources,
and unmapped source files retain broad ownership. Incomplete transforms broaden
all proposals. Old/new evidence is unioned; new tests enter proposals, deleted
units are not executed, and unknown changed paths broaden execution. Arbitrary
computed imports, filesystem reads, services, subprocesses, plugin effects and
shared state remain explicit runtime gaps. A retained native counterexample shows
an omitted file failing through a hidden filesystem dependency.

Node bytes and the entire installed module tree are privately hashed, including
file modes and internal symlink targets. Dependencies are verified and copied into
separate disposable sources for discovery and execution. Internal npm links work;
links escaping the installed tree (including linked workspace packages and external
pnpm stores), special files and broken links fail. Limits are 250k entries /2 GiB;
process output is bounded and cancellation cannot be a successful result.

Full execution preserves the configured test scope. Shadow runs exact native
project/file specifications for the proposal and all configured specifications for
the independent full run. Emitted CLI file filters may match extra files/projects;
that broadening does not grant production omission. Stable test identities include
project, file, full name and duplicate-name occurrence. Missing, unexpected,
nonterminal or malformed results invalidate execution; failures and skipped tests
retain their native outcomes. Unhandled global errors fail execution and invalidate
comparison because they have no reliable per-test attribution.

Runs disable Vitest result caching and use private Vite caches. Bundled configuration
loading is preserved; only a newly created, empty Vite bundler scratch directory
inside a private copied installation is removed before identity revalidation.
Preexisting entries or unexpected files/modes are retained and cause mutation
checks to fail. Source and runtime are checked before/after commands; this is not
a sandbox, transient-write detector or isolation from services/external state.

## Supported boundary

Initial native fixtures use Node **22.18.0**, Vitest **4.1.11**, Vite **7.3.1** on
Linux amd64 and macOS arm64 in release CI. Other contexts require independent
acceptance; Windows Node execution is experimental and not qualified.

Only ordinary whole-file `run` scopes are supported. Browser mode, typechecking,
coverage output, snapshot updating, changed/related scopes, shards, bail,
test-name/tag selection and ignored-unhandled-error modes are rejected.
Prerequisites that generate source/build inputs are unsupported for bound Vitest
workspaces. Keep those gates in your existing CI pipeline. Unbound legacy Vitest
file discovery remains available but retains full proposal gaps and cannot use
native `run`/`shadow` reconciliation.

## Native development checks

The Go core remains standard-library-only. Pinned npm dependencies in
`testdata/vitest-runtime/` are **development fixtures**, not Hayaku's runtime or
bundled release dependencies. Prepare them outside the checkout:

```sh
mkdir /tmp/hayaku-vitest-runtime
cp testdata/vitest-runtime/package*.json /tmp/hayaku-vitest-runtime/
npm ci --prefix /tmp/hayaku-vitest-runtime --ignore-scripts --no-audit --no-fund
export HAYAKU_VITEST_NODE="$(command -v node)"
export HAYAKU_VITEST_MODULES=/tmp/hayaku-vitest-runtime/node_modules
export HAYAKU_VITEST_BINARY=/tmp/hayaku-vitest-runtime/node_modules/.bin/vitest
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
```

Use Node 22.18.0 and canonical absolute paths (on macOS `/private/tmp` resolves
`/tmp`). Without explicit runtime variables, native application fixtures skip;
protocol, graph, runtime-copy and snapshot regressions still run. CI sets the
variables and requires the native fixtures. Tests cover aliases, setup/resources,
new/deleted tests, multiple projects, duplicate names, hooks, hidden dependencies,
unhandled errors, installed-input drift, dirty source, cache mutation and cancellation.
No representative CI speedup or production omission is claimed.

Primary references: [Vitest API](https://vitest.dev/api/advanced/vitest.html),
[CLI](https://vitest.dev/guide/cli.html), and
[reported tasks](https://vitest.dev/api/advanced/test-module.html). Current upstream
pages may describe newer versions; the implementation explicitly binds 4.1.11.

For dynamic filesystem observations that broaden the static envelope, see
[runtime observations](runtime-observations.md). These are separate diagnostics,
not an enforced execution backend or authorization to skip.
