package vitest

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/planner"
	vitestrunner "github.com/Cyberlane/hayaku/internal/runner/vitest"
)

func adversarialStartupFixture(t *testing.T, setup, body string) (string, model.Workspace) {
	t.Helper()
	configuration := "export default { resolve: { alias: { '@helper': new URL('./helper.ts', import.meta.url).pathname } }, test: { globals: true, pool: 'forks', setupFiles: ['./setup.ts'] } };\n"
	root, workspace := boundContextFixture(t, configuration)
	if err := os.Remove(filepath.Join(root, "value.test.ts")); err != nil {
		t.Fatal(err)
	}
	for name, source := range map[string]string{
		"helper.ts": "export const value = 7;\n",
		"a.test.ts": "import { value } from '@helper'; test('A',()=>expect(value).toBe(7));\n",
		"b.test.ts": body,
		"setup.ts":  setup,
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root, workspace
}

func adversarialUnitIDs(units []model.Unit) []string {
	result := make([]string, 0, len(units))
	for _, unit := range units {
		result = append(result, unit.ID)
	}
	return result
}

func TestInstalledVitestComputedStartupAndDormantImportsBroadenProposal(t *testing.T) {
	const setup = "globalThis.startupValue = 7;\n"
	const body = "test('B',()=>expect(globalThis.startupValue).toBe(7));\n"
	fixtures := []struct {
		name, setup, body string
	}{
		{"setup-computed", "const target='./helper'; globalThis.startupValue=(await import(target)).value;\n", body},
		{"setup-dormant", setup + "export async function dormant(){const target='./helper'; return import(target);}\n", body},
		{"setup-template", "const name='helper'; globalThis.startupValue=(await import(`./${name}`)).value;\n", body},
		{"body-computed", setup, "test('B',async()=>{const target='./helper';const value=await import(target);expect(value.value).toBe(7)});\n"},
		{"body-dormant", setup, "async function dormant(){const target='./helper'; return import(target);}\n" + body},
		{"body-foldable", setup, "test('B',async()=>{const value=await import('./'+'helper');expect(value.value).toBe(7)});\n"},
		{"body-eval", setup, "function dormant(){return eval(\"import('./helper')\");}\n" + body},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			root, workspace := adversarialStartupFixture(t, fixture.setup, fixture.body)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			evidence, err := Discover(ctx, root, workspace, model.Context{Env: map[string]string{"CI": "true"}})
			if err != nil {
				t.Fatal("actual configured discovery unavailable before proposal assertions", err)
			}
			want := []string{"js::a.test.ts", "js::b.test.ts"}
			if !reflect.DeepEqual(adversarialUnitIDs(evidence.Units), want) {
				t.Fatal("computed-import fixture lost its independent two-file inventory")
			}
			unknown := false
			for _, effect := range evidence.Effects {
				unknown = unknown || effect.Kind == "unknown"
			}
			if !unknown {
				t.Fatal("computed/dormant import influence has no explicit unknown effect")
			}
			for _, changes := range [][]model.Change{nil, {{Path: "helper.ts", Status: "M"}}} {
				plan, err := planner.Build([]model.Evidence{evidence}, []model.Evidence{evidence}, changes, model.Config{Schema: model.Schema, Workspaces: []model.Workspace{workspace}})
				// Selected remains full because runtime influence is unqualified.
				// Proposed must also broaden: full execution cannot rescue a
				// descriptive graph that omitted B's startup/dormant influence.
				if err != nil || !reflect.DeepEqual(adversarialUnitIDs(plan.Proposed), want) || !reflect.DeepEqual(adversarialUnitIDs(plan.Selected), want) || plan.Mode != "full-fallback" {
					t.Fatal("unknown import influence narrowed the descriptive proposal", err)
				}
			}
		})
	}
}

func TestInstalledVitestLiteralAliasDoesNotForceUnknownProposal(t *testing.T) {
	root, workspace := adversarialStartupFixture(t, "globalThis.startupValue=7;\n", "test('B',()=>expect(globalThis.startupValue).toBe(7));\n")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	evidence, err := Discover(ctx, root, workspace, model.Context{Env: map[string]string{"CI": "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(evidence.Inputs["helper.ts"], []string{"js::a.test.ts"}) || len(evidence.Effects) != 0 {
		t.Fatal("ordinary literal alias lost precise independent ownership")
	}
	plan, err := planner.Build([]model.Evidence{evidence}, []model.Evidence{evidence}, []model.Change{{Path: "helper.ts", Status: "M"}}, model.Config{Schema: model.Schema, Workspaces: []model.Workspace{workspace}})
	if err != nil || !reflect.DeepEqual(adversarialUnitIDs(plan.Proposed), []string{"js::a.test.ts"}) || !reflect.DeepEqual(adversarialUnitIDs(plan.Selected), []string{"js::a.test.ts", "js::b.test.ts"}) || plan.Mode != "full-fallback" {
		t.Fatal("literal alias negative control did not distinguish proposal from required scope", err)
	}
}

func assertGeneratedImportFullOutcomes(t *testing.T, ctx context.Context, root string, workspace model.Workspace) []model.Unit {
	t.Helper()
	execution := model.Context{Env: map[string]string{"CI": "true"}}
	units := []model.Unit{
		{ID: "js::a.test.ts", Workspace: "js", Kind: "vitest-file", Selector: "a.test.ts"},
		{ID: "js::b.test.ts", Workspace: "js", Kind: "vitest-file", Selector: "b.test.ts"},
	}
	// Real original full-command outcomes establish that this generated alias
	// influences B; a descriptive gap alone cannot establish that relationship.
	for _, wantPass := range []bool{true, false} {
		if !wantPass {
			if err := os.WriteFile(filepath.Join(root, "helper.ts"), []byte("export const value=8;\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		output, runErr := Execute(ctx, root, workspace, execution, nil)
		outcomes, decodeErr := vitestrunner.Reconcile(bytes.NewReader(output.Stdout), units)
		wantExit, action := 0, "pass"
		if !wantPass {
			wantExit, action = 1, "fail"
		}
		want := []vitestrunner.Outcome{{Unit: units[0].ID, Test: `["A",0]`, Action: action}, {Unit: units[1].ID, Test: `["B",0]`, Action: action}}
		if (runErr == nil) != wantPass || !output.Completed || output.ExitCode != wantExit || decodeErr != nil || outcomes.Failed == wantPass || outcomes.UnhandledErrors != 0 || len(outcomes.Missing) != 0 || !reflect.DeepEqual(outcomes.Tests, want) {
			t.Fatal("generated alias did not produce independent healthy/failing full outcomes", runErr, decodeErr)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "helper.ts"), []byte("export const value=7;\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return units
}

func generatedImportFixture(t *testing.T, expression string) (string, model.Workspace) {
	t.Helper()
	root, workspace := adversarialStartupFixture(t, "globalThis.startupValue=7;\n", "test('B',()=>expect(globalThis.startupValue).toBe(7));\n")
	generated := expression + " test('B',()=>expect(helper.value).toBe(7));"
	configuration := "export default { resolve: { alias: { '@helper': new URL('./helper.ts', import.meta.url).pathname } }, test: { globals: true, pool: 'forks', fileParallelism: false, setupFiles: ['./setup.ts'] }, plugins: [{ name: 'fixture-loader', transform(code,id){if(id.endsWith('/b.test.ts'))return " + strconv.Quote(generated) + ";} }] };\n"
	if err := os.WriteFile(filepath.Join(root, "vitest.config.mjs"), []byte(configuration), 0600); err != nil {
		t.Fatal(err)
	}
	return root, workspace
}

func TestInstalledVitestPluginHelperAliasBroadensActualInfluence(t *testing.T) {
	root, workspace := generatedImportFixture(t, "const loader=__vite_ssr_dynamic_import__; const helper=await loader('./helper.ts');")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	units := assertGeneratedImportFullOutcomes(t, ctx, root, workspace)
	execution := model.Context{Env: map[string]string{"CI": "true"}}
	evidence, err := Discover(ctx, root, workspace, execution)
	if err != nil || !reflect.DeepEqual(evidence.Units, units) {
		t.Fatal("generated alias native graph lost independently expected inventory", err)
	}
	unknown := false
	for _, effect := range evidence.Effects {
		unknown = unknown || effect.Kind == "unknown"
	}
	if !unknown {
		t.Error("escaping generated import helper retained a complete descriptive graph")
	}
	for _, changes := range [][]model.Change{nil, {{Path: "helper.ts", Status: "M"}}} {
		plan, err := planner.Build([]model.Evidence{evidence}, []model.Evidence{evidence}, changes, model.Config{Schema: model.Schema, Workspaces: []model.Workspace{workspace}})
		if err != nil || !reflect.DeepEqual(plan.Proposed, units) || !reflect.DeepEqual(plan.Selected, units) || plan.Mode != "full-fallback" {
			t.Fatal("real generated-helper influence omitted a descriptive proposal", err)
		}
	}
}

func TestInstalledVitestPluginLiteralHelperInfluenceIsCaptured(t *testing.T) {
	for name, expression := range map[string]string{
		"dynamic": "const helper=await __vite_ssr_dynamic_import__('./helper.ts');",
		"static":  "const helper=await __vite_ssr_import__('./helper.ts', { importedNames: ['value'] });",
	} {
		t.Run(name, func(t *testing.T) {
			root, workspace := generatedImportFixture(t, expression)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			units := assertGeneratedImportFullOutcomes(t, ctx, root, workspace)
			evidence, err := Discover(ctx, root, workspace, model.Context{Env: map[string]string{"CI": "true"}})
			if err != nil || !reflect.DeepEqual(evidence.Units, units) {
				t.Fatal("literal helper graph lost independent native inventory", err)
			}
			plan, err := planner.Build([]model.Evidence{evidence}, []model.Evidence{evidence}, []model.Change{{Path: "helper.ts", Status: "M"}}, model.Config{Schema: model.Schema, Workspaces: []model.Workspace{workspace}})
			// Either capture the literal edge or keep an explicit unknown. Both
			// must retain the two actual helper consumers in the proposal.
			if err != nil || !reflect.DeepEqual(plan.Proposed, units) || !reflect.DeepEqual(plan.Selected, units) || plan.Mode != "full-fallback" {
				t.Fatal("actual literal helper consumer omitted from descriptive proposal", err)
			}
		})
	}
}
