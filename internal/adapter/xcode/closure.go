package xcode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	apple "github.com/Cyberlane/hayaku/internal/adapter/swift"
	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/runner/native"
)

const ClosureVersion = "xcode-observed-closure-v4-reader-generations"

// Closure preserves configured test scope and binds independent observed cases.
// False completeness is deliberate: settings/enumeration do not prove absence
// of dynamic tests, plugins, native code, device effects or external state.
type Closure struct {
	seal, cleanup                  string
	products                       []string
	apple                          *apple.AppleClosure
	tool, resultTool, dir, derived string
	args                           []string
	settings, generated            string
	cases                          []native.Case
	workspace                      string
	InventoryComplete              bool
}

func digest(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func CaptureClosure(ctx context.Context, root string, w model.Workspace, c model.Context) (*Closure, error) {
	captured, err := apple.CaptureAppleClosure(ctx, root, w, c)
	if err != nil {
		return nil, err
	}
	args, err := command(w)
	if err != nil {
		return nil, err
	}
	tool, err := captured.CaptureTool(ctx, w.Command.Executable)
	if err != nil {
		return nil, err
	}
	if tool == "/usr/bin/xcodebuild" {
		tool, err = captured.SelectAppleTool(ctx, "xcodebuild")
		if err != nil {
			return nil, err
		}
	}
	if err := captured.CaptureAppleToolchain(ctx); err != nil {
		return nil, err
	}
	resultTool, err := captured.SelectAppleTool(ctx, "xcresulttool")
	if err != nil {
		return nil, err
	}
	dir, err := captured.NewOutputRoot(ctx, "hayaku-xcode-closure-")
	if err != nil {
		return nil, err
	}
	closure := &Closure{apple: captured, tool: tool, resultTool: resultTool, dir: dir, derived: filepath.Join(dir, "derived"), workspace: w.ID, cleanup: dir}
	// Private output paths are changed producer context, explicitly unqualified.
	// Keep every original testplan/destination/build flag and full test action.
	for i := 0; i < len(args); i++ {
		if args[i] == "-derivedDataPath" {
			if i+1 >= len(args) {
				closure.Close()
				return nil, errors.New("missing Xcode derived path")
			}
			i++
			continue
		}
		closure.args = append(closure.args, args[i])
	}
	closure.args = append(closure.args, "-derivedDataPath", closure.derived, "-disableAutomaticPackageResolution", "-skipPackageUpdates")
	settingsArgs := append([]string(nil), closure.args...)
	settingsArgs = append(settingsArgs, "-showBuildSettings", "-json")
	out, err := captured.Run(ctx, tool, settingsArgs)
	var records []struct {
		Action   string            `json:"action"`
		Target   string            `json:"target"`
		Settings map[string]string `json:"buildSettings"`
	}
	if err != nil || native.DecodeJSON(out.Stdout, &records) != nil || len(records) == 0 || len(records) > 50000 {
		closure.Close()
		return nil, errors.New("Xcode configured build settings unavailable")
	}
	for _, record := range records {
		if record.Action == "" || record.Target == "" || len(record.Settings) == 0 {
			closure.Close()
			return nil, errors.New("incomplete Xcode configured settings")
		}
	}
	seen := map[string]bool{}
	for _, record := range records {
		path := record.Settings["BUILT_PRODUCTS_DIR"]
		rel, err := filepath.Rel(closure.derived, path)
		if path == "" || !filepath.IsAbs(path) || err != nil || !config.Relative(filepath.ToSlash(rel)) {
			closure.Close()
			return nil, errors.New("Xcode products escape private DerivedData")
		}
		if !seen[path] {
			seen[path] = true
			closure.products = append(closure.products, path)
		}
	}
	closure.settings = digest(records)
	closure.seal = closure.requestDigest()
	return closure, nil
}
func (c *Closure) Close() error {
	if c == nil || c.dir == "" {
		return nil
	}
	return os.RemoveAll(c.cleanup)
}
func (c *Closure) Verify(ctx context.Context) error {
	if c == nil || c.InventoryComplete || c.seal != c.requestDigest() {
		return errors.New("invalid Xcode observational closure")
	}
	if err := c.apple.Verify(ctx); err != nil {
		return err
	}

	return nil
}
func (c *Closure) Collect(ctx context.Context) ([]native.Case, error) {
	if err := c.Verify(ctx); err != nil {
		return nil, err
	}
	file := filepath.Join(c.dir, "cases.json")
	args := append(append([]string(nil), c.args...), "-quiet", "-enumerate-tests", "-test-enumeration-style", "flat", "-test-enumeration-format", "json", "-test-enumeration-output-path", file)
	out, err := c.apple.RunProducer(ctx, c.tool, args, []string{c.derived})
	if err != nil || !out.Completed {
		return nil, errors.New("Xcode test enumeration failed (native diagnostics withheld)")
	}
	data, err := apple.ReadAppleFile(ctx, file, process.MaxOutput)
	if err != nil {
		return nil, errors.New("Xcode bounded enumeration is missing or invalid")
	}
	cases, err := native.XcodeCases(data, c.workspace+":suite")
	if err != nil {
		return nil, err
	}
	if err = c.apple.CaptureArtifactTree(ctx, c.derived); err != nil {
		return nil, errors.New("Xcode generated producer closure exceeds bounds or changed")
	}
	for _, path := range c.products {
		if err := c.apple.CaptureArtifactTree(ctx, path); err != nil {
			return nil, err
		}
	}
	c.generated = c.apple.TreeDigest(c.derived)
	c.cases = append([]native.Case(nil), cases...)
	if _, err := apple.AppleInventory([]model.Unit{{ID: c.workspace + ":suite", Workspace: c.workspace, Selector: "full", Kind: "suite"}}, cases); err != nil {
		return nil, err
	}
	c.seal = c.requestDigest()
	return cases, c.Verify(ctx)
}
func (c *Closure) Digest() string {
	return digest([]any{ClosureVersion, c.apple.Digest(), c.args, c.settings, c.generated, c.cases})
}
func (c *Closure) ReadResult(ctx context.Context, bundle string) (native.Result, error) {
	if err := c.Verify(ctx); err != nil {
		return native.Result{}, err
	}
	rel, pathErr := filepath.Rel(c.dir, bundle)
	if !filepath.IsAbs(bundle) || pathErr != nil || !config.Relative(filepath.ToSlash(rel)) || rel == "." {
		return native.Result{}, errors.New("Xcode result escapes private output context")
	}
	version, err := c.apple.Run(ctx, c.tool, []string{"-version"})
	if err != nil {
		return native.Result{}, errors.New("Xcode version unavailable")
	}
	schema, err := resultSchema(version.Stdout)
	if err != nil {
		return native.Result{}, err
	}
	var data [][]byte
	for _, kind := range []string{"tests", "summary"} {
		// xcresulttool lazily creates a database in the owned result bundle.
		// Bind that native reader generation; it cannot promote readonly inputs.
		out, err := c.apple.RunProducer(ctx, c.resultTool, []string{"get", "test-results", kind, "--schema-version", schema, "--path", bundle, "--compact"}, []string{bundle})
		if err != nil || !out.Completed {
			return native.Result{}, errors.New("xcresulttool export failed (native diagnostics withheld)")
		}
		data = append(data, out.Stdout)
	}
	return native.ReconcileXCResult(data[0], data[1], c.cases)
}

// Describe exposes sanitized conservative ownership, never native qualification.
func Describe(ctx context.Context, root string, w model.Workspace, c model.Context) (e model.Evidence, err error) {
	e = model.Evidence{Adapter: "xcode", Workspace: w.ID, Version: ClosureVersion, Inputs: map[string][]string{}}
	closure, err := CaptureClosure(ctx, root, w, c)
	if err != nil {
		return e, err
	}
	defer func() {
		if closeErr := closure.Close(); closeErr != nil {
			err = closeErr
		}
	}()
	if _, err := closure.Collect(ctx); err != nil {
		return e, err
	}
	owner := w.ID + ":suite"
	e.Units = []model.Unit{{ID: owner, Workspace: w.ID, Selector: "full", Kind: "suite"}}
	e.Nodes = []string{owner}
	e.Effects = []model.Effect{{Owner: owner, Kind: "unknown"}}
	e.Gaps = []model.Gap{{Code: "runtime-unqualified", Workspace: w.ID, Detail: "Xcode settings, testplans and independent XCTest enumeration do not prove command-specific SDK selection (--sdk/-sdk), secondary tool dispatch, SDK/framework bytes, macros/plugins, Swift Testing, dynamic collection, device/simulator state or complete native influence"}, {Code: "apple-private-build-context", Workspace: w.ID, Detail: "Private DerivedData, reader-created result databases and mixed compile/test same-content metadata generations change observed context; incremental producer/writer attribution and runtime metadata equivalence are unqualified"}}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		e.Inputs[filepath.ToSlash(rel)] = []string{owner}
		return nil
	})
	e.Version += ":" + closure.Digest()
	if err != nil {
		return e, err
	}
	return e, closure.Verify(ctx)
}
func resultSchema(version []byte) (string, error) {
	text := string(version)
	if strings.HasPrefix(text, "Xcode 26.3\n") {
		return "0.1.0", nil
	}
	if strings.HasPrefix(text, "Xcode 27.0\n") {
		return "0.4.0", nil
	}
	return "", errors.New("Xcode result reconciliation requires installed Xcode26.3 or27.0")
}

func (c *Closure) requestDigest() string {
	return digest([]any{c.tool, c.resultTool, c.dir, c.derived, c.cleanup, c.args, c.settings, c.generated, c.cases, c.workspace, c.InventoryComplete, c.products})
}

func (c *Closure) Inventory(ctx context.Context) (model.ExecutionInventory, error) {
	if err := c.Verify(ctx); err != nil {
		return model.ExecutionInventory{}, err
	}
	return apple.AppleInventory([]model.Unit{{ID: c.workspace + ":suite", Workspace: c.workspace, Selector: "full", Kind: "suite"}}, c.cases)
}

func (c *Closure) Run(ctx context.Context, args []string) (process.Output, error) {
	if err := c.Verify(ctx); err != nil {
		return process.Output{}, err
	}
	out, err := c.apple.RunProducer(ctx, c.tool, args, []string{c.derived})
	if !out.Completed {
		return out, err
	}
	c.generated = c.apple.TreeDigest(c.derived)
	c.seal = c.requestDigest()
	if verifyErr := c.Verify(ctx); verifyErr != nil {
		out.Completed = false
		return out, verifyErr
	}
	return out, err
}
