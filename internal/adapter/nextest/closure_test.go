package nextest

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/planner"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/runner"
	nativerunner "github.com/Cyberlane/hayaku/internal/runner/native"
)

func TestInstalledNextestSeededFailureReconcilesOriginalScope(t *testing.T) {
	root, w := stableNextestFixture(t)
	source := "#[cfg(test)] mod tests { #[test]fn basic(){assert_eq!(1,2);} #[test]fn other(){} #[test]#[ignore]fn ignored(){} }\n"
	if err := os.WriteFile(filepath.Join(root, "src", "lib.rs"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := ResolveClosure(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	expected, err := c.Native.ExpectedJUnitCases(c.Evidence().Units)
	if err != nil || len(expected) != 2 {
		t.Fatal("configured original scope unavailable", err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := process.Run(context.Background(), w.Command, root, map[string]string{"CARGO_NET_OFFLINE": "true", "RUSTUP_AUTO_INSTALL": "0", "CARGO_TARGET_DIR": filepath.Join(t.TempDir(), "run-target")})
	if err == nil || !out.Completed || out.ExitCode != 100 {
		t.Fatal("actual original command seeded fault did not fail")
	}
	data, err := os.ReadFile(filepath.Join(root, "target", "nextest", "ci", "junit.xml"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := nativerunner.ReconcileJUnit(bytes.NewReader(data), expected)
	if err != nil || !result.Failed || len(result.Tests) != 2 {
		t.Fatal("seeded native failure was lost during shared reconciliation", err)
	}
	failed, passed := 0, 0
	for _, test := range result.Tests {
		if test.Action == "fail" {
			failed++
		}
		if test.Action == "pass" {
			passed++
		}
	}
	if failed != 1 || passed != 1 {
		t.Fatal("independent pass/fail control not preserved")
	}
}

// Native nextest 0.9.146 creates empty profile-store directories even with a
// private --target-dir. This healthy context supplies them before capture; the
// separate mutation oracle verifies newly created stores remain fail-closed.
func stableNextestFixture(t *testing.T) (string, model.Workspace) {
	t.Helper()
	root, w := fixture(t)
	if err := os.MkdirAll(filepath.Join(root, "target", "nextest", "ci"), 0700); err != nil {
		t.Fatal(err)
	}
	return root, w
}

func TestInstalledNextestSharedBuildClosure(t *testing.T) {
	root, w := stableNextestFixture(t)
	if err := os.WriteFile(filepath.Join(root, "sibling.txt"), []byte("private sibling resource"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := ResolveClosure(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Native.InventoryComplete || c.Cargo.InventoryComplete || len(c.Native.Execution.Cases) != 2 || runner.ValidateInventory(c.Native.Execution) != nil || c.Native.BuildMetadataDigest == "" || c.Native.ToolchainDigest == "" || c.Native.Binaries[0].ArtifactDigest == "" {
		t.Fatal("configured shared artifact/case inventory incomplete")
	}
	if len(c.Evidence().Inputs["sibling.txt"]) != 1 {
		t.Fatal("shared conservative source ownership missing")
	}
	data, err := json.Marshal(c)
	if err != nil || strings.Contains(string(data), root) || strings.Contains(string(data), c.Cargo.PrivateTargetDirectory()) || strings.Contains(string(data), "private sibling resource") {
		t.Fatal("private build/source context leaked")
	}
	plan, err := planner.Build(nil, []model.Evidence{c.Evidence()}, nil, model.Config{Schema: model.Schema, Context: model.Context{ID: "nextest-closure"}, Workspaces: []model.Workspace{w}})
	if err != nil || len(plan.Proposed) != 1 {
		t.Fatal("unknown native influence narrowed empty diff", err)
	}
	if err := c.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.Native.Binaries[0].Cases[0].Name = "changed"
	if c.Verify(context.Background()) == nil {
		t.Fatal("caller native inventory mutation admitted")
	}
}

func TestInstalledNextestNativeMetadataRejectsContradictions(t *testing.T) {
	root, w := stableNextestFixture(t)
	c, err := ResolveClosure(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	a, err := parse(w)
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{"nextest", "list", "--message-format=json", "--offline", "--locked", "--target-dir", c.Cargo.PrivateTargetDirectory()}, a.list...)
	out, err := c.Cargo.RunExtension(context.Background(), "cargo-nextest", args)
	if err != nil || c.Verify(context.Background()) != nil {
		t.Fatal("healthy native protocol control", err)
	}
	var healthy map[string]any
	if json.Unmarshal(out.Stdout, &healthy) != nil {
		t.Fatal("native list malformed")
	}
	mutations := map[string]func(map[string]any){
		"count":           func(x map[string]any) { x["test-count"] = x["test-count"].(float64) + 1 },
		"target":          func(x map[string]any) { x["rust-build-meta"].(map[string]any)["target-directory"] = root },
		"build-directory": func(x map[string]any) { x["rust-build-meta"].(map[string]any)["build-directory"] = root },
		"producer": func(x map[string]any) {
			x["rust-build-meta"].(map[string]any)["build-script-out-dirs"] = map[string]any{"unknown-package": "debug/out"}
		},
		"package":       func(x map[string]any) { firstNativeSuite(x)["package-id"] = "unknown-package" },
		"cwd":           func(x map[string]any) { firstNativeSuite(x)["cwd"] = filepath.Dir(root) },
		"binary-target": func(x map[string]any) { firstNativeSuite(x)["binary-name"] = "unknown-target" },
		"artifact":      func(x map[string]any) { firstNativeSuite(x)["binary-path"] = filepath.Join(root, "src", "lib.rs") },
		"missing-cases": func(x map[string]any) { delete(firstNativeSuite(x), "testcases") },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			var altered map[string]any
			if json.Unmarshal(out.Stdout, &altered) != nil {
				t.Fatal("copy native control")
			}
			mutate(altered)
			data, err := json.Marshal(altered)
			if err != nil {
				t.Fatal(err)
			}
			candidate := Closure{Cargo: c.Cargo, Native: Inventory{Schema: 1, Version: NativeVersion}}
			candidate.Cargo.Inventory = model.ExecutionInventory{Schema: model.ExecutionSchema}
			if candidate.consume(context.Background(), data) == nil {
				t.Fatal("contradictory native protocol admitted")
			}
		})
	}
	if c.Verify(context.Background()) != nil {
		t.Fatal("negative corpus altered healthy source/context")
	}
}

func firstNativeSuite(x map[string]any) map[string]any {
	for _, suite := range x["rust-suites"].(map[string]any) {
		return suite.(map[string]any)
	}
	return nil
}

func TestInstalledNextestSourceMutationFallsBack(t *testing.T) {
	root, w := fixture(t)
	if err := os.RemoveAll(filepath.Join(root, "target")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "target")); !os.IsNotExist(err) {
		t.Fatal("mutation fixture did not begin with absent target")
	}
	if _, err := ResolveClosure(context.Background(), root, w, model.Context{}); err == nil || !strings.Contains(err.Error(), "source changed") {
		t.Fatal("native listing source mutation accepted", err)
	}
	info, err := os.Lstat(filepath.Join(root, "target", "nextest", "ci"))
	if err != nil || !info.IsDir() {
		t.Fatal("actual native profile creation was not observed")
	}
}

func TestInstalledNextestDeclaredPrivateStore(t *testing.T) {
	root, w := fixture(t)
	if err := os.RemoveAll(filepath.Join(root, "target")); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(t.TempDir(), "store")
	config := "[store]\ndir = " + strconv.Quote(store) + "\n[profile.ci]\ndefault-filter = \"all()\"\n[profile.ci.junit]\npath = \"junit.xml\"\n"
	if err := os.WriteFile(filepath.Join(root, ".config", "nextest.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := ResolveClosure(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := os.Lstat(filepath.Join(root, "target")); !os.IsNotExist(err) {
		t.Fatal("private declared store mutated source target")
	}
	if info, err := os.Lstat(filepath.Join(store, "ci")); err != nil || !info.IsDir() {
		t.Fatal("actual declared private profile store not observed", err)
	}
	if c.Verify(context.Background()) != nil || len(c.Native.Execution.Cases) != 2 || c.Native.InventoryComplete {
		t.Fatal("private healthy context not retained conservatively")
	}
}

func TestInstalledNextestZeroCaseScopeWithoutPlaceholder(t *testing.T) {
	root, w := stableNextestFixture(t)
	if err := os.WriteFile(filepath.Join(root, "src", "lib.rs"), []byte("pub fn value()->u8{1}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := ResolveClosure(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if len(c.Cargo.Scopes) != 1 || len(c.Evidence().Units) != 1 || len(c.Native.Execution.Units) != 0 || len(c.Native.Execution.Cases) != 0 || len(c.Native.Binaries) != 1 || len(c.Native.Binaries[0].Cases) != 0 {
		t.Fatal("zero-case source scope disappeared or placeholder case invented")
	}
}

func TestInstalledNextestPrivateArtifactDrift(t *testing.T) {
	root, w := stableNextestFixture(t)
	c, err := ResolveClosure(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	paths, err := filepath.Glob(filepath.Join(c.Cargo.PrivateTargetDirectory(), "debug", "deps", "native_nextest_fixture-*"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, name := range paths {
		info, err := os.Lstat(name)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			f, err := os.OpenFile(name, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.Write([]byte("detectable artifact mutation"))
			closeErr := f.Close()
			if err != nil || closeErr != nil {
				t.Fatal(err, closeErr)
			}
			found = true
			break
		}
	}
	if !found || c.Verify(context.Background()) == nil {
		t.Fatal("actual captured native artifact drift not rejected")
	}
}
