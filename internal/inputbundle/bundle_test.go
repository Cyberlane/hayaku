package inputbundle

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func fixture(t *testing.T) (Request, string) {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "generated/value.json"), `{"value":1}`, 0640)
	write(t, filepath.Join(root, "generated/bin"), "executable", 0750)
	if err := os.Mkdir(filepath.Join(root, "generated/empty"), 0750); err != nil {
		t.Fatal(err)
	}
	return Request{Roots: []Root{{Name: "project", Path: root, Mount: "project"}}, Inputs: []Input{{Root: "project", Path: "generated", Generated: true}}}, root
}

func write(t *testing.T, name, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(name, mode); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureStableImmutableAndVerifiedCopy(t *testing.T) {
	request, root := fixture(t)
	one, err := Capture(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	two, err := Capture(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if one.Manifest().Digest != two.Manifest().Digest {
		t.Fatal("stable source produced different identities")
	}
	manifest := one.Manifest()
	manifest.Entries[0].Path = "tampered"
	manifest.Roots[0].Path = "tampered"
	manifest.Inputs[0].Path = "tampered"
	if reflect.DeepEqual(manifest, one.Manifest()) {
		t.Fatal("manifest aliases private inventory")
	}
	destination := filepath.Join(t.TempDir(), "bundle")
	if err := one.Materialize(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	if err := one.VerifyMaterialization(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(destination, "project/generated/value.json"))
	if err != nil || string(contents) != `{"value":1}` {
		t.Fatalf("copy: %q %v", contents, err)
	}
	for name, mode := range map[string]os.FileMode{".": 0700, "project/generated/value.json": 0640, "project/generated/bin": 0750, "project/generated/empty": 0750} {
		info, err := os.Stat(filepath.Join(destination, filepath.FromSlash(name)))
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("mode %s: %v %v", name, info, err)
		}
	}
	encoded, err := json.Marshal(one.Manifest())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), root) || strings.Contains(string(encoded), `{"value":1}`) {
		t.Fatal("manifest discloses machine paths or source contents")
	}
	decoded, err := DecodeManifest(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, one.Manifest()) {
		t.Fatal("saved identity changed")
	}
}

func TestDeclaredTreeMutationAlwaysInvalidates(t *testing.T) {
	for _, change := range []string{"new", "deleted", "bytes", "mode", "link"} {
		t.Run(change, func(t *testing.T) {
			request, root := fixture(t)
			bundle, err := Capture(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(root, "generated/value.json")
			switch change {
			case "new":
				write(t, filepath.Join(root, "generated/new"), "new", 0600)
			case "deleted":
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
			case "bytes":
				write(t, name, `{"value":2}`, 0640)
			case "mode":
				if err := os.Chmod(name, 0600); err != nil {
					t.Fatal(err)
				}
			case "link":
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("bin", name); err != nil {
					t.Skip(err)
				}
			}
			if err := bundle.VerifySources(context.Background()); err == nil {
				t.Fatal("changed envelope verified")
			}
			if err := bundle.Materialize(context.Background(), filepath.Join(t.TempDir(), "copy")); err == nil {
				t.Fatal("changed source materialized")
			}
		})
	}
}

func TestLinkedWorkspaceCaptureRewritesOnlyDeclaredRoots(t *testing.T) {
	root, dependency := t.TempDir(), t.TempDir()
	write(t, filepath.Join(root, "generated/value"), "generated", 0644)
	write(t, filepath.Join(dependency, "src/value.js"), "dependency", 0644)
	if err := os.Mkdir(filepath.Join(root, "node_modules"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dependency, filepath.Join(root, "node_modules/pkg")); err != nil {
		t.Skip(err)
	}
	request := Request{Roots: []Root{{Name: "project", Path: root, Mount: "."}, {Name: "dependency", Path: dependency, Mount: "linked/pkg"}}, Inputs: []Input{{Root: "project", Path: "node_modules"}, {Root: "project", Path: "generated", Generated: true}}}
	bundle, err := Capture(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	manifest := bundle.Manifest()
	var link Entry
	for _, entry := range manifest.Entries {
		if entry.Path == "node_modules/pkg" {
			link = entry
		}
	}
	if link.Link != "" || !validDigest(link.LinkSHA256) || link.MaterializedLink != "../linked/pkg" {
		t.Fatalf("incorrect absolute link binding: %+v", link)
	}
	encoded, _ := json.Marshal(manifest)
	if strings.Contains(string(encoded), dependency) {
		t.Fatal("absolute source link leaked")
	}
	if _, err := DecodeManifest(bytes.NewReader(encoded)); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "copy")
	if err := bundle.Materialize(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(destination, "node_modules/pkg/src/value.js"))
	if err != nil || string(contents) != "dependency" {
		t.Fatalf("linked source: %q %v", contents, err)
	}
	write(t, filepath.Join(dependency, "src/new.js"), "new", 0644)
	if err := bundle.VerifySources(context.Background()); err == nil {
		t.Fatal("new linked input ignored")
	}
}

func TestInternalSymlinkTargetIdentityAndCycles(t *testing.T) {
	request, root := fixture(t)
	link := filepath.Join(root, "generated/link")
	if err := os.Symlink("value.json", link); err != nil {
		t.Skip(err)
	}
	bundle, err := Capture(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.Materialize(context.Background(), filepath.Join(t.TempDir(), "copy")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("bin", link); err != nil {
		t.Fatal(err)
	}
	if err := bundle.VerifySources(context.Background()); err == nil {
		t.Fatal("changed link target ignored")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("other", link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("link", filepath.Join(root, "generated/other")); err != nil {
		t.Fatal(err)
	}
	if _, err := Capture(context.Background(), request); err == nil {
		t.Fatal("symlink cycle accepted")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "generated/other")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".", link); err != nil {
		t.Fatal(err)
	}
	if _, err := Capture(context.Background(), request); err == nil {
		t.Fatal("directory cycle accepted")
	}
}

func TestUnsafeInputsAndRootCollisionsRejected(t *testing.T) {
	for _, scenario := range []string{"missing", "escape", "broken", "ancestor-link", "source-overlap", "mount-overlap", "duplicate", "special-mode", "noncanonical-link"} {
		t.Run(scenario, func(t *testing.T) {
			request, root := fixture(t)
			outside := t.TempDir()
			write(t, filepath.Join(outside, "value"), "outside", 0600)
			switch scenario {
			case "missing":
				request.Inputs[0].Path = "absent"
			case "escape":
				if err := os.Symlink(outside, filepath.Join(root, "generated/link")); err != nil {
					t.Skip(err)
				}
			case "broken":
				if err := os.Symlink("missing", filepath.Join(root, "generated/link")); err != nil {
					t.Skip(err)
				}
			case "ancestor-link":
				if err := os.Symlink(outside, filepath.Join(root, "alias")); err != nil {
					t.Skip(err)
				}
				request.Inputs[0].Path = "alias/value"
			case "source-overlap":
				request.Roots = append(request.Roots, Root{Name: "nested", Path: filepath.Join(root, "generated"), Mount: "nested"})
			case "mount-overlap":
				request.Roots = append(request.Roots, Root{Name: "outside", Path: outside, Mount: "project/nested"})
			case "duplicate":
				request.Inputs = append(request.Inputs, request.Inputs[0])
			case "special-mode":
				if err := os.Chmod(filepath.Join(root, "generated/bin"), 0755|os.ModeSetuid); err != nil {
					t.Skip(err)
				}
			case "noncanonical-link":
				if err := os.Symlink("empty/../value.json", filepath.Join(root, "generated/link")); err != nil {
					t.Skip(err)
				}
			}
			if _, err := Capture(context.Background(), request); err == nil {
				t.Fatal("unsafe declaration accepted")
			}
		})
	}
}

func TestMaterializationRejectsOverwriteAndTampering(t *testing.T) {
	request, root := fixture(t)
	bundle, err := Capture(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.Materialize(context.Background(), filepath.Join(root, "copy")); err == nil {
		t.Fatal("source tree destination accepted")
	}
	destination := t.TempDir()
	write(t, filepath.Join(destination, "sentinel"), "keep", 0600)
	if err := bundle.Materialize(context.Background(), destination); err == nil {
		t.Fatal("existing destination accepted")
	}
	data, err := os.ReadFile(filepath.Join(destination, "sentinel"))
	if err != nil || string(data) != "keep" {
		t.Fatal("existing destination modified")
	}
	for _, scenario := range []string{"bytes", "new", "missing", "mode", "escape"} {
		t.Run(scenario, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "copy")
			if err := bundle.Materialize(context.Background(), destination); err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(destination, "project/generated/value.json")
			switch scenario {
			case "bytes":
				write(t, name, "different", 0640)
			case "new":
				write(t, filepath.Join(destination, "extra"), "extra", 0600)
			case "missing":
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(name, 0600); err != nil {
					t.Fatal(err)
				}
			case "escape":
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, "generated/value.json"), name); err != nil {
					t.Skip(err)
				}
			}
			if err := bundle.VerifyMaterialization(context.Background(), destination); err == nil {
				t.Fatal("tampered copy verified")
			}
		})
	}
}

func TestInputBoundsCancellationAndPrivateFailureCleanup(t *testing.T) {
	for _, limits := range []Limits{{Entries: 2}, {FileBytes: 2}, {Bytes: 2}, {Depth: 1}, {Entries: -1}, {FileBytes: MaxFileBytes + 1}} {
		request, _ := fixture(t)
		request.Limits = limits
		if _, err := Capture(context.Background(), request); err == nil {
			t.Fatalf("limits accepted: %+v", limits)
		}
	}
	request, _ := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Capture(ctx, request); err != context.Canceled {
		t.Fatalf("capture cancellation: %v", err)
	}
	bundle, err := Capture(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "copy")
	if err := bundle.Materialize(ctx, destination); err != context.Canceled {
		t.Fatalf("materialization cancellation: %v", err)
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatal("canceled materialization left destination")
	}
}

func TestStrictSavedManifest(t *testing.T) {
	request, _ := fixture(t)
	bundle, err := Capture(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(bundle.Manifest())
	for _, invalid := range [][]byte{
		append(append([]byte(nil), encoded...), []byte(" {}")...),
		bytes.Replace(encoded, []byte(`"schema":1`), []byte(`"schema":1,"schema":1`), 1),
		bytes.Replace(encoded, []byte(`"schema":1`), []byte(`"schema":1,"unknown":1`), 1),
		bytes.Replace(encoded, []byte(`"generated":true`), []byte(`"generated":false`), 1),
		[]byte(`{"schema":1}`),
		[]byte(`null`),
	} {
		if _, err := DecodeManifest(bytes.NewReader(invalid)); err == nil {
			t.Fatal("malformed or changed manifest accepted")
		}
	}
	for _, change := range []string{"duplicate", "parent", "link-escape", "unknown-kind", "mode"} {
		manifest := bundle.Manifest()
		switch change {
		case "duplicate":
			manifest.Entries = append(manifest.Entries, manifest.Entries[len(manifest.Entries)-1])
		case "parent":
			manifest.Entries = append(manifest.Entries[:1], manifest.Entries[2:]...)
		case "link-escape":
			manifest.Entries[len(manifest.Entries)-1] = Entry{Path: "project/generated/value.json", Kind: "symlink", Mode: 0777, LinkSHA256: strings.Repeat("a", 64), MaterializedLink: "../../../escape"}
		case "unknown-kind":
			manifest.Entries[0].Kind = "device"
		case "mode":
			manifest.Entries[0].Mode = 0755
		}
		if err := ValidateManifest(manifest); err == nil {
			t.Fatalf("invalid inventory accepted: %s", change)
		}
	}
}

func TestSelectionOrderAndProvenanceAreBound(t *testing.T) {
	root, other := t.TempDir(), t.TempDir()
	write(t, filepath.Join(root, "a"), "a", 0644)
	write(t, filepath.Join(other, "b"), "b", 0644)
	request := Request{Roots: []Root{{Name: "a-b", Path: other, Mount: "other"}, {Name: "a", Path: root, Mount: "project"}}, Inputs: []Input{{Root: "a-b", Path: "b"}, {Root: "a", Path: "a", Generated: true}}}
	first, err := Capture(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Roots[0], request.Roots[1] = request.Roots[1], request.Roots[0]
	request.Inputs[0], request.Inputs[1] = request.Inputs[1], request.Inputs[0]
	second, err := Capture(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Manifest().Digest != second.Manifest().Digest {
		t.Fatal("request ordering affected digest")
	}
	encoded, _ := json.Marshal(first.Manifest())
	if _, err := DecodeManifest(bytes.NewReader(encoded)); err != nil {
		t.Fatal(err)
	}
	request.Inputs[0].Generated = false
	third, err := Capture(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Manifest().Digest == third.Manifest().Digest {
		t.Fatal("generated provenance not bound")
	}
}

// The context callback provides deterministic fault injection at a cancellation
// checkpoint, without sleeps or a competing filesystem writer.
type faultContext struct {
	context.Context
	calls int
	at    int
	fault func()
}

func (c *faultContext) Err() error {
	c.calls++
	if c.calls == c.at {
		c.fault()
	}
	return c.Context.Err()
}

func TestMutationDuringCaptureAndCancellationAfterReservation(t *testing.T) {
	request, root := fixture(t)
	ctx := &faultContext{Context: context.Background(), at: 5, fault: func() {
		write(t, filepath.Join(root, "generated/bin"), "different!", 0750)
	}}
	if _, err := Capture(ctx, request); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("mutation during capture: %v", err)
	}
	bundle, err := Capture(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	destination := filepath.Join(parent, "copy")
	cancellable, cancel := context.WithCancel(context.Background())
	// Cancel at the first checkpoint after the destination is reserved.
	probe := &reservationContext{Context: cancellable, destination: destination, cancel: cancel}
	if err := bundle.Materialize(probe, destination); err != context.Canceled {
		t.Fatalf("cancellation after reservation: %v", err)
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatal("partial private copy retained after cancellation")
	}
}

type reservationContext struct {
	context.Context
	destination string
	cancel      context.CancelFunc
}

func (c *reservationContext) Err() error {
	if _, err := os.Lstat(c.destination); err == nil {
		c.cancel()
	}
	return c.Context.Err()
}

func TestCommittedMountCollisionCannotSilentlyOverwrite(t *testing.T) {
	project, dependency := t.TempDir(), t.TempDir()
	write(t, filepath.Join(project, "linked/pkg/value"), "project", 0644)
	write(t, filepath.Join(dependency, "value"), "dependency", 0644)
	request := Request{Roots: []Root{{Name: "project", Path: project, Mount: "."}, {Name: "dependency", Path: dependency, Mount: "linked/pkg"}}, Inputs: []Input{{Root: "project", Path: "linked"}, {Root: "dependency", Path: "."}}}
	if _, err := Capture(context.Background(), request); err == nil {
		t.Fatal("two source trees merged at one materialized path")
	}
	contents, err := os.ReadFile(filepath.Join(project, "linked/pkg/value"))
	if err != nil || string(contents) != "project" {
		t.Fatal("collision changed source input")
	}
}
