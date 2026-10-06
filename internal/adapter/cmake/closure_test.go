package cmake

import (
	"context"
	"encoding/xml"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/inputbundle"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
)

func installedRequest(t *testing.T) Request {
	t.Helper()
	cmake, ninja, compiler := os.Getenv("HAYAKU_CMAKE_BIN"), os.Getenv("HAYAKU_NINJA_BIN"), os.Getenv("HAYAKU_CXX_BIN")
	if cmake == "" || ninja == "" || compiler == "" {
		if os.Getenv("HAYAKU_CMAKE_REQUIRED") == "1" {
			t.Fatal("explicit installed CMake/Ninja/compiler acceptance paths required")
		}
		t.Skip("installed CMake/Ninja/compiler acceptance context not supplied")
	}
	ctest := filepath.Join(filepath.Dir(cmake), "ctest")
	environment := []string{"PATH=" + filepath.Dir(ninja) + ":" + filepath.Dir(compiler) + ":/usr/bin:/bin", "LC_ALL=C", "HOME=" + t.TempDir()}
	if runtime.GOOS == "darwin" {
		environment = append(environment, "TMPDIR="+t.TempDir())
	}
	return Request{Source: t.TempDir(), Revision: "installed-fixture", CMake: cmake, Ninja: ninja, Compiler: compiler, CCompiler: os.Getenv("HAYAKU_CC_BIN"), CTest: ctest, Environment: environment, Definitions: []string{"CMAKE_BUILD_TYPE=Debug", "CMAKE_CXX_STANDARD=17"}}
}

func sourceFile(t *testing.T, root, name, data string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func built(t *testing.T, request Request) *Closure {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	c, err := Build(ctx, request)
	if err != nil {
		t.Fatalf("installed configured build failed: %v", err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("private build cleanup: %v", err)
		}
	})
	return c
}

func executableID(t *testing.T, c *Closure) string {
	t.Helper()
	api, err := c.Observation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range api.Targets {
		if target.Name == "suite" && target.Kind == "EXECUTABLE" {
			return target.ID
		}
	}
	t.Fatal("installed suite executable missing")
	return ""
}

func original(t *testing.T, c *Closure, exe string, args []string) process.Output {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	out, err := process.RunEnvironmentLimits(ctx, model.Command{Executable: exe, Dir: c.session.Build(), Args: args}, c.session.Build(), c.request.Environment, 32<<20, 1<<20)
	if !out.Completed || (out.ExitCode == 0 && err != nil) || (out.ExitCode != 0 && err == nil) {
		t.Fatalf("original configured command did not finish: exit=%d completed=%t error=%v", out.ExitCode, out.Completed, err)
	}
	return out
}

func catchCaseOutcomes(t *testing.T, data []byte, want map[string]bool) {
	t.Helper()
	var root struct {
		XMLName xml.Name
		Cases   []struct {
			Name   string `xml:"name,attr"`
			Result struct {
				Success *bool `xml:"success,attr"`
			} `xml:"OverallResult"`
		} `xml:"TestCase"`
	}
	if err := xml.Unmarshal(data, &root); err != nil || root.XMLName.Local != "Catch2TestRun" || len(root.Cases) != len(want) {
		t.Fatalf("native Catch2 outcome structure missing: %v", err)
	}
	seen := map[string]bool{}
	for _, item := range root.Cases {
		expected, ok := want[item.Name]
		if !ok || seen[item.Name] || item.Result.Success == nil || *item.Result.Success != expected {
			t.Fatalf("native Catch2 case outcome contradiction: %s", item.Name)
		}
		seen[item.Name] = true
	}
}

func TestInstalledGoogleTestInventoryAndOriginalFault(t *testing.T) {
	request := installedRequest(t)
	framework := os.Getenv("HAYAKU_GTEST_SOURCE")
	if framework == "" {
		if os.Getenv("HAYAKU_CMAKE_REQUIRED") == "1" {
			t.Fatal("verified GoogleTest source required")
		}
		t.Skip("GoogleTest source not supplied")
	}
	request.Inputs = []inputbundle.Request{{Roots: []inputbundle.Root{{Name: "gtest", Path: framework, Mount: "gtest"}}, Inputs: []inputbundle.Input{{Root: "gtest", Path: "."}}}}
	cmakelists := "cmake_minimum_required(VERSION 3.20)\nproject(Observed LANGUAGES CXX)\nset(INSTALL_GTEST OFF CACHE BOOL \"\" FORCE)\nadd_subdirectory(\"" + filepath.ToSlash(framework) + "\" \"${CMAKE_BINARY_DIR}/framework\" EXCLUDE_FROM_ALL)\nadd_executable(suite suite.cpp)\ntarget_link_libraries(suite PRIVATE GTest::gtest_main)\n"
	sourceFile(t, request.Source, "CMakeLists.txt", cmakelists)
	sourceFile(t, request.Source, "value.hpp", "inline int value(){return 7;}\n")
	sourceFile(t, request.Source, "suite.cpp", `#include <gtest/gtest.h>
#include "value.hpp"
TEST(Alpha, Works) { EXPECT_EQ(value(), 7); }
TEST(Beta, Works) { EXPECT_EQ(7, 7); }
TEST(Alpha, DISABLED_Skipped) { EXPECT_EQ(7, 7); }
template<typename T> class Typed : public ::testing::Test {};
using NativeTypes = ::testing::Types<int, double>;
TYPED_TEST_SUITE(Typed, NativeTypes);
TYPED_TEST(Typed, Value) { EXPECT_EQ(7, 7); }
class Params : public ::testing::TestWithParam<int> {};
TEST_P(Params, Value) { EXPECT_GT(GetParam(), 0); }
INSTANTIATE_TEST_SUITE_P(Inst, Params, ::testing::Values(1, 2));
`)
	healthy := built(t, request)
	id := executableID(t, healthy)
	inventory, err := healthy.GoogleTest(t.Context(), id, nil)
	if err != nil || len(inventory.Execution.Cases) != 7 {
		t.Fatalf("installed typed/value/disabled inventory missing: cases=%d error=%v", len(inventory.Execution.Cases), err)
	}
	filtered, err := healthy.GoogleTest(t.Context(), id, []string{"--gtest_filter=Alpha.*"})
	if err != nil || len(filtered.Execution.Cases) != 2 || filtered.ContextDigest == inventory.ContextDigest {
		t.Fatalf("actual GoogleTest filter context missing: cases=%d error=%v", len(filtered.Execution.Cases), err)
	}
	exe, err := healthy.executable(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	out, err := healthy.session.Run(t.Context(), model.Command{Executable: exe, Dir: "{build}"})
	if err != nil || !out.Completed || out.ExitCode != 0 || !strings.Contains(string(out.Stdout), "[  PASSED  ] 6 tests.") {
		t.Fatalf("original binary healthy control failed: exit=%d completed=%t error=%v", out.ExitCode, out.Completed, err)
	}
	if err = healthy.Close(); err != nil {
		t.Fatal(err)
	}
	sourceFile(t, request.Source, "value.hpp", "inline int value(){return 8;}\n")
	fault := built(t, request)
	id = executableID(t, fault)
	exe, err = fault.executable(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	out = original(t, fault, exe, nil)
	if out.ExitCode != 1 || !strings.Contains(string(out.Stdout), "[  FAILED  ] Alpha.Works") || !strings.Contains(string(out.Stdout), "[       OK ] Beta.Works") {
		t.Fatalf("original Alpha fault/healthy Beta lost: exit=%d completed=%t", out.ExitCode, out.Completed)
	}
	out, err = fault.session.Run(t.Context(), model.Command{Executable: exe, Dir: "{build}"})
	if err == nil || out.Completed || out.ExitCode != 1 || !strings.Contains(string(out.Stdout), "[  FAILED  ] Alpha.Works") {
		t.Fatalf("guarded stage accepted original runtime failure: exit=%d completed=%t error=%v", out.ExitCode, out.Completed, err)
	}
}

func TestInstalledCatch2InventorySectionsGeneratorsAndHidden(t *testing.T) {
	request := installedRequest(t)
	framework := os.Getenv("HAYAKU_CATCH_SOURCE")
	if framework == "" {
		if os.Getenv("HAYAKU_CMAKE_REQUIRED") == "1" {
			t.Fatal("verified Catch2 source required")
		}
		t.Skip("Catch2 source not supplied")
	}
	request.Inputs = []inputbundle.Request{{Roots: []inputbundle.Root{{Name: "catch", Path: framework, Mount: "catch"}}, Inputs: []inputbundle.Input{{Root: "catch", Path: "."}}}}
	sourceFile(t, request.Source, "CMakeLists.txt", "cmake_minimum_required(VERSION 3.20)\nproject(Observed LANGUAGES CXX)\nset(CATCH_BUILD_TESTING OFF CACHE BOOL \"\" FORCE)\nadd_subdirectory(\""+filepath.ToSlash(framework)+"\" \"${CMAKE_BINARY_DIR}/framework\" EXCLUDE_FROM_ALL)\nadd_executable(suite suite.cpp)\ntarget_link_libraries(suite PRIVATE Catch2::Catch2WithMain)\n")
	sourceFile(t, request.Source, "value.hpp", "inline int value(){return 7;}\n")
	sourceFile(t, request.Source, "suite.cpp", `#include <catch2/catch_test_macros.hpp>
#include <catch2/generators/catch_generators.hpp>
#include "value.hpp"
TEST_CASE("Alpha", "[public]") { SECTION("left") { REQUIRE(value() == 7); } SECTION("right") { REQUIRE(8 == 8); } }
TEST_CASE("Beta", "[data]") { auto v = GENERATE(1, 2); REQUIRE(v > 0); }
TEST_CASE("Hidden", "[.]") { REQUIRE(7 == 7); }
`)
	c := built(t, request)
	id := executableID(t, c)
	ordinary, err := c.Catch2(t.Context(), id, nil, "decl", "7")
	if err != nil || len(ordinary.Execution.Cases) != 2 || len(ordinary.Gaps) < 2 {
		t.Fatalf("registered case/section/generator boundary lost: cases=%d error=%v", len(ordinary.Execution.Cases), err)
	}
	all, err := c.Catch2(t.Context(), id, []string{"*"}, "lex", "8")
	if err != nil || len(all.Execution.Cases) != 3 || all.ContextDigest == ordinary.ContextDigest {
		t.Fatalf("hidden/spec/order/seed context missing: cases=%d error=%v", len(all.Execution.Cases), err)
	}
	exe, err := c.executable(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"--reporter", "xml", "--order", "decl", "--rng-seed", "7"}
	out := original(t, c, exe, args)
	catchCaseOutcomes(t, out.Stdout, map[string]bool{"Alpha": true, "Beta": true})
	if out.ExitCode != 0 || !strings.Contains(string(out.Stdout), `<TestCase name="Alpha"`) || !strings.Contains(string(out.Stdout), `<TestCase name="Beta"`) || !strings.Contains(string(out.Stdout), `<OverallResultsCases successes="2" failures="0"`) {
		t.Fatalf("original Catch2 section/generator positive failed: exit=%d completed=%t", out.ExitCode, out.Completed)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	sourceFile(t, request.Source, "value.hpp", "inline int value(){return 8;}\n")
	fault := built(t, request)
	id = executableID(t, fault)
	exe, err = fault.executable(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	out = original(t, fault, exe, args)
	catchCaseOutcomes(t, out.Stdout, map[string]bool{"Alpha": false, "Beta": true})
	// Catch2 3.9.1 catch_session.hpp defines TestFailureExitCode as 42.
	if out.ExitCode != 42 || !strings.Contains(string(out.Stdout), `<TestCase name="Alpha"`) || !strings.Contains(string(out.Stdout), `<TestCase name="Beta"`) || !strings.Contains(string(out.Stdout), `<OverallResultsCases successes="1" failures="1"`) {
		t.Fatalf("original Catch2 Alpha fault/Beta companion lost: exit=%d completed=%t", out.ExitCode, out.Completed)
	}
}

func TestInstalledCTestInventoryRetainsFixturesAndProperties(t *testing.T) {
	request := installedRequest(t)
	sourceFile(t, request.Source, "CMakeLists.txt", `cmake_minimum_required(VERSION 3.20)
project(Observed LANGUAGES CXX)
enable_testing()
add_executable(suite suite.cpp)
add_test(NAME Setup COMMAND suite setup)
set_tests_properties(Setup PROPERTIES FIXTURES_SETUP shared)
add_test(NAME Alpha COMMAND suite alpha)
set_tests_properties(Alpha PROPERTIES FIXTURES_REQUIRED shared ENVIRONMENT "FIXTURE_MODE=healthy" WORKING_DIRECTORY "${CMAKE_BINARY_DIR}" TIMEOUT 5)
add_test(NAME Beta COMMAND suite beta)
add_test(NAME ExpectedFailure COMMAND suite fail)
set_tests_properties(ExpectedFailure PROPERTIES WILL_FAIL TRUE)
add_test(NAME Skipped COMMAND suite skip)
set_tests_properties(Skipped PROPERTIES SKIP_RETURN_CODE 77)
`)
	program := `#include <string>
#include <fstream>
#include <cstdlib>
int main(int argc,char**argv){
if(argc<2)return 2;std::string v=argv[1];
if(v=="setup"){std::ofstream("shared.ready") << "ready";return 0;}
if(v=="alpha"){const char* env=std::getenv("FIXTURE_MODE");if(!env||std::string(env)!="healthy"||!std::ifstream("shared.ready"))return 3;return ALPHA_RESULT;}
if(v=="fail")return 1;if(v=="skip")return 77;return 0;}
`
	sourceFile(t, request.Source, "suite.cpp", strings.ReplaceAll(program, "ALPHA_RESULT", "0"))
	c := built(t, request)
	all, err := c.CTest(t.Context(), nil)
	if err != nil || len(all.Execution.Units) != 5 {
		t.Fatalf("CTest inventory/property context missing: cases=%d error=%v", len(all.Execution.Cases), err)
	}
	selected, err := c.CTest(t.Context(), []string{"-R", "^Alpha$"})
	if err != nil || len(selected.Execution.Units) != 2 || selected.ContextDigest == all.ContextDigest {
		t.Fatalf("CTest fixture inclusion missing: cases=%d error=%v", len(selected.Execution.Cases), err)
	}
	out, err := c.session.RunProducer(t.Context(), "ctest-original", model.Command{Executable: request.CTest, Dir: "{build}", Args: []string{"--output-on-failure"}})
	if err != nil || !out.Completed || out.ExitCode != 0 || !strings.Contains(string(out.Stdout), "100% tests passed") || !strings.Contains(string(out.Stdout), "Skipped") {
		t.Fatalf("configured CTest lifecycle control failed: exit=%d completed=%t error=%v", out.ExitCode, out.Completed, err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	sourceFile(t, request.Source, "suite.cpp", strings.ReplaceAll(program, "ALPHA_RESULT", "1"))
	fault := built(t, request)
	// A failed guarded producer stays unusable even when the original runner
	// completed and reported a meaningful native failure.
	guarded, guardErr := fault.session.RunProducer(t.Context(), "ctest-fault", model.Command{Executable: request.CTest, Dir: "{build}", Args: []string{"--output-on-failure"}})
	if guardErr == nil || guarded.Completed || guarded.ExitCode != 8 {
		t.Fatalf("guarded CTest fault admitted: exit=%d completed=%t error=%v", guarded.ExitCode, guarded.Completed, guardErr)
	}
	out = original(t, fault, request.CTest, []string{"--output-on-failure"})
	text := string(out.Stdout)
	alphaFailed := regexp.MustCompile(`(?m)^\s*[0-9]+/[0-9]+ Test #[0-9]+: Alpha[^\n]*\*\*\*Failed`).MatchString(text)
	betaPassed := regexp.MustCompile(`(?m)^\s*[0-9]+/[0-9]+ Test #[0-9]+: Beta[^\n]*Passed`).MatchString(text)
	if out.ExitCode != 8 || !strings.Contains(text, "80% tests passed, 1 tests failed out of 5") || !alphaFailed || !betaPassed || !strings.Contains(text, "Skipped") {
		t.Fatalf("original CTest Alpha fault/Beta/lifecycle companion lost: exit=%d completed=%t", out.ExitCode, out.Completed)
	}
}

func TestInstalledCTestShowOnlyProducesPrivateMetadata(t *testing.T) {
	request := installedRequest(t)
	sourceFile(t, request.Source, "CMakeLists.txt", "cmake_minimum_required(VERSION 3.20)\nproject(Observed LANGUAGES CXX)\nenable_testing()\nadd_executable(suite suite.cpp)\nadd_test(NAME Alpha COMMAND suite)\n")
	sourceFile(t, request.Source, "suite.cpp", "int main(){return 0;}\n")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	c, err := Build(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	before, err := c.session.Manifest(t.Context(), "build")
	if err != nil {
		t.Fatal(err)
	}
	out := original(t, c, request.CTest, []string{"--show-only=json-v1"})
	if out.ExitCode != 0 {
		t.Fatalf("original show-only failed: exit=%d completed=%t", out.ExitCode, out.Completed)
	}
	after, err := inputbundle.Capture(t.Context(), inputbundle.Request{Roots: []inputbundle.Root{{Name: "input", Path: c.session.Build(), Mount: "."}}, Inputs: []inputbundle.Input{{Root: "input", Path: "."}}})
	if err != nil {
		t.Fatal(err)
	}
	prior := map[string]inputbundle.Entry{}
	for _, entry := range before.Entries {
		prior[entry.Path] = entry
	}
	changed := 0
	for _, entry := range after.Manifest().Entries {
		old, ok := prior[entry.Path]
		if !ok || old != entry {
			changed++
			t.Logf("native show-only produced metadata: %s (%s)", entry.Path, entry.Kind)
		}
	}
	if changed == 0 {
		t.Fatal("native show-only unexpectedly produced no private metadata")
	}
	if _, _, err = c.session.Records(t.Context()); err == nil {
		t.Fatal("undeclared show-only output escaped sealed build guard")
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
}
