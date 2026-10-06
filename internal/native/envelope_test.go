package native

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Cyberlane/hayaku/internal/inputbundle"
)

func envelopeFixture(t *testing.T) (EnvelopeRequest, string) {
	t.Helper()
	root := t.TempDir()
	write(t, root, "data/value", "fixture")
	return EnvelopeRequest{
		Files: inputbundle.Request{
			Roots:  []inputbundle.Root{{Name: "source", Path: root, Mount: "."}},
			Inputs: []inputbundle.Input{{Root: "source", Path: "data"}},
		},
		Absent: []string{"data/missing", "data/missing/deep"},
		Env:    []string{"DECLARED=fixture"},
	}, root
}

func captureFixtureEnvelope(t *testing.T, request EnvelopeRequest) *Envelope {
	t.Helper()
	e, err := CaptureEnvelope(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestNativeEnvelopeNormalizedCopy(t *testing.T) {
	request, root := envelopeFixture(t)
	if err := os.Symlink("value", filepath.Join(root, "data/link")); err != nil {
		t.Fatal(err)
	}
	request.ScratchBytes = 1 << 20
	e := captureFixtureEnvelope(t, request)
	destination := filepath.Join(t.TempDir(), "resources")
	if err := e.materialize(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	defer removeEnvelopeCopy(destination)
	// Expected data, names, absence, link text and modes come from the fixture,
	// independently of the captured manifest and verifier.
	data, err := os.ReadFile(filepath.Join(destination, "data/link"))
	if err != nil || string(data) != "fixture" {
		t.Fatalf("resource bytes: %q %v", data, err)
	}
	link, err := os.Readlink(filepath.Join(destination, "data/link"))
	if err != nil || link != "value" {
		t.Fatalf("relative link: %q %v", link, err)
	}
	for _, name := range []string{".", "data"} {
		info, err := os.Stat(filepath.Join(destination, name))
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0555 {
			t.Fatalf("directory mode %s: %v %v", name, info, err)
		}
	}
	info, err := os.Stat(filepath.Join(destination, "data/value"))
	if err != nil || info.Mode().Perm() != 0444 {
		t.Fatalf("file mode: %v %v", info, err)
	}
	for _, name := range request.Absent {
		if _, err := os.Lstat(filepath.Join(destination, name)); !os.IsNotExist(err) {
			t.Fatalf("absence %s differs: %v", name, err)
		}
	}
	children, err := os.ReadDir(filepath.Join(destination, "data"))
	if err != nil || len(children) != 2 || children[0].Name() != "link" || children[1].Name() != "value" {
		t.Fatalf("membership: %v %v", children, err)
	}
	if err := e.verifyMaterialization(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
}

func TestNativeEnvelopeRejectsUncapturedAbsence(t *testing.T) {
	for _, name := range []string{"uncaptured", "data/value/missing", "data/linked/missing", "data/value", "data/missing"} {
		t.Run(name, func(t *testing.T) {
			request, root := envelopeFixture(t)
			if err := os.Symlink("value", filepath.Join(root, "data/linked")); err != nil {
				t.Fatal(err)
			}
			request.Absent = []string{name}
			if name == "data/missing" {
				// Capturing a file synthesizes parent entries, but does not
				// establish the parent's complete source membership.
				request.Files.Inputs[0].Path = "data/value"
			}
			if _, err := CaptureEnvelope(context.Background(), request); err == nil {
				t.Fatal("incomplete or conflicting absence accepted")
			}
		})
	}
}

func TestNativeEnvelopeRejectsRewrittenLinks(t *testing.T) {
	request, root := envelopeFixture(t)
	if err := os.Symlink(filepath.Join(root, "data/value"), filepath.Join(root, "data/absolute")); err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureEnvelope(context.Background(), request); err == nil {
		t.Fatal("absolute native link was silently normalized")
	}
}

func TestNativeEnvelopeSourceDrift(t *testing.T) {
	mutations := map[string]func(*testing.T, string){
		"bytes":          func(t *testing.T, root string) { write(t, root, "data/value", "changed") },
		"absent-created": func(t *testing.T, root string) { write(t, root, "data/missing", "new") },
		"membership":     func(t *testing.T, root string) { write(t, root, "data/other", "new") },
		"mode": func(t *testing.T, root string) {
			if err := os.Chmod(filepath.Join(root, "data/value"), 0644); err != nil {
				t.Fatal(err)
			}
		},
		"deleted": func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, "data/value")); err != nil {
				t.Fatal(err)
			}
		},
		"link": func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, "data/link")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("other", filepath.Join(root, "data/link")); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			request, root := envelopeFixture(t)
			write(t, root, "data/other", "fixture")
			if err := os.Symlink("value", filepath.Join(root, "data/link")); err != nil {
				t.Fatal(err)
			}
			e := captureFixtureEnvelope(t, request)
			mutate(t, root)
			if err := e.VerifySources(context.Background()); err == nil {
				t.Fatal("source drift accepted")
			}
			if err := e.materialize(context.Background(), filepath.Join(t.TempDir(), "copy")); err == nil {
				t.Fatal("drifted source materialized")
			}
		})
	}
}

func TestNativeEnvelopeCopyTampering(t *testing.T) {
	for _, kind := range []string{"bytes", "mode", "added", "deleted", "escape-link"} {
		t.Run(kind, func(t *testing.T) {
			request, _ := envelopeFixture(t)
			e := captureFixtureEnvelope(t, request)
			destination := filepath.Join(t.TempDir(), "copy")
			if err := e.materialize(context.Background(), destination); err != nil {
				t.Fatal(err)
			}
			defer removeEnvelopeCopy(destination)
			if err := os.Chmod(filepath.Join(destination, "data"), 0755); err != nil {
				t.Fatal(err)
			}
			value := filepath.Join(destination, "data/value")
			switch kind {
			case "bytes":
				if err := os.Chmod(value, 0644); err != nil {
					t.Fatal(err)
				}
				write(t, destination, "data/value", "changed")
				if err := os.Chmod(value, 0444); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(value, 0400); err != nil {
					t.Fatal(err)
				}
			case "added":
				write(t, destination, "data/unexpected", "new")
			case "deleted":
				if err := os.Remove(value); err != nil {
					t.Fatal(err)
				}
			case "escape-link":
				if err := os.Remove(value); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("/etc/passwd", value); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Chmod(filepath.Join(destination, "data"), 0555); err != nil {
				t.Fatal(err)
			}
			if err := e.verifyMaterialization(context.Background(), destination); err == nil {
				t.Fatal("tampered copy accepted")
			}
		})
	}
}

func TestNativeEnvelopePolicyValidation(t *testing.T) {
	changes := map[string]func(*EnvelopeRequest){
		"negative-scratch":  func(r *EnvelopeRequest) { r.ScratchBytes = -1 },
		"huge-scratch":      func(r *EnvelopeRequest) { r.ScratchBytes = MaxScratchBytes + 1 },
		"duplicate-env":     func(r *EnvelopeRequest) { r.Env = []string{"A=one", "A=two"} },
		"missing-equals":    func(r *EnvelopeRequest) { r.Env = []string{"A"} },
		"empty-key":         func(r *EnvelopeRequest) { r.Env = []string{"=value"} },
		"nul-env":           func(r *EnvelopeRequest) { r.Env = []string{"A=\x00"} },
		"invalid-utf8":      func(r *EnvelopeRequest) { r.Env = []string{"A=" + string([]byte{255})} },
		"oversized-env":     func(r *EnvelopeRequest) { r.Env = []string{"A=" + strings.Repeat("x", 65536)} },
		"many-env":          func(r *EnvelopeRequest) { r.Env = make([]string, 129) },
		"escape":            func(r *EnvelopeRequest) { r.Absent = []string{"../outside"} },
		"absolute":          func(r *EnvelopeRequest) { r.Absent = []string{"/outside"} },
		"duplicate-absence": func(r *EnvelopeRequest) { r.Absent = []string{"data/missing", "data/missing"} },
		"many-absence":      func(r *EnvelopeRequest) { r.Absent = make([]string, inputbundle.MaxEntries+1) },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			request, _ := envelopeFixture(t)
			change(&request)
			if _, err := CaptureEnvelope(context.Background(), request); err == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
}

func TestNativeEnvelopeIdentityAndPrivateValues(t *testing.T) {
	request, _ := envelopeFixture(t)
	e := captureFixtureEnvelope(t, request)
	baseline := e.Digest()
	request.Env[0] = "DECLARED=other"
	request.Absent[0] = "uncaptured"
	request.Files.Inputs[0].Path = "data/value"
	if e.Digest() != baseline || e.VerifySources(context.Background()) != nil || e.env[0] != "DECLARED=fixture" {
		t.Fatal("caller mutation changed retained capture")
	}
	e.env[0] = "DECLARED=changed"
	if e.VerifySources(context.Background()) == nil {
		t.Fatal("private environment drift accepted")
	}
	for _, mutation := range []func(*EnvelopeRequest){
		func(r *EnvelopeRequest) { r.Env[0] = "DECLARED=other" },
		func(r *EnvelopeRequest) { r.ScratchBytes = 1 << 20 },
		func(r *EnvelopeRequest) { r.Absent = []string{"data/different"} },
		func(r *EnvelopeRequest) { r.Files.Inputs[0].Generated = true },
	} {
		request, _ := envelopeFixture(t)
		before := captureFixtureEnvelope(t, request).Digest()
		mutation(&request)
		if captureFixtureEnvelope(t, request).Digest() == before {
			t.Fatal("policy/provenance identity missing")
		}
	}
}

func TestNativeEnvelopeCancellation(t *testing.T) {
	request, _ := envelopeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CaptureEnvelope(ctx, request); err == nil {
		t.Fatal("cancelled capture accepted")
	}
	e := captureFixtureEnvelope(t, request)
	destination := filepath.Join(t.TempDir(), "copy")
	if err := e.materialize(ctx, destination); err == nil {
		t.Fatal("cancelled copy accepted")
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatal("cancelled copy was published")
	}
	if (&Envelope{}).VerifySources(context.Background()) == nil {
		t.Fatal("empty envelope accepted")
	}
}
