package native

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Cyberlane/hayaku/internal/adapter/pytest"
	"github.com/Cyberlane/hayaku/internal/model"
	nativerunner "github.com/Cyberlane/hayaku/internal/runner/native"
)

func nativeFixture(t *testing.T, runner string, files map[string]string, args []string) (string, model.Workspace) {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	tool := "node"
	if runner == "unittest" {
		tool = "python3"
	}
	path, err := exec.LookPath(tool)
	if err != nil {
		t.Skip("installed native fixture unavailable")
	}
	if runner == "node-test" {
		if configured := os.Getenv("HAYAKU_NATIVE_NODE"); configured != "" {
			path = configured
		}
		version, err := exec.Command(path, "--version").Output()
		if err != nil || !supportedVersion(runner, strings.TrimPrefix(strings.TrimSpace(string(version)), "v")) {
			if os.Getenv("HAYAKU_NATIVE_NODE") != "" {
				t.Fatal("explicit native Node fixture is unavailable or has unsupported version")
			}
			t.Skip("fixture requires version-gated installed Node")
		}
	}
	if runner == "unittest" {
		if configured := os.Getenv("HAYAKU_NATIVE_UNITTEST_PYTHON"); configured != "" {
			path = configured
		}
	}
	return root, model.Workspace{ID: "native", Root: ".", Adapter: runner, Command: model.Command{Dir: ".", Executable: path, Args: args}}
}
func resultVersion(e model.Evidence) string { _, v, _ := strings.Cut(e.Version, "="); return v }
func TestNodeNativeCollectionAndCompleteRun(t *testing.T) {
	root, w := nativeFixture(t, "node-test", map[string]string{
		"a.test.mjs":       "import {test} from 'node:test';import assert from 'node:assert';test('nested',async(t)=>{ await t.test('child',()=>assert.equal(1,1));});\n",
		"b space.test.mjs": "import {test} from 'node:test';test('same',()=>{});test('same',()=>{});test('skip',{skip:true},()=>{throw Error('must not run')});\n",
		"input.txt":        "workspace input\n",
	}, []string{"--test", "--test-concurrency=1", "a.test.mjs", "b space.test.mjs"})
	e, err := Discover(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Units) != 2 || len(e.Inputs["input.txt"]) != 2 || len(e.Gaps) < 3 {
		t.Fatal("discovery narrowed unknown influence")
	}
	again, err := Discover(context.Background(), root, w, model.Context{})
	if err != nil || !reflect.DeepEqual(e, again) {
		t.Fatal("native discovery drift")
	}
	out, err := Execute(context.Background(), root, w, model.Context{}, nil)
	if err != nil || !out.Completed || out.ExitCode != 0 {
		t.Fatalf("run failed %v", err)
	}
	result, err := nativerunner.Reconcile(bytes.NewReader(out.Stdout), "node-test", resultVersion(e), e.Units)
	if err != nil || result.Failed || len(result.Units) != 2 || len(result.Tests) != 5 {
		t.Fatalf("bad run %+v %v", result, err)
	}
	selected := e.Units[:1]
	out, err = Execute(context.Background(), root, w, model.Context{}, selected)
	if err != nil {
		t.Fatal(err)
	}
	result, err = nativerunner.Reconcile(bytes.NewReader(out.Stdout), "node-test", resultVersion(e), selected)
	if err != nil || len(result.Units) != 1 {
		t.Fatal("selected whole file run mismatch", err)
	}
}
func TestNodeCollectionSuppressesBodiesButErrorsRemainFailures(t *testing.T) {
	root, w := nativeFixture(t, "node-test", map[string]string{"fail.test.mjs": "import {test} from 'node:test';test('failing',()=>{throw Error('private-source-sentinel')});\n"}, []string{"--test", "fail.test.mjs"})
	e, err := Discover(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal("test body executed during collection", err)
	}
	out, err := Execute(context.Background(), root, w, model.Context{}, nil)
	if err == nil || out.ExitCode != 1 {
		t.Fatal("failing native test accepted")
	}
	if bytes.Contains(out.Stdout, []byte("private-source-sentinel")) || len(out.Stderr) > 0 {
		t.Fatal("native diagnostics leaked")
	}
	result, decodeErr := nativerunner.Reconcile(bytes.NewReader(out.Stdout), "node-test", resultVersion(e), e.Units)
	if decodeErr != nil || !result.Failed {
		t.Fatal("native failed outcome lost", decodeErr)
	}
}

func TestNodeNativeTypeScriptStripping(t *testing.T) {
	root, w := nativeFixture(t, "node-test", map[string]string{
		"package.json":  "{\"type\":\"module\"}\n",
		"value.ts":      "export const value: number = 42;\n",
		"typed.test.ts": "import {test} from 'node:test';import assert from 'node:assert/strict';import {value} from './value.ts';test('typed native source',()=>assert.equal(value,42));\n",
	}, []string{"--test", "typed.test.ts"})
	e, err := Discover(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal("native TS discovery", err)
	}
	out, err := Execute(context.Background(), root, w, model.Context{}, nil)
	if err != nil {
		t.Fatal("native TS execution", err)
	}
	result, err := nativerunner.Reconcile(bytes.NewReader(out.Stdout), "node-test", resultVersion(e), e.Units)
	if err != nil || result.Failed || len(result.Tests) != 1 || len(e.Inputs["value.ts"]) != 1 {
		t.Fatalf("native TypeScript support incomplete %+v %v", result, err)
	}
}
func TestUnittestNativeDiscoveryAndSubtests(t *testing.T) {
	root, w := nativeFixture(t, "unittest", map[string]string{
		"tests/test_value.py": "import unittest\nclass Values(unittest.TestCase):\n def test_one(self): self.assertEqual(1,1)\n def test_subs(self):\n  for value in [1,2]:\n   with self.subTest(value=value): self.assertGreater(value,0)\n @unittest.skip('private reason')\n def test_skip(self): raise Exception('skip')\n",
		"tests/test_other.py": "import unittest\nclass Other(unittest.TestCase):\n def test_other(self): pass\n",
		"input.txt":           "workspace input\n",
	}, []string{"-m", "unittest", "discover", "-s", "tests", "-p", "test_*.py", "-v"})
	e, err := Discover(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Units) != 2 || len(e.Inputs["input.txt"]) != 2 {
		t.Fatal("missing broad native ownership")
	}
	for _, name := range []string{"tests/__pycache__", "__pycache__"} {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			t.Fatal("collection bytecode modified snapshot")
		}
	}
	out, err := Execute(context.Background(), root, w, model.Context{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := nativerunner.Reconcile(bytes.NewReader(out.Stdout), "unittest", resultVersion(e), e.Units)
	if err != nil || result.Failed || len(result.Tests) != 6 {
		t.Fatalf("bad unittest results %+v %v", result, err)
	}
	cmd, err := Proposal(w, e.Units)
	if err != nil || !reflect.DeepEqual(cmd.Args[:len(w.Command.Args)], w.Command.Args) || !strings.Contains(strings.Join(cmd.Args, " "), "-k test_other.*") {
		t.Fatal("unittest discovery context was not preserved")
	}
	selected := e.Units[:1]
	out, err = Execute(context.Background(), root, w, model.Context{}, selected)
	if err != nil {
		t.Fatal("native unittest selected context", err)
	}
	if _, err := nativerunner.Reconcile(bytes.NewReader(out.Stdout), "unittest", resultVersion(e), selected); err != nil {
		t.Fatal(err)
	}
}
func TestUnittestLoadTestsAndFailFastIncomplete(t *testing.T) {
	root, w := nativeFixture(t, "unittest", map[string]string{"test_custom.py": "import unittest\nclass Custom(unittest.TestCase):\n def test_a(self): self.fail('private failure values')\n def test_b(self): pass\ndef load_tests(loader, tests, pattern): return loader.loadTestsFromTestCase(Custom)\n"}, []string{"-m", "unittest", "-f", "test_custom"})
	e, err := Discover(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	out, err := Execute(context.Background(), root, w, model.Context{}, nil)
	if err == nil {
		t.Fatal("failfast failure accepted")
	}
	result, err := nativerunner.Reconcile(bytes.NewReader(out.Stdout), "unittest", resultVersion(e), e.Units)
	if err == nil {
		t.Fatal("incomplete failfast inventory accepted")
	}
	_ = result
	if bytes.Contains(out.Stdout, []byte("private failure values")) {
		t.Fatal("assertion values leaked")
	}
}
func TestCollectionRejectsUnknownFlagsPathsAndContext(t *testing.T) {
	for runner, commands := range map[string][][]string{"node-test": {{"--test"}, {"--test", "--watch", "a.mjs"}, {"--test", "../a.mjs"}, {"--test", "--test-concurrency=0", "a.mjs"}}, "unittest": {{"-m", "unittest", "--locals"}, {"-m", "unittest", "discover", "tests"}, {"-m", "unittest", "--unknown"}}, "jest": {{"--watch"}, {"--testNamePattern"}}, "playwright": {{"test", "--ui"}, {"test", "--shard=1/2"}}} {
		tool := map[string]string{"node-test": "node", "unittest": "python3", "jest": "jest", "playwright": "playwright"}[runner]
		for _, args := range commands {
			w := model.Workspace{Adapter: runner, Command: model.Command{Executable: tool, Args: args}}
			if _, err := parse(w); err == nil {
				t.Fatalf("accepted unsupported %s argv %v", runner, args)
			}
		}
	}
	root, w := nativeFixture(t, "unittest", map[string]string{"test_a.py": "import unittest\nclass A(unittest.TestCase):\n def test_a(self): pass\n"}, []string{"-m", "unittest", "test_a"})
	if _, err := Discover(context.Background(), root, w, model.Context{OS: "different"}); err == nil {
		t.Fatal("cross context collection accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Discover(ctx, root, w, model.Context{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.py")
	if err := os.WriteFile(outside, []byte("pass"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.py")); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(context.Background(), root, w, model.Context{}); err == nil {
		t.Fatal("symlink input accepted")
	}
}
func TestJestPrivateResultsAndPathExactProposals(t *testing.T) {
	data := []byte(`{"numTotalTestSuites":1,"numRuntimeErrorTestSuites":0,"testResults":[{"name":"/snapshot/a.test.js","status":"passed","message":"private source","assertionResults":[{"fullName":"duplicate","status":"passed"},{"fullName":"duplicate","status":"pending"}]}]}`)
	report, err := jestReport(data, "/snapshot", "30.2.0")
	if err != nil || len(report.Tests) != 2 || report.Tests[0].Test == report.Tests[1].Test {
		t.Fatal("Jest identities ambiguous", err)
	}
	interrupted := bytes.Replace(data, []byte(`{"numTotalTestSuites"`), []byte(`{"wasInterrupted":true,"numTotalTestSuites"`), 1)
	stopped, err := jestReport(interrupted, "/snapshot", "30.2.0")
	if err != nil || stopped.Complete {
		t.Fatal("interrupted Jest report accepted as complete")
	}
	w := model.Workspace{ID: "w", Adapter: "jest", Command: model.Command{Executable: "jest", Dir: ".", Args: []string{"--runInBand", "--config=jest.config.js"}}}
	units := []model.Unit{{Workspace: "w", Kind: "jest-file", Selector: "b.test.js"}, {Workspace: "w", Kind: "jest-file", Selector: "a.test.js"}}
	cmd, err := Proposal(w, units)
	if err != nil || !reflect.DeepEqual(cmd.Args, []string{"--runInBand", "--config=jest.config.js", "--runTestsByPath", "a.test.js", "b.test.js"}) {
		t.Fatal("Jest context flags lost")
	}
	w.Adapter = "playwright"
	w.Command.Executable = "playwright"
	w.Command.Args = []string{"test", "--project=chromium"}
	for i := range units {
		units[i].Kind = "playwright-file"
	}
	cmd, err = Proposal(w, units)
	if err != nil || cmd.Args[2] != "(^|[/\\\\])a\\.test\\.js$" {
		t.Fatalf("Playwright selector regex not exact: %+v %v", cmd, err)
	}
}

func installedJSFixture(t *testing.T, runner string) (string, model.Workspace) {
	t.Helper()
	node, modules := os.Getenv("HAYAKU_NATIVE_NODE"), os.Getenv("HAYAKU_NATIVE_MODULES")
	if modules == "" {
		t.Skip("set HAYAKU_NATIVE_MODULES to explicitly installed native dev fixtures")
	}
	if node == "" {
		var err error
		node, err = exec.LookPath("node")
		if err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"--runInBand", "--config=jest.config.cjs"}
	files := map[string]string{"jest.config.cjs": "module.exports={testEnvironment:'node',testMatch:['**/*.test.cjs']};\n", "a.test.cjs": "test('pass',()=>expect(1).toBe(1));test.skip('skip',()=>{});\n", "b.test.cjs": "test('duplicate',()=>{});test('duplicate',()=>{});\n"}
	if runner == "playwright" {
		args = []string{"test", "--config=playwright.config.cjs"}
		api := strconv.Quote(filepath.Join(modules, "@playwright", "test"))
		files = map[string]string{"playwright.config.cjs": "module.exports={testDir:'.',testMatch:'**/*.spec.cjs',workers:1};\n", "a.spec.cjs": "const {test,expect}=require(" + api + ");test('pass',async()=>expect(1).toBe(1));test.skip('skip',async()=>{});\n", "b.spec.cjs": "const {test}=require(" + api + ");test('second',async()=>{});\n"}
	}
	root := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root, model.Workspace{ID: "native", Root: ".", Adapter: runner, Command: model.Command{Dir: ".", Executable: runner, Args: args}, NodeRuntime: &model.NodeRuntime{Node: node, Modules: modules}}
}
func TestInstalledJestAndPlaywrightNativeFullAndProposal(t *testing.T) {
	for _, runner := range []string{"jest", "playwright"} {
		t.Run(runner, func(t *testing.T) {
			root, w := installedJSFixture(t, runner)
			e, err := Discover(context.Background(), root, w, model.Context{})
			if err != nil {
				t.Fatal(err)
			}
			if len(e.Units) != 2 {
				t.Fatalf("native units %+v", e.Units)
			}
			out, err := Execute(context.Background(), root, w, model.Context{}, nil)
			if err != nil {
				t.Fatal("native full execution", err)
			}
			result, err := nativerunner.Reconcile(bytes.NewReader(out.Stdout), runner, resultVersion(e), e.Units)
			if err != nil || result.Failed || len(result.Tests) < 3 {
				t.Fatalf("native results %+v %v", result, err)
			}
			selected := e.Units[:1]
			out, err = Execute(context.Background(), root, w, model.Context{}, selected)
			if err != nil {
				t.Fatal("native selected execution", err)
			}
			if _, err := nativerunner.Reconcile(bytes.NewReader(out.Stdout), runner, resultVersion(e), selected); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestInstalledPytestCompleteFixtureOutcomes(t *testing.T) {
	python := os.Getenv("HAYAKU_NATIVE_PYTHON")
	if python == "" {
		t.Skip("set HAYAKU_NATIVE_PYTHON to explicitly installed pytest dev interpreter")
	}
	root := t.TempDir()
	files := map[string]string{"test_values.py": "import pytest\n@pytest.mark.parametrize('value',[1,2])\ndef test_values(value): assert value > 0\n@pytest.mark.skip(reason='private reason')\ndef test_skip(): pass\n", "test_other.py": "def test_other(): pass\n"}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	w := model.Workspace{ID: "native", Root: ".", Adapter: "pytest", Command: model.Command{Dir: ".", Executable: python, Args: []string{"-m", "pytest", "-q"}}}
	e, err := pytest.Discover(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	out, err := Execute(context.Background(), root, w, model.Context{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := nativerunner.Reconcile(bytes.NewReader(out.Stdout), "pytest", "9.0.2", e.Units)
	if err != nil || result.Failed || len(result.Tests) != 4 {
		t.Fatalf("pytest terminal outcomes %+v %v", result, err)
	}
	selected := e.Units[:1]
	out, err = Execute(context.Background(), root, w, model.Context{}, selected)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nativerunner.Reconcile(bytes.NewReader(out.Stdout), "pytest", "9.0.2", selected); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "conftest.py"), []byte("import pytest\n@pytest.fixture(autouse=True)\ndef fail_teardown():\n yield\n raise RuntimeError('private teardown source')\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out, err = Execute(context.Background(), root, w, model.Context{}, nil)
	if err == nil {
		t.Fatal("fixture teardown failure accepted")
	}
	result, err = nativerunner.Reconcile(bytes.NewReader(out.Stdout), "pytest", "9.0.2", e.Units)
	if err != nil || !result.Failed || bytes.Contains(out.Stdout, []byte("private teardown source")) {
		t.Fatal("pytest fixture failure or privacy lost", err)
	}
}
