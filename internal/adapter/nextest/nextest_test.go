package nextest

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"bytes"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
	nativerunner "github.com/Cyberlane/hayaku/internal/runner/native"
)

func fixture(t *testing.T) (string, model.Workspace) {
	t.Helper()
	if _, err := exec.LookPath("cargo-nextest"); err != nil {
		if os.Getenv("HAYAKU_NEXTEST_REQUIRED") == "1" {
			t.Fatal("required installed nextest fixture unavailable")
		}
		t.Skip("installed nextest fixture unavailable")
	}
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Fatal("nextest fixture requires installed Cargo")
	}
	root := t.TempDir()
	files := map[string]string{"Cargo.toml": "[package]\nname = \"native-nextest-fixture\"\nversion = \"0.1.0\"\nedition = \"2021\"\n[features]\nextra = []\n", "src/lib.rs": "#[cfg(test)] mod tests { #[test] fn basic() { assert_eq!(1,1); } #[test] #[ignore] fn ignored() {} #[cfg(feature=\"extra\")] #[test] fn feature_case() {} }\n", ".config/nextest.toml": "[profile.ci]\ndefault-filter = \"all()\"\n[profile.ci.junit]\npath = \"junit.xml\"\n"}
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := process.Run(context.Background(), model.Command{Executable: "cargo", Args: []string{"generate-lockfile", "--offline"}}, root, map[string]string{"CARGO_NET_OFFLINE": "true", "RUSTUP_AUTO_INSTALL": "0"}); err != nil {
		t.Fatal("offline fixture lockfile generation", err)
	}
	return root, model.Workspace{ID: "rust", Root: ".", Adapter: "nextest", Command: model.Command{Dir: ".", Executable: "cargo", Args: []string{"nextest", "run", "--offline", "--locked", "--all-features", "--profile", "ci", "--no-fail-fast", "--user-config-file=none"}}}
}
func TestInstalledNativeNextestConfiguredInventory(t *testing.T) {
	root, w := fixture(t)
	inventory, err := Collect(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Version != NativeVersion || len(inventory.Binaries) != 1 || len(inventory.Binaries[0].Cases) != 3 {
		t.Fatalf("incomplete native inventory %+v", inventory)
	}
	selected, ignored := 0, 0
	for _, c := range inventory.Binaries[0].Cases {
		if c.Selected {
			selected++
		}
		if c.Ignored {
			ignored++
		}
	}
	if selected != 2 || ignored != 1 {
		t.Fatalf("native filter/ignore semantics %+v", inventory.Binaries[0].Cases)
	}
	e, err := Discover(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Units) != 1 || e.Units[0].Kind != "nextest-package" || e.Units[0].Selector != "native-nextest-fixture" || len(e.Inputs[".config/nextest.toml"]) != 1 {
		t.Fatalf("missing package/config ownership %+v", e)
	}
	expected, err := inventory.ExpectedJUnitCases(e.Units)
	if err != nil {
		t.Fatal(err)
	}
	// nextest creates empty configured profile store directories independently
	// of Cargo's target-dir. Compiled artifacts and report files stay private.
	if _, err := os.Stat(filepath.Join(root, "target")); err == nil {
		if err := filepath.WalkDir(filepath.Join(root, "target"), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() {
				return errors.New("native build artifact written to snapshot")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	// The installed runner's stable JUnit format matches its selected native
	// inventory. Experimental libtest-plus streams are not full fidelity.
	if _, err := process.Run(context.Background(), w.Command, root, map[string]string{"CARGO_NET_OFFLINE": "true", "RUSTUP_AUTO_INSTALL": "0", "CARGO_TARGET_DIR": filepath.Join(t.TempDir(), "run-target")}); err != nil {
		t.Fatal("native nextest full fixture", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "target", "nextest", "ci", "junit.xml"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := nativerunner.ReconcileJUnit(bytes.NewReader(data), expected)
	if err != nil || result.Failed || len(result.Tests) != 2 {
		t.Fatalf("native nextest JUnit results %+v %v", result, err)
	}
	w.Command.Args = append(w.Command.Args, "-E", "test(feature_case)")
	filtered, err := Collect(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	matched := 0
	for _, c := range filtered.Binaries[0].Cases {
		if c.Selected {
			matched++
			if c.Name != "tests::feature_case" {
				t.Fatal("configured native filter lost")
			}
		}
	}
	if matched != 1 {
		t.Fatal("native filter did not select one case")
	}
}
func TestProposalSeparatesRunnerAndCargoProfiles(t *testing.T) {
	w := model.Workspace{ID: "rust", Adapter: "nextest", Command: model.Command{Dir: "nested", Executable: "cargo", Args: []string{"nextest", "run", "--workspace", "--exclude", "old", "--profile=ci", "--cargo-profile", "release", "--features=extra", "--retries=2", "-E", "test(value)"}}}
	a, err := parse(w)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(a.cargo, " "), "ci") || !strings.Contains(strings.Join(a.cargo, " "), "--profile release") {
		t.Fatal("runner profile confused with Cargo build profile")
	}
	cmd, err := Proposal(w, []model.Unit{{Workspace: "rust", Kind: "nextest-package", Selector: "b"}, {Workspace: "rust", Kind: "nextest-package", Selector: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"nextest", "run", "--profile=ci", "--cargo-profile", "release", "--features=extra", "--retries=2", "-E", "test(value)", "--package", "a", "--package", "b"}
	if !reflect.DeepEqual(cmd.Args, want) || cmd.Dir != "nested" {
		t.Fatalf("proposal changed context %+v", cmd)
	}
	for _, args := range [][]string{{"nextest", "run", "--archive-file=x"}, {"nextest", "run", "--target-dir=x"}, {"nextest", "run", "--message-format=human"}, {"nextest", "run", "--features"}, {"nextest", "run", "--no-run"}, {"nextest", "run", "--workspace=true"}, {"nextest", "run", "--", "--ignored"}} {
		w.Command.Args = args
		if _, err := parse(w); err == nil {
			t.Fatal("unsupported native argv accepted", args)
		}
	}
}
func TestNativeNextestContextAndCancellation(t *testing.T) {
	root, w := fixture(t)
	if _, err := Collect(context.Background(), root, w, model.Context{OS: "different"}); err == nil {
		t.Fatal("cross-host discovery accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Collect(ctx, root, w, model.Context{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("native cancellation lost %v", err)
	}
}
