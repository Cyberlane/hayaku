package golang

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/runner"
	goresults "github.com/Cyberlane/hayaku/internal/runner/golang"
)

func closurePut(t *testing.T, root, name, data string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func closureFixture(t *testing.T) (string, model.Workspace, model.Context) {
	t.Helper()
	root := t.TempDir()
	for name, data := range map[string]string{
		"go.mod":              "module closure.fixture\ngo 1.26.0\n",
		"leaf/leaf.go":        "package leaf;func Value()int{return 2}",
		"left/left.go":        `package left;import "closure.fixture/leaf";func Value()int{return leaf.Value()}`,
		"right/right.go":      `package right;import "closure.fixture/leaf";func Value()int{return leaf.Value()}`,
		"assets/assets.go":    "package assets\nimport \"embed\"\n//go:embed data/*.txt\nvar Files embed.FS\n",
		"assets/data/one.txt": "PRIVATE_FIXTURE_RESOURCE_SENTINEL",
		"app/app.go":          `package app;import("closure.fixture/left";"closure.fixture/right";"closure.fixture/assets");func Sum()int{return left.Value()+right.Value()};func Resource()any{return assets.Files};func ExampleProduction(){println("ordinary declaration")/* Output: ordinary declaration */}`,
		"app/app_test.go":     `package app;import tt "testing";func TestInternal(t *tt.T){if Sum()!=4{t.Fatal("wrong")}};func Test日本語(t *tt.T){if Sum()!=4{t.Fatal("wrong")}};func Testlower(t *tt.T){t.Fatal("must not be collected")};func OnlyForTests()int{return Sum()}`,
		"app/external_test.go": `package app_test;import("testing";"fmt";"closure.fixture/app");func TestExternal(t *testing.T){if app.OnlyForTests()!=4{t.Fatal("wrong")}};func ExampleSum(){fmt.Println(app.Sum())
// Output: 4
}`,
		"app/tag_off.go": "//go:build !special\npackage app;func Tagged()int{return 1}\n",
		"app/tag_on.go":  "//go:build special\npackage app;func Tagged()int{return 2}\n",
	} {
		closurePut(t, root, name, data)
	}
	w := model.Workspace{ID: "go", Root: ".", Adapter: "go", Command: model.Command{Dir: ".", Executable: "go", Args: []string{"test", "-vet=off", "-count=1", "./app"}}}
	c := model.Context{OS: runtime.GOOS, Arch: runtime.GOARCH, Env: map[string]string{"GOWORK": "off", "GOFLAGS": "", "CGO_ENABLED": "0"}}
	return root, w, c
}

func closureHasGap(c Closure, code string) bool {
	for _, gap := range c.Gaps {
		if gap.Code == code {
			return true
		}
	}
	return false
}

func TestCompilerClosureVariantsTypedInventoryAndNativeOracle(t *testing.T) {
	root, w, execution := closureFixture(t)
	c, err := ResolveClosure(context.Background(), root, w, execution)
	if err != nil {
		t.Fatal(err)
	}
	expected := []model.Case{{Unit: "go:closure.fixture/app", ID: "ExampleSum"}, {Unit: "go:closure.fixture/app", ID: "TestExternal"}, {Unit: "go:closure.fixture/app", ID: "TestInternal"}, {Unit: "go:closure.fixture/app", ID: "Test日本語"}}
	if !c.InventoryComplete || !reflect.DeepEqual(c.Inventory.Cases, expected) || runner.ValidateInventory(c.Inventory) != nil {
		t.Fatalf("independent compiler case inventory incomplete: %v complete %v", c.Inventory.Cases, c.InventoryComplete)
	}
	variants := 0
	local := map[string]ClosurePackage{}
	for _, p := range c.Packages {
		if p.Directory != "" && !p.TestBinary {
			local[p.ImportPath] = p
			if p.SemanticDigest == "" {
				t.Fatalf("local typed binding missing: %s", p.ImportPath)
			}
		}
		if p.ForTest == "closure.fixture/app" {
			variants++
		}
	}
	if variants < 2 || local["closure.fixture/leaf"].ID == "" || local["closure.fixture/left"].ID == "" || local["closure.fixture/right"].ID == "" {
		t.Fatal("transitive diamond or external test variant lost")
	}
	external := local["closure.fixture/app_test [closure.fixture/app.test]"]
	if !slices.Contains(external.Imports, "go:closure.fixture/app [closure.fixture/app.test]") {
		t.Fatalf("external test imports ignored compiler variant mapping: %v", external.Imports)
	}
	for _, p := range c.Packages {
		if p.TestBinary && !closureHasGap(c, "compiler-test-main-unqualified") {
			t.Fatal("generated test-main producer was implicitly qualified")
		}
	}
	if !closureHasGap(c, "runtime-unqualified") || !closureHasGap(c, "standard-library-runtime-unqualified") || !closureHasGap(c, "package-global-state-unqualified") {
		t.Fatal("metadata erased runtime/global/std gaps")
	}
	encoded, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "PRIVATE_FIXTURE_RESOURCE_SENTINEL") || strings.Contains(string(encoded), root) || strings.Contains(string(encoded), "must not be collected") {
		t.Fatal("source contents or absolute paths leaked into closure report")
	}
	env, err := ExecutionEnvironment(context.Background(), root, w, execution)
	if err != nil {
		t.Fatal(err)
	}
	command, err := Execution(w)
	if err != nil {
		t.Fatal(err)
	}
	out, err := process.RunEnvironment(context.Background(), command, root, env)
	if err != nil || !out.Completed || out.ExitCode != 0 {
		t.Fatal("independent full-command fixture failed")
	}
	full, err := goresults.Reconcile(strings.NewReader(string(out.Stdout)), []string{"closure.fixture/app"})
	if err != nil {
		t.Fatal(err)
	}
	actual := []string{}
	for _, test := range full.Tests {
		actual = append(actual, test.Test)
	}
	slices.Sort(actual)
	if !reflect.DeepEqual(actual, []string{"ExampleSum", "TestExternal", "TestInternal", "Test日本語"}) {
		t.Fatalf("native full-command cases differ from independently authored inventory: %v", actual)
	}
}

func TestCompilerClosureBindsTransitiveBuildAndEmbedMembership(t *testing.T) {
	root, w, execution := closureFixture(t)
	first, err := ResolveClosure(context.Background(), root, w, execution)
	if err != nil {
		t.Fatal(err)
	}
	closurePut(t, root, "leaf/leaf.go", "package leaf;func Value()int{return 3}")
	if RevalidateClosure(context.Background(), root, first) == nil {
		t.Fatal("transitive changed source retained closure")
	}
	second, err := ResolveClosure(context.Background(), root, w, execution)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest == second.Digest || !reflect.DeepEqual(first.Inventory, second.Inventory) {
		t.Fatal("transitive body change failed to bind identity independently of inventory")
	}
	closurePut(t, root, "assets/data/two.txt", "second resource")
	third, err := ResolveClosure(context.Background(), root, w, execution)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, input := range third.Inputs {
		if input.Path == "assets/data/two.txt" && input.Kind == "embed" {
			found = true
		}
	}
	if !found || second.Digest == third.Digest {
		t.Fatal("glob expansion/membership not captured")
	}
	for _, name := range []string{"assets/data/one.txt", "assets/data/two.txt"} {
		if err := os.Remove(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ResolveClosure(context.Background(), root, w, execution); err == nil {
		t.Fatal("missing embedded producer input accepted")
	}
}

func TestCompilerClosureBuildTagsAndDormantEffectsRemainConservative(t *testing.T) {
	root, w, execution := closureFixture(t)
	closurePut(t, root, "app/dormant_windows.go", "package app;import \"time\";func DormantClock()int64{return time.Now().Unix()}\n")
	closurePut(t, root, "left/generated.go", "// Code generated by independent fixture. DO NOT EDIT.\npackage left;var Mutable=1;func init(){Mutable++};func Dormant(){go func(){}()}\n")
	first, err := ResolveClosure(context.Background(), root, w, execution)
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"generated-provenance-unqualified", "package-startup-unqualified", "package-global-state-unqualified", "concurrency-unqualified"} {
		if !closureHasGap(first, code) {
			t.Fatalf("missing dormant effect gap %s", code)
		}
	}
	clock := false
	for _, effect := range first.Effects {
		if effect.Kind == "clock" {
			clock = true
		}
	}
	if !clock {
		t.Fatal("OS-excluded source clock disappeared from envelope")
	}
	w.Command.Args = []string{"test", "-tags=special", "-count=1", "./app"}
	second, err := ResolveClosure(context.Background(), root, w, execution)
	if err != nil {
		t.Fatal(err)
	}
	if first.ContextDigest == second.ContextDigest || first.Digest == second.Digest {
		t.Fatal("build tags did not invalidate compiler context")
	}
	for _, p := range second.Packages {
		if canonical(p.ImportPath) == "closure.fixture/app" && !p.TestBinary && !slices.Contains(p.ActiveSources, "tag_on.go") {
			t.Fatalf("compiler-selected tag source missing: %v", p.ActiveSources)
		}
	}
}

func TestCompilerClosureBindsDeclaredExecutionContext(t *testing.T) {
	root, w, execution := closureFixture(t)
	first, err := ResolveClosure(context.Background(), root, w, execution)
	if err != nil {
		t.Fatal(err)
	}
	execution.ID = "another-declared-execution"
	second, err := ResolveClosure(context.Background(), root, w, execution)
	if err != nil {
		t.Fatal(err)
	}
	if first.SourceDigest != second.SourceDigest || !reflect.DeepEqual(first.Inventory, second.Inventory) {
		t.Fatal("execution identity changed independent source or case inventory")
	}
	if first.ContextDigest == second.ContextDigest || first.Digest == second.Digest {
		t.Fatal("declared execution identity did not bind descriptor")
	}
}

func TestCompilerClosureInventoryGaps(t *testing.T) {
	for _, test := range []struct {
		name, file, data, code string
		args                   []string
	}{
		{"subtests", "app/sub_test.go", `package app;import "testing";func TestSub(t *testing.T){if false{t.Run("child",func(t *testing.T){})}}`, "dynamic-case-inventory", nil},
		{"duplicate-internal-external", "app/duplicate_test.go", `package app_test;import "testing";func TestInternal(t *testing.T){}`, "duplicate-case-inventory", nil},
		{"count-repeat", "", "", "execution-multiplicity-unqualified", []string{"test", "-count=2", "./app"}},
		{"cpu-repeat", "", "", "execution-multiplicity-unqualified", []string{"test", "-cpu=1,2", "./app"}},
		{"no-test-scopes", "", "", "empty-case-inventory", []string{"test", "./..."}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, w, execution := closureFixture(t)
			if test.file != "" {
				closurePut(t, root, test.file, test.data)
			}
			if test.args != nil {
				w.Command.Args = test.args
			}
			c, err := ResolveClosure(context.Background(), root, w, execution)
			if err != nil {
				t.Fatal(err)
			}
			if !closureHasGap(c, test.code) || c.InventoryComplete {
				t.Fatalf("incomplete inventory silently became exact: %s complete %v", test.code, c.InventoryComplete)
			}
			if test.name == "duplicate-internal-external" {
				env, err := ExecutionEnvironment(context.Background(), root, w, execution)
				if err != nil {
					t.Fatal(err)
				}
				command, err := Execution(w)
				if err != nil {
					t.Fatal(err)
				}
				out, err := process.RunEnvironment(context.Background(), command, root, env)
				if err != nil || out.ExitCode != 0 {
					t.Fatal("actual duplicate-case fixture failed")
				}
				decoder := json.NewDecoder(strings.NewReader(string(out.Stdout)))
				occurrences := 0
				for {
					var event struct{ Test, Action string }
					err := decoder.Decode(&event)
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					if event.Test == "TestInternal" && event.Action == "pass" {
						occurrences++
					}
				}
				if occurrences != 2 {
					t.Fatal("independent Go command did not execute both same-name variants")
				}
			}
		})
	}
}

func TestCompilerClosureTestMainTypedSignatures(t *testing.T) {
	for _, custom := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary-test", true: "custom-startup"}[custom], func(t *testing.T) {
			root, w, execution := closureFixture(t)
			data := `package app;import "testing";func TestMain(t *testing.T){if Sum()!=4{t.Fatal("wrong")}}`
			if custom {
				data = `package app;import "testing";func TestMain(m *testing.M){panic("metadata must never execute startup")}`
			}
			closurePut(t, root, "app/main_test.go", data)
			c, err := ResolveClosure(context.Background(), root, w, execution)
			if err != nil {
				t.Fatal(err)
			}
			found := slices.Contains(c.Inventory.Cases, model.Case{Unit: "go:closure.fixture/app", ID: "TestMain"})
			if found == custom {
				t.Fatal("typed TestMain signature lost ordinary test or invented startup case")
			}
			if custom && !closureHasGap(c, "package-startup-unqualified") {
				t.Fatal("custom startup source silently qualified")
			}
		})
	}
}

func TestCompilerClosureLocalReplacementWorkspaceAndExternalGap(t *testing.T) {
	root, w, execution := closureFixture(t)
	closurePut(t, root, "replacement/go.mod", "module replacement.fixture\ngo 1.26.0\n")
	closurePut(t, root, "replacement/value.go", "package replacement;func Value()int{return 2}")
	closurePut(t, root, "go.mod", "module closure.fixture\ngo 1.26.0\nrequire replacement.fixture v0.0.0\nreplace replacement.fixture => ./replacement\n")
	closurePut(t, root, "leaf/leaf.go", `package leaf;import "replacement.fixture";func Value()int{return replacement.Value()}`)
	c, err := ResolveClosure(context.Background(), root, w, execution)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range c.Packages {
		if p.ImportPath == "replacement.fixture" && p.Directory == "replacement" && p.SemanticDigest != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("snapshot-contained module replacement lost typed provenance")
	}
	closurePut(t, root, "go.work", "go 1.26.0\nuse (\n.\n./replacement\n)\n")
	execution.Env["GOWORK"] = ""
	work, err := ResolveClosure(context.Background(), root, w, execution)
	if err != nil {
		t.Fatal(err)
	}
	if c.ContextDigest == work.ContextDigest {
		t.Fatal("workspace resolution context not bound")
	}
	external := t.TempDir()
	closurePut(t, external, "go.mod", "module replacement.fixture\ngo 1.26.0\n")
	closurePut(t, external, "value.go", "package replacement;func Value()int{return 2}")
	execution.Env["GOWORK"] = "off"
	closurePut(t, root, "go.mod", "module closure.fixture\ngo 1.26.0\nrequire replacement.fixture v0.0.0\nreplace replacement.fixture => "+filepath.ToSlash(external)+"\n")
	outside, err := ResolveClosure(context.Background(), root, w, execution)
	if err != nil {
		t.Fatal(err)
	}
	if !closureHasGap(outside, "external-source") || outside.InventoryComplete {
		t.Fatal("outside replacement silently entered immutable source authority")
	}
}

func TestCompilerClosureOriginalVendorResolution(t *testing.T) {
	root, w, execution := closureFixture(t)
	closurePut(t, root, "go.mod", "module closure.fixture\ngo 1.26.0\nrequire vendor.fixture v1.0.0\n")
	closurePut(t, root, "vendor/modules.txt", "# vendor.fixture v1.0.0\n## explicit; go 1.26.0\nvendor.fixture\n")
	closurePut(t, root, "vendor/vendor.fixture/value.go", "package vendor;func Value()int{return 2}")
	closurePut(t, root, "leaf/leaf.go", `package leaf;import "vendor.fixture";func Value()int{return vendor.Value()}`)
	c, err := ResolveClosure(context.Background(), root, w, execution)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range c.Packages {
		if p.ImportPath == "vendor.fixture" && p.Directory == "vendor/vendor.fixture" && p.SemanticDigest != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("native automatic vendoring was bypassed by readonly module override")
	}
	closurePut(t, root, "go.mod", "module closure.fixture\ngo 1.13\nrequire vendor.fixture v1.0.0\n")
	if _, err := ResolveClosure(context.Background(), root, w, execution); err == nil {
		t.Fatal("old module version silently inherited newer automatic vendoring")
	}
	closurePut(t, root, "go.mod", "module closure.fixture\ngo 1.26.0\nrequire vendor.fixture v1.0.0\n")
	w.Command.Args = []string{"test", "-mod=readonly", "./app"}
	if _, err := ResolveClosure(context.Background(), root, w, execution); err == nil {
		t.Fatal("explicit readonly module mode overwritten with vendor or dependency fetched")
	}
}

func TestCompilerClosureOpaqueGoEmbedResource(t *testing.T) {
	root, w, execution := closureFixture(t)
	closurePut(t, root, "assets/template.go", "package assets\nimport _ \"embed\"\n//go:embed templates/sample.go\nvar Template string\n")
	closurePut(t, root, "assets/templates/sample.go", "OPAQUE_TEMPLATE_IS_NOT_PARSEABLE_GO_SOURCE")
	c, err := ResolveClosure(context.Background(), root, w, execution)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, input := range c.Inputs {
		if input.Path == "assets/templates/sample.go" && input.Kind == "embed" {
			found = true
		}
	}
	if !found || closureHasGap(c, "source-parse-unqualified") {
		t.Fatal("opaque embedded .go resource lost input identity or became package source")
	}
	for _, p := range c.Packages {
		if _, exists := p.Sources["templates/sample.go"]; exists {
			t.Fatal("opaque asset entered private certifier source map")
		}
	}
	if err := RevalidateClosure(context.Background(), root, c); err != nil {
		t.Fatal(err)
	}
}

func TestCompilerClosureCaseChangesTypeErrorsAndPrivateCopyDrift(t *testing.T) {
	root, w, execution := closureFixture(t)
	first, err := ResolveClosure(context.Background(), root, w, execution)
	if err != nil {
		t.Fatal(err)
	}
	closurePut(t, root, "app/added_test.go", `package app;import "testing";func TestAdded(t *testing.T){}`)
	second, err := ResolveClosure(context.Background(), root, w, execution)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Inventory.Cases) != len(first.Inventory.Cases)+1 {
		t.Fatal("new test case not independently inventoried")
	}
	if err := os.Remove(filepath.Join(root, "app/added_test.go")); err != nil {
		t.Fatal(err)
	}
	third, err := ResolveClosure(context.Background(), root, w, execution)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Inventory, third.Inventory) {
		t.Fatal("deleted test remained in current inventory")
	}
	for _, p := range third.Packages {
		if p.ImportPath == "closure.fixture/leaf" {
			p.Sources["leaf.go"][0] = 'X'
		}
	}
	if RevalidateClosure(context.Background(), root, third) == nil {
		t.Fatal("mutated private source copy remained trusted")
	}
	closurePut(t, root, "leaf/leaf.go", "package leaf;func Value()int{return \"wrong type\"}")
	if _, err := ResolveClosure(context.Background(), root, w, execution); err == nil || strings.Contains(err.Error(), "wrong type") {
		t.Fatal("compiler/type failure accepted or source diagnostic exposed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ResolveClosure(ctx, root, w, execution); err == nil {
		t.Fatal("canceled capture accepted")
	}
}

func TestCompilerClosurePrivateSourceMembershipCannotShrink(t *testing.T) {
	for _, whole := range []bool{false, true} {
		t.Run(map[bool]string{false: "deleted-file", true: "deleted-map"}[whole], func(t *testing.T) {
			root, w, execution := closureFixture(t)
			c, err := ResolveClosure(context.Background(), root, w, execution)
			if err != nil {
				t.Fatal(err)
			}
			for i := range c.Packages {
				if c.Packages[i].ImportPath == "closure.fixture/leaf" {
					if whole {
						c.Packages[i].Sources = nil
					} else {
						delete(c.Packages[i].Sources, "leaf.go")
					}
				}
			}
			if RevalidateClosure(context.Background(), root, c) == nil {
				t.Fatal("deleted certifier inputs retained immutable closure identity")
			}
		})
	}
}
