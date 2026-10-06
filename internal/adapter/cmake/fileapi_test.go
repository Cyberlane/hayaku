package cmake

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyberlane/hayaku/internal/adapter/buildclosure"
	"github.com/Cyberlane/hayaku/internal/model"
)

// These helper-generated native protocol fixtures exercise the real shared
// process/sealed-generation reader. They are not installed-runner acceptance.
func TestMain(m *testing.M) {
	if os.Getenv("HAYAKU_CMAKE_METADATA_HELPER") != "1" {
		os.Exit(m.Run())
	}
	if len(os.Args) == 2 && os.Args[1] == "--show-only=json-v1" {
		mode, err := os.ReadFile("query-mode")
		if err != nil {
			os.Exit(7)
		}
		if os.MkdirAll("Testing/Temporary", 0700) != nil || os.WriteFile("Testing/Temporary/LastTest.log", []byte("query bookkeeping"), 0600) != nil {
			os.Exit(8)
		}
		if string(mode) == "ctest-list-malformed" {
			fmt.Print("not-json")
			os.Exit(0)
		}
		if string(mode) == "ctest-refresh-invalid" {
			if os.Remove(filepath.FromSlash(replyDir+"index-current.json")) != nil {
				os.Exit(9)
			}
			fmt.Print(`{"kind":"ctestInfo","version":{"major":1,"minor":0},"tests":[{"name":"Alpha","command":[],"properties":[]}]}`)
			os.Exit(0)
		}
		os.Exit(10)
	}
	if len(os.Args) != 5 || os.Args[1] != "--metadata" {
		os.Exit(2)
	}
	source, build, fault := os.Args[2], os.Args[3], os.Args[4]
	objects := []any{}
	client := map[string]any{}
	files := map[string]any{}
	for _, kind := range []string{"cache", "cmakeFiles", "codemodel", "toolchains"} {
		major := objectVersions[kind]
		ref := map[string]any{"kind": kind, "version": map[string]int{"major": major, "minor": 0}, "jsonFile": kind + ".json"}
		objects = append(objects, ref)
		client[fmt.Sprintf("%s-v%d", kind, major)] = ref
		files[kind+".json"] = map[string]any{"kind": kind, "version": map[string]int{"major": major, "minor": 0}}
	}
	index := map[string]any{"cmake": map[string]any{"version": map[string]any{"major": 4, "minor": 4, "patch": 3, "string": "4.4.3", "isDirty": false}, "generator": map[string]any{"name": "Ninja", "multiConfig": false}}, "objects": objects, "reply": map[string]any{"client-hayaku": client}}
	rootPaths := map[string]any{"source": source, "build": build}
	files["cmakeFiles.json"].(map[string]any)["paths"] = rootPaths
	files["cmakeFiles.json"].(map[string]any)["inputs"] = []any{map[string]any{"path": "CMakeLists.txt"}}
	files["cache.json"].(map[string]any)["entries"] = []any{map[string]any{"name": "CMAKE_BUILD_TYPE", "type": "STRING", "value": "Debug"}}
	files["toolchains.json"].(map[string]any)["toolchains"] = []any{map[string]any{"language": "CXX", "compiler": map[string]any{"path": "/private/compiler", "id": "Clang", "version": "21.1.0"}}}
	config := map[string]any{"name": "Debug", "targets": []any{map[string]any{"name": "suite", "id": "suite::id", "jsonFile": "target.json"}}, "directories": []any{map[string]any{"source": ".", "build": ".", "jsonFile": "directory.json"}}}
	model := files["codemodel.json"].(map[string]any)
	model["paths"] = rootPaths
	model["configurations"] = []any{config}
	target := map[string]any{"name": "suite", "id": "suite::id", "type": "EXECUTABLE", "paths": map[string]any{"source": ".", "build": "."}, "sources": []any{map[string]any{"path": "main.cpp"}, map[string]any{"path": filepath.Join(build, "generated.hpp"), "isGenerated": true}}, "artifacts": []any{map[string]any{"path": "suite"}}, "compileGroups": []any{map[string]any{"language": "CXX", "defines": []any{map[string]any{"define": "SECRET_TEST_VALUE=private"}}}}}
	files["target.json"] = target
	files["directory.json"] = map[string]any{"paths": map[string]any{"source": ".", "build": "."}}
	files["index-current.json"] = index
	switch fault {
	case "healthy", "unreferenced-malformed":
	case "optional-compiler-description":
		files["toolchains.json"].(map[string]any)["toolchains"] = []any{map[string]any{"language": "CXX", "compiler": map[string]any{"path": "/private/compiler"}}}
	case "missing-index":
		delete(files, "index-current.json")
	case "error-index":
		files["error-failed.json"] = map[string]any{"error": "failed"}
	case "escape-reference":
		client["codemodel-v2"].(map[string]any)["jsonFile"] = "../codemodel.json"
	case "duplicate-object":
		index["objects"] = append(objects, objects[0])
	case "foreign-roots":
		rootPaths["source"] = "/foreign/source"
	case "multiple-configs":
		model["configurations"] = []any{config, config}
	case "wrong-target":
		target["id"] = "replacement"
	case "unknown-dependency":
		target["dependencies"] = []any{map[string]any{"id": "absent"}}
	case "escaped-artifact":
		target["artifacts"] = []any{map[string]any{"path": "../outside"}}
	case "wrong-version":
		index["cmake"].(map[string]any)["version"].(map[string]any)["string"] = "4.4.4"
	case "external-source":
		target["sources"] = []any{map[string]any{"path": "/unbound/external.cpp"}}
	case "duplicate-json":
	case "malformed-directory":
		files["directory.json"] = "not an object"
	case "wrong-module-roots":
		files["cmakeFiles.json"].(map[string]any)["paths"] = map[string]any{"source": "/foreign", "build": build}
	case "duplicate-cache":
		files["cache.json"].(map[string]any)["entries"] = []any{map[string]any{"name": "same", "type": "STRING"}, map[string]any{"name": "same", "type": "STRING"}}
	case "missing-toolchains":
		delete(files["toolchains.json"].(map[string]any), "toolchains")
	case "ctest-list-malformed", "ctest-refresh-invalid":
		files["toolchains.json"].(map[string]any)["toolchains"] = []any{map[string]any{"language": "CXX", "compiler": map[string]any{"path": os.Args[0], "id": "fixture", "version": "1"}}}
		if os.WriteFile(filepath.Join(build, "query-mode"), []byte(fault), 0600) != nil {
			os.Exit(11)
		}
	default:
		os.Exit(3)
	}
	dir := filepath.Join(build, filepath.FromSlash(replyDir))
	if os.MkdirAll(dir, 0700) != nil {
		os.Exit(4)
	}
	for name, value := range files {
		data, err := json.Marshal(value)
		if err != nil || os.WriteFile(filepath.Join(dir, name), data, 0600) != nil {
			os.Exit(5)
		}
	}
	if fault == "unreferenced-malformed" {
		_ = os.WriteFile(filepath.Join(dir, "unrelated.json"), []byte("invalid"), 0600)
	}
	if fault == "duplicate-json" {
		_ = os.WriteFile(filepath.Join(dir, "target.json"), []byte(`{"id":"suite::id","id":"replacement"}`), 0600)
	}
	if os.WriteFile(filepath.Join(build, "suite"), []byte("fake artifact"), 0700) != nil || os.WriteFile(filepath.Join(build, "generated.hpp"), []byte("generated"), 0600) != nil {
		os.Exit(6)
	}
	os.Exit(0)
}

func metadataSession(t *testing.T, fault string) *buildclosure.Session {
	t.Helper()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "main.cpp"), []byte("int main(){}"), 0600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s, err := buildclosure.New(t.Context(), buildclosure.Request{Source: source, Revision: "fixture", Tools: []string{exe}, Environment: []string{"HAYAKU_CMAKE_METADATA_HELPER=1"}, Files: queryFiles()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	out, err := s.RunProducer(t.Context(), "configure", model.Command{Executable: exe, Args: []string{"--metadata", "{source}", "{build}", fault}, Dir: "{build}"})
	if err != nil || !out.Completed || out.ExitCode != 0 {
		t.Fatalf("metadata fixture process failed: %+v %v", out, err)
	}
	return s
}

func TestFileAPICurrentReferenceOwnershipAndGaps(t *testing.T) {
	for _, mode := range []string{"healthy", "unreferenced-malformed", "external-source", "optional-compiler-description"} {
		t.Run(mode, func(t *testing.T) {
			s := metadataSession(t, mode)
			api, err := CollectFileAPI(t.Context(), s)
			if err != nil || len(api.Targets) != 1 || len(api.Digest) != 64 || len(api.Gaps) == 0 {
				t.Fatalf("healthy observed metadata missing: %+v %v", api, err)
			}
			if mode == "optional-compiler-description" && len(api.Gaps) != 2 {
				t.Fatal("optional compiler metadata absence was silently closed")
			}
			if mode != "external-source" && (len(api.Targets[0].Sources) != 1 || api.Targets[0].Sources[0] != "source/main.cpp" || api.Targets[0].Generated[0] != "build/generated.hpp" || api.Targets[0].Artifacts[0] != "suite") {
				t.Fatalf("logical ownership missing: %+v", api.Targets[0])
			}
			c := &Closure{session: s, api: api}
			projection, err := c.Observation(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(projection)
			if strings.Contains(string(data), s.Source()) || strings.Contains(string(data), "SECRET_TEST_VALUE") || strings.Contains(string(data), "/unbound/") {
				t.Fatal("public observation leaked private metadata")
			}
			projection.Targets[0].Name = "changed"
			projection.Gaps[0].Code = "changed"
			again, err := c.Observation(t.Context())
			if err != nil || again.Targets[0].Name != "suite" || again.Gaps[0].Code == "changed" {
				t.Fatal("public metadata aliases private projection")
			}
		})
	}
}

func TestFileAPIRejectsIncompleteForeignAndAmbiguousReplies(t *testing.T) {
	for _, fault := range []string{"missing-index", "error-index", "escape-reference", "duplicate-object", "foreign-roots", "multiple-configs", "wrong-target", "unknown-dependency", "escaped-artifact", "wrong-version", "duplicate-json", "malformed-directory", "wrong-module-roots", "duplicate-cache", "missing-toolchains"} {
		t.Run(fault, func(t *testing.T) {
			s := metadataSession(t, fault)
			if api, err := CollectFileAPI(t.Context(), s); err == nil || api.Digest != "" {
				t.Fatalf("invalid native metadata accepted: %+v %v", api, err)
			}
		})
	}
}

func TestConfiguredPrimaryCompilerAdmission(t *testing.T) {
	s := metadataSession(t, "healthy")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Compiler: exe}
	api := FileAPI{compilers: map[string]string{"CXX": exe}}
	if err = admitCompilers(t.Context(), s, request, api); err != nil {
		t.Fatalf("actual bound compiler contradicted: %v", err)
	}
	for _, compilers := range []map[string]string{{"CXX": "/contradictory/compiler"}, {"CXX": exe, "C": exe}, {"Unknown": exe}} {
		if err = admitCompilers(t.Context(), s, request, FileAPI{compilers: compilers}); err == nil {
			t.Fatal("contradictory or uncaptured compiler admitted")
		}
	}
}

func TestCMakeOptionPreflightRejectsBeforeCapture(t *testing.T) {
	request := Request{Environment: []string{}}
	request.Definitions = make([]string, 1025)
	if _, err := Build(t.Context(), request); err == nil || !strings.Contains(err.Error(), "admission bounds") {
		t.Fatalf("definition count reached tool capture: %v", err)
	}
	for _, values := range [][]string{{strings.Repeat("x", 4097)}, make([]string, 1023), {string([]byte{0xff})}} {
		if bounded(values, 1022, 4096) == nil {
			t.Fatal("count/text admission accepted")
		}
	}
}

func TestCTestMalformedNativeListingOrRefreshRevokesObservation(t *testing.T) {
	for _, mode := range []string{"ctest-list-malformed", "ctest-refresh-invalid"} {
		t.Run(mode, func(t *testing.T) {
			s := metadataSession(t, mode)
			api, err := CollectFileAPI(t.Context(), s)
			if err != nil {
				t.Fatal(err)
			}
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			c := &Closure{session: s, request: Request{Compiler: exe, CTest: exe, Ninja: exe}, api: api}
			if _, err = c.Observation(t.Context()); err != nil {
				t.Fatal(err)
			}
			if inventory, err := c.CTest(t.Context(), nil); err == nil || inventory.ContextDigest != "" {
				t.Fatal("incomplete native query/graph accepted")
			}
			if observation, err := c.Observation(t.Context()); err == nil || observation.Digest != "" {
				t.Fatal("stale graph survived a produced generation")
			}
			records, _, err := s.Records(t.Context())
			if err != nil || len(records) != 2 || records[1].ID != "ctest-list-1" {
				t.Fatalf("query bookkeeping was not an actual sealed producer: %v %v", records, err)
			}
			data, err := s.ReadFile(t.Context(), "build", "Testing/Temporary/LastTest.log", 17)
			if err != nil || string(data) != "query bookkeeping" {
				t.Fatal("actual private query output missing")
			}
		})
	}
}

func TestFileAPIRejectsLastingBuildDriftAndCancellation(t *testing.T) {
	s := metadataSession(t, "healthy")
	if _, err := CollectFileAPI(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := CollectFileAPI(ctx, s); err == nil {
		t.Fatal("cancelled metadata admitted")
	}
	if err := os.WriteFile(filepath.Join(s.Build(), "generated.hpp"), []byte("drift"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CollectFileAPI(t.Context(), s); err == nil {
		t.Fatal("sealed generated input drift admitted")
	}
}
