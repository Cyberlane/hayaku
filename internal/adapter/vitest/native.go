package vitest

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
)

//go:embed bridge.mjs
var nativeBridge string

type graphScope struct {
	File         string   `json:"file"`
	Project      string   `json:"projectName"`
	Dependencies []string `json:"dependencies"`
	Incomplete   bool     `json:"incomplete"`
}
type graphInventory struct {
	Schema  int          `json:"schema"`
	Version string       `json:"version"`
	Scopes  []graphScope `json:"scopes"`
	Global  []string     `json:"global"`
}

// Execute runs a bound installed native runtime. Nil units preserves the full
// configured suite; selected units are observational whole-file proposals.
func Execute(ctx context.Context, root string, w model.Workspace, c model.Context, units []model.Unit) (process.Output, error) {
	if _, _, err := parse(w); err != nil {
		return process.Output{}, err
	}
	var selected []string
	if units != nil {
		if len(units) == 0 {
			return process.Output{}, errors.New("empty Vitest execution inventory")
		}
		for _, unit := range units {
			if unit.Workspace != w.ID || unit.Kind != "vitest-file" {
				return process.Output{}, errors.New("invalid Vitest execution unit")
			}
			selected = append(selected, unit.ID)
		}
	}
	return bridge(ctx, root, w, c, "run", selected)
}
func bridge(ctx context.Context, root string, w model.Workspace, c model.Context, mode string, selected []string) (process.Output, error) {
	if w.NodeRuntime == nil || !filepath.IsAbs(w.NodeRuntime.Node) || !filepath.IsAbs(w.NodeRuntime.Modules) {
		return process.Output{}, errors.New("Vitest native API requires bound absolute Node and modules paths")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return process.Output{}, err
	}
	cwd := filepath.Join(root, filepath.FromSlash(w.Root), filepath.FromSlash(w.Command.Dir))
	if !within(root, cwd) {
		return process.Output{}, errors.New("Vitest cwd escapes snapshot")
	}
	modules := w.NodeRuntime.Modules
	// Immutable snapshots materialize dependencies at their configured workspace cwd.
	if info, err := os.Stat(filepath.Join(cwd, "node_modules", "vitest", "package.json")); err == nil && info.Mode().IsRegular() {
		modules = filepath.Join(cwd, "node_modules")
	}
	request := struct {
		Root, Cwd, Modules, Workspace, Mode string
		Args, Selected                      []string
	}{root, cwd, modules, w.ID, mode, w.Command.Args, selected}
	// Explicit lower-case keys keep the embedded protocol independent of Go field names.
	payload, err := json.Marshal(map[string]any{"root": request.Root, "cwd": request.Cwd, "modules": request.Modules, "workspace": request.Workspace, "mode": request.Mode, "args": request.Args, "selected": request.Selected})
	if err != nil {
		return process.Output{}, err
	}
	dir, err := os.MkdirTemp("", "hayaku-vitest-bridge-")
	if err != nil {
		return process.Output{}, err
	}
	defer os.RemoveAll(dir)
	script := filepath.Join(dir, "bridge.mjs")
	if err = os.WriteFile(script, []byte(nativeBridge), 0600); err != nil {
		return process.Output{}, err
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	env := map[string]string{}
	for key, value := range c.Env {
		env[key] = value
	}
	env["CI"] = "true"
	return process.Run(bounded, model.Command{Executable: w.NodeRuntime.Node, Args: []string{script, string(payload)}}, cwd, env)
}

func discoverBound(ctx context.Context, root string, w model.Workspace, c model.Context, e model.Evidence) (model.Evidence, error) {
	out, err := bridge(ctx, root, w, c, "discover", nil)
	if err != nil {
		return e, errors.New("bound Vitest native graph discovery failed")
	}
	var inventory graphInventory
	if err = json.Unmarshal(out.Stdout, &inventory); err != nil || inventory.Schema != 1 || inventory.Version != "4.1.11" || len(inventory.Scopes) == 0 || len(inventory.Scopes) > 50000 {
		return e, errors.New("invalid Vitest graph inventory")
	}
	e.Version = "vitest-vite-graph-v1:" + inventory.Version
	e.Gaps = []model.Gap{{Code: "runtime-unqualified", Workspace: w.ID, Detail: "Configured Vite transforms provide experimental import influence only; dynamic imports, external resources and arbitrary runtime effects remain unqualified"}}
	global := map[string]bool{}
	for _, p := range inventory.Global {
		global[p] = true
	}
	owners := map[string][]string{}
	seen := map[string]bool{}
	for _, scope := range inventory.Scopes {
		if strings.Contains(scope.Project, ":") || strings.Contains(scope.File, ":") || !validRelative(scope.File) || !within(filepath.Join(root, w.Root, w.Command.Dir), filepath.Join(root, filepath.FromSlash(scope.File))) {
			return e, errors.New("Vitest graph file outside workspace")
		}
		id := w.ID + ":" + scope.Project + ":" + scope.File
		if seen[id] {
			return e, errors.New("duplicate Vitest graph scope")
		}
		seen[id] = true
		selector, err := filepath.Rel(filepath.Join(root, w.Root, w.Command.Dir), filepath.Join(root, filepath.FromSlash(scope.File)))
		if err != nil {
			return e, err
		}
		e.Nodes = append(e.Nodes, id)
		e.Units = append(e.Units, model.Unit{ID: id, Workspace: w.ID, Kind: "vitest-file", Selector: filepath.ToSlash(selector)})
		owners[scope.File] = append(owners[scope.File], id)
		for _, p := range scope.Dependencies {
			if !validRelative(p) {
				return e, errors.New("invalid Vitest graph dependency")
			}
			owners[p] = append(owners[p], id)
		}
		if scope.Incomplete {
			e.Gaps = append(e.Gaps, model.Gap{Code: "vite-graph-incomplete", Workspace: w.ID, Detail: "A configured transform or dependency could not be captured; proposal retains all tests"})
			global["*"] = true
		}
	}
	if err = workspaceInputs(ctx, root, w, &e); err != nil {
		return e, err
	}
	for p, ids := range owners {
		if _, ok := e.Inputs[p]; !ok {
			return e, errors.New("Vitest graph references missing input")
		}
		ext := strings.ToLower(filepath.Ext(p))
		if global["*"] || global[p] || (ext != ".js" && ext != ".mjs" && ext != ".cjs" && ext != ".ts" && ext != ".tsx" && ext != ".jsx" && ext != ".mts" && ext != ".cts") {
			continue
		}
		unique := map[string]bool{}
		for _, id := range ids {
			unique[id] = true
		}
		e.Inputs[p] = nil
		for id := range unique {
			e.Inputs[p] = append(e.Inputs[p], id)
		}
	}
	sortEvidence(&e)
	return e, nil
}
func validRelative(p string) bool {
	return p != "" && !filepath.IsAbs(p) && filepath.ToSlash(filepath.Clean(p)) == p && p != ".." && !strings.HasPrefix(p, "../") && !strings.Contains(p, "\\")
}
