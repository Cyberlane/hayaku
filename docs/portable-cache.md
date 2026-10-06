# Portable receipt authentication (unreleased)

The shared receipt store supports explicit Ed25519 issuer and verifier roles.
Independent macOS/Linux protocol tests, repository normal/race/vet checks and
source review passed. This is local implementation evidence; hosted CI trust and
distributed freshness remain separate gates in the [kanban](affected-tests-kanban.md).

Without portable options, the existing private local HMAC cache remains the
default. Selecting portable mode never generates, imports or restores that
local signing secret. A verifier receives public keys and signed receipt files;
it can reuse a valid pass, remove local copies for an audit and execute on a
miss. It cannot sign or publish a new pass. Fresh verifier execution reports
`verification-only`; unavailable storage still executes without creating
authority.

## Explicit policy and issuer

The trusted invoking process supplies an absolute policy path outside the
checkout and receipt cache. The parent directory must be private. Policy files
are bounded regular private files; duplicate/unknown fields, duplicate issuers,
wildcards, unsupported schemas and invalid keys reject the invocation.

```json
{
  "schema": 1,
  "namespace": "owner/repository",
  "epoch": 1,
  "public_keys": ["<64 lowercase hexadecimal characters for an Ed25519 public key>"]
}
```

The namespace has at most 256 ASCII letters, digits or `._:/-` characters. Epoch
must be positive; at most 16 distinct public keys are allowed. Issuer identities
are derived from public-key bytes, rather than caller-supplied names. A signing
job additionally receives an explicit private 32-byte Ed25519 seed file with
mode `0600`, also outside source/cache in a private parent. Its derived public
key must appear in the policy. Seed bytes never enter reports, cache transport,
guest arguments, guest environment or copied resources.

The unreleased `capsule`, `suite` and `native` commands use the same options:

```sh
hayaku capsule --root /source --config capsule.json --cache-dir /private/passes \
  --cache-policy /protected/policy.json --cache-signing-key /protected/issuer.seed

hayaku capsule --root /source --config capsule.json --cache-dir /private/passes \
  --cache-policy /protected/policy.json
```

The second invocation is verification-only. Omit the signing-key option to select
that role; explicitly selecting an empty signing-key path is an error.
An explicitly empty, missing,
malformed or changed policy/key cannot select local-cache authentication as a
fallback. Private file/parent identities, bytes and aliases are rechecked;
detected policy drift invalidates success and removes this invocation's copies
using its captured authority. Source/configuration drift cleanup uses that same
authority. No cleanup path silently reopens a local HMAC cache.

## Scope and transport

Signatures bind the versioned canonical envelope, namespace, epoch, derived
issuer, provider domain, existing artifact key and exact receipt value. The
provider still verifies its complete execution contract before consulting or
issuing a receipt. Authentication cannot qualify arbitrary native code, replace
private Go producer seals, admit a native input envelope, turn imported reports
into authority or bypass original full-scope fallbacks.

Transport only signed receipt files into a fresh private cache directory. Keep
policy and signing material under independent control. Existing artifact and
backend keys remain unchanged: a different controller, toolchain, OS/boot or
Linux daemon context can legitimately miss. A signature does not make different
native hosts equivalent or guarantee a useful cache hit rate.

Changing the trusted epoch or allowed keys rejects older receipts. Deleting
local files during an audit is local revocation; restoring a previously valid
signed file under the same epoch can still verify. Distributed freshness and
revocation remain separate work under K35.

Private modes do not establish hosted CI trust or isolate files from commands
running as their owner. A deployed CI policy must keep verification policy
immutable to untrusted jobs and exclude signing credentials from those jobs.
Signing jobs must trust the controller and code they execute, including original
full-command fallbacks. Hosted secret access, fork isolation, policy distribution
and publication permissions require separate acceptance; local disposable jobs
do not demonstrate those controls.

Local tests include actual WASI execution, independent issuer/verifier
processes, macOS pure-Go execution and receipt-only transfer, tampering,
namespace/epoch/domain/key substitution, legacy HMAC rejection, authority-file
drift, actual filesystem aliases, bounded FIFO handling, verifier audits and
fresh execution without receipt storage. Failed configured audits retain admitted
root/key handles and revoke authenticated provider-domain copies on early failure,
cancellation and later output failure; independent macOS/Linux fault controls and
full composition checks passed. Deployment and distributed freshness gates remain
separate in the kanban.
