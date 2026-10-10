package native

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func externalProofFixture() map[string][]byte {
	return map[string][]byte{
		"value.go":         []byte(`package sample; const factor=2; func Value(n int)int{return n*factor}`),
		"value_test.go":    []byte(`package sample; import "testing"; func helper()int{return Value(3)}; func TestInternal(t *testing.T){if helper()!=6{t.Fatal("internal")}}`),
		"external_test.go": []byte(`package sample_test; import "testing"; func helper()int{return 7}; func TestExternal(t *testing.T){if helper()!=7{t.Fatal("external")}}`),
	}
}

func TestExternalProofMixedIndependentNamespaces(t *testing.T) {
	files := externalProofFixture()
	cases, err := qualifyGo(files)
	if err != nil || !slices.Equal(cases, []string{"TestExternal", "TestInternal"}) {
		t.Fatalf("mixed independent inventory: %v %v", cases, err)
	}
	// The production helper and external helper intentionally have the same
	// name and different values. They belong to different Go namespaces.
	proof := goSourceProof(files)
	files["external_test.go"] = []byte(strings.Replace(string(files["external_test.go"]), "return 7", "return 8", 1))
	if proof == goSourceProof(files) {
		t.Fatal("external source bytes omitted from existing whole-package proof")
	}
	if cases, err = qualifyGo(files); err != nil || !slices.Equal(cases, []string{"TestExternal", "TestInternal"}) {
		t.Fatalf("pure source change must preserve case identity: %v %v", cases, err)
	}
}

func TestExternalProofProductionWithoutInternalTests(t *testing.T) {
	files := externalProofFixture()
	delete(files, "value_test.go")
	cases, err := qualifyGo(files)
	if err != nil || !slices.Equal(cases, []string{"TestExternal"}) {
		t.Fatalf("real production namespace still certified without own test: %v %v", cases, err)
	}
	files["value.go"] = []byte(`package sample; func Value()int{return 1}; func init(){}`)
	if _, err := qualifyGo(files); err == nil {
		t.Fatal("test-free production namespace startup escaped qualification")
	}
}

func TestExternalProofRejectsUnsupportedNamespacesAndDormantEffects(t *testing.T) {
	if _, err := qualifyGo(externalProofFixture()); err != nil {
		t.Fatalf("healthy mixed fixture: %v", err)
	}
	faults := []struct {
		name, file, source string
	}{
		{"foreign-package", "external_test.go", `package stranger; import "testing"; func TestExternal(t *testing.T){t.Fatal("wrong namespace")}`},
		{"external-production-file", "external.go", `package sample_test; func Uncalled()int{return 1}`},
		{"third-package", "third_test.go", `package third; import "testing"; func TestThird(t *testing.T){}`},
		{"duplicate-test", "external_test.go", `package sample_test; import "testing"; func TestInternal(t *testing.T){}`},
		{"subject-import", "external_test.go", `package sample_test; import("testing";"example.invalid/sample"); func TestExternal(t *testing.T){if sample.Value(3)!=6{t.Fatal("wrong")}}`},
		{"dormant-dependency", "dormant_test.go", `package sample_test; import "os"; func Uncalled(){os.ReadFile("fixture")}`},
		{"dormant-init", "dormant_test.go", `package sample_test; func init(){}`},
		{"dormant-TestMain", "dormant_test.go", `package sample_test; import "testing"; func TestMain(m *testing.M){}`},
		{"dormant-global", "dormant_test.go", `package sample_test; var state int`},
		{"dormant-thread", "dormant_test.go", `package sample_test; func Uncalled(){go helper()}`},
		{"dormant-function-value", "dormant_test.go", `package sample_test; func Uncalled(){f:=helper; _=f}`},
		{"dormant-testing-escape", "dormant_test.go", `package sample_test; import "testing"; func TestDormant(t *testing.T){other:=t; _=other}`},
		{"internal-dormant-effect", "dormant.go", `package sample; import "time"; func Uncalled()int64{return time.Now().UnixNano()}`},
		{"same-namespace-helper-collision", "duplicate.go", `package sample; func Value(n int)int{return n}`},
		{"external-shadowing", "dormant_test.go", `package sample_test; func Uncalled(){helper:=1; _=helper}`},
		{"external-directive", "dormant_test.go", "package sample_test\n//go:noinline\nfunc Uncalled(){}"},
		{"malformed-external-body", "dormant_test.go", `package sample_test; func Uncalled(){`},
	}
	for _, fault := range faults {
		t.Run(fault.name, func(t *testing.T) {
			files := externalProofFixture()
			files[fault.file] = []byte(fault.source)
			if _, err := qualifyGo(files); err == nil {
				t.Fatal("unsupported complete source admitted")
			}
		})
	}
}

func TestExternalProofRequiresCombinedRegisteredCases(t *testing.T) {
	for _, files := range []map[string][]byte{
		{},
		{"value.go": []byte(`package sample; func Value()int{return 1}`)},
		{"value.go": []byte(`package sample; func Value()int{return 1}`), "external_test.go": []byte(`package sample_test; func helper()int{return 2}`)},
	} {
		if _, err := qualifyGo(files); err == nil {
			t.Fatal("no registered test cases admitted")
		}
	}
	// Each namespace separately fits the existing limit. The combined binary
	// must still fit that same limit, rather than receiving twice the budget.
	files := map[string][]byte{}
	for index, pkg := range []string{"sample", "sample_test"} {
		var source strings.Builder
		fmt.Fprintf(&source, "package %s; import \"testing\";", pkg)
		for n := 0; n < 5001; n++ {
			fmt.Fprintf(&source, "func TestNamespace%dCase%d(t *testing.T){};", index, n)
		}
		data := []byte(source.String())
		files[pkg+"_test.go"] = data
		if cases, err := qualifyGo(map[string][]byte{pkg + "_test.go": data}); err != nil || len(cases) != 5001 {
			t.Fatalf("each namespace fits original inventory budget: %d %v", len(cases), err)
		}
	}
	if _, err := qualifyGo(files); err == nil {
		t.Fatal("combined native inventory budget exceeded")
	}
}

func TestExternalProofPreservesExistingSingleNamespace(t *testing.T) {
	for _, pkg := range []string{"sample", "sample_test"} {
		source := []byte(fmt.Sprintf(`package %s; import "testing"; func TestExisting(t *testing.T){if 2+3!=5{t.Fatal("wrong")}}`, pkg))
		cases, err := qualifyGo(map[string][]byte{"existing_test.go": source})
		if err != nil || !slices.Equal(cases, []string{"TestExisting"}) {
			t.Fatalf("existing single namespace: %v %v", cases, err)
		}
	}
}
