package native

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
)

func vitestWorkspaceFixture(t *testing.T) (string, Workspace, RunConfig) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{
		VitestProfileFile: VitestProfile, "package.json": VitestPackage,
		"alpha.test.ts": `import {test,expect} from 'vitest'; import {value} from './shared.js'; test('alpha',()=>{expect(value).toBe(3)});`,
		"beta.test.js":  `import {test,expect} from 'vitest'; test('beta',()=>{expect(2).toBe(2)});`,
		"shared.ts":     `export const value: number = 3;`, "README.md": "Fixture docs\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0644); err != nil {
			t.Fatal(err)
		}
	}
	w := Workspace{Workspace: model.Workspace{ID: "ts", Root: ".", Adapter: "vitest", NodeRuntime: &model.NodeRuntime{Node: "/sdk/host-node", Modules: "/sdk/node_modules"}}, Vitest: &VitestRuntimeInputs{Node: "/sdk/linux-node", Addon: "/sdk/rolldown.node"}}
	c := RunConfig{Config: Config{Schema: 1, Platform: "linux", Architecture: "arm64", Image: vitestOriginalImage}, Workspaces: []Workspace{w}}
	w.Command = VitestOriginalCommand("/installed/docker", root, w.NodeRuntime.Modules, w.Vitest.Node, w.Vitest.Addon, c.Image)
	c.Workspaces[0] = w
	return root, w, c
}

func TestVitestWorkspaceControllerAdmissionKeepsScopeCapturePortable(t *testing.T) {
	for _, tc := range []struct {
		os, arch string
		accepted bool
	}{
		{"darwin", "arm64", true}, {"linux", "arm64", true},
		{"darwin", "amd64", false}, {"linux", "amd64", false},
		{"windows", "arm64", false}, {"windows", "amd64", false},
		{"freebsd", "arm64", false}, {"linux", "386", false},
	} {
		if err := verifyVitestController(tc.os, tc.arch); (err == nil) != tc.accepted {
			t.Fatalf("controller %s/%s admission differs: %v", tc.os, tc.arch, err)
		}
	}
	root, w, c := vitestWorkspaceFixture(t)
	proof, err := captureVitestWorkspaceProof(context.Background(), root, w, c, "/installed/docker")
	if err != nil || proof.verify(context.Background()) != nil {
		t.Fatal("descriptive complete scope unavailable", err)
	}
	accepted := runtime.GOARCH == "arm64" && (runtime.GOOS == "darwin" || runtime.GOOS == "linux")
	admitted, err := admitVitestWorkspace(context.Background(), root, w, c, "/installed/docker")
	if (err == nil) != accepted || (admitted != nil) != accepted {
		t.Fatal("actual workspace admission did not enforce controller profile", err)
	}
	// A descriptive capture cannot bypass the independent private issuer check.
	err = verifyVitestIssuerScope(context.Background(), nil, proof, model.ExecutionInventory{})
	if err == nil || strings.Contains(err.Error(), "controller") == accepted {
		t.Fatal("issuer did not enforce actual controller before runtime owners", err)
	}
}

func TestVitestWorkspaceCompleteScopeAndOriginalInvocation(t *testing.T) {
	root, w, c := vitestWorkspaceFixture(t)
	proof, err := captureVitestWorkspaceProof(context.Background(), root, w, c, "/installed/docker")
	if err != nil || proof.verify(context.Background()) != nil {
		t.Fatal("healthy original profile", err)
	}
	if !slices.Equal(proof.scope.sources, []string{"alpha.test.ts", "beta.test.js", "shared.ts"}) || !slices.Equal(proof.scope.tests, []string{"alpha.test.ts", "beta.test.js"}) {
		t.Fatal("complete authored scope differs", proof.scope.sources, proof.scope.tests)
	}
	if err := os.WriteFile(filepath.Join(root, "new.test.ts"), []byte(`import {test,expect} from 'vitest';test('new',()=>{expect(3).toBe(3)})`), 0644); err != nil {
		t.Fatal(err)
	}
	if proof.verify(context.Background()) == nil {
		t.Fatal("new test escaped complete inventory")
	}
	if err := os.Remove(filepath.Join(root, "new.test.ts")); err != nil {
		t.Fatal(err)
	}
	proof, err = captureVitestWorkspaceProof(context.Background(), root, w, c, "/installed/docker")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "beta.test.js")); err != nil {
		t.Fatal(err)
	}
	if proof.verify(context.Background()) == nil {
		t.Fatal("deleted test escaped complete inventory")
	}
}

func TestVitestWorkspaceRejectsChangedOriginalContext(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Workspace, *RunConfig)
	}{
		{"extra filter", func(w *Workspace, c *RunConfig) { w.Command.Args = append(w.Command.Args, "alpha.test.ts") }},
		{"extra environment", func(w *Workspace, c *RunConfig) {
			c.Environment = map[string]string{"NODE_OPTIONS": "--require=changed.cjs"}
		}},
		{"prerequisite", func(w *Workspace, c *RunConfig) {
			w.Prerequisites = []model.Command{{Dir: ".", Executable: "/usr/bin/true"}}
		}},
		{"image", func(w *Workspace, c *RunConfig) { c.Image = "sha256:" + identity("another") }},
		{"host runner", func(w *Workspace, c *RunConfig) { w.Command.Executable = "/installed/vitest" }},
		{"platform", func(w *Workspace, c *RunConfig) { c.Platform = "darwin" }},
		{"configuration inputs", func(w *Workspace, c *RunConfig) { w.Inputs = []string{"undeclared-transform"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, w, c := vitestWorkspaceFixture(t)
			tc.mutate(&w, &c)
			if proof, err := captureVitestWorkspaceProof(context.Background(), root, w, c, "/installed/docker"); err == nil || proof != nil {
				t.Fatal("changed configured scope received issuer admission")
			}
		})
	}
}

func TestVitestWorkspaceRejectsAmbientConfigurationAndPhysicalDrift(t *testing.T) {
	for _, name := range []string{"tsconfig.json", "vite.config.mjs", "vite.config.ts", ".env.production", "setup.cjs", "nested/package.json"} {
		t.Run(name, func(t *testing.T) {
			root, w, c := vitestWorkspaceFixture(t)
			path := filepath.Join(root, name)
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("{}"), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := captureVitestWorkspaceProof(context.Background(), root, w, c, "/installed/docker"); err == nil {
				t.Fatal("ambient compiler/module configuration admitted")
			}
		})
	}
	for _, fault := range []string{"config bytes", "source bytes", "source inode", "module alias", "private profile seal"} {
		t.Run(fault, func(t *testing.T) {
			root, w, c := vitestWorkspaceFixture(t)
			proof, err := captureVitestWorkspaceProof(context.Background(), root, w, c, "/installed/docker")
			if err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "config bytes":
				err = os.WriteFile(filepath.Join(root, VitestProfileFile), []byte(VitestProfile+"// changed\n"), 0644)
			case "source bytes":
				err = os.WriteFile(filepath.Join(root, "shared.ts"), []byte("export const value=4;"), 0644)
			case "source inode":
				path := filepath.Join(root, "shared.ts")
				var data []byte
				data, err = os.ReadFile(path)
				if err == nil {
					err = os.Rename(path, path+".old")
				}
				if err == nil {
					err = os.WriteFile(path, data, 0644)
				}
			case "module alias":
				err = os.Symlink(filepath.Join(root, "shared.ts"), filepath.Join(root, "extra.ts"))
			case "private profile seal":
				proof.profile = identity("forged original invocation")
			}
			if err != nil {
				t.Fatal(err)
			}
			if proof.verify(context.Background()) == nil {
				t.Fatal("physical/source/private admission drift retained authority")
			}
		})
	}
}
