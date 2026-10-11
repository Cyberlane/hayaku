package native

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
)

func localHelpersFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, "go.mod", "module helperboundary\ngo 1.26\n")
	for name, source := range map[string]string{
		"leaf/leaf.go":         `package leaf;const Delta=1;func Value()int{return Delta}`,
		"left/left.go":         `package left;import "helperboundary/leaf";func Value()int{return leaf.Value()}`,
		"right/right.go":       `package right;import l "helperboundary/leaf";func Value()int{return l.Value()+1}`,
		"independent/value.go": `package independent;func Value()int{return 3}`,
		"a/value.go":           `package sample;import("helperboundary/left";"helperboundary/right");func Value()int{return left.Value()+right.Value()};func helper()int{return Value()}`,
		"b/value.go":           `package sample;import "helperboundary/independent";func Value()int{return independent.Value()};func helper()int{return Value()}`,
	} {
		write(t, root, name, source)
	}
	for _, name := range []string{"a", "b"} {
		write(t, root, name+"/internal_test.go", externalInternal)
		write(t, root, name+"/external_test.go", `package sample_test;import(tt "testing";subject "helperboundary/`+name+`");func helper()int{return 7};func TestBetaExternal(t *tt.T){if subject.Value()!=3||helper()!=7{t.Fatal("typed subject arithmetic")}}`)
	}
	return root
}

func stringHelpersFixture(t *testing.T) string {
	t.Helper()
	root := localHelpersFixture(t)
	write(t, root, "leaf/data.txt", "x")
	write(t, root, "leaf/leaf.go", "package leaf\nimport _ \"embed\"\n//go:embed data.txt\nvar Fixture string\nfunc Setup(value string)int{return len(value)}\nfunc Value()int{return Setup(Fixture)}\n")
	return root
}

func TestInstalledGoStringFixturesColdWarmAndReachableInvalidation(t *testing.T) {
	config := installedExternalConfig(t, "./a", "./b")
	root, cache := stringHelpersFixture(t), filepath.Join(t.TempDir(), "cache")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for _, name := range config.Packages {
		externalOriginal(t, ctx, root, name, "pass", "pass")
	}
	cold, err := Go(ctx, root, config, cache, false)
	for i := range config.Packages {
		externalNativeGroup(t, cold, err, config, i, "pass", "pass")
	}
	warm, err := Go(ctx, root, config, cache, false)
	for i := range config.Packages {
		g := externalNativeGroup(t, warm, err, config, i, "pass", "pass")
		if !g.Reused || g.Executed || g.Key != cold.Groups[i].Key {
			t.Fatal("literal string fixture did not warm reuse")
		}
	}
	write(t, root, "leaf/data.txt", "y")
	externalOriginal(t, ctx, root, "./a", "pass", "pass")
	changed, err := Go(ctx, root, config, cache, false)
	a := externalNativeGroup(t, changed, err, config, 0, "pass", "pass")
	b := externalNativeGroup(t, changed, err, config, 1, "pass", "pass")
	if !a.Executed || a.Reused || a.Key == cold.Groups[0].Key || !b.Reused || b.Executed || b.Key != cold.Groups[1].Key {
		t.Fatal("compiler string fixture invalidated wrong group")
	}
}

func TestInstalledGoStringFixturesAssertionFailureAndRetry(t *testing.T) {
	config := installedExternalConfig(t, "./a")
	root, cache := stringHelpersFixture(t), filepath.Join(t.TempDir(), "cache")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	write(t, root, "leaf/data.txt", "xx")
	externalOriginal(t, ctx, root, "./a", "fail", "fail")
	failed, err := Go(ctx, root, config, cache, false)
	g := externalNativeGroup(t, failed, err, config, 0, "fail", "fail")
	if !g.Executed || g.Reused {
		t.Fatal("fixture failure replaced by reuse")
	}
	if _, err := os.Stat(filepath.Join(cache, g.Key+".json")); !os.IsNotExist(err) {
		t.Fatal("fixture failure minted passing receipt")
	}
	write(t, root, "leaf/data.txt", "x")
	retry, err := Go(ctx, root, config, cache, false)
	g = externalNativeGroup(t, retry, err, config, 0, "pass", "pass")
	if !g.Executed || g.Reused {
		t.Fatal("fixed string fixture failed to execute")
	}
}

func TestInstalledGoStringFixtureVariantsKeepIndependentCaseInventory(t *testing.T) {
	config := installedExternalConfig(t, "./a")
	root, cache := stringHelpersFixture(t), filepath.Join(t.TempDir(), "cache")
	write(t, root, "a/internal.txt", "internal")
	write(t, root, "a/external.txt", "external")
	write(t, root, "a/internal_test.go", "package sample\nimport(\"testing\";_ \"embed\")\n//go:embed internal.txt\nvar fixture string\nfunc testHelper()int{return helper()}\nfunc TestAlphaInternal(t *testing.T){if testHelper()!=3||Value()!=3||fixture!=\"internal\"{t.Fatal(\"internal fixture\")}}\n")
	write(t, root, "a/external_test.go", "package sample_test\nimport(tt \"testing\";subject \"helperboundary/a\";_ \"embed\")\n//go:embed external.txt\nvar fixture string\nfunc helper()int{return 7}\nfunc TestBetaExternal(t *tt.T){if subject.Value()!=3||helper()!=7||fixture!=\"external\"{t.Fatal(\"external fixture\")}}\n")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	externalOriginal(t, ctx, root, "./a", "pass", "pass")
	cold, err := Go(ctx, root, config, cache, false)
	coldGroup := externalNativeGroup(t, cold, err, config, 0, "pass", "pass")
	warm, err := Go(ctx, root, config, cache, false)
	warmGroup := externalNativeGroup(t, warm, err, config, 0, "pass", "pass")
	if !warmGroup.Reused || warmGroup.Executed || warmGroup.Key != coldGroup.Key {
		t.Fatal("selected fixture variants failed warm reuse")
	}
	write(t, root, "a/external.txt", "fault")
	externalOriginal(t, ctx, root, "./a", "pass", "fail")
	failed, err := Go(ctx, root, config, cache, false)
	group := externalNativeGroup(t, failed, err, config, 0, "pass", "fail")
	if !group.Executed || group.Reused || group.Key == coldGroup.Key {
		t.Fatal("external fixture fault lost distinct namespace identity")
	}
	if _, err := os.Stat(filepath.Join(cache, group.Key+".json")); !os.IsNotExist(err) {
		t.Fatal("failed external fixture minted pass")
	}
}

func TestInstalledGoStringFixturesUnsupportedSetupAlwaysBroaden(t *testing.T) {
	config := installedExternalConfig(t, "./a")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for _, kind := range []string{"write", "bytes", "glob", "embed-fs", "init", "test-main"} {
		t.Run(kind, func(t *testing.T) {
			root, cache := stringHelpersFixture(t), filepath.Join(t.TempDir(), "cache")
			data, err := os.ReadFile(filepath.Join(root, "leaf/leaf.go"))
			if err != nil {
				t.Fatal(err)
			}
			source := string(data)
			switch kind {
			case "write":
				source += "func Dormant(){Fixture=Fixture}\n"
			case "bytes":
				source = rewriteExternal(t, source, "var Fixture string", "var Fixture []byte")
				source = rewriteExternal(t, source, "Setup(value string)", "Setup(value []byte)")
			case "glob":
				source = rewriteExternal(t, source, "//go:embed data.txt", "//go:embed data*.txt")
			case "embed-fs":
				source = "package leaf\nimport \"embed\"\n//go:embed data.txt\nvar Fixture embed.FS\nfunc Value()int{data,err:=Fixture.ReadFile(\"data.txt\");if err!=nil{return 0};return len(data)}\n"
			case "init":
				source += "func init(){}\n"
			case "test-main":
				write(t, root, "a/setup_test.go", "package sample\nimport(\"testing\";\"os\")\nfunc TestMain(m *testing.M){os.Exit(m.Run())}\n")
			}
			write(t, root, "leaf/leaf.go", source)
			externalOriginal(t, ctx, root, "./a", "pass", "pass")
			for attempt := 0; attempt < 2; attempt++ {
				report, err := Go(ctx, root, config, cache, false)
				if err != nil || !report.Complete || !report.Passed || len(report.Groups) != 1 {
					t.Fatal("unsupported setup failed original scope", err)
				}
				g := report.Groups[0]
				assertExternalOutcomes(t, g, "pass", "pass")
				if !g.Executed || g.Qualified || g.Reused || g.Key != "" || g.Context != "original-host" {
					t.Fatal("unsupported fixture acquired authority")
				}
			}
		})
	}
}

func TestInstalledGoWorkspaceNamespacesAndOriginalFallback(t *testing.T) {
	config := installedExternalConfig(t, "./a", "./b")
	root, cache := stringHelpersFixture(t), filepath.Join(t.TempDir(), "cache")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	config.Packages = nil
	c := RunConfig{Config: config}
	for _, name := range []string{"first", "second"} {
		c.Workspaces = append(c.Workspaces, Workspace{Workspace: model.Workspace{ID: name, Root: ".", Adapter: "go", Command: model.Command{Dir: ".", Executable: "go", Args: []string{"test", "-count=1", "./a", "./b"}}, Patterns: []string{"./a", "./b"}}})
	}
	if c.Platform != runtime.GOOS || c.Architecture != runtime.GOARCH {
		for attempt := 0; attempt < 2; attempt++ {
			report, err := RunWithOptions(ctx, root, c, ExecutionOptions{CacheDir: cache})
			if err != nil || !report.Complete || !report.Passed || len(report.Groups) != 2 {
				t.Fatal("foreign native target failed original host scope", err)
			}
			for i, g := range report.Groups {
				if g.Name != c.Workspaces[i].ID+"::suite" || !g.Executed || g.Reused || g.Qualified || g.Key != "" {
					t.Fatal("foreign native target acquired original-host authority")
				}
			}
		}
		return
	}
	cold, err := RunWithOptions(ctx, root, c, ExecutionOptions{CacheDir: cache})
	if err != nil || !cold.Complete || !cold.Passed || len(cold.Groups) != 4 {
		t.Fatal("typed Go cold workspace failed", err)
	}
	for index, name := range []string{"first::./a", "first::./b", "second::./a", "second::./b"} {
		g := cold.Groups[index]
		assertExternalOutcomes(t, g, "pass", "pass")
		if g.Name != name || !g.Qualified || !g.Executed || g.Reused || g.Key == "" {
			t.Fatal("typed workspace lost producer identity", name, g)
		}
	}
	if cold.Groups[0].Key == cold.Groups[2].Key || cold.Groups[1].Key == cold.Groups[3].Key {
		t.Fatal("workspace namespace absent from sealed keys")
	}
	warm, err := RunWithOptions(ctx, root, c, ExecutionOptions{CacheDir: cache})
	if err != nil || !warm.Complete || !warm.Passed || len(warm.Groups) != 4 {
		t.Fatal("typed Go warm workspace failed", err)
	}
	for i, g := range warm.Groups {
		if !g.Reused || g.Executed || g.Key != cold.Groups[i].Key {
			t.Fatal("typed workspace failed warm reuse")
		}
	}
	audited, err := RunWithOptions(ctx, root, c, ExecutionOptions{CacheDir: cache, Audit: true})
	if err != nil || !audited.Complete || !audited.Passed || len(audited.Groups) != 2 {
		t.Fatal("typed audit failed original configured scopes", err)
	}
	for i, g := range audited.Groups {
		if g.Name != c.Workspaces[i].ID+"::suite" || !g.Executed || g.Reused || g.Qualified || g.Key != "" {
			t.Fatal("typed audit narrowed configured scope")
		}
	}
	for _, g := range cold.Groups {
		if _, err := os.Stat(filepath.Join(cache, g.Key+".json")); !os.IsNotExist(err) {
			t.Fatal("typed audit retained earlier native history")
		}
	}
	c.Workspaces[1].Command.Args = []string{"test", "-v", "-count=1", "./a", "./b"}
	fallback, err := RunWithOptions(ctx, root, c, ExecutionOptions{CacheDir: cache})
	if err != nil || !fallback.Complete || !fallback.Passed || len(fallback.Groups) != 3 {
		t.Fatal("custom invocation failed complete original scope", err)
	}
	g := fallback.Groups[2]
	if g.Name != "second::suite" || !g.Executed || g.Qualified || g.Reused || g.Key != "" {
		t.Fatal("custom original scope acquired qualification")
	}
}

func TestInstalledGoWorkspaceLaterGapRunsOriginalOnceAndRevokesEarlierPass(t *testing.T) {
	config := installedExternalConfig(t, "./a", "./b")
	root, cache := stringHelpersFixture(t), filepath.Join(t.TempDir(), "cache")
	write(t, root, "b/dormant.go", "package sample;var Mutable=1\n")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	w := Workspace{Workspace: model.Workspace{ID: "scope", Root: ".", Adapter: "go", Command: model.Command{Dir: ".", Executable: "go", Args: []string{"test", "-count=1", "./a", "./b"}}, Patterns: []string{"./a", "./b"}}}
	originalConfig := RunConfig{Config: config, Workspaces: []Workspace{w}}
	originalConfig.Packages = nil
	if config.Platform != runtime.GOOS || config.Architecture != runtime.GOARCH {
		report, err := RunWithOptions(ctx, root, originalConfig, ExecutionOptions{CacheDir: cache})
		if err != nil || !report.Complete || !report.Passed || len(report.Groups) != 1 || !report.Groups[0].Executed || report.Groups[0].Qualified || report.Groups[0].Reused || report.Groups[0].Key != "" {
			t.Fatal("foreign original host context did not broaden", err)
		}
		return
	}
	config.workspace, config.deferCleanup = w.ID, true
	var profileErr error
	config.originalContext, profileErr = goOriginalContext(ctx, root, w, originalConfig)
	if profileErr != nil {
		t.Fatal("mandatory typed original context unavailable", profileErr)
	}
	config.verifyOriginal = func(ctx context.Context) error {
		current, err := goOriginalContext(ctx, root, w, originalConfig)
		if err != nil || current != config.originalContext {
			return errors.Join(errors.New("original profile drift"), err)
		}
		return nil
	}
	called := 0
	config.original = func(ctx context.Context, reason string) (Result, error) {
		called++
		return fullWorkspace(ctx, root, w, nil, reason)
	}
	for _, name := range config.Packages {
		externalOriginal(t, ctx, root, name, "pass", "pass")
	}
	for attempt := 0; attempt < 2; attempt++ {
		report, err := GoWithOptions(ctx, root, config, ExecutionOptions{CacheDir: cache})
		if report.finalize == nil {
			t.Fatal("typed provider released owner before coordinator")
		}
		finishErr := report.finalize(ctx)
		if err != nil || finishErr != nil || !report.Complete || !report.Passed || len(report.Groups) != 1 || called != attempt+1 {
			t.Fatal("scope gap did not run original exactly once", err, finishErr, called)
		}
		g := report.Groups[0]
		if g.Name != "scope::suite" || !g.Executed || g.Qualified || g.Reused || g.Key != "" {
			t.Fatal("scope fallback exposed narrowed results")
		}
		entries, err := os.ReadDir(cache)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if filepath.Ext(entry.Name()) == ".json" {
				t.Fatal("earlier narrowed receipt survived complete-scope fallback")
			}
		}
	}
}

func TestInstalledGoHelpersColdWarmTypedDiamond(t *testing.T) {
	config := installedExternalConfig(t, "./a", "./b")
	root, cache := localHelpersFixture(t), filepath.Join(t.TempDir(), "cache")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for _, name := range config.Packages {
		externalOriginal(t, ctx, root, name, "pass", "pass")
	}
	cold, err := Go(ctx, root, config, cache, false)
	keys := []string{}
	for i := range config.Packages {
		group := externalNativeGroup(t, cold, err, config, i, "pass", "pass")
		if !group.Executed || group.Reused {
			t.Fatal("typed helper cold run did not execute")
		}
		keys = append(keys, group.Key)
	}
	warm, err := Go(ctx, root, config, cache, false)
	for i := range config.Packages {
		group := externalNativeGroup(t, warm, err, config, i, "pass", "pass")
		if group.Executed || !group.Reused || group.Key != keys[i] {
			t.Fatal("typed helper warm identity did not reuse")
		}
	}
}

func TestInstalledGoHelpersReachableLeafChangePreservesIndependentReuse(t *testing.T) {
	config := installedExternalConfig(t, "./a", "./b")
	root, cache := localHelpersFixture(t), filepath.Join(t.TempDir(), "cache")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cold, err := Go(ctx, root, config, cache, false)
	for i := range config.Packages {
		externalNativeGroup(t, cold, err, config, i, "pass", "pass")
	}
	write(t, root, "leaf/leaf.go", `package leaf;const Delta=1;func Value()int{return Delta+0}`)
	externalOriginal(t, ctx, root, "./a", "pass", "pass")
	changed, err := Go(ctx, root, config, cache, false)
	a := externalNativeGroup(t, changed, err, config, 0, "pass", "pass")
	b := externalNativeGroup(t, changed, err, config, 1, "pass", "pass")
	if !a.Executed || a.Reused || a.Key == cold.Groups[0].Key || b.Executed || !b.Reused || b.Key != cold.Groups[1].Key {
		t.Fatal("reachable helper edit invalidated the wrong group")
	}
}

func TestInstalledGoHelpersSharedModuleContextInvalidatesBothGroups(t *testing.T) {
	config := installedExternalConfig(t, "./a", "./b")
	root, cache := localHelpersFixture(t), filepath.Join(t.TempDir(), "cache")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cold, err := Go(ctx, root, config, cache, false)
	for i := range config.Packages {
		externalNativeGroup(t, cold, err, config, i, "pass", "pass")
	}
	write(t, root, "go.mod", "module helperboundary\ngo 1.26.0\n")
	changed, err := Go(ctx, root, config, cache, false)
	for i := range config.Packages {
		g := externalNativeGroup(t, changed, err, config, i, "pass", "pass")
		if !g.Executed || g.Reused || g.Key == cold.Groups[i].Key {
			t.Fatal("changed shared module context retained a pass")
		}
	}
}

func TestInstalledGoHelpersAssertionFailureRetryAndNoReceipt(t *testing.T) {
	config := installedExternalConfig(t, "./a")
	root, cache := localHelpersFixture(t), filepath.Join(t.TempDir(), "cache")
	write(t, root, "leaf/leaf.go", `package leaf;const Delta=2;func Value()int{return Delta}`)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	externalOriginal(t, ctx, root, "./a", "fail", "fail")
	failed, err := Go(ctx, root, config, cache, false)
	g := externalNativeGroup(t, failed, err, config, 0, "fail", "fail")
	if !g.Executed || g.Reused {
		t.Fatal("typed helper failure was replaced by reuse")
	}
	if _, err := os.Stat(filepath.Join(cache, g.Key+".json")); !os.IsNotExist(err) {
		t.Fatal("failed helper run minted passing receipt")
	}
	write(t, root, "leaf/leaf.go", `package leaf;const Delta=1;func Value()int{return Delta}`)
	retry, err := Go(ctx, root, config, cache, false)
	g = externalNativeGroup(t, retry, err, config, 0, "pass", "pass")
	if !g.Executed || g.Reused {
		t.Fatal("fixed helper did not execute")
	}
}

func TestInstalledGoHelpersDormantEffectsAndUnsupportedSetupAlwaysBroaden(t *testing.T) {
	config := installedExternalConfig(t, "./a")
	root, cache := localHelpersFixture(t), filepath.Join(t.TempDir(), "cache")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for _, kind := range []string{"clock", "init", "global", "recursive", "loop", "inactive", "assembly"} {
		t.Run(kind, func(t *testing.T) {
			name := "leaf/dormant.go"
			source := "package leaf;"
			switch kind {
			case "clock":
				source += `import "time";func Dormant(){_ = time.Now()}`
			case "init":
				source += `func init(){}`
			case "global":
				source += `var Mutable=1`
			case "recursive":
				source += `func Dormant()int{return Dormant()}`
			case "loop":
				source += `func Dormant()int{x:=0;for i:=0;i<1;i++{x++};return x}`
			case "inactive":
				name = "leaf/dormant_windows.go"
				source += `func Dormant()int{return 1}`
			case "assembly":
				name = "leaf/dormant.s"
				source = "// Harmless dormant assembler input is deliberately unreviewed.\n"
			}
			write(t, root, name, source)
			defer os.Remove(filepath.Join(root, name))
			for attempt := 0; attempt < 2; attempt++ {
				r, err := Go(ctx, root, config, cache, false)
				if err != nil || !r.Complete || !r.Passed || len(r.Groups) != 1 {
					t.Fatal("unsupported helper source failed original scope", err)
				}
				g := r.Groups[0]
				assertExternalOutcomes(t, g, "pass", "pass")
				if !g.Executed || g.Qualified || g.Reused || g.Context != "original-host" || g.Reason == "" {
					t.Fatal("unsupported or dormant helper code acquired authority")
				}
			}
		})
	}
}

func TestInstalledGoHelperIssuerRejectsForgedFactsAndLiveDependencyDrift(t *testing.T) {
	config := installedExternalConfig(t, "./a")
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
	root := localHelpersFixture(t)
	temporary := t.TempDir()
	source := filepath.Join(temporary, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	sourceDigest, err := capture(ctx, root, source)
	if err != nil {
		t.Fatal(err)
	}
	captured, err := resolveGoDependencyClosure(ctx, root, source, temporary, sourceDigest, tool, config)
	if err != nil {
		t.Fatal(err)
	}
	build, err := captureGoBuildForPlatform(ctx, tool, config.Platform, config.Architecture)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := certifyGoDependency(ctx, captured, "./a", build.env)
	if err != nil {
		t.Fatal(err)
	}
	backend := externalBoundBackend(t, ctx, tool, temporary, config, build)
	binary := filepath.Join(temporary, "subject")
	if _, err := runGo(ctx, tool, source, build.env, "test", "-c", "-trimpath", "-buildvcs=false", "-o", binary, "./a"); err != nil {
		t.Fatal(err)
	}
	a := Artifact{Name: "./a", Program: binary, Proof: proof.digest, Cases: slices.Clone(proof.cases), Args: []string{"-test.v", "-test.count=1", "-test.parallel=1", "-test.timeout=60s"}, Env: []string{"GOMAXPROCS=1", "GODEBUG=randautoseed=0", "TZ=UTC"}}
	for _, kind := range []string{"proof", "cases", "environment"} {
		t.Run(kind, func(t *testing.T) {
			bad := a
			badProof := *proof
			switch kind {
			case "proof":
				badProof.digest = "forged"
			case "cases":
				bad.Cases = []string{"TestForged"}
			case "environment":
				badProof.env = []string{"GOARCH=forged"}
			}
			if minted, err := issueGoArtifact(ctx, backend, bad, build, root, source, filepath.Join(source, "a"), &badProof); err == nil || minted.authority != nil {
				t.Fatal("forged helper evidence issued authority")
			}
		})
	}
	issued, err := issueGoArtifact(ctx, backend, a, build, root, source, filepath.Join(source, "a"), proof)
	if err != nil || issued.authority == nil {
		t.Fatal("healthy closed helper issuer unavailable", err)
	}
	write(t, root, "leaf/leaf.go", `package leaf;const Delta=1;func Value()int{return Delta+0}`)
	if err := issued.authority.verify(ctx); err == nil {
		t.Fatal("live helper mutation retained private authority")
	}
	if minted, err := issueGoArtifact(ctx, backend, a, build, root, source, filepath.Join(source, "a"), proof); err == nil || minted.authority != nil {
		t.Fatal("live helper mutation minted new authority")
	}
}
