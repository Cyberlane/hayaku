package app

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
)

func nativeAppWrite(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func nativeAppGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("native fixture Git operation failed: %v: %s", err, output)
	}
	return strings.TrimSpace(string(output))
}

func nativeAppCommit(t *testing.T, root, message string) string {
	t.Helper()
	nativeAppGit(t, root, "add", ".")
	nativeAppGit(t, root, "commit", "-qm", message)
	return nativeAppGit(t, root, "rev-parse", "HEAD")
}

func nativeAppNodeFixture(t *testing.T) (string, string, model.Config) {
	t.Helper()
	node := os.Getenv("HAYAKU_NATIVE_NODE")
	if node == "" {
		t.Skip("set HAYAKU_NATIVE_NODE to the installed version-qualified Node fixture")
	}
	if !filepath.IsAbs(node) {
		t.Fatal("native app fixture requires an explicit absolute Node executable")
	}
	root := t.TempDir()
	c := model.Config{Schema: model.Schema, Context: model.Context{ID: "native-node-app-fixture", OS: runtime.GOOS, Arch: runtime.GOARCH, Env: map[string]string{"CI": "true"}}, Workspaces: []model.Workspace{{
		ID: "node", Root: ".", Adapter: "node-test",
		Command: model.Command{Dir: ".", Executable: node, Args: []string{"--test", "--test-concurrency=1", "a.test.mjs", "b space.test.mjs"}},
	}}}
	configuration, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"hayaku.json":      string(configuration),
		"package.json":     "{\"name\":\"hayaku-native-node-fixture\",\"private\":true,\"type\":\"module\"}\n",
		"value.ts":         "export const value: number = 1;\n",
		"a.test.mjs":       "import {test} from 'node:test';import assert from 'node:assert/strict';import {value} from './value.ts';test('same name',()=>assert.ok(value>0));\n",
		"b space.test.mjs": "import {test} from 'node:test';import assert from 'node:assert/strict';import {readFileSync} from 'node:fs';import {join} from 'node:path';test('same name',()=>{const path=join(process.cwd(),'resource'+'.json');assert.ok(JSON.parse(readFileSync(path,'utf8')).value>0)});\n",
		"resource.json":    "{\"value\":1}\n",
	}
	for name, contents := range files {
		nativeAppWrite(t, root, name, contents)
	}
	nativeAppGit(t, root, "init", "-q")
	nativeAppGit(t, root, "config", "user.name", "Fixture")
	nativeAppGit(t, root, "config", "user.email", "fixture@example.test")
	nativeAppGit(t, root, "config", "commit.gpgsign", "false")
	return root, nativeAppCommit(t, root, "native base"), c
}

func nativeAppAssertFullNodePlan(t *testing.T, plan model.Plan, c model.Config) {
	t.Helper()
	if plan.Mode != "full-fallback" || len(plan.Selected) != 2 || len(plan.Proposed) != 2 {
		t.Fatalf("unqualified Node discovery omitted an unknown runtime influence: mode=%s selected=%v proposed=%v", plan.Mode, plan.Selected, plan.Proposed)
	}
	if len(plan.Commands) != 1 || !reflect.DeepEqual(plan.Commands[0], c.Workspaces[0].Command) {
		t.Fatalf("required native Node command changed: %v", plan.Commands)
	}
	if plan.Selected[0].ID == plan.Selected[1].ID {
		t.Fatal("same native test names collapsed distinct file inventories")
	}
}

func nativeAppAssertCompleteNodeRun(t *testing.T, execution Execution, c model.Config) {
	t.Helper()
	if len(execution.Commands) != 1 || !execution.Commands[0].Complete || execution.Commands[0].Native == nil {
		t.Fatalf("native full outcomes were not reconciled: %+v", execution)
	}
	command := execution.Commands[0]
	if !reflect.DeepEqual(command.Command, c.Workspaces[0].Command) || len(command.Native.Units) != 2 || len(command.Native.Tests) != 2 {
		t.Fatalf("original full command or complete native inventory lost: %+v", command)
	}
	if command.Native.UnhandledErrors != 0 || len(command.Native.Missing) != 0 {
		t.Fatalf("unexpected native global error or missing terminal outcomes: %+v", command.Native)
	}
}

func TestNativeNodeAppBuildExecuteShadowKeepsRequiredFullSuite(t *testing.T) {
	root, base, c := nativeAppNodeFixture(t)
	nativeAppWrite(t, root, "value.ts", "export const value: number = 2;\n")
	candidate := nativeAppCommit(t, root, "typed source change")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	plan, err := Build(ctx, root, base, candidate, c, false)
	if err != nil {
		t.Fatal(err)
	}
	nativeAppAssertFullNodePlan(t, plan, c)
	execution, err := Execute(ctx, root, c, plan)
	if err != nil || !execution.Passed || execution.Mode != "full" {
		t.Fatalf("required native Node full run failed: %+v %v", execution, err)
	}
	nativeAppAssertCompleteNodeRun(t, execution, c)
	shadow, err := Shadow(ctx, root, c, plan)
	if err != nil || !shadow.ComparisonValid || !shadow.Proposal.Passed || !shadow.Full.Passed || len(shadow.ObservedMisses) != 0 || len(shadow.Inconsistencies) != 0 {
		t.Fatalf("native Node shadow comparison incomplete: %+v %v", shadow, err)
	}
	if !strings.Contains(shadow.Assurance, "observational-only") || shadow.Proposal.Mode != "shadow-proposal" || shadow.Full.Mode != "shadow-full" {
		t.Fatal("native shadow comparison claimed production skipping authority")
	}
	nativeAppAssertCompleteNodeRun(t, shadow.Full, c)
	if got := nativeAppGit(t, root, "status", "--porcelain=v1", "--untracked-files=all", "--ignored=matching"); got != "" {
		t.Fatalf("native execution or shadow mutated committed source: %s", got)
	}

	nativeAppWrite(t, root, "unexpected-input.json", "{\"value\":0}\n")
	if _, err := Execute(ctx, root, c, plan); err == nil {
		t.Fatal("native Execute accepted a changed candidate envelope")
	}
	if shadow, err := Shadow(ctx, root, c, plan); err == nil || shadow.ComparisonValid {
		t.Fatal("native Shadow certified a changed candidate envelope")
	}
}

func TestNativeNodeAppDynamicResourceFailureRemainsFullFailure(t *testing.T) {
	root, base, c := nativeAppNodeFixture(t)
	// Neither test imports this input. The second file computes its name at
	// runtime, so discovery must conservatively assign it to both native files.
	nativeAppWrite(t, root, "resource.json", "{\"value\":0}\n")
	candidate := nativeAppCommit(t, root, "dynamic runtime input failure")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	plan, err := Build(ctx, root, base, candidate, c, false)
	if err != nil {
		t.Fatal(err)
	}
	nativeAppAssertFullNodePlan(t, plan, c)
	execution, err := Execute(ctx, root, c, plan)
	if err == nil || execution.Passed {
		t.Fatalf("required full suite hid a dynamic resource failure: %+v %v", execution, err)
	}
	nativeAppAssertCompleteNodeRun(t, execution, c)
	if !execution.Commands[0].Native.Failed {
		t.Fatal("native failure disappeared during result reconciliation")
	}
	shadow, err := Shadow(ctx, root, c, plan)
	if err == nil || !shadow.ComparisonValid || shadow.Proposal.Passed || shadow.Full.Passed || len(shadow.ObservedMisses) != 0 || len(shadow.Inconsistencies) != 0 {
		t.Fatalf("complete matching failures were lost or certified as passes: %+v %v", shadow, err)
	}
	nativeAppAssertCompleteNodeRun(t, shadow.Full, c)
	if plan.Mode != "full-fallback" {
		t.Fatal("shadow failure changed original required full fallback")
	}
}

func TestNativeUnittestAppRequiresExplicitBytecodePolicy(t *testing.T) {
	python := os.Getenv("HAYAKU_NATIVE_UNITTEST_PYTHON")
	if python == "" {
		t.Skip("set HAYAKU_NATIVE_UNITTEST_PYTHON to the installed native Python fixture")
	}
	if !filepath.IsAbs(python) {
		t.Fatal("native app fixture requires an explicit absolute Python executable")
	}
	for _, setting := range []struct {
		name, bytecode string
		clean          bool
	}{{"explicit-no-bytecode", "1", true}, {"original-bytecode-effects", "", false}} {
		t.Run(setting.name, func(t *testing.T) {
			root := t.TempDir()
			c := model.Config{Schema: model.Schema, Context: model.Context{ID: "native-unittest-app-fixture", OS: runtime.GOOS, Arch: runtime.GOARCH, Env: map[string]string{"PYTHONDONTWRITEBYTECODE": setting.bytecode}}, Workspaces: []model.Workspace{{
				ID: "python", Root: ".", Adapter: "unittest",
				Command: model.Command{Dir: ".", Executable: python, Args: []string{"-m", "unittest", "discover", "-s", "tests", "-p", "test_*.py"}},
			}}}
			configuration, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			nativeAppWrite(t, root, "hayaku.json", string(configuration))
			nativeAppWrite(t, root, "value.py", "value = 1\n")
			nativeAppWrite(t, root, "tests/test_value.py", "import unittest\nfrom value import value\nclass Values(unittest.TestCase):\n def test_value(self): self.assertGreater(value, 0)\n")
			nativeAppGit(t, root, "init", "-q")
			nativeAppGit(t, root, "config", "user.name", "Fixture")
			nativeAppGit(t, root, "config", "user.email", "fixture@example.test")
			nativeAppGit(t, root, "config", "commit.gpgsign", "false")
			base := nativeAppCommit(t, root, "unittest base")
			nativeAppWrite(t, root, "value.py", "value = 2\n")
			candidate := nativeAppCommit(t, root, "Python source change")
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			plan, err := Build(ctx, root, base, candidate, c, false)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Mode != "full-fallback" || len(plan.Selected) != 1 || len(plan.Proposed) != 1 || len(plan.Commands) != 1 || !reflect.DeepEqual(plan.Commands[0], c.Workspaces[0].Command) {
				t.Fatalf("unittest collection changed original full command: %+v", plan)
			}
			shadow, shadowErr := Shadow(ctx, root, c, plan)
			if setting.clean {
				if shadowErr != nil || !shadow.ComparisonValid || !shadow.Full.Passed || !shadow.Proposal.Passed {
					t.Fatalf("explicit bytecode-free shadow failed: %+v %v", shadow, shadowErr)
				}
			} else if shadowErr == nil || shadow.ComparisonValid || shadow.Full.Passed || shadow.Proposal.Passed {
				t.Fatalf("runtime bytecode writes certified an immutable comparison: %+v %v", shadow, shadowErr)
			}
			if got := nativeAppGit(t, root, "status", "--porcelain=v1", "--untracked-files=all", "--ignored=matching"); got != "" {
				t.Fatalf("discovery or separate shadow copies modified original Python source: %s", got)
			}
			execution, executeErr := Execute(ctx, root, c, plan)
			if len(execution.Commands) != 1 || execution.Commands[0].Native == nil || !reflect.DeepEqual(execution.Commands[0].Command, c.Workspaces[0].Command) {
				t.Fatalf("unittest full command or native bridge disappeared: %+v %v", execution, executeErr)
			}
			if setting.clean {
				if executeErr != nil || !execution.Passed || !execution.Commands[0].Complete {
					t.Fatalf("explicit bytecode-free full run failed: %+v %v", execution, executeErr)
				}
				for _, path := range []string{"__pycache__", "tests/__pycache__"} {
					if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); !os.IsNotExist(err) {
						t.Fatalf("explicit no-bytecode policy generated source output: %s", path)
					}
				}
			} else {
				if executeErr == nil || execution.Passed || execution.Commands[0].Complete {
					t.Fatalf("original bytecode effects silently bypassed candidate validation: %+v %v", execution, executeErr)
				}
				if info, err := os.Stat(filepath.Join(root, "tests", "__pycache__")); err != nil || !info.IsDir() {
					t.Fatal("full native execution silently suppressed original interpreter bytecode effects")
				}
			}
		})
	}
}
