package native

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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
	request.Files.Roots[0] = inputbundle.Root{Name: "changed", Path: t.TempDir(), Mount: "other"}
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

func TestNativeEnvelopeFilesystemAbsenceAliases(t *testing.T) {
	for _, names := range [][2]string{{"lower", "LOWER"}, {"caf\u00e9", "cafe\u0301"}} {
		t.Run(names[0], func(t *testing.T) {
			request, root := envelopeFixture(t)
			write(t, root, "data/"+names[0], "alias fixture")
			request.Absent = []string{"data/" + names[1]}
			// Observe the filesystem independently; case-sensitive hosts must
			// retain distinct names instead of conservatively case-folding them.
			data, lookupErr := os.ReadFile(filepath.Join(root, "data", names[1]))
			e, err := CaptureEnvelope(context.Background(), request)
			if lookupErr == nil {
				if string(data) != "alias fixture" || err == nil {
					t.Fatal("actual existing alias accepted as absent")
				}
				if strings.Contains(err.Error(), root) {
					t.Fatal("private source path leaked")
				}
				t.Log("actual filesystem alias exists; absence rejected")
				return
			}
			if !errors.Is(lookupErr, syscall.ENOENT) || err != nil {
				t.Fatalf("distinct name absence: %v %v", lookupErr, err)
			}
			t.Log("actual filesystem names are distinct; absence admitted")
			destination := filepath.Join(t.TempDir(), "copy")
			if err := e.materialize(context.Background(), destination); err != nil {
				t.Fatal(err)
			}
			defer removeEnvelopeCopy(destination)
			data, err = os.ReadFile(filepath.Join(destination, "data", names[0]))
			if err != nil || string(data) != "alias fixture" {
				t.Fatalf("distinct existing name changed: %q %v", data, err)
			}
			if _, err := os.Lstat(filepath.Join(destination, "data", names[1])); !errors.Is(err, syscall.ENOENT) {
				t.Fatalf("distinct absent name changed: %v", err)
			}
		})
	}
}

func TestNativeEnvelopeAbsenceAliasAncestors(t *testing.T) {
	for _, kind := range []string{"symlink", "file"} {
		t.Run(kind, func(t *testing.T) {
			request, root := envelopeFixture(t)
			if kind == "symlink" {
				if err := os.Mkdir(filepath.Join(root, "data/directory"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("directory", filepath.Join(root, "data/lower")); err != nil {
					t.Fatal(err)
				}
			} else {
				write(t, root, "data/lower", "file parent")
			}
			request.Absent = []string{"data/LOWER/missing"}
			_, lookupErr := os.Lstat(filepath.Join(root, "data/LOWER"))
			_, err := CaptureEnvelope(context.Background(), request)
			if lookupErr == nil {
				if err == nil || !strings.Contains(err.Error(), "non-directory") {
					t.Fatalf("alias ancestor accepted: %v", err)
				}
			} else if !errors.Is(lookupErr, syscall.ENOENT) || err != nil {
				t.Fatalf("distinct absent ancestor rejected: %v %v", lookupErr, err)
			}
		})
	}
}

func TestNativeEnvelopeCopyChecksActualAbsence(t *testing.T) {
	request, root := envelopeFixture(t)
	if err := os.Mkdir(filepath.Join(root, "data/directory"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("directory", filepath.Join(root, "data/link")); err != nil {
		t.Fatal(err)
	}
	e := captureFixtureEnvelope(t, request)
	destination := filepath.Join(t.TempDir(), "copy")
	if err := e.materialize(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	defer removeEnvelopeCopy(destination)
	// Keep the entire captured tree healthy. A different asserted absence must
	// be probed in the copy itself, rather than relying on membership matching
	// or source verification to notice it.
	for _, name := range []string{"data/value", "data/value/missing", "data/VALUE", "data/link/missing", "data/LINK/missing"} {
		e.absent = []string{name}
		e.digest = e.currentDigest()
		_, lookupErr := os.Lstat(filepath.Join(destination, name))
		err := e.verifyMaterialization(context.Background(), destination)
		_, aliasParentErr := os.Lstat(filepath.Join(destination, "data/LINK"))
		if (name == "data/VALUE" && errors.Is(lookupErr, syscall.ENOENT)) || (name == "data/LINK/missing" && errors.Is(aliasParentErr, syscall.ENOENT)) {
			if err != nil {
				t.Fatalf("distinct copied absence rejected: %v", err)
			}
		} else if err == nil {
			t.Fatal("invalid copied absence accepted")
		}
	}
}

func TestNativeEnvelopeAbsenceSourceMapping(t *testing.T) {
	request, root := envelopeFixture(t)
	other := t.TempDir()
	write(t, other, "selected", "second root")
	request.Files.Roots = append(request.Files.Roots, inputbundle.Root{Name: "other", Path: other, Mount: "overlay"})
	request.Files.Inputs = append(request.Files.Inputs, inputbundle.Input{Root: "other", Path: "selected"})
	request.Files.Inputs[0].Path = "."
	request.Absent = []string{"overlay/missing"}
	if _, err := CaptureEnvelope(context.Background(), request); err == nil || !strings.Contains(err.Error(), "complete directory") {
		t.Fatalf("another root supplied complete coverage: %v", err)
	}
	request.Files.Inputs[1].Path = "."
	e := captureFixtureEnvelope(t, request)
	// An unselected source-path spelling cannot silently replace the retained
	// private mapping, even when it is another healthy real directory.
	e.sources[0].Path = root
	e.sources[1].Path = other
	if e.VerifySources(context.Background()) == nil {
		t.Fatal("private root mapping drift accepted")
	}
}

func TestNativeEnvelopeCopiedAliasMountOwnership(t *testing.T) {
	for _, names := range [][2]string{{"overlay", "OVERLAY"}, {"caf\u00e9", "cafe\u0301"}, {"parent/overlay", "PARENT/OVERLAY"}, {"parent/caf\u00e9", "PARENT/cafe\u0301"}} {
		t.Run(names[0], func(t *testing.T) {
			mount, alias := names[0], names[1]
			request, _ := envelopeFixture(t)
			other := t.TempDir()
			write(t, other, "selected", "captured")
			write(t, other, "missing", "not captured")
			request.Files.Roots = append(request.Files.Roots, inputbundle.Root{Name: "other", Path: other, Mount: mount})
			request.Files.Inputs[0].Path = "."
			request.Files.Inputs = append(request.Files.Inputs, inputbundle.Input{Root: "other", Path: "selected"})
			request.Absent = []string{alias + "/missing"}
			e := captureFixtureEnvelope(t, request)
			// Materialize a healthy baseline without an absence declaration so
			// its actual filesystem mount lookup is an independent oracle.
			baseline := request
			baseline.Absent = nil
			copy := captureFixtureEnvelope(t, baseline)
			destination := filepath.Join(t.TempDir(), "baseline")
			if err := copy.materialize(context.Background(), destination); err != nil {
				t.Fatal(err)
			}
			defer removeEnvelopeCopy(destination)
			data, lookupErr := os.ReadFile(filepath.Join(destination, alias, "selected"))
			err := e.verifyMaterialization(context.Background(), destination)
			if lookupErr == nil {
				if string(data) != "captured" || err == nil || !strings.Contains(err.Error(), "complete directory") {
					t.Fatalf("alias borrowed another root's coverage: %q %v", data, err)
				}
			} else if !errors.Is(lookupErr, syscall.ENOENT) || err != nil {
				t.Fatalf("distinct root name rejected: %v %v", lookupErr, err)
			}
			actual := filepath.Join(t.TempDir(), "resources")
			err = e.materialize(context.Background(), actual)
			if lookupErr == nil {
				if err == nil || !strings.Contains(err.Error(), "complete directory") {
					t.Fatalf("incomplete aliased mount materialized: %v", err)
				}
				if _, err := os.Lstat(actual); !errors.Is(err, syscall.ENOENT) {
					t.Fatalf("rejected alias copy retained: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				defer removeEnvelopeCopy(actual)
			}
			// A complete second root may establish a genuinely missing leaf
			// through the same actual alias. Its captured existing bytes remain.
			request.Files.Inputs[1].Path = "."
			request.Absent = []string{alias + "/healthy-absence"}
			healthy := captureFixtureEnvelope(t, request)
			healthyCopy := filepath.Join(t.TempDir(), "healthy")
			if err := healthy.materialize(context.Background(), healthyCopy); err != nil {
				t.Fatalf("complete mount alias rejected: %v", err)
			}
			defer removeEnvelopeCopy(healthyCopy)
			data, err = os.ReadFile(filepath.Join(healthyCopy, mount, "missing"))
			if err != nil || string(data) != "not captured" {
				t.Fatalf("complete mount lost source bytes: %q %v", data, err)
			}
			if _, err := os.Lstat(filepath.Join(healthyCopy, alias, "healthy-absence")); !errors.Is(err, syscall.ENOENT) {
				t.Fatalf("complete mount healthy absence differs: %v", err)
			}
			if lookupErr == nil {
				t.Log("actual mount alias resolves; incomplete ownership rejected and complete ownership admitted")
			} else {
				t.Log("actual mount names are distinct; absence admitted in original complete root")
			}
		})
	}
}

func TestNativeEnvelopeAbsenceLookupErrors(t *testing.T) {
	root := t.TempDir()
	if err := verifyEnvelopeAbsence(context.Background(), root, strings.Repeat("x", 256)); err == nil {
		t.Fatal("non-ENOENT lookup error accepted as absence")
	}
	request, _ := envelopeFixture(t)
	for _, change := range []func(*EnvelopeRequest){
		func(r *EnvelopeRequest) { r.Files.Roots = make([]inputbundle.Root, 129) },
		func(r *EnvelopeRequest) { r.Files.Inputs = make([]inputbundle.Input, inputbundle.MaxEntries+1) },
		func(r *EnvelopeRequest) { r.Files.Roots[0].Path = "/" + strings.Repeat("x", 4096) },
		func(r *EnvelopeRequest) { r.Files.Roots[0].Path = "/" + string([]byte{255}) },
	} {
		copy := request
		copy.Files.Roots = append([]inputbundle.Root(nil), request.Files.Roots...)
		change(&copy)
		if _, err := CaptureEnvelope(context.Background(), copy); err == nil {
			t.Fatal("invalid private source mapping accepted")
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
