package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Cyberlane/hayaku/internal/capsule"
	"github.com/Cyberlane/hayaku/internal/inputbundle"
	"github.com/Cyberlane/hayaku/internal/metrics"
	"github.com/Cyberlane/hayaku/internal/model"
	nativerunner "github.com/Cyberlane/hayaku/internal/runner/native"
)

func writeToolFixture(t *testing.T, root, name string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
		t.Fatal(err)
	}
}
func tool(t *testing.T, root string, args ...string) (int, []byte, string) {
	t.Helper()
	args = append(args, "--root", root)
	var out, errout bytes.Buffer
	status := Run(context.Background(), args, &out, &errout)
	return status, out.Bytes(), errout.String()
}

func TestCapsuleCLIAuthenticatesPassAndInvalidatesChangedInputs(t *testing.T) {
	root := t.TempDir()
	// A module with one empty _start function; no external capabilities.
	module := []byte{0, 97, 115, 109, 1, 0, 0, 0, 1, 4, 1, 96, 0, 0, 3, 2, 1, 0, 5, 3, 1, 0, 1, 7, 19, 2, 6, 109, 101, 109, 111, 114, 121, 2, 0, 6, 95, 115, 116, 97, 114, 116, 0, 0, 10, 4, 1, 2, 0, 11}
	if err := os.WriteFile(filepath.Join(root, "suite.wasm"), module, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "generated.json"), []byte("private-input"), 0600); err != nil {
		t.Fatal(err)
	}
	c := capsuleConfig{Schema: 1, Name: "fixture", Module: "suite.wasm", Inputs: []string{"generated.json"}, Args: []string{"suite.wasm"}, Env: map[string]string{"PRIVATE_VALUE": "must-never-appear"}, ProducerIdentity: strings.Repeat("a", 64)}
	writeToolFixture(t, root, "capsule.json", c)
	var previous capsule.Result
	for index, want := range []string{"executed", "reused", "executed"} {
		if index == 2 {
			if err := os.WriteFile(filepath.Join(root, "generated.json"), []byte("changed"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		status, data, errout := tool(t, root, "capsule", "--config", "capsule.json", "--cache-dir", "cache")
		if status != 0 {
			t.Fatalf("status=%d %s %s", status, data, errout)
		}
		var r capsule.Result
		if err := json.Unmarshal(data, &r); err != nil {
			t.Fatal(err)
		}
		if r.Mode != want || !r.Qualified || !r.Complete || !r.Passed {
			t.Fatalf("%+v", r)
		}
		if index == 2 && r.Key == previous.Key {
			t.Fatal("changed generated input reused")
		}
		previous = r
		if bytes.Contains(data, []byte("must-never-appear")) || bytes.Contains(data, []byte("private-input")) {
			t.Fatal("private values in report")
		}
	}
	status, data, errout := tool(t, root, "capsule", "--config", "capsule.json", "--cache-dir", "cache", "--no-reuse")
	if status != 0 || !bytes.Contains(data, []byte(`"mode": "executed"`)) {
		t.Fatalf("audit %d %s %s", status, data, errout)
	}
	if status, _, _ := tool(t, root, "capsule", "--config", "capsule.json", "--baseline", "capsule.json"); status == 0 {
		t.Fatal("unrelated tool flag accepted")
	}
	if status, _, _ := tool(t, root, "capsule", "--config", "capsule.json", "--output", filepath.Join(root, "capsule.json")); status == 0 {
		t.Fatal("overwrote configuration")
	}
	// The same CLI can consume generated and linked inputs through an explicit
	// envelope; changing source metadata invalidates its authenticated receipt.
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "dependency")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "value.ts"), []byte("linked bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(target, "value.ts"), filepath.Join(source, "linked.ts")); err != nil {
		t.Skip("native symlink permission unavailable")
	}
	c.Inputs = nil
	c.InputEnvelope = &inputsConfig{Schema: 1, Roots: []inputRoot{{Name: "source", Path: "source", Mount: "source"}, {Name: "dependency", Path: "dependency", Mount: "dependency"}}, Inputs: []inputbundle.Input{{Root: "source", Path: "linked.ts", Generated: true}}}
	writeToolFixture(t, root, "capsule.json", c)
	var initial capsule.Result
	for index, want := range []string{"executed", "reused", "executed"} {
		if index == 2 {
			if err := os.Chmod(filepath.Join(target, "value.ts"), 0640); err != nil {
				t.Fatal(err)
			}
		}
		status, data, errout := tool(t, root, "capsule", "--config", "capsule.json", "--cache-dir", "envelope-cache")
		if status != 0 {
			t.Fatalf("envelope %d %s %s", status, data, errout)
		}
		var r capsule.Result
		if err := json.Unmarshal(data, &r); err != nil {
			t.Fatal(err)
		}
		if r.Mode != want {
			t.Fatalf("envelope mode %+v", r)
		}
		if index == 0 {
			initial = r
		}
		if index == 2 && r.Key == initial.Key {
			t.Fatal("changed captured metadata reused")
		}
	}
	c.Inputs = []string{"generated.json"}
	writeToolFixture(t, root, "capsule.json", c)
	if status, _, _ := tool(t, root, "capsule", "--config", "capsule.json"); status == 0 {
		t.Fatal("ambiguous envelope/list accepted")
	}
}

func TestAuxiliaryDocumentsCannotAssertQualification(t *testing.T) {
	root := t.TempDir()
	for _, data := range []string{`{"schema":1,"schema":1}`, `{"schema":1,"qualified":true}`, `{"schema":1} {"schema":1}`} {
		if err := os.WriteFile(filepath.Join(root, "bad.json"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if status, _, _ := tool(t, root, "capsule", "--config", "bad.json"); status == 0 {
			t.Fatalf("accepted %s", data)
		}
	}
}

func TestInputCLIProducesBoundedManifestAndRefusesOverwrite(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "generated.ts"), []byte("secret source contents"), 0600); err != nil {
		t.Fatal(err)
	}
	c := inputsConfig{Schema: 1, Roots: []inputRoot{{Name: "source", Path: "source", Mount: "."}}, Inputs: []inputbundle.Input{{Root: "source", Path: "generated.ts", Generated: true}}}
	writeToolFixture(t, root, "inputs.json", c)
	status, data, errout := tool(t, root, "inputs", "--config", "inputs.json", "--destination", "envelope")
	if status != 0 {
		t.Fatalf("%d %s", status, errout)
	}
	m, err := inputbundle.DecodeManifest(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Entries) != 2 || !m.Inputs[0].Generated || m.Digest == "" || bytes.Contains(data, []byte("secret source contents")) || bytes.Contains(data, []byte(source)) {
		t.Fatalf("invalid/private manifest %s", data)
	}
	if got, err := os.ReadFile(filepath.Join(root, "envelope", "generated.ts")); err != nil || string(got) != "secret source contents" {
		t.Fatalf("copy %q %v", got, err)
	}
	if status, _, _ := tool(t, root, "inputs", "--config", "inputs.json", "--destination", "envelope"); status == 0 {
		t.Fatal("overwrote materialization")
	}
}

func TestComparisonCLICountsNetOverheadAndRefusesChangedContext(t *testing.T) {
	root := t.TempDir()
	before := metrics.Run{Context: metrics.Context{SourceDigest: "source", InputsDigest: "inputs", RunnerDigest: "runner", EnvironmentDigest: "env", Platform: "fixture", SuiteSetDigest: "suite"}, WallNanos: 100, Phases: []metrics.Phase{{ID: "tests", Kind: "runner", DurationNanos: 100}}, Suites: []metrics.SuiteEvidence{{ID: "test", Execution: "executed", Outcome: "passed"}}}
	after := before
	after.WallNanos = 110
	after.Phases = []metrics.Phase{{ID: "capture", Kind: "hayaku", DurationNanos: 110}}
	after.Suites = []metrics.SuiteEvidence{{ID: "test", Execution: "hayaku-reused", Outcome: "passed"}}
	writeToolFixture(t, root, "before.json", before)
	writeToolFixture(t, root, "after.json", after)
	status, data, errout := tool(t, root, "compare", "--baseline", "before.json", "--measurement", "after.json")
	if status != 0 {
		t.Fatalf("%d %s", status, errout)
	}
	var comparison metrics.Comparison
	if err := json.Unmarshal(data, &comparison); err != nil {
		t.Fatal(err)
	}
	if !comparison.Valid || comparison.NetSavingsNanos != -10 || comparison.Candidate.HayakuOverheadNanos != 110 {
		t.Fatalf("%+v", comparison)
	}
	after.Context.SourceDigest = "different"
	writeToolFixture(t, root, "after.json", after)
	if status, _, _ := tool(t, root, "compare", "--baseline", "before.json", "--measurement", "after.json"); status == 0 {
		t.Fatal("different context credited")
	}
}

func TestResultsCLIRequiresExactInventoryAndDiscardsDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeToolFixture(t, root, "inventory.json", resultInventory{Schema: 1, Cases: []nativerunner.Case{{Unit: "jvm:class", ID: nativerunner.JUnitID("class", "test")}}})
	xml := `<testsuite tests="1" failures="0" errors="0" skipped="0"><testcase classname="class" name="test"><system-out>private source</system-out></testcase></testsuite>`
	if err := os.WriteFile(filepath.Join(root, "junit.xml"), []byte(xml), 0600); err != nil {
		t.Fatal(err)
	}
	status, data, errout := tool(t, root, "results", "--format", "junit", "--inventory", "inventory.json", "--report", "junit.xml")
	if status != 0 {
		t.Fatalf("%d %s", status, errout)
	}
	if bytes.Contains(data, []byte("private source")) {
		t.Fatal("raw native diagnostics leaked")
	}
	writeToolFixture(t, root, "inventory.json", resultInventory{Schema: 1, Cases: []nativerunner.Case{{Unit: "jvm:class", ID: nativerunner.JUnitID("class", "missing")}}})
	if status, _, _ := tool(t, root, "results", "--format", "junit", "--inventory", "inventory.json", "--report", "junit.xml"); status == 0 {
		t.Fatal("missing inventory accepted")
	}
}

func TestNextestInventoryCLIProducesUsableJUnitCases(t *testing.T) {
	if _, err := exec.LookPath("cargo-nextest"); err != nil {
		if os.Getenv("HAYAKU_NEXTEST_REQUIRED") == "1" {
			t.Fatal("required nextest unavailable")
		}
		t.Skip("installed nextest fixture unavailable")
	}
	root := t.TempDir()
	c := model.Config{Schema: 1, Context: model.Context{ID: "nextest-cli", OS: runtime.GOOS, Arch: runtime.GOARCH}, Workspaces: []model.Workspace{{ID: "rust", Root: ".", Adapter: "nextest", Command: model.Command{Dir: ".", Executable: "cargo", Args: []string{"nextest", "run", "--workspace", "--offline", "--locked"}}}}}
	writeToolFixture(t, root, "hayaku.json", c)
	nativeAppWrite(t, root, "Cargo.toml", "[package]\nname=\"inventory-fixture\"\nversion=\"0.1.0\"\nedition=\"2021\"\n")
	nativeAppWrite(t, root, "Cargo.lock", "version = 4\n[[package]]\nname = \"inventory-fixture\"\nversion = \"0.1.0\"\n")
	nativeAppWrite(t, root, "src/lib.rs", "#[test] fn works() { assert_eq!(2 + 2, 4); }\n")
	nativeAppGit(t, root, "init", "-q")
	nativeAppGit(t, root, "config", "user.name", "Fixture")
	nativeAppGit(t, root, "config", "user.email", "fixture@example.test")
	nativeAppGit(t, root, "config", "commit.gpgsign", "false")
	nativeAppCommit(t, root, "fixture")
	status, data, errout := tool(t, root, "inventory", "--workspace", "rust")
	if status != 0 {
		t.Fatalf("inventory %d %s %s", status, data, errout)
	}
	var list nativeInventory
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatal(err)
	}
	if list.Qualified || len(list.Cases) != 1 || len(list.Units) != 1 || list.SourceDigest == "" || bytes.Contains(data, []byte(root)) {
		t.Fatalf("invalid inventory %s", data)
	}
	writeToolFixture(t, root, "cases.json", list)
	var cases resultInventory
	if err := readCaseInventory(root, "cases.json", &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases.Cases) != 1 || cases.Cases[0] != list.Cases[0] {
		t.Fatal("native inventory not usable by result importer")
	}
	list.Cases[0].ID = "forged"
	writeToolFixture(t, root, "cases.json", list)
	if err := readCaseInventory(root, "cases.json", &cases); err == nil {
		t.Fatal("inconsistent native inventory accepted")
	}
}
