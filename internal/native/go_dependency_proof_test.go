package native

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Cyberlane/hayaku/internal/adapter/golang"
	"github.com/Cyberlane/hayaku/internal/passcache"
)

func TestGoDependencyAMD64NeverLaunchesToolAndPreservesLegacyPureAdmission(t *testing.T) {
	root, temporary := t.TempDir(), t.TempDir()
	marker, tool := filepath.Join(temporary, "trace"), filepath.Join(temporary, "go")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nprintf unexpected > \"$HAYAKU_AMD64_DEPENDENCY_MARKER\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAYAKU_AMD64_DEPENDENCY_MARKER", marker)
	write(t, root, "go.mod", "module architecturecontrol\ngo 1.26\n")
	pure := []byte("package sample;import \"testing\";func TestPure(t *testing.T){if 1!=1{t.Fatal(\"wrong\")}}")
	if cases, err := qualifyGo(map[string][]byte{"pure_test.go": pure}); err != nil || len(cases) != 1 || cases[0] != "TestPure" {
		t.Fatal("preexisting packages-only pure proof changed", cases, err)
	}
	for _, source := range []string{
		"package sample;import(\"testing\";subject \"architecturecontrol/leaf\");func TestValue(t *testing.T){if subject.Value()!=3{t.Fatal(\"wrong\")}}",
		"package sample\nimport(\"testing\";_ \"embed\")\n//go:embed data.txt\nvar Fixture string\nfunc TestValue(t *testing.T){if Fixture!=\"abc\"{t.Fatal(\"wrong\")}}\n",
	} {
		files := map[string][]byte{"subject_test.go": []byte(source)}
		if _, err := qualifyGo(files); err == nil || !dependencyImportCandidate(files) {
			t.Fatal("fixture missed shared new helper/setup proof path", err)
		}
		write(t, root, "a/subject_test.go", source)
		config := Config{Schema: 1, Platform: "linux", Architecture: "amd64", Packages: []string{"./a"}}
		captured, err := resolveGoDependencyClosure(context.Background(), root, root, temporary, "", tool, config)
		if captured != nil || err == nil || !strings.Contains(err.Error(), "ARM64") {
			t.Fatal("AMD64 dependency capture passed release admission", captured, err)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("unreviewed AMD64 dependency attempted compiler work", err)
		}
	}
}

func TestGoDependencyIssuerRejectsUnreviewedArchitectureAndBuildContext(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a/pure_test.go", "package sample;import \"testing\";func TestPure(t *testing.T){if 1!=1{t.Fatal(\"wrong\")}}")
	artifact := Artifact{Name: "./a", Args: []string{"-test.v", "-test.count=1", "-test.parallel=1", "-test.timeout=60s"}, Env: []string{"GOMAXPROCS=1", "GODEBUG=randautoseed=0", "TZ=UTC"}}
	for _, target := range []struct{ backend, build string }{{"amd64", "amd64"}, {"amd64", "arm64"}, {"arm64", "amd64"}} {
		build := goBuildContext{env: buildEnvironmentForPlatform("linux", target.build)}
		proof := &goDependencyProof{closure: &GoClosure{root: root, source: root}, name: "./a", env: build.env}
		minted, err := issueGoArtifact(context.Background(), Backend{Platform: "linux", Architecture: target.backend}, artifact, build, root, root, filepath.Join(root, "a"), proof)
		if err == nil || minted.authority != nil || !strings.Contains(err.Error(), "producer context mismatch") {
			t.Fatal("unreviewed dependency architecture reached recertification", target, err)
		}
	}
}

func TestDependencyInputAdmissionRequiresClosedSelectedOwners(t *testing.T) {
	selected := map[string]golang.ClosurePackage{"local/helper": {ID: "native-go:local/helper"}}
	for _, kind := range []string{"active-go", "test-go", "external-test-go", "ignored-go", "module"} {
		if err := validateDependencyInputs([]golang.ClosureInput{{Owner: "native-go:local/helper", Kind: kind}}, selected); err != nil {
			t.Fatal("reviewed selected Go input rejected", err)
		}
	}
	for _, kind := range []string{"native", "native-go", "ignored-other", "embed", "test-embed", "external-test-embed", "replacement-module", "unknown"} {
		if err := validateDependencyInputs([]golang.ClosureInput{{Owner: "native-go:local/helper", Kind: kind}}, selected); err == nil {
			t.Fatal("unreviewed selected compiler input accepted", kind)
		}
		if err := validateDependencyInputs([]golang.ClosureInput{{Owner: "native-go:independent/helper", Kind: kind}}, selected); err != nil {
			t.Fatal("unrelated package input changed selected closure", err)
		}
	}
}

func TestDependencySourceTokenBoundsAndCancellation(t *testing.T) {
	if err := boundedHelperTokens(context.Background(), []byte(`package helper;func Value()int{return 1}`)); err != nil {
		t.Fatal(err)
	}
	if err := boundedHelperTokens(context.Background(), []byte(strings.Repeat(";", 100001))); err == nil {
		t.Fatal("oversized token stream accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := boundedHelperTokens(ctx, []byte("package helper")); err != context.Canceled {
		t.Fatal("token scanner lost cancellation", err)
	}
}

func TestDependencyCallBudgetCountsMultiplicityAndRejectsCycles(t *testing.T) {
	valid := map[string]*dependencyFunction{"leaf": {cost: 5}, "left": {cost: 3, calls: []string{"leaf"}}, "right": {cost: 3, calls: []string{"leaf"}}, "root": {cost: 2, calls: []string{"left", "right"}}}
	if err := boundedDependencyCalls(valid); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"direct", "indirect", "expanded-diamond", "missing"} {
		t.Run(kind, func(t *testing.T) {
			functions := map[string]*dependencyFunction{}
			switch kind {
			case "direct":
				functions["f"] = &dependencyFunction{cost: 1, calls: []string{"f"}}
			case "indirect":
				functions["f"] = &dependencyFunction{cost: 1, calls: []string{"g"}}
				functions["g"] = &dependencyFunction{cost: 1, calls: []string{"f"}}
			case "missing":
				functions["f"] = &dependencyFunction{cost: 1, calls: []string{"absent"}}
			case "expanded-diamond":
				functions["0"] = &dependencyFunction{cost: 1}
				for i := 1; i <= 18; i++ {
					previous := strconv.Itoa(i - 1)
					functions[strconv.Itoa(i)] = &dependencyFunction{cost: 1, calls: []string{previous, previous}}
				}
			}
			if err := boundedDependencyCalls(functions); err == nil {
				t.Fatal("unbounded helper work accepted")
			}
		})
	}
}

func TestGoBuildCleanupFailureRejectsAggregateAndRevokesOwnReceipts(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission denial requires an unprivileged host")
	}
	temporary := filepath.Join(t.TempDir(), "build")
	blocked := filepath.Join(temporary, "blocked")
	if err := os.MkdirAll(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "file"), []byte("cleanup control"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(blocked, 0700)
	cacheDir := filepath.Join(t.TempDir(), "cache")
	key, foreignKey := strings.Repeat("a", 64), strings.Repeat("b", 64)
	for _, item := range []struct{ domain, key string }{{"hayaku-native-pure-pass-v1", key}, {"foreign-domain", foreignKey}} {
		cache, err := passcache.OpenWithAuthority(cacheDir, item.domain, nil)
		if err != nil {
			t.Fatal(err)
		}
		// Synthetic authenticated cleanup controls do not establish native proof.
		if err := cache.Store(context.Background(), item.key, true); err != nil {
			t.Fatal(err)
		}
		if err := cache.Close(); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.ReadFile(filepath.Join(cacheDir, foreignKey+".json"))
	if err != nil {
		t.Fatal(err)
	}
	report := Report{Complete: true, Passed: true, Groups: []Result{{Key: key}}}
	closure := &GoClosure{source: temporary, temporary: temporary}
	var returned error
	finishGoBuild(temporary, ExecutionOptions{CacheDir: cacheDir}, &report, &returned, closure)
	if returned == nil || report.Complete || report.Passed || !closure.closed || closure.Directory() != "" {
		t.Fatal("failed cleanup retained successful aggregate or open capture")
	}
	if _, err := os.Stat(filepath.Join(cacheDir, key+".json")); !os.IsNotExist(err) {
		t.Fatal("failed cleanup retained own pass")
	}
	after, err := os.ReadFile(filepath.Join(cacheDir, foreignKey+".json"))
	if err != nil || string(after) != string(before) {
		t.Fatal("cleanup changed foreign-domain pass")
	}
}

func TestDependencyImportCandidateRetainsOriginalPureProfile(t *testing.T) {
	if dependencyImportCandidate(map[string][]byte{"x_test.go": []byte(`package x;import "testing";func TestX(t *testing.T){}`)}) {
		t.Fatal("pure profile needlessly invoked dependency collector")
	}
	if !dependencyImportCandidate(map[string][]byte{"x_test.go": []byte(`package x;import("testing";"example/helper");func TestX(t *testing.T){}`)}) {
		t.Fatal("local import collector skipped")
	}
}
