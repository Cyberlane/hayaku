package acceptance_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/native"
	"github.com/Cyberlane/hayaku/internal/passcache"
	"github.com/Cyberlane/hayaku/internal/runner"
	"github.com/Cyberlane/hayaku/internal/testutil/acceptance"
)

const nativeDomain = "hayaku-native-pure-pass-v1"

func put(t *testing.T, root, name, value string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

// The fixture oracle is authored here, rather than discovered from the native
// report. Its IDs and actions must remain independent of the selected run.
func oracle(names, cases []string, modes []string, qualified bool) acceptance.Expectation {
	w := acceptance.Expectation{Completed: true, Passed: true, Outcomes: model.Outcomes{Complete: true}}
	for i, name := range names {
		w.Inventory = append(w.Inventory, model.Unit{ID: name, Workspace: "native"})
		w.Outcomes.Units = append(w.Outcomes.Units, model.Outcome{Unit: name, Action: "pass"})
		for _, c := range cases {
			w.Outcomes.Tests = append(w.Outcomes.Tests, model.Outcome{Unit: name, Test: c, Action: "pass"})
		}
		w.Groups = append(w.Groups, acceptance.Group{Name: name, Mode: modes[i], Qualified: qualified})
		if qualified {
			w.Authorized = append(w.Authorized, name)
		}
	}
	return w
}

type nativeFixture struct {
	t            *testing.T
	root, cache  string
	config       native.Config
	audit        bool
	cancelInvoke bool
	keys         map[string]string
}

// Observe uses the actual native coordinator and independently reads the cache
// authority. Report cache labels are deliberately not used as receipt evidence.
func (f *nativeFixture) observe(ctx context.Context) (acceptance.Observation, error) {
	if f.cancelInvoke {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		ctx = cancelled
	}
	r, err := native.Go(ctx, f.root, f.config, f.cache, f.audit)
	o := acceptance.Observation{Completed: r.Complete, Passed: r.Passed, Outcomes: model.Outcomes{Complete: r.Complete}}
	for _, g := range r.Groups {
		o.Outcomes.Units = append(o.Outcomes.Units, g.Outcomes.Units...)
		o.Outcomes.Tests = append(o.Outcomes.Tests, g.Outcomes.Tests...)
		o.Outcomes.Failed = o.Outcomes.Failed || g.Outcomes.Failed
		o.Outcomes.Errors += g.Outcomes.Errors
		mode := nativeMode(g)
		o.Groups = append(o.Groups, acceptance.Group{Name: g.Name, Mode: mode, Qualified: g.Qualified})
		if g.Key != "" {
			f.keys[g.Name] = g.Key
		} else {
			// A fallback has no current qualified artifact. An older artifact's
			// receipt can remain stored without authorizing this changed scope.
			delete(f.keys, g.Name)
		}
	}
	if runner.Failed(o.Outcomes) {
		o.ExitCode = 1
	}
	cache, e := passcache.Open(f.cache, nativeDomain)
	if e != nil {
		f.t.Fatal("cannot independently inspect native receipt authority")
	}
	defer cache.Close()
	for name, key := range f.keys {
		_, state := cache.Load(key)
		if state == "hit" {
			o.Authorized = append(o.Authorized, name)
		} else if f.audit || !r.Complete {
			o.Revoked = append(o.Revoked, name)
		}
	}
	return o, err
}

func nativeMode(g native.Result) string {
	if (g.Complete && g.Executed == g.Reused) || (g.Reused && (!g.Qualified || g.Executed)) {
		return "invalid"
	}
	if g.Reused {
		return "reuse"
	}
	if g.Executed && !g.Qualified {
		return "fallback"
	}
	if g.Executed {
		return "execute"
	}
	return "incomplete"
}

func TestNativeDecisionRequiresActualExecution(t *testing.T) {
	for _, g := range []native.Result{
		{Complete: true, Qualified: true},
		{Complete: true, Qualified: true, Executed: true, Reused: true},
		{Complete: true, Reused: true},
	} {
		if nativeMode(g) != "invalid" {
			t.Fatal("contradictory native execution flags were accepted")
		}
	}
	if nativeMode(native.Result{}) != "incomplete" {
		t.Fatal("unstarted scope counted as execution")
	}
}

func checkScenario(t *testing.T, ctx context.Context, s acceptance.Scenario) {
	t.Helper()
	r := acceptance.Run(ctx, s)
	if r.State == "unavailable" {
		t.Skip(s.Unavailable)
	}
	if r.State != "passed" {
		t.Fatalf("acceptance scenario %s: %+v", s.Name, r)
	}
}

func TestActualGoSharedInputFallback(t *testing.T) {
	root := t.TempDir()
	put(t, root, "go.mod", "module fixture\ngo 1.26\n")
	put(t, root, "shared.txt", "good")
	for _, name := range []string{"a", "b"} {
		put(t, root, name+"/sample_test.go", `package sample;import("testing";"os");func TestShared(t *testing.T){data,err:=os.ReadFile("../shared.txt");if err!=nil||string(data)!="good"{t.Fatal("shared input changed")}}`)
	}
	f := nativeFixture{t: t, root: root, cache: filepath.Join(t.TempDir(), "cache"), config: native.Config{Schema: 1, Architecture: "arm64", Packages: []string{"./a", "./b"}}, keys: map[string]string{}}
	if runtime.GOOS == "windows" {
		// Full original execution does not need qualified cache authority.
		f.cache = ""
	}
	w := oracle([]string{"./a", "./b"}, []string{"TestShared"}, []string{"fallback", "fallback"}, false)
	failed := oracle([]string{"./a", "./b"}, []string{"TestShared"}, []string{"fallback", "fallback"}, false)
	failed.Passed, failed.Error, failed.ExitCode, failed.Outcomes.Failed = false, true, 1, true
	for i := range failed.Outcomes.Units {
		failed.Outcomes.Units[i].Action = "fail"
	}
	for i := range failed.Outcomes.Tests {
		failed.Outcomes.Tests[i].Action = "fail"
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	checkScenario(t, ctx, acceptance.Scenario{Name: "actual-go-shared-input", Steps: []acceptance.Step{
		{Name: "cold-full-fallback", Invoke: f.observe, Want: w},
		{Name: "warm-still-full-fallback", Invoke: f.observe, Want: w},
		{Name: "changed-shared-input", Prepare: func(context.Context) error { put(t, root, "shared.txt", "bad"); return nil }, Invoke: f.observe, Want: failed},
		{Name: "failed-retry", Invoke: f.observe, Want: failed},
		{Name: "repaired-shared-input", Prepare: func(context.Context) error { put(t, root, "shared.txt", "good"); return nil }, Invoke: f.observe, Want: w},
	}})
}

func TestActualCancelledGoAuditRevokesAuthority(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows receipt authority remains unqualified")
	}
	root := t.TempDir()
	put(t, root, "go.mod", "module fixture\ngo 1.26\n")
	f := nativeFixture{t: t, root: root, cache: filepath.Join(t.TempDir(), "cache"), config: native.Config{Schema: 1, Architecture: "arm64", Packages: []string{"."}}, audit: true, cancelInvoke: true, keys: map[string]string{"prior": strings.Repeat("a", 64)}}
	cache, err := passcache.Open(f.cache, nativeDomain)
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Store(context.Background(), f.keys["prior"], true); err != nil {
		t.Fatal(err)
	}
	cache.Close()
	w := acceptance.Expectation{Inventory: []model.Unit{{ID: ".", Workspace: "native"}}, Error: true, Revoked: []string{"prior"}}
	checkScenario(t, context.Background(), acceptance.Scenario{Name: "cancelled-actual-audit", Steps: []acceptance.Step{{Name: "revocation-before-cancelled-build", Invoke: f.observe, Want: w}}})
}

func TestActualInstalledGoLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		checkScenario(t, context.Background(), acceptance.Scenario{Name: "installed-linux-go", Unavailable: "Windows receipt authority remains unqualified"})
		return
	}
	image := os.Getenv("HAYAKU_NATIVE_IMAGE")
	if image == "" {
		checkScenario(t, context.Background(), acceptance.Scenario{Name: "installed-linux-go", Unavailable: "installed immutable HAYAKU_NATIVE_IMAGE required; no image is fetched"})
		return
	}
	arch := os.Getenv("HAYAKU_NATIVE_ARCH")
	if arch == "" {
		arch = "arm64"
	}
	root := t.TempDir()
	put(t, root, "go.mod", "module fixture\ngo 1.26\n")
	for _, p := range []string{"a", "b"} {
		put(t, root, p+"/sample.go", "package sample;func Value()int{return 1}")
		put(t, root, p+"/sample_test.go", `package sample;import "testing";func TestValue(t *testing.T){if Value()!=1{t.Fatal("wrong")}}`)
	}
	f := nativeFixture{t: t, root: root, cache: filepath.Join(t.TempDir(), "cache"), config: native.Config{Schema: 1, Image: image, Architecture: arch, Packages: []string{"./a", "./b"}}, keys: map[string]string{}}
	names, cases := []string{"./a", "./b"}, []string{"TestValue"}
	cold := oracle(names, cases, []string{"execute", "execute"}, true)
	warm := oracle(names, cases, []string{"reuse", "reuse"}, true)
	changed := oracle(names, cases, []string{"execute", "reuse"}, true)
	added := oracle(names, cases, []string{"execute", "reuse"}, true)
	added.Outcomes.Tests = append(added.Outcomes.Tests, model.Outcome{Unit: "./a", Test: "TestAdded", Action: "pass"})
	failed := oracle(names, cases, []string{"execute", "reuse"}, true)
	failed.Passed, failed.Error, failed.ExitCode, failed.Outcomes.Failed = false, true, 1, true
	failed.Outcomes.Units[0].Action, failed.Outcomes.Tests[0].Action = "fail", "fail"
	failed.Authorized = []string{"./b"}
	tampered := oracle(names, cases, []string{"reuse", "execute"}, true)
	broadened := oracle(names, cases, []string{"fallback", "reuse"}, false)
	broadened.Groups[1].Qualified = true
	broadened.Authorized = []string{"./b"}
	skipped := oracle(names, cases, []string{"reuse", "execute"}, true)
	skipped.Outcomes.Tests[1].Action = "skip"
	skipped.Authorized = []string{"./a"}
	steps := []acceptance.Step{
		{Name: "cold", Invoke: f.observe, Want: cold},
		{Name: "warm", Invoke: f.observe, Want: warm},
		{Name: "changed-own-input", Prepare: func(context.Context) error {
			put(t, root, "a/sample.go", "package sample;func Value()int{return 1+0}")
			return nil
		}, Invoke: f.observe, Want: changed},
		{Name: "added-test", Prepare: func(context.Context) error {
			put(t, root, "a/added_test.go", `package sample;import "testing";func TestAdded(t *testing.T){if Value()!=1{t.Fatal("wrong")}}`)
			return nil
		}, Invoke: f.observe, Want: added},
		// Removing the added test returns to an authenticated unchanged artifact.
		{Name: "deleted-test", Prepare: func(context.Context) error { return os.Remove(filepath.Join(root, "a/added_test.go")) }, Invoke: f.observe, Want: warm},
		{Name: "failing-change", Prepare: func(context.Context) error {
			put(t, root, "a/sample.go", "package sample;func Value()int{return 2}")
			return nil
		}, Invoke: f.observe, Want: failed},
		{Name: "failed-retry", Invoke: f.observe, Want: failed},
		{Name: "restored-input", Prepare: func(context.Context) error {
			put(t, root, "a/sample.go", "package sample;func Value()int{return 1+0}")
			return nil
		}, Invoke: f.observe, Want: warm},
		{Name: "unexecuted-effect-broadens", Prepare: func(context.Context) error {
			put(t, root, "a/unused.go", `package sample;import "os";func Uncalled()string{return os.Getenv("SYNTHETIC_FIXTURE")}`)
			return nil
		}, Invoke: f.observe, Want: broadened},
		{Name: "removed-effect-restores-qualification", Prepare: func(context.Context) error {
			return os.Remove(filepath.Join(root, "a/unused.go"))
		}, Invoke: f.observe, Want: warm},
		{Name: "skip-cannot-mint-pass", Prepare: func(context.Context) error {
			put(t, root, "b/sample_test.go", `package sample;import "testing";func TestValue(t *testing.T){t.Skip("fixture")}`)
			return nil
		}, Invoke: f.observe, Want: skipped},
		{Name: "skip-must-execute-again", Invoke: f.observe, Want: skipped},
		{Name: "restored-passing-test", Prepare: func(context.Context) error {
			put(t, root, "b/sample_test.go", `package sample;import "testing";func TestValue(t *testing.T){if Value()!=1{t.Fatal("wrong")}}`)
			return nil
		}, Invoke: f.observe, Want: warm},
		{Name: "tampered-receipt", Prepare: func(context.Context) error {
			return os.WriteFile(filepath.Join(f.cache, f.keys["./b"]+".json"), []byte(`{"schema":2,"passed":true}`), 0600)
		}, Invoke: f.observe, Want: tampered},
		{Name: "forced-audit", Prepare: func(context.Context) error { f.audit = true; return nil }, Invoke: f.observe, Want: cold},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	checkScenario(t, ctx, acceptance.Scenario{Name: "installed-linux-go", Steps: steps})
	// A full fallback writes to the live source tree after an earlier group hit.
	f.audit = false
	put(t, root, "c/sample_test.go", `package sample;import("testing";"os");func TestDrift(t *testing.T){if err:=os.WriteFile("drift",[]byte("fixture"),0600);err!=nil{t.Fatal(err)}}`)
	f.config.Packages = []string{"./b", "./c"}
	drift := oracle([]string{"./b", "./c"}, cases, []string{"reuse", "fallback"}, false)
	drift.Groups[0].Qualified = true
	drift.Outcomes.Tests[1].Test = "TestDrift"
	drift.Completed, drift.Passed, drift.Error, drift.Outcomes.Complete = false, false, true, false
	drift.Authorized, drift.Revoked = []string{"./a"}, []string{"./b"}
	checkScenario(t, ctx, acceptance.Scenario{Name: "source-drift", Steps: []acceptance.Step{{Name: "drift-revokes-earlier-hit", Invoke: f.observe, Want: drift}}})
}
