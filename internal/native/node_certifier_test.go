package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/snapshot"
)

func TestNodeSourceCandidateRequestPreflight(t *testing.T) {
	valid := NodeCertifierRequest{Root: "/source", Node: "/tool/node", TypeScriptSDK: "/tool/typescript", NodeSHA256: strings.Repeat("a", 64), CompilerSHA256: strings.Repeat("b", 64), Sources: []string{"math.ts", "test.ts"}, Tests: []string{"test.ts"}}
	if err := validateNodeCertifierRequest(valid); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*NodeCertifierRequest)
	}{
		{"source-count", func(r *NodeCertifierRequest) { r.Sources = make([]string, 129) }},
		{"duplicate-source", func(r *NodeCertifierRequest) { r.Sources = []string{"test.ts", "test.ts"} }},
		{"missing-test", func(r *NodeCertifierRequest) { r.Tests = []string{"absent.ts"} }},
		{"duplicate-test", func(r *NodeCertifierRequest) { r.Tests = []string{"test.ts", "test.ts"} }},
		{"escape", func(r *NodeCertifierRequest) { r.Sources[0] = "../math.ts" }},
		{"unknown-language", func(r *NodeCertifierRequest) { r.Sources[0] = "math.jsx" }},
		{"path-limit", func(r *NodeCertifierRequest) { r.Root = "/" + strings.Repeat("a", 4096) }},
		{"path-nul", func(r *NodeCertifierRequest) { r.Node += "\x00" }},
		{"path-unicode", func(r *NodeCertifierRequest) { r.Sources[0] = "\xff.ts" }},
		{"ambient-node", func(r *NodeCertifierRequest) { r.Node = "node" }},
		{"unpinned-node", func(r *NodeCertifierRequest) { r.NodeSHA256 = "" }},
		{"unpinned-compiler", func(r *NodeCertifierRequest) { r.CompilerSHA256 = strings.Repeat("A", 64) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := valid
			r.Sources, r.Tests = slices.Clone(valid.Sources), slices.Clone(valid.Tests)
			tc.mutate(&r)
			if err := validateNodeCertifierRequest(r); err == nil {
				t.Fatal("invalid declaration accepted")
			}
			if certificate, err := CertifyNodeSources(context.Background(), r); err == nil || certificate != nil {
				t.Fatal("preflight did not reject before tool/source access")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if c, err := CertifyNodeSources(ctx, valid); c != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled capture: %v", err)
	}
}

func TestNodeSourceCandidateResultProtocol(t *testing.T) {
	valid := nodeSourceResult{Schema: 1, Version: nodeSourcePolicy, ParserVersion: "6.0.2", NodeVersion: "22.18.0", Cases: []NodeSourceCase{{"test.ts", "alpha"}, {"test.ts", "beta"}}, SemanticDigest: strings.Repeat("a", 64), EmittedDigest: strings.Repeat("b", 64), EmitterDigest: strings.Repeat("c", 64), Cost: 10, Nodes: 30}
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	result, err := decodeNodeSourceResult(encoded, []string{"test.ts"})
	if err != nil || !slices.Equal(result.Cases, valid.Cases) {
		t.Fatalf("healthy exact independently expected cases: %v", err)
	}
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"duplicate-field", []byte(strings.Replace(string(encoded), `"schema":1`, `"schema":1,"schema":1`, 1))},
		{"extra-field", []byte(strings.Replace(string(encoded), `"schema":1`, `"schema":1,"authority":true`, 1))},
		{"trailing-json", append(slices.Clone(encoded), []byte(" {}")...)},
		{"trailing-garbage", append(slices.Clone(encoded), 'x')},
		{"oversize", []byte(strings.Repeat(" ", (1<<20)+1))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodeNodeSourceResult(tc.data, []string{"test.ts"}); err == nil {
				t.Fatal("malformed candidate protocol accepted")
			}
		})
	}
	for _, mutate := range []func(*nodeSourceResult){
		func(r *nodeSourceResult) { r.ParserVersion = "7.0.2" },
		func(r *nodeSourceResult) { r.Cases = append(r.Cases, r.Cases[0]) },
		func(r *nodeSourceResult) { r.Cases[0].File = "other.ts" },
		func(r *nodeSourceResult) { r.Cost = 100001 },
		func(r *nodeSourceResult) { r.Nodes = 0 },
		func(r *nodeSourceResult) { r.SemanticDigest = "caller-proof" },
	} {
		r := valid
		r.Cases = slices.Clone(valid.Cases)
		mutate(&r)
		data, _ := json.Marshal(r)
		if _, err := decodeNodeSourceResult(data, []string{"test.ts"}); err == nil {
			t.Fatal("invalid candidate inventory or policy accepted")
		}
	}
	if _, err := decodeNodeSourceResult(encoded, []string{"test.ts", "missing.ts"}); err == nil {
		t.Fatal("missing original test scope accepted")
	}
}

func TestNodeSourceCandidateAncestorRoutingAndFullSelectedMetadata(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "selected")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "source.js")
	if err := os.WriteFile(file, []byte("const value=1;"), 0600); err != nil {
		t.Fatal(err)
	}
	c := &NodeSourceCertificate{}
	for _, name := range []string{root, file} {
		if err := c.rememberPath(name); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.verifyIdentities(); err != nil {
		t.Fatal(err)
	}
	beforeParent := c.identities[parent]
	if err := os.WriteFile(filepath.Join(parent, "unrelated-work"), []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	// Deterministic directory bookkeeping change, even on coarse-time filesystems.
	if err := os.Chtimes(parent, beforeParent.ModTime().Add(time.Second), beforeParent.ModTime().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	afterParent, err := os.Lstat(parent)
	if err != nil || sameProgramInfo(beforeParent, afterParent) || !os.SameFile(beforeParent, afterParent) || beforeParent.Mode() != afterParent.Mode() {
		t.Fatalf("actual ancestor control did not isolate unrelated bookkeeping: %v", err)
	}
	if err := c.verifyIdentities(); err != nil {
		t.Fatalf("unselected ancestor bookkeeping invalidates stable selected inputs: %v", err)
	}
	for _, target := range []string{root, file} {
		old, err := os.Lstat(target)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(target, old.Mode().Perm()^0100); err != nil {
			t.Fatal(err)
		}
		if err := c.verifyIdentities(); err == nil {
			t.Fatal("selected root/file full-mode drift ignored")
		}
		if err := os.Chmod(target, old.Mode().Perm()); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if err := os.Chmod(parent, beforeParent.Mode().Perm()); err != nil {
			t.Errorf("restore test-owned ancestor permissions: %v", err)
		}
	})
	if err := os.Chmod(parent, beforeParent.Mode().Perm()^0100); err != nil {
		t.Fatal(err)
	}
	if err := c.verifyIdentities(); err == nil {
		t.Fatal("ancestor permission drift ignored")
	}
}

func TestNodeSourceCandidateRawRequestedPinsRejectRestoredMetadataBytes(t *testing.T) {
	root := t.TempDir()
	sdk := filepath.Join(root, "sdk")
	if err := os.MkdirAll(filepath.Join(sdk, "lib"), 0700); err != nil {
		t.Fatal(err)
	}
	node, compiler := filepath.Join(root, "node"), filepath.Join(sdk, "lib", "typescript.js")
	for _, name := range []string{node, compiler} {
		if err := os.WriteFile(name, []byte("original bytes"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	c := &NodeSourceCertificate{request: NodeCertifierRequest{Node: node, TypeScriptSDK: sdk, NodeSHA256: digest([]byte("original bytes")), CompilerSHA256: digest([]byte("original bytes"))}}
	for _, name := range []string{node, compiler} {
		if err := c.rememberPath(name); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.verifyToolPins(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{node, compiler} {
		before, err := os.Lstat(target)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("modified bytes"), before.Mode().Perm()); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(target, before.ModTime(), before.ModTime()); err != nil {
			t.Fatal(err)
		}
		if err := c.verifyIdentities(); err != nil {
			t.Fatalf("control metadata should remain identical: %v", err)
		}
		if err := c.verifyToolPins(context.Background()); err == nil {
			t.Fatal("same-path same-size restored-metadata bytes bypassed requested pin")
		}
		if err := os.WriteFile(target, []byte("original bytes"), before.Mode().Perm()); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(target, before.ModTime(), before.ModTime()); err != nil {
			t.Fatal(err)
		}
	}
}

func installedNodeCertifierRequest(t *testing.T, files map[string]string, test string) NodeCertifierRequest {
	t.Helper()
	node, sdk := os.Getenv("HAYAKU_NODE_CERTIFIER_NODE"), os.Getenv("HAYAKU_NODE_CERTIFIER_TYPESCRIPT")
	if node == "" || sdk == "" {
		if os.Getenv("HAYAKU_NODE_CERTIFIER_REQUIRED") == "1" {
			t.Fatal("explicit installed Node and TypeScript SDK required")
		}
		t.Skip("explicit installed source-parser acceptance not requested")
	}
	root := t.TempDir()
	sources := []string{}
	for name, content := range files {
		destination := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, name)
	}
	slices.Sort(sources)
	resolved, err := filepath.EvalSymlinks(node)
	if err != nil {
		t.Fatal(err)
	}
	nodeSHA, err := snapshot.FileDigest(context.Background(), resolved, 256<<20)
	if err != nil {
		t.Fatal(err)
	}
	compilerSHA, err := snapshot.FileDigest(context.Background(), filepath.Join(sdk, "lib", "typescript.js"), 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	return NodeCertifierRequest{Root: root, Node: node, TypeScriptSDK: sdk, NodeSHA256: nodeSHA, CompilerSHA256: compilerSHA, Sources: sources, Tests: []string{test}}
}

func TestInstalledNodeSourceCandidateJavaScriptAndTypeScript(t *testing.T) {
	for _, extension := range []string{"js", "ts"} {
		t.Run(extension, func(t *testing.T) {
			math := `export function add(a, b) { return a + b; }`
			if extension == "ts" {
				math = `export function add(a: number, b: number): number { return a + b; }`
			}
			test := "test." + extension
			r := installedNodeCertifierRequest(t, map[string]string{"math." + extension: math, test: `import { test, expect } from 'vitest'; import { add } from './math.js'; test('alpha', () => { expect(add(1, 2)).toBe(3); }); test('beta', () => { const value = false ? add(4, 5) : add(6, 7); expect(value).toBeGreaterThan(10); });`}, test)
			c, err := CertifyNodeSources(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			want := []NodeSourceCase{{test, "alpha"}, {test, "beta"}}
			summary := c.Summary()
			if !slices.Equal(summary.Cases, want) || summary.ToolTrust != "unqualified" || summary.ParserVersion != "6.0.2" || summary.Cost < 1 {
				t.Fatalf("candidate differs from authored inventory: %+v", summary)
			}
			if err := c.Verify(context.Background()); err != nil {
				t.Fatal(err)
			}
			// Caller-owned inputs and returned summaries cannot mutate the seal.
			r.Sources[0], r.Tests[0], summary.Cases[0].Label = "missing.js", "other.js", "mutated"
			if err := c.Verify(context.Background()); err != nil || !slices.Equal(c.Summary().Cases, want) {
				t.Fatalf("caller mutation reached retained certificate: %v", err)
			}
			if err := os.WriteFile(filepath.Join(r.Root, "math."+extension), []byte(strings.Replace(math, "+", "-", 1)), 0600); err != nil {
				t.Fatal(err)
			}
			if err := c.Verify(context.Background()); err == nil {
				t.Fatal("source changed after healthy certificate")
			}
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			if err := c.Verify(context.Background()); err == nil || c.Summary().Policy != "" {
				t.Fatal("closed certificate usable")
			}
		})
	}
}

func TestInstalledNodeSourceCandidateRejectsDormantAndEscapedEffects(t *testing.T) {
	healthy := `import { test, expect } from 'vitest'; test('alpha', () => { expect(1 + 2).toBe(3); });`
	r := installedNodeCertifierRequest(t, map[string]string{"test.ts": healthy}, "test.ts")
	c, err := CertifyNodeSources(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, source string }{
		{"dormant-time", `function unused() { return false ? Date.now() : 1; }` + healthy},
		{"dormant-random", `function unused() { return false ? Math.random() : 1; }` + healthy},
		{"dormant-eval", `function unused() { return eval('1'); }` + healthy},
		{"callback-escape", `import { test, expect } from 'vitest'; const callback = () => { expect(1).toBe(1); }; test('alpha', callback);`},
		{"shadowed-import", `import { test, expect } from 'vitest'; function f(expect) { return expect(1); } test('alpha', () => { expect(1).toBe(1); });`},
		{"prototype", `function unused() { return (1).constructor; }` + healthy},
		{"getter", `const x = {get value() { return 1; }};` + healthy},
		{"computed", `function unused() { return globalThis['process']; }` + healthy},
		{"async", `import { test, expect } from 'vitest'; test('alpha', async () => { expect(1).toBe(1); });`},
		{"skip", `import { test, expect } from 'vitest'; test.skip('alpha', () => { expect(1).toBe(1); });`},
		{"only", `import { test, expect } from 'vitest'; test.only('alpha', () => { expect(1).toBe(1); });`},
		{"hooks", `import { test, expect, beforeAll } from 'vitest'; test('alpha', () => { expect(1).toBe(1); });`},
		{"native-module", `import { readFileSync } from 'node:fs';` + healthy},
		{"dynamic-import", `function unused() { return import('./other.js'); }` + healthy},
		{"recursion", `function f(x) { return f(x); }` + healthy},
		{"mutual-recursion", `function f(x) { return g(x); } function g(x) { return f(x); }` + healthy},
		{"loop", `function f(x) { while (true) {} return x; }` + healthy},
		{"type-erased-forbidden", `declare function dangerous(): number;` + healthy},
		{"type-erased-assertion", `const n = (1 as any);` + healthy},
		{"unknown-syntax", `class Box { value = 1; }` + healthy},
		{"actual-jsx", `const value = <div/>;` + healthy},
		{"await-using-global", `await using value = 1;` + healthy},
		{"await-using-local", `function unused() { await using value = 1; return 1; }` + healthy},
		{"strict-arguments", `function unused(arguments) { return arguments; }` + healthy},
		{"strict-await", `function unused(await) { return await; }` + healthy},
		{"strict-yield", `function unused(yield) { return yield; }` + healthy},
		{"strict-future-reserved", `function unused(implements) { return implements; }` + healthy},
		{"reference-directive", "/// <reference types='unbound'/>\n" + healthy},
		{"compiler-pragma", "// @ts-nocheck\n" + healthy},
		{"duplicate-case", healthy + `test('alpha', () => { expect(2).toBe(2); });`},
		{"missing-module", `import { value } from './missing.js';` + healthy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(r.Root, "test.ts"), []byte(tc.source), 0600); err != nil {
				t.Fatal(err)
			}
			candidate, err := CertifyNodeSources(context.Background(), r)
			if candidate != nil {
				_ = candidate.Close()
			}
			if err == nil || candidate != nil || !strings.Contains(err.Error(), "certification") {
				t.Fatalf("forbidden source gained a candidate: %v", err)
			}
		})
	}
}

func TestInstalledNodeSourceCandidateBoundsCallDAG(t *testing.T) {
	source := func(depth int) string {
		body := `import { test, expect } from 'vitest'; function f0(x) { return x + 1; }`
		for i := 1; i <= depth; i++ {
			body += fmt.Sprintf(" function f%d(x) { return f%d(x) + f%d(x); }", i, i-1, i-1)
		}
		return body + fmt.Sprintf("test('alpha', () => { expect(f%d(1)).toBeGreaterThan(1); });", depth)
	}
	r := installedNodeCertifierRequest(t, map[string]string{"test.js": source(8)}, "test.js")
	c, err := CertifyNodeSources(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.Root, "test.js"), []byte(source(18)), 0600); err != nil {
		t.Fatal(err)
	}
	if c, err := CertifyNodeSources(context.Background(), r); err == nil || c != nil {
		if c != nil {
			_ = c.Close()
		}
		t.Fatal("exponentially repeated call DAG accepted after bounded DAG control")
	}
}

func TestInstalledNodeSourceCandidateRejectsEmitterContradictions(t *testing.T) {
	// A synthetic, explicitly unqualified SDK forwards the actual parser while
	// challenging emitted-source validation. It supplies no publisher trust.
	r := installedNodeCertifierRequest(t, map[string]string{"test.ts": `import { test, expect } from 'vitest'; test('alpha', () => { const value: number = 1 + 2; expect(value).toBe(3); });`}, "test.ts")
	compiler, err := readBounded(filepath.Join(r.TypeScriptSDK, "lib", "typescript.js"), 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	sdk := t.TempDir()
	if err := os.Mkdir(filepath.Join(sdk, "lib"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sdk, "lib", "core.cjs"), compiler, 0600); err != nil {
		t.Fatal(err)
	}
	r.TypeScriptSDK = sdk
	shim := func(transform string) string {
		return `const actual = require('./core.cjs'); module.exports = new Proxy(actual, { get(target, key) { if (key !== 'transpileModule') return Reflect.get(target, key); return (...args) => { const result = actual.transpileModule(...args); ` + transform + ` return result; }; } });`
	}
	write := func(transform string) {
		t.Helper()
		file := filepath.Join(sdk, "lib", "typescript.js")
		if err := os.WriteFile(file, []byte(shim(transform)), 0600); err != nil {
			t.Fatal(err)
		}
		var err error
		r.CompilerSHA256, err = snapshot.FileDigest(context.Background(), file, 64<<20)
		if err != nil {
			t.Fatal(err)
		}
	}
	emitted := `import { test, expect } from 'vitest'; test('alpha', () => { const value = 1 + 2; expect(value).toBe(3); });`
	setOutput := func(source string) string {
		encoded, _ := json.Marshal(source)
		return "result.outputText = " + string(encoded) + ";"
	}
	write(setOutput(emitted))
	c, err := CertifyNodeSources(context.Background(), r)
	if err != nil {
		t.Fatalf("forwarding parser/emitter control: %v", err)
	}
	if !slices.Equal(c.Summary().Cases, []NodeSourceCase{{"test.ts", "alpha"}}) || c.Summary().ToolTrust != "unqualified" {
		t.Fatal("synthetic SDK acquired trust or changed healthy cases")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, transform string }{
		{"arithmetic", setOutput(strings.Replace(emitted, "1 + 2", "1 - 2", 1))},
		{"case-identity", setOutput(strings.Replace(emitted, "'alpha'", "'other'", 1))},
		{"emitted-global-effect", setOutput(emitted + "\nprocess.exit(0);\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			write(tc.transform)
			candidate, err := CertifyNodeSources(context.Background(), r)
			if candidate != nil {
				_ = candidate.Close()
			}
			if candidate != nil || err == nil || !strings.Contains(err.Error(), "certification") {
				t.Fatalf("contradictory emitted source accepted: %v", err)
			}
		})
	}
}

func TestInstalledNodeSourceCandidateRejectsImportedTestOwnership(t *testing.T) {
	alpha := `import { test, expect } from 'vitest'; test('alpha', () => { expect(1).toBe(1); });`
	beta := `import { test, expect } from 'vitest'; export const value = 1; test('beta', () => { expect(value).toBe(1); });`
	r := installedNodeCertifierRequest(t, map[string]string{"test.js": alpha, "other.js": beta, "dependency.js": `export const local = 1;`}, "test.js")
	r.Tests = append(r.Tests, "other.js")
	c, err := CertifyNodeSources(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.Summary().Cases, []NodeSourceCase{{"other.js", "beta"}, {"test.js", "alpha"}}) {
		t.Fatal("healthy disjoint entries differ from authored ownership")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, file, source string }{
		{"direct", "test.js", `import { value } from './other.js'; ` + alpha},
		{"dormant-indirect", "dependency.js", `import { value } from './other.js'; export function unused() { return value; }`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(r.Root, "test.js"), []byte(alpha), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(r.Root, tc.file), []byte(tc.source), 0600); err != nil {
				t.Fatal(err)
			}
			candidate, err := CertifyNodeSources(context.Background(), r)
			if candidate != nil {
				_ = candidate.Close()
			}
			if candidate != nil || err == nil || !strings.Contains(err.Error(), "certification") {
				t.Fatalf("imported entry gained declaration ownership: %v", err)
			}
		})
	}
}

func TestInstalledNodeSourceCandidateStrictBindingsForJavaScriptAndTypeScript(t *testing.T) {
	healthy := `import { test, expect } from 'vitest'; function increment(value) { return value + 1; } test('alpha', () => { expect(increment(2)).toBe(3); });`
	for _, extension := range []string{"js", "ts"} {
		t.Run(extension, func(t *testing.T) {
			file := "test." + extension
			r := installedNodeCertifierRequest(t, map[string]string{file: healthy}, file)
			c, err := CertifyNodeSources(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			for _, reserved := range []string{"arguments", "await", "yield", "implements"} {
				t.Run(reserved, func(t *testing.T) {
					source := "function unused(" + reserved + ") { return " + reserved + "; }" + healthy
					if err := os.WriteFile(filepath.Join(r.Root, file), []byte(source), 0600); err != nil {
						t.Fatal(err)
					}
					candidate, err := CertifyNodeSources(context.Background(), r)
					if candidate != nil {
						_ = candidate.Close()
					}
					if candidate != nil || err == nil || !strings.Contains(err.Error(), "certification") {
						t.Fatalf("strict module binding admitted: %v", err)
					}
				})
			}
			for name, declaration := range map[string]string{
				"await-using-global": "await using value = 1;",
				"await-using-local":  "function unused() { await using value = 1; return 1; }",
			} {
				t.Run(name, func(t *testing.T) {
					if err := os.WriteFile(filepath.Join(r.Root, file), []byte(declaration+healthy), 0600); err != nil {
						t.Fatal(err)
					}
					candidate, err := CertifyNodeSources(context.Background(), r)
					if candidate != nil {
						_ = candidate.Close()
					}
					if candidate != nil || err == nil || !strings.Contains(err.Error(), "certification") {
						t.Fatalf("await-using declaration admitted: %v", err)
					}
				})
			}

		})
	}
}
