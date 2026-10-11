package native

import (
	"context"
	"errors"
	"go/ast"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Cyberlane/hayaku/internal/adapter/golang"
	"github.com/Cyberlane/hayaku/internal/snapshot"
)

// String embeds are compiler-initialized immutable values. Their declarations
// and every read are typed; no runtime filesystem or mutable storage is admitted.
func stringEmbedDeclaration(file dependencyFile, decl *ast.GenDecl, lookup func(dependencyFile, *ast.Ident) (golang.Binding, bool)) (golang.Binding, string, *ast.Comment, error) {
	if decl.Tok != token.VAR || decl.Lparen.IsValid() || len(decl.Specs) != 1 || decl.Doc == nil {
		return golang.Binding{}, "", nil, errors.New("Go setup declaration outside string embed contract")
	}
	spec, ok := decl.Specs[0].(*ast.ValueSpec)
	if !ok || len(spec.Names) != 1 || spec.Names[0].Name == "_" || len(spec.Values) != 0 {
		return golang.Binding{}, "", nil, errors.New("Go string embed declaration unsupported")
	}
	typ, ok := spec.Type.(*ast.Ident)
	if !ok || typ.Name != "string" {
		return golang.Binding{}, "", nil, errors.New("Go embed must be an explicit string")
	}
	binding, ok := lookup(file, spec.Names[0])
	if !ok || binding.Kind != "var" || !binding.PackageScope || binding.Type != "string" || binding.DeclarationFile != file.name || binding.DeclarationDigest != binding.Key.SourceDigest || binding.DeclarationOffset != binding.Key.Offset {
		return golang.Binding{}, "", nil, errors.New("Go string embed declaration identity unavailable")
	}
	var directive *ast.Comment
	for _, comment := range decl.Doc.List {
		if strings.HasPrefix(comment.Text, "//go:embed ") {
			if directive != nil {
				return golang.Binding{}, "", nil, errors.New("Go string embed has multiple directives")
			}
			directive = comment
		}
	}
	if directive == nil {
		return golang.Binding{}, "", nil, errors.New("Go mutable global outside embed contract")
	}
	name := strings.TrimPrefix(directive.Text, "//go:embed ")
	if len(name) > 256 || !fs.ValidPath(name) || strings.ContainsAny(name, "*?[]\\\"` \t\r\n\x00") || strings.HasPrefix(name, "all:") {
		return golang.Binding{}, "", nil, errors.New("Go embed requires one literal local file")
	}
	return binding, path.Join(file.variant.Directory, name), directive, nil
}

func certifyGoStringEmbeds(ctx context.Context, source string, files []dependencyFile, inputs []golang.ClosureInput, selected map[string]golang.ClosurePackage, lookup func(dependencyFile, *ast.Ident) (golang.Binding, bool)) (map[string]golang.Binding, []golang.ClosureInput, error) {
	globals := map[string]golang.Binding{}
	allowed := map[string]map[string]string{}
	directives := map[*ast.Comment]bool{}
	var total int
	for _, file := range files {
		count := 0
		for _, declaration := range file.file.Decls {
			decl, ok := declaration.(*ast.GenDecl)
			if !ok || decl.Tok != token.VAR {
				continue
			}
			binding, name, directive, err := stringEmbedDeclaration(file, decl, lookup)
			if err != nil {
				return nil, nil, err
			}
			data, err := snapshot.ReadFile(ctx, filepath.Join(source, filepath.FromSlash(name)), 1<<20)
			if err != nil {
				return nil, nil, errors.New("Go string fixture unavailable or exceeds bound")
			}
			total += len(data)
			if total > 8<<20 || len(globals) >= 128 {
				return nil, nil, errors.New("Go string fixtures exceed total bounds")
			}
			key := binding.ObjectVariant + ":" + binding.ObjectName
			if _, exists := globals[key]; exists {
				return nil, nil, errors.New("Go string fixture declaration duplicated")
			}
			globals[key], directives[directive] = binding, true
			if allowed[file.variant.Directory] == nil {
				allowed[file.variant.Directory] = map[string]string{}
			}
			allowed[file.variant.Directory][name] = digest(data)
			found := false
			for _, input := range inputs {
				if input.Owner == file.variant.ID && input.Path == name && stringEmbedKind(input.Kind) && input.Digest == digest(data) {
					found = true
				}
			}
			if !found {
				return nil, nil, errors.New("Go literal fixture absent from selected compiler inputs")
			}
			count++
		}
		blankEmbed := 0
		for _, imported := range file.file.Imports {
			name, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return nil, nil, err
			}
			if name == "embed" {
				if imported.Name == nil || imported.Name.Name != "_" || count == 0 {
					return nil, nil, errors.New("Go embed import outside literal string contract")
				}
				blankEmbed++
			}
		}
		if count > 0 && blankEmbed != 1 {
			return nil, nil, errors.New("Go string fixture requires one blank embed import")
		}
		for _, comments := range file.file.Comments {
			for _, comment := range comments.List {
				if (strings.HasPrefix(comment.Text, "//go:") || strings.HasPrefix(comment.Text, "// +build") || strings.Contains(comment.Text, "Code generated")) && !directives[comment] {
					return nil, nil, errors.New("Go unreviewed compiler directive or generator")
				}
			}
		}
	}
	owners := map[string]golang.ClosurePackage{}
	for _, p := range selected {
		owners[p.ID] = p
	}
	fixtures := []golang.ClosureInput{}
	for _, input := range inputs {
		owner, exists := owners[input.Owner]
		if !exists || !stringEmbedKind(input.Kind) {
			continue
		}
		if allowed[owner.Directory][input.Path] != input.Digest || input.Digest == "" {
			return nil, nil, errors.New("Go compiler embed input lacks literal declaration")
		}
		fixtures = append(fixtures, input)
	}
	return globals, fixtures, nil
}

func stringEmbedKind(kind string) bool {
	return kind == "embed" || kind == "test-embed" || kind == "external-test-embed"
}

func readOnlyStringEmbed(file dependencyFile, id *ast.Ident, binding golang.Binding, globals map[string]golang.Binding) bool {
	declaration, exists := globals[binding.ObjectVariant+":"+binding.ObjectName]
	if !exists || binding.Kind != "var" || !binding.PackageScope || binding.Type != "string" {
		return false
	}
	if binding.ObjectVariant == file.variant.ImportPath && (binding.DeclarationFile != declaration.DeclarationFile || binding.DeclarationDigest != declaration.DeclarationDigest || binding.DeclarationOffset != declaration.DeclarationOffset) {
		return false
	}
	var child ast.Node = id
	for parent := file.parents[child]; parent != nil; parent = file.parents[child] {
		if assignment, ok := parent.(*ast.AssignStmt); ok {
			for _, lhs := range assignment.Lhs {
				if lhs == child {
					return false
				}
			}
		}
		if _, ok := parent.(*ast.IncDecStmt); ok {
			return false
		}
		if _, ok := parent.(ast.Stmt); ok {
			break
		}
		child = parent
	}
	return true
}
