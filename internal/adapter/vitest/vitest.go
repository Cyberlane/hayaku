// Package vitest discovers native whole-file test scopes. Until a complete
// configured Vite influence graph is captured, all workspace files own all tests.
package vitest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
)

type fileScope struct {
	File    string `json:"file"`
	Project string `json:"projectName"`
}

// Discover invokes an already installed plain Vitest executable. No package
// manager, installer, related-tests heuristic or source parser is invoked.
// Configuration loading is executable native discovery and may have side effects.
func Discover(ctx context.Context, snapshotRoot string, w model.Workspace, c model.Context) (model.Evidence, error) {
	e := model.Evidence{Adapter: "vitest", Version: "vitest-files-v1", Workspace: w.ID, Inputs: map[string][]string{}, Gaps: []model.Gap{
		{Code: "runtime-unqualified", Workspace: w.ID, Detail: "Vitest file discovery does not establish complete configured runtime influence"},
		{Code: "dependency-graph-unavailable", Workspace: w.ID, Detail: "Configured Vite imports, aliases, transforms, setup and browser inputs are not captured; retain whole workspace"},
		{Code: "native-version-unqualified", Workspace: w.ID, Detail: "Vitest file-list protocol requires qualification against the installed version and project contexts"},
	}}
	if path, ok := c.Env["PATH"]; ok && path != os.Getenv("PATH") {
		return e, errors.New("different PATH overrides are unsupported; use an absolute native runner path")
	}
	flags, filters, err := parse(w)
	if err != nil {
		return e, err
	}
	root, err := filepath.EvalSymlinks(snapshotRoot)
	if err != nil {
		return e, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return e, err
	}
	dir := filepath.Join(root, filepath.FromSlash(w.Root), filepath.FromSlash(w.Command.Dir))
	if !within(root, dir) {
		return e, errors.New("Vitest cwd escapes snapshot")
	}
	timeout, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	env := map[string]string{}
	for k, v := range c.Env {
		env[k] = v
	}
	env["CI"] = "true"
	version, err := process.Run(timeout, model.Command{Executable: w.Command.Executable, Args: []string{"--version"}}, dir, env)
	if err != nil {
		return e, errors.New("installed Vitest runner unavailable")
	}
	e.Version += ":" + strings.TrimSpace(string(version.Stdout))
	args := append([]string{"list", "--filesOnly", "--json"}, flags...)
	args = append(args, filters...)
	out, err := process.Run(timeout, model.Command{Executable: w.Command.Executable, Args: args}, dir, env)
	if err != nil {
		return e, errors.New("native Vitest file discovery failed")
	}
	var scopes []fileScope
	if err = json.Unmarshal(out.Stdout, &scopes); err != nil || len(scopes) == 0 {
		return e, errors.New("invalid or empty Vitest file inventory")
	}
	if len(scopes) > 50000 {
		return e, errors.New("Vitest file inventory limit exceeded")
	}
	seen := map[string]bool{}
	for _, scope := range scopes {
		path := scope.File
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, filepath.FromSlash(path))
		}
		if !within(root, path) || !within(dir, path) {
			return e, errors.New("Vitest file lies outside configured snapshot cwd")
		}
		rel, _ := filepath.Rel(root, path)
		selector, _ := filepath.Rel(dir, path)
		id := w.ID + ":" + scope.Project + ":" + filepath.ToSlash(rel)
		if seen[id] {
			return e, errors.New("duplicate Vitest file scope")
		}
		seen[id] = true
		e.Nodes = append(e.Nodes, id)
		e.Units = append(e.Units, model.Unit{ID: id, Workspace: w.ID, Selector: filepath.ToSlash(selector), Kind: "vitest-file"})
	}
	sort.Strings(e.Nodes)
	sort.Slice(e.Units, func(i, j int) bool { return e.Units[i].ID < e.Units[j].ID })
	count := 0
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if timeout.Err() != nil {
			return timeout.Err()
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return errors.New("unsupported Vitest input type")
		}
		count++
		if count > 50000 {
			return errors.New("Vitest workspace input limit exceeded")
		}
		rel, _ := filepath.Rel(root, path)
		e.Inputs[filepath.ToSlash(rel)] = append([]string(nil), e.Nodes...)
		return nil
	})
	if err != nil {
		return e, err
	}
	for _, scope := range scopes {
		path := scope.File
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, filepath.FromSlash(path))
		}
		rel, _ := filepath.Rel(root, path)
		if _, ok := e.Inputs[filepath.ToSlash(rel)]; !ok {
			return e, errors.New("Vitest inventory references missing snapshot file")
		}
	}
	return e, nil
}
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return path != "" && err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func parse(w model.Workspace) (flags, filters []string, err error) {
	if (filepath.Base(w.Command.Executable) != "vitest" && filepath.Base(w.Command.Executable) != "vitest.cmd") || len(w.Command.Args) == 0 || w.Command.Args[0] != "run" {
		return nil, nil, errors.New("Vitest proposals require a plain vitest run command")
	}
	if len(w.Patterns) > 0 || len(w.BuildFlags) > 0 {
		return nil, nil, errors.New("Vitest configuration must be retained in the original command")
	}
	valued := map[string]bool{"--project": true, "--config": true, "--pool": true, "--environment": true, "--maxWorkers": true, "--minWorkers": true, "--testTimeout": true, "--hookTimeout": true, "--retry": true, "--reporter": true}
	booleans := map[string]bool{"--globals": true, "--isolate": true, "--no-isolate": true, "--fileParallelism": true, "--no-file-parallelism": true, "--coverage": true}
	for i := 1; i < len(w.Command.Args); i++ {
		arg := w.Command.Args[i]
		if !strings.HasPrefix(arg, "-") {
			if arg == "" || strings.Contains(arg, ":") {
				return nil, nil, errors.New("Vitest line or empty filters are unsupported")
			}
			filters = append(filters, arg)
			continue
		}
		name, _, has := strings.Cut(arg, "=")
		if !valued[name] && !booleans[name] {
			return nil, nil, fmt.Errorf("unsupported Vitest flag %q", name)
		}
		flags = append(flags, arg)
		if valued[name] && !has {
			i++
			if i >= len(w.Command.Args) {
				return nil, nil, errors.New("missing Vitest flag value")
			}
			flags = append(flags, w.Command.Args[i])
		}
	}
	return flags, filters, nil
}

// Proposal retains project/config/browser flags and selects complete files.
// File filters can match extra files; that broadening is intentional.
func Proposal(w model.Workspace, units []model.Unit) (model.Command, error) {
	flags, _, err := parse(w)
	if err != nil {
		return model.Command{}, err
	}
	names := map[string]bool{}
	for _, u := range units {
		if u.Workspace != w.ID || u.Kind != "vitest-file" || u.Selector == "" || strings.HasPrefix(u.Selector, "-") || strings.Contains(u.Selector, ":") {
			return model.Command{}, errors.New("invalid Vitest proposal unit")
		}
		names[u.Selector] = true
	}
	if len(names) == 0 {
		return model.Command{}, errors.New("empty Vitest proposal has no executable command")
	}
	selectors := []string{}
	for p := range names {
		selectors = append(selectors, p)
	}
	sort.Strings(selectors)
	command := w.Command
	command.Args = append([]string{"run"}, flags...)
	command.Args = append(command.Args, selectors...)
	return command, nil
}
func Execution(w model.Workspace) (model.Command, error) {
	if _, _, err := parse(w); err != nil {
		return model.Command{}, err
	}
	return w.Command, nil
}
