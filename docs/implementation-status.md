# Implementation status

The source and tests define current behavior. “Native” below means installed-runner evidence and result handling, not complete runtime influence or permission to skip a native suite. Original native commands remain full. v0.4.0 permits whole-suite reuse only within the separately enforced [deterministic WASI contract](capsules.md).

## Five improvement areas

| Area | Implemented scope | Remaining boundary |
| --- | --- | --- |
| Enforced execution | WASI preview1 module validation, immutable virtual files, explicit args/env, logical clocks and seeded entropy; unsupported host capabilities trap | Native Go/JS/Python/Rust equivalence, arbitrary services/FFI/subprocesses and OS isolation are unqualified |
| Actual reuse | Locally authenticated passing receipts bound to module, normalized inputs, producer, context, limits and backend; forced audits invalidate old receipts first | Windows ACL backend, distributed authority and native affected-only execution |
| Generated and linked inputs | Explicit multi-root envelope capture, bounded verified materialization, stable link/mode/provenance digest; capsule bridge normalizes the captured view | No candidate-generated overlay into BASE; no broader native generated/linked runtime qualification |
| Independent challenge/evidence | Existing fault corpus plus capsule capability/cache/audit adversaries, native inventory/result reconciliation and strict JUnit/TRX/Swift/libtest imports | Finite fixtures are not a soundness proof; more real incident and external-state workloads remain |
| Honest performance | Per-suite executed/Hayaku-reused/upstream-cached evidence, phase costs, exact-context net comparisons and a self-pilot including capture/compile | Representative consumer savings, cold/warm workload policy and production acceptance |

## Runner capabilities

| Integration | Implemented evidence | Assurance |
| --- | --- | --- |
| Go | Native package/import/embed/build inputs; execution and shadow challenges | Native runtime/cgo/external inputs unqualified |
| Vitest | Exact 3.2.7/6.4.3, 4.1.11/7.3.1 and 4.1.11/8.1.5 Vitest/Vite pairs; configured transforms, native outcomes, shadow and diagnostic runtime traces | Ordinary fork/thread file scopes only; custom Cloudflare/VM/browser/typecheck contexts rejected |
| Node test, unittest, pytest, Jest, Playwright | Version-gated native collection/execution, whole-file/module proposals, strict terminal outcomes | Every workspace input conservatively owns every unit; browser/services/runtime completeness unqualified |
| Cargo | Offline, locked package graph and package commands; Rust result imports | Build scripts, proc macros and external runtime influence unqualified |
| cargo-nextest | Pinned 0.9.146 configured binary/case inventory, Cargo package proposals and real JUnit fixture | Full original execution retained; automatic native shadow/case-reconciliation integration remains future work |
| JVM, .NET, Swift, Ruby, PHP, Dart, C/C++, Bun, Deno, Bazel | Explicit original full-scope command entries and applicable strict report imports | No claim of native framework discovery, filtering or runtime qualification for these entries |
| Generic command | Preserved original structured command and exit status | Full scope only |

`hayaku catalog` lists these distinctions. Report formats may require a runner-specific reporter/export step; an entry does not imply that every runner emits that dialect directly. [Runner support](runner-support.md) records exact fixture versions separately from accepted protocol ranges.

## Existing foundation

Exact local Git revisions, old/new graph union, deleted/renamed/new input handling, structured argv/cwd, bounded process output/cancellation, strict schemas, immutable source copies, context/tool digests, full fallback reasons, setup without overwrite and portable archives remain supported. Native Vitest traces broaden proposals monotonically and cover observed dynamic reads/probes/directories. Unobserved influence remains a gap and cannot authorize skipping.

The source includes pinned development fixtures and CI matrices. Local fixture success, cross-compilation, remote release acceptance and consumer CI qualification are separate claims; see [verification](verification.md), [release checks](release-checks.md) and [pilot results](pilot-results.md). Published release notes describe bounded implemented capabilities, not completion of every language/framework on the catalog.
