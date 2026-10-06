package native

import (
	"context"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/passcache"
	"github.com/Cyberlane/hayaku/internal/process"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Cyberlane/hayaku/internal/snapshot"
)

func TestInstalledGoBuildContext(t *testing.T) {
	tool, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	tool, err = filepath.EvalSymlinks(tool)
	if err != nil {
		t.Fatal(err)
	}
	build, err := captureGoBuild(context.Background(), tool, "arm64")
	if err != nil || build.identity == "" || build.root == "" {
		t.Fatal("installed complete compiler context unavailable", err)
	}
	if err := build.verify(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestGoBuildRejectsCompilerRootRetarget(t *testing.T) {
	tool, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	tool, err = filepath.EvalSymlinks(tool)
	if err != nil {
		t.Fatal(err)
	}
	output, err := runGo(context.Background(), tool, "", buildEnvironment("arm64"), "env", "GOROOT")
	if err != nil {
		t.Fatal(err)
	}
	realRoot, err := filepath.EvalSymlinks(strings.TrimSpace(string(output.Stdout)))
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "compiler")
	if err := os.Symlink(realRoot, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOROOT", alias)
	build, err := captureGoBuild(context.Background(), tool, "arm64")
	if err != nil {
		t.Fatal("healthy compiler alias unavailable", err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), alias); err != nil {
		t.Fatal(err)
	}
	// The resolved original tree is unchanged; only the effective root moved.
	before, err := snapshot.Digest(context.Background(), realRoot)
	if err != nil || before != build.tree {
		t.Fatal("fault altered original compiler tree", err)
	}
	if err := build.verify(context.Background()); err == nil {
		t.Fatal("retargeted compiler alias retained qualification")
	}
}

func TestNativeProgramDigestCancellation(t *testing.T) {
	file := filepath.Join(t.TempDir(), "program")
	if err := os.WriteFile(file, []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := fileDigestContext(context.Background(), file)
	if err != nil || got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatal("raw program identity changed", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fileDigestContext(ctx, file); err == nil {
		t.Fatal("cancelled authority read accepted")
	}
}

func TestGoProducerRetainsPrivateInputs(t *testing.T) {
	ctx := context.Background()
	root, source, tools := t.TempDir(), t.TempDir(), t.TempDir()
	for _, dir := range []string{root, source} {
		write(t, dir, "go.mod", "module sample\ngo 1.26\n")
		write(t, dir, "sample_test.go", "package sample; import \"testing\"; func TestValue(t *testing.T){if 1!=1{t.Fatal(\"wrong\")}}")
	}
	write(t, tools, "go", "synthetic compiler bytes")
	write(t, tools, "src/runtime.go", "synthetic runtime bytes")
	tool := filepath.Join(tools, "go")
	compiler, err := fileDigest(tool)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := snapshot.Digest(ctx, tools)
	if err != nil {
		t.Fatal(err)
	}
	build := goBuildContext{tool: tool, root: tools, executable: compiler, tree: tree, identity: identity([]string{compiler, tree})}
	files, err := packageSources(source)
	if err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(t.TempDir(), "program")
	if err := os.WriteFile(program, []byte("synthetic program bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	a := Artifact{Name: ".", Program: program, Proof: goSourceProof(files), Cases: []string{"TestValue"}, Args: []string{"-test.v", "-test.count=1", "-test.parallel=1", "-test.timeout=60s"}, Env: []string{"GOMAXPROCS=1", "GODEBUG=randautoseed=0", "TZ=UTC"}}
	backend := Backend{Identity: identity("synthetic backend")}
	sealed, err := issueGoArtifact(ctx, backend, a, build, root, source, source)
	if err != nil {
		t.Fatal(err)
	}
	// The producer must clone its input slices before retaining authority.
	a.Cases[0], a.Args[0], a.Env[0] = "TestForged", "-changed", "TZ=changed"
	if !slices.Equal(sealed.Cases, []string{"TestValue"}) || sealed.Args[0] != "-test.v" || sealed.Env[0] != "GOMAXPROCS=1" {
		t.Fatal("caller aliases changed retained inputs")
	}
	if _, _, err := verifyAuthority(ctx, backend, sealed); err != nil {
		t.Fatal("healthy synthetic producer admission unavailable", err)
	}
	for name, mutate := range map[string]func(){
		"runtime": func() { write(t, tools, "src/runtime.go", "changed runtime bytes") },
		"original-source": func() {
			write(t, root, "sample_test.go", "package sample; import \"testing\"; func TestValue(t *testing.T){t.Fatal(\"new\")}")
		},
		"copied-source": func() {
			write(t, source, "sample_test.go", "package sample; import \"testing\"; func TestValue(t *testing.T){t.Fatal(\"new\")}")
		},
		"original-manifest": func() { write(t, root, "go.mod", "module changed\ngo 1.26\n") },
		"copied-manifest":   func() { write(t, source, "go.sum", "new dependency bytes") },
	} {
		t.Run(name, func(t *testing.T) {
			before, err := os.ReadFile(filepath.Join(tools, "src/runtime.go"))
			if err != nil {
				t.Fatal(err)
			}
			original, _ := os.ReadFile(filepath.Join(root, "sample_test.go"))
			copied, _ := os.ReadFile(filepath.Join(source, "sample_test.go"))
			manifest, _ := os.ReadFile(filepath.Join(root, "go.mod"))
			mutate()
			if _, _, err := verifyAuthority(ctx, backend, sealed); err == nil {
				t.Fatal("live producer drift retained authority")
			}
			write(t, tools, "src/runtime.go", string(before))
			write(t, root, "sample_test.go", string(original))
			write(t, source, "sample_test.go", string(copied))
			write(t, root, "go.mod", string(manifest))
			if err := os.Remove(filepath.Join(source, "go.sum")); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if _, _, err := verifyAuthority(ctx, backend, sealed); err != nil {
				t.Fatal("healthy restored producer unavailable", err)
			}
		})
	}
}

func TestInstalledGoProducerIgnoresCallerDecoder(t *testing.T) {
	image := os.Getenv("HAYAKU_NATIVE_IMAGE")
	if image == "" {
		t.Skip("installed immutable Linux image required")
	}
	arch := os.Getenv("HAYAKU_NATIVE_ARCH")
	if arch == "" {
		arch = "arm64"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	tool, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	tool, err = filepath.EvalSymlinks(tool)
	if err != nil {
		t.Fatal(err)
	}
	build, err := captureGoBuild(ctx, tool, arch)
	if err != nil {
		t.Fatal(err)
	}
	temporary, root, source := t.TempDir(), t.TempDir(), t.TempDir()
	worker, err := prepareWorkerEnvironment(ctx, tool, temporary, arch, build.env)
	if err != nil {
		t.Fatal(err)
	}
	docker, err := ResolveDocker()
	if err != nil {
		t.Fatal(err)
	}
	backend := Backend{Docker: docker, Image: image, Worker: worker, Architecture: arch}
	if err := backend.Bind(ctx); err != nil {
		t.Fatal(err)
	}
	cacheDir := filepath.Join(t.TempDir(), "cache")
	for _, pass := range []bool{true, false} {
		condition := "1==1"
		if !pass {
			condition = "1!=1"
		}
		for _, dir := range []string{root, source} {
			write(t, dir, "go.mod", "module sample\ngo 1.26\n")
			write(t, dir, "sample_test.go", "package sample; import \"testing\"; func TestValue(t *testing.T){if !("+condition+"){t.Fatal(\"wrong\")}}")
		}
		program := filepath.Join(temporary, "program")
		if _, err := runGo(ctx, tool, source, build.env, "test", "-c", "-trimpath", "-buildvcs=false", "-o", program, "."); err != nil {
			t.Fatal(err)
		}
		files, err := packageSources(source)
		if err != nil {
			t.Fatal(err)
		}
		a := Artifact{Name: ".", Program: program, Proof: goSourceProof(files), Cases: []string{"TestValue"}, Args: []string{"-test.v", "-test.count=1", "-test.parallel=1", "-test.timeout=60s"}, Env: []string{"GOMAXPROCS=1", "GODEBUG=randautoseed=0", "TZ=UTC"}}
		a, err = issueGoArtifact(ctx, backend, a, build, root, source, source)
		if err != nil {
			t.Fatal(err)
		}
		result, err := Execute(ctx, backend, a, cacheDir, false, func(process.Output, Artifact) (model.Outcomes, error) {
			t.Fatal("caller decoder acquired receipt authority")
			return model.Outcomes{}, nil
		})
		if (err == nil) != pass || !result.Complete || result.Passed != pass || !result.Qualified || !result.Executed || result.Reused || result.Key == "" {
			t.Fatal("reviewed decoder lost raw terminal outcome", err)
		}
		want := "pass"
		if !pass {
			want = "fail"
		}
		if len(result.Outcomes.Tests) != 1 || result.Outcomes.Tests[0].Action != want {
			t.Fatal("raw case outcome was replaced")
		}
		cache, err := passcache.Open(cacheDir, "hayaku-native-pure-pass-v1")
		if err != nil {
			t.Fatal(err)
		}
		_, status := cache.Load(result.Key)
		cache.Close()
		if pass && status != "hit" || !pass && status == "hit" {
			t.Fatal("failing artifact acquired reusable pass")
		}
	}
}
