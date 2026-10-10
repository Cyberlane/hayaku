package native

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/passcache"
	"github.com/Cyberlane/hayaku/internal/process"
)

func TestExactNativeInventory(t *testing.T) {
	a := Artifact{Name: "./sample", Cases: []string{"TestOne"}}
	for name, out := range map[string]process.Output{
		"pass":        {Completed: true, ExitCode: 0, Stdout: []byte("=== RUN   TestOne\n--- PASS: TestOne (0.00s)\nPASS\n")},
		"missing":     {Completed: true, ExitCode: 0, Stdout: []byte("PASS\n")},
		"duplicate":   {Completed: true, ExitCode: 0, Stdout: []byte("=== RUN   TestOne\n--- PASS: TestOne (0.00s)\n--- PASS: TestOne (0.00s)\nPASS\n")},
		"wrong-exit":  {Completed: true, ExitCode: 1, Stdout: []byte("=== RUN   TestOne\n--- PASS: TestOne (0.00s)\nPASS\n")},
		"interrupted": {Completed: false, ExitCode: 0, Stdout: []byte("=== RUN   TestOne\n--- PASS: TestOne (0.00s)\nPASS\n")},
		"diagnostic":  {Completed: true, ExitCode: 0, Stdout: []byte("=== RUN   TestOne\n--- PASS: TestOne (0.00s)\nPASS\n"), Stderr: []byte("boundary violation")},
	} {
		t.Run(name, func(t *testing.T) {
			o, err := decodeGo(out, a)
			if name == "pass" {
				if err != nil || !passing(o, a) {
					t.Fatalf("%+v %v", o, err)
				}
			} else if err == nil {
				t.Fatal("incomplete execution accepted")
			}
		})
	}
}
func write(t *testing.T, root, name, source string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestFallbackRunsUnqualifiedEveryTime(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module sample\ngo 1.26\n")
	write(t, root, "sample_test.go", `package sample;import("testing";"time");func TestClock(t *testing.T){if time.Now().IsZero(){t.Fatal("zero")}}`)
	for i := 0; i < 2; i++ {
		r, err := Go(context.Background(), root, Config{Schema: 1, Architecture: "arm64", Packages: []string{"."}}, "", false)
		if err != nil || !r.Complete || !r.Passed || len(r.Groups) != 1 || r.Groups[0].Qualified || r.Groups[0].Reused || !r.Groups[0].Executed {
			t.Fatalf("fallback: %+v %v", r, err)
		}
	}
}

// Opt-in integration never pulls images or installs a runner.
func TestInstalledLinuxNativeGroups(t *testing.T) {
	image := os.Getenv("HAYAKU_NATIVE_IMAGE")
	if image == "" {
		t.Skip("set HAYAKU_NATIVE_IMAGE to installed immutable Linux image ID")
	}
	arch := os.Getenv("HAYAKU_NATIVE_ARCH")
	if arch == "" {
		arch = "arm64"
	}
	root := t.TempDir()
	cache := filepath.Join(t.TempDir(), "cache")
	write(t, root, "go.mod", "module sample\ngo 1.26\n")
	good := `package sample;import "testing";func TestValue(t *testing.T){if Value()!=1{t.Fatal("wrong")}}`
	for _, p := range []string{"a", "b"} {
		write(t, root, p+"/sample.go", "package sample;func Value()int{return 1}")
		write(t, root, p+"/sample_test.go", good)
	}
	config := Config{Schema: 1, Image: image, Architecture: arch, Packages: []string{"./a", "./b"}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cold, err := Go(ctx, root, config, cache, false)
	if err != nil || !cold.Passed {
		t.Fatalf("cold %+v: %v", cold, err)
	}
	for _, g := range cold.Groups {
		if !g.Executed || !g.Qualified || g.Reused {
			t.Fatalf("cold group %+v", g)
		}
	}
	warm, err := Go(ctx, root, config, cache, false)
	if err != nil || !warm.Passed {
		t.Fatalf("warm %+v: %v", warm, err)
	}
	for _, g := range warm.Groups {
		if !g.Reused || g.Executed {
			t.Fatalf("expected actual native reuse %+v", g)
		}
	}
	// Compare native functional outcomes with a complete original runner pass.
	goTool, e := exec.LookPath("go")
	if e != nil {
		t.Fatal(e)
	}
	for index, p := range config.Packages {
		full, e := fullGo(ctx, goTool, root, p, "challenge")
		if e != nil || identity(full.Outcomes) != identity(warm.Groups[index].Outcomes) {
			t.Fatalf("full/native mismatch %+v %v", full, e)
		}
	}
	write(t, root, "a/sample.go", "package sample;func Value()int{return 1+0}")
	changed, err := Go(ctx, root, config, cache, false)
	if err != nil || !changed.Passed || !changed.Groups[0].Executed || !changed.Groups[1].Reused {
		t.Fatalf("changed %+v: %v", changed, err)
	}
	audit, err := Go(ctx, root, config, cache, true)
	if err != nil || !audit.Passed {
		t.Fatalf("audit %+v: %v", audit, err)
	}
	for _, g := range audit.Groups {
		if !g.Executed || g.Reused {
			t.Fatalf("audit group %+v", g)
		}
	}
	// Tampered local JSON cannot authorize a hit.
	receipt := filepath.Join(cache, audit.Groups[1].Key+".json")
	if err := os.WriteFile(receipt, []byte(`{"schema":2,"passed":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	tampered, err := Go(ctx, root, config, cache, false)
	if err != nil || !tampered.Passed || !tampered.Groups[1].Executed {
		t.Fatalf("tamper %+v %v", tampered, err)
	}
	// A newly failing assertion invalidates the artifact; the other group can reuse.
	write(t, root, "a/sample.go", "package sample;func Value()int{return 2}")
	failed, err := Go(ctx, root, config, cache, false)
	if err == nil || failed.Passed || !failed.Groups[0].Complete || failed.Groups[0].Passed || !failed.Groups[1].Reused {
		t.Fatalf("failure %+v %v", failed, err)
	}
	if !strings.HasPrefix(failed.Groups[0].Context, "linux-") {
		t.Fatal("native backend did not execute")
	}
	retried, err := Go(ctx, root, config, cache, false)
	if err == nil || !retried.Groups[0].Executed || retried.Groups[0].Reused {
		t.Fatalf("failed execution was reused %+v %v", retried, err)
	}
	// Unexecuted new effects invalidate qualification without coverage evidence.
	write(t, root, "a/sample.go", "package sample;func Value()int{return 1}")
	write(t, root, "a/unused.go", `package sample;import "os";func Uncalled()string{return os.Getenv("SYNTHETIC_FIXTURE")}`)
	broadened, e := Go(ctx, root, config, cache, false)
	if e != nil || !broadened.Passed || broadened.Groups[0].Qualified || !broadened.Groups[0].Executed || broadened.Groups[0].Reused || !broadened.Groups[1].Reused {
		t.Fatalf("uncalled effect did not broaden %+v %v", broadened, e)
	}
	// Live source drift in a full fallback revokes an earlier reused group.
	write(t, root, "c/sample_test.go", `package sample;import("testing";"os");func TestDrift(t *testing.T){if err:=os.WriteFile("drift",[]byte("fixture"),0600);err!=nil{t.Fatal(err)}}`)
	driftConfig := config
	driftConfig.Packages = []string{"./b", "./c"}
	drifted, e := Go(ctx, root, driftConfig, cache, false)
	if e == nil || drifted.Complete || drifted.Passed || !drifted.Groups[0].Reused {
		t.Fatalf("live drift authorized %+v %v", drifted, e)
	}
	driftConfig.Packages = []string{"./b"}
	revoked, e := Go(ctx, root, driftConfig, cache, false)
	if e != nil || !revoked.Passed || !revoked.Groups[0].Executed || revoked.Groups[0].Reused || revoked.Groups[0].Key != drifted.Groups[0].Key {
		t.Fatalf("drift retained earlier authority %+v %v", revoked, e)
	}
}

// Skip receipt semantics form an independent scenario with fresh source/cache.
// Both native executions share the original three-minute scenario bound.
func TestInstalledLinuxNativeGroupSkips(t *testing.T) {
	image := os.Getenv("HAYAKU_NATIVE_IMAGE")
	if image == "" {
		t.Skip("set HAYAKU_NATIVE_IMAGE to installed immutable Linux image ID")
	}
	arch := os.Getenv("HAYAKU_NATIVE_ARCH")
	if arch == "" {
		arch = "arm64"
	}
	root := t.TempDir()
	cache := filepath.Join(t.TempDir(), "cache")
	write(t, root, "go.mod", "module sample\ngo 1.26\n")
	write(t, root, "b/sample.go", "package sample;func Value()int{return 1}")
	write(t, root, "b/sample_test.go", `package sample;import "testing";func TestOptional(t *testing.T){t.Skip("fixture")}`)
	config := Config{Schema: 1, Image: image, Architecture: arch, Packages: []string{"./b"}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var key string
	for i := 0; i < 2; i++ {
		skipped, err := Go(ctx, root, config, cache, false)
		if err != nil || !skipped.Passed || !skipped.Complete || len(skipped.Groups) != 1 {
			t.Fatalf("skip handling %+v %v", skipped, err)
		}
		group := skipped.Groups[0]
		if !group.Qualified || !group.Complete || !group.Passed || group.Context != "linux-"+arch || group.Key == "" || group.Cache != "miss" || group.Reused || !group.Executed || len(group.Outcomes.Tests) != 1 || group.Outcomes.Tests[0].Test != "TestOptional" || group.Outcomes.Tests[0].Action != "skip" {
			t.Fatalf("actual native skip handling %+v", group)
		}
		if i != 0 && group.Key != key {
			t.Fatal("unchanged skip scenario changed its receipt identity")
		}
		key = group.Key
		if _, err := os.Lstat(filepath.Join(cache, key+".json")); !os.IsNotExist(err) {
			t.Fatal("successful native skip minted a reusable receipt", err)
		}
	}
}
func TestCancelledAuditRevokesBeforeBuild(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module sample\ngo 1.26\n")
	cacheDir := filepath.Join(t.TempDir(), "cache")
	cache, err := passcache.Open(cacheDir, "hayaku-native-pure-pass-v1")
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	key := strings.Repeat("a", 64)
	if err = cache.Store(context.Background(), key, true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report, err := Go(ctx, root, Config{Schema: 1, Architecture: "arm64", Packages: []string{"."}}, cacheDir, true)
	if err == nil || report.Passed || report.Complete {
		t.Fatalf("cancelled audit %+v %v", report, err)
	}
	if _, status := cache.Load(key); status != "miss" {
		t.Fatal("interrupted audit retained authority", status)
	}
}
func TestSkippedNativeCaseIsSuccessfulButNotReusable(t *testing.T) {
	a := Artifact{Name: ".", Cases: []string{"TestOptional"}}
	output := process.Output{Completed: true, ExitCode: 0, Stdout: []byte("=== RUN   TestOptional\n--- SKIP: TestOptional (0.00s)\nPASS\n")}
	outcome, err := decodeGo(output, a)
	if err != nil || !outcome.Complete || outcome.Failed || passing(outcome, a) {
		t.Fatalf("skip %+v %v", outcome, err)
	}
}
func TestNativeEmptyGroupCannotSucceed(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module sample\ngo 1.26\n")
	write(t, root, "sample.go", "package sample;func Value()int{return 1}")
	report, err := Go(context.Background(), root, Config{Schema: 1, Architecture: "arm64", Packages: []string{"."}}, "", false)
	if err == nil || report.Complete || report.Passed {
		t.Fatalf("empty group %+v %v", report, err)
	}
}
func TestMalformedProducerInventoryCannotAuthorizeReuse(t *testing.T) {
	outcome := model.Outcomes{Complete: true, Units: []model.Outcome{{Unit: ".", Action: "pass"}}, Tests: []model.Outcome{{Unit: ".", Test: "TestOne", Action: "pass"}, {Unit: ".", Test: "TestOther", Action: "pass"}}}
	if passing(outcome, Artifact{Name: ".", Cases: []string{"TestOne", "TestOne"}}) {
		t.Fatal("duplicate expected case hid an unexpected terminal")
	}
}
func TestBoundedArtifactReadsRejectLinksAndOverflow(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "input", "fixture")
	if _, err := readBounded(filepath.Join(dir, "input"), 3); err == nil {
		t.Fatal("oversized artifact accepted")
	}
	if err := os.Symlink(filepath.Join(dir, "input"), filepath.Join(dir, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := readBounded(filepath.Join(dir, "linked"), 16); err == nil {
		t.Fatal("linked artifact accepted")
	}
}
