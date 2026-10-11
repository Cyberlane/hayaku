package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/native"
	"github.com/Cyberlane/hayaku/internal/passcache"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/runner"
	"github.com/Cyberlane/hayaku/internal/testutil/acceptance"
)

const goReleaseDomain = "hayaku-native-pure-pass-v1"

// These finite release controls exercise the packaged CLI. Their case identities,
// actions, decisions and authority expectations are authored independently here;
// they do not establish qualification of arbitrary Go or TypeScript programs.
func TestInstalledGoReleaseCLI(t *testing.T) {
	platform := os.Getenv("HAYAKU_GO_RELEASE_PLATFORM")
	unavailable := ""
	switch {
	case platform == "":
		unavailable = "set HAYAKU_GO_RELEASE_PLATFORM to darwin or linux"
	case platform != "darwin" && platform != "linux":
		t.Fatal("unsupported release acceptance platform")
	case runtime.GOOS != "darwin" && runtime.GOOS != "linux":
		unavailable = "release acceptance requires an actual macOS or Linux CLI host"
	case platform == "darwin" && runtime.GOOS != "darwin":
		unavailable = "Darwin native acceptance requires an actual macOS host"
	case platform == "linux" && os.Getenv("HAYAKU_NATIVE_IMAGE") == "":
		unavailable = "Linux acceptance requires an installed immutable HAYAKU_NATIVE_IMAGE; no image is fetched"
	}
	if unavailable != "" {
		if os.Getenv("HAYAKU_GO_RELEASE_REQUIRED") == "1" {
			t.Fatal(unavailable)
		}
		t.Skip(unavailable)
	}
	arch := os.Getenv("HAYAKU_NATIVE_ARCH")
	if arch == "" {
		arch = runtime.GOARCH
	}
	if arch != "arm64" && arch != "amd64" {
		t.Fatal("unsupported release acceptance architecture")
	}
	cli := buildGoReleaseCLI(t)
	config := native.Config{Schema: 1, Platform: platform, Architecture: arch, Packages: []string{"./a", "./b"}}
	if platform == "linux" {
		config.Image = os.Getenv("HAYAKU_NATIVE_IMAGE")
	}
	// A macOS CLI driving a Linux backend is explicitly distinct from Linux-host
	// CLI acceptance. Run this same test on Linux for the latter result.
	t.Run("host-"+runtime.GOOS+"-backend-"+platform, func(t *testing.T) {
		for _, family := range []struct {
			name string
			run  func(*testing.T, *goReleaseFixture)
		}{{"lifecycle", goReleaseLifecycle}, {"failures-and-fallback", goReleaseFailures}, {"cache-and-audit", goReleaseCacheAudit}} {
			t.Run(family.name, func(t *testing.T) { family.run(t, newGoReleaseFixture(t, cli, config)) })
		}
	})
}

// This positive typed-workspace control requires the original Go command and
// native backend to execute on the same actual host. A macOS controller driving
// a Linux backend does not count as Linux-host qualification.
func TestInstalledTypedGoReleaseCLI(t *testing.T) {
	platform := os.Getenv("HAYAKU_GO_RELEASE_PLATFORM")
	unavailable := ""
	switch {
	case platform == "":
		unavailable = "set HAYAKU_GO_RELEASE_PLATFORM to the actual CLI host platform"
	case platform != runtime.GOOS || (runtime.GOOS != "darwin" && runtime.GOOS != "linux"):
		unavailable = "typed Go positive acceptance requires the same actual macOS or Linux host"
	case platform == "linux" && os.Getenv("HAYAKU_NATIVE_IMAGE") == "":
		unavailable = "typed Linux acceptance requires an already installed immutable image"
	}
	if unavailable != "" {
		if os.Getenv("HAYAKU_TYPED_GO_RELEASE_REQUIRED") == "1" {
			t.Fatal(unavailable)
		}
		t.Skip(unavailable)
	}
	if (runtime.GOARCH != "arm64" && runtime.GOARCH != "amd64") || (os.Getenv("HAYAKU_NATIVE_ARCH") != "" && os.Getenv("HAYAKU_NATIVE_ARCH") != runtime.GOARCH) {
		t.Fatal("typed Go positive acceptance requires the actual host architecture")
	}
	cli := buildGoReleaseCLI(t)
	t.Run("host-"+runtime.GOOS+"-backend-"+platform, func(t *testing.T) {
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		f := &tsReleaseFixture{goReleaseFixture: &goReleaseFixture{t: t, root: root, cache: filepath.Join(t.TempDir(), "passes"), cli: cli,
			cases: map[string][]string{"go::./a": {"TestValue"}, "go::./b": {"TestValue"}}, current: map[string]string{}, history: map[string]map[string]bool{}}}
		nativeAppWrite(t, root, "go.mod", "module typedreleasefixture\ngo 1.26\n")
		for _, name := range []string{"a", "b"} {
			nativeAppWrite(t, root, name+"/value.go", "package sample;func Value()int{return 1}\n")
			nativeAppWrite(t, root, name+"/value_test.go", `package sample;import "testing";func TestValue(t *testing.T){if Value()!=1{t.Fatal("wrong")}}`)
		}
		flags := []string{"--platform", platform, "--architecture", runtime.GOARCH, "--package", "./a", "--package", "./b"}
		if platform == "linux" {
			flags = append(flags, "--image", os.Getenv("HAYAKU_NATIVE_IMAGE"))
		}
		f.config = tsReleaseEnroll(t, cli, root, "go", "go", flags)
		compiler, err := exec.LookPath("go")
		if err == nil {
			compiler, err = filepath.EvalSymlinks(compiler)
		}
		if err != nil || f.config.Platform != platform || f.config.Architecture != runtime.GOARCH || len(f.config.Workspaces) != 1 {
			t.Fatal("packaged Go enrollment changed the native host context")
		}
		workspace := f.config.Workspaces[0]
		if workspace.ID != "go" || workspace.Root != "." || workspace.Adapter != "go" || !slices.Equal(workspace.Patterns, []string{"./a", "./b"}) || !reflect.DeepEqual(workspace.Command, model.Command{Dir: ".", Executable: compiler, Args: []string{"test", "-count=1", "./a", "./b"}}) {
			t.Fatal("packaged Go enrollment did not retain the complete explicit original command")
		}
		f.writeConfig()
		scenario := typedGoReleaseScenario(t, f)
		tsReleaseRun(t, scenario.Name, scenario.Steps)
	})
}

func typedGoReleaseScenario(t *testing.T, f *tsReleaseFixture) acceptance.Scenario {
	root := f.root
	base := f.cases
	failed := tsReleaseFailed(tsReleaseWant(base, "reuse", map[string]string{"go::./a": "execute"}), "go::./a")
	full := tsReleaseWant(map[string][]string{"go::suite": nil}, "fallback", nil, "go::suite")
	audit := full
	audit.Revoked = []string{"go::./a", "go::./b"}
	return acceptance.Scenario{Name: "typed-go-canonical-original-lifecycle", Steps: []acceptance.Step{
		{Name: "cold", Invoke: f.observe, Want: tsReleaseWant(base, "execute", nil)},
		{Name: "warm-and-preserved-foreign-domain", Prepare: func(ctx context.Context) error {
			f.expectKeyChanges()
			return f.seedForeign(ctx)
		}, Invoke: f.observe, Want: tsReleaseWant(base, "reuse", nil)},
		{Name: "independent-source-change-reuses-other-package", Prepare: func(context.Context) error {
			f.expectKeyChanges("go::./a")
			nativeAppWrite(t, root, "a/value.go", "package sample;func Value()int{return 1+0}\n")
			return nil
		}, Invoke: f.observe, Want: tsReleaseWant(base, "reuse", map[string]string{"go::./a": "execute"})},
		{Name: "seeded-assertion", Prepare: func(context.Context) error {
			nativeAppWrite(t, root, "a/value.go", "package sample;func Value()int{return 2}\n")
			return nil
		}, Invoke: f.observe, Want: failed},
		{Name: "failed-case-never-gains-authority-and-executes-again", Invoke: f.observe, Want: failed},
		{Name: "restore-authenticated-healthy-history", Prepare: func(context.Context) error {
			nativeAppWrite(t, root, "a/value.go", "package sample;func Value()int{return 1+0}\n")
			return nil
		}, Invoke: f.observe, Want: tsReleaseWant(base, "reuse", nil)},
		{Name: "forced-audit-runs-original-and-revokes-history", Prepare: func(context.Context) error {
			f.audit, f.revoked = true, slices.Clone(audit.Revoked)
			return nil
		}, Invoke: f.observe, Want: audit},
		{Name: "unreferenced-dormant-effect-runs-entire-original", Prepare: func(context.Context) error {
			f.audit, f.revoked = false, nil
			nativeAppWrite(t, root, "a/dormant.go", `package sample;import "os";func Dormant()string{return os.Getenv("SYNTHETIC_FIXTURE")}`)
			return nil
		}, Invoke: f.observe, Want: full},
		{Name: "unsupported-original-fallback-never-has-a-key", Invoke: f.observe, Want: full},
	}}
}

func buildGoReleaseCLI(t *testing.T) string {
	t.Helper()
	compiler, err := exec.LookPath("go")
	if err != nil {
		t.Fatal("installed Go compiler required to build the actual CLI")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(t.TempDir(), "hayaku")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	output, err := process.Run(ctx, model.Command{Executable: compiler, Args: []string{"build", "-trimpath", "-buildvcs=false", "-o", cli, "./cmd/hayaku"}}, root, map[string]string{
		"GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off", "GOVCS": "*:off", "GOENV": "off", "GOFLAGS": "", "GOWORK": "off",
	})
	if err != nil || !output.Completed || output.ExitCode != 0 {
		t.Fatalf("packaged CLI build failed: completed=%t exit=%d error=%v", output.Completed, output.ExitCode, err)
	}
	return cli
}

type goReleaseFixture struct {
	t                     *testing.T
	root, cache, cli      string
	cases                 map[string][]string
	current               map[string]string
	history               map[string]map[string]bool
	audit, cancelledAudit bool
	foreignKey            string
	foreignHash           [32]byte
}

func newGoReleaseFixture(t *testing.T, cli string, config native.Config) *goReleaseFixture {
	t.Helper()
	f := &goReleaseFixture{t: t, root: t.TempDir(), cache: filepath.Join(t.TempDir(), "passes"), cli: cli,
		cases: map[string][]string{"./a": {"TestValue"}, "./b": {"TestValue"}}, current: map[string]string{}, history: map[string]map[string]bool{}}
	nativeAppWrite(t, f.root, "go.mod", "module releasefixture\ngo 1.26\n")
	for _, name := range []string{"a", "b"} {
		nativeAppWrite(t, f.root, name+"/value.go", "package sample;func Value()int{return 1}\n")
		nativeAppWrite(t, f.root, name+"/value_test.go", `package sample;import "testing";func TestValue(t *testing.T){if Value()!=1{t.Fatal("wrong")}}`)
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	nativeAppWrite(t, f.root, "native.json", string(data))
	return f
}

func goReleaseWant(aMode, bMode string) acceptance.Expectation {
	want := acceptance.Expectation{Completed: true, Passed: true, Outcomes: model.Outcomes{Complete: true}}
	for i, name := range []string{"./a", "./b"} {
		mode := []string{aMode, bMode}[i]
		want.Inventory = append(want.Inventory, model.Unit{ID: name, Workspace: "native"})
		want.Outcomes.Units = append(want.Outcomes.Units, model.Outcome{Unit: name, Action: "pass"})
		want.Outcomes.Tests = append(want.Outcomes.Tests, model.Outcome{Unit: name, Test: "TestValue", Action: "pass"})
		want.Groups = append(want.Groups, acceptance.Group{Name: name, Mode: mode, Qualified: mode != "fallback"})
		if mode != "fallback" {
			want.Authorized = append(want.Authorized, name)
		}
	}
	return want
}

func goReleaseRun(t *testing.T, name string, steps []acceptance.Step) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	result := acceptance.Run(ctx, acceptance.Scenario{Name: name, Steps: steps})
	if result.State != "passed" {
		t.Fatalf("packaged CLI acceptance failed: %+v", result)
	}
}

func goReleaseLifecycle(t *testing.T, f *goReleaseFixture) {
	added := goReleaseWant("execute", "reuse")
	added.Outcomes.Tests = append(added.Outcomes.Tests, model.Outcome{Unit: "./a", Test: "TestAdded", Action: "pass"})
	goReleaseRun(t, "packaged-go-lifecycle", []acceptance.Step{
		{Name: "cold", Invoke: f.observe, Want: goReleaseWant("execute", "execute")},
		{Name: "warm", Invoke: f.observe, Want: goReleaseWant("reuse", "reuse")},
		{Name: "own-source-change", Prepare: func(context.Context) error {
			nativeAppWrite(t, f.root, "a/value.go", "package sample;func Value()int{return 1+0}\n")
			return nil
		}, Invoke: f.observe, Want: goReleaseWant("execute", "reuse")},
		{Name: "new-case", Prepare: func(context.Context) error {
			nativeAppWrite(t, f.root, "a/added_test.go", `package sample;import "testing";func TestAdded(t *testing.T){if Value()!=1{t.Fatal("wrong")}}`)
			f.cases["./a"] = []string{"TestAdded", "TestValue"}
			return nil
		}, Invoke: f.observe, Want: added},
		{Name: "deleted-case", Prepare: func(context.Context) error {
			f.cases["./a"] = []string{"TestValue"}
			return os.Remove(filepath.Join(f.root, "a/added_test.go"))
		}, Invoke: f.observe, Want: goReleaseWant("reuse", "reuse")},
	})
}

func goReleaseFailures(t *testing.T, f *goReleaseFixture) {
	failed := goReleaseWant("execute", "reuse")
	failed.Passed, failed.Error, failed.ExitCode, failed.Outcomes.Failed = false, true, 1, true
	failed.Outcomes.Units[0].Action, failed.Outcomes.Tests[0].Action = "fail", "fail"
	failed.Authorized = []string{"./b"}
	skipped := goReleaseWant("reuse", "execute")
	skipped.Outcomes.Tests[1].Action = "skip"
	skipped.Authorized = []string{"./a"}
	goReleaseRun(t, "packaged-go-failure-and-fallback", []acceptance.Step{
		{Name: "healthy-control", Invoke: f.observe, Want: goReleaseWant("execute", "execute")},
		{Name: "seeded-assertion", Prepare: func(context.Context) error {
			nativeAppWrite(t, f.root, "a/value.go", "package sample;func Value()int{return 2}\n")
			return nil
		}, Invoke: f.observe, Want: failed},
		{Name: "failure-must-execute-again", Invoke: f.observe, Want: failed},
		{Name: "repair-assertion", Prepare: func(context.Context) error {
			nativeAppWrite(t, f.root, "a/value.go", "package sample;func Value()int{return 1}\n")
			return nil
		}, Invoke: f.observe, Want: goReleaseWant("reuse", "reuse")},
		{Name: "skip-no-authority", Prepare: func(context.Context) error {
			nativeAppWrite(t, f.root, "b/value_test.go", `package sample;import "testing";func TestValue(t *testing.T){t.Skip("fixture")}`)
			return nil
		}, Invoke: f.observe, Want: skipped},
		{Name: "skip-must-execute-again", Invoke: f.observe, Want: skipped},
		{Name: "repair-skip", Prepare: func(context.Context) error {
			nativeAppWrite(t, f.root, "b/value_test.go", `package sample;import "testing";func TestValue(t *testing.T){if Value()!=1{t.Fatal("wrong")}}`)
			return nil
		}, Invoke: f.observe, Want: goReleaseWant("reuse", "reuse")},
		{Name: "dormant-ambient-effect-full-fallback", Prepare: func(context.Context) error {
			nativeAppWrite(t, f.root, "a/dormant.go", `package sample;import("os";"time");func Dormant()string{return os.Getenv("SYNTHETIC_FIXTURE")+time.Now().String()}`)
			return nil
		}, Invoke: f.observe, Want: goReleaseWant("fallback", "reuse")},
		{Name: "fallback-never-reused", Invoke: f.observe, Want: goReleaseWant("fallback", "reuse")},
	})
}

func goReleaseCacheAudit(t *testing.T, f *goReleaseFixture) {
	cancelled := acceptance.Expectation{Inventory: []model.Unit{{ID: "./a", Workspace: "native"}, {ID: "./b", Workspace: "native"}}, Error: true, ExitCode: 1, Revoked: []string{"./a", "./b"}}
	goReleaseRun(t, "packaged-go-cache-and-audit", []acceptance.Step{
		{Name: "healthy-control", Invoke: f.observe, Want: goReleaseWant("execute", "execute")},
		{Name: "warm-and-foreign-domain", Prepare: func(ctx context.Context) error { return f.seedForeign(ctx) }, Invoke: f.observe, Want: goReleaseWant("reuse", "reuse")},
		{Name: "corrupt-one-authenticated-receipt", Prepare: func(context.Context) error {
			key := f.current["./b"]
			if key == "" {
				return errors.New("missing authored healthy receipt control")
			}
			return os.WriteFile(filepath.Join(f.cache, key+".json"), []byte(`{"schema":2,"passed":true}`), 0600)
		}, Invoke: f.observe, Want: goReleaseWant("reuse", "execute")},
		{Name: "forced-audit-executes-all", Prepare: func(context.Context) error { f.audit = true; return nil }, Invoke: f.observe, Want: goReleaseWant("execute", "execute")},
		{Name: "cancelled-audit-revokes-only-own-domain", Prepare: func(context.Context) error { f.cancelledAudit = true; return nil }, Invoke: f.observe, Want: cancelled},
	})
}

func (f *goReleaseFixture) observe(ctx context.Context) (acceptance.Observation, error) {
	args := []string{"native", "--root", f.root, "--cache-dir", f.cache}
	if f.audit {
		args = append(args, "--no-reuse")
	}
	if f.cancelledAudit {
		// Cancel inside the application, after the CLI can run its audit cleanup.
		args = append(args, "--timeout=1ns")
	}
	output, invokeErr := process.RunEnvironmentLimits(ctx, model.Command{Executable: f.cli, Args: args}, f.root, os.Environ(), 1<<20, 128<<10)
	if !output.Completed {
		return acceptance.Observation{ExitCode: output.ExitCode}, errors.Join(invokeErr, errors.New("packaged CLI did not join"))
	}
	var report native.Report
	if err := goReleaseDecode(output.Stdout, &report); err != nil || report.Schema != 1 || report.Contract != "native-pure-groups-v1" {
		f.t.Fatal("invalid packaged native report")
	}
	for _, group := range report.Groups {
		f.t.Logf("native group=%q mode=%q reason=%q qualified=%t complete=%t passed=%t", group.Name, goReleaseMode(group), group.Reason, group.Qualified, group.Complete, group.Passed)
	}
	observation := acceptance.Observation{Completed: report.Complete, Passed: report.Passed, ExitCode: output.ExitCode, Outcomes: model.Outcomes{Complete: report.Complete}}
	f.current = map[string]string{}
	for _, group := range report.Groups {
		observation.Outcomes.Units = append(observation.Outcomes.Units, group.Outcomes.Units...)
		observation.Outcomes.Tests = append(observation.Outcomes.Tests, group.Outcomes.Tests...)
		observation.Outcomes.Failed = observation.Outcomes.Failed || group.Outcomes.Failed
		observation.Outcomes.Errors += group.Outcomes.Errors
		observation.Groups = append(observation.Groups, acceptance.Group{Name: group.Name, Mode: goReleaseMode(group), Qualified: group.Qualified})
		if group.Key != "" {
			f.current[group.Name] = group.Key
		}
	}
	cache, err := goReleaseExistingCache(f.cache)
	if err != nil {
		f.t.Fatal("independent receipt inspection failed", err)
	}
	if cache != nil {
		for name, key := range f.current {
			data, state := cache.Load(key)
			if state == "hit" {
				if err := goReleaseReceipt(data, key, name, f.cases[name]); err != nil {
					f.t.Fatal("authenticated receipt contradicts the independent oracle", err)
				}
				observation.Authorized = append(observation.Authorized, name)
				if f.history[name] == nil {
					f.history[name] = map[string]bool{}
				}
				f.history[name][key] = true
			}
		}
		if f.cancelledAudit {
			for name, keys := range f.history {
				allAbsent := true
				for key := range keys {
					_, state := cache.Load(key)
					allAbsent = allAbsent && state == "miss"
				}
				if allAbsent {
					observation.Revoked = append(observation.Revoked, name)
				}
			}
		}
		if err := cache.Close(); err != nil {
			f.t.Fatal("independent receipt inspection did not close", err)
		}
	}
	if f.foreignKey != "" {
		data, err := os.ReadFile(filepath.Join(f.cache, f.foreignKey+".json"))
		if err != nil || sha256.Sum256(data) != f.foreignHash {
			f.t.Fatal("foreign-domain bytes changed")
		}
	}
	return observation, invokeErr
}

func goReleaseMode(group native.Result) string {
	if (group.Complete && group.Executed == group.Reused) || (group.Reused && (!group.Complete || !group.Passed || !group.Qualified || group.Executed)) {
		return "invalid"
	}
	if !group.Complete {
		return "incomplete"
	}
	if group.Reused {
		return "reuse"
	}
	if group.Executed && !group.Qualified {
		return "fallback"
	}
	if group.Executed {
		return "execute"
	}
	return "incomplete"
}

// Inspection must never create authority: Open creates absent roots and keys.
func goReleaseExistingCache(directory string) (*passcache.Cache, error) {
	info, err := os.Lstat(directory)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("existing receipt root is not private")
	}
	key, err := os.Lstat(filepath.Join(directory, "authority.key"))
	if err != nil || !key.Mode().IsRegular() || key.Size() != 32 || key.Mode().Perm()&0077 != 0 {
		return nil, errors.New("existing receipt authority is missing or invalid")
	}
	return passcache.Open(directory, goReleaseDomain)
}

func goReleaseDecode(data []byte, value any) error {
	if len(data) > passcache.MaxBytes {
		return errors.New("release evidence exceeds bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("release evidence has trailing content")
	}
	return nil
}

func goReleaseReceipt(data []byte, key, name string, cases []string) error {
	var receipt struct {
		Schema   int            `json:"schema"`
		Key      string         `json:"key"`
		Cases    []string       `json:"cases"`
		Outcomes model.Outcomes `json:"outcomes"`
	}
	if err := goReleaseDecode(data, &receipt); err != nil {
		return err
	}
	expected := model.Outcomes{Complete: true, Units: []model.Outcome{{Unit: name, Action: "pass"}}}
	wantedCases := slices.Clone(cases)
	slices.Sort(wantedCases)
	for _, test := range wantedCases {
		expected.Tests = append(expected.Tests, model.Outcome{Unit: name, Test: test, Action: "pass"})
	}
	gotCases := slices.Clone(receipt.Cases)
	slices.Sort(gotCases)
	slices.SortFunc(receipt.Outcomes.Tests, func(a, b model.Outcome) int { return bytes.Compare([]byte(a.Test), []byte(b.Test)) })
	if receipt.Schema != 1 || receipt.Key != key || len(cases) == 0 || !slices.Equal(gotCases, wantedCases) || !reflect.DeepEqual(receipt.Outcomes, expected) {
		return errors.New("authenticated receipt contradicts independently authored inventory or passing outcomes")
	}
	inventory := model.ExecutionInventory{Schema: model.ExecutionSchema, Units: []model.Unit{{ID: name, Workspace: "native"}}}
	for _, test := range wantedCases {
		inventory.Cases = append(inventory.Cases, model.Case{Unit: name, ID: test})
	}
	return runner.ValidateExact(receipt.Outcomes, inventory, true, 0)
}

func (f *goReleaseFixture) seedForeign(ctx context.Context) error {
	cache, err := goReleaseExistingCache(f.cache)
	if err != nil || cache == nil {
		return errors.Join(err, errors.New("foreign control requires existing healthy native authority"))
	}
	if err := cache.Close(); err != nil {
		return err
	}
	foreign, err := passcache.Open(f.cache, "hayaku-release-foreign-control-v1")
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte("authored-foreign-domain-control"))
	f.foreignKey = hex.EncodeToString(sum[:])
	err = foreign.Store(ctx, f.foreignKey, true)
	if _, state := foreign.Load(f.foreignKey); state != "hit" {
		err = errors.Join(err, errors.New("foreign-domain healthy control did not authenticate"))
	}
	err = errors.Join(err, foreign.Close())
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(f.cache, f.foreignKey+".json"))
	f.foreignHash = sha256.Sum256(data)
	return err
}

func TestGoReleaseReceiptOracleRejectsMalformedEvidence(t *testing.T) {
	key := fmt.Sprintf("%064x", 1)
	value := map[string]any{"schema": 1, "key": key, "cases": []string{"TestValue"}, "outcomes": model.Outcomes{Complete: true, Units: []model.Outcome{{Unit: "./a", Action: "pass"}}, Tests: []model.Outcome{{Unit: "./a", Test: "TestValue", Action: "pass"}}}}
	healthy, err := json.Marshal(value)
	if err != nil || goReleaseReceipt(healthy, key, "./a", []string{"TestValue"}) != nil {
		t.Fatal("independently authored healthy receipt oracle failed")
	}
	for _, data := range [][]byte{append(slices.Clone(healthy), []byte("{}")...), bytes.Repeat([]byte(" "), passcache.MaxBytes+1)} {
		if goReleaseReceipt(data, key, "./a", []string{"TestValue"}) == nil {
			t.Fatal("unbounded or trailing evidence accepted")
		}
	}
	for _, mutation := range []func(){
		func() { value["key"] = fmt.Sprintf("%064x", 2) },
		func() { value["unexpected"] = true },
		func() { value["cases"] = []string{"TestOther"} },
		func() {
			value["outcomes"] = model.Outcomes{Complete: true, Units: []model.Outcome{{Unit: "./a", Action: "pass"}}, Tests: []model.Outcome{{Unit: "./a", Test: "TestValue", Action: "skip"}}}
		},
	} {
		if json.Unmarshal(healthy, &value) != nil {
			t.Fatal("cannot restore oracle control")
		}
		delete(value, "unexpected")
		mutation()
		data, err := json.Marshal(value)
		if err != nil || goReleaseReceipt(data, key, "./a", []string{"TestValue"}) == nil {
			t.Fatal("receipt identity, inventory or terminal mutation accepted")
		}
	}
}

func TestGoReleaseInspectionCreatesNoAuthority(t *testing.T) {
	root := filepath.Join(t.TempDir(), "absent")
	cache, err := goReleaseExistingCache(root)
	if err != nil || cache != nil {
		t.Fatal("absent receipt inspection failed")
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatal("inspection created absent receipt authority")
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if cache, err := goReleaseExistingCache(root); err == nil || cache != nil {
		t.Fatal("missing existing signing key was accepted")
	}
	if _, err := os.Lstat(filepath.Join(root, "authority.key")); !os.IsNotExist(err) {
		t.Fatal("inspection created missing signing authority")
	}
}

func TestGoReleaseDecisionRequiresActualExecution(t *testing.T) {
	for _, group := range []native.Result{
		{Complete: true, Qualified: true},
		{Complete: true, Reused: true},
		{Qualified: true, Passed: true, Reused: true},
		{Complete: true, Qualified: true, Reused: true},
		{Qualified: true, Executed: true, Reused: true},
	} {
		if goReleaseMode(group) != "invalid" {
			t.Fatal("contradictory execution decision accepted")
		}
	}
	for _, group := range []native.Result{
		{},
		{Qualified: true, Executed: true},
		{Executed: true},
	} {
		if goReleaseMode(group) != "incomplete" {
			t.Fatal("unstarted or incomplete group counted as completed execution")
		}
	}
	for _, control := range []struct {
		group native.Result
		mode  string
	}{
		{native.Result{Complete: true, Qualified: true, Passed: true, Reused: true}, "reuse"},
		{native.Result{Complete: true, Qualified: true, Executed: true}, "execute"},
		{native.Result{Complete: true, Executed: true}, "fallback"},
	} {
		if goReleaseMode(control.group) != control.mode {
			t.Fatal("completed execution control misclassified")
		}
	}
}
