package native

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	vitestadapter "github.com/Cyberlane/hayaku/internal/adapter/vitest"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
)

func nodeRuntimeInventory() model.ExecutionInventory {
	units := []model.Unit{{ID: "ts::alpha.test.ts", Workspace: "ts", Kind: "vitest-file", Selector: "alpha.test.ts"}, {ID: "ts::beta.test.ts", Workspace: "ts", Kind: "vitest-file", Selector: "beta.test.ts"}}
	return model.ExecutionInventory{Schema: model.ExecutionSchema, Units: units, Cases: []model.Case{{Unit: units[0].ID, ID: `["alpha",0]`}, {Unit: units[1].ID, ID: `["beta",0]`}}}
}

func TestRestrictedNodeRuntimeSelectionKeepsExactCases(t *testing.T) {
	all := nodeRuntimeInventory()
	want, err := selectNodeInventory(all, all.Units[1:])
	if err != nil || len(want.Units) != 1 || len(want.Cases) != 1 || want.Cases[0] != all.Cases[1] {
		t.Fatal("selected case ownership changed", err)
	}
	for _, selected := range [][]model.Unit{{}, {all.Units[0], all.Units[0]}, {{ID: "foreign", Workspace: "ts"}}} {
		if _, err := selectNodeInventory(all, selected); err == nil {
			t.Fatal("invalid selected scope accepted")
		}
	}
	full, err := selectNodeInventory(all, nil)
	if err != nil || !slices.Equal(full.Cases, all.Cases) {
		t.Fatal("nil selection changed original scope")
	}
}

func TestRestrictedNodeRuntimeRejectsUnqualifiedContextsBeforeCapture(t *testing.T) {
	for _, backend := range []Backend{{}, {Platform: "darwin", Architecture: "arm64", Identity: "bound"}, {Platform: "linux", Architecture: "amd64", Identity: "bound"}, {Platform: "linux", Architecture: "arm64", Identity: "bound", Sandbox: "public"}} {
		if p, err := PrepareVitestRuntime(context.Background(), backend, NodeRuntimeRequest{}); err == nil || p != nil {
			t.Fatal("unqualified runtime profile admitted")
		}
	}
	p := &PreparedVitestRuntime{}
	if p.Verify(context.Background()) == nil {
		t.Fatal("empty public runtime supplied authority")
	}
	if p.Close() != nil || p.Close() != nil {
		t.Fatal("empty context cleanup not idempotent")
	}
}

func TestRestrictedNodeRuntimeFunctionalIdentityExcludesCaptureLocation(t *testing.T) {
	makeRuntime := func(dir string) *Runtime {
		return &Runtime{dir: dir, loader: &dynamicFile{digest: "loader", image: dynamicELF{soname: "ld-linux-aarch64.so.1"}}, libraries: map[string]*dynamicFile{"libc.so.6": {digest: "libc"}}, addons: map[string]*dynamicFile{"rolldown": {digest: "addon"}}}
	}
	a, b := makeRuntime("/fresh/a"), makeRuntime("/fresh/b")
	if nodeRuntimeFunctionalDigest(a) != nodeRuntimeFunctionalDigest(b) {
		t.Fatal("fresh transport path poisoned stable runtime context")
	}
	b.addons["rolldown"].digest = "changed"
	if nodeRuntimeFunctionalDigest(a) == nodeRuntimeFunctionalDigest(b) {
		t.Fatal("native addon bytes absent from functional context")
	}
}

func TestRestrictedNodeRuntimeRequiresFixedRoleBytes(t *testing.T) {
	// Independently recorded export identities; these descriptors exercise the
	// fixed admission table only. They lack capture seals and cannot launch.
	want := map[string]string{
		"ld-linux-aarch64.so.1": "a2db553caf40402c0db144234c5d146b2cf7a69d97508341724639661b226025",
		"libdl.so.2":            "e97c2f373f3495f135eaf302041d9f24f3ce2ac44c15ca2743488b396c81a9a7",
		"libstdc++.so.6":        "532f3a12d7b8fef6cc7a064ea106b6c4ab2365674b73e743e4a385d4ffb6c8ab",
		"libm.so.6":             "c36873edf7004eac309f7bea0b99930e050cba6b4c28482cc9178ec6ed1d5b2b",
		"libgcc_s.so.1":         "046856f95f4636f1fc7c3a12bf4f3cd5634c2fc5145c3fdf7395d4f349fa69c7",
		"libpthread.so.0":       "9b60c3aa448ef2b7f22c8538962b0f4d7c944b7f08aafd34b563ac5ec51daf9d",
		"libc.so.6":             "50227f7a4dcb8b2a40b84aedff1c101f7e0aa5a4fc180ba882b52e53be226427",
	}
	descriptors := func() *Runtime {
		r := &Runtime{libraries: map[string]*dynamicFile{}, addons: map[string]*dynamicFile{"rolldown": {digest: vitestRolldownSHA256}}}
		for name, digest := range want {
			file := &dynamicFile{digest: digest, image: dynamicELF{soname: name}}
			if name == "ld-linux-aarch64.so.1" {
				r.loader = file
			} else {
				r.libraries[name] = file
			}
		}
		return r
	}
	ctx, dir := context.Background(), t.TempDir()
	declaration := RuntimeRequest{Loader: filepath.Join(dir, "ld-linux-aarch64.so.1")}
	for name := range want {
		needed := []string{"ld-linux-aarch64.so.1"}
		if name == "ld-linux-aarch64.so.1" {
			needed = nil
		} else {
			declaration.Libraries = append(declaration.Libraries, RuntimeLibrary{Name: name, Path: filepath.Join(dir, name)})
		}
		if err := os.WriteFile(filepath.Join(dir, name), dynamicFixtureBytes("", name, needed), 0555); err != nil {
			t.Fatal(err)
		}
	}
	addon := filepath.Join(dir, "rolldown.node")
	if err := os.WriteFile(addon, dynamicFixtureBytes("", "", []string{"libc.so.6"}), 0555); err != nil {
		t.Fatal(err)
	}
	declaration.Addons = []RuntimeAddon{{Name: "rolldown", Path: addon}}
	forged, err := CaptureRuntime(ctx, declaration)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := forged.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := forged.Verify(ctx); err != nil {
		t.Fatal("compatible ELF replacement failed generic capture", err)
	}
	if verifyVitestRuntimeClosure(forged) == nil {
		t.Fatal("caller-captured compatible ELF bytes received fixed compiler trust")
	}
	if r := descriptors(); verifyVitestRuntimeClosure(r) != nil || r.Verify(ctx) == nil {
		t.Fatal("fixed role table changed or unsealed test descriptors could launch")
	}
	for name := range want {
		t.Run(name, func(t *testing.T) {
			r := descriptors()
			if name == "ld-linux-aarch64.so.1" {
				r.loader = forged.loader
			} else {
				r.libraries[name] = forged.libraries[name]
			}
			if verifyVitestRuntimeClosure(r) == nil {
				t.Fatal("compatible replacement role accepted")
			}
		})
	}
	for name, change := range map[string]func(*Runtime){
		"missing": func(r *Runtime) { delete(r.libraries, "libc.so.6") },
		"extra":   func(r *Runtime) { r.libraries["libextra.so"] = r.libraries["libc.so.6"] },
		"renamed": func(r *Runtime) {
			r.libraries["libother.so"] = r.libraries["libc.so.6"]
			delete(r.libraries, "libc.so.6")
		},
		"soname": func(r *Runtime) { r.libraries["libc.so.6"].image.soname = "libother.so" },
		"nil":    func(r *Runtime) { r.libraries["libc.so.6"] = nil },
		"loader": func(r *Runtime) { r.loader.image.soname = "other-loader.so" },
		"addon":  func(r *Runtime) { r.addons["rolldown"] = forged.addons["rolldown"] },
		"closed": func(r *Runtime) { r.closed.Store(true) },
	} {
		t.Run(name, func(t *testing.T) {
			r := descriptors()
			change(r)
			if verifyVitestRuntimeClosure(r) == nil {
				t.Fatal("changed fixed runtime roles accepted")
			}
		})
	}
	if verifyVitestRuntimeClosure(nil) == nil {
		t.Fatal("absent fixed runtime accepted")
	}
}

func nodeRuntimePortableResources(t *testing.T) vitestadapter.RuntimeRequest {
	t.Helper()
	root, modules := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "alpha.test.ts"), []byte("test('alpha',()=>{})"), 0600); err != nil {
		t.Fatal(err)
	}
	for name, version := range map[string]string{"vitest": "4.1.11", "vite": "8.2.2", "rolldown": "1.2.12"} {
		if err := os.Mkdir(filepath.Join(modules, name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(modules, name, "package.json"), []byte(`{"version":"`+version+`"}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return vitestadapter.RuntimeRequest{Root: root, Modules: modules, Workspace: "ts", Sources: []string{"alpha.test.ts"}, Tests: []string{"alpha.test.ts"}}
}

func TestRestrictedNodeRuntimeFailedCreationAbandonsResources(t *testing.T) {
	failed := &PreparedVitestRuntime{request: NodeRuntimeRequest{Files: nodeRuntimePortableResources(t)}}
	injected := errors.New("injected after resources prepared")
	var directory, marker string
	p, err := prepareVitestRuntimeResources(context.Background(), failed, func(created *vitestadapter.PreparedRuntime) error {
		directory = filepath.Dir(created.Files().Roots[0].Path)
		marker = filepath.Join(directory, "unknown-sibling")
		if err := os.WriteFile(marker, []byte("preserve"), 0600); err != nil {
			return err
		}
		return injected
	})
	if p != nil || !errors.Is(err, injected) || !failed.closed || failed.prepared == nil {
		t.Fatal("native failed preparation retained an owner or lost its error", err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "preserve" {
		t.Fatal("native failed preparation removed unknown sibling", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "resources")); !os.IsNotExist(err) {
		t.Fatal("native failed preparation retained known resources", err)
	}
	if failed.prepared.Verify(context.Background()) == nil || failed.Close() != nil || failed.prepared.Abandon() != nil {
		t.Fatal("native failed preparation retained cleanup-retry handle")
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
}

func TestRestrictedNodeRuntimeRejectsVersionOnlyFramework(t *testing.T) {
	failed := &PreparedVitestRuntime{request: NodeRuntimeRequest{Files: nodeRuntimePortableResources(t)}}
	prepared, err := prepareVitestRuntimeResources(context.Background(), failed, nil)
	if prepared != nil || err == nil || !strings.Contains(err.Error(), "complete framework closure") || !failed.closed {
		t.Fatal("three matching package versions acquired managed launch", err)
	}
	if failed.prepared == nil || failed.prepared.Verify(context.Background()) == nil || failed.Close() != nil {
		t.Fatal("rejected version-only owner leaked usable resources")
	}
}

func TestRestrictedNodeRuntimeExecutablePhysicalGuards(t *testing.T) {
	for _, mutation := range []string{"bytes", "inode", "mode", "alias", "ancestor"} {
		t.Run(mutation, func(t *testing.T) {
			root := t.TempDir()
			original := filepath.Join(root, "original")
			other := filepath.Join(root, "other")
			for _, dir := range []string{original, other} {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "node"), []byte(strings.Repeat("n", 128)), 0700); err != nil {
					t.Fatal(err)
				}
			}
			declared := filepath.Join(original, "node")
			if mutation == "alias" {
				declared = filepath.Join(root, "node-alias")
				if err := os.Symlink(filepath.Join(original, "node"), declared); err != nil {
					t.Fatal(err)
				}
			}
			if mutation == "ancestor" {
				parent := filepath.Join(root, "parent-alias")
				if err := os.Symlink(original, parent); err != nil {
					t.Fatal(err)
				}
				declared = filepath.Join(parent, "node")
			}
			guard, err := preflightDynamicFile(declared)
			if err != nil {
				t.Fatal(err)
			}
			guard.digest, err = fileDigestContext(context.Background(), guard.resolved)
			if err != nil || verifyNodeRuntimeExecutable(context.Background(), guard) != nil {
				t.Fatal("fresh actual runtime executable rejected", err)
			}
			switch mutation {
			case "bytes":
				err = os.WriteFile(guard.resolved, []byte(strings.Repeat("x", 128)), 0700)
				if err == nil {
					err = os.Chtimes(guard.resolved, guard.file.ModTime(), guard.file.ModTime())
				}
			case "inode":
				replacement := filepath.Join(other, "node")
				err = os.Chtimes(replacement, guard.file.ModTime(), guard.file.ModTime())
				if err == nil {
					err = os.Rename(replacement, guard.resolved)
				}
			case "mode":
				err = os.Chmod(guard.resolved, 0600)
			case "alias", "ancestor":
				alias, target := declared, filepath.Join(other, "node")
				if mutation == "ancestor" {
					alias, target = filepath.Dir(declared), other
				}
				err = os.Remove(alias)
				if err == nil {
					err = os.Symlink(target, alias)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if verifyNodeRuntimeExecutable(context.Background(), guard) == nil {
				t.Fatal("actual runtime executable drift escaped physical guards")
			}
		})
	}
}

func TestRestrictedNodeRuntimeIncompleteOutputCannotPass(t *testing.T) {
	want, err := selectNodeInventory(nodeRuntimeInventory(), nodeRuntimeInventory().Units[:1])
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"schema":1,"complete":true,"errors":0,"files":[{"file":"alpha.test.ts","project":"","action":"passed"}],"tests":[{"file":"alpha.test.ts","project":"","test":"[\"alpha\",0]","action":"passed"}]}`)
	good := process.Output{Completed: true, Stdout: data}
	if outcomes, err := decodeVitestRuntimeOutcomes(good, want); err != nil || !outcomes.Complete || outcomes.Failed {
		t.Fatal("healthy protocol control failed", err)
	}
	for _, bad := range []process.Output{{Stdout: data}, {Completed: true, Stdout: data[:len(data)/2]}, {Completed: true, Stdout: append(slices.Clone(data), []byte(" {}")...)}, {Completed: true, Stdout: []byte(strings.Replace(string(data), "alpha\\\"", "foreign\\\"", 1))}, {Completed: true, ExitCode: 1, Stdout: data}, {Completed: true, Stdout: []byte(strings.Repeat("x", process.MaxOutput+1))}} {
		if outcomes, err := decodeVitestRuntimeOutcomes(bad, want); err == nil || outcomes.Complete {
			t.Fatal("incomplete, foreign or oversized output became complete")
		}
	}
	if verifyVitestSourceToolchain(&NodeSourceCertificate{}) == nil || verifyNodeRuntimeExecutable(context.Background(), nil) == nil {
		t.Fatal("public source or empty executable acquired managed-profile admission")
	}
}

// Installed tests exercise real framework APIs and original CLI behavior. They
// grant no issuer authority, and require explicit isolated development tools.
type installedVitestFixture struct {
	dynamicBoundaryFixture
	files       vitestadapter.RuntimeRequest
	hostNode    string
	sdk         string
	addon       string
	config      string
	certificate *NodeSourceCertificate
	prepared    *PreparedVitestRuntime
}

func vitestPrerequisite(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		if os.Getenv("HAYAKU_VITEST_REQUIRED") == "1" {
			t.Fatal("required Vitest prerequisite missing:", name)
		}
		t.Skip("explicit installed Vitest prerequisite required: " + name)
	}
	return value
}

func prepareInstalledVitestFixture(t *testing.T, ctx context.Context, files map[string]string) installedVitestFixture {
	t.Helper()
	image := vitestPrerequisite(t, "HAYAKU_NATIVE_IMAGE")
	node := vitestPrerequisite(t, "HAYAKU_DYNAMIC_NODE")
	modules := vitestPrerequisite(t, "HAYAKU_VITEST_MODULES")
	host := vitestPrerequisite(t, "HAYAKU_NODE_CERTIFIER_NODE")
	sdk := vitestPrerequisite(t, "HAYAKU_NODE_CERTIFIER_TYPESCRIPT")
	addon := vitestPrerequisite(t, "HAYAKU_NATIVE_ADDON_ROLLDOWN")
	docker, err := ResolveDocker()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	worker := os.Getenv("HAYAKU_VITEST_WORKER")
	if worker == "" {
		tool, err := exec.LookPath("go")
		if err != nil {
			t.Fatal(err)
		}
		worker, err = prepareWorker(ctx, tool, directory, "arm64")
		if err != nil {
			t.Fatal(err)
		}
	}
	f := installedVitestFixture{dynamicBoundaryFixture: dynamicBoundaryFixture{docker: docker, primary: node, directory: directory, backend: Backend{Docker: docker, Image: image, Worker: worker, Architecture: "arm64"}}, hostNode: host, sdk: sdk, addon: addon, files: vitestadapter.RuntimeRequest{Root: filepath.Join(directory, "source"), Modules: modules, Workspace: "ts"}}
	if err := os.Mkdir(f.files.Root, 0755); err != nil {
		t.Fatal(err)
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(f.files.Root, name), []byte(contents), 0644); err != nil {
			t.Fatal(err)
		}
		f.files.Sources = append(f.files.Sources, name)
		if strings.Contains(name, ".test.") {
			f.files.Tests = append(f.files.Tests, name)
		}
	}
	slices.Sort(f.files.Sources)
	slices.Sort(f.files.Tests)
	// The CLI config is independently authored; the operational adapter never
	// imports it or uses CLI report parsing as its native outcome decoder.
	f.config = `export default { cacheDir:'/scratch/vite-cache', server:{watch:null}, test:{environment:'node',pool:'threads',minWorkers:1,maxWorkers:1,isolate:true,fileParallelism:false,globals:false,watch:false,cache:false,testTimeout:0,hookTimeout:0,retry:0,allowOnly:false,passWithNoTests:false,sequence:{shuffle:false},include:`
	encoded, _ := json.Marshal(f.files.Tests)
	f.config += string(encoded) + `} };`
	for name, contents := range map[string]string{"original.config.mjs": f.config, "package.json": `{"private":true,"type":"module"}`, "transforms.mjs": `import fs from 'node:fs'; import {transformWithOxc} from 'vite'; const result={}; for(const file of process.argv.slice(2)){const value=await transformWithOxc(fs.readFileSync(file,'utf8'),file,{sourcemap:false,target:'es2022'}); if(typeof value.code!=='string')throw Error('transform'); result[file]=value.code;} process.stdout.write(JSON.stringify(result));`} {
		if err := os.WriteFile(filepath.Join(f.files.Root, name), []byte(contents), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(f.files.Root, "node_modules"), 0755); err != nil {
		t.Fatal(err)
	}
	libs := []string{"libdl.so.2", "libstdc++.so.6", "libm.so.6", "libgcc_s.so.1", "libpthread.so.0", "libc.so.6"}
	loader := "ld-linux-aarch64.so.1"
	script := "set -eu\nmkdir /build/libs\ncp -L /lib/aarch64-linux-gnu/" + loader + " /build/libs/" + loader + "\n"
	for _, name := range libs {
		script += "cp -L /lib/aarch64-linux-gnu/" + name + " /build/libs/" + name + "\n"
	}
	out, err := runAdditionalContainer(t, ctx, f.dynamicBoundaryFixture, "vitest-libraries", "--mount", "type=bind,src="+directory+",dst=/build", "--entrypoint=/bin/sh", image, "-c", script)
	if err != nil || !out.Completed || out.ExitCode != 0 {
		t.Fatal("installed Vitest runtime extraction failed", err)
	}
	declaration := RuntimeRequest{Loader: filepath.Join(directory, "libs", loader), Addons: []RuntimeAddon{{Name: "rolldown", Path: addon}}}
	for _, name := range libs {
		declaration.Libraries = append(declaration.Libraries, RuntimeLibrary{Name: name, Path: filepath.Join(directory, "libs", name)})
	}
	f.runtime, err = CaptureRuntime(ctx, declaration)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.runtime.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := f.backend.Bind(ctx); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *installedVitestFixture) prepare(t *testing.T, ctx context.Context) {
	t.Helper()
	nodePin, err := fileDigestContext(ctx, f.hostNode)
	if err != nil || (nodePin != vitestNodeSHA256 && nodePin != vitestCertifierDarwinNodeSHA256) {
		t.Fatal("unaccepted source-certifier Node", err)
	}
	f.certificate, err = CertifyNodeSources(ctx, NodeCertifierRequest{Root: f.files.Root, Node: f.hostNode, NodeSHA256: nodePin, TypeScriptSDK: f.sdk, CompilerSHA256: vitestCompilerSHA256, Sources: f.files.Sources, Tests: f.files.Tests})
	if err != nil {
		t.Fatal("real closed-source candidate rejected", err)
	}
	t.Cleanup(func() {
		if err := f.certificate.Close(); err != nil {
			t.Error(err)
		}
	})
	f.prepared, err = PrepareVitestRuntime(ctx, f.backend, NodeRuntimeRequest{Files: f.files, Node: f.primary, Runtime: f.runtime, Source: f.certificate})
	if err != nil {
		t.Fatal("real fixed Vitest profile rejected", err)
	}
	t.Cleanup(func() {
		if err := f.prepared.Close(); err != nil {
			t.Error(err)
		}
	})
}

func (f installedVitestFixture) original(t *testing.T, ctx context.Context, role string, arguments ...string) (process.Output, error) {
	t.Helper()
	args := []string{"--read-only", "--user=65534:65534", "--tmpfs", "/scratch:rw,noexec,nosuid,nodev,size=64m,mode=0700,uid=65534,gid=65534", "--mount", "type=bind,src=" + f.primary + ",dst=/node,readonly", "--mount", "type=bind,src=" + f.addon + ",dst=/rolldown.node,readonly", "--mount", "type=bind,src=" + f.files.Root + ",dst=/resources,readonly", "--mount", "type=bind,src=" + f.files.Modules + ",dst=/resources/node_modules,readonly", "--workdir=/resources", "--entrypoint=/usr/bin/env", f.backend.Image, "-i", "CI=true", "TMPDIR=/scratch", "NAPI_RS_NATIVE_LIBRARY_PATH=/rolldown.node", "/node"}
	return runAdditionalContainer(t, ctx, f.dynamicBoundaryFixture, role, append(args, arguments...)...)
}

func vitestClosedFixture(fault bool) map[string]string {
	operator := "+"
	if fault {
		operator = "-"
	}
	return map[string]string{"math.ts": "export function add(a: number,b: number): number{return a" + operator + "b;}", "alpha.test.ts": `import {test,expect} from 'vitest';import {add} from './math.js';test('alpha',()=>{expect(add(1,2)).toBe(3)});test('alpha-extra',()=>{expect(add(4,5)).toBeGreaterThan(8)});`, "beta.test.ts": `import {test,expect} from 'vitest';test('beta',()=>{const value: number=true?9:0;expect(value).toBeGreaterThan(8)});`}
}

func originalVitestCases(t *testing.T, out process.Output, fault bool) map[model.Case]string {
	t.Helper()
	exit := 0
	if fault {
		exit = 1
	}
	if !out.Completed || out.ExitCode != exit {
		t.Fatal("original CLI terminal state differs", out.ExitCode)
	}
	var report struct {
		NumTotalTests int
		TestResults   []struct {
			Name             string
			AssertionResults []struct{ FullName, Status string }
		}
	}
	if json.Unmarshal(out.Stdout, &report) != nil || report.NumTotalTests != 3 || len(report.TestResults) != 2 {
		t.Fatal("original genuine CLI case inventory differs")
	}
	result := map[model.Case]string{}
	for _, file := range report.TestResults {
		for _, test := range file.AssertionResults {
			encoded, _ := json.Marshal([]any{test.FullName, 0})
			key := model.Case{Unit: "ts::" + filepath.Base(file.Name), ID: string(encoded)}
			if _, exists := result[key]; exists {
				t.Fatal("original CLI duplicate case")
			}
			result[key] = map[string]string{"passed": "pass", "failed": "fail"}[test.Status]
		}
	}
	return result
}

func TestInstalledLinuxVitestManagedOriginalAndSelected(t *testing.T) {
	for _, fault := range []bool{false, true} {
		t.Run(map[bool]string{false: "healthy", true: "assertion-fault"}[fault], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()
			f := prepareInstalledVitestFixture(t, ctx, vitestClosedFixture(fault))
			original, originalErr := f.original(t, ctx, "vitest-original", "/resources/node_modules/vitest/vitest.mjs", "run", "--config", "original.config.mjs", "--configLoader", "native", "--reporter=json")
			if (originalErr != nil) != fault {
				t.Fatal("original CLI seeded assertion control differs", originalErr)
			}
			cases := originalVitestCases(t, original, fault)
			f.prepare(t, ctx)
			inventory, err := f.prepared.Collect(ctx)
			if err != nil || len(inventory.Cases) != 3 || len(inventory.Units) != 2 {
				t.Fatal("real native collection differs", err)
			}
			out, outcomes, err := f.prepared.Run(ctx, inventory, nil)
			if !out.Completed || !outcomes.Complete || outcomes.Failed != fault || (err != nil) != fault || len(outcomes.Tests) != len(cases) {
				t.Fatal("real native terminal failure semantics differ", err)
			}
			for _, test := range outcomes.Tests {
				if cases[model.Case{Unit: test.Unit, ID: test.Test}] != test.Action {
					t.Fatal("original and modified native case outcomes differ")
				}
			}
			for _, unit := range inventory.Units {
				selected, terminal, err := f.prepared.Run(ctx, inventory, []model.Unit{unit})
				failed := fault && unit.Selector == "alpha.test.ts"
				want, _ := selectNodeInventory(inventory, []model.Unit{unit})
				if !selected.Completed || !terminal.Complete || terminal.Failed != failed || (err != nil) != failed || len(terminal.Tests) != len(want.Cases) || len(terminal.Units) != 1 {
					t.Fatal("genuine selected per-file case inventory differs", err)
				}
				for _, test := range terminal.Tests {
					if test.Unit != unit.ID || cases[model.Case{Unit: test.Unit, ID: test.Test}] != test.Action {
						t.Fatal("foreign or changed selected native case")
					}
				}
			}
			if f.prepared.Verify(ctx) != nil {
				t.Fatal("real framework execution changed bound closure")
			}
			assertNoManagedContainers(t, f.docker)
		})
	}
}

func TestInstalledLinuxVitestManagedActualRolldownEmission(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	typed := prepareInstalledVitestFixture(t, ctx, vitestClosedFixture(false))
	typed.prepare(t, ctx)
	arguments := append([]string{"/resources/transforms.mjs"}, typed.files.Sources...)
	out, err := typed.original(t, ctx, "vitest-actual-transform", arguments...)
	var emitted map[string]string
	if err != nil || !out.Completed || out.ExitCode != 0 || json.Unmarshal(out.Stdout, &emitted) != nil || len(emitted) != len(typed.files.Sources) || strings.Contains(emitted["math.ts"], ": number") {
		t.Fatal("genuine pinned Vite/Rolldown transform failed", err)
	}
	transformed := prepareInstalledVitestFixture(t, ctx, emitted)
	transformed.prepare(t, ctx)
	if typed.certificate.Summary().SemanticDigest != transformed.certificate.Summary().SemanticDigest {
		t.Fatal("actual Rolldown emitted closed grammar changed source semantics")
	}
	for _, fixture := range []installedVitestFixture{typed, transformed} {
		original, err := fixture.original(t, ctx, "vitest-emission-original", "/resources/node_modules/vitest/vitest.mjs", "run", "--config", "original.config.mjs", "--configLoader", "native", "--reporter=json")
		if err != nil {
			t.Fatal("original emitted-code behavior failed", err)
		}
		cases := originalVitestCases(t, original, false)
		inventory, err := fixture.prepared.Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		out, outcomes, err := fixture.prepared.Run(ctx, inventory, nil)
		if err != nil || !out.Completed || !outcomes.Complete || outcomes.Failed || len(outcomes.Tests) != len(cases) {
			t.Fatal("actual Rolldown original/native behavior differs", err)
		}
		for _, test := range outcomes.Tests {
			if cases[model.Case{Unit: test.Unit, ID: test.Test}] != test.Action {
				t.Fatal("actual emitted native case changed")
			}
		}
	}
	assertNoManagedContainers(t, typed.docker)
}

func TestInstalledLinuxVitestManagedGuardsAndDormantEffects(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	f := prepareInstalledVitestFixture(t, ctx, vitestClosedFixture(false))
	f.prepare(t, ctx)
	inventory, err := f.prepared.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out, good, err := f.prepared.Run(ctx, inventory, nil)
	if err != nil || !out.Completed || !good.Complete || good.Failed {
		t.Fatal("actual guard healthy control failed", err)
	}
	for _, bad := range []process.Output{{Stdout: out.Stdout}, {Completed: true, Stdout: out.Stdout[:len(out.Stdout)/2]}, {Completed: true, Stdout: append(slices.Clone(out.Stdout), []byte(" {}")...)}, {Completed: true, Stdout: []byte(strings.Replace(string(out.Stdout), "alpha\\\"", "foreign\\\"", 1))}} {
		if outcomes, err := decodeVitestRuntimeOutcomes(bad, inventory); err == nil || outcomes.Complete {
			t.Fatal("corrupted genuine framework report became complete")
		}
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if result, outcomes, err := f.prepared.Run(canceled, inventory, nil); err == nil || result.Completed || outcomes.Complete || !errors.Is(err, context.Canceled) {
		t.Fatal("canceled actual producer remained complete", err)
	}
	if err := os.WriteFile(filepath.Join(f.files.Root, "math.ts"), []byte(`export function add(a:number,b:number):number{if(false){return process.env.VALUE}return a+b;}`), 0644); err != nil {
		t.Fatal(err)
	}
	if result, outcomes, err := f.prepared.Run(ctx, inventory, nil); err == nil || result.Completed || outcomes.Complete || len(result.Stdout) != 0 {
		t.Fatal("source drift reached managed execution", err)
	}
	request := f.certificate.request
	if candidate, err := CertifyNodeSources(ctx, request); err == nil || candidate != nil {
		t.Fatal("dormant imported helper effect gained source admission")
	}
	// Full original remains a real healthy command for the never-taken effect.
	original, err := f.original(t, ctx, "vitest-dormant-original", "/resources/node_modules/vitest/vitest.mjs", "run", "--config", "original.config.mjs", "--configLoader", "native", "--reporter=json")
	if err != nil || len(originalVitestCases(t, original, false)) != 3 {
		t.Fatal("dormant-effect original full scope unavailable", err)
	}
	directory := filepath.Dir(f.prepared.prepared.Files().Roots[0].Path)
	marker := filepath.Join(directory, "unknown-owner-file")
	if err := os.WriteFile(marker, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if f.prepared.Close() == nil || f.prepared.Verify(ctx) == nil {
		t.Fatal("actual prepared owner cleanup fault retained usable context")
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "preserve" {
		t.Fatal("actual owner cleanup removed unrelated sibling", err)
	}
	if err := os.Remove(marker); err != nil || f.prepared.Close() != nil {
		t.Fatal("actual owned cleanup retry failed", err)
	}
	assertNoManagedContainers(t, f.docker)
}
