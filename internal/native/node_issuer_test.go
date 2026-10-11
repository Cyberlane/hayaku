package native

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	vitestadapter "github.com/Cyberlane/hayaku/internal/adapter/vitest"
	"github.com/Cyberlane/hayaku/internal/inputbundle"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
)

// No SDK executes here. The source and graph fixtures below test descriptive
// functional keys, not parser semantics, runtime admission or issuer authority.
func nodeIssuerSourceFixture(t *testing.T) (NodeSourceSummary, inputbundle.Manifest, *vitestWorkspaceProof, []model.Unit) {
	t.Helper()
	root, w, c := vitestWorkspaceFixture(t)
	proof, err := captureVitestWorkspaceProof(context.Background(), root, w, c, "/installed/docker")
	if err != nil {
		t.Fatal(err)
	}
	inputs := make([]inputbundle.Input, len(proof.scope.sources))
	for i, name := range proof.scope.sources {
		inputs[i] = inputbundle.Input{Root: "source", Path: name}
	}
	bundle, err := inputbundle.Capture(context.Background(), inputbundle.Request{Roots: []inputbundle.Root{{Name: "source", Path: root, Mount: "."}}, Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	summary := NodeSourceSummary{Policy: nodeSourcePolicy, ToolTrust: "unqualified", Dependencies: map[string][]string{"alpha.test.ts": {"shared.ts"}, "beta.test.js": {}, "shared.ts": {}}, Cases: []NodeSourceCase{{File: "alpha.test.ts", Label: "alpha"}, {File: "beta.test.js", Label: "beta"}}}
	units := []model.Unit{{ID: "ts::alpha.test.ts", Workspace: "ts", Kind: "vitest-file", Selector: "alpha.test.ts"}, {ID: "ts::beta.test.js", Workspace: "ts", Kind: "vitest-file", Selector: "beta.test.js"}}
	return summary, bundle.Manifest(), proof, units
}

func TestNodeIssuerReachableKeysExcludeOtherPureSources(t *testing.T) {
	summary, manifest, proof, units := nodeIssuerSourceFixture(t)
	keys := make([]string, len(units))
	for i, unit := range units {
		var err error
		keys[i], err = vitestFileProof(summary, manifest, proof.scope, unit)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Re-admitting a changed shared module invalidates only its importing file.
	for i := range manifest.Entries {
		if manifest.Entries[i].Path == "shared.ts" {
			manifest.Entries[i].SHA256 = identity("changed pure module")
			for j := range proof.scope.files {
				if proof.scope.files[j].name == "shared.ts" {
					proof.scope.files[j].digest = manifest.Entries[i].SHA256
				}
			}
		}
	}
	for i, unit := range units {
		key, err := vitestFileProof(summary, manifest, proof.scope, unit)
		if err != nil || (key != keys[i]) != (i == 0) {
			t.Fatal("reachable changed-byte key did not isolate importing file", i, err)
		}
		keys[i] = key
	}
	// Whole-summary semantic/source/emitted digests and another file's labels
	// describe complete admission, but do not enter an independent file proof.
	summary.SourceDigest, summary.SemanticDigest, summary.EmittedDigest = identity("whole source"), identity("whole semantics"), identity("whole emitted")
	summary.Cases[1].Label = "different beta label"
	for i, unit := range units {
		key, err := vitestFileProof(summary, manifest, proof.scope, unit)
		if err != nil || key != keys[i] {
			t.Fatal("global summary entered reachable file proof", err)
		}
	}
	summary.Dependencies["beta.test.js"] = []string{"shared.ts"}
	key, err := vitestFileProof(summary, manifest, proof.scope, units[1])
	if err != nil || key == keys[1] {
		t.Fatal("new reachable edge absent from key", err)
	}
	if proof.verify(context.Background()) == nil {
		t.Fatal("altered descriptive proof supplied live workspace authority")
	}
}

func TestNodeIssuerArtifactKeysExcludeUnrelatedInventoryAndPhysicalEnrollment(t *testing.T) {
	ctx := context.Background()
	b, artifact := nodeIssuerAuthorityFixture(t)
	unit := nodeRuntimeInventory().Units[0]
	var baseline string
	for _, change := range []string{"base", "add", "delete", "relocate"} {
		t.Run(change, func(t *testing.T) {
			root, workspace, configuration := vitestWorkspaceFixture(t)
			// Real enrollment contains physical source/SDK paths. These remain
			// complete-scope guards rather than independent functional inputs.
			if err := os.WriteFile(filepath.Join(root, "native.json"), []byte(`{"source":"`+root+`","modules":"`+t.TempDir()+`/node_modules"}`), 0644); err != nil {
				t.Fatal(err)
			}
			dependencies := map[string][]string{"alpha.test.ts": {"shared.ts"}, "beta.test.js": {}, "shared.ts": {}}
			switch change {
			case "add":
				if err := os.WriteFile(filepath.Join(root, "gamma.test.ts"), []byte(`import {test,expect} from 'vitest';test('gamma',()=>{expect(3).toBe(3)})`), 0644); err != nil {
					t.Fatal(err)
				}
				dependencies["gamma.test.ts"] = []string{}
			case "delete":
				if err := os.Remove(filepath.Join(root, "beta.test.js")); err != nil {
					t.Fatal(err)
				}
				delete(dependencies, "beta.test.js")
			}
			proof, err := captureVitestWorkspaceProof(ctx, root, workspace, configuration, "/installed/docker")
			if err != nil {
				t.Fatal(err)
			}
			request := nodeRuntimePortableResources(t)
			request.Root, request.Sources, request.Tests = root, proof.scope.sources, proof.scope.tests
			prepared, err := vitestadapter.PrepareRuntime(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := prepared.Close(); err != nil {
					t.Error(err)
				}
			}()
			actual, env, err := prepared.Invocation(ctx, "run", []model.Unit{unit}, "/runtime/#addon-rolldown.node")
			if err != nil {
				t.Fatal(err)
			}
			arguments, err := vitestFileArguments(actual, unit)
			if err != nil || len(actual) != 3 || (change != "delete" && actual[2] == arguments[2]) {
				t.Fatal("actual complete invocation was not retained separately", err)
			}
			inputs := make([]inputbundle.Input, len(proof.scope.sources))
			for i, name := range proof.scope.sources {
				inputs[i] = inputbundle.Input{Root: "source", Path: name}
			}
			captured, err := inputbundle.Capture(ctx, inputbundle.Request{Roots: []inputbundle.Root{{Name: "source", Path: root, Mount: "."}}, Inputs: inputs})
			if err != nil {
				t.Fatal(err)
			}
			fileProof, err := vitestFileProof(NodeSourceSummary{Dependencies: dependencies}, captured.Manifest(), proof.scope, unit)
			if err != nil {
				t.Fatal(err)
			}
			artifact.Proof, artifact.Args, artifact.Env = fileProof, arguments, env
			artifact.authority.launch.context = identity([]any{proof.profile, fileProof, artifact.inventory()})
			key, err := artifactBinding(b, artifact, identity("fixed program"), artifact.authority.producer)
			if err != nil {
				t.Fatal(err)
			}
			if baseline == "" {
				baseline = key
			} else if key != baseline {
				t.Fatal("unrelated test inventory or physical enrollment entered file artifact key")
			}
			if err := os.WriteFile(filepath.Join(root, "native.json"), []byte("changed complete configuration"), 0644); err != nil {
				t.Fatal(err)
			}
			if proof.verify(ctx) == nil {
				t.Fatal("key-stable configuration escaped live complete-scope verification")
			}
		})
	}
}

func TestNodeIssuerRejectsIncompleteReachabilityAndSourceOwnership(t *testing.T) {
	for _, fault := range []string{"missing-graph", "foreign-target", "cycle", "test-import", "foreign-unit", "entry-missing", "entry-digest", "entry-mode", "entry-link", "entry-duplicate"} {
		t.Run(fault, func(t *testing.T) {
			summary, manifest, proof, units := nodeIssuerSourceFixture(t)
			unit := units[0]
			index := slices.IndexFunc(manifest.Entries, func(e inputbundle.Entry) bool { return e.Path == "shared.ts" })
			switch fault {
			case "missing-graph":
				delete(summary.Dependencies, "shared.ts")
			case "foreign-target":
				summary.Dependencies[unit.Selector] = []string{"absent.ts"}
			case "cycle":
				summary.Dependencies["shared.ts"] = []string{"shared.ts"}
			case "test-import":
				summary.Dependencies["shared.ts"] = []string{"beta.test.js"}
			case "foreign-unit":
				unit.Selector = "absent.test.ts"
			case "entry-missing":
				manifest.Entries = append(manifest.Entries[:index:index], manifest.Entries[index+1:]...)
			case "entry-digest":
				manifest.Entries[index].SHA256 = identity("replacement")
			case "entry-mode":
				manifest.Entries[index].Mode = 0777
			case "entry-link":
				manifest.Entries[index].Link = "other.ts"
			case "entry-duplicate":
				manifest.Entries = append(manifest.Entries, manifest.Entries[index])
			}
			if _, err := vitestFileProof(summary, manifest, proof.scope, unit); err == nil {
				t.Fatal("incomplete reachable or mismatched captured source admitted")
			}
		})
	}
}

func TestNodeIssuerPublicOperationalDataCannotCreateArtifacts(t *testing.T) {
	for _, fault := range []string{"nil-runtime", "zero-runtime", "public-proof", "changed-profile", "changed-scope", "new-file", "forged-inventory"} {
		t.Run(fault, func(t *testing.T) {
			_, _, proof, _ := nodeIssuerSourceFixture(t)
			p := &PreparedVitestRuntime{}
			inventory := nodeRuntimeInventory()
			switch fault {
			case "nil-runtime":
				p = nil
			case "public-proof":
				proof = &vitestWorkspaceProof{scope: proof.scope, profile: proof.profile}
			case "changed-profile":
				proof.profile = identity("caller runtime profile")
			case "changed-scope":
				proof.scope.sources[0] = "different.ts"
			case "new-file":
				if err := os.WriteFile(filepath.Join(proof.scope.root, "new.test.ts"), []byte("test('new',()=>{})"), 0644); err != nil {
					t.Fatal(err)
				}
			case "forged-inventory":
				inventory.Cases[0].ID = "caller case"
			}
			if artifacts, err := issueVitestArtifacts(context.Background(), p, proof, inventory); err == nil || len(artifacts) != 0 {
				t.Fatal("public/empty operational data obtained private authority")
			}
			if fault == "changed-profile" || fault == "changed-scope" || fault == "new-file" || fault == "public-proof" {
				if proof.verify(context.Background()) == nil {
					t.Fatal("configured complete proof mutation retained admission")
				}
			}
		})
	}
}

func TestNodeIssuerForgedInventoryRejectedBeforeToolAdmission(t *testing.T) {
	_, _, proof, units := nodeIssuerSourceFixture(t)
	files := nodeRuntimePortableResources(t)
	files.Root, files.Sources, files.Tests = proof.scope.root, proof.scope.sources, proof.scope.tests
	prepared, err := vitestadapter.PrepareRuntime(context.Background(), files)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := prepared.Close(); err != nil {
			t.Error(err)
		}
	})
	inventory := model.ExecutionInventory{Schema: model.ExecutionSchema, Units: units, Cases: []model.Case{{Unit: units[0].ID, ID: `["alpha",0]`}, {Unit: units[1].ID, ID: `["beta",0]`}}}
	p := &PreparedVitestRuntime{prepared: prepared, expected: cloneInventory(inventory), request: NodeRuntimeRequest{Files: files, Source: &NodeSourceCertificate{}}}
	forged := cloneInventory(inventory)
	forged.Cases[0].ID = `["caller",0]`
	wanted := "cases differ"
	if err := verifyVitestController(runtime.GOOS, runtime.GOARCH); err != nil {
		wanted = err.Error()
	}
	if artifacts, err := issueVitestArtifacts(context.Background(), p, proof, forged); err == nil || len(artifacts) != 0 || !strings.Contains(err.Error(), wanted) {
		t.Fatal("forged native inventory reached fixed-tool admission", err)
	}
	if artifacts, err := issueVitestArtifacts(context.Background(), p, proof, inventory); err == nil || len(artifacts) != 0 {
		t.Fatal("matching descriptive inventory admitted fake framework")
	}
}

// Common private authority fixtures verify the engine contract only. They are
// never returned by the real issuer and cannot qualify a language/runtime.
func nodeIssuerAuthorityFixture(t *testing.T) (Backend, Artifact) {
	t.Helper()
	b, a, _, _ := providerLaunchFixture(t)
	want, err := selectNodeInventory(nodeRuntimeInventory(), nodeRuntimeInventory().Units[:1])
	if err != nil {
		t.Fatal(err)
	}
	a.Name, a.Cases = want.Units[0].ID, []string{want.Cases[0].ID}
	a.authority.inventory = &want
	a.authority.producer = identity([]string{vitestIssuerVersion, "descriptive fixed tool context"})
	a.authority.location = authorityLocation(b, a)
	a.authority.decode = func(out process.Output, _ Artifact) (model.Outcomes, error) {
		return decodeVitestRuntimeOutcomes(out, want)
	}
	program, err := fileDigest(a.Program)
	if err != nil {
		t.Fatal(err)
	}
	a.authority.binding, err = artifactBinding(b, a, program, a.authority.producer)
	if err != nil {
		t.Fatal(err)
	}
	return b, a
}

func TestNodeIssuerSharedAuthorityRejectsLaunchAndInventoryMutation(t *testing.T) {
	for _, fault := range []string{"public-copy", "private-program", "args", "env", "envelope", "context", "proof", "inventory", "cases"} {
		t.Run(fault, func(t *testing.T) {
			b, a := nodeIssuerAuthorityFixture(t)
			if _, _, err := verifyAuthority(context.Background(), b, a); err != nil {
				t.Fatal("healthy engine fixture", err)
			}
			switch fault {
			case "public-copy":
				a.authority = nil
			case "private-program":
				a.Program = filepath.Join(t.TempDir(), "foreign-node")
			case "args":
				a.Args = append(a.Args, "--inspect")
			case "env":
				a.Env = append(a.Env, "NODE_OPTIONS=--inspect")
			case "envelope":
				copy := *a.Envelope
				a.Envelope = &copy
			case "context":
				a.authority.launch.context = identity("caller functional context")
			case "proof":
				a.Proof = identity("different source closure")
			case "inventory":
				a.authority.inventory.Cases[0].ID = "caller case"
			case "cases":
				a.Cases[0] = "caller case"
			}
			dir, before := authorityCacheFixture(t, true)
			assertAuthorityRejectedWithoutMutation(t, b, a, dir, before, false, nil)
		})
	}
}

func TestNodeIssuerOwnedDecoderRequiresExactNativeOutcomes(t *testing.T) {
	_, a := nodeIssuerAuthorityFixture(t)
	data := `{"schema":1,"complete":true,"errors":0,"files":[{"file":"alpha.test.ts","project":"","action":"passed"}],"tests":[{"file":"alpha.test.ts","project":"","test":"[\"alpha\",0]","action":"passed"}]}`
	if outcomes, err := a.authority.decode(process.Output{Completed: true, Stdout: []byte(data)}, a); err != nil || !outcomes.Complete {
		t.Fatal("native exact decoder control", err)
	}
	for _, out := range []process.Output{
		{Completed: true, ExitCode: 1, Stdout: []byte(data)},
		{Completed: true, Stdout: []byte(strings.Replace(data, "alpha.test.ts", "foreign.test.ts", 1))},
		{Completed: true, Stdout: []byte(strings.Replace(data, "alpha\\\"", "caller\\\"", 1))},
		{Completed: true, Stdout: []byte(data + "{}")},
		{Stdout: []byte(data)},
	} {
		if outcomes, err := a.authority.decode(out, a); err == nil || outcomes.Complete {
			t.Fatal("contradictory native evidence became complete")
		}
	}
}

func TestNodeIssuerFixedToolsIgnoreGlobalUserSummary(t *testing.T) {
	ctx := context.Background()
	files := nodeRuntimePortableResources(t)
	prepared, err := vitestadapter.PrepareRuntime(ctx, files)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := prepared.Close(); err != nil {
			t.Error(err)
		}
	})
	envelope, err := CaptureEnvelope(ctx, EnvelopeRequest{Files: prepared.Files()})
	if err != nil {
		t.Fatal(err)
	}
	sdk := t.TempDir()
	if err := os.WriteFile(filepath.Join(sdk, "compiler.js"), []byte("descriptive SDK fixture"), 0644); err != nil {
		t.Fatal(err)
	}
	compiler, err := inputbundle.Capture(ctx, inputbundle.Request{Roots: []inputbundle.Root{{Name: "sdk", Path: sdk, Mount: "."}}, Inputs: []inputbundle.Input{{Root: "sdk", Path: "."}}})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(sdk, "compiler.js"))
	if err != nil {
		t.Fatal(err)
	}
	source := &NodeSourceCertificate{sdk: compiler, summary: NodeSourceSummary{Policy: nodeSourcePolicy, ParserVersion: "6.0.2", NodeVersion: "22.18.0", EmitterDigest: identity("compiler options"), ToolTrust: "unqualified"}}
	p := &PreparedVitestRuntime{prepared: prepared, node: &dynamicFile{digest: vitestNodeSHA256, file: info}, request: NodeRuntimeRequest{Source: source, Runtime: &Runtime{loader: &dynamicFile{digest: "loader", image: dynamicELF{soname: "loader.so"}}}}}
	key, err := vitestToolContext(p, envelope)
	if err != nil {
		t.Fatal(err)
	}
	source.summary.SourceDigest, source.summary.SemanticDigest, source.summary.EmittedDigest = identity("changed source"), identity("changed semantics"), identity("changed output")
	source.summary.Cases = []NodeSourceCase{{File: "unrelated.test.ts", Label: "changed label"}}
	p.digest = identity("changed complete prepared resources")
	if next, err := vitestToolContext(p, envelope); err != nil || next != key {
		t.Fatal("global user summary entered fixed tool context", err)
	}
	source.summary.EmitterDigest = identity("changed compiler options")
	if next, err := vitestToolContext(p, envelope); err != nil || next == key {
		t.Fatal("compiler option identity absent from fixed tool context", err)
	}
	if artifacts, err := issueVitestArtifacts(ctx, p, nil, nodeRuntimeInventory()); err == nil || len(artifacts) != 0 {
		t.Fatal("descriptive tool fixtures obtained actual issuer authority")
	}
}
