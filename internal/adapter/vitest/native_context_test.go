package vitest

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
	vitestrunner "github.com/Cyberlane/hayaku/internal/runner/vitest"
)

func boundContextFixture(t *testing.T, configuration string) (string, model.Workspace) {
	t.Helper()
	node, modules := os.Getenv("HAYAKU_VITEST_NODE"), os.Getenv("HAYAKU_VITEST_MODULES")
	if node == "" && modules == "" {
		t.Skip("set explicit installed Node and pinned Vitest module paths for native context fixtures")
	}
	if !filepath.IsAbs(node) || !filepath.IsAbs(modules) {
		t.Fatal("native context fixtures require absolute installed runtime paths")
	}
	root := t.TempDir()
	for name, contents := range map[string]string{
		"package.json":      `{"private":true,"type":"module"}`,
		"vitest.config.mjs": configuration,
		"value.ts":          "export const value: number = 7;\n",
		"value.test.ts":     "import { value } from './value';\ntest('native TypeScript', () => { expect(value).toBe(7) });\ntest.skip('retained skip', () => {});\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root, model.Workspace{ID: "js", Root: ".", Adapter: "vitest", Command: model.Command{Dir: ".", Executable: filepath.Join(modules, ".bin", "vitest"), Args: []string{"run", "--globals", "--config", "vitest.config.mjs"}}, NodeRuntime: &model.NodeRuntime{Node: node, Modules: modules}}
}

func TestNativeBoundNodePoolsPreserveTerminalResults(t *testing.T) {
	for _, pool := range []string{"forks", "threads"} {
		t.Run(pool, func(t *testing.T) {
			root, w := boundContextFixture(t, "export default { test: { globals: true, fileParallelism: false, pool: '"+pool+"' } };\n")
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			runtimeContext := model.Context{Env: map[string]string{"CI": "true"}}
			evidence, err := Discover(ctx, root, w, runtimeContext)
			if err != nil {
				t.Fatal(err)
			}
			if len(evidence.Units) != 1 || len(evidence.Inputs["value.ts"]) != 1 || len(evidence.Gaps) != 1 || evidence.Gaps[0].Code != "runtime-unqualified" {
				t.Fatalf("native context evidence: %+v", evidence)
			}
			output, err := Execute(ctx, root, w, runtimeContext, nil)
			if err != nil {
				t.Fatal(err)
			}
			result, err := vitestrunner.Reconcile(bytes.NewReader(output.Stdout), evidence.Units)
			if err != nil || result.Failed || len(result.Files) != 1 || len(result.Tests) != 2 || len(result.Missing) != 0 {
				t.Fatalf("native terminal outcomes: %v %+v", err, result)
			}
			outcomes := map[string]int{}
			for _, test := range result.Tests {
				outcomes[test.Action]++
			}
			if outcomes["pass"] != 1 || outcomes["skip"] != 1 {
				t.Fatalf("pass/skip outcomes changed: %+v", result.Tests)
			}
		})
	}
}

func TestNativeBoundUnreviewedExecutionContextsReject(t *testing.T) {
	for name, configuration := range map[string]string{
		"worker-pool":    "export default { test: { pool: '@cloudflare/vitest-pool-workers' } };\n",
		"vm-thread-pool": "export default { test: { pool: 'vmThreads' } };\n",
		"vm-fork-pool":   "export default { test: { pool: 'vmForks' } };\n",
		"browser":        "export default { test: { browser: { enabled: true, headless: true, provider: 'playwright' } } };\n",
		"typecheck":      "export default { test: { typecheck: { enabled: true } } };\n",
	} {
		t.Run(name, func(t *testing.T) {
			root, w := boundContextFixture(t, configuration)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if _, err := Discover(ctx, root, w, model.Context{Env: map[string]string{"CI": "true"}}); err == nil {
				t.Fatal("unreviewed runtime context returned narrow native evidence")
			}
		})
	}
}

func TestNativeBridgeRejectsInstalledUnqualifiedVersionBeforeAPIImport(t *testing.T) {
	node := os.Getenv("HAYAKU_VITEST_NODE")
	if !filepath.IsAbs(node) {
		t.Skip("explicit Node runtime required for installed version gate")
	}
	root := t.TempDir()
	for _, pair := range [][2]string{{"4.1.12", "7.3.1"}, {"3.2.7", "8.1.5"}, {"4.1.11", "8.1.4"}} {
		modules := t.TempDir()
		for name, version := range map[string]string{"vitest": pair[0], "vite": pair[1]} {
			if err := os.Mkdir(filepath.Join(modules, name), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(modules, name, "package.json"), []byte(`{"name":"`+name+`","version":"`+version+`"}`), 0600); err != nil {
				t.Fatal(err)
			}
		}
		w := model.Workspace{ID: "js", Root: ".", Command: model.Command{Dir: ".", Executable: "vitest", Args: []string{"run"}}, NodeRuntime: &model.NodeRuntime{Node: node, Modules: modules}}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		output, err := bridge(ctx, root, w, model.Context{Env: map[string]string{"CI": "true"}}, "discover", nil)
		cancel()
		if err == nil || output.ExitCode != 2 || len(output.Stdout) != 0 {
			t.Fatalf("unknown installed version produced native protocol: %v %+v", err, output)
		}
	}
}
