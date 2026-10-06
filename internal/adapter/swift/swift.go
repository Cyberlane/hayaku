// Package swift collects installed SwiftPM target evidence and XCTest outcomes.
// Target proposals are diagnostic; native full suites remain required.
package swift

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/noderuntime"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/runner/native"
)

const Version = "swiftpm-xctest-v2-closure"

type target struct {
	Name         string   `json:"name"`
	C99Name      string   `json:"c99name"`
	Path         string   `json:"path"`
	Type         string   `json:"type"`
	Dependencies []string `json:"target_dependencies"`
	Products     []string `json:"product_dependencies"`
}
type description struct {
	Targets      []target          `json:"targets"`
	Dependencies []json.RawMessage `json:"dependencies"`
}

// dependencyIdentity deliberately excludes compiler caches and build products.
func dependencyIdentity(ctx context.Context, runtime *model.SwiftRuntime) (noderuntime.Fingerprint, error) {
	if runtime == nil {
		return noderuntime.Fingerprint{}, nil
	}
	var state struct {
		Object struct {
			Artifacts    []json.RawMessage `json:"artifacts"`
			Dependencies []struct {
				Package struct {
					Kind string `json:"kind"`
				} `json:"packageRef"`
				State struct {
					Name string `json:"name"`
				} `json:"state"`
				Subpath string `json:"subpath"`
			} `json:"dependencies"`
		} `json:"object"`
	}
	data, err := ReadAppleFile(ctx, filepath.Join(runtime.Dependencies, "workspace-state.json"), 1<<20)
	var raw map[string]json.RawMessage
	if err != nil || len(data) > 1<<20 || native.DecodeJSON(data, &raw) != nil || json.Unmarshal(data, &state) != nil || state.Object.Artifacts == nil || state.Object.Dependencies == nil || len(state.Object.Artifacts) != 0 {
		return noderuntime.Fingerprint{}, errors.New("unsupported provisioned SwiftPM dependency state")
	}
	for _, dep := range state.Object.Dependencies {
		if dep.Package.Kind != "remoteSourceControl" || dep.State.Name != "sourceControlCheckout" || !config.Relative(dep.Subpath) || dep.Subpath == "." {
			return noderuntime.Fingerprint{}, errors.New("SwiftPM dependencies require provisioned source-control checkouts")
		}
	}
	entries, err := os.ReadDir(runtime.Dependencies)
	if err != nil || len(entries) != 3 {
		return noderuntime.Fingerprint{}, errors.New("provision SwiftPM dependency state separately from build products")
	}
	for _, entry := range entries {
		if entry.Name() != "checkouts" && entry.Name() != "repositories" && entry.Name() != "workspace-state.json" {
			return noderuntime.Fingerprint{}, errors.New("unexpected provisioned SwiftPM input")
		}
	}
	// Reuse the installed-runtime copy boundary, which preserves internal links
	// without recursively traversing directory links (GRDB's test tree uses one).
	tool, err := os.Executable()
	if err != nil {
		return noderuntime.Fingerprint{}, err
	}
	return noderuntime.Identity(ctx, model.NodeRuntime{Node: tool, Modules: runtime.Dependencies})
}

func RuntimeIdentity(ctx context.Context, runtime *model.SwiftRuntime) (string, error) {
	f, err := dependencyIdentity(ctx, runtime)
	if err != nil {
		return "", err
	}
	return f.ModulesDigest, nil
}

func flags(w model.Workspace) ([]string, error) {
	if len(w.Prerequisites) != 0 || len(w.Command.Args) == 0 || w.Command.Args[0] != "test" {
		return nil, errors.New("Swift adapter requires plain swift test without prerequisites")
	}
	var out []string
	boolean := map[string]bool{"--disable-swift-testing": true, "--enable-xctest": true, "--no-parallel": true, "--enable-code-coverage": true, "--disable-code-coverage": true, "--disable-automatic-resolution": true, "--skip-update": true}
	value := map[string]bool{"--configuration": true, "-c": true, "--jobs": true, "-j": true, "--sanitize": true, "--build-system": true}
	for i := 1; i < len(w.Command.Args); i++ {
		arg := w.Command.Args[i]
		if boolean[arg] {
			out = append(out, arg)
			continue
		}
		if value[arg] && i+1 < len(w.Command.Args) {
			i++
			v := w.Command.Args[i]
			if v == "" || strings.HasPrefix(v, "-") || (arg == "--build-system" && v != "native" && v != "swiftbuild") {
				return nil, errors.New("invalid Swift test flag value")
			}
			out = append(out, arg, v)
			continue
		}
		return nil, fmt.Errorf("unsupported Swift test flag %q", arg)
	}
	// This reconciler covers XCTest only. Never silently discard another framework.
	if !contains(out, "--disable-swift-testing") {
		return nil, errors.New("Swift XCTest reconciliation requires explicit --disable-swift-testing; retain mixed-framework commands separately")
	}
	return out, nil
}
func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

type session struct {
	cleanup    string
	dir        string
	scratch    string
	base       []string
	env        map[string]string
	workspace  model.Workspace
	closure    *AppleClosure
	tool       string
	generated  string
	dependency string
}

func open(ctx context.Context, root string, w model.Workspace, c model.Context) (*session, error) {
	if err := preflightAppleRequest(w, c); err != nil {
		return nil, err
	}
	f, err := flags(w)
	if err != nil {
		return nil, err
	}
	sourceRoot := root
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	dir, err := filepath.EvalSymlinks(filepath.Join(root, w.Root, w.Command.Dir))
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || !config.Relative(filepath.ToSlash(rel)) {
		return nil, errors.New("Swift package escapes source snapshot")
	}
	s := &session{dir: dir, workspace: w, env: map[string]string{}}
	for k, v := range c.Env {
		s.env[k] = v
	}
	// Installed SCM checkouts can be copied locally; remote Git transports reject.
	s.env["GIT_CONFIG_COUNT"] = "2"
	s.env["GIT_CONFIG_KEY_0"] = "protocol.allow"
	s.env["GIT_CONFIG_VALUE_0"] = "never"
	s.env["GIT_CONFIG_KEY_1"] = "protocol.file.allow"
	s.env["GIT_CONFIG_VALUE_1"] = "always"
	s.env["GIT_TERMINAL_PROMPT"] = "0"
	fingerprint, err := dependencyIdentity(ctx, w.SwiftRuntime)
	if err != nil {
		s.close()
		return nil, err
	}
	s.dependency = fingerprint.ModulesDigest
	closureContext := c
	closureContext.Env = s.env
	s.closure, err = CaptureAppleClosure(ctx, sourceRoot, w, closureContext)
	if err != nil {
		s.close()
		return nil, err
	}
	tmp, err := s.closure.NewOutputRoot(ctx, "hayaku-swift-")
	if err != nil {
		return nil, err
	}
	s.scratch, s.cleanup = tmp, tmp
	build := filepath.Join(tmp, "build")
	if w.SwiftRuntime != nil {
		tool, err := os.Executable()
		if err != nil {
			s.close()
			return nil, err
		}
		if err := noderuntime.CopyModules(ctx, model.NodeRuntime{Node: tool, Modules: w.SwiftRuntime.Dependencies}, build, fingerprint); err != nil {
			s.close()
			return nil, err
		}
	}
	s.workspace = s.closure.workspace
	s.tool, err = s.closure.CaptureTool(ctx, w.Command.Executable)
	if err != nil {
		s.close()
		return nil, err
	}
	if runtime.GOOS == "darwin" && s.tool == "/usr/bin/swift" {
		selected, resolveErr := s.closure.SelectAppleTool(ctx, "swift")
		if resolveErr != nil {
			s.close()
			return nil, resolveErr
		}
		s.tool = selected
	}
	if runtime.GOOS == "darwin" {
		if err := s.closure.CaptureAppleToolchain(ctx); err != nil {
			s.close()
			return nil, err
		}
	}
	s.base = []string{"--package-path", dir, "--scratch-path", build, "--cache-path", filepath.Join(tmp, "cache"), "--config-path", filepath.Join(tmp, "configuration"), "--security-path", filepath.Join(tmp, "security"), "--disable-automatic-resolution", "--skip-update"}
	// Package subcommands and test subcommands share build-context flags.
	s.base = append(s.base, f...)
	if err := s.verify(ctx); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}
func (s *session) close() error {
	if err := os.RemoveAll(s.cleanup); err != nil {
		return errors.New("Swift private build cleanup failed")
	}
	return nil
}
func (s *session) invoke(ctx context.Context, command string, extra ...string) (process.Output, error) {
	args := append([]string{command}, s.base...)
	args = append(args, extra...)
	if err := s.verifyDependencies(ctx); err != nil {
		return process.Output{}, err
	}
	var out process.Output
	var err error
	if command == "test" || command == "build" {
		out, err = s.closure.RunProducer(ctx, s.tool, args, []string{filepath.Join(s.scratch, "build")})
	} else {
		out, err = s.closure.Run(ctx, s.tool, args)
	}
	if verifyErr := s.verifyDependencies(ctx); verifyErr != nil {
		s.closure.invalidate()
		out.Completed = false
		return out, verifyErr
	}
	return out, err
}

func Describe(ctx context.Context, root string, w model.Workspace, c model.Context) (e model.Evidence, err error) {
	e = model.Evidence{Adapter: "swift", Version: Version, Workspace: w.ID, Inputs: map[string][]string{}, Gaps: []model.Gap{{Code: "runtime-unqualified", Workspace: w.ID, Detail: "SwiftPM target metadata does not establish complete native runtime influence"}}}
	s, err := open(ctx, root, w, c)
	if err != nil {
		return e, err
	}
	defer func() {
		if closeErr := s.close(); closeErr != nil {
			err = closeErr
		}
	}()
	return describe(ctx, root, w, c, s)
}

func describe(ctx context.Context, root string, w model.Workspace, c model.Context, s *session) (e model.Evidence, err error) {
	e = model.Evidence{Adapter: "swift", Version: Version, Workspace: w.ID, Inputs: map[string][]string{}, Gaps: []model.Gap{{Code: "runtime-unqualified", Workspace: w.ID, Detail: "SwiftPM target metadata does not establish complete native runtime influence"}}}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return e, err
	}
	version, err := s.closure.Run(ctx, s.tool, []string{"--version"})
	if err != nil || runtime.GOOS != "darwin" || !regexp.MustCompile(`Apple Swift version 6\.[0-4](\D|$)`).Match(version.Stdout) {
		return e, errors.New("Swift XCTest adapter requires a supported installed Darwin Swift 6.0–6.4 toolchain")
	}
	label := regexp.MustCompile(`Apple Swift version 6\.[0-4](?:\.[0-9]+)?`).Find(version.Stdout)
	e.Version += ":" + string(label) + ":version-output:" + appleDigest(version.Stdout)
	// Test-only flags are not accepted by `swift package`.
	base := s.base
	s.base = base[:12]
	out, err := s.invoke(ctx, "package", "describe", "--type", "json")
	s.base = base
	if err != nil {
		return e, errors.New("SwiftPM package description failed (native diagnostics withheld)")
	}
	var raw map[string]json.RawMessage
	if err = native.DecodeJSON(out.Stdout, &raw); err != nil {
		return e, err
	}
	var d description
	if json.Unmarshal(out.Stdout, &d) != nil || len(d.Targets) == 0 || len(d.Targets) > 50000 {
		return e, errors.New("incomplete SwiftPM target description")
	}
	if len(d.Dependencies) > 0 && w.SwiftRuntime == nil {
		return e, errors.New("SwiftPM remote dependencies must be explicitly provisioned before planning")
	}
	nodes := map[string]bool{}
	targets := map[string]target{}
	id := func(name string) string { return w.ID + ":" + name }
	for _, t := range d.Targets {
		if !identifier(t.Name) || targets[t.Name].Name != "" || !config.Relative(t.Path) || t.Path == "." {
			return e, errors.New("invalid or duplicate SwiftPM target")
		}
		if t.Type == "test" && t.C99Name != "" && t.C99Name != t.Name {
			return e, errors.New("Swift XCTest module names must match target names")
		}
		if t.Type == "plugin" || t.Type == "binary" || t.Type == "macro" {
			e.Gaps = append(e.Gaps, model.Gap{Code: "swift-generated-producer-unqualified", Workspace: w.ID, Detail: "SwiftPM plugins, binary targets and macros have incomplete generated/native producer closure"})
		}
		nodes[id(t.Name)] = true
		targets[t.Name] = t
		if t.Type == "test" {
			e.Units = append(e.Units, model.Unit{ID: id(t.Name), Workspace: w.ID, Selector: t.Name, Kind: "swift-target"})
		}
	}
	if len(e.Units) == 0 {
		return e, errors.New("SwiftPM package has no test targets")
	}
	for _, t := range d.Targets {
		for _, dep := range t.Dependencies {
			if !identifier(dep) {
				return e, errors.New("invalid SwiftPM dependency")
			}
			nodes[id(dep)] = true
			e.Edges = append(e.Edges, model.Edge{From: id(dep), To: id(t.Name), Reason: "SwiftPM target dependency"})
			if targets[dep].Name == "" {
				e.Gaps = append(e.Gaps, model.Gap{Code: "swift-graph-gap", Workspace: w.ID, Detail: "Target dependency is absent from package description"})
			}
		}
		for _, dep := range t.Products {
			if !identifier(dep) {
				return e, errors.New("invalid SwiftPM product dependency")
			}
			nodes[id(dep)] = true
			e.Edges = append(e.Edges, model.Edge{From: id(dep), To: id(t.Name), Reason: "SwiftPM product dependency"})
		}
		if t.Type == "plugin" || t.Type == "binary" || t.Type == "macro" {
			continue
		}
		path := filepath.Join(s.dir, t.Path)
		if err := filepath.WalkDir(path, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			if !entry.Type().IsRegular() {
				return errors.New("unsupported Swift target input")
			}
			rel, err := filepath.Rel(root, path)
			if err != nil || !config.Relative(filepath.ToSlash(rel)) {
				return errors.New("Swift input escapes snapshot")
			}
			e.Inputs[filepath.ToSlash(rel)] = append(e.Inputs[filepath.ToSlash(rel)], id(t.Name))
			return nil
		}); err != nil {
			return e, err
		}
	}
	for _, name := range []string{"Package.swift", "Package.resolved"} {
		p := filepath.Join(s.dir, name)
		if _, err := os.Stat(p); err == nil {
			rel, _ := filepath.Rel(root, p)
			for _, u := range e.Units {
				e.Inputs[filepath.ToSlash(rel)] = append(e.Inputs[filepath.ToSlash(rel)], u.ID)
			}
		}
	}
	for n := range nodes {
		e.Nodes = append(e.Nodes, n)
	}
	sourceOwner := w.ID + ":source:closure"
	nodes[sourceOwner] = true
	for _, unit := range e.Units {
		e.Edges = append(e.Edges, model.Edge{From: sourceOwner, To: unit.ID, Reason: "Conservative complete source ownership"})
	}
	// Preserve precise target ownership while conservatively assigning every
	// otherwise unowned source/resource/manifest/testplan to the full scope.
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
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
		name := filepath.ToSlash(rel)
		if len(e.Inputs[name]) == 0 {
			e.Inputs[name] = []string{sourceOwner}
		}
		return nil
	}); err != nil {
		return e, err
	}
	for _, unit := range e.Units {
		e.Effects = append(e.Effects, model.Effect{Owner: unit.ID, Kind: "unknown"})
	}
	e.Gaps = append(e.Gaps, model.Gap{Code: "apple-mixed-producer-stage-unqualified", Workspace: w.ID, Detail: "Private mixed compile/test stages bind old/new outputs and same-content artifact metadata generations; SDK/module caches and recopy writes have no incremental producer, writer-attribution or runtime metadata equivalence proof"})
	e.Gaps = append(e.Gaps, model.Gap{Code: "apple-build-runtime-closure-unqualified", Workspace: w.ID, Detail: "Observed compiler, targets and source resources do not prove command-specific SDK selection (--sdk/-sdk), secondary tool dispatch, SDK/framework, plugins/macros, generated producers, Swift Testing, ambient context or complete native runtime influence"})
	e.Nodes = append(e.Nodes, sourceOwner)
	if err := boundAppleProjection(e, model.ExecutionInventory{}); err != nil {
		return e, err
	}
	e.Version += ":dependencies:" + s.dependency + ":closure:" + s.contextDigest() + ":description:" + appleDigest(raw)
	if err := s.verify(ctx); err != nil {
		return e, err
	}
	sort.Strings(e.Nodes)
	sort.Slice(e.Units, func(i, j int) bool { return e.Units[i].ID < e.Units[j].ID })
	return e, nil
}

func identifier(s string) bool {
	return s != "" && len(s) < 4096 && !strings.ContainsAny(s, "\x00\n\r:/\\")
}

func Proposal(w model.Workspace, units []model.Unit) (model.Command, error) {
	if _, err := flags(w); err != nil {
		return model.Command{}, err
	}
	var selectors []string
	for _, u := range units {
		if u.Workspace != w.ID || u.Kind != "swift-target" || !identifier(u.Selector) {
			return model.Command{}, errors.New("invalid Swift target proposal")
		}
		selectors = append(selectors, regexp.QuoteMeta(u.Selector))
	}
	if len(selectors) == 0 {
		return model.Command{}, errors.New("empty Swift proposal")
	}
	sort.Strings(selectors)
	cmd := w.Command
	cmd.Args = append(append([]string{}, cmd.Args...), "--filter", "^("+strings.Join(selectors, "|")+")\\.")
	return cmd, nil
}

// Collect uses the independently built XCTest listing, not result names.
func collect(ctx context.Context, s *session, units []model.Unit) ([]native.Case, error) {
	out, err := s.invoke(ctx, "test", "list")
	if err != nil {
		return nil, errors.New("Swift XCTest discovery failed (native diagnostics withheld)")
	}
	want := map[string]string{}
	for _, u := range units {
		want[u.Selector] = u.ID
	}
	seen := map[string]bool{}
	var cases []native.Case
	for _, line := range strings.Split(strings.TrimSpace(string(out.Stdout)), "\n") {
		line = strings.TrimSpace(line)
		class, name, ok := strings.Cut(line, "/")
		module, _, modOK := strings.Cut(class, ".")
		if !ok || !modOK || name == "" || !identifier(module) {
			return nil, errors.New("unsupported Swift XCTest listing")
		}
		unit := want[module]
		if unit == "" {
			continue
		}
		key := native.JUnitID(class, name)
		if seen[key] {
			return nil, errors.New("duplicate Swift XCTest identity")
		}
		seen[key] = true
		cases = append(cases, native.Case{Unit: unit, ID: key})
	}
	counts := map[string]int{}
	for _, c := range cases {
		counts[c.Unit]++
	}
	for _, u := range units {
		if counts[u.ID] == 0 {
			return nil, errors.New("Swift test target has no independently discovered XCTest cases")
		}
	}
	generated := filepath.Join(s.scratch, "build")
	if err = s.closure.CaptureArtifactTree(ctx, generated); err != nil {
		return nil, errors.New("Swift generated producer closure exceeds bounds or changed")
	}
	base := s.base
	filtered := []string{}
	for _, arg := range base {
		if arg != "--disable-swift-testing" && arg != "--enable-xctest" && arg != "--no-parallel" {
			filtered = append(filtered, arg)
		}
	}
	s.base = filtered
	bin, binErr := s.invoke(ctx, "build", "--show-bin-path")
	s.base = base
	if binErr != nil {
		return nil, errors.New("Swift typed product discovery failed")
	}
	products := strings.TrimSpace(string(bin.Stdout))
	rel, pathErr := filepath.Rel(s.scratch, products)
	if !filepath.IsAbs(products) || pathErr != nil || !config.Relative(filepath.ToSlash(rel)) {
		s.closure.invalidate()
		return nil, errors.New("Swift products escape private build context")
	}
	if err := s.closure.CaptureArtifactTree(ctx, products); err != nil {
		s.closure.invalidate()
		return nil, err
	}
	s.generated = s.closure.TreeDigest(generated)
	return cases, s.verify(ctx)
}

func Execute(ctx context.Context, root string, w model.Workspace, c model.Context, units []model.Unit, proposal bool) (out process.Output, result native.Result, err error) {
	start := time.Now()
	defer func() { out.Duration = time.Since(start) }()
	s, err := open(ctx, root, w, c)
	if err != nil {
		return process.Output{}, result, err
	}
	defer func() {
		if closeErr := s.close(); closeErr != nil {
			out.Completed = false
			err = closeErr
		}
	}()
	w = s.closure.workspace
	if len(units) > 50000 {
		return process.Output{}, result, errors.New("Swift unit inventory exceeds bounds")
	}
	units = append([]model.Unit(nil), units...)
	cases, err := collect(ctx, s, units)
	if err != nil {
		return process.Output{}, result, err
	}
	extra := []string{}
	if proposal {
		cmd, err := Proposal(w, units)
		if err != nil {
			return process.Output{}, result, err
		}
		extra = append(extra, cmd.Args[len(w.Command.Args):]...)
	}
	// invoke owns fresh dependency and full source/tool/artifact checks on
	// both sides of the requested native command.
	out, runErr := s.invoke(ctx, "test", extra...)
	if !out.Completed {
		if runErr != nil {
			return out, result, runErr
		}
		return out, result, errors.New("Swift test process did not complete")
	}
	result, err = native.ReconcileXCTest(bytes.NewReader(out.Stdout), cases)
	if err != nil {
		return out, result, err
	}
	if runErr != nil {
		return out, result, runErr
	}
	if result.Failed {
		return out, result, errors.New("Swift XCTest reports failure")
	}
	return out, result, nil
}

func (s *session) verify(ctx context.Context) error {
	if err := s.verifyDependencies(ctx); err != nil {
		return err
	}
	return s.closure.Verify(ctx)
}
func (s *session) verifyDependencies(ctx context.Context) error {
	current, err := RuntimeIdentity(ctx, s.workspace.SwiftRuntime)
	if err != nil || current != s.dependency {
		s.closure.invalidate()
		return errors.New("Swift provisioned dependency inputs changed")
	}
	return ctx.Err()
}

func (s *session) contextDigest() string {
	return appleDigest([]any{s.closure.Digest(), s.dir, s.scratch, s.cleanup, s.base, s.env, s.workspace, s.tool, s.generated, s.dependency})
}
