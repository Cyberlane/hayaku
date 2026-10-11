package golang

import (
	"context"
	"errors"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"go/version"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/Cyberlane/hayaku/internal/model"
)

// BindingKey names an identifier in compiler-selected source, not an export
// token position. Variants of the same file deliberately have different keys.
type BindingKey struct {
	Variant, File, SourceDigest string
	Offset                      int
}

// Binding is cloned descriptive evidence. It cannot authorize execution.
// Imported declarations have no invented source location: a certifier must
// reconcile their target variant, name and signature with captured local defs.
type Binding struct {
	Key                                          BindingKey
	Name, ObjectName, Kind, ObjectVariant, Type  string
	DeclarationFile, DeclarationDigest           string
	DeclarationOffset                            int
	ImportedVariant, ConstantKind, ConstantValue string
	Parameters, Results                          []string
	Receiver, Generic, Variadic                  bool
	PackageScope                                 bool `json:",omitempty"`
}

type typedFactTable struct {
	root, checker, compiler, arch string
	compatible                    bool
	facts                         map[BindingKey]Binding
}

func nativeClosureEnvironment(env []string, execution model.Context) bool {
	seen := map[string]bool{}
	for _, pair := range env {
		key, _, ok := strings.Cut(pair, "=")
		if !ok || key == "" || seen[key] {
			return false
		}
		seen[key] = true
	}
	for key, want := range map[string]string{"GOENV": "off", "GOFLAGS": "", "GOWORK": "off", "GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off", "GOVCS": "*:off", "CGO_ENABLED": "0", "GOEXPERIMENT": "", "GOOS": execution.OS, "GOARCH": execution.Arch} {
		if !seen[key] || environmentValue(env, key) != want {
			return false
		}
	}
	return (execution.OS == "linux" || execution.OS == "darwin") && (execution.Arch == "arm64" || execution.Arch == "amd64")
}

func moduleLanguageVersion(value string) string {
	value = "go" + value
	if !version.IsValid(value) {
		return ""
	}
	return version.Lang(value)
}

func typedCheckerCompatible(compiler, arch string, env []string) bool {
	// Version equality admits the reviewed standard checker implementation;
	// the complete selected compiler tree is separately bound and revalidated.
	if compiler != runtime.Version() || !version.IsValid(compiler) || compiler != "go1.27.1" || (arch != "arm64" && arch != "amd64") || environmentValue(env, "GOEXPERIMENT") != "" {
		return false
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "GOEXPERIMENT" && setting.Value != "" {
				return false
			}
		}
	}
	return true
}

func primitiveType(t types.Type) string {
	switch t := t.(type) {
	case *types.Basic:
		if t.Kind() != types.Invalid && t.Info()&(types.IsBoolean|types.IsInteger|types.IsFloat|types.IsString) != 0 && t.Info()&types.IsComplex == 0 {
			return t.Name()
		}
	case *types.Slice:
		if element := primitiveType(t.Elem()); element != "" {
			return "[]" + element
		}
	case *types.Array:
		if element := primitiveType(t.Elem()); element != "" && t.Len() <= 100000 {
			return "[" + strconv.FormatInt(t.Len(), 10) + "]" + element
		}
	case *types.Pointer:
		if named, ok := t.Elem().(*types.Named); ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "testing" && named.Obj().Name() == "T" {
			return "*testing.T"
		}
	}
	return ""
}

func (table *typedFactTable) retain(ctx context.Context, p ClosurePackage, fset *token.FileSet, info *types.Info) error {
	digests := map[string]string{}
	for name, data := range p.Sources {
		digests[name] = closureDigest(data)
	}
	for _, objects := range []map[*ast.Ident]types.Object{info.Defs, info.Uses} {
		for id, object := range objects {
			if err := ctx.Err(); err != nil {
				return err
			}
			if object == nil {
				continue
			}
			file := fset.File(id.Pos())
			name := sourceName(p, file.Name())
			_, exists := p.Sources[name]
			if !exists || !slices.Contains(p.ActiveSources, name) || len(table.facts) >= 100000 {
				return errors.New("Go typed facts exceed admitted source or binding limits")
			}
			key := BindingKey{p.ID, name, digests[name], file.Offset(id.Pos())}
			binding := Binding{Key: key, Name: id.Name, ObjectName: object.Name(), Type: primitiveType(object.Type()), DeclarationOffset: -1}
			if object.Pkg() != nil {
				binding.ObjectVariant = object.Pkg().Path()
				if mapped := p.ImportMap[binding.ObjectVariant]; mapped != "" {
					binding.ObjectVariant = mapped
				}
			}
			switch object := object.(type) {
			case *types.Func:
				binding.Kind = "func"
				sig := object.Type().(*types.Signature)
				binding.Receiver, binding.Generic, binding.Variadic = sig.Recv() != nil, sig.TypeParams().Len() != 0 || sig.RecvTypeParams().Len() != 0, sig.Variadic()
				for i := 0; i < sig.Params().Len(); i++ {
					binding.Parameters = append(binding.Parameters, primitiveType(sig.Params().At(i).Type()))
				}
				for i := 0; i < sig.Results().Len(); i++ {
					binding.Results = append(binding.Results, primitiveType(sig.Results().At(i).Type()))
				}
			case *types.Const:
				binding.Kind = "const"
				binding.ConstantKind = object.Val().Kind().String()
				if object.Val().Kind() != constant.Unknown && len(object.Val().ExactString()) <= 4096 {
					binding.ConstantValue = object.Val().ExactString()
				}
			case *types.Var:
				binding.Kind = "var"
				binding.PackageScope = object.Pkg() != nil && object.Parent() == object.Pkg().Scope()
			case *types.PkgName:
				binding.Kind, binding.ImportedVariant = "package", object.Imported().Path()
				if mapped := p.ImportMap[binding.ImportedVariant]; mapped != "" {
					binding.ImportedVariant = mapped
				}
			case *types.Builtin:
				binding.Kind = "builtin"
			case *types.TypeName:
				binding.Kind = "type"
			case *types.Nil:
				binding.Kind = "nil"
			default:
				binding.Kind = "unsupported"
			}
			// Local object positions belong to this check. Export positions may
			// belong to unrelated token files and are intentionally not exposed.
			if object.Pkg() != nil && object.Pkg().Path() == p.ImportPath && object.Pos().IsValid() {
				if declaration := fset.File(object.Pos()); declaration != nil {
					declName := sourceName(p, declaration.Name())
					if _, ok := p.Sources[declName]; ok && slices.Contains(p.ActiveSources, declName) {
						binding.DeclarationFile, binding.DeclarationDigest, binding.DeclarationOffset = declName, digests[declName], declaration.Offset(object.Pos())
					}
				}
			}
			if previous, duplicate := table.facts[key]; duplicate && closureDigest(previous) != closureDigest(binding) {
				return errors.New("ambiguous Go typed binding")
			}
			table.facts[key] = binding
		}
	}
	return nil
}

func sourceName(p ClosurePackage, filename string) string {
	prefix := p.Directory + "/"
	if p.Directory == "." {
		prefix = ""
	}
	return strings.TrimPrefix(filepath.ToSlash(filename), prefix)
}

func (table *typedFactTable) digest() string {
	values := make([]Binding, 0, len(table.facts))
	for _, value := range table.facts {
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool {
		a, b := values[i].Key, values[j].Key
		if a.Variant != b.Variant {
			return a.Variant < b.Variant
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Offset < b.Offset
	})
	return closureDigest(struct {
		Checker, Compiler, Architecture string
		Compatible                      bool
		Bindings                        []Binding
	}{table.checker, table.compiler, table.arch, table.compatible, values})
}

// LookupBindings provides cloned facts after independent before/after source,
// compiler, export and private-table checks. Failure returns no partial facts.
func (c Closure) LookupBindings(ctx context.Context, keys []BindingKey) ([]Binding, error) {
	if c.typed == nil || !c.typed.compatible || len(keys) == 0 || len(keys) > 100000 || c.TypedDigest != c.typed.digest() {
		return nil, errors.New("Go typed binding context unavailable")
	}
	if err := RevalidateClosure(ctx, c.typed.root, c); err != nil {
		return nil, err
	}
	result := make([]Binding, 0, len(keys))
	seen := map[BindingKey]bool{}
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		value, exists := c.typed.facts[key]
		if !exists || seen[key] {
			return nil, errors.New("Go typed binding key absent or duplicated")
		}
		seen[key] = true
		value.Parameters, value.Results = slices.Clone(value.Parameters), slices.Clone(value.Results)
		result = append(result, value)
	}
	if c.TypedDigest != c.typed.digest() {
		return nil, errors.New("Go typed binding table changed")
	}
	if err := RevalidateClosure(ctx, c.typed.root, c); err != nil {
		return nil, err
	}
	return result, nil
}
