package golang

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Cyberlane/hayaku/internal/model"
)

// sourceEffects supplements the compiler's package graph. This intentionally
// does not claim complete Go effect analysis: aliases, interfaces, assembly and
// external libraries remain covered by the native runtime-unqualified gap.
func sourceEffects(ctx context.Context, root, filename, owner string) ([]model.Effect, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
	if err != nil {
		return []model.Effect{{Owner: owner, Kind: "unknown"}}, ctx.Err()
	}
	imports := map[string]string{}
	for _, item := range source.Imports {
		name, err := strconv.Unquote(item.Path.Value)
		if err != nil {
			return []model.Effect{{Owner: owner, Kind: "unknown"}}, nil
		}
		alias := filepath.Base(name)
		if item.Name != nil {
			alias = item.Name.Name
		}
		imports[alias] = name
	}
	var effects []model.Effect
	seen := map[model.Effect]bool{}
	add := func(kind, path string) {
		effect := model.Effect{Owner: owner, Kind: kind, Path: path}
		if !seen[effect] {
			effects = append(effects, effect)
			seen[effect] = true
		}
	}
	classify := func(pkg string) string {
		switch {
		case pkg == "os/exec":
			return "subprocess"
		case pkg == "os" || pkg == "io/fs" || pkg == "path/filepath":
			return "unknown"
		case pkg == "time":
			return "clock"
		case pkg == "math/rand" || pkg == "math/rand/v2" || pkg == "crypto/rand":
			return "random"
		case pkg == "net" || strings.HasPrefix(pkg, "net/"):
			return "network"
		case pkg == "syscall" || pkg == "unsafe" || pkg == "plugin" || pkg == "C" || pkg == "reflect":
			return "native"
		}
		return ""
	}
	for alias, pkg := range imports {
		if alias == "." || pkg == "C" {
			add("unknown", "")
		}
	}
	handled := map[*ast.SelectorExpr]bool{}
	visited := 0
	ast.Inspect(source, func(node ast.Node) bool {
		visited++
		if visited%1024 == 0 && ctx.Err() != nil {
			return false
		}
		switch item := node.(type) {
		case *ast.GoStmt:
			add("shared-state", "")
		case *ast.CallExpr:
			selector, ok := item.Fun.(*ast.SelectorExpr)
			if !ok {
				break
			}
			identifier, ok := selector.X.(*ast.Ident)
			if !ok {
				break
			}
			pkg := imports[identifier.Name]
			kind := classify(pkg)
			if kind == "" {
				break
			}
			handled[selector] = true
			if pkg == "os" && len(item.Args) > 0 {
				operation := map[string]string{"ReadFile": "read", "Open": "read", "ReadDir": "directory", "Stat": "metadata", "Lstat": "metadata"}[selector.Sel.Name]
				literal, ok := item.Args[0].(*ast.BasicLit)
				if operation != "" && ok && literal.Kind == token.STRING {
					name, err := strconv.Unquote(literal.Value)
					if err == nil && !filepath.IsAbs(name) {
						name = filepath.Join(filepath.Dir(filename), name)
						if within(root, name) {
							rel, err := filepath.Rel(root, name)
							if err == nil {
								add(operation, filepath.ToSlash(rel))
								break
							}
						}
					}
				}
			}
			add(kind, "")
		case *ast.SelectorExpr:
			if !handled[item] {
				if identifier, ok := item.X.(*ast.Ident); ok {
					if kind := classify(imports[identifier.Name]); kind != "" {
						add(kind, "")
					}
				}
			}
		}
		return ctx.Err() == nil
	})
	return effects, ctx.Err()
}
