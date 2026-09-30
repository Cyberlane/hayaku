package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
)

func TestRunnerIdentityCannotUseAnotherVitestInstallation(t *testing.T) {
	root := t.TempDir()
	modules := filepath.Join(root, "node_modules")
	if err := os.MkdirAll(filepath.Join(modules, "vitest"), 0700); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(modules, "vitest", "vitest.mjs")
	if err := os.WriteFile(entry, []byte("native runner"), 0700); err != nil {
		t.Fatal(err)
	}
	w := model.Workspace{Root: ".", Command: model.Command{Dir: ".", Executable: entry}, NodeRuntime: &model.NodeRuntime{Modules: modules}}
	if err := validateVitestRunner(root, w); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "vitest")
	if err := os.WriteFile(other, []byte("native runner"), 0700); err != nil {
		t.Fatal(err)
	}
	w.Command.Executable = other
	if err := validateVitestRunner(root, w); err == nil {
		t.Fatal("identical wrapper from an unbound installation accepted")
	}
}

func TestRuntimeMountCannotReplaceCommittedInputs(t *testing.T) {
	root := t.TempDir()
	modules := filepath.Join(root, "node_modules")
	if err := os.MkdirAll(modules, 0700); err != nil {
		t.Fatal(err)
	}
	installation := t.TempDir()
	node := filepath.Join(installation, "node")
	if err := os.WriteFile(node, []byte("installed runtime bytes"), 0700); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(installation, "node_modules")
	if err := os.Mkdir(installed, 0700); err != nil {
		t.Fatal(err)
	}
	c := model.Config{Workspaces: []model.Workspace{{Root: ".", Command: model.Command{Dir: "."}, NodeRuntime: &model.NodeRuntime{Node: node, Modules: installed}}}}
	if err := prepareNodeRuntimes(context.Background(), root, c); err == nil {
		t.Fatal("runtime mount over source inputs accepted")
	}
	c.Workspaces[0].NodeRuntime.Modules = filepath.Join(root, "different", "node_modules")
	if _, err := checkoutModules(root, c); err == nil {
		t.Fatal("unrelated ignored directory could be exempted")
	}
}
