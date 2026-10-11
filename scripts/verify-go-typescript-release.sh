#!/bin/sh
# Run from the repository root with separately provisioned, pinned SDK inputs.
set -eu

case "$(uname -s):$(uname -m)" in
  Darwin:arm64) HAYAKU_GO_RELEASE_PLATFORM=darwin ;;
  Linux:aarch64|Linux:arm64) HAYAKU_GO_RELEASE_PLATFORM=linux ;;
  *) echo 'Release acceptance requires an actual macOS/Linux ARM64 host.' >&2; exit 1 ;;
esac
: "${HAYAKU_NATIVE_IMAGE:?Supply the installed immutable Linux image ID}"
: "${HAYAKU_DYNAMIC_NODE:?Supply the installed Linux Node executable}"
: "${HAYAKU_NODE_CERTIFIER_NODE:?Supply the installed host Node executable}"
: "${HAYAKU_VITEST_MODULES:?Supply the pinned Vitest node_modules tree}"
: "${HAYAKU_NODE_CERTIFIER_TYPESCRIPT:?Supply the pinned TypeScript package}"
: "${HAYAKU_NATIVE_ADDON_ROLLDOWN:?Supply the pinned Linux Rolldown addon}"
: "${HAYAKU_VITEST_LIBRARY_DIR:?Supply the admitted ELF loader/library directory}"

export HAYAKU_GO_RELEASE_PLATFORM HAYAKU_NATIVE_ARCH=arm64
export HAYAKU_GO_RELEASE_REQUIRED=1 HAYAKU_TYPED_GO_RELEASE_REQUIRED=1
export HAYAKU_TYPESCRIPT_RELEASE_REQUIRED=1 HAYAKU_NODE_CERTIFIER_REQUIRED=1
export GOENV=off GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOWORK=off GOFLAGS=
export GOEXPERIMENT= GODEBUG=

release_logs=$(mktemp -d "${TMPDIR:-/tmp}/hayaku-go-ts-verify.XXXXXX")
trap 'rm -rf "$release_logs"' 0
cli_parents='TestInstalledGoReleaseCLI TestInstalledTypedGoReleaseCLI TestInstalledTypeScriptReleaseCLI'
node_parents='TestInstalledNodeSourceCandidateJavaScriptAndTypeScript TestInstalledNodeSourceCandidateRejectsAmbiguousJavaScriptTypeScriptImports TestInstalledNodeSourceCandidateRejectsDormantAndEscapedEffects TestInstalledNodeSourceCandidateBoundsCallDAG TestInstalledNodeSourceCandidateRejectsEmitterContradictions TestInstalledNodeSourceCandidateRejectsImportedTestOwnership TestInstalledNodeSourceCandidateStrictBindingsForJavaScriptAndTypeScript'

checked_go() {
  expected=$1
  shift
  if ! go test "$@" > "$release_logs/output" 2>&1; then
    cat "$release_logs/output" >&2
    exit 1
  fi
  cat "$release_logs/output"
  awk -v expected="$expected" '
    BEGIN { n=split(expected,names," "); for(i=1;i<=n;i++) want[names[i]]=1 }
    /--- SKIP:|WARNING: DATA RACE/ { bad=1 }
    /^--- PASS: / { if(!($3 in want)) bad=1; count[$3]++ }
    END { for(i=1;i<=n;i++) if(count[names[i]]!=1) bad=1; exit bad }
  ' "$release_logs/output" || { echo 'Required test inventory was not completed.' >&2; exit 1; }
}

for mode in normal race; do
  set --
  if test "$mode" = race; then set -- -race; fi
  checked_go "$cli_parents" "$@" -v -count=1 -p=1 -timeout=90m \
    -run '^(TestInstalledGoReleaseCLI|TestInstalledTypedGoReleaseCLI|TestInstalledTypeScriptReleaseCLI)$' ./internal/app
  node_pattern=$(printf '%s' "$node_parents" | tr ' ' '|')
  checked_go "$node_parents" "$@" -v -count=1 -p=1 -timeout=20m \
    -run "^($node_pattern)\$" ./internal/native
done
