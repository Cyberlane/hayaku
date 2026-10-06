package xcode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	apple "github.com/Cyberlane/hayaku/internal/adapter/swift"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/runner/native"
	"github.com/Cyberlane/hayaku/internal/snapshot"
)

func TestXcodeClosurePrivateContextAndFalseCompleteness(t *testing.T) {
	for _, fault := range []string{"args", "settings", "cases", "destination", "completeness"} {
		t.Run(fault, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "project.pbxproj"), []byte("original project"), 0600); err != nil {
				t.Fatal(err)
			}
			captured, err := apple.CaptureAppleClosure(context.Background(), root, model.Workspace{ID: "x", Root: ".", Command: model.Command{Dir: ".", Executable: "/usr/bin/true", Args: []string{"test"}}}, model.Context{})
			if err != nil {
				t.Fatal(err)
			}
			c := &Closure{apple: captured, tool: "/usr/bin/true", args: []string{"test", "-destination", "platform=macOS"}, settings: "original", workspace: "x", cases: []native.Case{{Unit: "x:suite", ID: "healthy"}}}
			c.seal = c.requestDigest()
			if err := c.Verify(context.Background()); err != nil {
				t.Fatal("healthy descriptive closure", err)
			}
			switch fault {
			case "args":
				c.args[0] = "build"
			case "settings":
				c.settings = "changed"
			case "cases":
				c.cases[0].ID = "changed"
			case "destination":
				c.args[2] = "platform=iOS Simulator"
			case "completeness":
				c.InventoryComplete = true
			}
			if err := c.Verify(context.Background()); err == nil {
				t.Fatal("changed context accepted")
			}
			if _, err := c.Collect(context.Background()); err == nil {
				t.Fatal("collection resealed prior context drift")
			}
			if _, err := c.ReadResult(context.Background(), "/unavailable-result"); err == nil {
				t.Fatal("result stage ignored prior context drift")
			}
		})
	}
}
func TestXcodeResultReaderOwnsOnlyPrivateOutputs(t *testing.T) {
	for _, fault := range []string{"healthy", "imported", "source", "product", "readonly-result", "malformed-summary"} {
		t.Run(fault, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			root, tools := t.TempDir(), t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("original source"), 0600); err != nil {
				t.Fatal(err)
			}
			tool := filepath.Join(tools, "build")
			reader := filepath.Join(tools, "reader")
			if err := os.WriteFile(tool, []byte("#!/bin/sh\nprintf 'Xcode 27.0\\nBuild version fixture\\n'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			tree := `{"devices":[{}],"testPlanConfigurations":[{}],"testNodes":[{"nodeType":"Unit test bundle","name":"Tests","children":[{"nodeType":"Test Case","nodeIdentifier":"Class/testPass()","result":"Passed"}]}]}`
			summary := `{"result":"Passed","totalTestCount":1,"passedTests":1,"failedTests":0,"skippedTests":0,"expectedFailures":0,"startTime":1,"finishTime":2}`
			if fault == "malformed-summary" {
				summary = `{}`
			}
			extra := ""
			if fault == "source" {
				extra = `printf changed > "$ORIGINAL_SOURCE/source.txt"` + "\n"
			}
			if fault == "product" {
				extra = `printf changed > "$7/../product/data.txt"` + "\n"
			}
			body := "#!/bin/sh\nset -eu\n" + extra + `printf cache > "$7/database.sqlite3"` + "\nif [ \"$3\" = tests ]; then\nprintf '%s\\n' '" + tree + "'\nelse\nprintf '%s\\n' '" + summary + "'\nfi\n"
			if err := os.WriteFile(reader, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			w := model.Workspace{ID: "x", Root: ".", Command: model.Command{Dir: ".", Executable: tool}}
			captured, err := apple.CaptureAppleClosure(ctx, root, w, model.Context{Env: map[string]string{"ORIGINAL_SOURCE": root}})
			if err != nil {
				t.Fatal(err)
			}
			tool, err = captured.CaptureTool(ctx, tool)
			if err != nil {
				t.Fatal(err)
			}
			reader, err = captured.CaptureTool(ctx, reader)
			if err != nil {
				t.Fatal(err)
			}
			dir, err := captured.NewOutputRoot(ctx, "reader-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			bundle := filepath.Join(dir, "result.xcresult")
			original := ""
			if fault == "imported" {
				original = t.TempDir()
				if err := os.WriteFile(filepath.Join(original, "result-data"), []byte("declared native result"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := captured.CopyInputTree(ctx, original, bundle); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(bundle, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(bundle, "result-data"), []byte("declared native result"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if fault == "product" {
				// A result reader may not alter a different retained input tree.
				// Use a relative path from the bundle so no caller environment is merged.
				product := filepath.Join(dir, "product")
				if err := os.Mkdir(product, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(product, "data.txt"), []byte("original product"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := captured.CaptureArtifactTree(ctx, product); err != nil {
					t.Fatal(err)
				}
			}
			if fault == "readonly-result" {
				if err := captured.CaptureArtifactTree(ctx, bundle); err != nil {
					t.Fatal(err)
				}
			}
			cases, err := native.XcodeCases([]byte(`{"errors":[],"values":[{"enabledTests":[{"identifier":"Tests/Class/testPass()"}],"disabledTests":[]}]}`), "x:suite")
			if err != nil {
				t.Fatal(err)
			}
			c := &Closure{apple: captured, tool: tool, resultTool: reader, dir: dir, cases: cases, workspace: "x"}
			c.seal = c.requestDigest()
			result, err := c.ReadResult(ctx, bundle)
			if fault == "healthy" || fault == "imported" {
				if err != nil || !result.Complete || result.Failed || len(result.Tests) != 1 {
					t.Fatal("private reader generation", result, err)
				}
				if data, err := os.ReadFile(filepath.Join(bundle, "database.sqlite3")); err != nil || string(data) != "cache" {
					t.Fatal("actual reader output missing", err)
				}
				if err := c.Verify(ctx); err != nil || captured.TreeDigest(bundle) == "" {
					t.Fatal("final private result binding", err)
				}
			} else if err == nil || result.Complete {
				t.Fatal("invalid reader context accepted", result, err)
			}
			if original != "" {
				if _, err := os.Lstat(filepath.Join(original, "database.sqlite3")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("import reader mutated original", err)
				}
			}
			if fault == "readonly-result" {
				if _, err := os.Lstat(filepath.Join(bundle, "database.sqlite3")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("readonly result promoted into output", err)
				}
			}
		})
	}
}

func TestXcodeConfiguredScopeRetainsTestPlanAndDestination(t *testing.T) {
	w := model.Workspace{Command: model.Command{Executable: "/usr/bin/xcodebuild", Args: []string{"-scheme", "Fixture", "-testPlan", "test", "-destination", "platform=macOS", "test"}}}
	args, err := command(w)
	if err != nil || len(args) != 7 || args[3] != "test" || args[5] != "platform=macOS" {
		t.Fatal("configured testplan/destination lost", args, err)
	}
	w.Command.Args[1] = "changed caller"
	if args[1] != "Fixture" {
		t.Fatal("retained arguments alias caller")
	}
	for _, args := range [][]string{{"-testPlan", "test"}, {"-scheme", "test"}, {"-configuration", "test"}, {"-destination"}} {
		w.Command.Args = args
		if _, err := command(w); err == nil {
			t.Fatal("option value invented full test action", args)
		}
	}
	for _, version := range []string{"Xcode 26.3\nBuild version fixture\n", "Xcode 27.0\nBuild version fixture\n"} {
		if _, err := resultSchema([]byte(version)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := resultSchema([]byte("Xcode 28.0\n")); err == nil {
		t.Fatal("unknown schema guessed")
	}
}

func TestInstalledXcodeOriginalAndObservedClosure(t *testing.T) {
	tool := os.Getenv("HAYAKU_XCODE_BINARY")
	if tool == "" {
		t.Skip("set HAYAKU_XCODE_BINARY to installed Xcode")
	}
	if !filepath.IsAbs(tool) {
		t.Fatal("absolute installed tool required")
	}
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(filepath.Join("..", "..", "..", "testdata", "xcode-native"))); err != nil {
		t.Fatal(err)
	}
	w := model.Workspace{ID: "observed", Root: ".", Adapter: "xcode", Command: model.Command{Dir: ".", Executable: tool, Args: []string{"-project", "Fixture.xcodeproj", "-scheme", "Fixture", "-destination", "platform=macOS", "-derivedDataPath", t.TempDir(), "-parallel-testing-enabled", "NO", "CODE_SIGNING_ALLOWED=NO", "test"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if err := PrepareSnapshot(ctx, root, w); err != nil {
		t.Fatal(err)
	}
	baseline, err := process.Run(ctx, w.Command, root, nil)
	if err != nil || !baseline.Completed || baseline.ExitCode != 0 || !strings.Contains(string(baseline.Stdout), "test") {
		t.Fatal("original Xcode fixture failed", err)
	}
	c, err := CaptureClosure(ctx, root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cases, err := c.Collect(ctx)
	if err != nil || len(cases) != 2 {
		t.Fatal("observed original inventory", cases, err)
	}
	inventory, err := c.Inventory(ctx)
	if err != nil || len(inventory.Cases) != 2 || c.InventoryComplete {
		t.Fatal("shared incomplete inventory", inventory, err)
	}
	if err := c.Verify(ctx); err != nil {
		t.Fatal("healthy observed closure", err)
	}
	bundle := filepath.Join(c.dir, "observed-run.xcresult")
	args := append(append([]string(nil), c.args...), "-quiet", "-resultBundlePath", bundle)
	before := xcodeProductFacts(t, ctx, c.products)
	out, err := c.Run(ctx, args)
	if err != nil || !out.Completed || out.ExitCode != 0 {
		after := xcodeProductFacts(t, ctx, c.products)
		for name, old := range before {
			next, ok := after[name]
			if !ok {
				t.Logf("product removed: %s", name)
				continue
			}
			if !os.SameFile(old.info, next.info) || old.info.Mode() != next.info.Mode() || old.info.Size() != next.info.Size() || !old.info.ModTime().Equal(next.info.ModTime()) || old.digest != next.digest {
				t.Logf("product changed: %s inode=%v mode=%v size=%v mtime=%v bytes=%v", name, !os.SameFile(old.info, next.info), old.info.Mode() != next.info.Mode(), old.info.Size() != next.info.Size(), !old.info.ModTime().Equal(next.info.ModTime()), old.digest != next.digest)
			}
		}
		for name := range after {
			if _, ok := before[name]; !ok {
				t.Logf("product added: %s", name)
			}
		}
		t.Fatal("retained full original scope", err)
	}
	// Standalone import reads only a verified private copy. The original bundle
	// has no reader database yet and must retain its exact source identity.
	beforeImport, err := snapshot.Digest(ctx, bundle)
	if err != nil {
		t.Fatal(err)
	}
	imported, err := ReadResult(ctx, bundle, cases, model.Context{})
	if err != nil || !imported.Complete || imported.Failed || len(imported.Tests) != 2 {
		t.Fatal("standalone original result import", imported, err)
	}
	afterImport, err := snapshot.Digest(ctx, bundle)
	if err != nil || beforeImport != afterImport {
		t.Fatal("standalone import mutated original result", err)
	}
	if _, err := os.Lstat(filepath.Join(bundle, "database.sqlite3")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("original result gained reader database", err)
	}
	resultBefore := xcodeProductFacts(t, ctx, []string{bundle})
	result, err := c.ReadResult(ctx, bundle)
	if err != nil || !result.Complete || result.Failed || len(result.Tests) != 2 {
		resultAfter := xcodeProductFacts(t, ctx, []string{bundle})
		for name, old := range resultBefore {
			next, ok := resultAfter[name]
			if !ok {
				t.Logf("result entry removed: %s", name)
				continue
			}
			if !os.SameFile(old.info, next.info) || old.info.Mode() != next.info.Mode() || old.info.Size() != next.info.Size() || !old.info.ModTime().Equal(next.info.ModTime()) || old.digest != next.digest {
				t.Logf("result entry changed: %s inode=%v mode=%v size=%v mtime=%v bytes=%v", name, !os.SameFile(old.info, next.info), old.info.Mode() != next.info.Mode(), old.info.Size() != next.info.Size(), !old.info.ModTime().Equal(next.info.ModTime()), old.digest != next.digest)
			}
		}
		for name := range resultAfter {
			if _, ok := resultBefore[name]; !ok {
				t.Logf("result entry added: %s", name)
			}
		}
		args := []string{"get", "test-results", "tests", "--schema-version", "0.4.0", "--path", bundle, "--compact"}
		guarded, guardedErr := c.apple.Run(ctx, c.resultTool, args)
		t.Logf("guarded export: completed=%v exit=%d error=%v", guarded.Completed, guarded.ExitCode, guardedErr)
		original, originalErr := process.Run(ctx, model.Command{Executable: c.resultTool, Args: args}, root, nil)
		t.Logf("original export: completed=%v exit=%d bytes=%d error=%v", original.Completed, original.ExitCode, len(original.Stdout), originalErr)
		t.Fatal("retained independent terminal cases", result, err)
	}
	if err := os.WriteFile(filepath.Join(root, "Sources", "Main.swift"), []byte("public func number() -> Int { 8 }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.Verify(ctx); err == nil {
		t.Fatal("lasting source/resource drift accepted")
	}
}

type xcodeProductFact struct {
	info   os.FileInfo
	digest string
}

func xcodeProductFacts(t *testing.T, ctx context.Context, roots []string) map[string]xcodeProductFact {
	t.Helper()
	result := map[string]xcodeProductFact{}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if len(result) >= 50000 {
				return errors.New("product fact count bound")
			}
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			name := filepath.Base(root) + "/" + filepath.ToSlash(rel)
			digest := ""
			if info.Mode().IsRegular() {
				digest, err = snapshot.FileDigest(ctx, path, 64<<20)
				if err != nil {
					return err
				}
			} else if info.Mode()&os.ModeSymlink != 0 {
				digest, err = os.Readlink(path)
				if err != nil {
					return err
				}
			}
			result[name] = xcodeProductFact{info, digest}
			return nil
		})
		if err != nil {
			t.Fatal("independent product facts", err)
		}
	}
	return result
}
