// Package xcode preserves full xcodebuild test scope and independently
// enumerates/reconciles XCTest outcomes. It does not narrow app or UI tests.
package xcode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/runner/native"
)

func command(w model.Workspace) ([]string, error) {
	if filepath.Base(w.Command.Executable) != "xcodebuild" || len(w.Prerequisites) != 0 {
		return nil, errors.New("Xcode reconciliation requires plain xcodebuild without prerequisites")
	}
	hasTest := false
	for _, arg := range w.Command.Args {
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

func Collect(ctx context.Context, root string, w model.Workspace, c model.Context) ([]native.Case, error) {
	args, err := command(w)
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "hayaku-xcode-inventory-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	file := filepath.Join(tmp, "cases.json")
	args = append(args, "-enumerate-tests", "-test-enumeration-style", "flat", "-test-enumeration-format", "json", "-test-enumeration-output-path", file, "-disableAutomaticPackageResolution", "-skipPackageUpdates")
	out, err := process.Run(ctx, model.Command{Dir: w.Command.Dir, Executable: w.Command.Executable, Args: args}, filepath.Join(root, w.Root, w.Command.Dir), c.Env)
	if err != nil || !out.Completed {
		return nil, errors.New("Xcode test enumeration failed (native diagnostics withheld)")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, errors.New("Xcode enumeration is missing")
	}
	return native.XcodeCases(data, w.ID+":suite")
}

func ReadResult(ctx context.Context, bundle string, expected []native.Case, c model.Context) (native.Result, error) {
	version, err := process.Run(ctx, model.Command{Dir: ".", Executable: "/usr/bin/xcodebuild", Args: []string{"-version"}}, filepath.Dir(bundle), c.Env)
	if err != nil || !regexp.MustCompile(`^Xcode (26\.3|27\.0)\n`).Match(version.Stdout) {
		return native.Result{}, errors.New("Xcode result reconciliation requires installed Xcode 26.3 or 27.0")
	}
	var data [][]byte
	for _, kind := range []string{"tests", "summary"} {
		out, err := process.Run(ctx, model.Command{Dir: ".", Executable: "/usr/bin/xcrun", Args: []string{"xcresulttool", "get", "test-results", kind, "--path", bundle, "--compact"}}, filepath.Dir(bundle), c.Env)
		if err != nil || !out.Completed {
			return native.Result{}, errors.New("xcresulttool export failed (native diagnostics withheld)")
		}
		data = append(data, out.Stdout)
	}
	return native.ReconcileXCResult(data[0], data[1], expected)
}

func Execute(ctx context.Context, root string, w model.Workspace, c model.Context) (out process.Output, result native.Result, err error) {
	start := time.Now()
	defer func() { out.Duration = time.Since(start) }()
	cases, err := Collect(ctx, root, w, c)
	if err != nil {
		return process.Output{}, result, err
	}
	args, err := command(w)
	if err != nil {
		return process.Output{}, result, err
	}
	tmp, err := os.MkdirTemp("", "hayaku-xcode-result-")
	if err != nil {
		return process.Output{}, result, err
	}
	defer os.RemoveAll(tmp)
	bundle := filepath.Join(tmp, "run.xcresult")
	args = append(args, "-resultBundlePath", bundle, "-disableAutomaticPackageResolution", "-skipPackageUpdates")
	out, runErr := process.Run(ctx, model.Command{Dir: w.Command.Dir, Executable: w.Command.Executable, Args: args}, filepath.Join(root, w.Root, w.Command.Dir), c.Env)
	result, err = ReadResult(ctx, bundle, cases, c)
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
