package vitest

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/inputbundle"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/runner"
	"github.com/Cyberlane/hayaku/internal/snapshot"
)

const RuntimeCompilerProfile = "trusted-managed-compiler-v1"
const RuntimeProfile = "vitest-4.1.11-vite-8.2.2-rolldown-1.2.12-trusted-managed-compiler-v1-threads-v1"

//go:embed runtime.mjs
var runtimeInvoker []byte

// RuntimeRequest selects an explicit fixed profile, not an arbitrary configured
// Vitest command. Source qualification and enforced launch remain native duties.
type RuntimeRequest struct {
	Root, Modules, Workspace string
	Sources, Tests           []string
}

// PreparedRuntime owns frozen resources. Its identity is descriptive, never
// issuer authority. No host paths or temporary inode identities enter Digest.
type PreparedRuntime struct {
	mu                         sync.Mutex
	request                    RuntimeRequest
	source, modules, resources *inputbundle.Bundle
	directory, root            string
	owner                      *os.Root
	identity                   os.FileInfo
	digest                     string
	closed                     bool
	guards                     []runtimeGuard
}

type runtimeGuard struct {
	path string
	info os.FileInfo
	link string
	full bool
}

func validateRuntimeRequest(r RuntimeRequest) error {
	if r.Workspace == "" || len(r.Workspace) > 128 || strings.ContainsAny(r.Workspace, ":\\\x00\r\n") || len(r.Sources) == 0 || len(r.Sources) > 128 || len(r.Tests) == 0 || len(r.Tests) > len(r.Sources) {
		return errors.New("invalid restricted Vitest source scope")
	}
	for _, root := range []string{r.Root, r.Modules} {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root || strings.ContainsAny(root, "\x00\r\n") {
			return errors.New("restricted Vitest requires clean absolute source and module paths")
		}
	}
	seen := map[string]bool{}
	for _, name := range r.Sources {
		if len(name) > 4096 || !fs.ValidPath(name) || strings.ContainsAny(name, "\\:") || (!strings.HasSuffix(name, ".ts") && !strings.HasSuffix(name, ".js")) || seen[name] {
			return errors.New("invalid restricted Vitest source declaration")
		}
		seen[name] = true
	}
	tests := map[string]bool{}
	for _, name := range r.Tests {
		if !seen[name] || tests[name] {
			return errors.New("missing or repeated restricted Vitest test entry")
		}
		tests[name] = true
	}
	return nil
}

func sourceRequest(r RuntimeRequest, root string) inputbundle.Request {
	inputs := make([]inputbundle.Input, len(r.Sources))
	for i, name := range r.Sources {
		inputs[i] = inputbundle.Input{Root: "source", Path: name}
	}
	return inputbundle.Request{Roots: []inputbundle.Root{{Name: "source", Path: root, Mount: "."}}, Inputs: inputs,
		Limits: inputbundle.Limits{Entries: 1024, FileBytes: 256 << 10, Bytes: 8 << 20, Depth: 64}}
}

func PrepareRuntime(ctx context.Context, request RuntimeRequest) (*PreparedRuntime, error) {
	return prepareRuntime(ctx, request, nil)
}

func prepareRuntime(ctx context.Context, request RuntimeRequest, created func(*PreparedRuntime) error) (_ *PreparedRuntime, returnedErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateRuntimeRequest(request); err != nil {
		return nil, err
	}
	request.Sources, request.Tests = slices.Clone(request.Sources), slices.Clone(request.Tests)
	slices.Sort(request.Sources)
	slices.Sort(request.Tests)
	p := &PreparedRuntime{request: request}
	var err error
	p.directory, err = os.MkdirTemp("", "hayaku-vitest-runtime-")
	if err != nil {
		return nil, err
	}
	p.identity, err = os.Lstat(p.directory)
	if err != nil {
		return nil, err
	}
	defer func() {
		if returnedErr != nil {
			returnedErr = errors.Join(returnedErr, p.Close(), p.Abandon())
		}
	}()
	resolved, err := filepath.EvalSymlinks(p.directory)
	if err != nil {
		return nil, err
	}
	p.directory = resolved
	p.owner, err = os.OpenRoot(p.directory)
	if err != nil {
		return nil, err
	}
	opened, err := p.owner.Stat(".")
	if err != nil || !os.SameFile(p.identity, opened) || p.identity.Mode() != opened.Mode() {
		return nil, errors.Join(err, errors.New("restricted Vitest private owner changed during creation"))
	}
	if created != nil {
		if err = created(p); err != nil {
			return nil, err
		}
	}
	p.source, err = inputbundle.Capture(ctx, sourceRequest(request, request.Root))
	if err != nil {
		return nil, err
	}
	for _, e := range p.source.Manifest().Entries {
		if e.Kind == "symlink" {
			return nil, errors.New("restricted Vitest source aliases are unsupported")
		}
	}
	p.root = filepath.Join(p.directory, "resources")
	if err = p.source.Materialize(ctx, p.root); err != nil {
		return nil, err
	}
	p.modules, err = inputbundle.Capture(ctx, inputbundle.Request{Roots: []inputbundle.Root{{Name: "modules", Path: request.Modules, Mount: "."}}, Inputs: []inputbundle.Input{{Root: "modules", Path: "."}}, Limits: inputbundle.Limits{Bytes: 256 << 20}})
	if err != nil {
		return nil, err
	}
	if err = p.modules.Materialize(ctx, filepath.Join(p.root, "node_modules")); err != nil {
		return nil, err
	}
	for name, version := range map[string]string{"vitest": "4.1.11", "vite": "8.2.2", "rolldown": "1.2.12"} {
		filename := filepath.Join(p.root, "node_modules", name, "package.json")
		info, err := os.Lstat(filename)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return nil, errors.New("restricted Vitest package metadata is not bounded")
		}
		data, err := snapshot.ReadFile(ctx, filename, 1<<20)
		if err != nil || len(data) > 1<<20 {
			return nil, errors.New("restricted Vitest package metadata unavailable")
		}
		var pkg struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(data, &pkg) != nil || pkg.Version != version {
			return nil, errors.New("restricted Vitest module versions unsupported")
		}
	}
	controls := filepath.Join(p.root, ".hayaku")
	if err = os.Mkdir(controls, 0700); err != nil {
		return nil, err
	}
	for name, data := range map[string][]byte{"runtime.mjs": runtimeInvoker, "openssl.cnf": []byte("# Explicit restricted Vitest startup configuration\n")} {
		if err = os.WriteFile(filepath.Join(controls, name), data, 0600); err != nil {
			return nil, err
		}
	}
	if err = os.WriteFile(filepath.Join(p.root, "package.json"), []byte("{\"private\":true,\"type\":\"module\"}\n"), 0600); err != nil {
		return nil, err
	}
	p.resources, err = inputbundle.Capture(ctx, p.files())
	if err != nil {
		return nil, err
	}
	for _, group := range []struct {
		root     string
		bundle   *inputbundle.Bundle
		complete bool
	}{{request.Root, p.source, false}, {request.Modules, p.modules, true}, {p.root, p.resources, true}} {
		for _, entry := range group.bundle.Manifest().Entries {
			name := filepath.Join(group.root, filepath.FromSlash(entry.Path))
			info, err := os.Lstat(name)
			if err != nil {
				return nil, err
			}
			guard := runtimeGuard{path: name, info: info, full: group.complete || !info.IsDir()}
			if info.Mode()&os.ModeSymlink != 0 {
				guard.link, err = os.Readlink(name)
				if err != nil {
					return nil, err
				}
			}
			p.guards = append(p.guards, guard)
		}
	}
	p.digest = config.Digest(struct {
		Profile, Source, Modules, Resources string
		Tests                               []string
		Workspace                           string
	}{RuntimeProfile, p.source.Manifest().Digest, p.modules.Manifest().Digest, p.resources.Manifest().Digest, request.Tests, request.Workspace})
	if err = p.Verify(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *PreparedRuntime) files() inputbundle.Request {
	return inputbundle.Request{Roots: []inputbundle.Root{{Name: "resources", Path: p.root, Mount: "."}}, Inputs: []inputbundle.Input{{Root: "resources", Path: "."}}, Limits: inputbundle.Limits{Bytes: 272 << 20}}
}

// Files returns a fresh declaration for the native resource capture. The caller
// must retain this owner and independently verify it before and after each use.
func (p *PreparedRuntime) Files() inputbundle.Request { return p.files() }
func (p *PreparedRuntime) Digest() string             { return p.digest }
func (p *PreparedRuntime) ModulesDigest() string      { return p.modules.Manifest().Digest }
func (p *PreparedRuntime) Units() []model.Unit {
	result := make([]model.Unit, len(p.request.Tests))
	for i, name := range p.request.Tests {
		result[i] = model.Unit{ID: p.request.Workspace + "::" + name, Workspace: p.request.Workspace, Kind: "vitest-file", Selector: name}
	}
	return result
}

func (p *PreparedRuntime) Verify(ctx context.Context) error {
	if p == nil {
		return errors.New("restricted Vitest resources unavailable")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.owner == nil || p.resources == nil {
		return errors.New("restricted Vitest resources unavailable")
	}
	current, err := os.Lstat(p.directory)
	if err != nil || !os.SameFile(p.identity, current) || current.Mode() != p.identity.Mode() {
		return errors.New("restricted Vitest ownership changed")
	}
	for _, guard := range p.guards {
		current, err := os.Lstat(guard.path)
		if err != nil || !os.SameFile(guard.info, current) || guard.info.Mode() != current.Mode() || (guard.full && (guard.info.Size() != current.Size() || !guard.info.ModTime().Equal(current.ModTime()))) {
			return errors.New("restricted Vitest selected identity changed")
		}
		if guard.link != "" {
			link, err := os.Readlink(guard.path)
			if err != nil || link != guard.link {
				return errors.New("restricted Vitest selected alias changed")
			}
		}
	}
	copied, err := inputbundle.Capture(ctx, sourceRequest(p.request, p.root))
	if err != nil {
		return err
	}
	// Materialize deliberately normalizes its enclosing root to private0700;
	// original root mode remains bound by source.VerifySources independently.
	a, b := copied.Manifest(), p.source.Manifest()
	if len(a.Roots) != 1 || a.Roots[0].Mode != 0700 || !slices.Equal(a.Entries, b.Entries) || !slices.Equal(a.Inputs, b.Inputs) || a.Bytes != b.Bytes {
		return errors.Join(err, errors.New("restricted Vitest source copy changed"))
	}
	return errors.Join(p.source.VerifySources(ctx), p.modules.VerifySources(ctx), p.modules.VerifyMaterialization(ctx, filepath.Join(p.root, "node_modules")), p.resources.VerifySources(ctx))
}

// Invocation changes only explicit operation/selection, never framework setup.
func (p *PreparedRuntime) Invocation(ctx context.Context, mode string, selected []model.Unit, addon string) (args, env []string, err error) {
	if err = p.Verify(ctx); err != nil {
		return nil, nil, err
	}
	if mode != "collect" && mode != "run" {
		return nil, nil, errors.New("unsupported restricted Vitest operation")
	}
	if addon != "/runtime/#addon-rolldown.node" {
		return nil, nil, errors.New("unbound restricted Vitest addon role")
	}
	var chosen []string
	if selected != nil {
		if mode != "run" || len(selected) == 0 || len(selected) > len(p.request.Tests) {
			return nil, nil, errors.New("invalid restricted Vitest selected scope")
		}
		seen := map[string]bool{}
		for _, unit := range selected {
			if !slices.Contains(p.Units(), unit) || seen[unit.ID] {
				return nil, nil, errors.New("unexpected restricted Vitest selected unit")
			}
			seen[unit.ID] = true
			chosen = append(chosen, unit.ID)
		}
	}
	data, err := json.Marshal(struct {
		Schema    int      `json:"schema"`
		Profile   string   `json:"profile"`
		Compiler  string   `json:"compiler"`
		Workspace string   `json:"workspace"`
		Mode      string   `json:"mode"`
		Tests     []string `json:"tests"`
		Selected  []string `json:"selected"`
	}{1, RuntimeProfile, RuntimeCompilerProfile, p.request.Workspace, mode, p.request.Tests, chosen})
	if err != nil || len(data) > 65536 {
		return nil, nil, errors.New("restricted Vitest invocation exceeds bound")
	}
	return []string{"--openssl-config=/resources/.hayaku/openssl.cnf", "/resources/.hayaku/runtime.mjs", string(data)}, []string{"CI=true", "TMPDIR=/scratch", "NAPI_RS_NATIVE_LIBRARY_PATH=" + addon}, nil
}

func (p *PreparedRuntime) DecodeCollection(out process.Output) (model.ExecutionInventory, error) {
	e := model.Evidence{Version: "vitest-vite-graph-v1:4.1.11:vite:8.2.2", Units: p.Units()}
	return decodeCollection(out, p.request.Workspace, e)
}

func (p *PreparedRuntime) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	if p.owner == nil {
		if p.identity != nil && p.directory != "" {
			current, err := os.Lstat(p.directory)
			if err != nil || !os.SameFile(p.identity, current) || current.Mode() != p.identity.Mode() {
				return errors.Join(err, errors.New("restricted Vitest early cleanup ownership changed"))
			}
			if err = os.Remove(p.directory); err != nil {
				return err
			}
			p.directory = ""
		}
		return nil
	}
	err := p.owner.RemoveAll("resources")
	current, statErr := os.Lstat(p.directory)
	if statErr != nil || !os.SameFile(p.identity, current) || current.Mode() != p.identity.Mode() {
		return errors.Join(err, statErr, errors.New("restricted Vitest cleanup ownership changed"))
	}
	err = errors.Join(err, os.Remove(p.directory))
	if err == nil {
		err = p.owner.Close()
		p.owner = nil
		p.directory = ""
	}
	return err
}

// Abandon releases a failed creator's retained handle after guarded cleanup.
// It removes no files and leaves unknown siblings intact. Successful owners use
// Close instead, retaining its cleanup retry contract.
func (p *PreparedRuntime) Abandon() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	if p.owner == nil {
		return nil
	}
	err := p.owner.Close()
	p.owner = nil
	p.directory = ""
	return err
}

// MatchCases checks independently known source cases against native collection.
// This verifies identities, not runtime qualification or passing outcomes.
func MatchCases(inventory model.ExecutionInventory, expected model.ExecutionInventory) error {
	if runner.ValidateInventory(inventory) != nil || runner.ValidateInventory(expected) != nil {
		return errors.New("invalid restricted Vitest inventory")
	}
	a, _ := json.Marshal(inventory)
	b, _ := json.Marshal(expected)
	if !bytes.Equal(a, b) {
		return errors.New("restricted Vitest native cases differ from source cases")
	}
	return nil
}
