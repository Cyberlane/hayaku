package xcode

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/snapshot"
)

func TestRejectsIncompleteScope(t *testing.T) {
	w := model.Workspace{Command: model.Command{Executable: "xcodebuild", Args: []string{"test"}}}
	if _, err := command(w); err != nil {
		t.Fatal(err)
	}
	for _, arg := range []string{"-only-testing:Tests", "-skip-testing:Tests", "-retry-tests-on-failure", "-test-iterations", "-resultBundlePath", "-enumerate-tests"} {
		w.Command.Args = []string{"test", arg}
		if _, err := command(w); err == nil {
			t.Fatal("partial/repeated caller scope accepted", arg)
		}
	}
	w.Command.Args = []string{"build"}
	if _, err := command(w); err == nil {
		t.Fatal("build is not tests")
	}
}

func TestNativeXcodeInventoryAndTerminalResults(t *testing.T) {
	tool := os.Getenv("HAYAKU_XCODE_BINARY")
	if runtime.GOOS != "darwin" || tool == "" {
		t.Skip("set HAYAKU_XCODE_BINARY to installed Xcode")
	}
	if !filepath.IsAbs(tool) {
		t.Fatal("explicit installed Xcode required")
	}
	root := t.TempDir()
	fixture := filepath.Join("..", "..", "..", "testdata", "xcode-native")
	err := filepath.WalkDir(fixture, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(fixture, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(root, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	w := model.Workspace{ID: "x", Root: ".", Adapter: "xcode", Command: model.Command{Dir: ".", Executable: tool, Args: []string{"-project", "Fixture.xcodeproj", "-scheme", "Fixture", "-destination", "platform=macOS", "-derivedDataPath", t.TempDir(), "-parallel-testing-enabled", "NO", "CODE_SIGNING_ALLOWED=NO", "test"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	before, err := snapshot.Digest(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	out, result, err := Execute(ctx, root, w, model.Context{})
	if err != nil || !out.Completed || !result.Complete || len(result.Tests) != 2 {
		t.Fatalf("native Xcode run %+v: %v", result, err)
	}
	skips := 0
	for _, test := range result.Tests {
		if test.Action == "skip" {
			skips++
		}
	}
	if skips != 1 {
		t.Fatal("native skip lost")
	}
	after, err := snapshot.Digest(ctx, root)
	if err != nil || before != after {
		t.Fatal("Xcode mutated source", err)
	}
	path := filepath.Join(root, "Sources", "Main.swift")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(data), "7", "8")), 0600); err != nil {
		t.Fatal(err)
	}
	_, result, err = Execute(ctx, root, w, model.Context{})
	if err == nil || !result.Complete || !result.Failed {
		t.Fatal("native assertion failure lost", result, err)
	}
}
