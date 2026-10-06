package cmake

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/model"
)

// These are installed offline build/inventory controls, not runtime-influence
// qualification. No framework source, new tool, or package fetch is required.
func adversarialRequest(t *testing.T) Request {
	t.Helper()
	r := installedRequest(t)
	sourceFile(t, r.Source, "settings.cmake", "set(GENERATED_VALUE 7)\n")
	sourceFile(t, r.Source, "generated.hpp.in", "#define GENERATED_VALUE @GENERATED_VALUE@\n")
	sourceFile(t, r.Source, "value.hpp", "inline int observed_value(){return 7;}\n")
	sourceFile(t, r.Source, "CMakeLists.txt", `cmake_minimum_required(VERSION 3.20)
project(Adversarial LANGUAGES CXX)
include(settings.cmake)
configure_file(generated.hpp.in generated.hpp @ONLY)
enable_testing()
add_executable(suite suite.cpp "${CMAKE_BINARY_DIR}/generated.hpp")
target_include_directories(suite PRIVATE "${CMAKE_BINARY_DIR}")
add_test(NAME Alpha COMMAND suite alpha)
add_test(NAME Beta COMMAND suite beta)
`)
	sourceFile(t, r.Source, "suite.cpp", `#include "value.hpp"
#include "generated.hpp"
#include <cstring>
int main(int argc,char **argv){
 if(argc!=2)return 2;
 if(std::strcmp(argv[1],"alpha")==0)return observed_value()==7&&GENERATED_VALUE==7?0:9;
 if(std::strcmp(argv[1],"beta")==0)return 0;
 return 2;
}
`)
	return r
}

func adversarialBuilt(t *testing.T, r Request) *Closure {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	c, err := Build(ctx, r)
	if err != nil {
		t.Fatalf("installed adversarial build failed: %v", err)
	}
	source, build := c.session.Source(), c.session.Build()
	// The foreign original source stays outside the owned private cleanup tree.
	foreign, err := os.ReadFile(filepath.Join(r.Source, "suite.cpp"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// A fault may remain visible to Close. Cleanup must still remove both
		// owned views; acceptance cannot be inferred from Close's error alone.
		if err := c.Close(); err != nil {
			t.Logf("rejected generation also rejected at cleanup: %v", err)
		}
		for _, name := range []string{source, build, filepath.Dir(build)} {
			if _, err := os.Lstat(name); !os.IsNotExist(err) {
				t.Errorf("owned build view survived cleanup: %v", err)
			}
		}
		current, err := os.ReadFile(filepath.Join(r.Source, "suite.cpp"))
		if err != nil || !bytes.Equal(current, foreign) {
			t.Errorf("private cleanup changed foreign source: %v", err)
		}
	})
	return c
}

func adversarialHealthy(t *testing.T, c *Closure) (FileAPI, string) {
	t.Helper()
	api, err := c.Observation(t.Context())
	if err != nil || api.Digest == "" || !slices.Contains(api.HeaderInputs, "source/value.hpp") || !slices.Contains(api.HeaderInputs, "build/generated.hpp") {
		t.Fatalf("actual header observation unavailable: digest=%t headers=%d error=%v", api.Digest != "", len(api.HeaderInputs), err)
	}
	id := executableID(t, c)
	listed, err := c.CTest(t.Context(), nil)
	if err != nil || listed.ContextDigest == "" || len(listed.Execution.Units) != 2 || len(listed.Execution.Cases) != 2 {
		t.Fatalf("actual healthy CTest inventory unavailable: units=%d cases=%d error=%v", len(listed.Execution.Units), len(listed.Execution.Cases), err)
	}
	names := []string{listed.Execution.Units[0].ID, listed.Execution.Units[1].ID}
	if !slices.Contains(names, "ctest:Alpha") || !slices.Contains(names, "ctest:Beta") {
		t.Fatal("actual CTest inventory contradicted independently authored Alpha/Beta")
	}
	for _, item := range listed.Execution.Cases {
		if (item.ID != "Alpha" && item.ID != "Beta") || item.Unit != "ctest:"+item.ID {
			t.Fatal("actual CTest registered case contradicted its authored executable scope")
		}
	}
	for _, selector := range []string{"alpha", "beta"} {
		exe, err := c.executable(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		out := original(t, c, exe, []string{selector})
		if out.ExitCode != 0 {
			t.Fatalf("original healthy %s did not pass: exit=%d", selector, out.ExitCode)
		}
	}
	return api, id
}

// Overwrite a known local fixture in place, then prove that only bytes changed.
// This keeps inode/full mode/size/mtime controls independent of the collector.
func adversarialSameMetadataBytes(t *testing.T, name string) {
	t.Helper()
	before, err := os.Lstat(name)
	if err != nil || !before.Mode().IsRegular() {
		t.Fatalf("regular drift fixture unavailable: %v", err)
	}
	original, err := os.ReadFile(name)
	if err != nil || len(original) == 0 {
		t.Fatalf("nonempty drift fixture unavailable: %v", err)
	}
	changed := append([]byte(nil), original...)
	changed[len(changed)-1] ^= 1
	if err := os.Chmod(name, before.Mode().Perm()|0200); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, changed, before.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(name, before.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(name, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := os.Lstat(name)
	actual, readErr := os.ReadFile(name)
	if err != nil || readErr != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || !bytes.Equal(actual, changed) || bytes.Equal(actual, original) {
		t.Fatalf("independent same-metadata byte fault was not established: stat=%v read=%v", err, readErr)
	}
}

func adversarialRejectInventoryAndObservation(t *testing.T, c *Closure) {
	t.Helper()
	// Invoke the inventory entry first. A failed prior Observation must not
	// supply the only reason the native inventory gets rejected.
	inventory, err := c.CTest(t.Context(), nil)
	if err == nil || inventory.ContextDigest != "" || len(inventory.Execution.Units) != 0 || len(inventory.Execution.Cases) != 0 {
		t.Fatalf("drift retained a native inventory: units=%d cases=%d error=%v", len(inventory.Execution.Units), len(inventory.Execution.Cases), err)
	}
	api, err := c.Observation(t.Context())
	if err == nil || api.Digest != "" || len(api.Targets) != 0 || len(api.HeaderInputs) != 0 {
		t.Fatalf("rejected inventory retained a stale observation: targets=%d headers=%d error=%v", len(api.Targets), len(api.HeaderInputs), err)
	}
}

func TestInstalledCMakeAdversarialConsumedBytesDrift(t *testing.T) {
	for _, fault := range []struct{ area, name string }{
		{"original", "value.hpp"}, {"original", "settings.cmake"},
		{"copy", "value.hpp"}, {"copy", "suite.cpp"}, {"copy", "settings.cmake"},
		{"build", "generated.hpp"}, {"build", "build.ninja"}, {"artifact", "suite"},
	} {
		t.Run(fault.area+"-"+fault.name, func(t *testing.T) {
			r := adversarialRequest(t)
			c := adversarialBuilt(t, r)
			_, id := adversarialHealthy(t, c)
			root := r.Source
			if fault.area == "copy" {
				root = c.session.Source()
			} else if fault.area == "build" || fault.area == "artifact" {
				root = c.session.Build()
			}
			name := filepath.Join(root, fault.name)
			if fault.area == "artifact" {
				var err error
				name, err = c.executable(t.Context(), id)
				if err != nil {
					t.Fatal(err)
				}
			}
			adversarialSameMetadataBytes(t, name)
			adversarialRejectInventoryAndObservation(t, c)
		})
	}
}

func TestInstalledCMakeAdversarialFreshRebuildPreservesNativeFault(t *testing.T) {
	r := adversarialRequest(t)
	healthy := built(t, r)
	adversarialHealthy(t, healthy)
	out, err := healthy.session.RunProducer(t.Context(), "original-healthy-ctest", model.Command{Executable: r.CTest, Dir: "{build}", Args: []string{"--output-on-failure"}})
	text := string(out.Stdout)
	alpha := regexp.MustCompile(`(?m)^\s*1/2 Test #1: Alpha[^\n]*Passed`).MatchString(text)
	beta := regexp.MustCompile(`(?m)^\s*2/2 Test #2: Beta[^\n]*Passed`).MatchString(text)
	// Installed CTest 4.4.3 omits the older zero-failure clause. Both exact
	// summary forms retain the count; independent native rows must both pass.
	summary := regexp.MustCompile(`(?m)^100% tests passed(?:, 0 tests failed)? out of 2\r?$`).MatchString(text)
	if err != nil || !out.Completed || out.ExitCode != 0 || !alpha || !beta || !summary {
		t.Fatalf("original healthy CTest control failed: completed=%t exit=%d error=%v", out.Completed, out.ExitCode, err)
	}
	if err := healthy.Close(); err != nil {
		t.Fatal(err)
	}
	sourceFile(t, r.Source, "value.hpp", "inline int observed_value(){return 8;}\n")
	fault := built(t, r)
	inventory, err := fault.CTest(t.Context(), nil)
	if err != nil || len(inventory.Execution.Units) != 2 {
		t.Fatalf("fresh fault build lost actual inventory: units=%d error=%v", len(inventory.Execution.Units), err)
	}
	guarded, err := fault.session.RunProducer(t.Context(), "original-fault-ctest", model.Command{Executable: r.CTest, Dir: "{build}", Args: []string{"--output-on-failure"}})
	if err == nil || guarded.Completed || guarded.ExitCode != 8 {
		t.Fatalf("guarded native test failure admitted: completed=%t exit=%d error=%v", guarded.Completed, guarded.ExitCode, err)
	}
	out = original(t, fault, r.CTest, []string{"--output-on-failure"})
	text = string(out.Stdout)
	alpha = regexp.MustCompile(`(?m)^\s*1/2 Test #1: Alpha[^\n]*\*\*\*Failed`).MatchString(text)
	beta = regexp.MustCompile(`(?m)^\s*2/2 Test #2: Beta[^\n]*Passed`).MatchString(text)
	if out.ExitCode != 8 || !alpha || !beta || !strings.Contains(text, "50% tests passed, 1 tests failed out of 2") {
		t.Fatalf("fresh original CTest did not preserve Alpha failure/Beta success: exit=%d alpha=%t beta=%t", out.ExitCode, alpha, beta)
	}
	// A real guarded failure precedes the independent original native command;
	// neither command may leave its prior configured generation consumable.
	if api, err := fault.Observation(t.Context()); err == nil || api.Digest != "" {
		t.Fatalf("failing original run retained an observation: %v", err)
	}
}

func TestInstalledCMakeAdversarialCompilerAliasRetarget(t *testing.T) {
	r := adversarialRequest(t)
	compiler, err := filepath.EvalSymlinks(r.Compiler)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), filepath.Base(r.Compiler))
	if err := os.Symlink(compiler, alias); err != nil {
		t.Fatal(err)
	}
	r.Compiler = alias
	c := adversarialBuilt(t, r)
	adversarialHealthy(t, c)
	before, err := os.Stat(compiler)
	if err != nil {
		t.Fatal(err)
	}
	ninja, err := filepath.EvalSymlinks(r.Ninja)
	if err != nil || ninja == compiler {
		t.Fatalf("distinct already-bound alias target unavailable: %v", err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ninja, alias); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(alias)
	after, statErr := os.Stat(compiler)
	if err != nil || statErr != nil || resolved != ninja || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("alias control did not preserve the original installed compiler: resolve=%v stat=%v", err, statErr)
	}
	adversarialRejectInventoryAndObservation(t, c)
}
