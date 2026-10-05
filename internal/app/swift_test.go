package app

import (
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
)

func TestNativeSwiftShadowKeepsFullSuiteAndDetectsRuntimeMiss(t *testing.T) {
	tool := os.Getenv("HAYAKU_SWIFT_BINARY")
	if runtime.GOOS != "darwin" || tool == "" {
		t.Skip("explicit installed Darwin Swift fixture")
	}
	root := t.TempDir()
	write := func(path, contents string) {
		t.Helper()
		path = filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	c := model.Config{Schema: 1, Context: model.Context{ID: "swift-fixture", OS: runtime.GOOS, Arch: runtime.GOARCH}, Workspaces: []model.Workspace{{ID: "s", Root: ".", Adapter: "swift", Command: model.Command{Dir: ".", Executable: tool, Args: []string{"test", "--disable-swift-testing"}}}}}
	data, _ := json.Marshal(c)
	write("hayaku.json", string(data))
	write("Package.swift", "// swift-tools-version: 6.0\nimport PackageDescription\nlet package = Package(name: \"Fixture\", targets: [.target(name: \"First\"), .target(name: \"Second\"), .testTarget(name: \"FirstTests\", dependencies: [\"First\"]), .testTarget(name: \"SecondTests\", dependencies: [\"Second\"])])\n")
	write("Sources/First/First.swift", "public func number() -> Int { return 1 }\n")
	write("Sources/Second/Second.swift", "public func number() -> Int { 9 }\n")
	write("Tests/FirstTests/FirstTests.swift", "import XCTest\n@testable import First\nfinal class FirstTests: XCTestCase { func testNumber() { XCTAssertGreaterThan(number(), 0) } }\n")
	write("Tests/SecondTests/SecondTests.swift", `import XCTest
import Foundation
@testable import Second
final class SecondTests: XCTestCase {
 func testRuntimeSource() throws {
 let package = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
 let text = try String(contentsOf: package.appendingPathComponent("Sources/First/First.swift"), encoding: .utf8)
 XCTAssertFalse(text.contains("return 2"))
 }
}
`)
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git fixture: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("config", "user.name", "Fixture")
	git("config", "user.email", "fixture@example.test")
	git("config", "commit.gpgsign", "false")
	git("add", ".")
	git("commit", "-qm", "base")
	base := git("rev-parse", "HEAD")
	write("Sources/First/First.swift", "public func number() -> Int { return 2 }\n")
	git("add", ".")
	git("commit", "-qm", "runtime change")
	head := git("rev-parse", "HEAD")
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	p, err := Build(ctx, root, base, head, c, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Proposed) != 1 || p.Proposed[0].Selector != "FirstTests" || len(p.Selected) != 2 || p.Mode != "full-fallback" {
		t.Fatalf("Swift safety envelope %+v", p)
	}
	if len(p.Commands) != 1 || strings.Join(p.Commands[0].Args, " ") != "test --disable-swift-testing" {
		t.Fatal("full command narrowed")
	}
	result, err := Shadow(ctx, root, c, p)
	if err == nil || len(result.ObservedMisses) == 0 {
		t.Fatal("runtime influence miss not exposed", result, err)
	}
	run, err := Execute(ctx, root, c, p)
	if err == nil || run.Passed {
		t.Fatal("failed full XCTest suite became green", run, err)
	}
	// New/deleted target resources must map by the target's directory, even when
	// the candidate or base does not contain the individual file.
	write("Sources/First/new-resource.txt", "fixture")
	git("add", ".")
	git("commit", "-qm", "new resource")
	resource := git("rev-parse", "HEAD")
	p, err = Build(ctx, root, head, resource, c, false)
	if err != nil || len(p.Proposed) != 1 || p.Proposed[0].Selector != "FirstTests" {
		t.Fatal("new resource ownership lost", err, p)
	}
	if err := os.Remove(filepath.Join(root, "Sources", "First", "new-resource.txt")); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "deleted resource")
	p, err = Build(ctx, root, resource, git("rev-parse", "HEAD"), c, false)
	if err != nil || len(p.Proposed) != 1 || p.Proposed[0].Selector != "FirstTests" {
		t.Fatal("deleted resource ownership lost", err, p)
	}
}
