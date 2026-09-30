package vitest

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
)

// This fixture verifies the documented native file-list protocol, not actual
// installed Vitest qualification. A missing native runner is never installed.
func protocolFixture(t *testing.T) (string, model.Workspace) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix fake executable protocol fixture")
	}
	root := t.TempDir()
	for _, path := range []string{"one.test.ts", "two.test.ts", "setup.ts", "resource.json"} {
		if err := os.WriteFile(filepath.Join(root, path), []byte("fixture\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	data, err := json.Marshal([]fileScope{{File: "one.test.ts", Project: "node"}, {File: "one.test.ts", Project: "browser"}, {File: "two.test.ts", Project: "node"}})
	if err != nil {
		t.Fatal(err)
	}
	runner := filepath.Join(root, "vitest")
	script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then printf '%s\\n' 'vitest/3.2.4 fixture'; exit 0; fi\nif [ \"$1\" != \"list\" ] || [ \"$2\" != \"--filesOnly\" ] || [ \"$3\" != \"--json\" ]; then exit 2; fi\nprintf '%s\\n' '" + string(data) + "'\n"
	if err := os.WriteFile(runner, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return root, model.Workspace{ID: "js", Root: ".", Command: model.Command{Dir: ".", Executable: runner, Args: []string{"run", "--project", "node", "--project", "browser"}}}
}
func TestWholeWorkspaceOwnershipAndProjects(t *testing.T) {
	root, w := protocolFixture(t)
	e, err := Discover(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Units) != 3 {
		t.Fatalf("project scopes %+v", e.Units)
	}
	for _, path := range []string{"one.test.ts", "two.test.ts", "setup.ts", "resource.json"} {
		if len(e.Inputs[path]) != 3 {
			t.Fatalf("incomplete conservative ownership %s: %v", path, e.Inputs[path])
		}
	}
	command, err := Proposal(w, e.Units)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"run", "--project", "node", "--project", "browser", "one.test.ts", "two.test.ts"}
	if !reflect.DeepEqual(command.Args, want) {
		t.Fatalf("proposal %v", command.Args)
	}
	if len(e.Gaps) < 3 {
		t.Fatal("protocol fixture must not certify native graph qualification")
	}
	if err := os.Remove(filepath.Join(root, "one.test.ts")); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(context.Background(), root, w, model.Context{}); err == nil {
		t.Fatal("missing discovered file accepted")
	}
}
func TestUnsupportedScopeAndMissingNative(t *testing.T) {
	w := model.Workspace{Command: model.Command{Executable: "vitest", Args: []string{"run"}}}
	for _, args := range [][]string{{"watch"}, {"run", "-t", "partial test name"}, {"run", "x.test.ts:3"}, {"run", "--root", "/other"}, {"run", "--config"}, {"run", "--changed"}} {
		w.Command.Args = args
		if _, _, err := parse(w); err == nil {
			t.Fatalf("unsupported arguments accepted %v", args)
		}
	}
	root := t.TempDir()
	w.Command = model.Command{Executable: filepath.Join(root, "vitest"), Args: []string{"run"}}
	if _, err := Discover(context.Background(), root, w, model.Context{}); err == nil {
		t.Fatal("missing runner accepted")
	}
}

// HAYAKU_VITEST_BINARY opts into an existing runtime without changing PATH or
// installing packages. Only this disposable fixture's config/tests are loaded.
func TestInstalledVitestInventory(t *testing.T) {
	runner := os.Getenv("HAYAKU_VITEST_BINARY")
	var err error
	if runner != "" {
		if !filepath.IsAbs(runner) {
			t.Fatal("HAYAKU_VITEST_BINARY must be an absolute existing runner path")
		}
		runner, err = exec.LookPath(runner)
		if err != nil {
			t.Fatal("configured native Vitest runner is unavailable")
		}
	} else {
		runner, err = exec.LookPath("vitest")
		if err != nil {
			t.Skip("Vitest not installed; set HAYAKU_VITEST_BINARY to test an existing runtime")
		}
	}
	root := t.TempDir()
	files := map[string]string{
		"package.json":     "{\"name\":\"hayaku-native-fixture\",\"type\":\"module\"}\n",
		"vitest.config.js": "export default { test: { projects: [ { test: { name: 'one', include: ['one.test.js'] } }, { test: { name: 'two', include: ['two.test.js'] } } ] } }\n",
		"one.test.js":      "test('one native fixture', () => { expect(1).toBe(1) })\n",
		"two.test.js":      "test('two native fixture', () => { expect(2).toBe(2) })\n",
	}
	for path, data := range files {
		if err := os.WriteFile(filepath.Join(root, path), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	w := model.Workspace{ID: "js", Root: ".", Command: model.Command{Dir: ".", Executable: runner, Args: []string{"run", "--globals", "--config", "vitest.config.js"}}}
	e, err := Discover(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Units) != 2 {
		t.Fatalf("native multi-project inventory incorrect: %+v", e.Units)
	}
	projects := map[string]bool{}
	for _, unit := range e.Units {
		projects[unit.ID] = true
	}
	if !projects["js:one:one.test.js"] || !projects["js:two:two.test.js"] {
		t.Fatal("native project names were not preserved")
	}
	command, err := Proposal(w, e.Units)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = process.Run(context.Background(), command, root, map[string]string{"CI": "true"}); err != nil {
		t.Fatal("native multi-project fixture run failed")
	}
	// A failing excluded project must not leak into the configured project scope.
	if err := os.WriteFile(filepath.Join(root, "two.test.js"), []byte("test('excluded failure', () => { expect(1).toBe(2) })\n"), 0600); err != nil {
		t.Fatal(err)
	}
	w.Command.Args = append(w.Command.Args, "--project", "one")
	selected, err := Discover(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected.Units) != 1 || selected.Units[0].ID != "js:one:one.test.js" {
		t.Fatal("native project filter scope was not preserved")
	}
	command, err = Proposal(w, selected.Units)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = process.Run(context.Background(), command, root, map[string]string{"CI": "true"}); err != nil {
		t.Fatal("native project-filtered fixture unexpectedly ran excluded failure")
	}
}
