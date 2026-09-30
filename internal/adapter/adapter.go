// Package adapter dispatches evidence providers without ecosystem logic in the planner.
package adapter

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"

	"github.com/Cyberlane/hayaku/internal/adapter/cargo"
	"github.com/Cyberlane/hayaku/internal/adapter/golang"
	"github.com/Cyberlane/hayaku/internal/adapter/pytest"
	"github.com/Cyberlane/hayaku/internal/adapter/vitest"
	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/model"
)

type Capability struct {
	ID                 string `json:"id"`
	Tool               string `json:"tool"`
	NativeDiscovery    bool   `json:"native_discovery"`
	Granularity        string `json:"granularity"`
	ProductionOmission bool   `json:"production_omission"`
}

func Capabilities() []Capability {
	return []Capability{
		{ID: "go", Tool: "go", NativeDiscovery: true, Granularity: "package"},
		{ID: "vitest", Tool: "vitest", NativeDiscovery: true, Granularity: "file/project"},
		{ID: "bazel", Tool: "bazel", Granularity: "suite"},
		{ID: "pytest", Tool: "python3", NativeDiscovery: true, Granularity: "file"},
		{ID: "cargo", Tool: "cargo", NativeDiscovery: true, Granularity: "package"},
		{ID: "maven", Tool: "mvn", Granularity: "suite"},
		{ID: "gradle", Tool: "gradle", Granularity: "suite"},
		{ID: "sbt", Tool: "sbt", Granularity: "suite"},
		{ID: "dotnet", Tool: "dotnet", Granularity: "suite"},
		{ID: "xcode", Tool: "xcodebuild", Granularity: "suite"},
		{ID: "swift", Tool: "swift", Granularity: "suite"},
		{ID: "command", Granularity: "suite"},
	}
}

func Discover(ctx context.Context, root string, w model.Workspace, c model.Context) (model.Evidence, error) {
	switch w.Adapter {
	case "go":
		return golang.Discover(ctx, root, w, c)
	case "cargo":
		return cargo.Discover(ctx, root, w, c)
	case "pytest":
		return pytest.Discover(ctx, root, w, c)
	case "vitest":
		return vitest.Discover(ctx, root, w, c)
	}
	for _, capability := range Capabilities() {
		if capability.ID == w.Adapter {
			return fullSuite(ctx, root, w)
		}
	}
	return model.Evidence{}, fmt.Errorf("unknown adapter %q: use command for a full-run integration", w.Adapter)
}

// fullSuite is a genuine coarse fallback, not a claim that native collection was
// performed. The original configured command remains the execution authority.
func fullSuite(ctx context.Context, root string, w model.Workspace) (model.Evidence, error) {
	id := w.ID + ":suite"
	e := model.Evidence{Adapter: w.Adapter, Version: "full-suite-v1", Workspace: w.ID, Nodes: []string{id}, Inputs: map[string][]string{}, Edges: []model.Edge{}, Units: []model.Unit{{ID: id, Workspace: w.ID, Selector: "suite", Kind: "full-suite"}}, Gaps: []model.Gap{{Code: "adapter-unqualified", Workspace: w.ID, Detail: "Native dependency, discovery and result semantics are not implemented for this adapter; retain original full suite"}}}
	err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(w.Root)), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("unsupported snapshot input type")
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !config.Relative(rel) {
			return fmt.Errorf("unsafe input path")
		}
		if len(e.Inputs) >= 50000 {
			return fmt.Errorf("adapter input limit exceeded")
		}
		e.Inputs[rel] = []string{id}
		return nil
	})
	return e, err
}

func Proposal(w model.Workspace, units []model.Unit) (model.Command, error) {
	var cmd model.Command
	var err error
	switch w.Adapter {
	case "go":
		cmd, err = golang.Proposal(w, units)
	case "cargo":
		cmd, err = cargo.Proposal(w, units)
	case "pytest":
		cmd, err = pytest.Proposal(w, units)
	case "vitest":
		cmd, err = vitest.Proposal(w, units)
	default:
		if len(units) == 0 {
			return model.Command{}, fmt.Errorf("empty full-suite proposal")
		}
		cmd = w.Command
	}
	cmd.Dir = filepath.ToSlash(filepath.Join(w.Root, cmd.Dir))
	return cmd, err
}

func Normalize(e *model.Evidence) {
	sort.Strings(e.Nodes)
	sort.Slice(e.Units, func(i, j int) bool { return e.Units[i].ID < e.Units[j].ID })
	for input, owners := range e.Inputs {
		sort.Strings(owners)
		e.Inputs[input] = owners
	}
}

func ImplementationVersions() map[string]string {
	return map[string]string{"go": "go-native-v1", "cargo": "cargo-native-v1", "pytest": "pytest-native-v1", "vitest": "vitest-native-v1", "fallback": "full-suite-v1"}
}
