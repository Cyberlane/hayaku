package app

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/noderuntime"
	"github.com/Cyberlane/hayaku/internal/snapshot"
)

func validateVitestRunner(root string, w model.Workspace) error {
	name := w.Command.Executable
	if !filepath.IsAbs(name) {
		if strings.ContainsAny(name, "/\\") {
			name = filepath.Join(root, w.Root, w.Command.Dir, name)
		} else {
			var err error
			name, err = exec.LookPath(name)
			if err != nil {
				return errors.New("configured Vitest runner is unavailable")
			}
		}
	}
	actual, err := filepath.EvalSymlinks(name)
	if err != nil {
		return errors.New("configured Vitest runner is unavailable")
	}
	entry := filepath.Join("vitest", "vitest.mjs")
	switch w.Adapter {
	case "jest":
		entry = filepath.Join("jest", "bin", "jest.js")
	case "playwright":
		entry = filepath.Join("@playwright", "test", "cli.js")
	case "node-test":
		expected, err := filepath.EvalSymlinks(w.NodeRuntime.Node)
		if err != nil || actual != expected {
			return errors.New("configured Node runner differs from bound runtime")
		}
		return nil
	}
	expected, err := filepath.EvalSymlinks(filepath.Join(w.NodeRuntime.Modules, entry))
	if err != nil || actual != expected {
		return errors.New("configured Vitest runner differs from the bound installation")
	}
	return nil
}

func moduleDestination(w model.Workspace) string {
	return filepath.ToSlash(filepath.Join(w.Root, w.Command.Dir, "node_modules"))
}

func hasNodeRuntime(c model.Config) bool {
	for _, w := range c.Workspaces {
		if w.NodeRuntime != nil {
			return true
		}
	}
	return false
}

// checkoutModules permits an in-repository dependency tree only at the exact
// native cwd where its snapshot copy will be mounted. All other dirty inputs
// remain forbidden. Identities are checked separately before and after commands.
func checkoutModules(root string, c model.Config) ([]string, error) {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, w := range c.Workspaces {
		if w.NodeRuntime == nil {
			continue
		}
		modules, err := filepath.EvalSymlinks(w.NodeRuntime.Modules)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(root, modules)
		if err != nil {
			return nil, err
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if filepath.ToSlash(rel) != moduleDestination(w) {
			return nil, errors.New("in-repository modules must match the configured Vitest cwd")
		}
		paths = append(paths, filepath.ToSlash(rel))
	}
	return paths, nil
}

// prepareNodeRuntimes copies already installed modules. No install, fetch or
// link to a mutable external dependency tree takes place.
func prepareNodeRuntimes(ctx context.Context, root string, c model.Config) error {
	seen := map[string]string{}
	for _, w := range c.Workspaces {
		if w.NodeRuntime == nil {
			continue
		}
		name := moduleDestination(w)
		if !config.Relative(name) {
			return errors.New("unsafe runtime destination")
		}
		id, err := noderuntime.Identity(ctx, *w.NodeRuntime)
		if err != nil {
			return err
		}
		if prior, ok := seen[name]; ok {
			if prior != id.Digest {
				return errors.New("conflicting module runtimes share a native cwd")
			}
			continue
		}
		if err := noderuntime.CopyModules(ctx, *w.NodeRuntime, filepath.Join(root, filepath.FromSlash(name)), id); err != nil {
			return err
		}
		seen[name] = id.Digest
	}
	return nil
}

// executionTreeDigest binds source and copied dependency trees independently,
// allowing internal npm links without permitting source symlinks generally.
func executionTreeDigest(ctx context.Context, root string, c model.Config) (string, error) {
	modules := map[string]string{}
	var paths []string
	for _, w := range c.Workspaces {
		if w.NodeRuntime == nil {
			continue
		}
		name := moduleDestination(w)
		copy := *w.NodeRuntime
		copy.Modules = filepath.Join(root, filepath.FromSlash(name))
		id, err := noderuntime.Identity(ctx, copy)
		if err != nil {
			return "", err
		}
		if _, ok := modules[name]; !ok {
			paths = append(paths, name)
		}
		modules[name] = id.Digest
	}
	source, err := snapshot.DigestWithModules(ctx, root, paths)
	if err != nil {
		return "", err
	}
	return config.Digest(struct {
		Source  string
		Modules map[string]string
	}{source, modules}), nil
}

func treeValidator(ctx context.Context, root string, c model.Config, original string, also func() error) func() error {
	return func() error {
		if also != nil {
			if err := also(); err != nil {
				return err
			}
		}
		current, err := executionTreeDigest(ctx, root, c)
		if err != nil {
			return err
		}
		if current != original {
			return errors.New("native execution mutated source or installed runtime inputs")
		}
		return nil
	}
}
