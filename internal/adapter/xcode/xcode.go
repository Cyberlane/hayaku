// Package xcode preserves full xcodebuild test scope and independently
// enumerates/reconciles XCTest outcomes. It does not narrow app or UI tests.
package xcode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	apple "github.com/Cyberlane/hayaku/internal/adapter/swift"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/runner/native"
)

func command(w model.Workspace) ([]string, error) {
	if filepath.Base(w.Command.Executable) != "xcodebuild" || len(w.Prerequisites) != 0 {
		return nil, errors.New("Xcode reconciliation requires plain xcodebuild without prerequisites")
	}
	hasTest := false
	values := map[string]bool{"-project": true, "-workspace": true, "-scheme": true, "-target": true, "-configuration": true, "-sdk": true, "-arch": true, "-destination": true, "-destination-timeout": true, "-derivedDataPath": true, "-testPlan": true, "-clonedSourcePackagesDirPath": true, "-parallel-testing-enabled": true, "-maximum-concurrent-test-simulator-destinations": true, "-maximum-concurrent-test-device-destinations": true, "-parallel-testing-worker-count": true, "-maximum-parallel-testing-workers": true, "-xcconfig": true, "-toolchain": true}
	for i := 0; i < len(w.Command.Args); i++ {
		arg := w.Command.Args[i]
		if values[arg] {
			if i+1 >= len(w.Command.Args) || w.Command.Args[i+1] == "" || strings.HasPrefix(w.Command.Args[i+1], "-") {
				return nil, errors.New("missing Xcode option value")
			}
			i++
			continue
		}
		if arg == "test" {
			hasTest = true
		}
		for _, prefix := range []string{"-only-testing", "-skip-testing", "-test-iterations", "-retry-tests", "-run-tests-until", "-test-enumeration", "-enumerate-tests", "-resultBundlePath"} {
			if strings.HasPrefix(arg, prefix) {
				return nil, errors.New("unsupported Xcode test selection, repetitions or caller-owned result bundle")
			}
		}
	}
	if !hasTest {
		return nil, errors.New("Xcode adapter requires the full test action")
	}
	return append([]string{}, w.Command.Args...), nil
}

func Collect(ctx context.Context, root string, w model.Workspace, c model.Context) (cases []native.Case, err error) {
	closure, err := CaptureClosure(ctx, root, w, c)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := closure.Close(); closeErr != nil {
			cases = nil
			err = closeErr
		}
	}()
	return closure.Collect(ctx)
}

func ReadResult(ctx context.Context, bundle string, expected []native.Case, c model.Context) (result native.Result, err error) {
	if len(expected) > 100000 {
		return result, errors.New("Xcode imported inventory exceeds bounds")
	}
	owners := map[string]bool{}
	var units []model.Unit
	total := 0
	for _, item := range expected {
		total += len(item.Unit) + len(item.ID)
		if len(item.Unit) > 8192 || len(item.ID) > 8192 || total > 8<<20 {
			return result, errors.New("Xcode imported inventory text exceeds bounds")
		}
		if !owners[item.Unit] {
			if len(owners) >= 50000 {
				return result, errors.New("Xcode imported owners exceed bounds")
			}
			owners[item.Unit] = true
			units = append(units, model.Unit{ID: item.Unit, Workspace: "result", Selector: "full", Kind: "suite"})
		}
	}
	if _, err := apple.AppleInventory(units, expected); err != nil {
		return result, err
	}
	cases := append([]native.Case(nil), expected...)
	// Imports keep the original result readonly. Reader-created databases belong
	// only to a bounded private copy, a changed and unqualified native context.
	w := model.Workspace{ID: "result", Root: ".", Command: model.Command{Dir: ".", Executable: "/usr/bin/xcodebuild"}}
	captured, err := apple.CaptureAppleClosure(ctx, bundle, w, c)
	if err != nil {
		return result, err
	}
	tool, err := captured.CaptureTool(ctx, "/usr/bin/xcodebuild")
	if err != nil {
		return result, err
	}
	tool, err = captured.SelectAppleTool(ctx, "xcodebuild")
	if err != nil {
		return result, err
	}
	selected, err := captured.SelectAppleTool(ctx, "xcresulttool")
	if err != nil {
		return result, err
	}
	dir, err := captured.NewOutputRoot(ctx, "hayaku-xcode-import-")
	if err != nil {
		return result, err
	}
	closure := &Closure{apple: captured, tool: tool, resultTool: selected, dir: dir, cleanup: dir, workspace: "result", cases: cases}
	defer func() {
		if closeErr := closure.Close(); closeErr != nil {
			result.Complete = false
			err = errors.New("Xcode imported result cleanup failed")
		}
	}()
	copy := filepath.Join(dir, "result.xcresult")
	if err := captured.CopyInputTree(ctx, bundle, copy); err != nil {
		return result, err
	}
	closure.seal = closure.requestDigest()
	return closure.ReadResult(ctx, copy)
}

func Execute(ctx context.Context, root string, w model.Workspace, c model.Context) (out process.Output, result native.Result, err error) {
	start := time.Now()
	defer func() { out.Duration = time.Since(start) }()
	closure, err := CaptureClosure(ctx, root, w, c)
	if err != nil {
		return process.Output{}, result, err
	}
	defer func() {
		if closeErr := closure.Close(); closeErr != nil {
			out.Completed = false
			err = closeErr
		}
	}()
	if _, err := closure.Collect(ctx); err != nil {
		return process.Output{}, result, err
	}
	if err := closure.Verify(ctx); err != nil {
		return process.Output{}, result, err
	}
	bundle := filepath.Join(closure.dir, "run.xcresult")
	args := append(append([]string(nil), closure.args...), "-quiet", "-resultBundlePath", bundle)
	out, runErr := closure.Run(ctx, args)
	if !out.Completed {
		return out, result, runErr
	}
	// Test execution may produce logs/results but must preserve compiled inputs.
	if err := closure.Verify(ctx); err != nil {
		out.Completed = false
		return out, result, err
	}
	result, err = closure.ReadResult(ctx, bundle)
	if err != nil {
		return out, result, err
	}
	if !out.Completed {
		return out, result, errors.New("Xcode process incomplete")
	}
	if runErr != nil {
		return out, result, runErr
	}
	if result.Failed {
		return out, result, errors.New("Xcode tests failed")
	}
	return out, result, nil
}

// PrepareSnapshot establishes Xcode's empty local-package metadata directories
// before the caller binds the private execution tree. They remain part of that
// digest: files or other mutation there are not ignored or silently removed.
func PrepareSnapshot(ctx context.Context, root string, w model.Workspace) error {
	count := 0
	return filepath.WalkDir(filepath.Join(root, w.Root), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		count++
		if count > 50000 {
			return errors.New("Xcode package input limit exceeded")
		}
		if d.IsDir() {
			if d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			if strings.HasSuffix(d.Name(), ".xcworkspace") {
				return os.MkdirAll(filepath.Join(path, "xcshareddata", "swiftpm", "configuration"), 0700)
			}
			return nil
		}
		if d.Name() != "Package.swift" {
			return nil
		}
		for _, name := range []string{"configuration", "xcode"} {
			if err := os.MkdirAll(filepath.Join(filepath.Dir(path), ".swiftpm", name), 0700); err != nil {
				return err
			}
		}
		return nil
	})
}
