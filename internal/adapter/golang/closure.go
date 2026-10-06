package golang

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"go/ast"
	"go/doc"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/runner"
	"github.com/Cyberlane/hayaku/internal/snapshot"
)

const ClosureVersion = "go-compiler-closure-v1"

// Closure is compiler/source identity evidence, never a qualification proof.
// Inventory contains independently collected top-level cases. Dynamic cases,
// startup effects and scopes without cases retain explicit gaps.
type Closure struct {
	Version, Digest, SourceDigest, ToolchainDigest, ContextDigest string
	Packages                                                      []ClosurePackage
	Scopes                                                        []model.Unit
	Inventory                                                     model.ExecutionInventory
	InventoryComplete                                             bool
	Inputs                                                        []ClosureInput
	Edges                                                         []model.Edge
	Effects                                                       []model.Effect
	Gaps                                                          []model.Gap
	artifacts                                                     []closureArtifact
}

type closureArtifact struct {
	path, digest string
	limit        int64
}

type ClosurePackage struct {
	ID, ImportPath, ForTest, Directory, ExportDigest, SemanticDigest string
	Standard, Root, TestBinary                                       bool
	Imports, ActiveSources                                           []string
	// Private copies for a subsequent source certifier. Contents never serialize.
	Sources map[string][]byte `json:"-"`
}

type ClosureInput struct {
	Owner, Path, Kind, Digest string
}

type closureListed struct {
	listedPackage
	Standard  bool
	Export    string
	ImportMap map[string]string
}

func closureDigest(value any) string {
	if data, ok := value.([]byte); ok {
		sum := sha256.Sum256(data)
		return hex.EncodeToString(sum[:])
	}
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ResolveClosure operates on an immutable materialization. The installed tool
// resolves variants and exports offline; the importer may open only copied
// export bytes returned by that invocation, never ambient source paths.
func ResolveClosure(ctx context.Context, snapshotRoot string, w model.Workspace, execution model.Context) (Closure, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	c := Closure{Version: ClosureVersion, Inventory: model.ExecutionInventory{Schema: model.ExecutionSchema}}
	if w.ID == "" {
		return c, errors.New("Go closure requires a workspace identity")
	}
	flags, patterns, err := commandParts(w)
	if err != nil {
		return c, err
	}
	root, err := filepath.Abs(snapshotRoot)
	if err != nil {
		return c, errors.New("Go closure snapshot unavailable")
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return c, errors.New("Go closure snapshot unavailable")
	}
	c.SourceDigest, err = snapshot.Digest(ctx, root)
	if err != nil {
		return c, errors.New("Go closure requires a bounded immutable regular-file snapshot")
	}
	dir := filepath.Join(root, filepath.FromSlash(w.Root), filepath.FromSlash(w.Command.Dir))
	if !within(root, dir) {
		return c, errors.New("Go closure cwd escapes snapshot")
	}
	env, err := ExecutionEnvironment(ctx, root, w, execution)
	if err != nil {
		return c, err
	}
	flags, err = closureModuleFlags(ctx, root, dir, w, env, flags)
	if err != nil {
		return c, err
	}
	goTool, err := exec.LookPath(w.Command.Executable)
	if err != nil {
		return c, errors.New("installed Go compiler unavailable")
	}
	goTool, err = filepath.EvalSymlinks(goTool)
	if err != nil {
		return c, errors.New("installed Go compiler unavailable")
	}
	tool, err := closureRead(ctx, goTool, 256<<20)
	if err != nil {
		return c, err
	}
	c.artifacts = append(c.artifacts, closureArtifact{goTool, closureDigest(tool), 256 << 20})
	toolInfo, err := process.RunEnvironment(ctx, model.Command{Executable: goTool, Args: []string{"env", "-json", "GOTOOLDIR", "GOVERSION"}}, dir, env)
	if err != nil || !toolInfo.Completed {
		return c, errors.New("offline Go toolchain identity unavailable")
	}
	var tools struct{ GOTOOLDIR, GOVERSION string }
	if json.Unmarshal(toolInfo.Stdout, &tools) != nil || tools.GOTOOLDIR == "" || tools.GOVERSION == "" {
		return c, errors.New("invalid Go toolchain identity")
	}
	toolDigests := map[string]string{"go": closureDigest(tool), "version": tools.GOVERSION}
	for _, name := range []string{"compile", "asm", "link"} {
		filename := filepath.Join(tools.GOTOOLDIR, name)
		if strings.HasSuffix(goTool, ".exe") {
			filename += ".exe"
		}
		data, err := closureRead(ctx, filename, 256<<20)
		if err != nil {
			return c, err
		}
		toolDigests[name] = closureDigest(data)
		c.artifacts = append(c.artifacts, closureArtifact{filename, closureDigest(data), 256 << 20})
	}
	c.ToolchainDigest = closureDigest(toolDigests)
	c.ContextDigest = closureDigest(struct {
		Workspace     model.Workspace
		Execution     model.Context
		Environment   []string
		CompilerFlags []string
	}{w, execution, env, flags})
	args := append([]string{"list", "-deps", "-test", "-export", "-json"}, flags...)
	args = append(args, patterns...)
	output, err := process.RunEnvironmentLimits(ctx, model.Command{Executable: goTool, Args: args}, dir, env, maxOutput, 1<<20)
	if err != nil || !output.Completed || output.ExitCode != 0 {
		return c, errors.New("offline Go closure discovery/build failed; diagnostics withheld")
	}
	listed := map[string]closureListed{}
	decoder := json.NewDecoder(bytes.NewReader(output.Stdout))
	for {
		var p closureListed
		if err := decoder.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return c, errors.New("invalid Go closure metadata")
		}
		if p.ImportPath == "" || p.Incomplete || p.Error != nil || len(p.DepsErrors) != 0 || len(listed) >= 10000 {
			return c, errors.New("incomplete or oversized Go compiler closure")
		}
		if _, exists := listed[p.ImportPath]; exists {
			return c, errors.New("duplicate Go compiler package identity")
		}
		listed[p.ImportPath] = p
	}
	if len(listed) == 0 {
		return c, errors.New("empty Go compiler closure")
	}
	gapSet := map[string]bool{}
	gap := func(code, owner string) {
		key := code + "\x00" + owner
		if !gapSet[key] {
			c.Gaps = append(c.Gaps, model.Gap{Code: code, Workspace: w.ID, Detail: owner})
			gapSet[key] = true
		}
	}
	gap("runtime-unqualified", "Compiler and typed source identities do not establish a complete enforced runtime boundary")
	commandFlags, _, _, _ := parseCommand(w.Command)
	for i, argument := range commandFlags {
		name, value, hasValue := strings.Cut(strings.TrimPrefix(argument, "-"), "=")
		if !hasValue && (name == "count" || name == "cpu") && i+1 < len(commandFlags) {
			value = commandFlags[i+1]
		}
		if (name == "count" && value != "1") || (name == "cpu" && strings.Contains(value, ",")) {
			gap("execution-multiplicity-unqualified", w.ID)
		}
	}
	exports := map[string][]byte{}
	inputs := map[ClosureInput]bool{}
	var total int64
	addInput := func(owner, name, kind string) ([]byte, error) {
		if !within(root, name) {
			gap("external-source", owner)
			return nil, nil
		}
		data, err := closureRead(ctx, name, 16<<20)
		if err != nil {
			return nil, err
		}
		total += int64(len(data))
		if total > 256<<20 || len(inputs) >= 100000 {
			return nil, errors.New("Go closure source inputs exceed limit")
		}
		rel, _ := filepath.Rel(root, name)
		inputs[ClosureInput{Owner: owner, Path: filepath.ToSlash(rel), Kind: kind, Digest: closureDigest(data)}] = true
		return data, nil
	}
	ids := make([]string, 0, len(listed))
	for id := range listed {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	roots := map[string]model.Unit{}
	for _, id := range ids {
		p := listed[id]
		owner := w.ID + ":" + id
		cp := ClosurePackage{ID: owner, ImportPath: id, ForTest: p.ForTest, Standard: p.Standard, TestBinary: isTestBinary(p.listedPackage), Root: !p.DepOnly && p.ForTest == "" && !isTestBinary(p.listedPackage), Sources: map[string][]byte{}}
		if cp.Root {
			roots[id] = model.Unit{ID: owner, Workspace: w.ID, Selector: id, Kind: "go-package"}
		}
		if cp.TestBinary {
			gap("compiler-test-main-unqualified", owner)
		}
		for _, dep := range p.Imports {
			if mapped := p.ImportMap[dep]; mapped != "" {
				dep = mapped
			}
			if _, exists := listed[dep]; !exists && dep != "unsafe" {
				gap("unresolved-import", owner)
			}
			cp.Imports = append(cp.Imports, w.ID+":"+dep)
			c.Edges = append(c.Edges, model.Edge{From: w.ID + ":" + dep, To: owner, Reason: "Compiler-resolved Go import"})
		}
		if p.ForTest != "" {
			c.Edges = append(c.Edges, model.Edge{From: owner, To: w.ID + ":" + p.ForTest, Reason: "Compiler-resolved Go test variant"})
		}
		if p.Export != "" {
			data, err := closureRead(ctx, p.Export, 64<<20)
			if err != nil {
				return c, err
			}
			total += int64(len(data))
			if total > 256<<20 {
				return c, errors.New("Go closure exports exceed limit")
			}
			exports[id], cp.ExportDigest = data, closureDigest(data)
			c.artifacts = append(c.artifacts, closureArtifact{p.Export, cp.ExportDigest, 64 << 20})
		}
		if !within(root, p.Dir) {
			if p.Standard {
				gap("standard-library-runtime-unqualified", owner)
			} else {
				gap("external-source", owner)
			}
			c.Packages = append(c.Packages, cp)
			continue
		}
		rel, _ := filepath.Rel(root, p.Dir)
		cp.Directory = filepath.ToSlash(rel)
		if !cp.TestBinary {
			sets := []struct {
				kind  string
				names []string
			}{
				{"active-go", p.GoFiles}, {"native-go", p.CgoFiles}, {"test-go", p.TestGoFiles}, {"external-test-go", p.XTestGoFiles}, {"ignored-go", p.IgnoredGoFiles},
				{"native", append(append(append(append(append(append(append(append(append([]string{}, p.CFiles...), p.CXXFiles...), p.MFiles...), p.HFiles...), p.FFiles...), p.SFiles...), p.SwigFiles...), p.SwigCXXFiles...), p.SysoFiles...)},
				{"ignored-other", p.IgnoredOtherFiles}, {"embed", p.EmbedFiles}, {"test-embed", p.TestEmbedFiles}, {"external-test-embed", p.XTestEmbedFiles},
			}
			for _, set := range sets {
				for _, name := range set.names {
					filename := name
					if !filepath.IsAbs(name) {
						filename = filepath.Join(p.Dir, name)
					}
					data, err := addInput(owner, filename, set.kind)
					if err != nil {
						return c, err
					}
					if data == nil {
						continue
					}
					if closureGoSourceKind(set.kind) {
						cp.Sources[name] = data
					}
					if set.kind == "active-go" {
						cp.ActiveSources = append(cp.ActiveSources, name)
					}
					if set.kind == "native" || set.kind == "native-go" {
						gap("native-input-unqualified", owner)
					}
				}
			}
		}
		if p.Module != nil {
			for _, name := range []string{p.Module.GoMod, filepath.Join(p.Module.Dir, "go.sum")} {
				if name == "" {
					continue
				}
				if _, err := os.Lstat(name); errors.Is(err, os.ErrNotExist) {
					continue
				}
				if _, err := addInput(owner, name, "module"); err != nil {
					return c, err
				}
			}
			if p.Module.Replace != nil {
				if _, err := addInput(owner, p.Module.Replace.GoMod, "replacement-module"); err != nil {
					return c, err
				}
			}
		}
		slices.Sort(cp.ActiveSources)
		slices.Sort(cp.Imports)
		c.Packages = append(c.Packages, cp)
	}
	for ancestor := dir; within(root, ancestor); ancestor = filepath.Dir(ancestor) {
		for _, name := range []string{"go.mod", "go.sum", "go.work", "go.work.sum", "vendor/modules.txt"} {
			filename := filepath.Join(ancestor, name)
			if _, err := os.Lstat(filename); errors.Is(err, os.ErrNotExist) {
				continue
			}
			if _, err := addInput(w.ID, filename, "build-context"); err != nil {
				return c, err
			}
		}
		if ancestor == root {
			break
		}
	}
	caseSet := map[model.Case]bool{}
	addCase := func(item model.Case) {
		if caseSet[item] {
			gap("duplicate-case-inventory", item.Unit)
		}
		caseSet[item] = true
	}
	for i := range c.Packages {
		cp := &c.Packages[i]
		if cp.Directory == "" || cp.TestBinary || cp.Standard {
			continue
		}
		files, parsed := []*ast.File{}, map[string]*ast.File{}
		fset := token.NewFileSet()
		for _, name := range sortedSourceNames(cp.Sources) {
			f, err := parser.ParseFile(fset, filepath.ToSlash(filepath.Join(cp.Directory, name)), cp.Sources[name], parser.ParseComments|parser.AllErrors)
			if err != nil {
				gap("source-parse-unqualified", cp.ID)
				continue
			}
			parsed[name] = f
			if slices.Contains(cp.ActiveSources, name) {
				files = append(files, f)
			}
			for _, declaration := range f.Decls {
				if fn, ok := declaration.(*ast.FuncDecl); ok && (fn.Name.Name == "init" || fn.Name.Name == "TestMain") {
					gap("package-startup-unqualified", cp.ID)
				}
				if d, ok := declaration.(*ast.GenDecl); ok && d.Tok == token.VAR {
					gap("package-global-state-unqualified", cp.ID)
				}
			}
			for _, group := range f.Comments {
				for _, comment := range group.List {
					if strings.Contains(comment.Text, "Code generated") {
						gap("generated-provenance-unqualified", cp.ID)
					}
					if strings.HasPrefix(comment.Text, "//go:") && !strings.HasPrefix(comment.Text, "//go:build") && !strings.HasPrefix(comment.Text, "//go:embed") {
						gap("compiler-directive-unqualified", cp.ID)
					}
				}
			}
			ast.Inspect(f, func(node ast.Node) bool {
				switch n := node.(type) {
				case *ast.GoStmt, *ast.SendStmt, *ast.SelectStmt, *ast.ChanType:
					gap("concurrency-unqualified", cp.ID)
				case *ast.SelectorExpr:
					if n.Sel.Name == "Run" || n.Sel.Name == "Parallel" {
						gap("dynamic-case-inventory", cp.ID)
					}
				}
				return ctx.Err() == nil
			})
			effects, err := sourceEffects(ctx, root, filepath.Join(root, cp.Directory, name), cp.ID)
			if err != nil {
				return c, err
			}
			c.Effects = append(c.Effects, effects...)
		}
		if len(files) == 0 && len(cp.ActiveSources) == 0 && len(listed[cp.ImportPath].CgoFiles) == 0 {
			// A test-only package has a legitimate empty production variant;
			// its separate compiler test variant supplies the actual case types.
			cp.SemanticDigest = closureDigest(struct {
				Package string
				Empty   bool
			}{cp.ImportPath, true})
			continue
		}
		if len(files) == 0 {
			gap("typed-semantics-unavailable", cp.ID)
			continue
		}
		info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}}
		base := importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
			data, exists := exports[path]
			if !exists {
				return nil, errors.New("import absent from captured compiler exports")
			}
			return io.NopCloser(bytes.NewReader(data)), nil
		})
		metadata := listed[cp.ImportPath]
		config := types.Config{Importer: closureImporter{base: base, mapping: metadata.ImportMap}, Sizes: types.SizesFor("gc", environmentValue(env, "GOARCH")), Error: func(error) {}}
		if config.Sizes == nil {
			gap("typed-semantics-unavailable", cp.ID)
			continue
		}
		if _, err := config.Check(cp.ImportPath, fset, files, info); err != nil {
			gap("typed-semantics-unavailable", cp.ID)
			continue
		}
		bindings := []string{}
		for _, objects := range []map[*ast.Ident]types.Object{info.Defs, info.Uses} {
			for id, object := range objects {
				if object != nil {
					bindings = append(bindings, fset.Position(id.Pos()).String()+":"+object.String()+":"+types.TypeString(object.Type(), func(p *types.Package) string { return p.Path() }))
				}
			}
		}
		sort.Strings(bindings)
		cp.SemanticDigest = closureDigest(bindings)
		unit, isRoot := roots[metadata.ForTest]
		if !isRoot {
			continue
		}
		for name, file := range parsed {
			if !strings.HasSuffix(name, "_test.go") || !slices.Contains(cp.ActiveSources, name) {
				continue
			}
			for _, declaration := range file.Decls {
				fn, ok := declaration.(*ast.FuncDecl)
				if !ok || fn.Recv != nil {
					continue
				}
				if strings.HasPrefix(fn.Name.Name, "Fuzz") {
					gap("dynamic-case-inventory", cp.ID)
					continue
				}
				if !testEntryName(fn.Name.Name) {
					continue
				}
				object, ok := info.Defs[fn.Name].(*types.Func)
				if ok && fn.Name.Name == "TestMain" && !typedTest(object) {
					continue
				}
				if !ok || !typedTest(object) {
					gap("test-signature-unqualified", cp.ID)
					continue
				}
				addCase(model.Case{Unit: unit.ID, ID: fn.Name.Name})
			}
		}
		exampleFiles := []*ast.File{}
		for name, file := range parsed {
			if strings.HasSuffix(name, "_test.go") && slices.Contains(cp.ActiveSources, name) {
				exampleFiles = append(exampleFiles, file)
			}
		}
		for _, example := range doc.Examples(exampleFiles...) {
			if example.Output != "" || example.EmptyOutput {
				addCase(model.Case{Unit: unit.ID, ID: "Example" + example.Name})
			}
		}
	}
	for _, unit := range roots {
		c.Scopes = append(c.Scopes, unit)
		found := false
		for item := range caseSet {
			if item.Unit == unit.ID {
				found = true
				break
			}
		}
		if found {
			c.Inventory.Units = append(c.Inventory.Units, unit)
		} else {
			gap("empty-case-inventory", unit.ID)
		}
	}
	for item := range caseSet {
		c.Inventory.Cases = append(c.Inventory.Cases, item)
	}
	for input := range inputs {
		c.Inputs = append(c.Inputs, input)
	}
	sort.Slice(c.Inputs, func(i, j int) bool {
		return c.Inputs[i].Owner+"\x00"+c.Inputs[i].Path+"\x00"+c.Inputs[i].Kind < c.Inputs[j].Owner+"\x00"+c.Inputs[j].Path+"\x00"+c.Inputs[j].Kind
	})
	sort.Slice(c.Scopes, func(i, j int) bool { return c.Scopes[i].ID < c.Scopes[j].ID })
	sort.Slice(c.Inventory.Units, func(i, j int) bool { return c.Inventory.Units[i].ID < c.Inventory.Units[j].ID })
	sort.Slice(c.Inventory.Cases, func(i, j int) bool {
		return c.Inventory.Cases[i].Unit+"\x00"+c.Inventory.Cases[i].ID < c.Inventory.Cases[j].Unit+"\x00"+c.Inventory.Cases[j].ID
	})
	sort.Slice(c.Gaps, func(i, j int) bool {
		return c.Gaps[i].Code+"\x00"+c.Gaps[i].Detail < c.Gaps[j].Code+"\x00"+c.Gaps[j].Detail
	})
	sort.Slice(c.Effects, func(i, j int) bool {
		return c.Effects[i].Owner+"\x00"+c.Effects[i].Kind+"\x00"+c.Effects[i].Path < c.Effects[j].Owner+"\x00"+c.Effects[j].Kind+"\x00"+c.Effects[j].Path
	})
	if runner.ValidateInventory(c.Inventory) != nil {
		gap("incomplete-test-inventory", w.ID)
	}
	c.InventoryComplete = true
	for _, issue := range c.Gaps {
		switch issue.Code {
		case "duplicate-case-inventory", "dynamic-case-inventory", "execution-multiplicity-unqualified", "typed-semantics-unavailable", "source-parse-unqualified", "test-signature-unqualified", "empty-case-inventory", "incomplete-test-inventory", "external-source", "unresolved-import":
			c.InventoryComplete = false
		}
	}
	c.Digest = closureDigest(c)
	if err := RevalidateClosure(ctx, root, c); err != nil {
		return c, err
	}
	return c, nil
}

// RevalidateClosure rejects snapshot drift. This observes current state, not
// transient writes or external influence; it grants no runtime qualification.
func RevalidateClosure(ctx context.Context, root string, c Closure) error {
	want := c.Digest
	c.Digest = ""
	if want == "" || closureDigest(c) != want {
		return errors.New("Go closure evidence changed")
	}
	digest, err := snapshot.Digest(ctx, root)
	if err != nil || digest != c.SourceDigest {
		return errors.New("Go closure snapshot changed")
	}
	for _, artifact := range c.artifacts {
		data, err := closureRead(ctx, artifact.path, artifact.limit)
		if err != nil || closureDigest(data) != artifact.digest {
			return errors.New("Go closure compiler/export identity changed")
		}
	}
	packages := map[string]ClosurePackage{}
	for _, p := range c.Packages {
		packages[p.ID] = p
	}
	expectedSources := map[string]map[string]string{}
	for _, input := range c.Inputs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !closureGoSourceKind(input.Kind) {
			continue
		}
		p, exists := packages[input.Owner]
		if !exists || p.Directory == "" {
			return errors.New("Go closure source owner missing")
		}
		name, err := filepath.Rel(filepath.FromSlash(p.Directory), filepath.FromSlash(input.Path))
		if err != nil || !filepath.IsLocal(name) || !strings.HasSuffix(name, ".go") {
			return errors.New("Go closure source membership escapes package")
		}
		if expectedSources[p.ID] == nil {
			expectedSources[p.ID] = map[string]string{}
		}
		expectedSources[p.ID][filepath.ToSlash(name)] = input.Digest
	}
	for _, p := range c.Packages {
		if len(p.Sources) != len(expectedSources[p.ID]) {
			return errors.New("Go closure private source membership changed")
		}
		for name, data := range p.Sources {
			if err := ctx.Err(); err != nil {
				return err
			}
			if expectedSources[p.ID][filepath.ToSlash(name)] != closureDigest(data) {
				return errors.New("Go closure private source copies changed")
			}
		}
	}
	return nil
}

func closureGoSourceKind(kind string) bool {
	return kind == "active-go" || kind == "native-go" || kind == "test-go" || kind == "external-test-go" || kind == "ignored-go"
}

func closureRead(ctx context.Context, name string, limit int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	before, err := os.Lstat(name)
	if err != nil || !before.Mode().IsRegular() || before.Size() < 0 || before.Size() > limit {
		return nil, errors.New("Go closure input must be a bounded regular file")
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, errors.New("Go closure input unavailable")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, errors.New("Go closure input changed")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	after, e := os.Lstat(name)
	if err != nil || e != nil || !os.SameFile(opened, after) || after.Size() != int64(len(data)) || int64(len(data)) > limit || !before.ModTime().Equal(after.ModTime()) {
		return nil, errors.New("Go closure input changed or exceeds limit")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

type closureImporter struct {
	base    types.Importer
	mapping map[string]string
}

func (i closureImporter) Import(path string) (*types.Package, error) {
	if target := i.mapping[path]; target != "" {
		path = target
	}
	return i.base.Import(path)
}
func sortedSourceNames(sources map[string][]byte) []string {
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
func environmentValue(env []string, name string) string {
	for _, pair := range env {
		if key, value, ok := strings.Cut(pair, "="); ok && key == name {
			return value
		}
	}
	return ""
}

// Match the original tool's automatic vendor selection without changing an
// explicit supported -mod mode. Dependency installation remains disabled.
func closureModuleFlags(ctx context.Context, root, dir string, w model.Workspace, env, flags []string) ([]string, error) {
	_, original, _, _ := parseCommand(w.Command)
	for _, flag := range original {
		if flag == "-mod" || strings.HasPrefix(flag, "-mod=") {
			return flags, nil
		}
	}
	work := environmentValue(env, "GOWORK")
	if work != "" && work != "off" {
		if _, err := os.Stat(filepath.Join(filepath.Dir(work), "vendor", "modules.txt")); err == nil {
			enabled, err := closureAutomaticVendor(ctx, work, 22, "1.18")
			if err != nil {
				return nil, err
			}
			if enabled {
				flags[len(flags)-1] = "-mod=vendor"
			}
		}
		return flags, nil
	}
	for ancestor := dir; within(root, ancestor); ancestor = filepath.Dir(ancestor) {
		if _, err := os.Stat(filepath.Join(ancestor, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(ancestor, "vendor", "modules.txt")); err == nil {
				enabled, err := closureAutomaticVendor(ctx, filepath.Join(ancestor, "go.mod"), 14, "1.16")
				if err != nil {
					return nil, err
				}
				if enabled {
					flags[len(flags)-1] = "-mod=vendor"
				}
			}
			break
		}
		if ancestor == root {
			break
		}
	}
	return flags, nil
}

func closureAutomaticVendor(ctx context.Context, filename string, minimum int, defaultVersion string) (bool, error) {
	data, err := closureRead(ctx, filename, 16<<20)
	if err != nil {
		return false, err
	}
	version := defaultVersion
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "go" {
			version = fields[1]
			break
		}
	}
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return false, errors.New("Go module/workspace version unavailable")
	}
	major, e1 := strconv.Atoi(parts[0])
	minor, e2 := strconv.Atoi(parts[1])
	if e1 != nil || e2 != nil {
		return false, errors.New("Go module/workspace version unavailable")
	}
	return major > 1 || (major == 1 && minor >= minimum), nil
}
func testEntryName(name string) bool {
	if !strings.HasPrefix(name, "Test") {
		return false
	}
	if len(name) == 4 {
		return true
	}
	r, _ := utf8.DecodeRuneInString(name[4:])
	return !unicode.IsLower(r)
}
func typedTest(fn *types.Func) bool {
	s, ok := fn.Type().(*types.Signature)
	if !ok || s.Params().Len() != 1 || s.Results().Len() != 0 || s.TypeParams().Len() != 0 {
		return false
	}
	p, ok := s.Params().At(0).Type().(*types.Pointer)
	if !ok {
		return false
	}
	n, ok := types.Unalias(p.Elem()).(*types.Named)
	return ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == "testing" && n.Obj().Name() == "T"
}
