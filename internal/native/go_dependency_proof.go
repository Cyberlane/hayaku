package native

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/Cyberlane/hayaku/internal/adapter/golang"
	"github.com/Cyberlane/hayaku/internal/model"
)

const goDependencyVersion = "go-closed-local-helpers-v1"

type goDependencyProof struct {
	closure         *GoClosure
	name, digest    string
	cases           []string
	env             []string
	workspace       string
	originalContext string
	verifyOriginal  func(context.Context) error
}

type dependencyFunction struct {
	key     string
	variant golang.ClosurePackage
	file    string
	node    *ast.FuncDecl
	binding golang.Binding
	calls   []string
	cost    int
	test    bool
}

type dependencyFile struct {
	variant golang.ClosurePackage
	name    string
	file    *ast.File
	fset    *token.FileSet
	parents map[ast.Node]ast.Node
	digest  string
}

// Capture uses the existing private materialization. Descriptive facts cannot
// issue an Artifact; only certifyGoDependency and issueGoArtifact consume them.
func resolveGoDependencyClosure(ctx context.Context, root, source, temporary, sourceDigest, tool string, config Config) (*GoClosure, error) {
	if config.Architecture != "arm64" || runtime.GOARCH != "arm64" || (runtime.GOOS != "darwin" && runtime.GOOS != "linux") {
		return nil, errors.New("Go dependency qualification requires reviewed ARM64 context")
	}
	env := buildEnvironmentForPlatform(config.Platform, config.Architecture)
	execution := model.Context{OS: config.Platform, Arch: config.Architecture}
	w := model.Workspace{ID: "native-go", Root: ".", Adapter: "go", Command: model.Command{Executable: tool, Dir: ".", Args: append([]string{"test", "-trimpath", "-count=1"}, config.Packages...)}}
	c := &GoClosure{root: root, source: source, temporary: temporary, SourceDigest: sourceDigest}
	var err error
	c.Evidence, err = golang.ResolveClosureEnvironment(ctx, source, w, execution, env)
	if err != nil {
		return nil, err
	}
	c.evidenceDigest = c.Evidence.Digest
	if err := c.Revalidate(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

func certifyGoDependency(ctx context.Context, captured *GoClosure, name string, env []string) (*goDependencyProof, error) {
	if captured == nil || captured.Revalidate(ctx) != nil || !validPackage(name) {
		return nil, errors.New("Go dependency capture unavailable")
	}
	c := captured.Evidence
	if !c.InventoryComplete {
		return nil, errors.New("Go helper inventory incomplete")
	}
	byPath := map[string]golang.ClosurePackage{}
	var root golang.ClosurePackage
	directory := strings.TrimPrefix(name, "./")
	for _, p := range c.Packages {
		byPath[p.ImportPath] = p
		if p.Root && p.Directory == directory {
			root = p
		}
	}
	if root.ID == "" || root.ModulePath == "" || root.Replaced || root.ModuleVersion != "" {
		return nil, errors.New("Go helper root is outside a closed local module")
	}
	for _, input := range c.Inputs {
		if input.Kind == "build-context" && (strings.HasSuffix(input.Path, "go.work") || strings.HasSuffix(input.Path, "go.work.sum") || strings.HasSuffix(input.Path, "vendor/modules.txt")) {
			return nil, errors.New("Go workspace or vendor setup is outside helper contract")
		}
	}
	selected := map[string]golang.ClosurePackage{}
	queue := []string{}
	for _, p := range c.Packages {
		if !p.TestBinary && p.ForTest == root.ImportPath {
			queue = append(queue, p.ImportPath)
		}
	}
	if len(queue) == 0 {
		return nil, errors.New("Go selected test variants absent")
	}
	for len(queue) != 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path := queue[0]
		queue = queue[1:]
		if _, exists := selected[path]; exists {
			continue
		}
		p, exists := byPath[path]
		if !exists || p.Standard || p.TestBinary || p.Directory == "" || p.Replaced || p.ModulePath != root.ModulePath || p.ModuleVersion != "" || p.GoVersion == "" || len(selected) >= 100 {
			return nil, errors.New("Go dependency is outside captured local contract")
		}
		selected[path] = p
		for _, dependency := range p.Imports {
			path := strings.TrimPrefix(dependency, "native-go:")
			if path == "testing" || path == "embed" {
				continue
			}
			queue = append(queue, path)
		}
	}
	paths := make([]string, 0, len(selected))
	for path := range selected {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	files := []dependencyFile{}
	requests := []golang.BindingKey{}
	var bytes int
	nodes := 0
	for _, path := range paths {
		p := selected[path]
		// Compiler-selected files are typed. Other captured Go files are still
		// checked by the lexical whitelist and cannot disappear as dormant code.
		for _, name := range sortedGoSourceNames(p.Sources) {
			if !filepath.IsLocal(name) || strings.Contains(name, "/") || !strings.HasSuffix(name, ".go") {
				return nil, errors.New("Go helper source name unsupported")
			}
			data := p.Sources[name]
			bytes += len(data)
			if len(data) > 16<<20 || bytes > 64<<20 || len(files) >= 1000 {
				return nil, errors.New("Go helper sources exceed bounds")
			}
			if !slices.Contains(p.ActiveSources, name) {
				// Root production/internal/external variants collectively account
				// for the same test files. An inactive file with no typed owner is
				// conservatively unsupported, even if it appears harmless.
				activeElsewhere := false
				for _, other := range selected {
					if other.Directory == p.Directory && slices.Contains(other.ActiveSources, name) && digest(other.Sources[name]) == digest(data) {
						activeElsewhere = true
					}
				}
				if !activeElsewhere {
					return nil, errors.New("Go dormant source lacks selected typed owner")
				}
				continue
			}
			fset := token.NewFileSet()
			if err := boundedHelperTokens(ctx, data); err != nil {
				return nil, err
			}
			file, err := parser.ParseFile(fset, name, data, parser.ParseComments|parser.AllErrors)
			if err != nil {
				return nil, errors.New("Go helper source parse failed")
			}
			parents := map[ast.Node]ast.Node{}
			stack := []ast.Node{}
			tooMany := false
			ast.Inspect(file, func(node ast.Node) bool {
				if node == nil {
					stack = stack[:len(stack)-1]
					return false
				}
				nodes++
				if nodes > 100000 {
					tooMany = true
					return false
				}
				if len(stack) > 0 {
					parents[node] = stack[len(stack)-1]
				}
				stack = append(stack, node)
				return true
			})
			if tooMany {
				return nil, errors.New("Go helper AST nodes exceed bounds")
			}
			sourceDigest := digest(data)
			for _, decl := range file.Decls {
				ast.Inspect(decl, func(node ast.Node) bool {
					if id, ok := node.(*ast.Ident); ok && id.Name != "_" {
						requests = append(requests, golang.BindingKey{Variant: p.ID, File: name, SourceDigest: sourceDigest, Offset: fset.File(id.Pos()).Offset(id.Pos())})
					}
					return true
				})
			}
			files = append(files, dependencyFile{p, name, file, fset, parents, sourceDigest})
		}
	}
	if len(requests) == 0 || len(requests) > 100000 {
		return nil, errors.New("Go helper binding requests exceed bounds")
	}
	bindings, err := c.LookupBindings(ctx, requests)
	if err != nil {
		return nil, err
	}
	facts := map[golang.BindingKey]golang.Binding{}
	for _, b := range bindings {
		facts[b.Key] = b
	}
	lookup := func(file dependencyFile, id *ast.Ident) (golang.Binding, bool) {
		key := golang.BindingKey{Variant: file.variant.ID, File: file.name, SourceDigest: file.digest, Offset: file.fset.File(id.Pos()).Offset(id.Pos())}
		b, ok := facts[key]
		return b, ok && b.Name == id.Name
	}
	globals, fixtures, err := certifyGoStringEmbeds(ctx, captured.source, files, c.Inputs, selected, lookup)
	if err != nil {
		return nil, err
	}
	if err := validateDependencyInputs(c.Inputs, selected, fixtures); err != nil {
		return nil, err
	}
	functions := map[string]*dependencyFunction{}
	cases := map[string]bool{}
	for _, file := range files {
		for _, decl := range file.file.Decls {
			switch decl := decl.(type) {
			case *ast.GenDecl:
				if decl.Tok == token.VAR {
					continue
				} // Already checked as exact string embeds.
				if decl.Tok != token.IMPORT && decl.Tok != token.CONST {
					return nil, errors.New("Go helper mutable global or type declaration")
				}
				for _, spec := range decl.Specs {
					if imported, ok := spec.(*ast.ImportSpec); ok && imported.Name != nil && (imported.Name.Name == "_" || imported.Name.Name == ".") {
						if imported.Name.Name != "_" || imported.Path.Value != `"embed"` {
							return nil, errors.New("Go helper implicit import bindings unsupported")
						}
					}
					if constants, ok := spec.(*ast.ValueSpec); ok {
						for _, name := range constants.Names {
							if name.Name == "_" {
								continue
							}
							b, ok := lookup(file, name)
							if !ok || b.Kind != "const" || b.Type == "" || b.ConstantValue == "" {
								return nil, errors.New("Go helper constant outside bounded primitive contract")
							}
						}
					}
				}
			case *ast.FuncDecl:
				b, ok := lookup(file, decl.Name)
				if !ok || b.Kind != "func" || decl.Recv != nil || decl.Body == nil || b.Receiver || b.Generic || b.Variadic || decl.Name.Name == "init" || decl.Name.Name == "TestMain" {
					return nil, errors.New("Go helper function declaration unsupported")
				}
				isTest := file.variant.ForTest == root.ImportPath && strings.HasSuffix(file.name, "_test.go") && strings.HasPrefix(decl.Name.Name, "Test") && (len(decl.Name.Name) == 4 || !lowerAfterTest(decl.Name.Name))
				if isTest {
					if decl.Type.TypeParams != nil || decl.Type.Results != nil || len(decl.Type.Params.List) != 1 || len(decl.Type.Params.List[0].Names) != 1 || decl.Type.Params.List[0].Names[0].Name == "_" || !slices.Equal(b.Parameters, []string{"*testing.T"}) || len(b.Results) != 0 || cases[decl.Name.Name] {
						return nil, errors.New("Go helper native test identity or signature unsupported")
					}
					cases[decl.Name.Name] = true
				} else {
					if strings.HasPrefix(decl.Name.Name, "Test") || strings.HasPrefix(decl.Name.Name, "Example") || strings.HasPrefix(decl.Name.Name, "Benchmark") || strings.HasPrefix(decl.Name.Name, "Fuzz") {
						return nil, errors.New("Go helper entrypoint unsupported")
					}
					for _, t := range append(slices.Clone(b.Parameters), b.Results...) {
						if t == "" || t == "*testing.T" {
							return nil, errors.New("Go helper signature is not primitive")
						}
					}
				}
				key := file.variant.ImportPath + ":" + decl.Name.Name
				if functions[key] != nil {
					return nil, errors.New("Go helper function duplicated")
				}
				functions[key] = &dependencyFunction{key: key, variant: file.variant, file: file.name, node: decl, binding: b, test: isTest}
			default:
				return nil, errors.New("Go helper declaration unsupported")
			}
		}
	}
	if len(cases) == 0 || len(cases) > 10000 {
		return nil, errors.New("Go helper tests empty or oversized")
	}
	for _, file := range files {
		for _, decl := range file.file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok {
				if err := checkDependencyFunction(ctx, file, functions[file.variant.ImportPath+":"+fn.Name.Name], functions, lookup, globals); err != nil {
					return nil, err
				}
			}
		}
	}
	if err := boundedDependencyCalls(functions); err != nil {
		return nil, err
	}
	result := make([]string, 0, len(cases))
	for name := range cases {
		result = append(result, name)
	}
	sort.Strings(result)
	expected := []string{}
	for _, item := range c.Inventory.Cases {
		if item.Unit == root.ID {
			expected = append(expected, item.ID)
		}
	}
	sort.Strings(expected)
	if !slices.Equal(result, expected) {
		return nil, errors.New("Go helper compiler and private case inventories differ")
	}
	packages := []golang.ClosurePackage{}
	sources := map[string]string{}
	for _, path := range paths {
		p := selected[path]
		for name, data := range p.Sources {
			sources[path+":"+name] = digest(data)
		}
		p.Sources = nil
		packages = append(packages, p)
	}
	// Source and typed values bind only the reachable group closure. The full
	// workspace digest is retained solely as an aggregate drift guard.
	version := goDependencyVersion
	if len(fixtures) > 0 {
		version = "go-closed-local-string-fixtures-v1"
	}
	proof := identity(struct {
		Version, Toolchain string
		Environment        []string
		Packages           []golang.ClosurePackage
		Bindings           []golang.Binding
		Sources            map[string]string
		Cases              []string
		Fixtures           []golang.ClosureInput `json:",omitempty"`
	}{version, c.ToolchainDigest, env, packages, bindings, sources, result, fixtures})
	if err := captured.Revalidate(ctx); err != nil {
		return nil, err
	}
	return &goDependencyProof{closure: captured, name: name, digest: proof, cases: result, env: slices.Clone(env)}, nil
}

func validateDependencyInputs(inputs []golang.ClosureInput, selected map[string]golang.ClosurePackage, admitted ...[]golang.ClosureInput) error {
	fixtures := map[golang.ClosureInput]bool{}
	if len(admitted) > 1 {
		return errors.New("ambiguous Go fixture input admission")
	}
	if len(admitted) == 1 {
		for _, input := range admitted[0] {
			fixtures[input] = true
		}
	}
	owners := map[string]bool{}
	for _, p := range selected {
		owners[p.ID] = true
	}
	for _, input := range inputs {
		if !owners[input.Owner] {
			continue
		}
		switch input.Kind {
		case "active-go", "test-go", "external-test-go", "ignored-go", "module":
		default:
			if !stringEmbedKind(input.Kind) || !fixtures[input] {
				return errors.New("Go helper selected compiler input outside reviewed source contract")
			}
		}
	}
	return nil
}

func boundedHelperTokens(ctx context.Context, data []byte) error {
	var lexer scanner.Scanner
	files := token.NewFileSet()
	lexer.Init(files.AddFile("bounded", -1, len(data)), data, nil, 0)
	for count := 0; ; count++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if count >= 100000 {
			return errors.New("Go helper source tokens exceed bounds")
		}
		if _, kind, _ := lexer.Scan(); kind == token.EOF {
			return nil
		}
	}
}

func sortedGoSourceNames(sources map[string][]byte) []string {
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

var helperBuiltins = map[string]bool{"len": true, "cap": true, "append": true, "copy": true, "min": true, "max": true, "clear": true}
var helperConversions = map[string]bool{"int": true, "int8": true, "int16": true, "int32": true, "int64": true, "uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true, "float32": true, "float64": true, "string": true, "bool": true, "byte": true, "rune": true}
var helperTestingMethods = map[string]bool{"Error": true, "Errorf": true, "Fail": true, "FailNow": true, "Fatal": true, "Fatalf": true, "Helper": true, "Skip": true, "Skipf": true, "SkipNow": true, "Failed": true, "Skipped": true, "Name": true}

func checkDependencyFunction(ctx context.Context, file dependencyFile, fn *dependencyFunction, functions map[string]*dependencyFunction, lookup func(dependencyFile, *ast.Ident) (golang.Binding, bool), globals map[string]golang.Binding) error {
	var invalid error
	testArg := ""
	if fn.test {
		testArg = fn.node.Type.Params.List[0].Names[0].Name
	}
	ast.Inspect(fn.node, func(node ast.Node) bool {
		if node == nil || invalid != nil {
			return false
		}
		if err := ctx.Err(); err != nil {
			invalid = err
			return false
		}
		fn.cost++
		if fn.cost > 100000 {
			invalid = errors.New("Go helper operation count exceeds bounds")
			return false
		}
		parent := file.parents[node]
		switch node := node.(type) {
		case *ast.FuncDecl, *ast.FuncType, *ast.FieldList, *ast.Field, *ast.BlockStmt, *ast.ReturnStmt, *ast.IfStmt, *ast.SwitchStmt, *ast.CaseClause, *ast.IncDecStmt, *ast.AssignStmt, *ast.DeclStmt, *ast.ValueSpec, *ast.ExprStmt, *ast.EmptyStmt, *ast.BasicLit, *ast.BinaryExpr, *ast.ParenExpr, *ast.IndexExpr, *ast.SliceExpr, *ast.CompositeLit, *ast.KeyValueExpr, *ast.ArrayType:
		case *ast.GenDecl:
			if node.Tok != token.CONST && node.Tok != token.VAR {
				invalid = errors.New("Go helper local declaration unsupported")
			}
		case *ast.BranchStmt:
			if node.Tok != token.BREAK {
				invalid = errors.New("Go helper branch unsupported")
			}
		case *ast.UnaryExpr:
			if node.Op != token.ADD && node.Op != token.SUB && node.Op != token.NOT && node.Op != token.XOR {
				invalid = errors.New("Go helper pointer or channel operation")
			}
		case *ast.StarExpr:
			if !fn.test || node != fn.node.Type.Params.List[0].Type {
				invalid = errors.New("Go helper pointer type unsupported")
			}
		case *ast.Ident:
			if node.Name == "_" {
				break
			}
			b, ok := lookup(file, node)
			if !ok {
				invalid = errors.New("Go helper identifier lacks typed binding")
				break
			}
			if b.Kind == "func" {
				_, definition := parent.(*ast.FuncDecl)
				call, called := parent.(*ast.CallExpr)
				sel, selected := parent.(*ast.SelectorExpr)
				selectorCall, selectorCalled := file.parents[parent].(*ast.CallExpr)
				if !definition && !(called && call.Fun == node) && !(selected && sel.Sel == node && selectorCalled && selectorCall.Fun == sel) {
					invalid = errors.New("Go helper function escapes as a value")
				}
			}
			if b.Kind == "package" {
				sel, ok := parent.(*ast.SelectorExpr)
				if !ok || sel.X != node {
					invalid = errors.New("Go helper package binding used indirectly")
				}
			}
			if b.Kind == "var" && b.Type == "" {
				invalid = errors.New("Go helper variable has unsupported type")
			}
			if b.Kind == "var" && b.PackageScope && !readOnlyStringEmbed(file, node, b, globals) {
				invalid = errors.New("Go global outside read-only fixture contract")
			}
			if b.Type == "*testing.T" {
				field, definition := parent.(*ast.Field)
				sel, receiver := parent.(*ast.SelectorExpr)
				if node.Name != testArg || !(definition && slices.Contains(field.Names, node)) && !(receiver && sel.X == node) {
					invalid = errors.New("Go testing handle escapes")
				}
			}
		case *ast.SelectorExpr:
			id, ok := node.X.(*ast.Ident)
			if !ok {
				invalid = errors.New("Go helper computed selector")
				break
			}
			receiver, ok := lookup(file, id)
			if !ok {
				invalid = errors.New("Go helper selector receiver unbound")
				break
			}
			selected, ok := lookup(file, node.Sel)
			if !ok {
				invalid = errors.New("Go helper selected identifier unbound")
				break
			}
			if receiver.Kind == "package" {
				if receiver.ImportedVariant == "testing" {
					ptr, ok := parent.(*ast.StarExpr)
					if !fn.test || !ok || ptr != fn.node.Type.Params.List[0].Type || node.Sel.Name != "T" {
						invalid = errors.New("Go testing selector outside parameter")
					}
				} else if selected.ObjectVariant != receiver.ImportedVariant || selected.Kind != "func" && selected.Kind != "const" && !(selected.Kind == "var" && readOnlyStringEmbed(file, node.Sel, selected, globals)) {
					invalid = errors.New("Go imported selector outside primitive contract")
				}
			} else if receiver.Type != "*testing.T" || id.Name != testArg || !helperTestingMethods[node.Sel.Name] || selected.ObjectVariant != "testing" {
				invalid = errors.New("Go helper method unsupported")
			}
		case *ast.CallExpr:
			var b golang.Binding
			var ok bool
			switch target := node.Fun.(type) {
			case *ast.Ident:
				b, ok = lookup(file, target)
				if ok && (b.Kind == "builtin" && helperBuiltins[target.Name] || b.Kind == "type" && helperConversions[target.Name] && b.ObjectVariant == "") {
					break
				}
			case *ast.SelectorExpr:
				if receiver, yes := target.X.(*ast.Ident); yes && receiver.Name == testArg && testArg != "" {
					break
				}
				b, ok = lookup(file, target.Sel)
			default:
				invalid = errors.New("Go helper indirect call")
				return false
			}
			if b.Kind == "" {
				break
			}
			if b.Kind == "builtin" && helperBuiltins[b.ObjectName] || b.Kind == "type" && helperConversions[b.ObjectName] && b.ObjectVariant == "" {
				break
			}
			callee := functions[b.ObjectVariant+":"+b.ObjectName]
			if !ok || b.Kind != "func" || callee == nil || callee.test || b.Receiver || b.Generic || b.Variadic || !slices.Equal(b.Parameters, callee.binding.Parameters) || !slices.Equal(b.Results, callee.binding.Results) {
				invalid = errors.New("Go helper call target does not match captured local declaration")
				break
			}
			if b.ObjectVariant == file.variant.ImportPath && (b.DeclarationFile != callee.file || b.DeclarationOffset != callee.binding.Key.Offset || b.DeclarationDigest != callee.binding.Key.SourceDigest) {
				invalid = errors.New("Go local call declaration identity mismatch")
				break
			}
			fn.calls = append(fn.calls, callee.key)
		default:
			invalid = errors.New("Go helper syntax or effects unsupported")
		}
		return invalid == nil
	})
	return invalid
}

func boundedDependencyCalls(functions map[string]*dependencyFunction) error {
	active, costs := map[string]bool{}, map[string]int{}
	var cost func(string) (int, error)
	cost = func(key string) (int, error) {
		if active[key] {
			return 0, errors.New("Go recursive helpers unsupported")
		}
		if value, ok := costs[key]; ok {
			return value, nil
		}
		fn := functions[key]
		if fn == nil {
			return 0, errors.New("Go helper target absent")
		}
		active[key] = true
		value := fn.cost
		for _, target := range fn.calls {
			called, err := cost(target)
			if err != nil {
				return 0, err
			}
			if value > 100000-called {
				return 0, errors.New("Go expanded helper work exceeds bounds")
			}
			value += called
		}
		active[key] = false
		costs[key] = value
		return value, nil
	}
	for key := range functions {
		if _, err := cost(key); err != nil {
			return err
		}
	}
	return nil
}

func dependencyImportCandidate(files map[string][]byte) bool {
	for name, data := range files {
		file, err := parser.ParseFile(token.NewFileSet(), name, data, parser.ImportsOnly)
		if err != nil {
			return false
		}
		for _, item := range file.Imports {
			path, err := strconv.Unquote(item.Path.Value)
			if err != nil {
				return false
			}
			if path != "testing" {
				return true
			}
		}
	}
	return false
}
