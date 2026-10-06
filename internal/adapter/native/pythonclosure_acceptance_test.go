package native

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/planner"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/runner"
	nativerunner "github.com/Cyberlane/hayaku/internal/runner/native"
)

func pythonAcceptanceFixture(t *testing.T, adapter string, files map[string]string) (string, model.Workspace, model.Context) {
	t.Helper()
	python := pythonFixtureInterpreter(t, adapter)
	root := t.TempDir()
	for name, source := range files {
		pythonAcceptanceWrite(t, root, name, source)
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"-m", "unittest", "discover", "-p", "test_*.py"}
	if adapter == "pytest" {
		args = []string{"-m", "pytest", "-p", "no:cacheprovider", "-q"}
	}
	workspace := model.Workspace{ID: "native", Root: ".", Adapter: adapter, Command: model.Command{Dir: ".", Executable: python, Args: args}}
	execution := model.Context{Env: map[string]string{"PYTHONDONTWRITEBYTECODE": "1", "PYTEST_DISABLE_PLUGIN_AUTOLOAD": "1"}}
	return root, workspace, execution
}

func pythonFixtureInterpreter(t *testing.T, adapter string) string {
	t.Helper()
	python := os.Getenv("HAYAKU_NATIVE_UNITTEST_PYTHON")
	if adapter == "pytest" {
		python = os.Getenv("HAYAKU_NATIVE_PYTHON")
		if python == "" {
			t.Skip("explicit installed pytest interpreter unavailable")
		}
	} else if python == "" {
		var err error
		python, err = exec.LookPath("python3")
		if err != nil {
			t.Skip("installed unittest interpreter unavailable")
		}
	}
	return python
}

func pythonAcceptanceWrite(t *testing.T, root, name, source string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPythonAcceptanceRejectsLossyNativeCaseIdentity(t *testing.T) {
	source := "import unittest\nclass Values(unittest.TestCase):\n def id(self): return 'custom_case'\n def test_value(self): self.assertEqual(1, 1)\n"
	root, workspace, execution := pythonAcceptanceFixture(t, "unittest", map[string]string{"test_value.py": source})
	healthy, err := CollectPythonClosure(context.Background(), root, workspace, execution)
	if err != nil || len(healthy.Inventory.Cases) != 1 || healthy.Inventory.Cases[0].ID != "custom_case" {
		t.Fatal("healthy custom native identity unavailable before fault", err)
	}
	pythonAcceptanceWrite(t, root, "test_value.py", strings.Replace(source, "custom_case", "case_\\ud800", 1))
	broken, err := CollectPythonClosure(context.Background(), root, workspace, execution)
	if err == nil || !reflect.DeepEqual(broken, PythonClosure{}) {
		t.Fatal("lossy surrogate identity produced usable native evidence")
	}
}

func TestPythonAcceptancePinnedProtocolEnvironment(t *testing.T) {
	t.Setenv("HAYAKU_ACCEPTANCE_AMBIENT", "parent-only")
	t.Setenv("HAYAKU_ACCEPTANCE_PINNED", "parent-value")
	root, workspace, execution := pythonAcceptanceFixture(t, "unittest", map[string]string{
		"test_env.py": "import os, unittest\nassert 'HAYAKU_ACCEPTANCE_AMBIENT' not in os.environ\nassert os.environ.get('HAYAKU_ACCEPTANCE_PINNED') == 'captured-value'\nclass Environment(unittest.TestCase):\n def test_env(self): pass\n",
	})
	parsed, err := parse(workspace)
	if err != nil {
		t.Fatal(err)
	}
	environment := []string{"HAYAKU_ACCEPTANCE_PINNED=captured-value"}
	wantEnvironment := append([]string(nil), environment...)
	collected, output, err := invokeEnvironment(context.Background(), root, root, workspace, execution, parsed, "discover", nil, environment)
	if err != nil || !output.Completed || output.ExitCode != 0 || !collected.Complete || len(collected.Items) != 1 || !reflect.DeepEqual(collected.Items[0].Tests, []string{"test_env.Environment.test_env"}) {
		t.Fatalf("explicit environment did not reach healthy collection: %v", err)
	}
	if !reflect.DeepEqual(environment, wantEnvironment) {
		t.Fatal("native protocol mutated caller environment")
	}
	pythonAcceptanceWrite(t, root, "test_env.py", "import os, unittest\nassert 'HAYAKU_ACCEPTANCE_AMBIENT' not in os.environ\nassert 'HAYAKU_ACCEPTANCE_PINNED' not in os.environ\nclass Environment(unittest.TestCase):\n def test_env(self): pass\n")
	collected, _, err = invokeEnvironment(context.Background(), root, root, workspace, execution, parsed, "discover", nil, []string{})
	if err != nil || !collected.Complete || len(collected.Items) != 1 || !reflect.DeepEqual(collected.Items[0].Tests, []string{"test_env.Environment.test_env"}) {
		t.Fatalf("empty explicit environment inherited ambient values: %v", err)
	}
}

func pythonAcceptanceSources(adapter string) map[string]string {
	files := map[string]string{
		"pkg/__init__.py": "from .leaf import VALUE\n",
		"pkg/leaf.py":     "VALUE = 41\n",
		"support.py":      "from pkg import VALUE\nimport os\nfrom pathlib import Path\ndef checked():\n Path(os.environ['HAYAKU_ACCEPTANCE_BODY']).write_text('ran')\n return VALUE\n",
		"resource.txt":    "41\n",
		"dormant.py":      "import importlib, os, subprocess\ndef dormant(name):\n importlib.import_module(name)\n subprocess.run(['unused'])\n return os.getenv('UNUSED_BRANCH')\n",
	}
	if adapter == "pytest" {
		files["conftest.py"] = "import pytest\n@pytest.fixture(scope='session')\ndef base(): return 41\n@pytest.fixture(autouse=True)\ndef seed(base): return base\n"
		files["test_alpha.py"] = "import pytest\nfrom pathlib import Path\nfrom support import checked\n@pytest.mark.parametrize('index', [1, 2], ids=['one', 'two'])\ndef test_direct(index, seed): assert checked() == seed\ndef test_resource(): assert int(Path('resource.txt').read_text()) == checked()\n"
		files["test_beta.py"] = "from support import checked\ndef test_other(): assert checked() == 41\n"
	} else {
		files["test_alpha.py"] = "import unittest\nfrom pathlib import Path\nfrom support import checked\nclass Values(unittest.TestCase):\n def test_direct(self): self.assertEqual(checked(), 41)\n def test_resource(self): self.assertEqual(int(Path('resource.txt').read_text()), checked())\n"
		files["test_beta.py"] = "import unittest\nfrom support import checked\nclass Other(unittest.TestCase):\n def test_other(self): self.assertEqual(checked(), 41)\n"
	}
	return files
}

func pythonAcceptanceInventory(adapter string) model.ExecutionInventory {
	alpha, beta := "test_alpha", "test_beta"
	ids := []string{"test_alpha.Values.test_direct", "test_alpha.Values.test_resource", "test_beta.Other.test_other"}
	if adapter == "pytest" {
		alpha, beta = alpha+".py", beta+".py"
		ids = []string{"test_alpha.py::test_direct[one]", "test_alpha.py::test_direct[two]", "test_alpha.py::test_resource", "test_beta.py::test_other"}
	}
	left, right := "native:"+adapter+":"+alpha, "native:"+adapter+":"+beta
	inventory := model.ExecutionInventory{Schema: model.ExecutionSchema, Units: []model.Unit{{ID: left, Workspace: "native", Selector: alpha, Kind: adapter + "-file"}, {ID: right, Workspace: "native", Selector: beta, Kind: adapter + "-file"}}}
	for index, id := range ids {
		unit := left
		if index == len(ids)-1 {
			unit = right
		}
		inventory.Cases = append(inventory.Cases, model.Case{Unit: unit, ID: id})
	}
	return inventory
}

func TestPythonAcceptanceIndependentInventoryImportsAndOriginalCommand(t *testing.T) {
	for _, adapter := range []string{"unittest", "pytest"} {
		t.Run(adapter, func(t *testing.T) {
			root, workspace, execution := pythonAcceptanceFixture(t, adapter, pythonAcceptanceSources(adapter))
			marker := filepath.Join(t.TempDir(), "body-marker")
			execution.Env["HAYAKU_ACCEPTANCE_BODY"] = marker
			closure, err := CollectPythonClosure(context.Background(), root, workspace, execution)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(closure.Inventory, pythonAcceptanceInventory(adapter)) {
				t.Fatalf("independent expected collection differs: %+v", closure.Inventory)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("collection executed a test body")
			}
			for _, wanted := range []model.Edge{{From: "native:python-source:pkg/leaf.py", To: "native:python-source:pkg/__init__.py", Reason: "static-python-import"}, {From: "native:python-source:pkg/__init__.py", To: "native:python-source:support.py", Reason: "static-python-import"}, {From: "native:python-source:support.py", To: "native:python-source:test_alpha.py", Reason: "static-python-import"}} {
				found := false
				for _, edge := range closure.Evidence.Edges {
					found = found || edge == wanted
				}
				if !found {
					t.Fatalf("independent import edge missing: %+v; observed %+v", wanted, closure.Evidence.Edges)
				}
			}
			for _, code := range []string{"runtime-unqualified", "dynamic-import-unqualified", "subprocess-unqualified"} {
				found := false
				for _, gap := range closure.Evidence.Gaps {
					found = found || gap.Code == code
				}
				if !found {
					t.Fatalf("dormant influence lost explicit gap %s", code)
				}
			}
			if adapter == "pytest" {
				want := []PythonFixture{{Owner: "conftest.py", ID: "base", Dependencies: []string{}, Scope: "session"}, {Owner: "conftest.py", ID: "seed", Dependencies: []string{"base"}, Scope: "function", Autouse: true}}
				if !reflect.DeepEqual(closure.Fixtures, want) {
					t.Fatalf("independent fixture ownership/dependencies differ: %+v", closure.Fixtures)
				}
			}
			if err := RevalidatePythonClosure(context.Background(), root, closure); err != nil {
				t.Fatal("unchanged closure rejected", err)
			}
			plan, err := planner.Build([]model.Evidence{closure.Evidence}, []model.Evidence{closure.Evidence}, []model.Change{{Path: "pkg/leaf.py", Status: "M"}}, model.Config{Schema: model.Schema, Workspaces: []model.Workspace{workspace}})
			if err != nil || plan.Mode != "full-fallback" || len(plan.Selected) != len(closure.Inventory.Units) || !reflect.DeepEqual(plan.Commands, []model.Command{workspace.Command}) {
				t.Fatalf("descriptive closure did not preserve common full-command planning: %v", err)
			}
			output, err := process.Run(context.Background(), workspace.Command, root, execution.Env)
			if err != nil || !output.Completed || output.ExitCode != 0 {
				t.Fatalf("original full-command healthy baseline failed: %v", err)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatal("original full command did not execute bodies")
			}
			if err := RevalidatePythonClosure(context.Background(), root, closure); err != nil {
				t.Fatal("healthy original command changed guarded sources", err)
			}
		})
	}
}

func TestPythonAcceptanceSourceMembershipAndMetadataDrift(t *testing.T) {
	for _, adapter := range []string{"unittest", "pytest"} {
		for _, fault := range []string{"direct-import", "resource-change", "resource-delete", "new-test", "deleted-test", "private-source", "inventory-tamper"} {
			t.Run(adapter+"/"+fault, func(t *testing.T) {
				root, workspace, execution := pythonAcceptanceFixture(t, adapter, pythonAcceptanceSources(adapter))
				execution.Env["HAYAKU_ACCEPTANCE_BODY"] = filepath.Join(t.TempDir(), "body-marker")
				closure, err := CollectPythonClosure(context.Background(), root, workspace, execution)
				if err != nil {
					t.Fatal("healthy collection baseline unavailable", err)
				}
				if !reflect.DeepEqual(closure.Inventory, pythonAcceptanceInventory(adapter)) || RevalidatePythonClosure(context.Background(), root, closure) != nil {
					t.Fatal("healthy baseline did not establish independent inventory")
				}
				runtimeFault := fault == "direct-import" || fault == "resource-change" || fault == "resource-delete"
				if runtimeFault {
					output, err := process.Run(context.Background(), workspace.Command, root, execution.Env)
					if err != nil || !output.Completed || output.ExitCode != 0 {
						t.Fatalf("same-runtime healthy full command failed before seeded fault: %v", err)
					}
				}
				switch fault {
				case "direct-import":
					pythonAcceptanceWrite(t, root, "pkg/leaf.py", "VALUE = 7\n")
				case "resource-change":
					pythonAcceptanceWrite(t, root, "resource.txt", "7\n")
				case "resource-delete":
					if err := os.Remove(filepath.Join(root, "resource.txt")); err != nil {
						t.Fatal(err)
					}
				case "new-test":
					source := "import unittest\nclass Added(unittest.TestCase):\n def test_new(self): pass\n"
					if adapter == "pytest" {
						source = "def test_new(): pass\n"
					}
					pythonAcceptanceWrite(t, root, "test_new.py", source)
				case "deleted-test":
					if err := os.Remove(filepath.Join(root, "test_beta.py")); err != nil {
						t.Fatal(err)
					}
				case "private-source":
					pythonAcceptanceWrite(t, root, "dormant.py", "def unused(): return 7\n")
				case "inventory-tamper":
					closure.Inventory.Cases = closure.Inventory.Cases[:len(closure.Inventory.Cases)-1]
				}
				if RevalidatePythonClosure(context.Background(), root, closure) == nil {
					t.Fatal("changed source membership or exported inventory retained validity")
				}
				if runtimeFault {
					output, runErr := Execute(context.Background(), root, workspace, execution, nil)
					outcomes, decodeErr := nativerunner.Reconcile(bytes.NewReader(output.Stdout), adapter, closure.RunnerVersion, closure.Inventory.Units)
					// Reconcile rejects incomplete reports before returning nil.
					normalized := model.Outcomes{Complete: decodeErr == nil}
					for _, item := range outcomes.Units {
						normalized.Units = append(normalized.Units, model.Outcome{Unit: item.Unit, Action: item.Action})
					}
					for _, item := range outcomes.Tests {
						normalized.Tests = append(normalized.Tests, model.Outcome{Unit: item.Unit, Test: item.Test, Action: item.Action})
					}
					if runErr == nil || !output.Completed || output.ExitCode != 1 || decodeErr != nil || runner.ValidateExact(normalized, pythonAcceptanceInventory(adapter), true, 1) != nil {
						t.Fatalf("seeded full-command fault did not preserve exact original cases: run=%v decode=%v", runErr, decodeErr)
					}
					for _, outcome := range outcomes.Tests {
						wanted := "pass"
						if fault == "direct-import" || strings.HasSuffix(outcome.Test, "test_resource") {
							wanted = "fail"
						}
						if outcome.Action != wanted {
							t.Fatalf("seeded fault case action differs: %s=%s expected %s", outcome.Test, outcome.Action, wanted)
						}
					}
				}
				if fault == "new-test" || fault == "deleted-test" {
					after, err := CollectPythonClosure(context.Background(), root, workspace, execution)
					if err != nil {
						t.Fatal(err)
					}
					wanted := pythonAcceptanceInventory(adapter)
					if fault == "deleted-test" {
						wanted.Units = wanted.Units[:1]
						wanted.Cases = wanted.Cases[:len(wanted.Cases)-1]
					} else {
						selector, id := "test_new", "test_new.Added.test_new"
						if adapter == "pytest" {
							selector, id = "test_new.py", "test_new.py::test_new"
						}
						unit := model.Unit{ID: "native:" + adapter + ":" + selector, Workspace: "native", Selector: selector, Kind: adapter + "-file"}
						wanted.Units = append(wanted.Units, unit)
						wanted.Cases = append(wanted.Cases, model.Case{Unit: unit.ID, ID: id})
					}
					if !reflect.DeepEqual(after.Inventory, wanted) {
						t.Fatal("added/deleted case recollection differs from independent expectation")
					}
				}
			})
		}
	}
}

func TestPythonAcceptanceCollectionStageFaultsHaveHealthyBaseline(t *testing.T) {
	for _, adapter := range []string{"unittest", "pytest"} {
		for _, stage := range []string{"import", "collection-hook", "source-drift"} {
			t.Run(adapter+"/"+stage, func(t *testing.T) {
				files := pythonAcceptanceSources(adapter)
				files["startup.py"] = "def healthy(): return 41\n"
				files["test_alpha.py"] = "import startup\n" + files["test_alpha.py"]
				if adapter == "pytest" {
					files["conftest.py"] += "def pytest_sessionstart(session): pass\n"
				} else {
					files["test_alpha.py"] += "def load_tests(loader, tests, pattern): return tests\n"
				}
				root, workspace, execution := pythonAcceptanceFixture(t, adapter, files)
				before, err := CollectPythonClosure(context.Background(), root, workspace, execution)
				if err != nil || !reflect.DeepEqual(before.Inventory, pythonAcceptanceInventory(adapter)) {
					t.Fatalf("same-runtime/config healthy stage unreachable: %v", err)
				}
				switch stage {
				case "import":
					pythonAcceptanceWrite(t, root, "startup.py", "raise RuntimeError('seeded-import-fault')\n")
				case "collection-hook":
					if adapter == "pytest" {
						pythonAcceptanceWrite(t, root, "conftest.py", files["conftest.py"]+"def pytest_sessionstart(session): raise RuntimeError('seeded-hook-fault')\n")
					} else {
						pythonAcceptanceWrite(t, root, "test_alpha.py", files["test_alpha.py"]+"def load_tests(loader, tests, pattern): raise RuntimeError('seeded-hook-fault')\n")
					}
				case "source-drift":
					pythonAcceptanceWrite(t, root, "startup.py", "from pathlib import Path\nPath('resource.txt').write_text('changed-during-collection')\n")
				}
				after, err := CollectPythonClosure(context.Background(), root, workspace, execution)
				if err == nil || len(after.Inventory.Units) != 0 || len(after.Inventory.Cases) != 0 {
					t.Fatalf("seeded %s produced a usable collection", stage)
				}
				if stage == "source-drift" {
					data, err := os.ReadFile(filepath.Join(root, "resource.txt"))
					if err != nil || !strings.Contains(string(data), "changed-during-collection") {
						t.Fatal("drift rejection did not observe seeded collection work")
					}
				}
			})
		}
	}
}
