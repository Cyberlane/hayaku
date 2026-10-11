package app

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/native"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/testutil/acceptance"
)

const tsReleaseAlpha = "ts::alpha.test.ts"
const tsReleaseBeta = "ts::beta.test.ts"
const tsReleaseGamma = "ts::gamma.test.ts"

type tsReleaseInputs struct {
	image, node, hostNode, modules, compiler, addon, libraries string
}

func TestInstalledTypeScriptReleaseCLI(t *testing.T) {
	inputs := tsReleasePrerequisites(t)
	cli := buildGoReleaseCLI(t)
	t.Run("host-"+runtime.GOOS+"-backend-linux", func(t *testing.T) {
		for _, family := range []struct {
			name  string
			mixed bool
			run   func(*testing.T, *tsReleaseFixture)
		}{
			{"lifecycle", false, tsReleaseLifecycle},
			{"failures-and-fallback", false, tsReleaseFailures},
			{"ambiguous-source-collision", false, tsReleaseCollision},
			{"cache-and-audit", false, tsReleaseCacheAudit},
			{"mixed-failure-continuation", true, tsReleaseMixedFailures},
			{"mixed-late-original-mutation", true, tsReleaseMixedMutation},
		} {
			t.Run(family.name, func(t *testing.T) { family.run(t, newTSReleaseFixture(t, cli, inputs, family.mixed)) })
		}
	})
}

func tsReleasePrerequisites(t *testing.T) tsReleaseInputs {
	t.Helper()
	inputs := tsReleaseInputs{}
	for _, entry := range []struct {
		name string
		out  *string
	}{
		{"HAYAKU_NATIVE_IMAGE", &inputs.image}, {"HAYAKU_DYNAMIC_NODE", &inputs.node},
		{"HAYAKU_NODE_CERTIFIER_NODE", &inputs.hostNode}, {"HAYAKU_VITEST_MODULES", &inputs.modules},
		{"HAYAKU_NODE_CERTIFIER_TYPESCRIPT", &inputs.compiler}, {"HAYAKU_NATIVE_ADDON_ROLLDOWN", &inputs.addon},
		{"HAYAKU_VITEST_LIBRARY_DIR", &inputs.libraries},
	} {
		*entry.out = os.Getenv(entry.name)
		if *entry.out == "" {
			if os.Getenv("HAYAKU_TYPESCRIPT_RELEASE_REQUIRED") == "1" {
				t.Fatal("required installed TypeScript release prerequisite missing:", entry.name)
			}
			t.Skip("explicit installed TypeScript release prerequisites required: " + entry.name)
		}
	}
	if (runtime.GOOS != "darwin" && runtime.GOOS != "linux") || runtime.GOARCH != "arm64" {
		t.Fatal("this fixed TypeScript release acceptance requires an actual Darwin or Linux ARM64 host")
	}
	return inputs
}

type tsReleaseFixture struct {
	*goReleaseFixture
	tsRoot           string
	config           native.RunConfig
	previous         map[string]string
	changed          []string
	exactKeys        map[string]string
	revoked          []string
	mutationExpected bool
}

func tsReleaseCases() map[string][]string {
	return map[string][]string{tsReleaseAlpha: {`["alpha",0]`}, tsReleaseBeta: {`["beta",0]`}, tsReleaseGamma: {`["gamma",0]`}}
}

func tsReleaseWrite(t *testing.T, root, name, source string) {
	t.Helper()
	nativeAppWrite(t, root, name, source)
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
}

func newTSReleaseFixture(t *testing.T, cli string, inputs tsReleaseInputs, mixed bool) *tsReleaseFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &tsReleaseFixture{goReleaseFixture: &goReleaseFixture{t: t, root: root, cache: filepath.Join(t.TempDir(), "passes"), cli: cli,
		cases: tsReleaseCases(), current: map[string]string{}, history: map[string]map[string]bool{}}, tsRoot: root}
	if mixed {
		f.tsRoot = filepath.Join(root, "ts")
	}
	for name, source := range map[string]string{
		"shared.ts":     "export function Shared(): number{return 1;}",
		"a.ts":          "import {Shared} from './shared.js';export function Value(): number{return Shared();}",
		"b.ts":          "import {Shared} from './shared.js';export function Value(): number{return Shared();}",
		"alpha.test.ts": `import {test,expect} from 'vitest';import {Value} from './a.js';test('alpha',()=>{expect(Value()).toBeGreaterThan(0)});`,
		"beta.test.ts":  `import {test,expect} from 'vitest';import {Value} from './b.js';test('beta',()=>{expect(Value()).toBeGreaterThan(0)});`,
		"gamma.test.ts": `import {test,expect} from 'vitest';test('gamma',()=>{const value: number=true?1:0;expect(value).toBe(1)});`,
	} {
		tsReleaseWrite(t, f.tsRoot, name, source)
	}
	if err := os.Chmod(f.tsRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(f.tsRoot, "node_modules"), 0755); err != nil {
		t.Fatal(err)
	}
	// Copy the independently exported immutable role bytes into fixture-owned
	// paths. The reference stays untouched; only the fixture loader gains exec.
	libraries := t.TempDir()
	for _, name := range []string{"ld-linux-aarch64.so.1", "libdl.so.2", "libstdc++.so.6", "libm.so.6", "libgcc_s.so.1", "libpthread.so.0", "libc.so.6"} {
		path := filepath.Join(inputs.libraries, name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<20 {
			t.Fatal("required independently exported runtime role unavailable")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0644)
		if name == "ld-linux-aarch64.so.1" {
			mode = 0755
		}
		if err := os.WriteFile(filepath.Join(libraries, name), data, mode); err != nil {
			t.Fatal(err)
		}
	}
	f.config = tsReleaseEnroll(t, cli, f.tsRoot, "vitest", "ts", []string{
		"--image", inputs.image, "--node", inputs.hostNode, "--runtime-node", inputs.node, "--modules", inputs.modules,
		"--typescript-sdk", inputs.compiler, "--loader", filepath.Join(libraries, "ld-linux-aarch64.so.1"),
		"--library-dir", libraries, "--addon", inputs.addon,
	})
	for name, want := range map[string]string{native.VitestProfileFile: native.VitestProfile, "package.json": native.VitestPackage} {
		if data, err := os.ReadFile(filepath.Join(f.tsRoot, name)); err != nil || string(data) != want {
			t.Fatal("packaged enrollment did not preserve the canonical original context")
		}
	}
	docker, err := native.ResolveDocker()
	if err != nil || len(f.config.Workspaces) != 1 || !reflect.DeepEqual(f.config.Workspaces[0].Command, native.VitestOriginalCommand(docker, f.tsRoot, inputs.modules, inputs.node, inputs.addon, inputs.image)) {
		t.Fatal("packaged enrollment did not configure the complete original Vitest command")
	}
	if mixed {
		f.config.Workspaces[0].Root = "ts"
		goRoot := filepath.Join(root, "go")
		nativeAppWrite(t, goRoot, "go.mod", "module mixedrelease\ngo 1.26\n")
		nativeAppWrite(t, goRoot, "a/value.go", "package sample;func Value()int{return 1}")
		nativeAppWrite(t, goRoot, "a/value_test.go", `package sample;import "testing";func TestValue(t *testing.T){if Value()!=1{t.Fatal("wrong")}}`)
		goConfig := tsReleaseEnroll(t, cli, goRoot, "go", "go", []string{"--platform", runtime.GOOS, "--architecture", runtime.GOARCH, "--package", "./a"})
		goConfig.Workspaces[0].Root = "go"
		f.config.Workspaces = append(f.config.Workspaces, goConfig.Workspaces[0])
		if runtime.GOOS == "linux" {
			f.cases["go::./a"] = []string{"TestValue"}
		} else {
			f.cases["go::suite"] = nil
		}
	}
	f.writeConfig()
	return f
}

func tsReleaseEnroll(t *testing.T, cli, root, language, id string, flags []string) native.RunConfig {
	t.Helper()
	args := append([]string{"native-init", "--root", root, "--config", "enrollment.json", "--language", language, "--workspace", id}, flags...)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := process.RunEnvironmentLimits(ctx, model.Command{Executable: cli, Args: args}, root, os.Environ(), 128<<10, 128<<10)
	if err != nil || !out.Completed || out.ExitCode != 0 {
		t.Fatal("actual packaged enrollment did not complete successfully", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "enrollment.json"))
	var config native.RunConfig
	if err != nil || goReleaseDecode(data, &config) != nil || os.Remove(filepath.Join(root, "enrollment.json")) != nil {
		t.Fatal("packaged enrollment configuration unavailable")
	}
	return config
}

func (f *tsReleaseFixture) writeConfig() {
	f.t.Helper()
	data, err := json.Marshal(f.config)
	if err != nil {
		f.t.Fatal(err)
	}
	nativeAppWrite(f.t, f.root, "native.json", string(data))
}

func tsReleaseWant(cases map[string][]string, mode string, overrides map[string]string, originals ...string) acceptance.Expectation {
	want := acceptance.Expectation{Completed: true, Passed: true, Outcomes: model.Outcomes{Complete: true}, OriginalScopes: slices.Clone(originals)}
	for _, name := range slices.Sorted(maps.Keys(cases)) {
		actualMode := mode
		if override := overrides[name]; override != "" {
			actualMode = override
		}
		workspace := "ts"
		if len(name) >= 4 && name[:4] == "go::" {
			workspace = "go"
		}
		want.Inventory = append(want.Inventory, model.Unit{ID: name, Workspace: workspace})
		want.Outcomes.Units = append(want.Outcomes.Units, model.Outcome{Unit: name, Action: "pass"})
		for _, test := range cases[name] {
			want.Outcomes.Tests = append(want.Outcomes.Tests, model.Outcome{Unit: name, Test: test, Action: "pass"})
		}
		qualified := actualMode != "fallback" && actualMode != "incomplete"
		want.Groups = append(want.Groups, acceptance.Group{Name: name, Mode: actualMode, Qualified: qualified})
		if qualified {
			want.Authorized = append(want.Authorized, name)
		}
	}
	return want
}

func tsReleaseFailed(want acceptance.Expectation, unit string) acceptance.Expectation {
	want.Passed, want.Error, want.ExitCode, want.Outcomes.Failed = false, true, 1, true
	for i := range want.Outcomes.Units {
		if want.Outcomes.Units[i].Unit == unit {
			want.Outcomes.Units[i].Action = "fail"
		}
	}
	for i := range want.Outcomes.Tests {
		if want.Outcomes.Tests[i].Unit == unit {
			want.Outcomes.Tests[i].Action = "fail"
		}
	}
	want.Authorized = slices.DeleteFunc(want.Authorized, func(name string) bool { return name == unit })
	return want
}

func (f *tsReleaseFixture) expectKeyChanges(changed ...string) {
	f.previous, f.changed = maps.Clone(f.current), slices.Clone(changed)
}

func (f *tsReleaseFixture) observe(ctx context.Context) (acceptance.Observation, error) {
	observation, err := f.goReleaseFixture.observe(ctx)
	for _, group := range observation.Groups {
		if group.Mode == "fallback" && f.current[group.Name] != "" {
			f.t.Fatal("complete original fallback was assigned a reusable group key")
		}
	}
	for name, old := range f.previous {
		if current := f.current[name]; old == "" || current == "" || (current != old) != slices.Contains(f.changed, name) {
			f.t.Fatal("actual group key violates the independently authored influence relation")
		}
	}
	for name, key := range f.exactKeys {
		if key == "" || f.current[name] != key {
			f.t.Fatal("restored case set did not recover its authenticated historical key")
		}
	}
	f.previous, f.changed, f.exactKeys = nil, nil, nil
	if len(f.revoked) != 0 {
		cache, inspectErr := goReleaseExistingCache(f.cache)
		if inspectErr != nil || cache == nil {
			f.t.Fatal("independent historical revocation inspection unavailable")
		}
		for _, name := range f.revoked {
			if len(f.history[name]) == 0 {
				f.t.Fatal("revocation control has no authenticated history")
			}
			for key := range f.history[name] {
				if _, state := cache.Load(key); state != "miss" {
					f.t.Fatal("revoked historical own-domain key retained authority or corrupt bytes")
				}
			}
			observation.Revoked = append(observation.Revoked, name)
		}
		if inspectErr := cache.Close(); inspectErr != nil {
			f.t.Fatal(inspectErr)
		}
	}
	if f.mutationExpected {
		if data, readErr := os.ReadFile(filepath.Join(f.tsRoot, "shared.ts")); readErr != nil || string(data) != "export function Shared(): number{return 2;}" {
			f.t.Fatal("real late original Go mutator did not change the earlier TypeScript source")
		}
	}
	return observation, err
}

func tsReleaseRun(t *testing.T, name string, steps []acceptance.Step) {
	t.Helper()
	for i := range steps {
		invoke, step := steps[i].Invoke, steps[i].Name
		steps[i].Invoke = func(ctx context.Context) (acceptance.Observation, error) {
			ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
			defer cancel()
			started := time.Now()
			observation, err := invoke(ctx)
			t.Logf("packaged CLI step=%s elapsed_ms=%d", step, time.Since(started).Milliseconds())
			return observation, err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if result := acceptance.Run(ctx, acceptance.Scenario{Name: name, Steps: steps}); result.State != "passed" {
		t.Fatalf("packaged CLI acceptance failed: %+v", result)
	}
}

func tsReleaseLifecycle(t *testing.T, f *tsReleaseFixture) {
	scenario := tsReleaseLifecycleScenario(t, f)
	tsReleaseRun(t, scenario.Name, scenario.Steps)
}

func tsReleaseLifecycleScenario(t *testing.T, f *tsReleaseFixture) acceptance.Scenario {
	base := tsReleaseCases()
	added, renamed, entry := maps.Clone(base), maps.Clone(base), maps.Clone(base)
	added[tsReleaseAlpha] = []string{`["alpha",0]`, `["alpha-extra",0]`}
	renamed[tsReleaseAlpha] = []string{`["alpha",0]`, `["alpha-renamed",0]`}
	entry["ts::nested/delta.test.js"] = []string{`["delta",0]`}
	alpha := map[string]string{tsReleaseAlpha: "execute"}
	var restoredAlpha string
	changedTest := `import {test,expect} from 'vitest';import {Value} from './a.js';test('alpha',()=>{expect(Value()).toBeGreaterThan(1)});`
	return acceptance.Scenario{Name: "typescript-lifecycle", Steps: []acceptance.Step{
		{Name: "cold", Invoke: f.observe, Want: tsReleaseWant(base, "execute", nil)},
		{Name: "warm", Prepare: func(context.Context) error { f.expectKeyChanges(); return nil }, Invoke: f.observe, Want: tsReleaseWant(base, "reuse", nil)},
		{Name: "own-source-edit", Prepare: func(context.Context) error {
			f.expectKeyChanges(tsReleaseAlpha)
			tsReleaseWrite(t, f.tsRoot, "a.ts", "import {Shared} from './shared.js';export function Value(): number{return Shared()+1;}")
			return nil
		}, Invoke: f.observe, Want: tsReleaseWant(base, "reuse", alpha)},
		{Name: "own-test-edit", Prepare: func(context.Context) error {
			f.expectKeyChanges(tsReleaseAlpha)
			tsReleaseWrite(t, f.tsRoot, "alpha.test.ts", changedTest)
			return nil
		}, Invoke: f.observe, Want: tsReleaseWant(base, "reuse", alpha)},
		{Name: "reachable-shared-helper-edit", Prepare: func(context.Context) error {
			f.expectKeyChanges(tsReleaseAlpha, tsReleaseBeta)
			tsReleaseWrite(t, f.tsRoot, "shared.ts", "export function Shared(): number{return 2;}")
			return nil
		}, Invoke: f.observe, Want: tsReleaseWant(base, "reuse", map[string]string{tsReleaseAlpha: "execute", tsReleaseBeta: "execute"})},
		{Name: "new-case", Prepare: func(context.Context) error {
			restoredAlpha = f.current[tsReleaseAlpha]
			f.expectKeyChanges(tsReleaseAlpha)
			f.cases = maps.Clone(added)
			tsReleaseWrite(t, f.tsRoot, "alpha.test.ts", changedTest+`test('alpha-extra',()=>{expect(Value()).toBeGreaterThan(1)});`)
			return nil
		}, Invoke: f.observe, Want: tsReleaseWant(added, "reuse", alpha)},
		{Name: "renamed-case", Prepare: func(context.Context) error {
			f.expectKeyChanges(tsReleaseAlpha)
			f.cases = maps.Clone(renamed)
			tsReleaseWrite(t, f.tsRoot, "alpha.test.ts", changedTest+`test('alpha-renamed',()=>{expect(Value()).toBeGreaterThan(1)});`)
			return nil
		}, Invoke: f.observe, Want: tsReleaseWant(renamed, "reuse", alpha)},
		{Name: "deleted-case-restores-history", Prepare: func(context.Context) error {
			f.expectKeyChanges(tsReleaseAlpha)
			f.exactKeys, f.cases = map[string]string{tsReleaseAlpha: restoredAlpha}, maps.Clone(base)
			tsReleaseWrite(t, f.tsRoot, "alpha.test.ts", changedTest)
			return nil
		}, Invoke: f.observe, Want: tsReleaseWant(base, "reuse", nil)},
		{Name: "new-nested-js-entry-in-full-glob", Prepare: func(context.Context) error {
			f.expectKeyChanges()
			f.cases = maps.Clone(entry)
			tsReleaseWrite(t, f.tsRoot, "nested/delta.test.js", `import {test,expect} from 'vitest';test('delta',()=>{expect(1).toBe(1)});`)
			return nil
		}, Invoke: f.observe, Want: tsReleaseWant(entry, "reuse", map[string]string{"ts::nested/delta.test.js": "execute"})},
		{Name: "deleted-entry-is-not-reported-or-reused", Prepare: func(context.Context) error {
			f.expectKeyChanges()
			delete(f.previous, "ts::nested/delta.test.js")
			f.cases = maps.Clone(base)
			return os.Remove(filepath.Join(f.tsRoot, "nested/delta.test.js"))
		}, Invoke: f.observe, Want: tsReleaseWant(base, "reuse", nil)},
	}}
}

func tsReleaseFailures(t *testing.T, f *tsReleaseFixture) {
	scenario := tsReleaseFailuresScenario(t, f)
	tsReleaseRun(t, scenario.Name, scenario.Steps)
}

func tsReleaseFailuresScenario(t *testing.T, f *tsReleaseFixture) acceptance.Scenario {
	base := tsReleaseCases()
	failed := tsReleaseFailed(tsReleaseWant(base, "reuse", map[string]string{tsReleaseAlpha: "execute"}), tsReleaseAlpha)
	full := tsReleaseWant(map[string][]string{"ts::suite": nil}, "fallback", nil, "ts::suite")
	return acceptance.Scenario{Name: "typescript-failure-and-original-fallback", Steps: []acceptance.Step{
		{Name: "healthy-control", Invoke: f.observe, Want: tsReleaseWant(base, "execute", nil)},
		{Name: "seeded-assertion", Prepare: func(context.Context) error {
			tsReleaseWrite(t, f.tsRoot, "a.ts", "export function Value(): number{return 0;}")
			return nil
		}, Invoke: f.observe, Want: failed},
		{Name: "failed-case-has-no-valid-receipt-and-executes-again", Invoke: f.observe, Want: failed},
		{Name: "restore-authenticated-healthy-history", Prepare: func(context.Context) error {
			tsReleaseWrite(t, f.tsRoot, "a.ts", "import {Shared} from './shared.js';export function Value(): number{return Shared();}")
			return nil
		}, Invoke: f.observe, Want: tsReleaseWant(base, "reuse", nil)},
		{Name: "unimported-dormant-effect-broadens-to-complete-original", Prepare: func(context.Context) error {
			tsReleaseWrite(t, f.tsRoot, "dormant.ts", `export function Dormant(): string{return false?process.env.RELEASE_FIXTURE:"";}`)
			return nil
		}, Invoke: f.observe, Want: full},
		{Name: "original-fallback-never-gains-a-key-or-reuse", Invoke: f.observe, Want: full},
	}}
}

func tsReleaseCollision(t *testing.T, f *tsReleaseFixture) {
	scenario := tsReleaseCollisionScenario(t, f)
	tsReleaseRun(t, scenario.Name, scenario.Steps)
}

func tsReleaseCollisionScenario(t *testing.T, f *tsReleaseFixture) acceptance.Scenario {
	full := tsReleaseWant(map[string][]string{"ts::suite": nil}, "fallback", nil, "ts::suite")
	return acceptance.Scenario{Name: "typescript-ambiguous-source-original-fallback", Steps: []acceptance.Step{
		{Name: "healthy-complete-inventory-control", Invoke: f.observe, Want: tsReleaseWant(tsReleaseCases(), "execute", nil)},
		{Name: "same-stem-js-and-ts-run-complete-original", Prepare: func(context.Context) error {
			for _, name := range []string{"helper.js", "helper.ts"} {
				tsReleaseWrite(t, f.tsRoot, name, "export const value=1;")
			}
			tsReleaseWrite(t, f.tsRoot, "alpha.test.ts", `import {test,expect} from 'vitest';import {value} from './helper.js';test('alpha',()=>{expect(value).toBe(1)});`)
			return nil
		}, Invoke: f.observe, Want: full},
		{Name: "repeated-collision-fallback-has-no-private-key-or-authority", Invoke: f.observe, Want: full},
	}}
}

func (f *tsReleaseFixture) corruptExactCase(ctx context.Context) error {
	key := f.current[tsReleaseAlpha]
	cache, err := goReleaseExistingCache(f.cache)
	if err != nil || cache == nil || key == "" {
		return errors.Join(err, errors.New("corruption requires an existing authenticated healthy case control"))
	}
	foreignCase := `["foreign",0]`
	value := struct {
		Schema   int            `json:"schema"`
		Key      string         `json:"key"`
		Cases    []string       `json:"cases"`
		Outcomes model.Outcomes `json:"outcomes"`
	}{1, key, []string{foreignCase}, model.Outcomes{Complete: true, Units: []model.Outcome{{Unit: tsReleaseAlpha, Action: "pass"}}, Tests: []model.Outcome{{Unit: tsReleaseAlpha, Test: foreignCase, Action: "pass"}}}}
	err = cache.Store(ctx, key, value)
	data, state := cache.Load(key)
	if state != "hit" || goReleaseReceipt(data, key, tsReleaseAlpha, f.cases[tsReleaseAlpha]) == nil {
		err = errors.Join(err, errors.New("signed foreign-case corruption control did not authenticate and contradict the oracle"))
	}
	return errors.Join(err, cache.Close())
}

func tsReleaseCacheAudit(t *testing.T, f *tsReleaseFixture) {
	scenario := tsReleaseCacheAuditScenario(t, f)
	tsReleaseRun(t, scenario.Name, scenario.Steps)
}

func tsReleaseCacheAuditScenario(t *testing.T, f *tsReleaseFixture) acceptance.Scenario {
	base := tsReleaseCases()
	audit := tsReleaseWant(map[string][]string{"ts::suite": nil}, "fallback", nil, "ts::suite")
	audit.Revoked = slices.Sorted(maps.Keys(base))
	return acceptance.Scenario{Name: "typescript-cache-and-original-audit", Steps: []acceptance.Step{
		{Name: "healthy-control", Invoke: f.observe, Want: tsReleaseWant(base, "execute", nil)},
		{Name: "warm-and-preserved-foreign-domain", Prepare: f.seedForeign, Invoke: f.observe, Want: tsReleaseWant(base, "reuse", nil)},
		{Name: "second-authenticated-history-version", Prepare: func(context.Context) error {
			tsReleaseWrite(t, f.tsRoot, "a.ts", "import {Shared} from './shared.js';export function Value(): number{return Shared()+1;}")
			return nil
		}, Invoke: f.observe, Want: tsReleaseWant(base, "reuse", map[string]string{tsReleaseAlpha: "execute"})},
		{Name: "signed-wrong-exact-case-must-execute-and-repair", Prepare: f.corruptExactCase, Invoke: f.observe, Want: tsReleaseWant(base, "reuse", map[string]string{tsReleaseAlpha: "execute"})},
		{Name: "forced-audit-runs-complete-original-and-revokes-history", Prepare: func(context.Context) error {
			f.audit, f.revoked = true, slices.Sorted(maps.Keys(base))
			return nil
		}, Invoke: f.observe, Want: audit},
	}}
}

func tsReleaseMixedModes(goMode string) map[string]string {
	if runtime.GOOS == "linux" {
		return map[string]string{"go::./a": goMode}
	}
	return map[string]string{"go::suite": "fallback"}
}

func tsReleaseMixedWant(cases map[string][]string, mode string, overrides map[string]string) acceptance.Expectation {
	if runtime.GOOS == "linux" {
		return tsReleaseWant(cases, mode, overrides)
	}
	return tsReleaseWant(cases, mode, overrides, "go::suite")
}

func tsReleaseMixedFailures(t *testing.T, f *tsReleaseFixture) {
	scenario := tsReleaseMixedFailuresScenario(t, f)
	tsReleaseRun(t, scenario.Name, scenario.Steps)
}

func tsReleaseMixedFailuresScenario(t *testing.T, f *tsReleaseFixture) acceptance.Scenario {
	base := maps.Clone(f.cases)
	goUnit := "go::./a"
	if runtime.GOOS != "linux" {
		goUnit = "go::suite"
	}
	tsFailed := tsReleaseMixedModes("reuse")
	tsFailed[tsReleaseAlpha] = "execute"
	goFailed := tsReleaseMixedModes("execute")
	goFailed[tsReleaseBeta] = "execute"
	return acceptance.Scenario{Name: "mixed-workspace-failure-continuation", Steps: []acceptance.Step{
		{Name: "cold", Invoke: f.observe, Want: tsReleaseMixedWant(base, "execute", tsReleaseMixedModes("execute"))},
		{Name: "warm", Invoke: f.observe, Want: tsReleaseMixedWant(base, "reuse", tsReleaseMixedModes("reuse"))},
		{Name: "typescript-failure-still-completes-later-go-scope", Prepare: func(context.Context) error {
			tsReleaseWrite(t, f.tsRoot, "a.ts", "export function Value(): number{return 0;}")
			return nil
		}, Invoke: f.observe, Want: tsReleaseFailed(tsReleaseMixedWant(base, "reuse", tsFailed), tsReleaseAlpha)},
		{Name: "go-failure-first-still-executes-changed-typescript-scope", Prepare: func(context.Context) error {
			tsReleaseWrite(t, f.tsRoot, "a.ts", "import {Shared} from './shared.js';export function Value(): number{return Shared();}")
			tsReleaseWrite(t, f.tsRoot, "b.ts", "import {Shared} from './shared.js';export function Value(): number{return Shared()+1;}")
			nativeAppWrite(t, filepath.Join(f.root, "go"), "a/value.go", "package sample;func Value()int{return 2}")
			slices.Reverse(f.config.Workspaces)
			f.writeConfig()
			return nil
		}, Invoke: f.observe, Want: tsReleaseFailed(tsReleaseMixedWant(base, "reuse", goFailed), goUnit)},
		{Name: "failed-go-cannot-gain-a-pass-receipt", Invoke: f.observe, Want: tsReleaseFailed(tsReleaseMixedWant(base, "reuse", tsReleaseMixedModes("execute")), goUnit)},
	}}
}

func tsReleaseMixedMutation(t *testing.T, f *tsReleaseFixture) {
	scenario := tsReleaseMixedMutationScenario(t, f)
	tsReleaseRun(t, scenario.Name, scenario.Steps)
}

func tsReleaseMixedMutationScenario(t *testing.T, f *tsReleaseFixture) acceptance.Scenario {
	base := maps.Clone(f.cases)
	lateCases := tsReleaseCases()
	lateCases["go::suite"] = nil
	late := tsReleaseWant(lateCases, "incomplete", nil)
	late.Completed, late.Passed, late.Error, late.ExitCode, late.Outcomes.Complete = false, false, true, 1, false
	late.Revoked = []string{tsReleaseAlpha, tsReleaseBeta, tsReleaseGamma}
	return acceptance.Scenario{Name: "mixed-late-original-source-mutation", Steps: []acceptance.Step{
		{Name: "healthy-composed-control", Invoke: f.observe, Want: tsReleaseMixedWant(base, "execute", tsReleaseMixedModes("execute"))},
		{Name: "warm-composed-and-foreign-control", Prepare: f.seedForeign, Invoke: f.observe, Want: tsReleaseMixedWant(base, "reuse", tsReleaseMixedModes("reuse"))},
		{Name: "late-original-go-write-revokes-earlier-typeScript-keys", Prepare: func(context.Context) error {
			cache, err := goReleaseExistingCache(f.cache)
			if err != nil || cache == nil {
				return errors.Join(err, errors.New("late mutation requires authenticated earlier TypeScript keys"))
			}
			for _, name := range []string{tsReleaseAlpha, tsReleaseBeta, tsReleaseGamma} {
				err = errors.Join(err, cache.Invalidate(f.current[name]))
			}
			err = errors.Join(err, cache.Close())
			if err != nil {
				return err
			}
			f.revoked, f.mutationExpected = slices.Clone(late.Revoked), true
			nativeAppWrite(t, filepath.Join(f.root, "go"), "a/value_test.go", `package sample;import("os";"testing");func TestValue(t *testing.T){if Value()!=1{t.Fatal("wrong")};if err:=os.WriteFile("../../ts/shared.ts",[]byte("export function Shared(): number{return 2;}"),0644);err!=nil{t.Fatal("mutator")}}`)
			return nil
		}, Invoke: f.observe, Want: late},
	}}
}

// Check the same definitions used by installed acceptance before provisioning or
// executing any fixture. Cancellation occurs only after every oracle validates.
func TestReleaseCLIScenarioDefinitions(t *testing.T) {
	for _, test := range []struct {
		name  string
		steps int
		mixed bool
		build func(*testing.T, *tsReleaseFixture) acceptance.Scenario
	}{
		{"typed-go-canonical-original-lifecycle", 9, false, typedGoReleaseScenario},
		{"typescript-lifecycle", 10, false, tsReleaseLifecycleScenario},
		{"typescript-failure-and-original-fallback", 6, false, tsReleaseFailuresScenario},
		{"typescript-ambiguous-source-original-fallback", 3, false, tsReleaseCollisionScenario},
		{"typescript-cache-and-original-audit", 5, false, tsReleaseCacheAuditScenario},
		{"mixed-workspace-failure-continuation", 5, true, tsReleaseMixedFailuresScenario},
		{"mixed-late-original-source-mutation", 3, true, tsReleaseMixedMutationScenario},
	} {
		t.Run(test.name, func(t *testing.T) {
			cases := tsReleaseCases()
			if test.name == "typed-go-canonical-original-lifecycle" {
				cases = map[string][]string{"go::./a": {"TestValue"}, "go::./b": {"TestValue"}}
			} else if test.mixed {
				if runtime.GOOS == "linux" {
					cases["go::./a"] = []string{"TestValue"}
				} else {
					cases["go::suite"] = nil
				}
			}
			f := &tsReleaseFixture{goReleaseFixture: &goReleaseFixture{t: t, root: "unused", cases: cases}}
			scenario := test.build(t, f)
			if scenario.Name != test.name || len(scenario.Steps) != test.steps {
				t.Fatalf("actual scenario identity or step inventory changed: %s %d", scenario.Name, len(scenario.Steps))
			}
			called := false
			for i := range scenario.Steps {
				scenario.Steps[i].Prepare = func(context.Context) error { called = true; return nil }
				scenario.Steps[i].Invoke = func(context.Context) (acceptance.Observation, error) {
					called = true
					return acceptance.Observation{}, nil
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			result := acceptance.Run(ctx, scenario)
			if called || result.State != "failed" || len(result.Steps) != 1 || result.Steps[0].Name != scenario.Steps[0].Name || !slices.Equal(result.Steps[0].Issues, []string{"scenario_context_cancelled"}) {
				t.Fatalf("actual scenario did not validate before cancellation: %+v callbacks=%t", result, called)
			}
			if test.name == "typed-go-canonical-original-lifecycle" {
				// Reproduce the original defect in a late step: no callback may run,
				// even when earlier steps are valid and the context is cancelled.
				scenario.Steps[6].Want.OriginalScopes = nil
				result = acceptance.Run(ctx, scenario)
				if called || result.State != "failed" || len(result.Steps) != 1 || !slices.Equal(result.Steps[0].Issues, []string{"invalid_scenario"}) {
					t.Fatalf("undeclared original scope reached callbacks: %+v", result)
				}
			}
		})
	}
}

func TestTypeScriptReleaseReceiptOracle(t *testing.T) {
	key := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cases := []string{`["alpha",0]`, `["alpha-extra",0]`}
	outcomes := model.Outcomes{Complete: true, Units: []model.Outcome{{Unit: tsReleaseAlpha, Action: "pass"}}}
	for _, test := range cases {
		outcomes.Tests = append(outcomes.Tests, model.Outcome{Unit: tsReleaseAlpha, Test: test, Action: "pass"})
	}
	value := map[string]any{"schema": 1, "key": key, "cases": cases, "outcomes": outcomes}
	healthy, err := json.Marshal(value)
	if err != nil || goReleaseReceipt(healthy, key, tsReleaseAlpha, cases) != nil {
		t.Fatal("independently authored multi-case TypeScript receipt failed its healthy control")
	}
	for _, foreign := range []string{tsReleaseBeta, "other::alpha.test.ts"} {
		if goReleaseReceipt(healthy, key, foreign, cases) == nil {
			t.Fatal("a receipt crossed its independent file or workspace namespace")
		}
	}
	value["cases"] = []string{`["alpha",1]`, `["alpha-extra",0]`}
	changed, err := json.Marshal(value)
	if err != nil || goReleaseReceipt(changed, key, tsReleaseAlpha, cases) == nil {
		t.Fatal("a changed exact TypeScript case identity was accepted")
	}
}
