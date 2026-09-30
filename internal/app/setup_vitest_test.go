package app

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestInitDetectsVitestWithoutExecutingPackageScripts(t *testing.T) {
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	data := `{"devDependencies":{"vitest":"4.1.11"},"scripts":{"test":"touch never-run; vitest --changed"}}`
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	if status := initialize(root, "hayaku.json", &out, &errout); status != 0 {
		t.Fatalf("initialization failed: %s", errout.String())
	}
	c, err := readConfig(root, "hayaku.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Workspaces) != 1 || c.Workspaces[0].Adapter != "vitest" || !reflect.DeepEqual(c.Workspaces[0].Command.Args, []string{"run"}) || c.Workspaces[0].NodeRuntime != nil {
		t.Fatalf("wrong Vitest template: %+v", c.Workspaces)
	}
	if _, err := os.Stat(filepath.Join(root, "never-run")); !os.IsNotExist(err) {
		t.Fatal("package script was executed")
	}
	if status := initialize(root, "hayaku.json", &out, &errout); status == 0 {
		t.Fatal("existing reviewed configuration overwritten")
	}
}
