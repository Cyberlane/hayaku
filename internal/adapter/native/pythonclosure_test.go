package native

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/planner"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/runner"
	nativerunner "github.com/Cyberlane/hayaku/internal/runner/native"
	"github.com/Cyberlane/hayaku/internal/snapshot"
)

func pythonClosurePut(t *testing.T, root, path, source string) {
	t.Helper()
	filename := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
}

func pythonClosureFixture(t *testing.T, adapter string) (string, model.Workspace, model.Context) {
	t.Helper()
	root := t.TempDir()
	tool, args := pythonFixtureInterpreter(t, adapter), []string{"-m", "unittest", "discover", "-s", "tests", "-t", "."}
	files := map[string]string{
		"pkg/__init__.py":     "from .leaf import value\n",
		"pkg/leaf.py":         "def value(): return 4\n",
		"ns/helper.py":        "from pkg import leaf\ndef value(): return leaf.value()\n",
		"src/support.py":      "VALUE = 4\n",
		"tests/__init__.py":   "",
		"tests/test_value.py": "import unittest as u\nfrom ns import helper as h\nimport support\nclass Values(u.TestCase):\n def test_one(self): self.assertEqual(h.value(), support.VALUE)\n def test_two(self): self.assertEqual(h.value(), 4)\n",
		"unused.py":           "from pathlib import Path as P\nimport os as env\nimport time as clock\nimport random as entropy\nimport subprocess as child\nimport importlib as loader\nimport ctypes\ndef dormant(name):\n P('data/value.txt').read_text()\n P('optional.txt').exists()\n P('data').iterdir()\n env.getenv('PRIVATE_ENV_SENTINEL')\n clock.time()\n entropy.random()\n child.run(['PRIVATE_COMMAND_SENTINEL'])\n ctypes.CDLL(name)\n loader.import_module(name)\n loader.import_module('pkg.leaf')\n",
		"data/value.txt":      "PRIVATE_SOURCE_SENTINEL",
	}
	if adapter == "pytest" {
		args = []string{"-m", "pytest", "-q", "-p", "no:cacheprovider", "tests"}
		files["tests/test_value.py"] = "import pytest\nfrom ns import helper as h\nimport support\n@pytest.mark.parametrize('item',[1,2])\ndef test_parameter(item, base): assert h.value() == base == support.VALUE\ndef test_other(base): assert base == 4\n"
		files["conftest.py"] = "import pytest\n@pytest.fixture(scope='session')\ndef base(): return 4\n@pytest.fixture(autouse=True)\ndef prepared(base):\n yield\n"
	}
	for name, source := range files {
		pythonClosurePut(t, root, name, source)
	}
	w := model.Workspace{ID: "python", Root: ".", Adapter: adapter, Command: model.Command{Dir: ".", Executable: tool, Args: args}}
	c := model.Context{ID: "python-closure-fixture", Env: map[string]string{"PYTHONDONTWRITEBYTECODE": "1", "PYTHONPATH": "src", "PRIVATE": "PRIVATE_ENV_SENTINEL"}}
	return root, w, c
}

func pythonClosureHasGap(closure PythonClosure, code string) bool {
	for _, gap := range closure.Evidence.Gaps {
		if gap.Code == code {
			return true
		}
	}
	return false
}

func TestPythonClosureCapturedEncodingCookie(t *testing.T) {
	root, workspace, execution := pythonClosureFixture(t, "unittest")
	source := []byte("# coding: latin-1\nfrom pkg import leaf\nLABEL = 'caf\xe9'\n")
	if err := os.WriteFile(filepath.Join(root, "encoded.py"), source, 0600); err != nil {
		t.Fatal(err)
	}
	closure, err := CollectPythonClosure(context.Background(), root, workspace, execution)
	if err != nil || pythonClosureHasGap(closure, "source-syntax-or-encoding-unqualified") {
		t.Fatal("captured encoding-cookie bytes were not parsed", err)
	}
	want := sha256.Sum256(source)
	found := false
	for _, input := range closure.Sources {
		if input.Path == "encoded.py" && input.Digest == hex.EncodeToString(want[:]) {
			found = true
		}
	}
	if !found {
		t.Fatal("encoded source identity differs from captured raw bytes")
	}
	found = false
	for _, edge := range closure.Evidence.Edges {
		if edge.From == "python:python-source:pkg/leaf.py" && edge.To == "python:python-source:encoded.py" {
			found = true
		}
	}
	if !found {
		t.Fatal("encoding-cookie source lost its dormant import influence")
	}
}

func TestPythonClosureNativeInventoryAndDormantEnvelope(t *testing.T) {
	for _, adapter := range []string{"unittest", "pytest"} {
		t.Run(adapter, func(t *testing.T) {
			root, w, c := pythonClosureFixture(t, adapter)
			closure, err := CollectPythonClosure(context.Background(), root, w, c)
			if err != nil {
				t.Fatal(err)
			}
			selector, expected := "tests.test_value", []string{"tests.test_value.Values.test_one", "tests.test_value.Values.test_two"}
			if adapter == "pytest" {
				selector, expected = "tests/test_value.py", []string{"tests/test_value.py::test_parameter[1]", "tests/test_value.py::test_parameter[2]", "tests/test_value.py::test_other"}
			}
			unit := model.Unit{ID: "python:" + adapter + ":" + selector, Workspace: "python", Selector: selector, Kind: adapter + "-file"}
			inventory := model.ExecutionInventory{Schema: model.ExecutionSchema, Units: []model.Unit{unit}}
			for _, identity := range expected {
				inventory.Cases = append(inventory.Cases, model.Case{Unit: unit.ID, ID: identity})
			}
			if !reflect.DeepEqual(closure.Inventory, inventory) {
				t.Fatalf("independent native inventory differs: %+v", closure.Inventory)
			}
			out, executionErr := Execute(context.Background(), root, w, c, nil)
			if executionErr != nil {
				t.Fatal("independent original-scope execution failed", executionErr)
			}
			outcomes, err := nativerunner.Reconcile(bytes.NewReader(out.Stdout), adapter, closure.RunnerVersion, inventory.Units)
			common := model.Outcomes{Complete: err == nil, Failed: outcomes.Failed, Errors: outcomes.UnhandledErrors}
			for _, outcome := range outcomes.Units {
				common.Units = append(common.Units, model.Outcome{Unit: outcome.Unit, Action: outcome.Action})
			}
			for _, outcome := range outcomes.Tests {
				common.Tests = append(common.Tests, model.Outcome{Unit: outcome.Unit, Test: outcome.Test, Action: outcome.Action})
			}
			if err != nil || runner.ValidateExact(common, inventory, out.Completed, out.ExitCode) != nil || !runner.AllPassed(common, inventory) {
				t.Fatal("original native outcomes differ from independent fixture oracle", err)
			}
			if err := RevalidatePythonClosure(context.Background(), root, closure); err != nil {
				t.Fatal(err)
			}
			plan, err := planner.Build([]model.Evidence{closure.Evidence}, []model.Evidence{closure.Evidence}, nil, model.Config{Schema: model.Schema, Context: c, Workspaces: []model.Workspace{w}})
			if err != nil || plan.Mode != "full-fallback" || !reflect.DeepEqual(plan.Selected, inventory.Units) || !reflect.DeepEqual(plan.Commands, []model.Command{w.Command}) {
				t.Fatal("common planner failed to preserve original Python scope", err)
			}
			kinds := map[string]bool{}
			paths := map[string]bool{}
			for _, effect := range closure.Evidence.Effects {
				kinds[effect.Kind], paths[effect.Path] = true, true
			}
			for _, kind := range []string{"read", "exists", "directory", "unknown", "clock", "random", "subprocess", "native", "shared-state"} {
				if !kinds[kind] {
					t.Fatalf("dormant branch effect missing: %s", kind)
				}
			}
			for _, path := range []string{"data/value.txt", "optional.txt", "data"} {
				if !paths[path] {
					t.Fatalf("literal dormant input missing: %s", path)
				}
			}
			for _, code := range []string{"dynamic-import-unqualified", "runtime-unqualified", "installed-package-plugin-stdlib-native-unqualified", "static-import-resolution-unqualified"} {
				if !pythonClosureHasGap(closure, code) {
					t.Fatalf("missing conservative Python gap: %s", code)
				}
			}
			edges := map[[2]string]bool{}
			for _, edge := range closure.Evidence.Edges {
				edges[[2]string{edge.From, edge.To}] = true
			}
			for _, pair := range [][2]string{{"pkg/leaf.py", "pkg/__init__.py"}, {"pkg/leaf.py", "ns/helper.py"}, {"ns/helper.py", "tests/test_value.py"}, {"src/support.py", "tests/test_value.py"}} {
				if !edges[[2]string{"python:python-source:" + pair[0], "python:python-source:" + pair[1]}] {
					t.Fatalf("relative/namespace/configured import missing: %v", pair)
				}
			}
			if adapter == "pytest" {
				if len(closure.Fixtures) != 2 || closure.Fixtures[0].Scope != "session" || !closure.Fixtures[1].Autouse || !reflect.DeepEqual(closure.Fixtures[1].Dependencies, []string{"base"}) {
					t.Fatalf("fixture lifecycle/dependency identity missing: %+v", closure.Fixtures)
				}
			}
			encoded, err := json.Marshal(closure)
			if err != nil || strings.Contains(string(encoded), root) || strings.Contains(string(encoded), w.Command.Executable) || strings.Contains(string(encoded), "PRIVATE_") {
				t.Fatal("closure emitted private source/environment/host paths")
			}
		})
	}
}

func TestPythonClosureSourceMetadataContextAndCaseDrift(t *testing.T) {
	root, w, c := pythonClosureFixture(t, "unittest")
	closure, err := CollectPythonClosure(context.Background(), root, w, c)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"source-summary", "deleted-source-entry", "deleted-case", "duplicate-case", "version", "forged-recomputed-digest"} {
		t.Run(kind, func(t *testing.T) {
			// Roundtrip clones exported arrays while restoring private guard state.
			data, _ := json.Marshal(closure)
			changed := closure
			if err := json.Unmarshal(data, &changed); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "source-summary":
				changed.SourceDigest = strings.Repeat("0", 64)
			case "deleted-source-entry":
				changed.Sources = changed.Sources[1:]
			case "deleted-case":
				changed.Inventory.Cases = changed.Inventory.Cases[:1]
			case "duplicate-case":
				changed.Inventory.Cases[1] = changed.Inventory.Cases[0]
			case "version":
				changed.PythonVersion = "3.99.0"
			case "forged-recomputed-digest":
				changed.Inventory.Cases[0].ID = "forged"
				changed.Digest = ""
				changed.Digest = pythonDigest(changed)
			}
			if RevalidatePythonClosure(context.Background(), root, changed) == nil {
				t.Fatal("tampered Python closure retained evidence")
			}
		})
	}
	c.ID = "changed-declared-context"
	other, err := CollectPythonClosure(context.Background(), root, w, c)
	if err != nil || other.ContextDigest == closure.ContextDigest || other.Digest == closure.Digest || other.SourceDigest != closure.SourceDigest {
		t.Fatal("declared context identity not bound", err)
	}
	pythonClosurePut(t, root, "pkg/leaf.py", "def value(): return 5\n")
	if RevalidatePythonClosure(context.Background(), root, closure) == nil {
		t.Fatal("transitive source drift retained closure")
	}
	pythonClosurePut(t, root, "pkg/leaf.py", "def value(): return 4\n")
	pythonClosurePut(t, root, "tests/test_new.py", "import unittest\nclass Added(unittest.TestCase):\n def test_added(self): pass\n")
	added, err := CollectPythonClosure(context.Background(), root, w, c)
	if err != nil || len(added.Inventory.Cases) != 3 || added.Digest == other.Digest {
		t.Fatal("new independent case did not change inventory", err)
	}
	if err := os.Remove(filepath.Join(root, "tests/test_new.py")); err != nil {
		t.Fatal(err)
	}
	deleted, err := CollectPythonClosure(context.Background(), root, w, c)
	if err != nil || len(deleted.Inventory.Cases) != 2 {
		t.Fatal("deleted native case retained inventory", err)
	}
}

func TestPythonClosureCollectionFaultsRejectAfterHealthyBaseline(t *testing.T) {
	for _, fault := range []string{"unittest-import", "unittest-load-tests", "pytest-import", "pytest-conftest", "pytest-suppressed-partial-error", "pytest-source-drift", "pytest-duplicate", "pytest-custom-owner"} {
		t.Run(fault, func(t *testing.T) {
			adapter := "pytest"
			if strings.HasPrefix(fault, "unittest") {
				adapter = "unittest"
			}
			root, w, c := pythonClosureFixture(t, adapter)
			if _, err := CollectPythonClosure(context.Background(), root, w, c); err != nil {
				t.Fatal("healthy collection unavailable before fault", err)
			}
			switch fault {
			case "unittest-import", "pytest-import":
				pythonClosurePut(t, root, "tests/test_value.py", "raise RuntimeError('PRIVATE_FAULT_SENTINEL')\n")
			case "unittest-load-tests":
				pythonClosurePut(t, root, "tests/test_bad.py", "def load_tests(loader, tests, pattern): raise RuntimeError('PRIVATE_FAULT_SENTINEL')\n")
			case "pytest-conftest":
				pythonClosurePut(t, root, "conftest.py", "raise RuntimeError('PRIVATE_FAULT_SENTINEL')\n")
			case "pytest-suppressed-partial-error":
				pythonClosurePut(t, root, "tests/test_broken.py", "raise RuntimeError('PRIVATE_FAULT_SENTINEL')\n")
				pythonClosurePut(t, root, "conftest.py", "def pytest_sessionfinish(session, exitstatus): session.exitstatus = 0\n")
			case "pytest-source-drift":
				pythonClosurePut(t, root, "conftest.py", "from pathlib import Path\ndef pytest_collection_finish(session): Path('data/value.txt').write_text('changed')\n")
			case "pytest-duplicate":
				pythonClosurePut(t, root, "conftest.py", "def pytest_collection_finish(session): session.items.append(session.items[0])\n")
			case "pytest-custom-owner":
				pythonClosurePut(t, root, "tests/sample.case", "PRIVATE_CUSTOM_SOURCE")
				pythonClosurePut(t, root, "conftest.py", "import pytest\nclass Item(pytest.Item):\n def runtest(self): pass\nclass File(pytest.File):\n def collect(self): yield Item.from_parent(self, name='case')\ndef pytest_collect_file(file_path, parent):\n if file_path.suffix == '.case': return File.from_parent(parent, path=file_path)\n")
			}
			closure, err := CollectPythonClosure(context.Background(), root, w, c)
			if err == nil || len(closure.Inventory.Cases) != 0 || strings.Contains(err.Error(), "PRIVATE_FAULT_SENTINEL") || strings.Contains(err.Error(), root) {
				t.Fatal("collection/source fault returned usable inventory or leaked diagnostics")
			}
			if fault == "pytest-source-drift" {
				data, readErr := os.ReadFile(filepath.Join(root, "data/value.txt"))
				if readErr != nil || string(data) != "changed" {
					t.Fatal("source-drift fault was rejected before seeded mutation executed")
				}
			}
		})
	}
}

func TestPythonClosureContextEncodingAndDefensiveCapture(t *testing.T) {
	for _, field := range []string{"id", "prerequisite", "runtime"} {
		t.Run(field, func(t *testing.T) {
			root, w, c := pythonClosureFixture(t, "unittest")
			switch field {
			case "id":
				c.ID = "invalid\xff"
			case "prerequisite":
				w.Prerequisites = []model.Command{{Dir: ".", Executable: "prepare", Args: []string{"invalid\xff"}}}
			case "runtime":
				w.NodeRuntime = &model.NodeRuntime{Node: "invalid\xff"}
			}
			closure, err := CollectPythonClosure(context.Background(), root, w, c)
			if err == nil || !reflect.DeepEqual(closure, PythonClosure{}) {
				t.Fatal("lossy context returned descriptive evidence")
			}
		})
	}
	root, w, c := pythonClosureFixture(t, "unittest")
	w.Prerequisites = []model.Command{{Dir: ".", Executable: "prepare", Args: []string{"original"}}}
	w.NodeRuntime = &model.NodeRuntime{Node: "original-runtime-descriptor"}
	closure, err := CollectPythonClosure(context.Background(), root, w, c)
	if err != nil {
		t.Fatal(err)
	}
	w.Command.Args[2] = "changed-caller-argument"
	w.Prerequisites[0].Args[0] = "changed-caller-prerequisite"
	w.NodeRuntime.Node = "changed-caller-runtime"
	c.Env["PRIVATE"] = "changed-caller-environment"
	if closure.workspace.Command.Args[2] != "discover" || closure.workspace.Prerequisites[0].Args[0] != "original" || closure.workspace.NodeRuntime.Node != "original-runtime-descriptor" || closure.execution.Env["PRIVATE"] != "PRIVATE_ENV_SENTINEL" {
		t.Fatal("caller mutation altered captured private context")
	}
	if err := RevalidatePythonClosure(context.Background(), root, closure); err != nil {
		t.Fatal("defensive private replay context became invalid", err)
	}
}

func TestPythonClosureDormantFixtureScopePrivacy(t *testing.T) {
	root, w, c := pythonClosureFixture(t, "unittest")
	pythonClosurePut(t, root, "unused_fixture.py", "from pytest import fixture as f\n@f(scope='PRIVATE_SCOPE_SENTINEL')\ndef inactive(base): pass\n")
	closure, err := CollectPythonClosure(context.Background(), root, w, c)
	if err != nil || len(closure.Fixtures) != 1 || closure.Fixtures[0].Scope != "unknown" || !pythonClosureHasGap(closure, "dynamic-fixture-unqualified") {
		t.Fatal("dormant fixture scope was not normalized", err)
	}
	data, err := json.Marshal(closure)
	if err != nil || strings.Contains(string(data), "PRIVATE_SCOPE_SENTINEL") {
		t.Fatal("private source fixture literal emitted")
	}
}

func TestPythonClosureIdenticalBytesCannotRelocateContext(t *testing.T) {
	root, w, c := pythonClosureFixture(t, "unittest")
	c.Env["PYTHONPATH"] = ""
	pythonClosurePut(t, root, "tests/test_value.py", "import unittest\nclass Values(unittest.TestCase):\n def test_only(self): pass\n")
	closure, err := CollectPythonClosure(context.Background(), root, w, c)
	if err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(filepath.Join(other, relative), info.Mode().Perm())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(other, relative), data, info.Mode().Perm())
	})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := snapshot.Digest(context.Background(), other)
	if err != nil || digest != closure.SourceDigest {
		t.Fatal("relocation fixture does not have identical source bytes/modes", err)
	}
	if RevalidatePythonClosure(context.Background(), other, closure) == nil {
		t.Fatal("identical bytes authorized a different cwd/__file__ context")
	}
	second, err := CollectPythonClosure(context.Background(), other, w, c)
	if err != nil || second.ContextDigest == closure.ContextDigest || second.SourceDigest != closure.SourceDigest {
		t.Fatal("fresh relocated context identity was not distinct", err)
	}
}

func TestPythonClosureUnimportedUnknownSyntaxBroadens(t *testing.T) {
	root, w, c := pythonClosureFixture(t, "unittest")
	pythonClosurePut(t, root, "unused_broken.py", "def broken(:\n")
	closure, err := CollectPythonClosure(context.Background(), root, w, c)
	if err != nil || len(closure.Inventory.Cases) != 2 || !pythonClosureHasGap(closure, "source-syntax-or-encoding-unqualified") {
		t.Fatal("unimported unknown syntax lost independent collection or gap", err)
	}
	plan, err := planner.Build([]model.Evidence{closure.Evidence}, []model.Evidence{closure.Evidence}, nil, model.Config{Schema: model.Schema, Context: c, Workspaces: []model.Workspace{w}})
	if err != nil || plan.Mode != "full-fallback" || len(plan.Selected) != 1 {
		t.Fatal("unknown syntax did not preserve original workspace", err)
	}
}

func TestPythonClosureFinalCollectionItemsAndCanceledContext(t *testing.T) {
	root, w, c := pythonClosureFixture(t, "pytest")
	pythonClosurePut(t, root, "conftest.py", "import pytest\n@pytest.hookimpl(trylast=True)\ndef pytest_collection_finish(session): session.items = session.items[-1:]\n")
	closure, err := CollectPythonClosure(context.Background(), root, w, c)
	if err != nil || len(closure.Inventory.Cases) != 1 || closure.Inventory.Cases[0].ID != "tests/test_value.py::test_other" {
		t.Fatal("collector ignored final native hook mutation", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CollectPythonClosure(ctx, root, w, c); err == nil {
		t.Fatal("canceled Python closure accepted")
	}
	// Actual executable identity is captured; it is not the assignment's stale
	// recorded patch version or an AST source label.
	out, err := process.Run(context.Background(), model.Command{Executable: w.Command.Executable, Args: []string{"--version"}}, root, c.Env)
	if err != nil || !strings.Contains(string(out.Stdout), closure.PythonVersion) {
		t.Fatal("installed native interpreter identity differs", err)
	}
	ids := []string{}
	for _, source := range closure.Sources {
		ids = append(ids, source.Path)
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatal("bounded source membership ordering unstable")
	}
}
