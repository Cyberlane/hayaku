package cmake

import (
	"reflect"
	"testing"
)

func TestNativeRegisteredInventoriesPreserveCaseIdentities(t *testing.T) {
	gtest := []byte("Running main() from /private/gtest_main.cc\nAlpha.\n  Works\n  DISABLED_Skipped\nTyped/0.  # TypeParam = int\n  Value\nInst/Values.\n  Case/0  # GetParam() = 7\n")
	names, err := googleNames(gtest)
	want := []string{"Alpha.Works", "Alpha.DISABLED_Skipped", "Typed/0.Value", "Inst/Values.Case/0"}
	if err != nil || !reflect.DeepEqual(names, want) {
		t.Fatalf("native names changed: %v %v", names, err)
	}
	if _, err := binaryInventory("suite", "gtest", names, gtest); err != nil {
		t.Fatal(err)
	}
	catch := []byte(`<?xml version="1.0"?><MatchingTests><TestCase><Name>name with &amp; spaces</Name><Tags>[hidden][.]</Tags><SourceInfo><File>/private.cpp</File><Line>1</Line></SourceInfo></TestCase><TestCase><Name>generator</Name><Tags>[data]</Tags></TestCase></MatchingTests>`)
	names, err = catchNames(catch)
	if err != nil || !reflect.DeepEqual(names, []string{"name with & spaces", "generator"}) {
		t.Fatalf("Catch2 names changed: %v %v", names, err)
	}
	if _, err := binaryInventory("suite", "catch2", names, catch); err != nil {
		t.Fatal(err)
	}
}

func TestNinjaObservedTransitiveHeadersAndIncompleteReports(t *testing.T) {
	data := []byte("CMakeFiles/suite.dir/main.cpp.o: #deps 3, deps mtime 42 (VALID)\n    /private/source/main.cpp\n    /private/source/shared/header.hpp\n    /unbound/sdk/header.h\n\n")
	names, err := ninjaHeaders(data, "/private/source", "/private/build")
	if err != nil || len(names) != 3 {
		t.Fatalf("observed dependency report missing: %v %v", names, err)
	}
	if names[1] != "source/main.cpp" || names[2] != "source/shared/header.hpp" {
		t.Fatalf("logical header ownership lost: %v", names)
	}
	for _, bad := range []string{"object: #deps 2, deps mtime 42 (VALID)\n    /private/source/one.hpp\n", "object: #deps 0, deps mtime 42 (STALE)\n", "    /unowned/header.h\n"} {
		if _, err := ninjaHeaders([]byte(bad), "/private/source", "/private/build"); err == nil {
			t.Fatal("incomplete/stale/unowned dependency report accepted")
		}
	}
}

func TestNativeInventoriesRejectAmbiguousAndIncompleteNames(t *testing.T) {
	for _, data := range []string{"caseWithoutSuite\n", "Suite.\n    ambiguous\n", "Suite.\n  Case\n  Case\n", "Alpha.\n  Works\nBeta.\n"} {
		names, err := googleNames([]byte(data))
		if err == nil {
			_, err = binaryInventory("suite", "gtest", names, data)
		}
		if err == nil {
			t.Fatalf("ambiguous gtest accepted: %q", data)
		}
	}
	for _, data := range []string{`<MatchingTests><TestCase></TestCase></MatchingTests>`, `<MatchingTests><TestCase><Name>a</Name><Name>b</Name></TestCase></MatchingTests>`, `<MatchingTests><TestCase><Name><Nested>a</Nested></Name></TestCase></MatchingTests>`, `<MatchingTests><TestCase><Name>a</Name></TestCase>`, `<MatchingTests/>trailing`, `<!DOCTYPE x><MatchingTests/>`} {
		if _, err := catchNames([]byte(data)); err == nil {
			t.Fatalf("ambiguous Catch2 accepted: %q", data)
		}
	}
}

func TestCTestInventoryBindsLifecyclePropertiesAndUnresolvedCommands(t *testing.T) {
	healthy := []byte(`{"kind":"ctestInfo","version":{"major":1,"minor":0},"tests":[{"name":"Alpha","command":["/private/suite","--alpha"],"properties":[{"name":"WILL_FAIL","value":true},{"name":"ENVIRONMENT","value":["PRIVATE=hidden"]},{"name":"FIXTURES_REQUIRED","value":["shared"]},{"name":"WORKING_DIRECTORY","value":"/private"}]},{"name":"Beta","command":[],"properties":[{"name":"DISABLED","value":true}]}]}`)
	r, err := ctestInventory(healthy, []string{"-C", "Debug"})
	if err != nil || len(r.Execution.Units) != 2 || len(r.Execution.Cases) != 2 || len(r.Gaps) != 2 {
		t.Fatalf("CTest lifecycle metadata missing: %+v %v", r, err)
	}
	changed, err := ctestInventory(healthy, []string{"-C", "Release"})
	if err != nil || changed.ContextDigest == r.ContextDigest {
		t.Fatal("configured CTest scope not bound")
	}
	for _, data := range []string{`{"kind":"ctestInfo","version":{"major":2},"tests":[]}`, `{"kind":"ctestInfo","kind":"ctestInfo","version":{"major":1,"minor":0},"tests":[]}`, `{"kind":"ctestInfo","version":{"major":1,"minor":0},"tests":[{"name":"Alpha","command":[],"properties":[{"name":"TIMEOUT","value":1},{"name":"TIMEOUT","value":2}]}]}`} {
		if _, err := ctestInventory([]byte(data), nil); err == nil {
			t.Fatal("ambiguous CTest lifecycle admitted")
		}
	}
}
