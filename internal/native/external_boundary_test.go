package native

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
)

// Production/internal tests share a real Go namespace. The external namespace
// intentionally defines the same helper names with different arithmetic.
const externalProduction = `package sample;func helper()int{return 3;};func Value()int{return helper()}`
const externalInternal = `package sample;import "testing";func testHelper()int{return helper()};func TestAlphaInternal(t *testing.T){if testHelper()!=3 || Value()!=3{t.Fatal("internal arithmetic")}}`
const externalTests = `package sample_test;import "testing";func helper()int{return 7;};func testHelper()int{return helper()};func TestBetaExternal(t *testing.T){if testHelper()!=7{t.Fatal("external arithmetic")}}`

func installedExternalConfig(t *testing.T, packages ...string) Config {
	t.Helper()
	platform := os.Getenv("HAYAKU_GO_EXTERNAL_PLATFORM")
	if platform == "" {
		platform = runtime.GOOS
	}
	if platform != "darwin" && platform != "linux" {
		if os.Getenv("HAYAKU_GO_EXTERNAL_REQUIRED") == "1" || os.Getenv("HAYAKU_GO_EXTERNAL_PLATFORM") != "" {
			t.Fatal("mandatory external corpus requires actual macOS or Linux")
		}
		t.Skip("actual macOS or Linux context required")
	}
	if platform == "darwin" && runtime.GOOS != "darwin" {
		t.Fatal("Darwin external corpus requires actual macOS")
	}
	arch := runtime.GOARCH
	image := ""
	if platform == "linux" {
		image = os.Getenv("HAYAKU_NATIVE_IMAGE")
		if image == "" {
			if os.Getenv("HAYAKU_GO_EXTERNAL_REQUIRED") == "1" {
				t.Fatal("mandatory external corpus requires installed immutable Linux image")
			}
			t.Skip("set HAYAKU_NATIVE_IMAGE for installed Linux external corpus")
		}
		if value := os.Getenv("HAYAKU_NATIVE_ARCH"); value != "" {
			arch = value
		}
	}
	return Config{Schema: 1, Platform: platform, Image: image, Architecture: arch, Packages: packages}
}

func writeExternalPackage(t *testing.T, root, name string) {
	t.Helper()
	write(t, root, filepath.Join(name, "value.go"), externalProduction)
	write(t, root, filepath.Join(name, "internal_test.go"), externalInternal)
	write(t, root, filepath.Join(name, "external_test.go"), externalTests)
}

// A seeded edit must change exactly one intended expression. A stale fixture
// spelling must fail before it can turn an adversarial run into a healthy one.
func rewriteExternal(t *testing.T, source, before, after string) string {
	t.Helper()
	if before == after || strings.Count(source, before) != 1 {
		t.Fatal("external fixture mutation does not identify exactly one expression")
	}
	return strings.Replace(source, before, after, 1)
}

func assertExternalOutcomes(t *testing.T, result Result, alpha, beta string) {
	t.Helper()
	failed := alpha == "fail" || beta == "fail"
	unit := "pass"
	if failed {
		unit = "fail"
	}
	want := model.Outcomes{Complete: true, Failed: failed,
		Units: []model.Outcome{{Unit: result.Name, Action: unit}},
		Tests: []model.Outcome{{Unit: result.Name, Test: "TestAlphaInternal", Action: alpha}, {Unit: result.Name, Test: "TestBetaExternal", Action: beta}}}
	if !result.Complete || result.Passed == failed || !reflect.DeepEqual(result.Outcomes, want) {
		t.Fatalf("independent external native inventory/outcomes differ: complete=%t passed=%t", result.Complete, result.Passed)
	}
}

func externalOriginal(t *testing.T, ctx context.Context, root, name, alpha, beta string) Result {
	t.Helper()
	tool, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	result, err := fullGo(ctx, tool, root, name, "independent external original oracle")
	if (alpha == "fail" || beta == "fail") != (err != nil) {
		t.Fatal("original runner expected exit/error pair differs")
	}
	assertExternalOutcomes(t, result, alpha, beta)
	if !result.Executed || result.Reused || result.Qualified || result.Context != "original-host" {
		t.Fatal("original full runner control was replaced by native evidence")
	}
	return result
}

func externalNativeGroup(t *testing.T, report Report, err error, config Config, index int, alpha, beta string) Result {
	t.Helper()
	failed := alpha == "fail" || beta == "fail"
	if !report.Complete || report.Passed == failed || failed != (err != nil) || len(report.Groups) != len(config.Packages) {
		t.Fatal("native external aggregate completion/exit pair differs", err)
	}
	result := report.Groups[index]
	assertExternalOutcomes(t, result, alpha, beta)
	if !result.Qualified || result.Key == "" || result.Context != config.Platform+"-"+config.Architecture {
		t.Fatal("external package silently used unqualified original-host fallback")
	}
	return result
}

func TestInstalledGoExternalColdWarmAndIndependentGroupChange(t *testing.T) {
	config := installedExternalConfig(t, "./a", "./b")
	root, cache := t.TempDir(), filepath.Join(t.TempDir(), "cache")
	write(t, root, "go.mod", "module externalboundary\ngo 1.26\n")
	for _, name := range []string{"a", "b"} {
		writeExternalPackage(t, root, name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for _, name := range config.Packages {
		externalOriginal(t, ctx, root, name, "pass", "pass")
	}
	cold, err := Go(ctx, root, config, cache, false)
	keys := []string{}
	for i := range config.Packages {
		g := externalNativeGroup(t, cold, err, config, i, "pass", "pass")
		if !g.Executed || g.Reused {
			t.Fatal("external cold run did not actually execute")
		}
		keys = append(keys, g.Key)
	}
	warm, err := Go(ctx, root, config, cache, false)
	for i := range config.Packages {
		g := externalNativeGroup(t, warm, err, config, i, "pass", "pass")
		if g.Executed || !g.Reused || g.Key != keys[i] {
			t.Fatal("external warm run did not reuse authenticated original outcomes")
		}
	}
	write(t, root, "a/external_test.go", rewriteExternal(t, externalTests, "return 7;", "return 7+0;"))
	externalOriginal(t, ctx, root, "./a", "pass", "pass")
	changed, err := Go(ctx, root, config, cache, false)
	a := externalNativeGroup(t, changed, err, config, 0, "pass", "pass")
	b := externalNativeGroup(t, changed, err, config, 1, "pass", "pass")
	if !a.Executed || a.Reused || a.Key == keys[0] || b.Executed || !b.Reused || b.Key != keys[1] {
		t.Fatal("external source changed wrong group or retained old authority")
	}
}

func TestInstalledGoExternalAuditAndTamperedReceipt(t *testing.T) {
	config := installedExternalConfig(t, ".")
	root, cache := t.TempDir(), filepath.Join(t.TempDir(), "cache")
	write(t, root, "go.mod", "module externalboundary\ngo 1.26\n")
	writeExternalPackage(t, root, ".")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	externalOriginal(t, ctx, root, ".", "pass", "pass")
	key := ""
	for _, step := range []string{"cold", "audit", "tamper"} {
		if step == "tamper" {
			if _, err := os.Stat(filepath.Join(cache, key+".json")); err != nil {
				t.Fatal("external audit did not retain an authenticated receipt", err)
			}
			if err := os.WriteFile(filepath.Join(cache, key+".json"), []byte(`{"schema":1,"passed":true}`), 0600); err != nil {
				t.Fatal(err)
			}
		}
		report, err := Go(ctx, root, config, cache, step == "audit")
		group := externalNativeGroup(t, report, err, config, 0, "pass", "pass")
		if !group.Executed || group.Reused || key != "" && group.Key != key {
			t.Fatal("external cold/audit/tampered receipt did not execute its exact native inventory", step)
		}
		key = group.Key
	}
}

func TestInstalledGoExternalIndependentNamespaceFailures(t *testing.T) {
	for _, namespace := range []string{"internal", "external"} {
		t.Run(namespace, func(t *testing.T) {
			config := installedExternalConfig(t, ".")
			root, cache := t.TempDir(), filepath.Join(t.TempDir(), "cache")
			write(t, root, "go.mod", "module externalboundary\ngo 1.26\n")
			writeExternalPackage(t, root, ".")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			externalOriginal(t, ctx, root, ".", "pass", "pass")
			healthy, err := Go(ctx, root, config, cache, false)
			g := externalNativeGroup(t, healthy, err, config, 0, "pass", "pass")
			if !g.Executed || g.Reused {
				t.Fatal("namespace fault lacks healthy native receipt control")
			}
			alpha, beta := "pass", "pass"
			if namespace == "internal" {
				write(t, root, "value.go", rewriteExternal(t, externalProduction, "return 3;", "return 4;"))
				alpha = "fail"
			} else {
				write(t, root, "external_test.go", rewriteExternal(t, externalTests, "return 7;", "return 8;"))
				beta = "fail"
			}
			externalOriginal(t, ctx, root, ".", alpha, beta)
			for attempt := 0; attempt < 2; attempt++ {
				r, err := Go(ctx, root, config, cache, false)
				failed := externalNativeGroup(t, r, err, config, 0, alpha, beta)
				if !failed.Executed || failed.Reused || failed.Key == g.Key {
					t.Fatal("namespace assertion failure reused old pass")
				}
				if _, err := os.Stat(filepath.Join(cache, failed.Key+".json")); !os.IsNotExist(err) {
					t.Fatal("namespace failure minted authenticated all-pass receipt")
				}
			}
		})
	}
}

func TestInstalledGoExternalSkipDoesNotMintPass(t *testing.T) {
	config := installedExternalConfig(t, ".")
	root, cache := t.TempDir(), filepath.Join(t.TempDir(), "cache")
	write(t, root, "go.mod", "module externalboundary\ngo 1.26\n")
	writeExternalPackage(t, root, ".")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	externalOriginal(t, ctx, root, ".", "pass", "pass")
	healthy, err := Go(ctx, root, config, cache, false)
	externalNativeGroup(t, healthy, err, config, 0, "pass", "pass")
	write(t, root, "external_test.go", `package sample_test;import "testing";func TestBetaExternal(t *testing.T){t.Skip("external fixture")}`)
	externalOriginal(t, ctx, root, ".", "pass", "skip")
	for attempt := 0; attempt < 2; attempt++ {
		r, err := Go(ctx, root, config, cache, false)
		g := externalNativeGroup(t, r, err, config, 0, "pass", "skip")
		if !g.Executed || g.Reused {
			t.Fatal("external skip reused an all-cases pass")
		}
		if _, err := os.Stat(filepath.Join(cache, g.Key+".json")); !os.IsNotExist(err) {
			t.Fatal("external skip minted reusable all-cases pass")
		}
	}
}

func TestInstalledGoExternalSubjectImportAndDormantEffectBroaden(t *testing.T) {
	config := installedExternalConfig(t, ".")
	root, cache := t.TempDir(), filepath.Join(t.TempDir(), "cache")
	write(t, root, "go.mod", "module externalboundary\ngo 1.26\n")
	writeExternalPackage(t, root, ".")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	externalOriginal(t, ctx, root, ".", "pass", "pass")
	healthy, err := Go(ctx, root, config, cache, false)
	externalNativeGroup(t, healthy, err, config, 0, "pass", "pass")
	for _, fault := range []string{"subject-import", "dormant-clock"} {
		if fault == "subject-import" {
			write(t, root, "external_test.go", `package sample_test;import("testing";subject "externalboundary");func TestBetaExternal(t *testing.T){if subject.Value()!=3{t.Fatal("subject arithmetic")}}`)
		} else {
			write(t, root, "external_test.go", externalTests)
			write(t, root, "dormant_test.go", `package sample_test;import "time";func Uncalled(){_ = time.Now()}`)
		}
		externalOriginal(t, ctx, root, ".", "pass", "pass")
		for attempt := 0; attempt < 2; attempt++ {
			r, err := Go(ctx, root, config, cache, false)
			if err != nil || !r.Complete || !r.Passed || len(r.Groups) != 1 {
				t.Fatal("external unsupported source did not run complete original command", err)
			}
			g := r.Groups[0]
			assertExternalOutcomes(t, g, "pass", "pass")
			if fault == "subject-import" {
				externalNativeGroup(t, r, err, config, 0, "pass", "pass")
				if (attempt == 0) != g.Executed || (attempt == 1) != g.Reused {
					t.Fatal("closed typed subject import did not execute then reuse")
				}
				continue
			}
			if g.Qualified || g.Reused || !g.Executed || g.Context != "original-host" || g.Reason == "" {
				t.Fatal("subject import or dormant external effect acquired pure authority")
			}
		}
	}
}

func externalBoundBackend(t *testing.T, ctx context.Context, tool, dir string, config Config, build goBuildContext) Backend {
	t.Helper()
	b := Backend{Platform: config.Platform, Architecture: config.Architecture}
	var err error
	if config.Platform == "darwin" {
		b.Worker, err = prepareDarwinWorker(ctx, tool, dir, build.env)
		b.Sandbox = "/usr/bin/sandbox-exec"
	} else {
		b.Worker, err = prepareWorkerEnvironment(ctx, tool, dir, config.Architecture, build.env)
		if err == nil {
			b.Docker, err = ResolveDocker()
		}
		b.Image = config.Image
	}
	if err != nil {
		t.Fatal("actual external worker unavailable", err)
	}
	if err = b.Bind(ctx); err != nil {
		t.Fatal("actual external backend binding failed", err)
	}
	return b
}

func TestInstalledGoExternalIssuerGuardsAndProducerDecoder(t *testing.T) {
	config := installedExternalConfig(t, ".")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	tool, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	tool, err = filepath.EvalSymlinks(tool)
	if err != nil {
		t.Fatal(err)
	}
	build, err := captureGoBuildForPlatform(ctx, tool, config.Platform, config.Architecture)
	if err != nil {
		t.Fatal(err)
	}
	root, source, temporary := t.TempDir(), t.TempDir(), t.TempDir()
	cache := filepath.Join(t.TempDir(), "cache")
	for _, dir := range []string{root, source} {
		write(t, dir, "go.mod", "module externalboundary\ngo 1.26\n")
		writeExternalPackage(t, dir, ".")
	}
	externalOriginal(t, ctx, root, ".", "pass", "pass")
	backend := externalBoundBackend(t, ctx, tool, temporary, config, build)
	binA, binB := filepath.Join(temporary, "bin-a"), filepath.Join(temporary, "bin-b")
	for _, dir := range []string{binA, binB} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(temporary, "binary-alias")
	if err := os.Symlink(binA, alias); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(alias, "program")
	produce := func() Artifact {
		if _, err := runGo(ctx, tool, source, build.env, "test", "-c", "-trimpath", "-buildvcs=false", "-o", filepath.Join(binA, "program"), "."); err != nil {
			t.Fatal(err)
		}
		files, err := packageSources(source)
		if err != nil {
			t.Fatal(err)
		}
		a := Artifact{Name: ".", Program: program, Proof: goSourceProof(files), Cases: []string{"TestAlphaInternal", "TestBetaExternal"}, Args: []string{"-test.v", "-test.count=1", "-test.parallel=1", "-test.timeout=60s"}, Env: []string{"GOMAXPROCS=1", "GODEBUG=randautoseed=0", "TZ=UTC"}}
		a, err = issueGoArtifact(ctx, backend, a, build, root, source, source)
		if err != nil {
			t.Fatal("actual mixed-namespace issuer unavailable", err)
		}
		return a
	}
	a := produce()
	key, _, err := verifyAuthority(ctx, backend, a)
	if err != nil || key == "" {
		t.Fatal("healthy alias-bound external private admission missing", err)
	}
	decoder := func(process.Output, Artifact) (model.Outcomes, error) {
		t.Fatal("caller decoder acquired external native authority")
		return model.Outcomes{}, nil
	}
	healthy, err := Execute(ctx, backend, a, cache, false, decoder)
	assertExternalOutcomes(t, healthy, "pass", "pass")
	if err != nil || !healthy.Qualified || !healthy.Executed || healthy.Reused || healthy.Key != key {
		t.Fatal("actual external producer decoder/alias healthy execution missing", err)
	}
	stored, err := os.Stat(filepath.Join(cache, key+".json"))
	if err != nil || !stored.Mode().IsRegular() {
		t.Fatal("external authority guard challenges lack an actual stored healthy receipt", err)
	}
	before := observableCacheSnapshot(t, cache)
	programBytes, err := readBounded(filepath.Join(binA, "program"), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	// Build a genuinely failing alternate binary independently, restoring copied
	// source before any guard challenge. Retargeting keeps the same alias spelling.
	write(t, source, "external_test.go", rewriteExternal(t, externalTests, "return 7;", "return 8;"))
	externalOriginal(t, ctx, source, ".", "pass", "fail")
	if _, err := runGo(ctx, tool, source, build.env, "test", "-c", "-trimpath", "-buildvcs=false", "-o", filepath.Join(binB, "program"), "."); err != nil {
		t.Fatal(err)
	}
	alternateBytes, err := readBounded(filepath.Join(binB, "program"), 16<<20)
	if err != nil || len(alternateBytes) == 0 || slices.Equal(programBytes, alternateBytes) {
		t.Fatal("independently failing alternate program did not change executable bytes", err)
	}
	write(t, source, "external_test.go", externalTests)
	for _, audit := range []bool{false, true} {
		for _, fault := range []string{"public", "json", "proof", "inventory", "environment", "program-bytes", "program-location", "alias-retarget", "original-internal", "original-external", "copied-external", "copied-module", "backend-location"} {
			t.Run(fault+map[bool]string{false: "/normal", true: "/audit"}[audit], func(t *testing.T) {
				artifact, b := a, backend
				artifact.Cases = slices.Clone(a.Cases)
				artifact.Args = slices.Clone(a.Args)
				artifact.Env = slices.Clone(a.Env)
				switch fault {
				case "public":
					artifact.authority = nil
				case "json":
					data, err := json.Marshal(a)
					if err != nil {
						t.Fatal(err)
					}
					artifact = Artifact{}
					if err := json.Unmarshal(data, &artifact); err != nil {
						t.Fatal(err)
					}
				case "proof":
					artifact.Proof = strings.Repeat("f", 64)
				case "inventory":
					artifact.Cases = []string{"TestAlphaInternal"}
				case "environment":
					artifact.Env = append(artifact.Env, "UNDECLARED=external-fixture")
				case "program-bytes":
					if err := os.WriteFile(filepath.Join(binA, "program"), append(slices.Clone(programBytes), 0), 0700); err != nil {
						t.Fatal(err)
					}
					defer func() {
						if err := os.WriteFile(filepath.Join(binA, "program"), programBytes, 0700); err != nil {
							t.Error(err)
						}
					}()
				case "program-location":
					artifact.Program = filepath.Join(binA, "program")
				case "alias-retarget":
					if err := os.Remove(alias); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(binB, alias); err != nil {
						t.Fatal(err)
					}
					defer func() {
						if err := os.Remove(alias); err != nil {
							t.Error(err)
							return
						}
						if err := os.Symlink(binA, alias); err != nil {
							t.Error(err)
						}
					}()
				case "original-internal":
					write(t, root, "internal_test.go", rewriteExternal(t, externalInternal, "testHelper()!=3", "testHelper()!=4"))
					defer write(t, root, "internal_test.go", externalInternal)
				case "original-external":
					write(t, root, "external_test.go", rewriteExternal(t, externalTests, "return 7;", "return 8;"))
					defer write(t, root, "external_test.go", externalTests)
				case "copied-external":
					write(t, source, "external_test.go", rewriteExternal(t, externalTests, "return 7;", "return 8;"))
					defer write(t, source, "external_test.go", externalTests)
				case "copied-module":
					write(t, source, "go.mod", "module changedexternal\ngo 1.26\n")
					defer write(t, source, "go.mod", "module externalboundary\ngo 1.26\n")
				case "backend-location":
					b.Worker = filepath.Join(temporary, "other-worker-location")
				}
				if err := ctx.Err(); err != nil {
					t.Fatal("external guard fixture exhausted its original deadline", err)
				}
				_, _, guardErr := verifyAuthority(ctx, b, artifact)
				wantError := "native artifact changed after qualification"
				switch fault {
				case "public", "json", "program-location", "backend-location":
					wantError = "native artifact has no matching producer authority"
				case "original-internal", "original-external", "copied-external":
					wantError = "native Go package changed"
				case "copied-module":
					wantError = "native Go module context changed"
				}
				if guardErr == nil || guardErr.Error() != wantError || ctx.Err() != nil {
					t.Fatal("external mutation did not fail at its independent admission guard", guardErr)
				}
				result, err := Execute(ctx, b, artifact, cache, audit, decoder)
				if err == nil || ctx.Err() != nil || result.Complete || result.Passed || result.Qualified || result.Executed || result.Reused || result.Key != "" || !reflect.DeepEqual(before, observableCacheSnapshot(t, cache)) {
					t.Fatal("external mutation reached execution/cache/audit or changed prior authenticated bytes")
				}
			})
		}
	}
	warm, err := Execute(ctx, backend, a, cache, false, decoder)
	assertExternalOutcomes(t, warm, "pass", "pass")
	if err != nil || !warm.Reused || warm.Executed || warm.Key != key {
		t.Fatal("rejected external guards damaged healthy prior receipt", err)
	}
	for _, dir := range []string{root, source} {
		write(t, dir, "external_test.go", rewriteExternal(t, externalTests, "return 7;", "return 8;"))
	}
	externalOriginal(t, ctx, root, ".", "pass", "fail")
	failing := produce()
	failed, err := Execute(ctx, backend, failing, cache, false, decoder)
	assertExternalOutcomes(t, failed, "pass", "fail")
	if err == nil || !failed.Qualified || !failed.Executed || failed.Reused || failed.Key == key {
		t.Fatal("caller decoder hid actual external assertion failure")
	}
	if _, err := os.Stat(filepath.Join(cache, failed.Key+".json")); !os.IsNotExist(err) {
		t.Fatal("producer-decoded external failure minted pass receipt")
	}
}
