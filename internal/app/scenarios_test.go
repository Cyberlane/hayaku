package app

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
)

type nativeScenarioFixture struct {
	workspace model.Workspace
	files     map[string]string
	changes   map[string]scenarioMutation
	selectors map[string]string
}
type scenarioMutation struct {
	path, contents string
	remove         bool
	failures       []string
}

// Fixture syntax is authored separately for each language. The actual Git,
// plan, execution and outcome assertions below are one shared harness.
func nativeScenario(t *testing.T, id string) nativeScenarioFixture {
	t.Helper()
	f := nativeScenarioFixture{workspace: model.Workspace{ID: "suite", Root: ".", Adapter: id, Command: model.Command{Dir: "."}}, files: map[string]string{"resource.json": "{\"value\":1}\n"}, changes: map[string]scenarioMutation{}, selectors: map[string]string{}}
	switch id {
	case "go":
		tool, err := exec.LookPath("go")
		if err != nil {
			t.Fatal(err)
		}
		f.workspace.Command.Executable = tool
		f.workspace.Command.Args = []string{"test", "./..."}
		f.files["go.mod"] = "module example.test/scenarios\n\ngo 1.26\n"
		f.files["shared/shared.go"] = "package shared\nconst Enabled=true\n"
		f.files["a/value.go"] = "package a\nfunc Value()int{return 1}\n"
		f.files["a/value_test.go"] = "package a\nimport(\"testing\";\"example.test/scenarios/shared\")\nfunc TestValue(t *testing.T){if Value()<=0 || !shared.Enabled{t.Fatal(\"bad value\")}}\n"
		f.files["b/resource_test.go"] = "package b\nimport(\"testing\";\"os\";\"encoding/json\";\"example.test/scenarios/shared\")\nfunc TestResource(t *testing.T){data,err:=os.ReadFile(\"../resource.json\");if err!=nil{t.Fatal(err)};var v struct{Value int};if json.Unmarshal(data,&v)!=nil || v.Value<=0 || !shared.Enabled{t.Fatal(\"bad resource\")}}\n"
		f.selectors = map[string]string{"a": "example.test/scenarios/a", "b": "example.test/scenarios/b"}
		f.changes["direct"] = scenarioMutation{path: "a/value.go", contents: "package a\nfunc Value()int{return -1}\n", failures: []string{"a"}}
		f.changes["shared-setup"] = scenarioMutation{path: "shared/shared.go", contents: "package shared\nconst Enabled=false\n", failures: []string{"a", "b"}}
		f.changes["new-test"] = scenarioMutation{path: "a/value_test.go", contents: f.files["a/value_test.go"] + "func TestNew(t *testing.T){t.Fatal(\"new failing test\")}\n", failures: []string{"a"}}
	case "unittest", "pytest":
		python := os.Getenv("HAYAKU_NATIVE_UNITTEST_PYTHON")
		if id == "pytest" {
			python = os.Getenv("HAYAKU_NATIVE_PYTHON")
		}
		if python == "" {
			t.Skip("set explicit installed Python path for shared native scenarios")
		}
		f.workspace.Command.Executable = python
		f.workspace.Command.Args = []string{"-m", "unittest", "discover", "-s", "."}
		f.files["value.py"] = "value=1\n"
		f.files["shared.py"] = "enabled=True\n"
		f.files["test_a.py"] = "import unittest,value,shared\nclass A(unittest.TestCase):\n def test_value(self):\n  self.assertGreater(value.value,0)\n  self.assertTrue(shared.enabled)\n"
		f.files["test_b.py"] = "import unittest,json,pathlib,shared\nclass B(unittest.TestCase):\n def test_resource(self):\n  self.assertGreater(json.loads(pathlib.Path('resource.json').read_text())['value'],0)\n  self.assertTrue(shared.enabled)\n"
		f.selectors = map[string]string{"a": "test_a", "b": "test_b"}
		f.changes["direct"] = scenarioMutation{path: "value.py", contents: "value=-1\n", failures: []string{"a"}}
		f.changes["shared-setup"] = scenarioMutation{path: "shared.py", contents: "enabled=False\n", failures: []string{"a", "b"}}
		f.changes["new-test"] = scenarioMutation{path: "test_a.py", contents: f.files["test_a.py"] + " def test_new(self):\n  self.fail('new failing test')\n", failures: []string{"a"}}
		if id == "pytest" {
			f.workspace.Command.Args = []string{"-m", "pytest", "-q", "-p", "no:cacheprovider"}
			f.selectors = map[string]string{"a": "test_a.py", "b": "test_b.py"}
		}
	case "node-test", "vitest":
		node := os.Getenv("HAYAKU_NATIVE_NODE")
		if id == "vitest" {
			node = os.Getenv("HAYAKU_VITEST_NODE")
		}
		if node == "" {
			t.Skip("set explicit installed Node path for shared native scenarios")
		}
		f.files["package.json"] = `{"private":true,"type":"module"}`
		f.files["shared.mjs"] = "export const enabled=true;\n"
		valueFile := "value.mjs"
		valueImport := "./value.mjs"
		imports := "import {test} from 'node:test';import assert from 'node:assert/strict';"
		check := "assert.ok"
		f.workspace.Command.Executable = node
		f.workspace.Command.Args = []string{"--test", "--test-concurrency=1", "a.test.mjs", "b.test.mjs"}
		if id == "vitest" {
			modules := os.Getenv("HAYAKU_VITEST_MODULES")
			if modules == "" {
				t.Skip("set existing pinned Vitest/Vite modules for shared scenarios")
			}
			valueFile = "value.ts"
			valueImport = "./value.ts"
			imports = ""
			check = "check"
			f.files["vitest.config.mjs"] = "export default {test:{globals:true,fileParallelism:false}};\n"
			f.workspace.Command.Executable = filepath.Join(modules, ".bin", "vitest")
			f.workspace.Command.Args = []string{"run", "--globals", "--config", "vitest.config.mjs"}
			f.workspace.NodeRuntime = &model.NodeRuntime{Node: node, Modules: modules}
			imports = "const check=(value)=>expect(value).toBe(true);"
		}
		f.files[valueFile] = "export const value=1;\n"
		f.files["a.test.mjs"] = imports + "import {value} from '" + valueImport + "';import {enabled} from './shared.mjs';test('value',()=>{" + check + "(value>0);" + check + "(enabled)});\n"
		f.files["b.test.mjs"] = imports + "import {readFileSync} from 'node:fs';import {enabled} from './shared.mjs';test('resource',()=>{" + check + "(JSON.parse(readFileSync('resource.json','utf8')).value>0);" + check + "(enabled)});\n"
		f.selectors = map[string]string{"a": "a.test.mjs", "b": "b.test.mjs"}
		f.changes["direct"] = scenarioMutation{path: valueFile, contents: "export const value=-1;\n", failures: []string{"a"}}
		f.changes["shared-setup"] = scenarioMutation{path: "shared.mjs", contents: "export const enabled=false;\n", failures: []string{"a", "b"}}
		f.changes["new-test"] = scenarioMutation{path: "a.test.mjs", contents: f.files["a.test.mjs"] + "test('new failing test',()=>" + check + "(false));\n", failures: []string{"a"}}
	default:
		t.Fatal("missing native scenario fixture")
	}
	f.changes["resource"] = scenarioMutation{path: "resource.json", contents: "{\"value\":-1}\n", failures: []string{"b"}}
	f.changes["deleted-resource"] = scenarioMutation{path: "resource.json", remove: true, failures: []string{"b"}}
	return f
}

func TestSharedNativeScenarios(t *testing.T) {
	for _, id := range []string{"go", "node-test", "unittest", "pytest", "vitest"} {
		t.Run(id, func(t *testing.T) {
			for _, scenario := range []string{"direct", "shared-setup", "resource", "deleted-resource", "new-test"} {
				t.Run(scenario, func(t *testing.T) {
					fixture := nativeScenario(t, id)
					root := t.TempDir()
					c := model.Config{Schema: model.Schema, Context: model.Context{ID: "shared-scenarios", OS: runtime.GOOS, Arch: runtime.GOARCH, Env: map[string]string{"CI": "true", "PYTHONDONTWRITEBYTECODE": "1"}}, Workspaces: []model.Workspace{fixture.workspace}}
					configuration, err := json.Marshal(c)
					if err != nil {
						t.Fatal(err)
					}
					fixture.files["hayaku.json"] = string(configuration)
					for name, contents := range fixture.files {
						nativeAppWrite(t, root, name, contents)
					}
					nativeAppGit(t, root, "init", "-q")
					nativeAppGit(t, root, "config", "user.name", "Fixture")
					nativeAppGit(t, root, "config", "user.email", "fixture@example.test")
					nativeAppGit(t, root, "config", "commit.gpgsign", "false")
					base := nativeAppCommit(t, root, "baseline")
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
					defer cancel()
					baseline, err := Build(ctx, root, base, base, c, false)
					if err != nil {
						t.Fatal(err)
					}
					clean, err := Execute(ctx, root, c, baseline)
					if err != nil || !clean.Passed {
						t.Fatalf("native baseline failed: %v", err)
					}
					mutation := fixture.changes[scenario]
					if mutation.remove {
						err = os.Remove(filepath.Join(root, mutation.path))
					} else {
						nativeAppWrite(t, root, mutation.path, mutation.contents)
					}
					if err != nil {
						t.Fatal(err)
					}
					candidate := nativeAppCommit(t, root, "independently authored fault")
					plan, err := Build(ctx, root, base, candidate, c, false)
					if err != nil {
						t.Fatal(err)
					}
					if plan.Mode != "full-fallback" || len(plan.Commands) != 1 || !reflect.DeepEqual(plan.Commands[0].Args, fixture.workspace.Command.Args) {
						t.Fatal("shared integration narrowed required native gate")
					}
					run, err := Execute(ctx, root, c, plan)
					if err == nil || run.Passed {
						t.Fatal("independent native failure became green")
					}
					states := outcomes(run)
					for _, name := range mutation.failures {
						found := false
						for _, unit := range plan.Selected {
							if unit.Selector == fixture.selectors[name] && states[unit.ID] == "fail" {
								found = true
							}
						}
						if !found {
							t.Fatalf("expected native failure %s missing: %+v", name, states)
						}
					}
					for _, command := range run.Commands {
						if !command.Complete || command.Outcomes == nil {
							t.Fatal("native terminal failure was not normalized as complete")
						}
					}
				})
			}
		})
	}
}
