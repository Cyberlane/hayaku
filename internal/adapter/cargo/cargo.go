// Package cargo extracts configured native Cargo package metadata. It preserves
// whole-package test scopes and reports unknown runtime influence explicitly.
package cargo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

type metadata struct {
	Version         int      `json:"version"`
	WorkspaceRoot   string   `json:"workspace_root"`
	TargetDirectory string   `json:"target_directory"`
	Packages        []pkg    `json:"packages"`
	Members         []string `json:"workspace_members"`
	DefaultMembers  []string `json:"workspace_default_members"`
	Resolve         *struct {
		Root  *string `json:"root"`
		Nodes []struct {
			ID           string   `json:"id"`
			Dependencies []string `json:"dependencies"`
		} `json:"nodes"`
	} `json:"resolve"`
}
type pkg struct {
	ID, Name, Version string
	ManifestPath      string `json:"manifest_path"`
	Source            *string
	Targets           []struct {
		Kind       []string
		CrateTypes []string `json:"crate_types"`
	}
}
type arguments struct {
	preserved, metadata, packages, excludes []string
	workspace                               bool
}

// Discover uses the installed Cargo without downloading dependencies or
// toolchains, running build scripts, or modifying the lockfile.
func Discover(ctx context.Context, snapshotRoot string, w model.Workspace, c model.Context) (model.Evidence, error) {
	e := model.Evidence{Adapter: "cargo", Version: "cargo-native-v1", Workspace: w.ID, Inputs: map[string][]string{}, Gaps: []model.Gap{{Code: "runtime-unqualified", Workspace: w.ID, Detail: "Cargo metadata does not establish complete runtime, generated-input or external-service influence"}}}
	if path, ok := c.Env["PATH"]; ok && path != os.Getenv("PATH") {
		return e, errors.New("different PATH overrides are unsupported; use an absolute native runner path")
	}
	args, err := parse(w)
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
		return e, errors.New("Cargo workspace escapes snapshot")
	}
	env := map[string]string{}
	for k, v := range c.Env {
		env[k] = v
	}
	env["CARGO_NET_OFFLINE"] = "true"
	env["RUSTUP_AUTO_INSTALL"] = "0"
	timeout, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	version, err := process.Run(timeout, model.Command{Executable: w.Command.Executable, Args: []string{"--version"}}, dir, env)
	if err != nil {
		return e, errors.New("installed Cargo tool unavailable")
	}
	e.Version += ":" + strings.TrimSpace(string(version.Stdout))
	nativeArgs := append([]string{"metadata", "--offline", "--locked", "--format-version", "1"}, args.metadata...)
	out, err := process.Run(timeout, model.Command{Executable: w.Command.Executable, Args: nativeArgs}, dir, env)
	if err != nil {
		return e, errors.New("offline locked Cargo metadata discovery failed")
	}
	var m metadata
	if json.Unmarshal(out.Stdout, &m) != nil || m.Version != 1 || m.Resolve == nil || len(m.Packages) == 0 {
		return e, errors.New("incomplete Cargo metadata")
	}
	nodes, ids, local := map[string]bool{}, map[string]string{}, map[string]pkg{}
	for _, p := range m.Packages {
		if p.ID == "" || p.Name == "" || p.Version == "" {
			return e, errors.New("invalid Cargo package identity")
		}
		hash := sha256.Sum256([]byte(p.ID))
		id := w.ID + ":external:" + hex.EncodeToString(hash[:])
		if p.Source == nil {
			if !within(root, p.ManifestPath) {
				e.Gaps = append(e.Gaps, model.Gap{Code: "external-source", Workspace: w.ID, Detail: "A Cargo path dependency is outside the immutable snapshot"})
			} else {
				rel, _ := filepath.Rel(root, filepath.Dir(p.ManifestPath))
				id = w.ID + ":" + filepath.ToSlash(rel) + ":" + p.Name + "@" + p.Version
				local[p.ID] = p
			}
		}
		if _, ok := ids[p.ID]; ok {
			return e, errors.New("duplicate Cargo package identity")
		}
		ids[p.ID] = id
		nodes[id] = true
	}
	members := map[string]bool{}
	for _, p := range m.Members {
		if ids[p] == "" {
			return e, errors.New("unknown Cargo workspace member")
		}
		members[p] = true
	}
	selected := map[string]bool{}
	if args.workspace {
		for p := range members {
			selected[p] = true
		}
	} else if len(args.packages) == 0 {
		if m.Resolve.Root != nil {
			selected[*m.Resolve.Root] = true
		} else {
			for _, p := range m.DefaultMembers {
				selected[p] = true
			}
		}
	}
	for _, name := range args.packages {
		found := false
		for id, p := range local {
			if members[id] && p.Name == name {
				selected[id] = true
				found = true
			}
		}
		if !found {
			return e, errors.New("Cargo package selector cannot be reconciled with metadata")
		}
	}
	for _, name := range args.excludes {
		found := false
		for id, p := range local {
			if members[id] && p.Name == name {
				delete(selected, id)
				found = true
			}
		}
		if !found {
			return e, errors.New("unknown Cargo excluded package")
		}
	}
	for id := range selected {
		p, ok := local[id]
		if !ok || !members[id] {
			return e, errors.New("Cargo selected package is outside snapshot")
		}
		e.Units = append(e.Units, model.Unit{ID: ids[id], Workspace: w.ID, Selector: p.Name, Kind: "cargo-package"})
	}
	if len(e.Units) == 0 {
		return e, errors.New("Cargo discovery has no runnable package scope")
	}
	for _, n := range m.Resolve.Nodes {
		to, ok := ids[n.ID]
		if !ok {
			return e, errors.New("unknown Cargo resolver node")
		}
		for _, dep := range n.Dependencies {
			from, ok := ids[dep]
			if !ok {
				return e, errors.New("unknown Cargo dependency")
			}
			if from != to {
				e.Edges = append(e.Edges, model.Edge{From: from, To: to, Reason: "Cargo configured dependency"})
			}
		}
	}
	count := 0
	for opaque, p := range local {
		owner := ids[opaque]
		for _, target := range p.Targets {
			for _, kind := range append(append([]string(nil), target.Kind...), target.CrateTypes...) {
				if kind == "custom-build" || kind == "proc-macro" {
					e.Gaps = append(e.Gaps, model.Gap{Code: "generated-input-unqualified", Workspace: w.ID, Detail: "Cargo build scripts and procedural macros require a complete external-input model"})
					break
				}
			}
		}
		err := filepath.WalkDir(filepath.Dir(p.ManifestPath), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if timeout.Err() != nil {
				return timeout.Err()
			}
			if d.IsDir() {
				if path == m.TargetDirectory || d.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() {
				return errors.New("unsupported Cargo snapshot input type")
			}
			count++
			if count > 50000 {
				return errors.New("Cargo input limit exceeded")
			}
			rel, _ := filepath.Rel(root, path)
			e.Inputs[filepath.ToSlash(rel)] = append(e.Inputs[filepath.ToSlash(rel)], owner)
			return nil
		})
		if err != nil {
			return e, err
		}
	}
	// Definitions and toolchain/config inputs influence every selected package.
	visited := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		visited++
		if visited > 100000 {
			return errors.New("Cargo definition inventory limit exceeded")
		}
		if err != nil {
			return err
		}
		if timeout.Err() != nil {
			return timeout.Err()
		}
		if d.IsDir() {
			if path == m.TargetDirectory || d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if name != "Cargo.toml" && name != "Cargo.lock" && name != "rust-toolchain" && name != "rust-toolchain.toml" && !(filepath.Base(filepath.Dir(path)) == ".cargo" && (name == "config" || name == "config.toml")) {
			return nil
		}
		if !d.Type().IsRegular() {
			return errors.New("unsupported Cargo definition input type")
		}
		rel, _ := filepath.Rel(root, path)
		for _, u := range e.Units {
			e.Inputs[filepath.ToSlash(rel)] = append(e.Inputs[filepath.ToSlash(rel)], u.ID)
		}
		return nil
	})
	if err != nil {
		return e, err
	}
	for n := range nodes {
		e.Nodes = append(e.Nodes, n)
	}
	sort.Strings(e.Nodes)
	sort.Slice(e.Units, func(i, j int) bool { return e.Units[i].ID < e.Units[j].ID })
	sort.Slice(e.Edges, func(i, j int) bool {
		a, b := e.Edges[i], e.Edges[j]
		if a.From != b.From {
			return a.From < b.From
		}
		return a.To < b.To
	})
	for path, owners := range e.Inputs {
		sort.Strings(owners)
		unique := owners[:0]
		for _, id := range owners {
			if len(unique) == 0 || unique[len(unique)-1] != id {
				unique = append(unique, id)
			}
		}
		e.Inputs[path] = unique
	}
	sort.Slice(e.Gaps, func(i, j int) bool { return e.Gaps[i].Code < e.Gaps[j].Code })
	return e, nil
}
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return path != "" && err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func parse(w model.Workspace) (arguments, error) {
	a := arguments{}
	if (filepath.Base(w.Command.Executable) != "cargo" && filepath.Base(w.Command.Executable) != "cargo.exe") || len(w.Command.Args) == 0 || w.Command.Args[0] != "test" {
		return a, errors.New("Cargo proposals require a plain cargo test command")
	}
	if len(w.Patterns) > 0 {
		return a, errors.New("Cargo package scope must be encoded in the original command")
	}
	booleans := map[string]bool{"--workspace": true, "--all": true, "--no-default-features": true, "--all-features": true, "--offline": true, "--locked": true, "--frozen": true, "--release": true, "--no-fail-fast": true, "--quiet": true, "-q": true, "--verbose": true, "-v": true}
	valued := map[string]bool{"--package": true, "-p": true, "--exclude": true, "--features": true, "-F": true, "--target": true, "--profile": true, "--jobs": true, "-j": true}
	for i := 1; i < len(w.Command.Args); i++ {
		arg := w.Command.Args[i]
		name, value, has := strings.Cut(arg, "=")
		if !booleans[name] && !valued[name] {
			return a, fmt.Errorf("unsupported Cargo test argument %q", name)
		}
		item := []string{arg}
		if valued[name] && !has {
			i++
			if i >= len(w.Command.Args) {
				return a, errors.New("missing Cargo flag value")
			}
			value = w.Command.Args[i]
			item = append(item, value)
		}
		if booleans[name] && has {
			return a, errors.New("Cargo boolean flag cannot have a value")
		}
		switch name {
		case "--workspace", "--all":
			a.workspace = true
		case "--package", "-p":
			a.packages = append(a.packages, value)
		case "--exclude":
			a.excludes = append(a.excludes, value)
		default:
			a.preserved = append(a.preserved, item...)
		}
		switch name {
		case "--features", "-F", "--no-default-features", "--all-features", "--frozen":
			a.metadata = append(a.metadata, item...)
		case "--target":
			a.metadata = append(a.metadata, "--filter-platform", value)
		}
	}
	if len(a.excludes) > 0 && !a.workspace {
		return a, errors.New("Cargo exclusions require workspace scope")
	}
	if len(w.BuildFlags) > 0 && strings.Join(w.BuildFlags, "\x00") != strings.Join(a.metadata, "\x00") {
		return a, errors.New("Cargo discovery flags differ from original command")
	}
	return a, nil
}

// Proposal replaces only package-scope arguments; all test targets including
// unit, integration and documentation tests retain Cargo's configured defaults.
func Proposal(w model.Workspace, units []model.Unit) (model.Command, error) {
	args, err := parse(w)
	if err != nil {
		return model.Command{}, err
	}
	names := map[string]bool{}
	for _, u := range units {
		if u.Workspace != w.ID || u.Kind != "cargo-package" || u.Selector == "" || strings.HasPrefix(u.Selector, "-") {
			return model.Command{}, errors.New("invalid Cargo proposal unit")
		}
		names[u.Selector] = true
	}
	if len(names) == 0 {
		return model.Command{}, errors.New("empty Cargo proposal has no executable command")
	}
	selectors := []string{}
	for name := range names {
		selectors = append(selectors, name)
	}
	sort.Strings(selectors)
	command := w.Command
	command.Args = append([]string{"test"}, args.preserved...)
	for _, name := range selectors {
		command.Args = append(command.Args, "--package", name)
	}
	return command, nil
}
func Execution(w model.Workspace) (model.Command, error) {
	if _, err := parse(w); err != nil {
		return model.Command{}, err
	}
	return w.Command, nil
}
