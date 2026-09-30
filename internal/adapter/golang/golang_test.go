package golang

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
	gorunner "github.com/Cyberlane/hayaku/internal/runner/golang"
)

func fixture(t *testing.T) (string, model.Workspace) {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod":          "module example.test/fixture\n\ngo 1.26.0\n",
		"lib/lib.go":      "package lib\nfunc Value() int { return 1 }\n",
		"app/app.go":      "package app\nimport (\"example.test/fixture/lib\"; _ \"embed\")\n//go:embed data.txt\nvar Data string\nfunc Value() int { return lib.Value() }\n",
		"app/app_test.go": "package app_test\nimport (\"testing\"; \"example.test/fixture/app\")\nfunc TestValue(t *testing.T) { if app.Value()!=1 {t.Fatal(\"value\")} }\n",
		"app/ignored.go":  "//go:build imaginary\n\npackage app\n",
		"app/data.txt":    "hello\n",
	}
	for path, content := range files {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root, model.Workspace{ID: "fixture", Root: ".", Command: model.Command{Dir: ".", Executable: "go", Args: []string{"test", "./..."}}}
}
func TestNativeDiscovery(t *testing.T) {
	root, w := fixture(t)
	e, err := Discover(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Units) != 2 {
		t.Fatalf("units %+v", e.Units)
	}
	for _, path := range []string{"app/app.go", "app/app_test.go", "app/data.txt", "app/ignored.go", "lib/lib.go", "go.mod"} {
		if len(e.Inputs[path]) == 0 {
			t.Errorf("missing ownership for %s", path)
		}
	}
	want := model.Edge{From: "fixture:example.test/fixture/lib", To: "fixture:example.test/fixture/app", Reason: "Go import"}
	found := false
	for _, edge := range e.Edges {
		if edge == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing import edge %+v", e.Edges)
	}
	if len(e.Gaps) == 0 || e.Gaps[0].Code != "runtime-unqualified" {
		t.Fatal("native discovery must not certify runtime influence")
	}
	again, err := Discover(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e, again) {
		t.Fatal("discovery is not deterministic")
	}
}
func TestDiscoveryFailsClosed(t *testing.T) {
	root, w := fixture(t)
	if err := os.WriteFile(filepath.Join(root, "app/app.go"), []byte("package app\nimport \"example.test/notcached\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Discover(context.Background(), root, w, model.Context{})
	if err == nil {
		t.Fatal("missing dependency accepted")
	}
	content, _ := os.ReadFile(filepath.Join(root, "go.mod"))
	if strings.Contains(string(content), "notcached") {
		t.Fatal("discovery modified module graph")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Discover(ctx, root, w, model.Context{}); err == nil {
		t.Fatal("cancel ignored")
	}
}
func TestProposalPreservesFlags(t *testing.T) {
	w := model.Workspace{ID: "w", Command: model.Command{Dir: "nested", Executable: "go", Args: []string{"test", "-race", "-count=1", "-timeout", "1m", "./..."}}}
	command, err := Proposal(w, []model.Unit{{ID: "w:b", Workspace: "w", Selector: "example.test/b", Kind: "go-package"}, {ID: "w:a", Workspace: "w", Selector: "example.test/a", Kind: "go-package"}})
	if err != nil {
		t.Fatal(err)
	}
	want := model.Command{Dir: "nested", Executable: "go", Args: []string{"test", "-race", "-count=1", "-timeout", "1m", "example.test/a", "example.test/b"}}
	if !reflect.DeepEqual(command, want) {
		t.Fatalf("got %+v", command)
	}
	for _, args := range [][]string{{"test", "-run=TestOne", "./..."}, {"test", "-args", "custom"}, {"test", "-mod=mod", "./..."}, {"test", "x.go"}, {"test", "-tags"}} {
		w.Command.Args = args
		if _, err := Proposal(w, []model.Unit{{Workspace: "w", Selector: "example.test/a", Kind: "go-package"}}); err == nil {
			t.Errorf("unsupported command accepted: %v", args)
		}
	}
}
func TestDiscoveryEnvironmentCannotEnableDownloads(t *testing.T) {
	root, w := fixture(t)
	env, err := ExecutionEnvironment(context.Background(), root, w, model.Context{Env: map[string]string{"GOPROXY": "https://example.test", "GOTOOLCHAIN": "auto"}})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	for _, line := range []string{"GOPROXY=off", "GOTOOLCHAIN=local", "GOFLAGS=", "GOENV=off", "GOVCS=*:off"} {
		if !strings.Contains(joined, line+"\n") && !strings.HasSuffix(joined, line) {
			t.Errorf("missing %s", line)
		}
	}
}

func TestExecutionPreservesScope(t *testing.T) {
	w := model.Workspace{Command: model.Command{Dir: "nested", Executable: "go", Args: []string{"test", "-count=1", "./..."}}}
	c, err := Execution(w)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Args, []string{"test", "-json", "-count=1", "./..."}) || c.Dir != "nested" {
		t.Fatalf("command changed: %+v", c)
	}
	w.Command = c
	c, err = Execution(w)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(w.Command, c) {
		t.Fatal("JSON flag duplicated")
	}
}

func TestGoFlagsMustMoveIntoArgv(t *testing.T) {
	root, w := fixture(t)
	configured := model.Context{Env: map[string]string{"GOFLAGS": "-tags=imaginary"}}
	if _, err := Discover(context.Background(), root, w, configured); err == nil || !strings.Contains(err.Error(), "configured command argv") {
		t.Fatalf("configured GOFLAGS was not actionable: %v", err)
	}
	t.Setenv("GOFLAGS", "-tags=imaginary")
	if _, err := ExecutionEnvironment(context.Background(), root, w, model.Context{}); err == nil || !strings.Contains(err.Error(), "command argv") {
		t.Fatalf("inherited GOFLAGS silently removed: %v", err)
	}
}

func TestPersistedGoFlagsMustMoveIntoArgv(t *testing.T) {
	root, w := fixture(t)
	// Isolate go env -w semantics using its file format. Never edit global state.
	envFile := filepath.Join(t.TempDir(), "go-env")
	if err := os.WriteFile(envFile, []byte("GOFLAGS=-tags=imaginary\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOFLAGS", "")
	if err := os.Unsetenv("GOFLAGS"); err != nil {
		t.Fatal(err)
	}
	if _, err := ExecutionEnvironment(context.Background(), root, w, model.Context{Env: map[string]string{"GOENV": envFile}}); err == nil || !strings.Contains(err.Error(), "persisted or inherited") {
		t.Fatalf("persisted GOFLAGS silently removed: %v", err)
	}
}

func TestEffectiveCompilerContextIsFrozen(t *testing.T) {
	root, w := fixture(t)
	envFile := filepath.Join(t.TempDir(), "go-env")
	if err := os.WriteFile(envFile, []byte("CGO_ENABLED=0\nCC=fixture-compiler\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CGO_ENABLED", "")
	if err := os.Unsetenv("CGO_ENABLED"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CC", "")
	if err := os.Unsetenv("CC"); err != nil {
		t.Fatal(err)
	}
	env, err := ExecutionEnvironment(context.Background(), root, w, model.Context{Env: map[string]string{"GOENV": envFile, "GOFLAGS": ""}})
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, pair := range env {
		k, v, _ := strings.Cut(pair, "=")
		values[k] = v
	}
	if values["CGO_ENABLED"] != "0" || values["CC"] != "fixture-compiler" || values["GOENV"] != "off" {
		t.Fatal("effective compiler settings not frozen")
	}
	// Changing the persisted file cannot change the captured execution context.
	if err := os.WriteFile(envFile, []byte("CGO_ENABLED=1\nCC=changed-compiler\n"), 0600); err != nil {
		t.Fatal(err)
	}
	output, err := invoke(context.Background(), root, env, "go", []string{"env", "-json", "CGO_ENABLED", "CC"}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	var actual map[string]string
	if err := json.Unmarshal(output, &actual); err != nil {
		t.Fatal(err)
	}
	if actual["CGO_ENABLED"] != "0" || actual["CC"] != "fixture-compiler" {
		t.Fatal("execution reloaded mutable persisted configuration")
	}
	nativeOutput, err := invoke(context.Background(), root, env, "go", []string{"test", "-json", "./..."}, maxOutput)
	if err != nil {
		t.Fatal(err)
	}
	result, err := gorunner.Reconcile(strings.NewReader(string(nativeOutput)), []string{"example.test/fixture/app", "example.test/fixture/lib"})
	if err != nil || result.Failed {
		t.Fatal("captured effective context failed original native scope")
	}
	if len(result.Tests) != 1 || result.Tests[0].Test != "TestValue" {
		t.Fatal("original native test scope was not executed")
	}

}

func TestGoWorkspaceScopeIsPreservedOrRejected(t *testing.T) {
	root, w := fixture(t)
	if err := os.WriteFile(filepath.Join(root, "go.work"), []byte("go 1.26.0\nuse .\n"), 0600); err != nil {
		t.Fatal(err)
	}
	env, err := ExecutionEnvironment(context.Background(), root, w, model.Context{Env: map[string]string{"GOFLAGS": "", "GOWORK": ""}})
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, pair := range env {
		k, v, _ := strings.Cut(pair, "=")
		values[k] = v
	}
	realRoot, _ := filepath.EvalSymlinks(root)
	if values["GOWORK"] != filepath.Join(realRoot, "go.work") {
		t.Fatal("native ancestor workspace was not preserved")
	}
	off, err := ExecutionEnvironment(context.Background(), root, w, model.Context{Env: map[string]string{"GOWORK": "off", "GOFLAGS": ""}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(off, "\n"), "GOWORK=off\n") {
		t.Fatal("explicit workspace exclusion overwritten")
	}
	external := filepath.Join(t.TempDir(), "go.work")
	if err := os.WriteFile(external, []byte("go 1.26.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ExecutionEnvironment(context.Background(), root, w, model.Context{Env: map[string]string{"GOWORK": external, "GOFLAGS": ""}}); err == nil || !strings.Contains(err.Error(), "outside the snapshot") {
		t.Fatalf("external workspace silently disabled: %v", err)
	}
}

func TestContradictoryEffectiveContextIsRejected(t *testing.T) {
	root, w := fixture(t)
	for _, c := range []model.Context{{Env: map[string]string{"PATH": "/different-path"}}, {OS: "linux", Env: map[string]string{"GOOS": "darwin"}}, {Arch: "arm64", Env: map[string]string{"GOARCH": "amd64"}}} {
		if _, err := ExecutionEnvironment(context.Background(), root, w, c); err == nil {
			t.Fatal("contradictory execution context accepted")
		}
	}
}
