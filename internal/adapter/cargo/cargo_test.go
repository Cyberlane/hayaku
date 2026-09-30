package cargo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
)

func rustFixture(t *testing.T) (string, model.Workspace) {
	t.Helper()
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("native Cargo unavailable")
	}
	root := t.TempDir()
	files := map[string]string{
		"Cargo.toml":           "[workspace]\nmembers = [\"core\", \"app\"]\nresolver = \"2\"\n",
		"core/Cargo.toml":      "[package]\nname = \"fixture-core\"\nversion = \"0.1.0\"\nedition = \"2021\"\n[features]\nextra = []\n",
		"core/src/lib.rs":      "/// Value\n/// ```\n/// assert_eq!(fixture_core::value(), 1);\n/// ```\npub fn value() -> u32 { 1 }\n#[test]\nfn test_value() {assert_eq!(value(), 1);}\n",
		"app/Cargo.toml":       "[package]\nname = \"fixture-app\"\nversion = \"0.1.0\"\nedition = \"2021\"\n[dependencies]\nfixture-core = {path = \"../core\"}\n",
		"app/src/lib.rs":       "pub fn value() -> u32 {fixture_core::value()}\n#[test]\nfn test_value() {assert_eq!(value(), 1);}\n",
		"app/tests/fixture.rs": "#[test]\nfn integration() {assert_eq!(fixture_app::value(), 1);}\n",
		"app/data.txt":         "fixture data\n",
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
	out, err := process.Run(context.Background(), model.Command{Executable: "cargo", Args: []string{"generate-lockfile", "--offline"}}, root, map[string]string{"RUSTUP_AUTO_INSTALL": "0"})
	if err != nil {
		t.Fatalf("fixture lockfile: %v (exit %d)", err, out.ExitCode)
	}
	w := model.Workspace{ID: "rust", Root: ".", Command: model.Command{Dir: ".", Executable: "cargo", Args: []string{"test", "--workspace", "--offline", "--locked"}}}
	return root, w
}
func TestNativeMetadataAndExecution(t *testing.T) {
	root, w := rustFixture(t)
	before, err := os.ReadFile(filepath.Join(root, "Cargo.lock"))
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := Discover(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence.Units) != 2 {
		t.Fatalf("units %+v", evidence.Units)
	}
	for _, path := range []string{"Cargo.toml", "Cargo.lock", "core/src/lib.rs", "app/src/lib.rs", "app/tests/fixture.rs", "app/data.txt"} {
		if len(evidence.Inputs[path]) == 0 {
			t.Errorf("missing input %s", path)
		}
	}
	found := false
	for _, edge := range evidence.Edges {
		if edge.From == "rust:core:fixture-core@0.1.0" && edge.To == "rust:app:fixture-app@0.1.0" {
			found = true
		}
	}
	if !found {
		t.Fatal("native dependency edge missing")
	}
	again, err := Discover(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(evidence, again) {
		t.Fatal("non deterministic metadata")
	}
	after, _ := os.ReadFile(filepath.Join(root, "Cargo.lock"))
	if !reflect.DeepEqual(before, after) {
		t.Fatal("metadata modified lockfile")
	}
	command, err := Proposal(w, evidence.Units)
	if err != nil {
		t.Fatal(err)
	}
	out, err := process.Run(context.Background(), command, root, map[string]string{"RUSTUP_AUTO_INSTALL": "0", "CARGO_NET_OFFLINE": "true"})
	if err != nil {
		t.Fatalf("native unit, integration and documentation tests failed (exit %d)", out.ExitCode)
	}
}
func TestPackageAndFeatureScope(t *testing.T) {
	root, w := rustFixture(t)
	w.Command.Args = []string{"test", "-p", "fixture-core", "--features", "extra", "--offline", "--locked"}
	e, err := Discover(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Units) != 1 || e.Units[0].Selector != "fixture-core" {
		t.Fatalf("scope %+v", e.Units)
	}
	command, err := Proposal(w, e.Units)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"test", "--features", "extra", "--offline", "--locked", "--package", "fixture-core"}
	if !reflect.DeepEqual(command.Args, want) {
		t.Fatalf("args %v", command.Args)
	}
}
func TestUnsupportedScopesAndMissingLock(t *testing.T) {
	root, w := rustFixture(t)
	for _, args := range [][]string{{"test", "TestName"}, {"test", "--", "--ignored"}, {"test", "--manifest-path", "other/Cargo.toml"}, {"test", "--features"}, {"test", "--exclude", "fixture-app"}} {
		w.Command.Args = args
		if _, err := parse(w); err == nil {
			t.Errorf("unsupported args accepted %v", args)
		}
	}
	w.Command.Args = []string{"test", "--workspace", "--offline", "--locked"}
	if err := os.Remove(filepath.Join(root, "Cargo.lock")); err != nil {
		t.Fatal(err)
	}
	if _, err := Discover(context.Background(), root, w, model.Context{}); err == nil {
		t.Fatal("absent immutable lockfile accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "Cargo.lock")); !os.IsNotExist(err) {
		t.Fatal("discovery generated lockfile")
	}
}
