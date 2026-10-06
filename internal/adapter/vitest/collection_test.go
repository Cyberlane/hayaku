package vitest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
)

func collectionFixture() (collectedInventory, model.Evidence) {
	data := collectedInventory{Schema: 1, Version: "4.1.11", ViteVersion: "8.2.2",
		Files: []collectionFile{{File: "one.test.ts", Project: "one"}, {File: "one.test.ts", Project: "two"}},
		Cases: []collectionCase{{File: "one.test.ts", Project: "one", Test: `["duplicate",0]`}, {File: "one.test.ts", Project: "one", Test: `["duplicate",1]`}, {File: "one.test.ts", Project: "two", Test: `["duplicate",0]`}},
	}
	evidence := model.Evidence{Version: "vitest-vite-graph-v1:4.1.11:vite:8.2.2", Units: []model.Unit{
		{ID: "js:one:one.test.ts", Workspace: "js", Kind: "vitest-file"}, {ID: "js:two:one.test.ts", Workspace: "js", Kind: "vitest-file"},
	}}
	return data, evidence
}

func TestIndependentCollectionRejectsIncompleteOrConflictingCases(t *testing.T) {
	for name, mutate := range map[string]func(*collectedInventory){
		"valid":               func(*collectedInventory) {},
		"missing-file":        func(d *collectedInventory) { d.Files = d.Files[:1] },
		"duplicate-file":      func(d *collectedInventory) { d.Files[1] = d.Files[0] },
		"outside-file":        func(d *collectedInventory) { d.Files[0].File = "../outside.ts" },
		"unknown-project":     func(d *collectedInventory) { d.Cases[0].Project = "unknown" },
		"duplicate-case":      func(d *collectedInventory) { d.Cases[1] = d.Cases[0] },
		"missing-owner-cases": func(d *collectedInventory) { d.Cases = d.Cases[:2] },
		"empty-cases":         func(d *collectedInventory) { d.Cases = nil },
		"occurrence-gap":      func(d *collectedInventory) { d.Cases[1].Test = `["duplicate",2]` },
		"not-case-json":       func(d *collectedInventory) { d.Cases[0].Test = "arbitrary" },
		"negative-occurrence": func(d *collectedInventory) { d.Cases[0].Test = `["duplicate",-1]` },
		"empty-name":          func(d *collectedInventory) { d.Cases[0].Test = `["",0]` },
		"version-drift":       func(d *collectedInventory) { d.ViteVersion = "8.1.5" },
		"unsupported-schema":  func(d *collectedInventory) { d.Schema++ },
	} {
		t.Run(name, func(t *testing.T) {
			data, evidence := collectionFixture()
			mutate(&data)
			encoded, err := json.Marshal(data)
			if err != nil {
				t.Fatal(err)
			}
			inventory, err := decodeCollection(process.Output{Completed: true, ExitCode: 0, Stdout: encoded}, "js", evidence)
			if (err == nil) != (name == "valid") {
				t.Fatalf("collection err=%v", err)
			}
			if name == "valid" && len(inventory.Cases) != 3 {
				t.Fatal("duplicate names or project ownership collapsed")
			}
		})
	}
	data, evidence := collectionFixture()
	encoded, _ := json.Marshal(data)
	for _, out := range []process.Output{{Completed: false, Stdout: encoded}, {Completed: true, ExitCode: 1, Stdout: encoded}, {Completed: true, Stdout: append(encoded, []byte(" {}")...)}, {Completed: true, Stdout: []byte(`{"schema":1,"schema":1}`)}} {
		if _, err := decodeCollection(out, "js", evidence); err == nil {
			t.Fatal("failed/truncated/conflicting collection accepted")
		}
	}
}

// Legacy discovery/run compatibility has not independently accepted this new
// collection API. Those fixture jobs still exercise their original paths and
// visibly skip collection acceptance, rather than falsely passing its cases.
func requireCollectionFixture(t *testing.T, root string, workspace model.Workspace) {
	t.Helper()
	evidence, err := Discover(context.Background(), root, workspace, model.Context{Env: map[string]string{"CI": "true"}})
	if err != nil {
		t.Fatal(err)
	}
	pair := strings.TrimPrefix(evidence.Version, "vitest-vite-graph-v1:")
	vitest, vite, ok := strings.Cut(pair, ":vite:")
	if !ok {
		t.Fatal("missing installed collection fixture pair identity")
	}
	if !supportedCollectionVersions(vitest, vite) {
		t.Skip("independent collection not accepted for installed pair " + pair)
	}
}

func TestInstalledVitestIndependentCasesWithoutExecutingBodies(t *testing.T) {
	root, w := boundContextFixture(t, "export default { test: { globals: true, fileParallelism: false, pool: 'forks' } };\n")
	source := `import { value } from './value';
test('must not execute', () => { throw new Error('body was executed'); });
describe('table', () => { test.each([1, 2])('value %i', () => {}); });
test('duplicate', () => {}); test('duplicate', () => {});
test.skip('retained skip', () => {}); test.todo('future');
test('computed ' + value, () => {});
`
	if err := os.WriteFile(filepath.Join(root, "value.test.ts"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	requireCollectionFixture(t, root, w)
	collected, err := Collect(context.Background(), root, w, model.Context{Env: map[string]string{"CI": "true"}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{`["must not execute",0]`: true, `["table > value 1",0]`: true, `["table > value 2",0]`: true, `["duplicate",0]`: true, `["duplicate",1]`: true, `["retained skip",0]`: true, `["future",0]`: true, `["computed 7",0]`: true}
	if len(collected.Inventory.Cases) != len(want) || len(collected.SourceDigest) != 64 || len(collected.ContextDigest) != 64 {
		t.Fatalf("independent collection identities incomplete: %+v", collected.Inventory)
	}
	for _, item := range collected.Inventory.Cases {
		if item.Unit != "js::value.test.ts" || !want[item.ID] {
			t.Fatalf("unexpected case: %+v", item)
		}
		delete(want, item.ID)
	}
	if len(want) != 0 {
		t.Fatal("missing independent case")
	}
	foundRuntime, foundCollection := false, false
	for _, gap := range collected.Evidence.Gaps {
		foundRuntime = foundRuntime || gap.Code == "runtime-unqualified"
		foundCollection = foundCollection || gap.Code == "collection-effects-unqualified"
	}
	if !foundRuntime || !foundCollection {
		t.Fatal("collection accidentally removed qualification gaps")
	}
}

func TestInstalledVitestCollectionFailuresCannotProduceInventory(t *testing.T) {
	for _, stage := range []string{"import", "setup", "global-setup", "global-teardown", "source-drift"} {
		t.Run(stage, func(t *testing.T) {
			configuration := "export default { test: { globals: true, pool: 'forks'"
			if stage == "setup" {
				configuration += ", setupFiles: ['./broken.ts']"
			}
			if stage == "global-setup" || stage == "global-teardown" {
				configuration += ", globalSetup: ['./global.mjs']"
			}
			configuration += " } };\n"
			root, w := boundContextFixture(t, configuration)
			// Reach the stage with a healthy inventory before injecting its fault.
			// An unrelated binding/version failure must not satisfy this test.
			healthy := map[string]string{
				"value.test.ts": "test('one',()=>{throw new Error('body must not execute')});\n",
				"broken.ts":     "export const ready = true;\n",
				"global.mjs":    "export default function(){return()=>{}}\n",
			}
			if stage == "import" {
				healthy["value.test.ts"] = "import './broken';\n" + healthy["value.test.ts"]
			}
			if stage == "source-drift" {
				healthy["value.test.ts"] = "import fs from 'node:fs';\n" + healthy["value.test.ts"]
			}
			for name, source := range healthy {
				if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0600); err != nil {
					t.Fatal(err)
				}
			}
			requireCollectionFixture(t, root, w)
			execution := model.Context{Env: map[string]string{"CI": "true"}}
			baseline, err := Collect(context.Background(), root, w, execution)
			if err != nil {
				t.Fatalf("healthy %s collection unavailable before fault injection: %v", stage, err)
			}
			if len(baseline.Inventory.Units) != 1 || baseline.Inventory.Units[0].ID != "js::value.test.ts" ||
				len(baseline.Inventory.Cases) != 1 || baseline.Inventory.Cases[0] != (model.Case{Unit: "js::value.test.ts", ID: `["one",0]`}) {
				t.Fatalf("healthy %s collection differs from independent inventory: %+v", stage, baseline.Inventory)
			}
			files := map[string]string{}
			switch stage {
			case "import":
				files["broken.ts"] = "throw new Error('collection import fault');\n"
			case "setup":
				files["broken.ts"] = "throw new Error('collection setup fault');\n"
			case "global-setup":
				files["global.mjs"] = "export default function(){throw new Error('global setup fault')}\n"
			case "global-teardown":
				files["global.mjs"] = "export default function(){return()=>{throw new Error('global teardown fault')}}\n"
			case "source-drift":
				files["value.test.ts"] = "import fs from 'node:fs'; fs.writeFileSync(new URL('./value.ts', import.meta.url), 'export const value = 8'); test('one',()=>{});\n"
			}
			for name, source := range files {
				if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0600); err != nil {
					t.Fatal(err)
				}
			}
			result, err := Collect(context.Background(), root, w, execution)
			if err == nil {
				t.Fatal("failed or mutated collection accepted")
			}
			if len(result.Inventory.Units) != 0 || len(result.Inventory.Cases) != 0 {
				t.Fatal("failed collection returned a usable partial inventory")
			}
			if stage == "source-drift" {
				changed, err := os.ReadFile(filepath.Join(root, "value.ts"))
				if err != nil || string(changed) != "export const value = 8" {
					t.Fatal("collection rejected before the seeded source drift actually executed")
				}
			}
		})
	}
}
