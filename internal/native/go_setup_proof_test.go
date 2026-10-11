package native

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/Cyberlane/hayaku/internal/adapter/golang"
)

// Parser controls exercise admission boundaries; actual compiler and issuer
// qualification is covered separately by the installed-runner corpus.
func parsedStringFixture(t *testing.T, source string) (dependencyFile, func(dependencyFile, *ast.Ident) (golang.Binding, bool)) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", source, parser.ParseComments|parser.AllErrors)
	if err != nil {
		t.Fatal(err)
	}
	p := golang.ClosurePackage{ID: "native-go:fixture", ImportPath: "fixture", Directory: "fixture", Sources: map[string][]byte{"fixture.go": []byte(source)}}
	parents := map[ast.Node]ast.Node{}
	stack := []ast.Node{}
	ast.Inspect(file, func(node ast.Node) bool {
		if node == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		if len(stack) > 0 {
			parents[node] = stack[len(stack)-1]
		}
		stack = append(stack, node)
		return true
	})
	f := dependencyFile{variant: p, name: "fixture.go", file: file, fset: fset, parents: parents, digest: digest([]byte(source))}
	declarations := map[string]int{}
	for _, declaration := range file.Decls {
		if decl, ok := declaration.(*ast.GenDecl); ok && decl.Tok == token.VAR {
			for _, value := range decl.Specs {
				for _, name := range value.(*ast.ValueSpec).Names {
					declarations[name.Name] = fset.File(name.Pos()).Offset(name.Pos())
				}
			}
		}
	}
	lookup := func(f dependencyFile, id *ast.Ident) (golang.Binding, bool) {
		offset, exists := declarations[id.Name]
		return golang.Binding{Key: golang.BindingKey{Variant: p.ID, File: f.name, SourceDigest: f.digest, Offset: fset.File(id.Pos()).Offset(id.Pos())}, Name: id.Name, ObjectName: id.Name, ObjectVariant: p.ImportPath, Kind: "var", Type: "string", PackageScope: true, DeclarationFile: f.name, DeclarationDigest: f.digest, DeclarationOffset: offset}, exists
	}
	return f, lookup
}

func TestGoStringEmbedContractAndCompilerInputIdentity(t *testing.T) {
	root := t.TempDir()
	write(t, root, "fixture/data.txt", "exact compiler fixture")
	f, lookup := parsedStringFixture(t, "package fixture\nimport _ \"embed\"\n//go:embed data.txt\nvar Fixture string\n")
	selected := map[string]golang.ClosurePackage{f.variant.ImportPath: f.variant}
	input := golang.ClosureInput{Owner: f.variant.ID, Path: "fixture/data.txt", Kind: "embed", Digest: digest([]byte("exact compiler fixture"))}
	for _, kind := range []string{"embed", "test-embed", "external-test-embed"} {
		input.Kind = kind
		globals, fixtures, err := certifyGoStringEmbeds(context.Background(), root, []dependencyFile{f}, []golang.ClosureInput{input}, selected, lookup)
		if err != nil || len(globals) != 1 || len(fixtures) != 1 || validateDependencyInputs([]golang.ClosureInput{input}, selected, fixtures) != nil {
			t.Fatal("literal selected fixture rejected", kind, err)
		}
	}
	for _, mutation := range []func(*golang.ClosureInput){
		func(i *golang.ClosureInput) { i.Digest = strings.Repeat("0", 64) },
		func(i *golang.ClosureInput) { i.Path = "fixture/other.txt" },
		func(i *golang.ClosureInput) { i.Owner = "another-owner" },
		func(i *golang.ClosureInput) { i.Kind = "native" },
	} {
		changed := input
		mutation(&changed)
		if _, _, err := certifyGoStringEmbeds(context.Background(), root, []dependencyFile{f}, []golang.ClosureInput{changed}, selected, lookup); err == nil {
			t.Fatal("uncaptured fixture acquired proof")
		}
	}
	extra := input
	extra.Path = "fixture/other.txt"
	if _, _, err := certifyGoStringEmbeds(context.Background(), root, []dependencyFile{f}, []golang.ClosureInput{input, extra}, selected, lookup); err == nil {
		t.Fatal("undeclared compiler fixture admitted")
	}
	write(t, root, "fixture/data.txt", strings.Repeat("x", (1<<20)+1))
	if _, _, err := certifyGoStringEmbeds(context.Background(), root, []dependencyFile{f}, []golang.ClosureInput{input}, selected, lookup); err == nil {
		t.Fatal("oversized fixture admitted")
	}
}

func TestGoStringEmbedRejectsUnsupportedSetup(t *testing.T) {
	for _, declaration := range []string{
		"var Fixture string", "//go:embed *.txt\nvar Fixture string", "//go:embed data.txt other.txt\nvar Fixture string",
		"//go:embed ../data.txt\nvar Fixture string", "//go:embed data.txt\nvar Fixture []byte", "//go:embed data.txt\nvar Fixture embed.FS",
		"//go:embed data.txt\nvar Fixture string = \"runtime\"", "//go:embed data.txt\nvar (Fixture string)",
		"//go:embed data.txt\n//go:embed other.txt\nvar Fixture string", "//go:embed data.txt\n//go:noinline\nvar Fixture string",
		"//go:embed \"data.txt\"\nvar Fixture string", "//go:embed all:data.txt\nvar Fixture string",
	} {
		t.Run(declaration, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, "fixture/data.txt", "fixture")
			f, lookup := parsedStringFixture(t, "package fixture\nimport _ \"embed\"\n"+declaration+"\n")
			input := golang.ClosureInput{Owner: f.variant.ID, Path: "fixture/data.txt", Kind: "embed", Digest: digest([]byte("fixture"))}
			if _, _, err := certifyGoStringEmbeds(context.Background(), root, []dependencyFile{f}, []golang.ClosureInput{input}, map[string]golang.ClosurePackage{f.variant.ImportPath: f.variant}, lookup); err == nil {
				t.Fatal("unsupported setup admitted")
			}
		})
	}
	root := t.TempDir()
	write(t, root, "fixture/data/item.txt", "directory fixture")
	f, lookup := parsedStringFixture(t, "package fixture\nimport _ \"embed\"\n//go:embed data\nvar Fixture string\n")
	input := golang.ClosureInput{Owner: f.variant.ID, Path: "fixture/data/item.txt", Kind: "embed", Digest: digest([]byte("directory fixture"))}
	if _, _, err := certifyGoStringEmbeds(context.Background(), root, []dependencyFile{f}, []golang.ClosureInput{input}, map[string]golang.ClosurePackage{f.variant.ImportPath: f.variant}, lookup); err == nil {
		t.Fatal("directory embed admitted")
	}
}

func TestGoStringEmbedRejectsEveryWriteBinding(t *testing.T) {
	for _, control := range []struct {
		body string
		read bool
	}{
		{"_ = Fixture", true}, {"return Fixture", true}, {"Fixture = \"changed\"", false}, {"Fixture += \"changed\"", false}, {"(Fixture) = \"changed\"", false}, {"_, Fixture = \"x\", \"changed\"", false}, {"if false { Fixture = \"changed\" }", false},
	} {
		f, lookup := parsedStringFixture(t, "package fixture\nvar Fixture string\nfunc value() string {"+control.body+"}\n")
		decl := f.file.Decls[0].(*ast.GenDecl).Specs[0].(*ast.ValueSpec).Names[0]
		binding, _ := lookup(f, decl)
		globals := map[string]golang.Binding{"fixture:Fixture": binding}
		count := 0
		ast.Inspect(f.file.Decls[1], func(node ast.Node) bool {
			if id, ok := node.(*ast.Ident); ok && id.Name == "Fixture" {
				count++
				use, _ := lookup(f, id)
				if got := readOnlyStringEmbed(f, id, use, globals); got != control.read {
					t.Fatal("fixture read/write misclassified", control.body, got)
				}
				use.PackageScope = false
				if readOnlyStringEmbed(f, id, use, globals) {
					t.Fatal("local shadow acquired global identity")
				}
			}
			return true
		})
		if count == 0 {
			t.Fatal("read/write control has no fixture binding")
		}
	}
}
