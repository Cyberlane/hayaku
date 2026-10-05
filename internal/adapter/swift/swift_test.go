package swift

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/snapshot"
)

func write(t *testing.T, root, name, data string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestScopeRejectsModesThatCannotBeReconciled(t *testing.T) {
	w := model.Workspace{ID: "s", Root: ".", Adapter: "swift", Command: model.Command{Dir: ".", Executable: "swift", Args: []string{"test", "--disable-swift-testing"}}}
	u := []model.Unit{{ID: "s:FirstTests", Workspace: "s", Selector: "FirstTests", Kind: "swift-target"}}
	cmd, err := Proposal(w, u)
	if err != nil || cmd.Args[len(cmd.Args)-1] != "^(FirstTests)\\." {
		t.Fatal("target filter changed", cmd, err)
	}
	for _, args := range [][]string{{"test"}, {"test", "--disable-swift-testing", "--parallel"}, {"test", "--disable-swift-testing", "--filter", "other"}, {"test", "--disable-swift-testing", "--skip-build"}, {"test", "--disable-swift-testing", "--scratch-path", "/tmp/shared"}, {"test", "--disable-swift-testing", "--jobs"}} {
		w.Command.Args = args
		if _, err := Proposal(w, u); err == nil {
			t.Fatal("unsupported scope accepted", args)
		}
	}
}

func TestProvisionedDependencyStateIsBoundAndRestricted(t *testing.T) {
	root := t.TempDir()
	write(t, root, "workspace-state.json", `{"version":7,"object":{"artifacts":[],"dependencies":[]}}`)
	for _, name := range []string{"checkouts", "repositories"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	w := &model.SwiftRuntime{Dependencies: root}
	first, err := RuntimeIdentity(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	write(t, root, "checkouts/dependency/Package.swift", "// changed bytes\n")
	second, err := RuntimeIdentity(context.Background(), w)
	if err != nil || first == second {
		t.Fatal("dependency bytes not bound", err)
	}
	for _, state := range []string{`{"object":{"artifacts":[{}],"dependencies":[]}}`, `{"object":{"dependencies":[{"packageRef":{"kind":"registry"},"state":{"name":"sourceControlCheckout"},"subpath":"dep"}]}}`, `{"object":{"dependencies":[{"packageRef":{"kind":"remoteSourceControl"},"state":{"name":"sourceControlCheckout"},"subpath":"../escape"}]}}`, `{"object":{},"object":{}}`} {
		write(t, root, "workspace-state.json", state)
		if _, err := RuntimeIdentity(context.Background(), w); err == nil {
			t.Fatal("unprovisioned dependency effects accepted")
		}
	}
}

func TestNativeSwiftTargetGraphAndTerminalResults(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin XCTest console fixture")
	}
	tool := os.Getenv("HAYAKU_SWIFT_BINARY")
	if tool == "" {
		t.Skip("set HAYAKU_SWIFT_BINARY to the installed supported Swift toolchain")
	}
	if !filepath.IsAbs(tool) {
		t.Fatal("Swift fixture requires explicit installed toolchain")
	}
	if _, err := exec.LookPath(tool); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	write(t, root, "Package.swift", "// swift-tools-version: 6.0\nimport PackageDescription\nlet package = Package(name: \"Fixture\", targets: [.target(name: \"First\"), .target(name: \"Second\"), .testTarget(name: \"FirstTests\", dependencies: [\"First\"]), .testTarget(name: \"SecondTests\", dependencies: [\"Second\"])])\n")
	write(t, root, "Sources/First/First.swift", "public func number() -> Int { 7 }\n")
	write(t, root, "Sources/Second/Second.swift", "public func number() -> Int { 9 }\n")
	write(t, root, "Tests/FirstTests/FirstTests.swift", "import XCTest\n@testable import First\nfinal class FirstTests: XCTestCase { func testNumber() { XCTAssertEqual(First.number(), 7) }; func testSkip() throws { throw XCTSkip(\"fixture\") } }\n")
	write(t, root, "Tests/SecondTests/SecondTests.swift", "import XCTest\n@testable import Second\nfinal class SecondTests: XCTestCase { func testNumber() { XCTAssertEqual(Second.number(), 9) } }\n")
	w := model.Workspace{ID: "s", Root: ".", Adapter: "swift", Command: model.Command{Dir: ".", Executable: tool, Args: []string{"test", "--disable-swift-testing"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	before, err := snapshot.Digest(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	e, err := Describe(ctx, root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Units) != 2 || len(e.Inputs["Sources/First/First.swift"]) != 1 || e.Inputs["Sources/First/First.swift"][0] != "s:First" {
		t.Fatalf("target graph lost ownership: %+v", e)
	}
	out, result, err := Execute(ctx, root, w, model.Context{}, e.Units, false)
	if err != nil || !out.Completed || !result.Complete || len(result.Tests) != 3 {
		t.Fatalf("full XCTest inventory: %+v %v", result, err)
	}
	skips := 0
	for _, r := range result.Tests {
		if r.Action == "skip" {
			skips++
		}
	}
	if skips != 1 {
		t.Fatal("native skip became pass")
	}
	selected := []model.Unit{}
	for _, u := range e.Units {
		if u.Selector == "FirstTests" {
			selected = append(selected, u)
		}
	}
	_, result, err = Execute(ctx, root, w, model.Context{}, selected, true)
	if err != nil || len(result.Tests) != 2 {
		t.Fatal("target proposal failed", err, result)
	}
	write(t, root, "Sources/First/First.swift", "public func number() -> Int { 8 }\n")
	_, result, err = Execute(ctx, root, w, model.Context{}, selected, true)
	if err == nil || !result.Failed || !result.Complete {
		t.Fatal("completed native failure lost", err, result)
	}
	write(t, root, "Sources/First/First.swift", "public func number() -> Int { 7 }\n")
	after, err := snapshot.Digest(ctx, root)
	if err != nil || before != after {
		t.Fatal("native discovery/execution mutated source", err)
	}
	if !strings.Contains(e.Version, "Apple Swift version") {
		t.Fatal("toolchain version missing")
	}
}
