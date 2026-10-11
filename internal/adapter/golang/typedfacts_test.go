package golang

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/Cyberlane/hayaku/internal/snapshot"
)

func retainedFactsFixture(t *testing.T) (Closure, []BindingKey) {
	t.Helper()
	root := t.TempDir()
	data := []byte("package sample;const Factor=2;func Add(a,b int)int{return a+b};func Value()int{return Add(Factor,3)}")
	if err := os.WriteFile(filepath.Join(root, "lib.go"), data, 0600); err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "lib.go", data, parser.AllErrors)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}}
	config := types.Config{GoVersion: "go1.26", Sizes: types.SizesFor("gc", runtime.GOARCH)}
	if _, err := config.Check("fixture/sample", fset, []*ast.File{file}, info); err != nil {
		t.Fatal(err)
	}
	p := ClosurePackage{ID: "go:fixture/sample", ImportPath: "fixture/sample", Directory: ".", ActiveSources: []string{"lib.go"}, Sources: map[string][]byte{"lib.go": data}}
	table := &typedFactTable{root: root, checker: runtime.Version(), compiler: runtime.Version(), arch: runtime.GOARCH, compatible: true, facts: map[BindingKey]Binding{}}
	if err := table.retain(context.Background(), p, fset, info); err != nil {
		t.Fatal(err)
	}
	sourceDigest, err := snapshot.Digest(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	c := Closure{Version: ClosureVersion, SourceDigest: sourceDigest, Packages: []ClosurePackage{p}, Inputs: []ClosureInput{{Owner: p.ID, Path: "lib.go", Kind: "active-go", Digest: closureDigest(data)}}, typed: table, TypedDigest: table.digest()}
	c.Digest = closureDigest(c)
	keys := []BindingKey{}
	for key, b := range table.facts {
		if b.Kind == "func" && b.Name == "Add" {
			keys = append(keys, key)
		}
	}
	if len(keys) != 2 {
		t.Fatal("fixture definition/use inventory differs")
	}
	return c, keys
}

func TestRetainedTypedBindingsCloneAndExactSourceLocations(t *testing.T) {
	c, keys := retainedFactsFixture(t)
	values, err := c.LookupBindings(context.Background(), keys)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range values {
		if b.Name != "Add" || b.ObjectVariant != "fixture/sample" || b.DeclarationFile != "lib.go" || b.DeclarationDigest != b.Key.SourceDigest || b.DeclarationOffset < 0 || !slices.Equal(b.Parameters, []string{"int", "int"}) || !slices.Equal(b.Results, []string{"int"}) {
			t.Fatalf("typed local identity incomplete: %+v", b)
		}
	}
	values[0].Parameters[0] = "forged"
	second, err := c.LookupBindings(context.Background(), keys)
	if err != nil || second[0].Parameters[0] != "int" {
		t.Fatal("returned facts alias private table")
	}
	for _, change := range []string{"variant", "source", "offset", "duplicate"} {
		bad := slices.Clone(keys[:1])
		switch change {
		case "variant":
			bad[0].Variant += " [test]"
		case "source":
			bad[0].SourceDigest = "forged"
		case "offset":
			bad[0].Offset++
		case "duplicate":
			bad = append(bad, bad[0])
		}
		if result, err := c.LookupBindings(context.Background(), bad); err == nil || result != nil {
			t.Fatalf("%s key accepted partial facts", change)
		}
	}
}

func TestRetainedTypedBindingsRejectSourceSummaryAndPrivateTableDrift(t *testing.T) {
	for _, kind := range []string{"source", "source-copy", "summary", "table", "cancellation"} {
		t.Run(kind, func(t *testing.T) {
			c, keys := retainedFactsFixture(t)
			ctx := context.Background()
			switch kind {
			case "source":
				if err := os.WriteFile(filepath.Join(c.typed.root, "lib.go"), []byte("package changed"), 0600); err != nil {
					t.Fatal(err)
				}
			case "source-copy":
				c.Packages[0].Sources["lib.go"][0] = 'X'
			case "summary":
				c.Packages[0].ImportPath = "forged"
			case "table":
				b := c.typed.facts[keys[0]]
				b.Results = []string{"string"}
				c.typed.facts[keys[0]] = b
			case "cancellation":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if result, err := c.LookupBindings(ctx, keys); err == nil || result != nil {
				t.Fatal("drift returned typed facts")
			}
		})
	}
}

func TestTypedCheckerAdmissionAndPrimitiveSignatures(t *testing.T) {
	if moduleLanguageVersion("1.26.0") != "go1.26" || moduleLanguageVersion("garbage") != "" || moduleLanguageVersion("") != "" {
		t.Fatal("module language was guessed")
	}
	for _, compiler := range []string{"go1.26.0", "go1.99.1", "devel go1.27"} {
		if typedCheckerCompatible(compiler, "arm64", nil) {
			t.Fatal("unknown checker/compiler pair admitted")
		}
	}
	if typedCheckerCompatible(runtime.Version(), "unknown", nil) || typedCheckerCompatible(runtime.Version(), runtime.GOARCH, []string{"GOEXPERIMENT=unreviewed"}) {
		t.Fatal("unsupported checker context admitted")
	}
	if primitiveType(types.NewMap(types.Typ[types.String], types.Typ[types.Int])) != "" || primitiveType(types.NewArray(types.Typ[types.Int], 100001)) != "" || primitiveType(types.NewSlice(types.Typ[types.Int])) != "[]int" {
		t.Fatal("nonprimitive signature admitted")
	}
}
