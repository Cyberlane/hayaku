package vitest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
)

func runtimeFixture(t *testing.T) RuntimeRequest {
	t.Helper()
	root, modules := t.TempDir(), t.TempDir()
	for name, text := range map[string]string{"helper.ts": "export const value: number = 7;", "alpha.test.ts": "import {test,expect} from 'vitest'; test('alpha',()=>{expect(7).toBe(7)});", "beta.test.ts": "import {test,expect} from 'vitest'; test('beta',()=>{expect(9).toBe(9)});"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for name, version := range map[string]string{"vitest": "4.1.11", "vite": "8.2.2", "rolldown": "1.2.12"} {
		if err := os.Mkdir(filepath.Join(modules, name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(modules, name, "package.json"), []byte(`{"version":"`+version+`"}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return RuntimeRequest{Root: root, Modules: modules, Workspace: "ts", Sources: []string{"helper.ts", "alpha.test.ts", "beta.test.ts"}, Tests: []string{"beta.test.ts", "alpha.test.ts"}}
}

func TestRestrictedVitestRuntimePreflightAndInvocation(t *testing.T) {
	r := runtimeFixture(t)
	for _, mutate := range []func(*RuntimeRequest){func(r *RuntimeRequest) { r.Root = "relative" }, func(r *RuntimeRequest) { r.Workspace = "ts:other" }, func(r *RuntimeRequest) { r.Sources = []string{"../escape.ts"} }, func(r *RuntimeRequest) { r.Tests = []string{"missing.ts"} }, func(r *RuntimeRequest) { r.Tests = []string{"alpha.test.ts", "alpha.test.ts"} }, func(r *RuntimeRequest) { r.Sources = []string{"file.jsx"} }} {
		bad := r
		mutate(&bad)
		if p, err := PrepareRuntime(context.Background(), bad); err == nil || p != nil {
			t.Fatal("invalid profile admitted")
		}
	}
	p, err := PrepareRuntime(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	}()
	// Caller mutation cannot rewrite frozen ownership or selection.
	r.Tests[0] = "absent.test.ts"
	units := p.Units()
	if len(units) != 2 || units[0].ID != "ts::alpha.test.ts" || len(p.Digest()) != 64 {
		t.Fatal("unstable captured scope")
	}
	args, env, err := p.Invocation(context.Background(), "run", units[:1], "/runtime/#addon-rolldown.node")
	if err != nil || len(env) != 3 || len(args) != 3 || args[0] != "--openssl-config=/resources/.hayaku/openssl.cnf" {
		t.Fatalf("invocation: %v", err)
	}
	var payload struct {
		Profile  string   `json:"profile"`
		Compiler string   `json:"compiler"`
		Selected []string `json:"selected"`
		Tests    []string `json:"tests"`
	}
	if json.Unmarshal([]byte(args[len(args)-1]), &payload) != nil || payload.Profile != RuntimeProfile || payload.Compiler != RuntimeCompilerProfile || len(payload.Selected) != 1 || payload.Selected[0] != units[0].ID || len(payload.Tests) != 2 {
		t.Fatal("selection changed configured inventory")
	}
	for _, selected := range [][]model.Unit{{}, {units[0], units[0]}, {{ID: "foreign", Workspace: "ts", Kind: "vitest-file"}}} {
		if _, _, err := p.Invocation(context.Background(), "run", selected, "/runtime/#addon-rolldown.node"); err == nil {
			t.Fatal("invalid selection accepted")
		}
	}
	for _, mode := range []string{"discover", "observe", "unknown"} {
		if _, _, err := p.Invocation(context.Background(), mode, nil, "/runtime/#addon-rolldown.node"); err == nil {
			t.Fatal("unknown operation accepted")
		}
	}
	if _, _, err := p.Invocation(context.Background(), "collect", nil, "/runtime/#addon-forged.node"); err == nil {
		t.Fatal("foreign executable role accepted")
	}
}

func TestRestrictedVitestRuntimeStableIdentityAndDrift(t *testing.T) {
	r := runtimeFixture(t)
	first, err := PrepareRuntime(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := PrepareRuntime(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if first.directory == second.directory || first.Digest() != second.Digest() {
		t.Fatal("fresh physical copies contaminated stable functional identity")
	}
	if err := os.WriteFile(filepath.Join(first.root, ".hayaku/runtime.mjs"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if first.Verify(context.Background()) == nil || second.Verify(context.Background()) != nil {
		t.Fatal("private-copy drift or independent owner guard broken")
	}
	if err := os.WriteFile(filepath.Join(r.Root, "helper.ts"), []byte("export const value=8;"), 0600); err != nil {
		t.Fatal(err)
	}
	if second.Verify(context.Background()) == nil {
		t.Fatal("original dormant-source drift ignored")
	}
}

func TestRestrictedVitestRuntimeCollectionAndRetainedCleanup(t *testing.T) {
	p, err := PrepareRuntime(context.Background(), runtimeFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	data := collectedInventory{Schema: 1, Version: "4.1.11", ViteVersion: "8.2.2", Files: []collectionFile{{File: "alpha.test.ts"}, {File: "beta.test.ts"}}, Cases: []collectionCase{{File: "alpha.test.ts", Test: `["alpha",0]`}, {File: "beta.test.ts", Test: `["beta",0]`}}}
	encoded, _ := json.Marshal(data)
	inventory, err := p.DecodeCollection(process.Output{Completed: true, Stdout: encoded})
	if err != nil {
		t.Fatal(err)
	}
	if err = MatchCases(inventory, inventory); err != nil {
		t.Fatal(err)
	}
	inventory.Cases[0].ID = `["foreign",0]`
	want, err := p.DecodeCollection(process.Output{Completed: true, Stdout: encoded})
	if err != nil {
		t.Fatal(err)
	}
	if MatchCases(inventory, want) == nil {
		t.Fatal("foreign native case accepted")
	}
	marker := filepath.Join(p.directory, "unrelated-owner-file")
	if err = os.WriteFile(marker, []byte("retain"), 0600); err != nil {
		t.Fatal(err)
	}
	if p.Close() == nil || p.Verify(context.Background()) == nil {
		t.Fatal("cleanup failure retained usable runtime")
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "retain" {
		t.Fatal("unknown sibling deleted")
	}
	if err = os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err = p.Close(); err != nil {
		t.Fatal(err)
	}
	if err = p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(p.directory); !os.IsNotExist(err) {
		t.Fatal("owned resources leaked")
	}
}

func TestRestrictedVitestRuntimeRejectsVersionAndMissingModules(t *testing.T) {
	r := runtimeFixture(t)
	if err := os.WriteFile(filepath.Join(r.Modules, "vite/package.json"), []byte(`{"version":"8.2.3"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if p, err := PrepareRuntime(context.Background(), r); err == nil || p != nil {
		t.Fatal("unaccepted tool version admitted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if p, err := PrepareRuntime(ctx, r); err == nil || p != nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatal("canceled preparation admitted")
	}
}

func TestRestrictedVitestRuntimeSameBytesReplacementInvalidates(t *testing.T) {
	for _, role := range []string{"source", "module", "copied"} {
		t.Run(role, func(t *testing.T) {
			r := runtimeFixture(t)
			p, err := PrepareRuntime(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			filename := filepath.Join(r.Root, "helper.ts")
			if role == "module" {
				filename = filepath.Join(r.Modules, "vitest/package.json")
			}
			if role == "copied" {
				filename = filepath.Join(p.root, ".hayaku/runtime.mjs")
			}
			data, err := os.ReadFile(filename)
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(filename)
			if err != nil {
				t.Fatal(err)
			}
			replacement := filename + ".replacement"
			if err = os.WriteFile(replacement, data, info.Mode().Perm()); err != nil {
				t.Fatal(err)
			}
			if err = os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
				t.Fatal(err)
			}
			if err = os.Rename(replacement, filename); err != nil {
				t.Fatal(err)
			}
			if p.Verify(context.Background()) == nil {
				t.Fatal("same bytes and restored time concealed selected identity replacement")
			}
		})
	}
}

func TestRestrictedVitestRuntimeFailedCreationReleasesOwner(t *testing.T) {
	injected := errors.New("injected after private owner retained")
	var failed *PreparedRuntime
	var owner *os.Root
	var directory, marker string
	p, err := prepareRuntime(context.Background(), runtimeFixture(t), func(created *PreparedRuntime) error {
		failed, owner, directory = created, created.owner, created.directory
		marker = filepath.Join(directory, "unknown-sibling")
		if err := os.WriteFile(marker, []byte("preserve"), 0600); err != nil {
			return err
		}
		if err := os.Mkdir(filepath.Join(directory, "resources"), 0700); err != nil {
			return err
		}
		return injected
	})
	if p != nil || !errors.Is(err, injected) || failed == nil || failed.owner != nil {
		t.Fatal("failed preparation retained an owner or lost its error", err)
	}
	if _, err := owner.Stat("."); err == nil {
		t.Fatal("failed preparation leaked its root handle")
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "preserve" {
		t.Fatal("failed preparation deleted unknown sibling", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "resources")); !os.IsNotExist(err) {
		t.Fatal("failed preparation retained known resources", err)
	}
	if failed.Verify(context.Background()) == nil || failed.Close() != nil || failed.Abandon() != nil {
		t.Fatal("abandoned preparation remained usable or cleanup was not terminal")
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
}
