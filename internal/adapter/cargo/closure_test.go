package cargo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyberlane/hayaku/internal/inputbundle"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/planner"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/runner"
)

func TestInstalledCargoProcMacroSiblingResource(t *testing.T) {
	root, w := rustFixture(t)
	closurePut(t, root, "Cargo.toml", "[workspace]\nmembers=[\"core\",\"app\",\"macro\"]\nresolver=\"2\"\n")
	closurePut(t, root, "macro/Cargo.toml", "[package]\nname=\"fixture-macro\"\nversion=\"0.1.0\"\nedition=\"2021\"\n[lib]\nproc-macro=true\n")
	closurePut(t, root, "macro/src/lib.rs", "extern crate proc_macro;\n#[proc_macro] pub fn number(_:proc_macro::TokenStream)->proc_macro::TokenStream{ let path=std::path::Path::new(env!(\"CARGO_MANIFEST_DIR\")).join(\"../resource.txt\"); let value=std::fs::read_to_string(path).unwrap(); value.trim().parse().unwrap() }\n")
	closurePut(t, root, "resource.txt", "1\n")
	closurePut(t, root, "core/Cargo.toml", "[package]\nname=\"fixture-core\"\nversion=\"0.1.0\"\nedition=\"2021\"\n[features]\nextra=[]\n[dependencies]\nfixture-macro={path=\"../macro\"}\n")
	closurePut(t, root, "core/src/lib.rs", "pub fn value()->u32{fixture_macro::number!()}\n#[test]fn actual_macro_resource(){assert_eq!(value(),1);}\n")
	if _, err := process.Run(context.Background(), model.Command{Executable: "cargo", Args: []string{"generate-lockfile", "--offline"}}, root, map[string]string{"CARGO_NET_OFFLINE": "true", "RUSTUP_AUTO_INSTALL": "0"}); err != nil {
		t.Fatal(err)
	}
	c, err := ResolveClosure(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if len(c.Evidence().Inputs["resource.txt"]) != 3 || len(c.Inventory.Cases) != 3 || c.InventoryComplete {
		t.Fatal("transitive proc-macro resource closure not conservatively retained")
	}
	if c.RuntimeDigest == "" || len(c.directories) != 2 {
		t.Fatal("compiler and private target runtime search views were not captured")
	}
	for _, artifact := range c.Artifacts {
		if len(artifact.Cases) > 0 && artifact.ContextDigest == "" {
			t.Fatal("observed native cases lack their derived environment identity")
		}
	}
	found := false
	for _, gap := range c.Gaps {
		found = found || gap.Code == "generated-input-unqualified"
	}
	if !found {
		t.Fatal("host native producer ambiguity omitted")
	}
	// A successful compilation is the independent positive resource-read oracle.
	// The source change after capture must invalidate the whole shared closure.
	closurePut(t, root, "resource.txt", "2\n")
	if c.Verify(context.Background()) == nil {
		t.Fatal("proc-macro sibling source drift admitted")
	}
}

func TestInstalledCargoPrelaunchAliasAndBytesDrift(t *testing.T) {
	root, w := rustFixture(t)
	actual, err := exec.LookPath("cargo")
	if err != nil {
		t.Fatal(err)
	}
	private := t.TempDir()
	alias := filepath.Join(private, "cargo")
	if err := os.Symlink(actual, alias); err != nil {
		t.Fatal(err)
	}
	w.Command.Executable = alias
	c, err := ResolveMetadata(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.RunNative(context.Background(), []string{"--version"}); err != nil {
		t.Fatal("healthy private alias", err)
	}
	marker := filepath.Join(private, "executed")
	fake := filepath.Join(private, "replacement")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf invoked > '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fake, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RunNative(context.Background(), []string{"--version"}); err == nil {
		t.Fatal("retargeted Cargo alias executed")
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatal("changed alias marker executed")
	}
	// Restore the actual alias before capturing a separate private extension.
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actual, alias); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	c, err = ResolveMetadata(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.CaptureTool(context.Background(), fake); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RunExtension(context.Background(), fake, nil); err != nil {
		t.Fatal("healthy captured extension", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal("healthy extension did not execute", err)
	}
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf changed > '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RunExtension(context.Background(), fake, nil); err == nil {
		t.Fatal("changed captured extension executed")
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatal("changed extension marker executed")
	}
}

func TestInstalledCargoRetainedCallerContextAndLatePATH(t *testing.T) {
	root, w := rustFixture(t)
	execution := model.Context{Env: map[string]string{"CLOSURE_TEST_VALUE": "original"}}
	c, err := ResolveMetadata(context.Background(), root, w, execution)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	w.Command.Args[0] = "changed"
	if _, err := c.RunNative(context.Background(), []string{"--version"}); err == nil {
		t.Fatal("caller-owned argv drift admitted")
	}
	w.Command.Args[0] = "test"
	execution.Env["CLOSURE_TEST_VALUE"] = "changed"
	if c.Verify(context.Background()) == nil {
		t.Fatal("caller-owned environment drift admitted")
	}
	execution.Env["CLOSURE_TEST_VALUE"] = "original"
	private := t.TempDir()
	closurePut(t, private, "cargo-nextest", "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(filepath.Join(private, "cargo-nextest"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", private+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := c.CaptureTool(context.Background(), "cargo-nextest"); err == nil {
		t.Fatal("late ambient PATH resolution admitted")
	}
}

func closurePut(t *testing.T, root, name, value string) {
	t.Helper()
	full := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestInstalledCargoObservedBuildClosure(t *testing.T) {
	root, w := rustFixture(t)
	// Identically named top-level tests in different lib/integration binaries must
	// have independent shared identities, even under the same owning package.
	closurePut(t, root, "app/tests/fixture.rs", "#[test]\nfn test_value(){assert_eq!(fixture_app::value(),1);}\n")
	closurePut(t, root, "sibling/value.txt", "sibling data")
	c, err := ResolveClosure(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.InventoryComplete || len(c.Inventory.Cases) != 3 || runner.ValidateInventory(c.Inventory) != nil || len(c.Artifacts) < 3 || len(c.Packages) != 2 {
		t.Fatalf("observed closure %+v", c.Inventory)
	}
	appCases := 0
	for _, test := range c.Inventory.Cases {
		if strings.Contains(test.Unit, ":app:") {
			appCases++
			if !strings.HasSuffix(test.ID, "::test_value") {
				t.Fatal("native case name lost")
			}
		}
	}
	if appCases != 2 {
		t.Fatal("lib/integration identities collapsed")
	}
	for _, name := range []string{"sibling/value.txt", "core/src/lib.rs", "app/data.txt"} {
		if len(c.Evidence().Inputs[name]) != 2 {
			t.Fatal("snapshotwide conservative ownership missing", name)
		}
	}
	data, err := json.Marshal(c)
	if err != nil || strings.Contains(string(data), root) || strings.Contains(string(data), c.temp) || strings.Contains(string(data), "sibling data") {
		t.Fatal("private source/artifact identity leaked")
	}
	if err := c.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	artifacts := c.temp
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(artifacts); !os.IsNotExist(err) {
		t.Fatal("private build cleanup failed")
	}
	if c.Verify(context.Background()) == nil {
		t.Fatal("released build retained revalidation authority")
	}
}
func TestInstalledCargoFeaturesTargetsAndRenamedDependencies(t *testing.T) {
	root, w := rustFixture(t)
	closurePut(t, root, "app/Cargo.toml", "[package]\nname=\"fixture-app\"\nversion=\"0.1.0\"\nedition=\"2021\"\n[dependencies]\nrenamed={package=\"fixture-core\",path=\"../core\",features=[\"extra\"]}\n[target.'cfg(unix)'.dev-dependencies]\nrenamed={package=\"fixture-core\",path=\"../core\"}\n")
	closurePut(t, root, "app/src/lib.rs", "pub fn value()->u32{renamed::value()}\n")
	c, err := ResolveMetadata(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	feature, rename, platform, doctest := false, false, false, false
	for _, p := range c.Packages {
		for _, f := range p.Features {
			feature = feature || f == "extra"
		}
		for _, target := range p.Targets {
			doctest = doctest || target.Doctest && target.Source == "core/src/lib.rs"
		}
	}
	for _, d := range c.Dependencies {
		rename = rename || d.Name == "renamed" && d.Kind == "normal"
		platform = platform || d.Name == "renamed" && d.Kind == "dev" && d.Platform == "cfg(unix)"
	}
	if !feature || !rename || !platform || !doctest {
		t.Fatalf("metadata omitted variants: %v %v %v %v", feature, rename, platform, doctest)
	}
}
func TestInstalledCargoUnknownBroadensWithNoChanges(t *testing.T) {
	root, w := rustFixture(t)
	c, err := ResolveMetadata(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	config := model.Config{Schema: model.Schema, Context: model.Context{ID: "rust-fixture"}, Workspaces: []model.Workspace{{ID: w.ID, Root: w.Root, Adapter: "cargo", Command: w.Command}}}
	plan, err := planner.Build(nil, []model.Evidence{c.Evidence()}, nil, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Proposed) != len(c.Scopes) {
		t.Fatalf("unknown native influence narrowed empty diff: %+v", plan)
	}
}
func TestCargoListShapeDoesNotProveCustomHarness(t *testing.T) {
	cases, err := parseCases([]byte("same: test\nother: benchmark\n"))
	if err != nil || len(cases) != 2 {
		t.Fatal(cases, err)
	}
	for _, data := range []string{"same: test\nsame: test\n", "plausible summary\n", string([]byte{255})} {
		if _, err := parseCases([]byte(data)); err == nil {
			t.Fatal("invalid list admitted")
		}
	}
}

func TestInstalledCargoCustomHarnessAndZeroCaseScope(t *testing.T) {
	root, w := rustFixture(t)
	closurePut(t, root, "core/Cargo.toml", "[package]\nname=\"fixture-core\"\nversion=\"0.1.0\"\nedition=\"2021\"\n[features]\nextra=[]\n[lib]\nharness=false\n")
	closurePut(t, root, "core/src/lib.rs", "pub fn value()->u32{1}\nfn main(){println!(\"plausible: test\");}\n")
	closurePut(t, root, "app/src/lib.rs", "pub fn value()->u32{fixture_core::value()}\n")
	if err := os.Remove(filepath.Join(root, "app/tests/fixture.rs")); err != nil {
		t.Fatal(err)
	}
	c, err := ResolveClosure(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.InventoryComplete || len(c.Scopes) != 2 || len(c.Inventory.Units) != 1 || len(c.Inventory.Cases) != 1 || !strings.HasSuffix(c.Inventory.Cases[0].ID, "::plausible") {
		t.Fatalf("custom list certified or zero-case scope fabricated %+v", c.Inventory)
	}
	if len(c.Effects) != 2 || len(c.Evidence().Units) != 2 {
		t.Fatal("custom/zero-case package lost conservative scope")
	}
}
func TestInstalledCargoBuildScriptMutationRejected(t *testing.T) {
	root, w := rustFixture(t)
	closurePut(t, root, "app/build.rs", "fn main(){let p=std::env::var(\"CARGO_MANIFEST_DIR\").unwrap()+\"/src/lib.rs\";std::fs::write(p,\"pub fn value()->u32{fixture_core::value()} // producer changed source\\n\").unwrap();}\n")
	c, err := ResolveClosure(context.Background(), root, w, model.Context{})
	if err == nil {
		c.Close()
		t.Fatal("build producer source drift admitted")
	}
	data, readErr := os.ReadFile(filepath.Join(root, "app/src/lib.rs"))
	if readErr != nil || !strings.Contains(string(data), "producer changed source") {
		t.Fatal("rejection happened before actual producer mutation")
	}
	if c.temp != "" {
		t.Fatal("failed build leaked private target")
	}
}
func TestInstalledCargoArtifactAndMetadataDrift(t *testing.T) {
	root, w := rustFixture(t)
	c, err := ResolveClosure(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// The public owner map is cloned; mutating it cannot rewrite captured state.
	e := c.Evidence()
	e.Inputs["app/src/lib.rs"][0] = "caller"
	if c.Evidence().Inputs["app/src/lib.rs"][0] == "caller" {
		t.Fatal("public evidence aliases captured ownership")
	}
	var name string
	for _, binding := range c.bindings {
		if within(c.PrivateTargetDirectory(), binding.source) {
			name = binding.source
			break
		}
	}
	if name == "" {
		t.Fatal("no real compiler artifact captured")
	}
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 1
	if err := os.WriteFile(name, data, 0700); err != nil {
		t.Fatal(err)
	}
	if c.Verify(context.Background()) == nil {
		t.Fatal("actual captured compiler artifact drift admitted")
	}
}
func TestCargoClosureCancellationAndBounds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, w := rustFixture(t)
	if _, err := ResolveMetadata(ctx, t.TempDir(), w, model.Context{}); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled metadata admitted")
	}
}

func TestRustClosureConfiguredBounds(t *testing.T) {
	w := model.Workspace{ID: "rust", Root: ".", Adapter: "cargo", Command: model.Command{Dir: ".", Executable: "cargo", Args: []string{"test", "--offline"}}}
	if ValidateClosureRequest(w, model.Context{}) != nil {
		t.Fatal("healthy context rejected")
	}
	for name, alter := range map[string]func(*model.Workspace, *model.Context){
		"argument-count":  func(w *model.Workspace, c *model.Context) { w.Command.Args = make([]string, 1025) },
		"argument-bytes":  func(w *model.Workspace, c *model.Context) { w.Command.Args = []string{strings.Repeat("x", 8193)} },
		"invalid-utf8":    func(w *model.Workspace, c *model.Context) { w.ID = string([]byte{255}) },
		"environment-key": func(w *model.Workspace, c *model.Context) { c.Env = map[string]string{"BAD=KEY": "value"} },
		"environment-nul": func(w *model.Workspace, c *model.Context) { c.Env = map[string]string{"KEY": "value\x00"} },
		"environment-count": func(w *model.Workspace, c *model.Context) {
			c.Env = map[string]string{}
			for i := 0; i < 1025; i++ {
				c.Env[string(rune(i+1000))] = "value"
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate, execution := w, model.Context{}
			alter(&candidate, &execution)
			if ValidateClosureRequest(candidate, execution) == nil {
				t.Fatal("unbounded or malformed context admitted")
			}
		})
	}
}

func TestInstalledCargoSiblingTargetAndRequiredFeature(t *testing.T) {
	root, w := rustFixture(t)
	closurePut(t, root, "shared/core.rs", "pub fn value()->u32{1}\n")
	closurePut(t, root, "core/Cargo.toml", "[package]\nname=\"fixture-core\"\nversion=\"0.1.0\"\nedition=\"2021\"\n[lib]\npath=\"../shared/core.rs\"\n[features]\nextra=[]\n[[bin]]\nname=\"feature-only\"\npath=\"src/optional.rs\"\nrequired-features=[\"extra\"]\n")
	closurePut(t, root, "core/src/optional.rs", "fn main(){}\n")
	c, err := ResolveMetadata(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	sibling, required := false, false
	for _, p := range c.Packages {
		for _, target := range p.Targets {
			sibling = sibling || target.Source == "shared/core.rs"
			required = required || target.Name == "feature-only" && len(target.RequiredFeatures) == 1 && target.RequiredFeatures[0] == "extra"
		}
	}
	if !sibling || !required || len(c.Evidence().Inputs["shared/core.rs"]) != 2 {
		t.Fatal("target outside package or required feature omitted")
	}
}
func TestInstalledCargoProducerOutputsBound(t *testing.T) {
	root, w := rustFixture(t)
	closurePut(t, root, "app/build.rs", "fn main(){let p=std::env::var(\"OUT_DIR\").unwrap()+\"/value.rs\";std::fs::write(p,\"pub fn generated()->u32{1}\\n\").unwrap();println!(\"cargo:rerun-if-changed=data.txt\");}\n")
	closurePut(t, root, "app/src/lib.rs", "include!(concat!(env!(\"OUT_DIR\"),\"/value.rs\"));pub fn value()->u32{generated()}#[test]fn test_generated(){assert_eq!(value(),1);}\n")
	c, err := ResolveClosure(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if len(c.Producers) != 1 || c.Producers[0].OutputsDigest == "" || c.InventoryComplete {
		t.Fatal("generated output lacks descriptive producer binding")
	}
	var output string
	for _, binding := range c.bindings {
		candidate := filepath.Join(binding.source, "value.rs")
		if _, err := os.Stat(candidate); err == nil {
			output = candidate
			break
		}
	}
	if output == "" {
		t.Fatal("actual OUT_DIR fixture was not produced")
	}
	data, err := os.ReadFile(output)
	if err != nil || string(data) != "pub fn generated()->u32{1}\n" {
		t.Fatal("unexpected producer output")
	}
	if err := os.WriteFile(output, []byte("changed output"), 0600); err != nil {
		t.Fatal(err)
	}
	if c.Verify(context.Background()) == nil {
		t.Fatal("generated output drift admitted")
	}
}

func TestInstalledCargoInvocationAndPrivateContextPinned(t *testing.T) {
	root, w := rustFixture(t)
	c, err := ResolveMetadata(context.Background(), root, w, model.Context{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !filepath.IsAbs(c.cargoInvocation) || !filepath.IsAbs(environmentValue(c.env, "RUSTC")) || !filepath.IsAbs(environmentValue(c.env, "RUSTDOC")) {
		t.Fatal("tool invocations remain lexical PATH lookups")
	}
	// A real new earlier candidate proves the next invocation does not re-resolve
	// through ambient PATH. No actual installed tool or host alias is changed.
	baseline, err := c.RunNative(context.Background(), []string{"--version"})
	if err != nil || !strings.HasPrefix(string(baseline.Stdout), "cargo ") {
		t.Fatal("native baseline unavailable")
	}
	fake := t.TempDir()
	closurePut(t, fake, "cargo", "#!/bin/sh\nprintf 'shadow-cargo-sentinel\\n'\n")
	if err := os.Chmod(filepath.Join(fake, "cargo"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fake+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := c.RunNative(context.Background(), []string{"--version"})
	if err != nil || string(output.Stdout) != string(baseline.Stdout) {
		t.Fatal("captured CLI changed after PATH insertion")
	}
	c.env = append(c.env, "PRIVATE_MUTATION=changed")
	if c.Verify(context.Background()) == nil {
		t.Fatal("private environment mutation escaped identity")
	}
	if _, err := c.RunNative(context.Background(), []string{"--version"}); err == nil {
		t.Fatal("mutated context reached execution")
	}
}

func TestRustRuntimeDirectoryBytesAndAliasDrift(t *testing.T) {
	for _, fault := range []string{"bytes", "alias", "membership"} {
		t.Run(fault, func(t *testing.T) {
			base, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			real := filepath.Join(base, "actual")
			closurePut(t, real, "libfixture.so", "healthy-library-bytes")
			alias := filepath.Join(base, "selected")
			if err := os.Symlink(real, alias); err != nil {
				t.Fatal(err)
			}
			c := Closure{requestDigest: closureIdentity([]any{model.Workspace{}, model.Context{}})}
			if err := c.captureRuntimeDirectory(context.Background(), alias); err != nil {
				t.Fatal(err)
			}
			if err := c.verifyBindings(context.Background()); err != nil {
				t.Fatal("healthy captured runtime", err)
			}
			switch fault {
			case "bytes":
				closurePut(t, real, "libfixture.so", "modified-library-bytes")
			case "alias":
				other := filepath.Join(base, "other")
				closurePut(t, other, "libfixture.so", "healthy-library-bytes")
				if err := os.Remove(alias); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, alias); err != nil {
					t.Fatal(err)
				}
			case "membership":
				closurePut(t, real, "extra.so", "new library")
			}
			if c.verifyBindings(context.Background()) == nil {
				t.Fatal("changed runtime view admitted")
			}
		})
	}
}

func TestRustRuntimeSearchIsBoundedAndPrivate(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	closurePut(t, base, "target/debug/deps/test-binary", "binary")
	closurePut(t, base, "compiler/libstd.so", "library")
	c := Closure{temp: base, libraryDirectory: filepath.Join(base, "compiler"), env: []string{"LANG=C"}}
	binary := filepath.Join(base, "target/debug/deps/test-binary")
	healthy, err := c.nativeListEnvironment(binary)
	if err != nil || !strings.Contains(environmentValue(healthy, "LD_LIBRARY_PATH")+environmentValue(healthy, "DYLD_FALLBACK_LIBRARY_PATH"), c.libraryDirectory) {
		t.Fatal("healthy private compiler search was not restored", err)
	}
	key := "LD_LIBRARY_PATH"
	if environmentValue(healthy, "DYLD_FALLBACK_LIBRARY_PATH") != "" {
		key = "DYLD_FALLBACK_LIBRARY_PATH"
	}
	unsetIdentity := c.privateStateDigest()
	c.env = append(c.env, key+"=")
	if c.privateStateDigest() == unsetIdentity {
		t.Fatal("unset versus explicit empty original search context lost")
	}
	if _, err := c.nativeListEnvironment(binary); err != nil {
		t.Fatal("explicit empty inherited search", err)
	}
	for _, outside := range []string{filepath.Dir(base), "relative/path"} {
		c.env = []string{key + "=" + outside}
		if _, err := c.nativeListEnvironment(binary); err == nil {
			t.Fatal("unbound inherited search admitted")
		}
	}
	c.env = nil
	c.linkedDirectories = make([]string, 1025)
	if _, err := c.nativeListEnvironment(binary); err == nil {
		t.Fatal("unbounded search directory inventory admitted")
	}
	c.linkedDirectories = nil
	c.directoryBytes = inputbundle.MaxBytes - 2
	if _, err := c.bindFile(context.Background(), binary); err == nil {
		t.Fatal("runtime and artifact combined byte limit ignored")
	}
}

func TestRustRuntimeQueryCannotChangeCapturedLibraries(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lib := filepath.Join(base, "library")
	closurePut(t, lib, "libfixture.so", "healthy")
	query := filepath.Join(base, "rustc")
	trigger := filepath.Join(base, "change-library")
	// This private compiler stand-in returns the same target-libdir even after
	// changing its bytes. The query result alone cannot validate the view.
	script := "#!/bin/sh\nif test -f '" + trigger + "'; then printf modified > '" + filepath.Join(lib, "libfixture.so") + "'; fi\nprintf '%s\\n' '" + lib + "'\n"
	if err := os.WriteFile(query, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	c := Closure{dir: base, env: []string{"RUSTC=" + query}, libraryQuery: lib, requestDigest: closureIdentity([]any{model.Workspace{}, model.Context{}})}
	if _, _, err := c.bindInvocation(context.Background(), query); err != nil {
		t.Fatal(err)
	}
	if err := c.captureRuntimeDirectory(context.Background(), lib); err != nil {
		t.Fatal(err)
	}
	if err := c.verifyBindings(context.Background()); err != nil {
		t.Fatal("healthy query and captured libraries", err)
	}
	if err := os.WriteFile(trigger, []byte("trigger"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.verifyBindings(context.Background()); err == nil || !strings.Contains(err.Error(), "runtime directory changed") {
		t.Fatal("compiler query changed runtime bytes without rejection", err)
	}
	actual, err := os.ReadFile(filepath.Join(lib, "libfixture.so"))
	if err != nil || string(actual) != "modified" {
		t.Fatal("query mutation did not actually occur")
	}
}
