package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

func TestGenericNodeRuntimePreservesFullCommandAndRejectsDrift(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("installed Node required")
	}
	node, err = filepath.EvalSymlinks(node)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	modules := filepath.Join(t.TempDir(), "node_modules")
	nativeAppWrite(t, filepath.Dir(modules), "node_modules/fixture/package.json", `{"type":"module","exports":"./index.js"}`)
	nativeAppWrite(t, filepath.Dir(modules), "node_modules/fixture/index.js", "export const value=7;\n")
	nativeAppWrite(t, root, "check.mjs", "import assert from 'node:assert/strict';import {value} from 'fixture';assert.equal(value,7);\n")
	nativeAppGit(t, root, "init", "-q")
	nativeAppGit(t, root, "config", "user.name", "Fixture")
	nativeAppGit(t, root, "config", "user.email", "fixture@example.test")
	base := nativeAppCommit(t, root, "base")
	c := model.Config{Schema: 1, Context: model.Context{ID: "generic-node", OS: runtime.GOOS, Arch: runtime.GOARCH}, Workspaces: []model.Workspace{{ID: "node", Root: ".", Adapter: "command", Command: model.Command{Dir: ".", Executable: node, Args: []string{"check.mjs"}}, NodeRuntime: &model.NodeRuntime{Node: node, Modules: modules}}}}
	p, err := Build(context.Background(), root, base, base, c, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Selected) != 1 || len(p.Proposed) != 1 || strings.Join(p.Commands[0].Args, " ") != "check.mjs" {
		t.Fatal("generic full scope changed")
	}
	r, err := Execute(context.Background(), root, c, p)
	if err != nil || !r.Passed {
		t.Fatal("bound generic command could not execute", err)
	}
	nativeAppWrite(t, filepath.Dir(modules), "node_modules/fixture/index.js", "export const value=8;\n")
	if _, err := Execute(context.Background(), root, c, p); err == nil {
		t.Fatal("installed dependency drift accepted")
	}
	c.Workspaces[0].Command.Executable = "other-node"
	if err := validateVitestRunner(root, c.Workspaces[0]); err == nil {
		t.Fatal("unbound generic interpreter accepted")
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
