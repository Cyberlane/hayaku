package vitest

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

// The fake Node validates protocol interpretation independently of a native Vite
// runtime. Native qualification uses opt-in installed runtime integration tests.
func graphFixture(t *testing.T, inventory graphInventory) (string, model.Workspace) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix protocol fixture")
	}
	root := t.TempDir()
	for _, name := range []string{"one.test.ts", "two.test.ts", "src.ts", "setup.ts", "resource.json", "vitest.config.ts"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	payload, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	node := filepath.Join(t.TempDir(), "node")
	if err = os.WriteFile(node, []byte("#!/bin/sh\nprintf '%s\\n' '"+string(payload)+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return root, model.Workspace{ID: "js", Root: ".", Command: model.Command{Dir: ".", Executable: "vitest", Args: []string{"run"}}, NodeRuntime: &model.NodeRuntime{Node: node, Modules: t.TempDir()}}
}
func graphData() graphInventory {
	return graphInventory{Schema: 1, Version: "4.1.11", Global: []string{"setup.ts", "vitest.config.ts"}, Scopes: []graphScope{{File: "one.test.ts", Project: "one", Dependencies: []string{"one.test.ts", "src.ts", "setup.ts", "resource.json", "vitest.config.ts"}}, {File: "two.test.ts", Project: "two", Dependencies: []string{"two.test.ts"}}}}
}
func TestConfiguredGraphOwnersRetainGlobalAndResources(t *testing.T) {
	root, w := graphFixture(t, graphData())
	got, err := Discover(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Inputs["src.ts"], []string{"js:one:one.test.ts"}) {
		t.Fatalf("native graph ownership: %v", got.Inputs)
	}
	for _, name := range []string{"resource.json", "setup.ts", "vitest.config.ts"} {
		if len(got.Inputs[name]) != 2 {
			t.Fatalf("global/resource input narrowed: %s", name)
		}
	}
	if len(got.Gaps) != 1 || got.Gaps[0].Code != "runtime-unqualified" {
		t.Fatalf("qualification overstated: %+v", got.Gaps)
	}
}
func TestIncompleteTransformBroadensEveryGraphInput(t *testing.T) {
	data := graphData()
	data.Scopes[0].Incomplete = true
	root, w := graphFixture(t, data)
	got, err := Discover(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	for path, owners := range got.Inputs {
		if len(owners) != 2 {
			t.Fatalf("incomplete graph narrowed %s: %v", path, owners)
		}
	}
	if len(got.Gaps) != 2 {
		t.Fatal("incomplete graph gap lost")
	}
}
func TestRejectUnqualifiedOrEscapingGraph(t *testing.T) {
	for _, mutate := range []func(*graphInventory){func(g *graphInventory) { g.Version = "5.0.0" }, func(g *graphInventory) { g.Scopes[0].File = "../outside.ts" }, func(g *graphInventory) { g.Scopes[0].Dependencies = []string{"missing.ts"} }, func(g *graphInventory) { g.Scopes[0].Dependencies = []string{"../escape.ts"} }, func(g *graphInventory) { g.Scopes[1] = g.Scopes[0] }} {
		data := graphData()
		mutate(&data)
		root, w := graphFixture(t, data)
		if _, err := Discover(context.Background(), root, w, model.Context{}); err == nil {
			t.Fatalf("invalid native graph accepted %+v", data)
		}
	}
}

// The bundled loader may leave an empty generated directory. Verify restoration
// does not hide pre-existing directories, unexpected files, modes or symlinks.
func TestNativeBundlerTemporaryDirectoryRestoration(t *testing.T) {
	node := os.Getenv("HAYAKU_VITEST_NODE")
	if node == "" {
		var err error
		node, err = exec.LookPath("node")
		if err != nil {
			t.Skip("existing Node runtime required for embedded bridge helper fixture")
		}
	}
	if runtime.GOOS == "windows" {
		t.Skip("Unix directory permission fixture")
	}
	cases := []struct {
		name, create                                    string
		preexisting, external, wantError, wantDirectory bool
	}{
		{name: "new-empty", create: `fs.mkdirSync(bundlerDirectory,{mode:0o755});fs.chmodSync(bundlerDirectory,0o755);`},
		{name: "preexisting-empty", preexisting: true, wantDirectory: true},
		{name: "new-nonempty", create: `fs.mkdirSync(bundlerDirectory,{mode:0o755});fs.chmodSync(bundlerDirectory,0o755);fs.writeFileSync(path.join(bundlerDirectory,'unexpected'),'preserve');`, wantError: true, wantDirectory: true},
		{name: "new-unexpected-mode", create: `fs.mkdirSync(bundlerDirectory,{mode:0o700});fs.chmodSync(bundlerDirectory,0o700);`, wantError: true, wantDirectory: true},
		{name: "external-runtime", create: `fs.mkdirSync(bundlerDirectory,{mode:0o755});`, external: true, wantDirectory: true},
		{name: "new-symlink", create: `fs.symlinkSync(input.modules,bundlerDirectory);`, wantError: true, wantDirectory: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cwd := t.TempDir()
			modules := filepath.Join(cwd, "node_modules")
			if err := os.Mkdir(modules, 0755); err != nil {
				t.Fatal(err)
			}
			generated := filepath.Join(modules, ".vite-temp")
			if tc.preexisting {
				if err := os.Mkdir(generated, 0755); err != nil {
					t.Fatal(err)
				}
			}
			if tc.external {
				modules = t.TempDir()
			}
			payload, err := json.Marshal(map[string]string{"cwd": cwd, "modules": modules})
			if err != nil {
				t.Fatal(err)
			}
			prefix, _, ok := strings.Cut(nativeBridge, "const relative = file =>")
			if !ok {
				t.Fatal("bridge helper boundary missing")
			}
			script := filepath.Join(t.TempDir(), "fixture.mjs")
			if err = os.WriteFile(script, []byte(prefix+"\n"+tc.create+"\nawait cleanBundlerDirectory();\n"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err = exec.CommandContext(ctx, node, script, string(payload)).Run()
			if (err != nil) != tc.wantError {
				t.Fatalf("cleanup error=%v wantederror=%v", err, tc.wantError)
			}
			_, err = os.Lstat(generated)
			exists := err == nil
			if exists != tc.wantDirectory {
				t.Fatalf("directory preserved=%v wanted=%v", exists, tc.wantDirectory)
			}
			if tc.name == "new-nonempty" {
				content, err := os.ReadFile(filepath.Join(generated, "unexpected"))
				if err != nil || string(content) != "preserve" {
					t.Fatal("unexpected runtime content removed")
				}
			}
		})
	}
}
