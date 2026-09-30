package pytest

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
)

// This subprocess is a protocol fixture, not a claim of native qualification.
func init() {
	if os.Getenv("HAYAKU_PYTEST_FIXTURE") != "1" {
		return
	}
	if len(os.Args) != 5 || os.Args[1] != "-c" {
		os.Exit(7)
	}
	root, err := os.Getwd()
	if err != nil {
		os.Exit(8)
	}
	result := map[string]any{"schema": 1, "pytest_version": "9.0.0", "python_version": "3.14.0", "exit_code": 0, "items": []map[string]string{
		{"nodeid": "tests/test_one.py::test_param[a::b]", "path": filepath.Join(root, "tests", "test_one.py")},
		{"nodeid": "tests/test_one.py::test_param[with newline\nvalue]", "path": filepath.Join(root, "tests", "test_one.py")},
		{"nodeid": "tests/test_two.py::TestClass::test_method", "path": filepath.Join(root, "tests", "test_two.py")},
	}}
	data, err := json.Marshal(result)
	if err != nil {
		os.Exit(9)
	}
	if err := os.WriteFile(os.Args[3], data, 0600); err != nil {
		os.Exit(10)
	}
	os.Exit(0)
}

func workspace(executable string) model.Workspace {
	return model.Workspace{ID: "py", Root: ".", Adapter: "pytest", Command: model.Command{Dir: ".", Executable: executable, Args: []string{"-m", "pytest", "-k", "enabled", "--strict-markers", "tests"}}}
}

func TestCollectionProtocolAndConservativeOwnership(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper hardlink requires Unix executable naming")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "tests"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"tests/test_one.py", "tests/test_two.py", "conftest.py", "pyproject.toml", "fixture.json", "package.py"} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	current, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	python := filepath.Join(t.TempDir(), "python3")
	if err := os.Link(current, python); err != nil {
		t.Fatal(err)
	}
	w := workspace(python)
	e, err := Discover(context.Background(), root, w, model.Context{OS: runtime.GOOS, Arch: runtime.GOARCH, Env: map[string]string{"HAYAKU_PYTEST_FIXTURE": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Units) != 2 || len(e.Nodes) != 2 {
		t.Fatalf("whole-file rounding=%v", e.Units)
	}
	if !strings.Contains(e.Version, "pytest=9.0.0") {
		t.Fatal("missing tool provenance")
	}
	for _, path := range []string{"tests/test_one.py", "tests/test_two.py", "conftest.py", "pyproject.toml", "fixture.json", "package.py"} {
		if !reflect.DeepEqual(e.Inputs[path], e.Nodes) {
			t.Fatalf("input %q does not conservatively own all files", path)
		}
	}
	c, err := Proposal(w, e.Units)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Args, []string{"-m", "pytest", "-k", "enabled", "--strict-markers", "tests/test_one.py", "tests/test_two.py"}) {
		t.Fatalf("flags or selectors changed: %v", c.Args)
	}
	if len(e.Gaps) < 3 {
		t.Fatal("assurance or side effects gap omitted")
	}
}

func TestCollectionDecodeFailsClosed(t *testing.T) {
	for _, payload := range []string{
		`{"schema":1,"pytest_version":"9.0.0","python_version":"3.14","exit_code":0,"items":[]}`,
		`{"schema":1,"pytest_version":"10.0.0","python_version":"3.14","exit_code":0,"items":[{"nodeid":"x","path":"x"}]}`,
		`{"schema":1,"pytest_version":"9.0.0","python_version":"3.14","exit_code":1,"items":[{"nodeid":"x","path":"x"}]}`,
		`{"schema":1,"pytest_version":"9.0.0","python_version":"3.14","exit_code":0,"items":[{"nodeid":"x","path":"x"}],"unknown":true}`,
		`{invalid`,
	} {
		if _, err := decodeCollection([]byte(payload)); err == nil {
			t.Fatalf("accepted invalid collection %s", payload)
		}
	}
}

func TestUnsupportedFlagsAndInvalidUnits(t *testing.T) {
	for _, args := range [][]string{
		{"-m", "pytest", "--last-failed"},
		{"-m", "pytest", "--collect-only"},
		{"-m", "pytest", "--pyargs", "package"},
		{"-m", "pytest", "-k"},
	} {
		if _, _, err := commandParts(model.Command{Executable: "python3", Args: args}); err == nil {
			t.Fatalf("accepted unsupported command %v", args)
		}
	}
	w := workspace("python3")
	for _, units := range [][]model.Unit{nil, {{Workspace: "other", Selector: "tests/x.py", Kind: "pytest-file"}}, {{Workspace: "py", Selector: "../x.py", Kind: "pytest-file"}}, {{Workspace: "py", Selector: "test.py::test_case", Kind: "pytest-file"}}} {
		if _, err := Proposal(w, units); err == nil {
			t.Fatal("accepted invalid proposal")
		}
	}
	if _, err := Discover(context.Background(), t.TempDir(), w, model.Context{OS: "unsupported-platform"}); err == nil {
		t.Fatal("collected wrong host context")
	}
}

func TestNativePytestCollection(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python is not installed")
	}
	probe := exec.Command(python, "-c", "import pytest; print(pytest.__version__)")
	if _, err := probe.Output(); err != nil {
		t.Skip("pytest is not installed; no automatic installation")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "test_sample.py"), []byte("import pytest\n@pytest.mark.parametrize('value', [1,2])\ndef test_parameter(value):\n    assert value > 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	w := workspace(python)
	w.Command.Args = []string{"-m", "pytest"}
	e, err := Discover(context.Background(), root, w, model.Context{OS: runtime.GOOS, Arch: runtime.GOARCH})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Units) != 1 || e.Units[0].Selector != "test_sample.py" {
		t.Fatalf("native collection=%v", e.Units)
	}
}
