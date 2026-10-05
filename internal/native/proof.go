package native

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const proofVersion = "go-pure-package-v1"

// qualifyGo is deliberately a whitelist, not an effect blacklist or coverage
// inference. Every declaration and branch is checked. Unknown syntax broadens.
// The trusted Go compiler, testing package and runtime are part of this contract.
func qualifyGo(files map[string][]byte) ([]string, error) {
	fset := token.NewFileSet()
	parsed := []*ast.File{}
	funcs := map[string]bool{}
	tests := map[string]bool{}
	packageName := ""
	for name, data := range files {
		if !strings.HasSuffix(name, ".go") {
			return nil, errors.New("non-Go package input")
		}
		f, err := parser.ParseFile(fset, name, data, parser.ParseComments|parser.AllErrors)
		if err != nil {
			return nil, errors.New("Go source could not be parsed")
		}
		if packageName == "" {
			packageName = f.Name.Name
		}
		if f.Name.Name != packageName {
			return nil, errors.New("external test package")
		}
		for _, g := range f.Comments {
			for _, c := range g.List {
				if strings.HasPrefix(c.Text, "//go:") || strings.HasPrefix(c.Text, "// +build") {
					return nil, errors.New("compiler directive")
				}
			}
		}
		for _, im := range f.Imports {
			if im.Path.Value != "\"testing\"" || im.Name != nil || !strings.HasSuffix(name, "_test.go") {
				return nil, errors.New("import outside pure contract")
			}
		}
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok {
				if fn.Recv != nil || fn.Body == nil || fn.Name.Name == "init" || fn.Name.Name == "TestMain" || funcs[fn.Name.Name] {
					return nil, errors.New("function outside pure contract")
				}
				funcs[fn.Name.Name] = true
				if strings.HasSuffix(name, "_test.go") && strings.HasPrefix(fn.Name.Name, "Test") && (len(fn.Name.Name) == 4 || !lowerAfterTest(fn.Name.Name)) {
					if !testSignature(fn) {
						return nil, errors.New("test signature outside pure contract")
					}
					tests[fn.Name.Name] = true
				} else if strings.HasPrefix(fn.Name.Name, "Example") || strings.HasPrefix(fn.Name.Name, "Fuzz") || strings.HasPrefix(fn.Name.Name, "Benchmark") {
					return nil, errors.New("non-test entry point")
				}
			} else if gd, ok := d.(*ast.GenDecl); !ok || (gd.Tok != token.IMPORT && gd.Tok != token.CONST) {
				return nil, errors.New("mutable global or type declaration")
			}
		}
		parsed = append(parsed, f)
	}
	if len(tests) == 0 || len(tests) > 10000 {
		return nil, errors.New("empty or oversized test inventory")
	}
	builtins := map[string]bool{"len": true, "cap": true, "append": true, "copy": true, "int": true, "int8": true, "int16": true, "int32": true, "int64": true, "uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true, "float32": true, "float64": true, "string": true, "bool": true, "byte": true, "rune": true, "min": true, "max": true, "clear": true}
	methods := map[string]bool{"Error": true, "Errorf": true, "Fail": true, "FailNow": true, "Fatal": true, "Fatalf": true, "Helper": true, "Skip": true, "Skipf": true, "SkipNow": true, "Failed": true, "Skipped": true, "Name": true}
	for _, f := range parsed {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			testArg := ""
			if tests[fn.Name.Name] {
				testArg = fn.Type.Params.List[0].Names[0].Name
			}
			var invalid bool
			ast.Inspect(fn, func(n ast.Node) bool {
				if n == nil || invalid {
					return false
				}
				switch x := n.(type) {
				case *ast.FuncDecl, *ast.FuncType, *ast.FieldList, *ast.Field, *ast.BlockStmt, *ast.ReturnStmt, *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.CaseClause, *ast.BranchStmt, *ast.IncDecStmt, *ast.AssignStmt, *ast.DeclStmt, *ast.ValueSpec, *ast.ExprStmt, *ast.EmptyStmt, *ast.BasicLit, *ast.BinaryExpr, *ast.ParenExpr, *ast.IndexExpr, *ast.SliceExpr, *ast.CompositeLit, *ast.KeyValueExpr, *ast.ArrayType:
				case *ast.GenDecl:
					invalid = x.Tok != token.CONST && x.Tok != token.VAR
				case *ast.Ident:
					// Callable names must never be shadowed or escape as values.
					if x != fn.Name && x != nil && (funcs[x.Name] || builtins[x.Name] || x.Name == "testing" || x.Name == testArg) && x.Obj != nil && x.Obj.Kind != ast.Fun && x.Name != testArg {
						invalid = true
					}
				case *ast.UnaryExpr:
					invalid = x.Op == token.AND || x.Op == token.ARROW || x.Op == token.MUL
				case *ast.StarExpr:
					invalid = !(tests[fn.Name.Name] && x == fn.Type.Params.List[0].Type)
				case *ast.SelectorExpr:
					id, ok := x.X.(*ast.Ident)
					invalid = !ok || !((id.Name == testArg && methods[x.Sel.Name]) || (id.Name == "testing" && x.Sel.Name == "T"))
				case *ast.CallExpr:
					switch fun := x.Fun.(type) {
					case *ast.Ident:
						invalid = !funcs[fun.Name] && !builtins[fun.Name]
					case *ast.SelectorExpr:
						id, ok := fun.X.(*ast.Ident)
						invalid = !ok || id.Name != testArg || !methods[fun.Sel.Name]
					default:
						invalid = true
					}
				default:
					invalid = true
				}
				return !invalid
			})
			if invalid {
				return nil, errors.New("function has unsupported syntax or effects")
			}
			// A testing handle must only be the receiver of approved assertion calls;
			// it cannot escape into another function, be reassigned, or be aliased.
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && id.Name == testArg && testArg != "" {
					invalid = true
				}
				if call, ok := n.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
						if id, ok := sel.X.(*ast.Ident); ok && id.Name == testArg {
							for _, arg := range call.Args {
								ast.Inspect(arg, func(n ast.Node) bool {
									if id, ok := n.(*ast.Ident); ok && id.Name == testArg {
										invalid = true
									}
									return true
								})
							}
							return false
						}
					}
				}
				return true
			})
			if invalid {
				return nil, errors.New("testing handle escapes")
			}
		}
	}
	// Parent checks prohibit direct/indirect function and method values, including
	// cross-file names and values passed to assertion formatting.
	for _, f := range parsed {
		var invalid bool
		stack := []ast.Node{}
		ast.Inspect(f, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return false
			}
			var parent ast.Node
			if len(stack) > 0 {
				parent = stack[len(stack)-1]
			}
			stack = append(stack, n)
			if id, ok := n.(*ast.Ident); ok && funcs[id.Name] {
				allowed := false
				if fn, ok := parent.(*ast.FuncDecl); ok && fn.Name == id {
					allowed = true
				}
				if call, ok := parent.(*ast.CallExpr); ok && call.Fun == id {
					allowed = true
				}
				if !allowed {
					invalid = true
				}
			}
			if sel, ok := n.(*ast.SelectorExpr); ok {
				allowed := false
				if call, ok := parent.(*ast.CallExpr); ok && call.Fun == sel {
					allowed = true
				}
				if ptr, ok := parent.(*ast.StarExpr); ok && ptr.X == sel {
					allowed = true
				}
				if !allowed {
					invalid = true
				}
			}
			return true
		})
		if invalid {
			return nil, errors.New("function or method value outside pure contract")
		}
	}
	result := make([]string, 0, len(tests))
	for name := range tests {
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}
func testSignature(f *ast.FuncDecl) bool {
	if f.Type.TypeParams != nil || f.Type.Results != nil || len(f.Type.Params.List) != 1 {
		return false
	}
	p := f.Type.Params.List[0]
	if len(p.Names) != 1 || p.Names[0].Name == "_" {
		return false
	}
	ptr, ok := p.Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := ptr.X.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "T" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == "testing"
}

func lowerAfterTest(name string) bool {
	r, _ := utf8.DecodeRuneInString(name[4:])
	return unicode.IsLower(r)
}
