package capsule

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyberlane/hayaku/internal/inputbundle"
)

func TestGeneratedLinkedWorkspaceEnvelopeConsumedAndChangesInvalidate(t *testing.T) {
	module, compiled := compileWASI(t, "./internal/capsule/testdata/guest/main.go", []string{"go.mod", "go.sum", "internal/capsule/testdata/guest/main.go"})
	project, dependency := t.TempDir(), t.TempDir()
	for _, directory := range []string{filepath.Join(project, "generated"), filepath.Join(project, "node_modules"), filepath.Join(dependency, "src", "empty")} {
		if err := os.MkdirAll(directory, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(project, "generated", "value.txt"), []byte("generated"), 0644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dependency, "src", "value.txt")
	if err := os.WriteFile(target, []byte("dependency"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dependency, filepath.Join(project, "node_modules", "pkg")); err != nil {
		t.Skip("platform cannot create linked workspace fixture")
	}
	if err := os.Symlink(target, filepath.Join(project, "generated", "alias.txt")); err != nil {
		t.Fatal(err)
	}
	envelope := inputbundle.Request{Roots: []inputbundle.Root{{Name: "project", Path: project, Mount: "."}, {Name: "dependency", Path: dependency, Mount: "linked/pkg"}}, Inputs: []inputbundle.Input{{Root: "project", Path: "generated", Generated: true}, {Root: "project", Path: "node_modules"}}}
	files, manifest, err := CaptureBundle(context.Background(), envelope)
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Name: "generated linked WASI", Module: module, Files: files, ProducerIdentity: digest([]byte(compiled + "\x00" + manifest)), Args: []string{"guest.wasm", "bundle", "dependency"}}
	options := Options{CacheDir: filepath.Join(t.TempDir(), "passes")}
	first, err := Run(context.Background(), request, options)
	if err != nil || !first.Passed || !first.Qualified {
		t.Fatalf("captured monorepo inputs=%+v err=%v", first, err)
	}
	second, err := Run(context.Background(), request, options)
	if err != nil || second.Mode != "reused" || !second.Passed {
		t.Fatalf("captured monorepo reuse=%+v err=%v", second, err)
	}
	if err := os.WriteFile(target, []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	changedFiles, changedManifest, err := CaptureBundle(context.Background(), envelope)
	if err != nil {
		t.Fatal(err)
	}
	if changedManifest == manifest {
		t.Fatal("changed linked target did not invalidate original manifest")
	}
	request.Files, request.ProducerIdentity = changedFiles, digest([]byte(compiled+"\x00"+changedManifest))
	request.Args[2] = "changed"
	changed, err := Run(context.Background(), request, options)
	if err != nil || changed.Mode != "executed" || !changed.Passed || changed.Key == first.Key || changed.StdoutDigest == first.StdoutDigest {
		t.Fatalf("changed target reused previous suite=%+v err=%v", changed, err)
	}
}

func TestCapsuleBundleRejectsStaleSourcesCyclesAndEscapes(t *testing.T) {
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "generated.txt"), []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	request := inputbundle.Request{Roots: []inputbundle.Root{{Name: "project", Path: project, Mount: "."}}, Inputs: []inputbundle.Input{{Root: "project", Path: "generated.txt", Generated: true}}}
	bundle, err := inputbundle.Capture(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "generated.txt"), []byte("after"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CapsuleFiles(context.Background(), bundle); err == nil {
		t.Fatal("stale input envelope normalized")
	}
	if _, err := CapsuleFiles(context.Background(), nil); err == nil {
		t.Fatal("missing envelope normalized")
	}
	view := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(view, "escape")); err == nil {
		if _, err := flattenEnvelope(context.Background(), view); err == nil {
			t.Fatal("external staging link normalized")
		}
		if err := os.Remove(filepath.Join(view, "escape")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(".", filepath.Join(view, "cycle")); err != nil {
			t.Fatal(err)
		}
		if _, err := flattenEnvelope(context.Background(), view); err == nil {
			t.Fatal("directory cycle normalized")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := CaptureBundle(ctx, request); err == nil {
		t.Fatal("cancelled envelope capture succeeded")
	}
}

func TestEmptyDirectoryMembershipIsBoundAndNormalized(t *testing.T) {
	module := wasmCommand("", "", nil, nil, false, nil, nil)
	request := validRequest(module)
	request.Files = []File{{Path: "generated", Directory: true}, {Path: "generated/empty", Directory: true}, {Path: "generated/value", Data: []byte("value")}}
	first, err := Run(context.Background(), request, Options{})
	if err != nil || !first.Passed {
		t.Fatal(err)
	}
	guestFS := newMemoryFS(request.Files)
	file, err := guestFS.Open("generated/empty")
	if err != nil {
		t.Fatal(err)
	}
	info, _ := file.Stat()
	file.Close()
	if !info.IsDir() || info.Mode().Perm() != 0555 {
		t.Fatal("empty directory omitted or host mode leaked")
	}
	request.Files = append(request.Files, File{Path: "generated/another-empty", Directory: true})
	second, err := Run(context.Background(), request, Options{})
	if err != nil || second.Key == first.Key || second.InputDigest == first.InputDigest {
		t.Fatal("new empty directory did not invalidate capsule")
	}
	invalid := request
	invalid.Files = []File{{Path: "file", Directory: true, Data: []byte("not directory bytes")}}
	if _, err := Run(context.Background(), invalid, Options{}); err == nil {
		t.Fatal("directory with bytes accepted")
	}
	invalid.Files = []File{{Path: "file", Directory: true}, {Path: "file"}}
	if _, err := Run(context.Background(), invalid, Options{}); err == nil {
		t.Fatal("file/directory collision accepted")
	}
	invalid.Seed = strings.Repeat("z", 64)
	if _, err := Run(context.Background(), invalid, Options{}); err == nil {
		t.Fatal("invalid seeded context accepted")
	}
}
