package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/noderuntime"
)

// Native qualification uses an explicitly provided, existing standalone runtime.
// These tests never install packages or load another project's configuration.
func vitestNativeFixture(t *testing.T, hidden bool) (string, string, model.Config) {
	t.Helper()
	node, modules := os.Getenv("HAYAKU_VITEST_NODE"), os.Getenv("HAYAKU_VITEST_MODULES")
	if node == "" && modules == "" {
		t.Skip("set HAYAKU_VITEST_NODE and HAYAKU_VITEST_MODULES for native qualification")
	}
	if !filepath.IsAbs(node) || !filepath.IsAbs(modules) {
		t.Fatal("native qualification requires explicit absolute Node and dependency paths")
	}
	root := t.TempDir()
	c := model.Config{Schema: model.Schema, Context: model.Context{ID: "native-vitest-fixture", OS: runtime.GOOS, Arch: runtime.GOARCH, Env: map[string]string{"CI": "true"}}, Workspaces: []model.Workspace{{
		ID: "js", Root: ".", Adapter: "vitest", NodeRuntime: &model.NodeRuntime{Node: node, Modules: modules},
		Command: model.Command{Dir: ".", Executable: filepath.Join(modules, ".bin", "vitest"), Args: []string{"run", "--globals", "--config", "vitest.config.mjs"}},
	}}}
	configuration, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"hayaku.json":       string(configuration),
		"package.json":      "{\"name\":\"hayaku-native-fixture\",\"private\":true,\"type\":\"module\"}\n",
		".gitignore":        "node_modules/\n",
		"vitest.config.mjs": "import { fileURLToPath } from 'node:url';\nexport default { resolve: { alias: { '@value': fileURLToPath(new URL('./value.ts', import.meta.url)) } }, test: { globals: true, fileParallelism: false } };\n",
		"value.ts":          "export const value: number = 1;\n",
		"a.test.ts":         "import { value } from '@value';\ntest('same name', () => { expect(value).toBeGreaterThan(0) });\n",
		"b.test.ts":         "test('same name', () => { expect(2).toBe(2) });\n",
		"resource.json":     "{\"value\":1}\n",
	}
	if hidden {
		files["b.test.ts"] = "import { readFileSync } from 'node:fs';\nimport { join } from 'node:path';\ntest('same name', () => { const path = join(process.cwd(), 'value' + '.ts'); expect(readFileSync(path, 'utf8')).not.toContain('= 2'); });\n"
	}
	for name, contents := range files {
		vitestWrite(t, root, name, contents)
	}
	vitestGit(t, root, "init", "-q")
	vitestGit(t, root, "config", "user.name", "Fixture")
	vitestGit(t, root, "config", "user.email", "fixture@example.test")
	vitestGit(t, root, "config", "commit.gpgsign", "false")
	vitestGit(t, root, "add", ".")
	vitestGit(t, root, "commit", "-qm", "base")
	return root, vitestGit(t, root, "rev-parse", "HEAD"), c
}

func vitestWrite(t *testing.T, root, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func vitestGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture Git operation failed: %v", err)
	}
	return strings.TrimSpace(string(output))
}

func vitestCandidate(t *testing.T, root string) string {
	t.Helper()
	vitestGit(t, root, "add", ".")
	vitestGit(t, root, "commit", "-qm", "candidate")
	return vitestGit(t, root, "rev-parse", "HEAD")
}

func TestNativeVitestAliasProposalAndRequiredFullRun(t *testing.T) {
	root, base, c := vitestNativeFixture(t, false)
	vitestWrite(t, root, "value.ts", "export const value: number = 2;\n")
	candidate := vitestCandidate(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	plan, err := Build(ctx, root, base, candidate, c, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Mode != "full-fallback" || len(plan.Selected) != 2 || len(plan.Proposed) != 1 || plan.Proposed[0].Selector != "a.test.ts" {
		t.Fatalf("configured TypeScript alias proposal incorrect: mode=%s selected=%v proposed=%v", plan.Mode, plan.Selected, plan.Proposed)
	}
	if len(plan.Commands) != 1 || strings.Join(plan.Commands[0].Args, " ") != "run --globals --config vitest.config.mjs" {
		t.Fatal("required full command was narrowed")
	}
	execution, err := Execute(ctx, root, c, plan)
	if err != nil || !execution.Passed || len(execution.Commands) != 1 || !execution.Commands[0].Complete {
		t.Fatalf("required native full run incomplete: err=%v execution=%+v", err, execution)
	}
	shadow, err := Shadow(ctx, root, c, plan)
	if err != nil || !shadow.ComparisonValid || len(shadow.ObservedMisses) != 0 || !shadow.Proposal.Passed || !shadow.Full.Passed {
		t.Fatalf("native shadow comparison invalid: err=%v result=%+v", err, shadow)
	}
	vitestWrite(t, root, "untracked-input.txt", "unexpected runtime input\n")
	if _, err := Execute(ctx, root, c, plan); err == nil {
		t.Fatal("dirty candidate accepted for native execution")
	}
}

func TestNativeVitestHiddenResourceFailureIsObservedMiss(t *testing.T) {
	root, base, c := vitestNativeFixture(t, true)
	vitestWrite(t, root, "value.ts", "export const value: number = 2;\n")
	candidate := vitestCandidate(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	plan, err := Build(ctx, root, base, candidate, c, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Proposed) != 1 || len(plan.Selected) != 2 {
		t.Fatalf("hidden runtime dependency unexpectedly certified: %+v", plan.Proposed)
	}
	shadow, err := Shadow(ctx, root, c, plan)
	if err == nil || !shadow.ComparisonValid || len(shadow.ObservedMisses) == 0 || !shadow.Proposal.Passed || shadow.Full.Passed {
		t.Fatalf("omitted native failure not detected: err=%v result=%+v", err, shadow)
	}
	full, err := Execute(ctx, root, c, plan)
	if err == nil || full.Passed || len(full.Commands) != 1 || !full.Commands[0].Complete {
		t.Fatalf("completed failing suite lost its failure: err=%v result=%+v", err, full)
	}
}

func TestNativeVitestUnmappedResourceAndNewDeletedTestsBroaden(t *testing.T) {
	root, base, c := vitestNativeFixture(t, false)
	vitestWrite(t, root, "resource.json", "{\"value\":2}\n")
	if err := os.Remove(filepath.Join(root, "b.test.ts")); err != nil {
		t.Fatal(err)
	}
	vitestWrite(t, root, "new.test.ts", "test('newly discovered', () => { expect(true).toBe(true) });\n")
	candidate := vitestCandidate(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	plan, err := Build(ctx, root, base, candidate, c, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Selected) != 2 || len(plan.Proposed) != 2 || plan.Mode != "full-fallback" {
		t.Fatalf("unknown resource influence failed to broaden: selected=%v proposed=%v", plan.Selected, plan.Proposed)
	}
	for _, unit := range plan.Selected {
		if unit.Selector == "b.test.ts" {
			t.Fatal("deleted test remained executable")
		}
	}
	canceled, cancelNow := context.WithCancel(ctx)
	cancelNow()
	shadow, err := Shadow(canceled, root, c, plan)
	if err == nil || shadow.ComparisonValid {
		t.Fatal("canceled native shadow comparison became valid")
	}
}

func TestNativeVitestProjectsAndDuplicateNamesRemainDistinct(t *testing.T) {
	root, _, c := vitestNativeFixture(t, false)
	vitestWrite(t, root, "vitest.config.mjs", "import { fileURLToPath } from 'node:url';\nexport default { resolve: { alias: { '@value': fileURLToPath(new URL('./value.ts', import.meta.url)) } }, test: { globals: true, fileParallelism: false, projects: [{ extends: true, test: { name: 'one', include: ['a.test.ts', 'b.test.ts'] } }, { extends: true, test: { name: 'two', include: ['a.test.ts', 'b.test.ts'] } }] } };\n")
	vitestWrite(t, root, "a.test.ts", "import { value } from '@value';\ntest('same name', () => { expect(value).toBeGreaterThan(0) });\ntest('same name', () => { expect(value).toBeGreaterThan(0) });\n")
	base := vitestCandidate(t, root)
	vitestWrite(t, root, "value.ts", "export const value: number = 2;\n")
	candidate := vitestCandidate(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	plan, err := Build(ctx, root, base, candidate, c, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Selected) != 4 || len(plan.Proposed) != 2 {
		t.Fatalf("same-file project scopes collapsed: selected=%v proposed=%v", plan.Selected, plan.Proposed)
	}
	if plan.Proposed[0].ID == plan.Proposed[1].ID {
		t.Fatal("native project identity collision")
	}
	shadow, err := Shadow(ctx, root, c, plan)
	if err != nil || !shadow.ComparisonValid || len(shadow.Inconsistencies) != 0 {
		t.Fatalf("duplicate native test names or project scopes corrupted comparison: err=%v result=%+v", err, shadow)
	}
}

func TestNativeVitestFailedHookRemainsCompletedFailure(t *testing.T) {
	root, base, c := vitestNativeFixture(t, false)
	vitestWrite(t, root, "a.test.ts", "beforeAll(() => { throw new Error('fixture hook failure'); });\ntest('same name', () => { expect(true).toBe(true) });\n")
	candidate := vitestCandidate(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	plan, err := Build(ctx, root, base, candidate, c, false)
	if err != nil {
		t.Fatal(err)
	}
	shadow, err := Shadow(ctx, root, c, plan)
	if err == nil || !shadow.ComparisonValid || len(shadow.ObservedMisses) != 0 || shadow.Proposal.Passed || shadow.Full.Passed {
		t.Fatalf("native hook failure lost completion or failure identity: err=%v result=%+v", err, shadow)
	}
	for _, execution := range []Execution{shadow.Proposal, shadow.Full} {
		if len(execution.Commands) != 1 || !execution.Commands[0].Complete {
			t.Fatal("terminal hook failure incorrectly classified as incomplete execution")
		}
	}
}

func TestNativeVitestBoundIgnoredModulesAndDependencyDrift(t *testing.T) {
	root, _, c := vitestNativeFixture(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	w := &c.Workspaces[0]
	identity, err := noderuntime.Identity(ctx, *w.NodeRuntime)
	if err != nil {
		t.Fatal(err)
	}
	modules := filepath.Join(root, "node_modules")
	if err := noderuntime.CopyModules(ctx, *w.NodeRuntime, modules, identity); err != nil {
		t.Fatal(err)
	}
	w.NodeRuntime.Modules = modules
	w.Command.Executable = filepath.Join(modules, ".bin", "vitest")
	configuration, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	vitestWrite(t, root, "hayaku.json", string(configuration))
	base := vitestCandidate(t, root)
	vitestWrite(t, root, "value.ts", "export const value: number = 2;\n")
	candidate := vitestCandidate(t, root)
	plan, err := Build(ctx, root, base, candidate, c, false)
	if err != nil {
		t.Fatal(err)
	}
	full, err := Execute(ctx, root, c, plan)
	if err != nil || !full.Passed {
		t.Fatalf("explicitly bound ignored dependency tree rejected: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if status := Run(ctx, []string{"run", "--root", root, "--base", base, "--candidate", candidate}, &stdout, &stderr); status != 0 {
		t.Fatalf("CLI rejected bound ignored dependencies: %s", stderr.String())
	}
	// Mutate only this disposable runtime copy, never the shared opt-in source.
	vitestWrite(t, root, "node_modules/unexpected-runtime-input.txt", "dependency tree changed\n")
	if _, err := Execute(ctx, root, c, plan); err == nil {
		t.Fatal("dependency bytes changed after planning but execution accepted")
	}
}

func TestNativeVitestUnhandledErrorsInvalidateComparison(t *testing.T) {
	root, base, c := vitestNativeFixture(t, false)
	vitestWrite(t, root, "b.test.ts", "test('unhandled failure', async () => { void Promise.reject(new Error('fixture rejection')); await new Promise(resolve => setTimeout(resolve, 20)); expect(true).toBe(true); });\n")
	candidate := vitestCandidate(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	plan, err := Build(ctx, root, base, candidate, c, false)
	if err != nil {
		t.Fatal(err)
	}
	shadow, err := Shadow(ctx, root, c, plan)
	if err == nil || shadow.ComparisonValid || shadow.Full.Passed || len(shadow.Full.Commands) != 1 || shadow.Full.Commands[0].Vitest == nil || shadow.Full.Commands[0].Vitest.UnhandledErrors == 0 {
		t.Fatalf("unattributed native error became a valid comparison: err=%v result=%+v", err, shadow)
	}
}

func TestNativeVitestSetupChangeBroadensEveryConfiguredFile(t *testing.T) {
	root, _, c := vitestNativeFixture(t, false)
	vitestWrite(t, root, "vitest.config.mjs", "import { fileURLToPath } from 'node:url';\nexport default { resolve: { alias: { '@value': fileURLToPath(new URL('./value.ts', import.meta.url)) } }, test: { globals: true, setupFiles: ['./setup.ts'], fileParallelism: false } };\n")
	vitestWrite(t, root, "setup.ts", "globalThis.fixtureValue = 1;\n")
	base := vitestCandidate(t, root)
	vitestWrite(t, root, "setup.ts", "globalThis.fixtureValue = 2;\n")
	candidate := vitestCandidate(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	plan, err := Build(ctx, root, base, candidate, c, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Proposed) != 2 || len(plan.Selected) != 2 || plan.Mode != "full-fallback" {
		t.Fatalf("configured setup influence omitted a test file: proposed=%v selected=%v", plan.Proposed, plan.Selected)
	}
}

func TestNativeVitestConfiguredRootIsNotOverridden(t *testing.T) {
	root, _, c := vitestNativeFixture(t, false)
	if err := os.Mkdir(filepath.Join(root, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	vitestWrite(t, root, "nested/inside.test.ts", "test('inside configured root', () => { expect(true).toBe(true) });\n")
	vitestWrite(t, root, "b.test.ts", "test('outside configured root', () => { throw new Error('must not execute'); });\n")
	vitestWrite(t, root, "vitest.config.mjs", "export default { root: './nested', test: { globals: true } };\n")
	candidate := vitestCandidate(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	plan, err := Build(ctx, root, candidate, candidate, c, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Selected) != 1 || plan.Selected[0].Selector != "nested/inside.test.ts" {
		t.Fatalf("configured root changed native full inventory: %+v", plan.Selected)
	}
	full, err := Execute(ctx, root, c, plan)
	if err != nil || !full.Passed || !full.Commands[0].Complete {
		t.Fatalf("configured root execution changed: err=%v result=%+v", err, full)
	}
}

func TestNativeVitestContradictoryCIContextIsRejected(t *testing.T) {
	root, base, c := vitestNativeFixture(t, false)
	c.Context.Env["CI"] = "false"
	// A forced CI value would silently omit b from this conditional inventory.
	vitestWrite(t, root, "vitest.config.mjs", "export default { test: { globals: true, include: process.env.CI === 'true' ? ['a.test.ts'] : ['a.test.ts', 'b.test.ts'] } };\n")
	candidate := vitestCandidate(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if _, err := Build(ctx, root, base, candidate, c, false); err == nil {
		t.Fatal("contradictory CI context was silently replaced")
	}
}
