package nextest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Cyberlane/hayaku/internal/adapter/cargo"
	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/runner"
	nativerunner "github.com/Cyberlane/hayaku/internal/runner/native"
)

// Closure retains configured nextest observations and the shared Cargo build
// context. Neither native listing nor artifact hashes prove runtime influence.
type Closure struct {
	Native    Inventory     `json:"native"`
	Cargo     cargo.Closure `json:"cargo"`
	workspace model.Workspace
	execution model.Context
}

func nativeContextDigest(w model.Workspace, execution model.Context) string {
	data, _ := json.Marshal([]any{w, execution})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func inventoryDigest(i Inventory) string {
	i.Digest = ""
	data, _ := json.Marshal(i)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (c *Closure) Verify(ctx context.Context) error {
	if c == nil || c.Native.Digest == "" || inventoryDigest(c.Native) != c.Native.Digest || nativeContextDigest(c.workspace, c.execution) != c.Native.ContextDigest {
		return errors.New("nextest inventory changed during collection")
	}
	return c.Cargo.Verify(ctx)
}

func (c *Closure) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	validation := c.Verify(ctx)
	cancel()
	if err := c.Cargo.Close(); err != nil {
		return err
	}
	return validation
}

// ResolveClosure asks nextest itself to compile/list the unchanged native
// filters. Cargo contributes metadata/context only; its test-list stage is not
// run. Unknown custom harness, native effects and producer inputs stay explicit.
func ResolveClosure(ctx context.Context, root string, w model.Workspace, execution model.Context) (c Closure, err error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	defer func() {
		if err != nil && ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	if err = ctx.Err(); err != nil {
		return c, err
	}
	if err = cargo.ValidateClosureRequest(w, execution); err != nil {
		return c, err
	}
	c.workspace, c.execution = w, execution
	originalContext := nativeContextDigest(w, execution)
	a, err := parse(w)
	if err != nil {
		return c, err
	}
	if execution.OS != "" && execution.OS != runtime.GOOS || execution.Arch != "" && execution.Arch != runtime.GOARCH {
		return c, errors.New("nextest native listing requires host context")
	}
	metadataWorkspace := w
	metadataWorkspace.Adapter = "cargo"
	metadataWorkspace.Command = model.Command{Dir: w.Command.Dir, Executable: w.Command.Executable, Args: append([]string{"test"}, a.cargo...)}
	c.Cargo, err = cargo.ResolveMetadata(ctx, root, metadataWorkspace, execution)
	if err != nil {
		return c, err
	}
	defer func() {
		if err != nil {
			_ = c.Cargo.Close()
		}
	}()
	tool, err := c.Cargo.CaptureTool(ctx, "cargo-nextest")
	if err != nil {
		return c, err
	}
	version, err := c.Cargo.RunExtension(ctx, "cargo-nextest", []string{"nextest", "--version"})
	if err != nil || !strings.HasPrefix(string(version.Stdout), "cargo-nextest "+NativeVersion+" ") {
		return c, errors.New("installed nextest version is unsupported or unavailable")
	}
	args := append([]string{"nextest", "list", "--message-format=json", "--offline", "--locked", "--target-dir", c.Cargo.PrivateTargetDirectory()}, a.list...)
	out, err := c.Cargo.RunExtension(ctx, "cargo-nextest", args)
	if err != nil || !out.Completed || out.ExitCode != 0 {
		return c, errors.New("offline locked nextest listing failed; diagnostics withheld")
	}
	c.Native = Inventory{Schema: 1, Version: NativeVersion, ToolchainDigest: tool, ContextDigest: originalContext, Binaries: []Binary{}}
	if err = c.consume(ctx, out.Stdout); err != nil {
		return c, err
	}
	c.Cargo.Gaps = append(c.Cargo.Gaps,
		model.Gap{Code: "nextest-runtime-unqualified", Workspace: w.ID, Detail: "Configured nextest artifacts and cases are descriptive; custom harnesses, native extensions, services, startup scripts and ambient configuration remain unqualified"},
		model.Gap{Code: "nextest-doctests-excluded", Workspace: w.ID, Detail: "Nextest excludes Cargo doctests; its configured inventory does not establish cargo-test or rustdoc completeness"})
	if err = c.Cargo.FinalizeObservations(ctx); err != nil {
		return c, err
	}
	c.Native.Execution = c.Cargo.Inventory
	c.Native.InventoryComplete = false
	c.Native.Digest = inventoryDigest(c.Native)
	err = c.Verify(ctx)
	return c, err
}

type nativeSuite struct {
	Package       string `json:"package-name"`
	PackageID     string `json:"package-id"`
	ID            string `json:"binary-id"`
	Name          string `json:"binary-name"`
	Kind          string `json:"kind"`
	BinaryPath    string `json:"binary-path"`
	BuildPlatform string `json:"build-platform"`
	Cwd           string `json:"cwd"`
	Status        string `json:"status"`
	Cases         map[string]struct {
		Ignored bool `json:"ignored"`
		Filter  struct {
			Status string `json:"status"`
		} `json:"filter-match"`
	} `json:"testcases"`
}

func (c *Closure) consume(ctx context.Context, data []byte) error {
	var raw map[string]json.RawMessage
	var payload struct {
		TestCount int                    `json:"test-count"`
		Suites    map[string]nativeSuite `json:"rust-suites"`
		Build     json.RawMessage        `json:"rust-build-meta"`
	}
	if len(data) > process.MaxOutput || nativerunner.DecodeJSON(data, &raw) != nil || json.Unmarshal(data, &payload) != nil || len(raw["test-count"]) == 0 || payload.TestCount < 0 || payload.TestCount > 100000 || len(payload.Suites) == 0 || len(payload.Suites) > 50000 || len(payload.Build) == 0 {
		return errors.New("invalid nextest native list inventory")
	}
	if err := c.captureBuildMetadata(ctx, payload.Build); err != nil {
		return err
	}
	selected := map[string]model.Unit{}
	for _, unit := range c.Cargo.Scopes {
		unit.Kind = "nextest-package"
		selected[unit.ID] = unit
	}
	count := 0
	caseOwners := map[string]bool{}
	for key, suite := range payload.Suites {
		if key != suite.ID || !identity(suite.ID) || !identity(suite.Name) || !identity(suite.Kind) || suite.Cases == nil || suite.Status != "listed" || suite.BuildPlatform != "host" && suite.BuildPlatform != "target" {
			return errors.New("invalid nextest binary identity")
		}
		owner, err := c.Cargo.NativeOwner(suite.PackageID, suite.Package, suite.Name, suite.Kind, suite.Cwd)
		if err != nil || selected[owner].ID == "" {
			return errors.New("nextest binary does not match configured Cargo package/target")
		}
		sum, err := c.Cargo.CaptureArtifact(ctx, suite.BinaryPath)
		if err != nil {
			return err
		}
		binary := Binary{Owner: owner, ArtifactDigest: sum, BuildPlatform: suite.BuildPlatform, ID: suite.ID, Package: suite.Package, Name: suite.Name, Kind: suite.Kind, Cases: []Case{}}
		artifact := cargo.ClosureArtifact{Owner: owner, Name: suite.ID, Kind: "nextest-test-binary", Digest: sum}
		for name, test := range suite.Cases {
			count++
			if count > 100000 || !identity(name) || test.Filter.Status != "matches" && test.Filter.Status != "mismatch" {
				return errors.New("invalid nextest native case/filter identity")
			}
			binary.Cases = append(binary.Cases, Case{Name: name, Ignored: test.Ignored, Selected: test.Filter.Status == "matches"})
			artifact.Cases = append(artifact.Cases, name)
			if test.Filter.Status == "matches" {
				caseOwners[owner] = true
				c.Cargo.Inventory.Cases = append(c.Cargo.Inventory.Cases, model.Case{Unit: owner, ID: nativerunner.JUnitID(suite.ID, name)})
			}
		}
		sort.Slice(binary.Cases, func(i, j int) bool { return binary.Cases[i].Name < binary.Cases[j].Name })
		sort.Strings(artifact.Cases)
		c.Native.Binaries = append(c.Native.Binaries, binary)
		c.Cargo.Artifacts = append(c.Cargo.Artifacts, artifact)
	}
	if count != payload.TestCount {
		return errors.New("incomplete nextest native case count")
	}
	for _, unit := range selected {
		if caseOwners[unit.ID] {
			c.Cargo.Inventory.Units = append(c.Cargo.Inventory.Units, unit)
		}
	}
	if len(c.Cargo.Inventory.Units) > 0 && runner.ValidateInventory(c.Cargo.Inventory) != nil {
		return errors.New("invalid shared nextest case inventory")
	}
	sort.Slice(c.Native.Binaries, func(i, j int) bool { return c.Native.Binaries[i].ID < c.Native.Binaries[j].ID })
	return nil
}

func (c *Closure) captureBuildMetadata(ctx context.Context, raw json.RawMessage) error {
	var build struct {
		Target    string                     `json:"target-directory"`
		Directory string                     `json:"build-directory"`
		Base      []string                   `json:"base-output-directories"`
		Linked    []string                   `json:"linked-paths"`
		OutDirs   map[string]string          `json:"build-script-out-dirs"`
		Info      map[string]json.RawMessage `json:"build-script-info"`
		NonTest   map[string][]struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
			Path string `json:"path"`
		} `json:"non-test-binaries"`
	}
	if json.Unmarshal(raw, &build) != nil || filepath.Clean(build.Target) != c.Cargo.PrivateTargetDirectory() || filepath.Clean(build.Directory) != c.Cargo.PrivateTargetDirectory() || len(build.Base)+len(build.Linked)+len(build.OutDirs)+len(build.NonTest)+len(build.Info) > 50000 {
		return errors.New("invalid nextest build context")
	}
	for _, name := range append(append([]string(nil), build.Base...), build.Linked...) {
		if !config.Relative(name) {
			return errors.New("nextest build context escapes private target")
		}
	}
	count := 0
	for packageID, artifacts := range build.NonTest {
		owner := c.Cargo.PackageOwner(packageID)
		if owner == "" {
			return errors.New("nextest non-test artifact owner unavailable")
		}
		for _, artifact := range artifacts {
			count++
			if count > 50000 || !identity(artifact.Name) || !identity(artifact.Kind) || !config.Relative(artifact.Path) {
				return errors.New("invalid nextest non-test artifact")
			}
			sum, err := c.Cargo.CaptureArtifact(ctx, filepath.Join(build.Target, filepath.FromSlash(artifact.Path)))
			if err != nil {
				return err
			}
			c.Cargo.Artifacts = append(c.Cargo.Artifacts, cargo.ClosureArtifact{Owner: owner, Name: artifact.Name, Kind: "nextest-non-test-" + artifact.Kind, Digest: sum})
		}
	}
	for packageID, name := range build.OutDirs {
		owner := c.Cargo.PackageOwner(packageID)
		if owner == "" || !config.Relative(name) {
			return errors.New("invalid nextest producer owner/path")
		}
		record, _ := json.Marshal([]any{packageID, name, build.Info[packageID]})
		if _, err := c.Cargo.CaptureProducer(ctx, owner, filepath.Join(build.Target, filepath.FromSlash(name)), record); err != nil {
			return err
		}
	}
	for packageID := range build.Info {
		if c.Cargo.PackageOwner(packageID) == "" || build.OutDirs[packageID] == "" {
			return errors.New("unbound nextest producer context")
		}
	}
	sum := sha256.Sum256(raw)
	c.Native.BuildMetadataDigest = hex.EncodeToString(sum[:])
	return nil
}

func collect(ctx context.Context, root string, w model.Workspace, execution model.Context, _ arguments) (inventory Inventory, evidence model.Evidence, err error) {
	c, err := ResolveClosure(ctx, root, w, execution)
	if err != nil {
		return inventory, evidence, err
	}
	defer func() {
		if closeErr := c.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	return c.Native, c.Evidence(), nil
}

func (c *Closure) Evidence() model.Evidence {
	e := c.Cargo.Evidence()
	e.Adapter, e.Version = "nextest", Version+";nextest="+c.Native.Version+";closure="+c.Cargo.Digest+";native="+c.Native.Digest
	for i := range e.Units {
		e.Units[i].Kind = "nextest-package"
	}
	return e
}

func Discover(ctx context.Context, root string, w model.Workspace, execution model.Context) (model.Evidence, error) {
	a, err := parse(w)
	if err != nil {
		return model.Evidence{}, err
	}
	_, evidence, err := collect(ctx, root, w, execution, a)
	return evidence, err
}
