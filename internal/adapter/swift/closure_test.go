package swift

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/inputbundle"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
)

func appleFixture(t *testing.T) (*AppleClosure, string, string) {
	t.Helper()
	root := t.TempDir()
	write(t, root, "source.swift", "let number = 7\n")
	tool := filepath.Join(t.TempDir(), "producer")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nprintf 'healthy\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	w := model.Workspace{ID: "apple", Root: ".", Command: model.Command{Dir: ".", Executable: tool, Args: []string{"test"}}}
	c, err := CaptureAppleClosure(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	captured, err := c.CaptureTool(context.Background(), tool)
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.Run(context.Background(), captured, nil)
	if err != nil || !out.Completed || string(out.Stdout) != "healthy\n" {
		t.Fatal("healthy producer", out, err)
	}
	return c, root, tool
}
func TestAppleClosureRejectsProducerSourceAndPrivateContextDrift(t *testing.T) {
	for _, fault := range []string{"source", "resource", "tool-bytes", "alias", "args", "env"} {
		t.Run(fault, func(t *testing.T) {
			c, root, tool := appleFixture(t)
			marker := filepath.Join(t.TempDir(), "marker")
			switch fault {
			case "source":
				write(t, root, "source.swift", "let number = 8\n")
			case "resource":
				write(t, root, "fixtures/data.txt", "external-resource\n")
			case "tool-bytes":
				if err := os.WriteFile(tool, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); err != nil {
					t.Fatal(err)
				}
			case "alias":
				target := tool + "-new"
				if err := os.WriteFile(target, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(tool); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, tool); err != nil {
					t.Fatal(err)
				}
			case "args":
				c.workspace.Command.Args[0] = "build"
			case "env":
				c.env = append(c.env, "APPLE_FAULT=1")
			}
			if out, err := c.Run(context.Background(), tool, nil); err == nil || out.Completed {
				t.Fatal("changed collector launched", out, err)
			}
			if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("untrusted changed producer ran", err)
			}
		})
	}
}
func TestAppleClosureFreezesOriginalCallerAndAmbientEnvironment(t *testing.T) {
	root := t.TempDir()
	write(t, root, "Package.swift", "// healthy\n")
	tool := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nprintf '%s' \"$APPLE_CAPTURE_FIXTURE\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APPLE_CAPTURE_FIXTURE", "before")
	w := model.Workspace{ID: "s", Root: ".", Command: model.Command{Dir: ".", Executable: tool, Args: []string{"test"}}}
	env := map[string]string{"APPLE_CAPTURE_FIXTURE": "frozen"}
	c, err := CaptureAppleClosure(context.Background(), root, w, model.Context{Env: env})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CaptureTool(context.Background(), tool); err != nil {
		t.Fatal(err)
	}
	w.Command.Args[0] = "changed"
	env["APPLE_CAPTURE_FIXTURE"] = "changed"
	t.Setenv("APPLE_CAPTURE_FIXTURE", "ambient changed")
	out, err := c.Run(context.Background(), tool, nil)
	if err != nil || string(out.Stdout) != "frozen" {
		t.Fatal("caller/ambient drift changed captured launch", out, err)
	}
}
func TestAppleDocumentCaptureBoundsAndRealInputs(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "state.json")
	if err := os.WriteFile(name, []byte(`{"healthy":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := ReadAppleFile(context.Background(), name, 1024)
	if err != nil || string(data) != `{"healthy":true}` {
		t.Fatal("healthy bounded document", err)
	}
	for _, fault := range []string{"growth", "directory", "symlink", "cancel"} {
		t.Run(fault, func(t *testing.T) {
			tmp := t.TempDir()
			path := filepath.Join(tmp, "state")
			ctx := context.Background()
			switch fault {
			case "growth":
				if err := os.WriteFile(path, []byte(strings.Repeat("x", 1025)), 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(name, path); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				if err := os.WriteFile(path, []byte("healthy"), 0600); err != nil {
					t.Fatal(err)
				}
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			if _, err := ReadAppleFile(ctx, path, 1024); err == nil {
				t.Fatal("unsafe document accepted")
			}
		})
	}
}
func TestAppleGeneratedTreeDriftRejectsLaunch(t *testing.T) {
	c, _, tool := appleFixture(t)
	tree := t.TempDir()
	write(t, tree, "artifact", "compiled-bytes")
	if err := c.CaptureTree(context.Background(), tree); err != nil {
		t.Fatal(err)
	}
	if err := c.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	write(t, tree, "artifact", "changed-generated-bytes")
	if _, err := c.Run(context.Background(), tool, nil); err == nil {
		t.Fatal("changed producer artifact launched")
	}
}

func TestAppleRequestPreflightAndRecaptureDoNotBlessDrift(t *testing.T) {
	for _, field := range []string{"patterns", "flags", "inputs", "prerequisite", "runtime", "context"} {
		t.Run(field, func(t *testing.T) {
			w := model.Workspace{Root: ".", Command: model.Command{Dir: ".", Executable: "/usr/bin/true"}}
			c := model.Context{}
			huge := strings.Repeat("x", (1<<20)+1)
			switch field {
			case "patterns":
				w.Patterns = []string{huge}
			case "flags":
				w.BuildFlags = []string{huge}
			case "inputs":
				w.Inputs = []string{huge}
			case "prerequisite":
				w.Prerequisites = []model.Command{{Executable: huge}}
			case "runtime":
				w.SwiftRuntime = &model.SwiftRuntime{Dependencies: huge}
			case "context":
				c.ID = huge
			}
			if _, err := CaptureAppleClosure(context.Background(), "/unavailable-input", w, c); err == nil || !strings.Contains(err.Error(), "bounds") {
				t.Fatal("request copied/probed before preflight", err)
			}
		})
	}
	c, _, tool := appleFixture(t)
	tree := t.TempDir()
	write(t, tree, "artifact", "healthy")
	if err := os.Symlink("artifact", filepath.Join(tree, "resource-alias")); err != nil {
		t.Fatal(err)
	}
	if err := c.CaptureTree(context.Background(), tree); err != nil {
		t.Fatal("healthy relative generated link", err)
	}
	if out, err := c.Run(context.Background(), tool, nil); err != nil || !out.Completed {
		t.Fatal("healthy generated tree", err)
	}
	write(t, tree, "artifact", "changed")
	if err := c.CaptureTree(context.Background(), tree); err == nil {
		t.Fatal("recapture blessed changed generated input")
	}
}

func TestInstalledSwiftResourceClosureAndUnknownScopes(t *testing.T) {
	tool := os.Getenv("HAYAKU_SWIFT_BINARY")
	if tool == "" {
		t.Skip("set HAYAKU_SWIFT_BINARY to installed supported Apple Swift")
	}
	if !filepath.IsAbs(tool) {
		t.Fatal("absolute installed producer required")
	}
	root := t.TempDir()
	write(t, root, "Package.swift", "// swift-tools-version:6.0\nimport PackageDescription\nlet package = Package(name: \"Resource\", targets: [.testTarget(name: \"ResourceTests\", resources: [.copy(\"fixture.txt\")])])\n")
	write(t, root, "Tests/ResourceTests/fixture.txt", "healthy resource\n")
	write(t, root, "Tests/ResourceTests/ResourceTests.swift", "import XCTest\nimport Foundation\nfinal class ResourceTests: XCTestCase { func testResource() throws { let path = Bundle.module.url(forResource: \"fixture\", withExtension: \"txt\")!; XCTAssertEqual(try String(contentsOf: path, encoding: .utf8), \"healthy resource\\n\") } }\n")
	w := model.Workspace{ID: "resources", Root: ".", Adapter: "swift", Command: model.Command{Dir: ".", Executable: tool, Args: []string{"test", "--disable-swift-testing", "--jobs", "1"}}}
	// An independent original command establishes the native healthy oracle.
	baselineCtx, baselineCancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer baselineCancel()
	baseline, err := process.Run(baselineCtx, model.Command{Executable: tool, Args: []string{"test", "--disable-swift-testing", "--jobs", "1", "--package-path", root, "--scratch-path", filepath.Join(t.TempDir(), "build"), "--disable-automatic-resolution", "--skip-update"}}, root, nil)
	if err != nil || !baseline.Completed || baseline.ExitCode != 0 || !strings.Contains(string(baseline.Stdout), "testResource") {
		t.Fatal("original installed resource suite failed", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	evidence, err := Describe(ctx, root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence.Units) != 1 || len(evidence.Inputs["Tests/ResourceTests/fixture.txt"]) != 1 || len(evidence.Effects) != 1 || evidence.Effects[0].Kind != "unknown" || evidence.Effects[0].Owner != evidence.Units[0].ID {
		t.Fatal("resource ownership or unchanged-scope fallback missing", evidence)
	}
	out, result, err := Execute(ctx, root, w, model.Context{}, evidence.Units, false)
	if err != nil || !out.Completed || !result.Complete || len(result.Tests) != 1 {
		t.Fatal("healthy resource inventory/reconciliation", result, err)
	}
	write(t, root, "Tests/ResourceTests/fixture.txt", "seeded resource fault\n")
	out, result, err = Execute(ctx, root, w, model.Context{}, evidence.Units, false)
	if err == nil || !out.Completed || !result.Complete || !result.Failed {
		t.Fatal("seeded original resource assertion disappeared", result, err)
	}
}

func TestInstalledSwiftObservedInventoryRevalidatesProvisionedDependencies(t *testing.T) {
	tool := os.Getenv("HAYAKU_SWIFT_BINARY")
	if tool == "" {
		t.Skip("set HAYAKU_SWIFT_BINARY to installed supported Apple Swift")
	}
	if !filepath.IsAbs(tool) {
		t.Fatal("absolute installed producer required")
	}
	root := t.TempDir()
	write(t, root, "Package.swift", "// swift-tools-version:6.0\nimport PackageDescription\nlet package = Package(name: \"Observed\", targets: [.testTarget(name: \"ObservedTests\")])\n")
	write(t, root, "Tests/ObservedTests/ObservedTests.swift", "import XCTest\nfinal class ObservedTests: XCTestCase { func testHealthy() { XCTAssertEqual(7,7) } }\n")
	dependencies := t.TempDir()
	write(t, dependencies, "workspace-state.json", `{"version":7,"object":{"artifacts":[],"dependencies":[]}}`)
	if err := os.Mkdir(filepath.Join(dependencies, "repositories"), 0700); err != nil {
		t.Fatal(err)
	}
	write(t, dependencies, "checkouts/retained/data.txt", "declared dependency bytes\n")
	w := model.Workspace{ID: "observed", Root: ".", Adapter: "swift", SwiftRuntime: &model.SwiftRuntime{Dependencies: dependencies}, Command: model.Command{Dir: ".", Executable: tool, Args: []string{"test", "--disable-swift-testing", "--jobs", "1"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	closure, err := CaptureClosure(ctx, root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	defer closure.Close()
	if closure.InventoryComplete || len(closure.Inventory.Units) != 1 || len(closure.Inventory.Cases) != 1 || closure.Inventory.Cases[0].Unit != closure.Inventory.Units[0].ID {
		t.Fatal("shared observed inventory or false completeness lost", closure.Inventory)
	}
	if err := closure.Verify(ctx); err != nil {
		t.Fatal("healthy exported verification", err)
	}
	write(t, dependencies, "checkouts/retained/data.txt", "lasting declared dependency fault\n")
	if err := closure.Verify(ctx); err == nil || !strings.Contains(err.Error(), "dependency") {
		t.Fatal("exported verification missed original dependency drift", err)
	}
	write(t, dependencies, "checkouts/retained/data.txt", "declared dependency bytes\n")
	if err := closure.Verify(ctx); err == nil {
		t.Fatal("failed dependency context became usable after restoration")
	}
}

func TestInstalledSwiftNativeGenerationControl(t *testing.T) {
	tool := os.Getenv("HAYAKU_SWIFT_BINARY")
	if tool == "" {
		t.Skip("set HAYAKU_SWIFT_BINARY to installed supported Apple Swift")
	}
	root := t.TempDir()
	write(t, root, "Package.swift", "// swift-tools-version:6.0\nimport PackageDescription\nlet package = Package(name: \"Generation\", targets: [.testTarget(name: \"GenerationTests\")])\n")
	write(t, root, "Tests/GenerationTests/GenerationTests.swift", "import XCTest\nfinal class GenerationTests: XCTestCase { func testHealthy() { XCTAssertEqual(7,7) } }\n")
	tmp := t.TempDir()
	build := filepath.Join(tmp, "build")
	args := []string{"test", "--package-path", root, "--scratch-path", build, "--cache-path", filepath.Join(tmp, "cache"), "--config-path", filepath.Join(tmp, "configuration"), "--security-path", filepath.Join(tmp, "security"), "--disable-automatic-resolution", "--skip-update", "--disable-swift-testing", "--jobs", "1"}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	listed, err := process.Run(ctx, model.Command{Executable: tool, Args: append(append([]string(nil), args...), "list")}, root, nil)
	if err != nil || !listed.Completed || !strings.Contains(string(listed.Stdout), "GenerationTests.GenerationTests/testHealthy") {
		t.Fatal("original independent listing", err)
	}
	capture := func() *inputbundle.Bundle {
		t.Helper()
		b, err := inputbundle.Capture(ctx, inputbundle.Request{Roots: []inputbundle.Root{{Name: "generation", Path: build, Mount: "."}}, Inputs: []inputbundle.Input{{Root: "generation", Path: ".", Generated: true}}})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	before := capture()
	rootBefore, err := os.Lstat(build)
	if err != nil {
		t.Fatal(err)
	}
	ran, err := process.Run(ctx, model.Command{Executable: tool, Args: args}, root, nil)
	if err != nil || !ran.Completed || ran.ExitCode != 0 || !strings.Contains(string(ran.Stdout), "Executed 1 test") {
		t.Fatal("original independent healthy execution", err)
	}
	after := capture()
	rootAfter, err := os.Lstat(build)
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string]inputbundle.Entry{}
	for _, entry := range before.Manifest().Entries {
		entries[entry.Path] = entry
	}
	for _, entry := range after.Manifest().Entries {
		old, exists := entries[entry.Path]
		if !exists || old != entry {
			t.Logf("native generated mutation: %s (%s)", entry.Path, entry.Kind)
		}
		delete(entries, entry.Path)
	}
	for name := range entries {
		t.Logf("native generated removal: %s", name)
	}
	t.Logf("native generation before=%s after=%s rootMetadataUnchanged=%t", before.Manifest().Digest, after.Manifest().Digest, sameAppleFile(rootBefore, rootAfter))
}

func TestAppleProducerStagesPreserveInputsAndBecomeUnusableOnFailure(t *testing.T) {
	for _, fault := range []string{"healthy", "product", "source", "escape", "cancel"} {
		t.Run(fault, func(t *testing.T) {
			c, root, _ := appleFixture(t)
			ctx := context.Background()
			namespace, err := c.NewOutputRoot(ctx, "apple-producer-test-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(namespace)
			build := filepath.Join(namespace, "build")
			products := filepath.Join(build, "Products")
			write(t, products, "binary", "immutable product")
			write(t, build, "build.db", "seed generation")
			healthy := filepath.Join(t.TempDir(), "healthy")
			script := "#!/bin/sh\nprintf 'new generation' > \"$1/build.db\"\nprintf 'stage-success\\n'\n"
			if err := os.WriteFile(healthy, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := c.CaptureTool(ctx, healthy); err != nil {
				t.Fatal(err)
			}
			out, err := c.RunProducer(ctx, healthy, []string{build}, []string{build})
			if err != nil || !out.Completed || string(out.Stdout) != "stage-success\n" || len(c.stages) != 1 || c.TreeDigest(build) == "" {
				t.Fatal(out, err)
			}
			if data, err := os.ReadFile(filepath.Join(build, "build.db")); err != nil || string(data) != "new generation" {
				t.Fatal("actual output", err)
			}
			if err := c.CaptureTree(ctx, products); err != nil {
				t.Fatal(err)
			}
			if _, err := c.RunProducer(ctx, healthy, nil, []string{products}); err == nil {
				t.Fatal("immutable Products promoted to output")
			}
			switch fault {
			case "product":
				script += "printf 'changed product' > \"$1/Products/binary\"\n"
			case "source":
				script += "printf 'changed source' > \"$2/source.swift\"\n"
			case "escape":
				script += "rm -rf \"$1\"\nln -s \"$2\" \"$1\"\n"
			case "cancel":
				script += "sleep 10\n"
			}
			tool := filepath.Join(t.TempDir(), "next-stage")
			if err := os.WriteFile(tool, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := c.CaptureTool(ctx, tool); err != nil {
				t.Fatal(err)
			}
			before := c.Digest()
			runCtx := ctx
			if fault == "cancel" {
				var stop context.CancelFunc
				runCtx, stop = context.WithTimeout(ctx, 50*time.Millisecond)
				defer stop()
			}
			out, err = c.RunProducer(runCtx, tool, []string{build, root}, []string{build})
			if fault == "healthy" {
				if err != nil || !out.Completed || c.Digest() == before || len(c.stages) != 2 {
					t.Fatal(out, err)
				}
				if err := c.Verify(ctx); err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil || out.Completed || !c.unusable {
					t.Fatal("fault stage admitted", out, err)
				}
				if err := c.Verify(ctx); err == nil {
					t.Fatal("failed producer usable")
				}
				if _, err := c.RunProducer(ctx, healthy, []string{build}, []string{build}); err == nil {
					t.Fatal("failed producer resealed")
				}
			}
		})
	}
}

func TestAppleCapturedTreeRetainsAbsoluteInternalRootAlias(t *testing.T) {
	c, _, tool := appleFixture(t)
	tree := t.TempDir()
	write(t, tree, "value", "known internal generated bytes")
	if err := os.Symlink(filepath.Join(tree, "value"), filepath.Join(tree, "absolute-link")); err != nil {
		t.Fatal(err)
	}
	if err := c.CaptureTree(context.Background(), tree); err != nil {
		t.Fatal("absolute internal declared alias rejected", err)
	}
	resolved, err := filepath.EvalSymlinks(tree)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("actual declared parent alias differs from physical root: %v", resolved != tree)

	if out, err := c.Run(context.Background(), tool, nil); err != nil || !out.Completed {
		t.Fatal("healthy captured alias", err)
	}
	if err := os.Remove(filepath.Join(tree, "absolute-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), filepath.Join(tree, "absolute-link")); err != nil {
		t.Fatal(err)
	}
	if out, err := c.Run(context.Background(), tool, nil); err == nil || out.Completed {
		t.Fatal("changed external alias accepted", err)
	}
	fresh, _, _ := appleFixture(t)
	outside := tree + "-external"
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(outside)
	write(t, outside, "value", "actual outside prefix-neighbor bytes")
	candidate := tree
	if err := os.Remove(filepath.Join(candidate, "absolute-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "value"), filepath.Join(candidate, "external-link")); err != nil {
		t.Fatal(err)
	}
	if err := fresh.CaptureTree(context.Background(), candidate); err == nil || !strings.Contains(err.Error(), "symlink escapes declared roots") {
		t.Fatal("actual external root admitted", err)
	}
}

func TestAppleObservedFrameworkArtifactsBindTopologyAndRealBytes(t *testing.T) {
	for _, fault := range []string{"healthy", "bytes", "membership", "metadata", "retarget", "outside"} {
		t.Run(fault, func(t *testing.T) {
			c, _, tool := appleFixture(t)
			ctx := context.Background()
			tree := t.TempDir()
			write(t, tree, "Fixture.framework/Versions/A/Resources/value", "native resource bytes")
			write(t, tree, "Fixture.framework/Versions/B/Resources/value", "other captured generation")
			current := filepath.Join(tree, "Fixture.framework", "Versions", "Current")
			if err := os.Symlink("A", current); err != nil {
				t.Fatal(err)
			}
			alias := filepath.Join(tree, "Fixture.framework", "Resources")
			if err := os.Symlink("Versions/Current/Resources", alias); err != nil {
				t.Fatal(err)
			}
			if err := c.CaptureTree(ctx, tree); err == nil || !strings.Contains(err.Error(), "ancestors must be real directories") {
				t.Fatal("strict runtime bundle unexpectedly accepts alias ancestors", err)
			}
			if err := c.CaptureArtifactTree(ctx, tree); err != nil {
				t.Fatal("healthy observed framework", err)
			}
			if out, err := c.Run(ctx, tool, nil); err != nil || !out.Completed {
				t.Fatal("healthy framework validation", err)
			}
			target := filepath.Join(tree, "Fixture.framework/Versions/A/Resources/value")
			switch fault {
			case "healthy":
				return
			case "bytes":
				if err := os.WriteFile(target, []byte("lasting changed native resource"), 0600); err != nil {
					t.Fatal(err)
				}
			case "membership":
				write(t, tree, "Fixture.framework/Versions/A/Resources/added", "unselected artifact")
			case "metadata":
				if err := os.Chmod(target, 0400); err != nil {
					t.Fatal(err)
				}
			case "retarget":
				if err := os.Remove(current); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("B", current); err != nil {
					t.Fatal(err)
				}
			case "outside":
				outside := tree + "-external"
				if err := os.Mkdir(outside, 0700); err != nil {
					t.Fatal(err)
				}
				defer os.RemoveAll(outside)
				write(t, outside, "value", "real outside bytes")
				if err := os.Remove(alias); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, alias); err != nil {
					t.Fatal(err)
				}
			}
			if out, err := c.Run(ctx, tool, nil); err == nil || out.Completed {
				t.Fatal("changed readonly artifact accepted", err)
			}
		})
	}
}

func TestAppleOwnedProducerBindsSameContentMetadataGenerations(t *testing.T) {
	for _, fault := range []string{"healthy", "bytes", "mode", "membership", "link", "root-alias", "strict", "outside", "cancel"} {
		t.Run(fault, func(t *testing.T) {
			c, _, _ := appleFixture(t)
			ctx := context.Background()
			namespace, err := c.NewOutputRoot(ctx, "apple-generation-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(namespace)
			build := filepath.Join(namespace, "build")
			products := filepath.Join(build, "Products")
			write(t, products, "Versions/A/Resources/value", "stable compiled resource")
			write(t, products, "Versions/B/Resources/value", "other generation")
			current := filepath.Join(products, "Versions/Current")
			if fault != "strict" {
				if err := os.Symlink("A", current); err != nil {
					t.Fatal(err)
				}
			}
			file := filepath.Join(products, "Versions/A/Resources/value")
			if err := os.Chtimes(file, time.Unix(1700000000, 0), time.Unix(1700000000, 0)); err != nil {
				t.Fatal(err)
			}
			seed := filepath.Join(t.TempDir(), "seed")
			if err := os.WriteFile(seed, []byte("#!/bin/sh\nprintf 'bookkeeping' > \"$1/build.db\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := c.CaptureTool(ctx, seed); err != nil {
				t.Fatal(err)
			}
			if out, err := c.RunProducer(ctx, seed, []string{build}, []string{build}); err != nil || !out.Completed {
				t.Fatal("initial producer", err)
			}
			if fault == "strict" {
				if err := c.CaptureTree(ctx, products); err != nil {
					t.Fatal(err)
				}
			} else if err := c.CaptureArtifactTree(ctx, products); err != nil {
				t.Fatal(err)
			}
			before := c.trees[products]
			oldInfo, err := os.Lstat(file)
			if err != nil {
				t.Fatal(err)
			}
			script := "#!/bin/sh\nset -eu\ncp -Rp \"$1/Products\" \"$1/rebuilt\"\ntouch \"$1/rebuilt/Versions/A/Resources/value\"\nrm -rf \"$1/Products\"\nmv \"$1/rebuilt\" \"$1/Products\"\nprintf 'native-success\\n'\n"
			switch fault {
			case "bytes":
				script += "printf 'changed compiled bytes' > \"$1/Products/Versions/A/Resources/value\"\n"
			case "mode":
				script += "chmod 400 \"$1/Products/Versions/A/Resources/value\"\n"
			case "membership":
				script += "printf 'extra' > \"$1/Products/added\"\n"
			case "link":
				script += "rm \"$1/Products/Versions/Current\"\nln -s B \"$1/Products/Versions/Current\"\n"
			case "root-alias":
				script += "mv \"$1/Products\" \"$1/elsewhere\"\nln -s elsewhere \"$1/Products\"\n"
			case "outside":
				external := t.TempDir()
				write(t, external, "value", "same external bytes")
				if err := c.CaptureArtifactTree(ctx, external); err != nil {
					t.Fatal(err)
				}
				script += "cp -p \"$2/value\" \"$2/replacement\"\nmv \"$2/replacement\" \"$2/value\"\n"
				namespace = external
			case "cancel":
				script += "sleep 10\n"
			}
			tool := filepath.Join(t.TempDir(), "recopy")
			if err := os.WriteFile(tool, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := c.CaptureTool(ctx, tool); err != nil {
				t.Fatal(err)
			}
			runCtx := ctx
			if fault == "cancel" {
				var cancel context.CancelFunc
				runCtx, cancel = context.WithTimeout(ctx, 50*time.Millisecond)
				defer cancel()
			}
			out, err := c.RunProducer(runCtx, tool, []string{build, namespace}, []string{build})
			if fault != "healthy" {
				if err == nil || out.Completed || !c.unusable {
					t.Fatal("invalid generation accepted", fault, out.Completed, err)
				}
				if fault == "cancel" && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("cancellation identity lost", err)
				}
				if err := c.Verify(ctx); err == nil {
					t.Fatal("invalid generation usable")
				}
				return
			}
			stageErr := err
			after := c.trees[products]
			newInfo, err := os.Lstat(file)
			if err != nil {
				t.Fatal(err)
			}
			if stageErr != nil || !out.Completed || out.ExitCode != 0 || string(out.Stdout) != "native-success\n" || before.digest == after.digest || before.semantic != after.semantic || os.SameFile(oldInfo, newInfo) || oldInfo.ModTime().Equal(newInfo.ModTime()) || len(c.stages) != 2 {
				t.Fatal("physical generation not bound", out.Completed, err)
			}
			if err := c.Verify(ctx); err != nil {
				t.Fatal("admitted generation", err)
			}
			replacement := file + "-copy"
			if err := os.WriteFile(replacement, []byte("stable compiled resource"), newInfo.Mode().Perm()); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(replacement, newInfo.ModTime(), newInfo.ModTime()); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(replacement, file); err != nil {
				t.Fatal(err)
			}
			if err := c.Verify(ctx); err == nil {
				t.Fatal("out-of-stage same-byte physical replacement accepted")
			}
		})
	}
}

func TestAppleStrictInputCopyReservesAndVerifiesBothViews(t *testing.T) {
	for _, fault := range []string{"healthy", "source", "copy", "descriptor", "observed", "existing", "alias", "bounds", "cancel"} {
		t.Run(fault, func(t *testing.T) {
			c, _, tool := appleFixture(t)
			ctx := context.Background()
			source := t.TempDir()
			write(t, source, "input.txt", "retained input")
			dir, err := c.NewOutputRoot(ctx, "import-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			dest := filepath.Join(dir, "copy")
			switch fault {
			case "observed":
				if err := c.CaptureArtifactTree(ctx, source); err != nil {
					t.Fatal(err)
				}
			case "existing":
				if err := os.Mkdir(dest, 0700); err != nil {
					t.Fatal(err)
				}
				write(t, dest, "keep.txt", "existing input")
			case "alias":
				alias := source + "-alias"
				if err := os.Symlink(source, alias); err != nil {
					t.Fatal(err)
				}
				source = alias
			case "bounds":
				f, err := os.OpenFile(filepath.Join(source, "too-large"), os.O_CREATE|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.Truncate(inputbundle.MaxFileBytes + 1); err != nil {
					f.Close()
					t.Fatal(err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			err = c.CopyInputTree(ctx, source, dest)
			if fault == "observed" || fault == "existing" || fault == "alias" || fault == "bounds" || fault == "cancel" {
				if err == nil {
					t.Fatal("invalid input copy accepted")
				}
				if fault == "existing" {
					data, err := os.ReadFile(filepath.Join(dest, "keep.txt"))
					if err != nil || string(data) != "existing input" {
						t.Fatal("copy overwrote existing input", err)
					}
				} else if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("failed copy retained partial bytes", err)
				}
				if fault == "observed" || fault == "alias" || fault == "bounds" {
					if err := c.Verify(context.Background()); err == nil {
						t.Fatal("failed input capture became usable")
					}
				}
				return
			}
			if err != nil || c.copy == nil {
				t.Fatal("healthy private input copy", err)
			}
			data, err := os.ReadFile(filepath.Join(dest, "input.txt"))
			if err != nil || string(data) != "retained input" {
				t.Fatal("private copy bytes", err)
			}
			switch fault {
			case "source":
				write(t, source, "input.txt", "source fault")
			case "copy":
				write(t, dest, "input.txt", "copy fault")
			case "descriptor":
				c.copy.destination = source
			}
			out, err := c.RunProducer(ctx, tool, nil, []string{dest})
			if fault == "healthy" {
				if err != nil || !out.Completed || out.ExitCode != 0 || c.copy != nil || c.TreeDigest(dest) == "" || c.Verify(ctx) != nil {
					t.Fatal("reserved copy first stage", err)
				}
			} else if err == nil || out.Completed {
				t.Fatal("changed copy/source/descriptor launched", err)
			}
		})
	}
}

// The internal SDK probe verifies its exact dispatcher first; a requested
// command must still pass the complete fresh role check after that probe.
func TestAppleToolGatesRejectDispatcherAndSecondaryDrift(t *testing.T) {
	for _, fault := range []string{"dispatcher-bytes", "dispatcher-alias", "secondary-bytes", "secondary-alias", "no-sdk-secondary"} {
		t.Run(fault, func(t *testing.T) {
			c, _, dispatcher := appleFixture(t)
			secondary := filepath.Join(t.TempDir(), "compiler")
			if err := os.WriteFile(secondary, []byte("#!/bin/sh\nprintf 'compiler\\n'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := c.CaptureTool(context.Background(), secondary); err != nil {
				t.Fatal(err)
			}
			gate := map[string]appleTool{dispatcher: c.tools[dispatcher]}
			if err := verifyAppleTools(context.Background(), gate); err != nil {
				t.Fatal("healthy dispatcher", err)
			}
			if err := c.Verify(context.Background()); err != nil {
				t.Fatal("healthy all-role gate", err)
			}
			changed := secondary
			if strings.HasPrefix(fault, "dispatcher") {
				changed = dispatcher
			}
			if strings.HasSuffix(fault, "alias") {
				replacement := changed + "-replacement"
				if err := os.WriteFile(replacement, []byte("#!/bin/sh\nprintf 'changed\\n'\n"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(changed); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(replacement, changed); err != nil {
					t.Fatal(err)
				}
			} else {
				before, err := os.Lstat(changed)
				if err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(changed)
				if err != nil || len(data) == 0 {
					t.Fatal("known small tool fixture", err)
				}
				data[0] = '!'
				if err := os.WriteFile(changed, data, before.Mode().Perm()); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(changed, before.ModTime(), before.ModTime()); err != nil {
					t.Fatal(err)
				}
				if err := c.tools[changed].verifyIdentity(); err != nil {
					t.Fatal("byte fault must preserve admitted metadata", err)
				}
			}
			if strings.HasPrefix(fault, "dispatcher") {
				if err := verifyAppleTools(context.Background(), gate); err == nil {
					t.Fatal("changed dispatcher admitted to internal query")
				}
			} else if err := verifyAppleTools(context.Background(), gate); err != nil {
				t.Fatal("unchanged dispatcher rejected", err)
			}
			if err := c.verifyTools(context.Background()); err == nil {
				t.Fatal("changed role passed complete post-query gate")
			}
			if fault == "no-sdk-secondary" {
				if err := c.Verify(context.Background()); err == nil {
					t.Fatal("SDK-free Verify stopped checking secondary bytes")
				}
			}
		})
	}
}
