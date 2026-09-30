package noderuntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
)

func fixture(t *testing.T) model.NodeRuntime {
	t.Helper()
	root := t.TempDir()
	node := filepath.Join(root, "node")
	modules := filepath.Join(root, "node_modules")
	if err := os.WriteFile(node, []byte("explicit-node-runtime"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(modules, "package"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modules, "package", "index.js"), []byte("export const value = 1;"), 0644); err != nil {
		t.Fatal(err)
	}
	return model.NodeRuntime{Node: node, Modules: modules}
}

func TestRuntimeIdentityBindsNodeDependenciesAndModes(t *testing.T) {
	r := fixture(t)
	initial, err := Identity(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	changes := []struct {
		name, path string
		content    []byte
		mode       os.FileMode
	}{
		{"dependency", filepath.Join(r.Modules, "package", "index.js"), []byte("export const value = 2;"), 0644},
		{"node", r.Node, []byte("different-node-runtime"), 0755},
		{"permission", filepath.Join(r.Modules, "package", "index.js"), nil, 0755},
	}
	last := initial
	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			if change.content != nil {
				if err := os.WriteFile(change.path, change.content, change.mode); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Chmod(change.path, change.mode); err != nil {
				t.Fatal(err)
			}
			current, err := Identity(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			if current.Digest == last.Digest {
				t.Fatal("changed runtime input retained identity")
			}
			last = current
		})
	}
}

func TestControlledCopyPreservesInternalLinksAndRejectsStaleIdentity(t *testing.T) {
	r := fixture(t)
	if err := os.Mkdir(filepath.Join(r.Modules, ".bin"), 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(r.Modules, ".bin", "runner")
	if err := os.Symlink("../package/index.js", link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	before, err := Identity(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "node_modules")
	if err := CopyModules(context.Background(), r, destination, before); err != nil {
		t.Fatal(err)
	}
	copied := r
	copied.Modules = destination
	after, err := Identity(context.Background(), copied)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("controlled copy lost content or modes")
	}
	if target, err := os.Readlink(filepath.Join(destination, ".bin", "runner")); err != nil || target != "../package/index.js" {
		t.Fatalf("link changed: %q %v", target, err)
	}
	if err := CopyModules(context.Background(), r, destination, before); err == nil {
		t.Fatal("overwrote existing module directory")
	}
	if err := os.WriteFile(filepath.Join(r.Modules, "package", "index.js"), []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	staleTarget := filepath.Join(t.TempDir(), "node_modules")
	if err := CopyModules(context.Background(), r, staleTarget, before); err == nil {
		t.Fatal("accepted stale identity")
	}
	if _, err := os.Stat(staleTarget); !os.IsNotExist(err) {
		t.Fatal("stale copy became visible")
	}
}

func TestRuntimeRejectsEscapingBrokenAndAbsoluteLinks(t *testing.T) {
	for _, kind := range []string{"escape", "absolute", "broken", "root"} {
		t.Run(kind, func(t *testing.T) {
			r := fixture(t)
			outside := filepath.Join(filepath.Dir(r.Modules), "outside.js")
			if err := os.WriteFile(outside, []byte("unbound"), 0644); err != nil {
				t.Fatal(err)
			}
			target := "../outside.js"
			link := filepath.Join(r.Modules, "link")
			switch kind {
			case "absolute":
				target = outside
			case "broken":
				target = "missing"
			case "root":
				target = r.Modules
				link = filepath.Join(filepath.Dir(r.Modules), "modules-link")
			}
			if err := os.Symlink(target, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if kind == "root" {
				r.Modules = link
			}
			if _, err := Identity(context.Background(), r); err == nil {
				t.Fatal("accepted unsafe module link")
			}
		})
	}
}

func TestRuntimeCancellationMissingAndBounds(t *testing.T) {
	r := fixture(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Identity(cancelled, r); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	fingerprint, err := Identity(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if err := CopyModules(cancelled, r, filepath.Join(t.TempDir(), "node_modules"), fingerprint); !errors.Is(err, context.Canceled) {
		t.Fatalf("copy cancellation lost: %v", err)
	}
	missing := r
	missing.Node = filepath.Join(t.TempDir(), "missing")
	if _, err := Identity(context.Background(), missing); err == nil {
		t.Fatal("accepted missing Node")
	}
	missing = r
	missing.Modules = filepath.Join(t.TempDir(), "missing")
	if _, err := Identity(context.Background(), missing); err == nil {
		t.Fatal("accepted missing modules")
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(r.Node, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := Identity(context.Background(), r); err == nil {
			t.Fatal("accepted nonexecutable Node")
		}
		if err := os.Chmod(r.Node, 0755); err != nil {
			t.Fatal(err)
		}
	}
	large := filepath.Join(r.Modules, "large")
	f, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxBytes + 1); err != nil {
		f.Close()
		t.Skipf("sparse files unavailable: %v", err)
	}
	f.Close()
	if _, err := Identity(context.Background(), r); err == nil {
		t.Fatal("accepted oversized runtime")
	}
}
