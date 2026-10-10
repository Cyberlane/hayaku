package native

import (
	"context"
	"debug/elf"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These encoded layouts test capture and role guards, never installed loading.
// Independent installed NAPI/Rolldown controls are in addon_boundary_test.go.
func addonDeclaration(t *testing.T, data []byte) (RuntimeRequest, string) {
	t.Helper()
	dir := t.TempDir()
	for name, bytes := range map[string][]byte{
		"loader":       dynamicFixtureBytes("", "ldfixture.so", nil),
		"library":      dynamicFixtureBytes("", "libfixture.so", []string{"ldfixture.so"}),
		"binding.node": data,
		"program":      dynamicFixtureBytes("/lib/ldfixture.so", "", []string{"libfixture.so"}),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), bytes, 0555); err != nil {
			t.Fatal(err)
		}
	}
	return RuntimeRequest{Loader: filepath.Join(dir, "loader"), Libraries: []RuntimeLibrary{{Name: "libfixture.so", Path: filepath.Join(dir, "library")}}, Addons: []RuntimeAddon{{Name: "codec", Path: filepath.Join(dir, "binding.node")}}}, dir
}

func TestRuntimeAddonCaptureAndTransportRoles(t *testing.T) {
	for _, soname := range []string{"", "nativecodec.so"} {
		t.Run("soname="+soname, func(t *testing.T) {
			request, dir := addonDeclaration(t, dynamicFixtureBytes("", soname, []string{"libfixture.so"}))
			r, err := CaptureRuntime(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := r.Close(); err != nil {
					t.Error(err)
				}
			})
			path, err := r.AddonPath(t.Context(), "codec")
			if err != nil || path != "/runtime/#addon-codec.node" {
				t.Fatal("captured addon route unavailable", err)
			}
			if r.bytes != 3*8192 || len(r.files()) != 3 || r.addons["codec"].copied.Mode().Perm() != 0400 {
				t.Fatal("role accounting or immutable private mode differs")
			}
			request.Addons[0].Name = "caller-change"
			if _, err := r.AddonPath(t.Context(), "codec"); err != nil {
				t.Fatal("caller mutation changed captured name", err)
			}
			p, err := CaptureDynamicProgram(t.Context(), DynamicProgramRequest{Program: filepath.Join(dir, "program"), Args: []string{path}, Runtime: r})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := p.Close(); err != nil {
					t.Error(err)
				}
			})
			files, err := p.materialize(t.Context(), filepath.Join(t.TempDir(), "runtime"), "arm64")
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != 4 {
				t.Fatal("transport omitted a captured role")
			}
			var addon *dynamicTransportFile
			for i := range files {
				f := &files[i]
				want := os.FileMode(0555)
				if filepath.Base(f.path) == "#addon-codec.node" {
					want = 0444
					addon = f
				}
				if f.info.Mode().Perm() != want {
					t.Fatal("transport executable role mode differs")
				}
			}
			if addon == nil {
				t.Fatal("addon transport route missing")
			}
			if err := os.Chmod(addon.path, 0555); err != nil {
				t.Fatal(err)
			}
			if err := verifyDynamicTransport(t.Context(), files); err == nil {
				t.Fatal("addon transport permission drift accepted")
			}
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := r.AddonPath(t.Context(), "codec"); err == nil {
				t.Fatal("closed addon route accepted")
			}
		})
	}
}

func TestRuntimeAddonRejectsUnclosedAndConflictingImages(t *testing.T) {
	healthy := dynamicFixtureBytes("", "", []string{"libfixture.so"})
	request, _ := addonDeclaration(t, healthy)
	r, err := CaptureRuntime(t.Context(), request)
	if err != nil {
		t.Fatal("healthy admission control failed", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	for _, fault := range []string{"missing-needed", "loader-soname", "library-soname", "interpreter", "architecture", "executable", "write-exec", "library-role"} {
		t.Run(fault, func(t *testing.T) {
			data := append([]byte(nil), healthy...)
			switch fault {
			case "missing-needed":
				data = dynamicFixtureBytes("", "", []string{"unclosed.so"})
			case "loader-soname":
				data = dynamicFixtureBytes("", "ldfixture.so", nil)
			case "library-soname":
				data = dynamicFixtureBytes("", "libfixture.so", nil)
			case "interpreter":
				data = dynamicFixtureBytes("/lib/ldfixture.so", "", nil)
			case "architecture":
				binary.LittleEndian.PutUint16(data[18:20], uint16(elf.EM_X86_64))
			case "executable":
				binary.LittleEndian.PutUint16(data[16:18], uint16(elf.ET_EXEC))
			case "write-exec":
				binary.LittleEndian.PutUint32(data[68:72], 7)
			}
			request, _ := addonDeclaration(t, data)
			if fault == "library-role" {
				request.Libraries = append(request.Libraries, RuntimeLibrary{Name: "binding.node", Path: request.Addons[0].Path})
				request.Addons = nil
			}
			bad, err := CaptureRuntime(t.Context(), request)
			if bad != nil {
				_ = bad.Close()
				t.Fatal("invalid role returned a runtime")
			}
			if err == nil {
				t.Fatal("invalid addon image admitted")
			}
		})
	}
}

func TestRuntimeAddonDeclarationsAndLifetime(t *testing.T) {
	for _, name := range []string{"", ".", "..", "a/b", "a\\b", strings.Repeat("a", 129)} {
		request := RuntimeRequest{Loader: "/loader", Addons: []RuntimeAddon{{Name: name, Path: "/binding.node"}}}
		if err := preflightRuntime(request); err == nil {
			t.Fatal("invalid addon declaration accepted")
		}
	}
	request := RuntimeRequest{Loader: "/loader", Addons: []RuntimeAddon{{Name: "a", Path: "/binding.node"}, {Name: "a", Path: "/other.node"}}}
	if err := preflightRuntime(request); err == nil {
		t.Fatal("duplicate addon identity admitted")
	}
	request.Addons = make([]RuntimeAddon, runtimeAddonLimit+1)
	if err := preflightRuntime(request); err == nil {
		t.Fatal("addon count exceeds bound")
	}
	request.Libraries = make([]RuntimeLibrary, dynamicLibraryLimit)
	for i := range request.Libraries {
		request.Libraries[i] = RuntimeLibrary{Name: strings.Repeat("a", i+1), Path: "/library"}
	}
	request.Addons = []RuntimeAddon{{Name: "a", Path: "/binding.node"}}
	if err := preflightRuntime(request); err == nil {
		t.Fatal("combined native role count exceeds bound")
	}
	request, _ = addonDeclaration(t, dynamicFixtureBytes("", "", []string{"libfixture.so"}))
	r, err := CaptureRuntime(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := r.AddonPath(t.Context(), "missing"); err == nil {
		t.Fatal("undeclared addon route returned")
	}
	var empty *Runtime
	if _, err := empty.AddonPath(t.Context(), "codec"); err == nil {
		t.Fatal("nil runtime route returned")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.AddonPath(ctx, "codec"); !errors.Is(err, context.Canceled) {
		t.Fatal("typed addon cancellation lost", err)
	}
	initial := r.seal
	r.addons["renamed"] = r.addons["codec"]
	delete(r.addons, "codec")
	if r.currentDigest() == initial || r.Verify(t.Context()) == nil {
		t.Fatal("private addon role rename retained identity")
	}
}

func TestRuntimeAddonRejectsUnchangedMetadataByteDrift(t *testing.T) {
	for _, target := range []string{"source", "private-copy"} {
		t.Run(target, func(t *testing.T) {
			request, _ := addonDeclaration(t, dynamicFixtureBytes("", "", []string{"libfixture.so"}))
			r, err := CaptureRuntime(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := r.Close(); err != nil {
					t.Error(err)
				}
			})
			if _, err := r.AddonPath(t.Context(), "codec"); err != nil {
				t.Fatal("fresh route control failed", err)
			}
			path := r.addons["codec"].source
			if target == "private-copy" {
				path = r.addons["codec"].copy
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data[len(data)-1] ^= 1
			if err := os.Chmod(path, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, before.Mode().Perm()); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
				t.Fatal(err)
			}
			after, err := os.Lstat(path)
			if err != nil || !sameProgramInfo(before, after) {
				t.Fatal("fixture did not restore byte-independent metadata", err)
			}
			if result, err := r.AddonPath(t.Context(), "codec"); err == nil || result != "" {
				t.Fatal("drifted role returned a usable route")
			}
		})
	}
}

func TestRuntimeAddonCannotSupplyAnotherAddonsNeededLibrary(t *testing.T) {
	request, dir := addonDeclaration(t, dynamicFixtureBytes("", "codec.so", []string{"libfixture.so"}))
	r, err := CaptureRuntime(t.Context(), request)
	if err != nil {
		t.Fatal("closed role control failed", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(dir, "second.node")
	if err := os.WriteFile(second, dynamicFixtureBytes("", "second.so", []string{"codec.so"}), 0444); err != nil {
		t.Fatal(err)
	}
	request.Addons = append(request.Addons, RuntimeAddon{Name: "second", Path: second})
	bad, err := CaptureRuntime(t.Context(), request)
	if bad != nil {
		_ = bad.Close()
		t.Fatal("addon was promoted into library resolution")
	}
	if err == nil {
		t.Fatal("addon-only needed name supplied a declared library")
	}
	if err := os.Chmod(second, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, dynamicFixtureBytes("", "codec.so", []string{"libfixture.so"}), 0444); err != nil {
		t.Fatal(err)
	}
	bad, err = CaptureRuntime(t.Context(), request)
	if bad != nil {
		_ = bad.Close()
		t.Fatal("duplicate addon SONAME admitted")
	}
	if err == nil {
		t.Fatal("duplicate addon SONAME accepted")
	}
}

func TestRuntimeAddonCleanupPreservesReplacementAndUnknownSiblings(t *testing.T) {
	for _, scope := range []string{"runtime", "program"} {
		t.Run(scope, func(t *testing.T) {
			request, source := addonDeclaration(t, dynamicFixtureBytes("", "", []string{"libfixture.so"}))
			r, err := CaptureRuntime(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := r.Close(); err != nil {
					t.Error(err)
				}
			})
			p, err := CaptureDynamicProgram(t.Context(), DynamicProgramRequest{Program: filepath.Join(source, "program"), Runtime: r})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := p.Close(); err != nil {
					t.Error(err)
				}
			})
			name, closeOwned, verify := r.dir, r.Close, r.Verify
			children := r.cleanup.children
			if scope == "program" {
				name, closeOwned, verify, children = p.dir, p.Close, p.Verify, p.cleanup.children
			}
			if err := verify(t.Context()); err != nil {
				t.Fatal("healthy ownership control failed", err)
			}
			moved := name + "-moved"
			if err := os.Rename(name, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(name, 0700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(name, "foreign")
			if err := os.WriteFile(marker, []byte("preserve"), 0400); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(name); os.RemoveAll(moved) })
			if err := closeOwned(); err == nil || verify(t.Context()) == nil {
				t.Fatal("retargeted cleanup succeeded or closed owner remained usable")
			}
			if data, err := os.ReadFile(marker); err != nil || string(data) != "preserve" {
				t.Fatal("foreign replacement was altered", err)
			}
			for _, child := range children {
				if _, err := os.Lstat(filepath.Join(moved, child)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("retained owner did not remove its private child", err)
				}
			}
			if err := os.RemoveAll(name); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(moved, name); err != nil {
				t.Fatal(err)
			}
			unknown := filepath.Join(name, "unknown")
			if err := os.WriteFile(unknown, []byte("preserve"), 0400); err != nil {
				t.Fatal(err)
			}
			if err := closeOwned(); err == nil {
				t.Fatal("unexpected sibling was recursively removed")
			}
			if data, err := os.ReadFile(unknown); err != nil || string(data) != "preserve" {
				t.Fatal("unexpected sibling was altered", err)
			}
			if err := os.Remove(unknown); err != nil {
				t.Fatal(err)
			}
			if err := closeOwned(); err != nil {
				t.Fatal("restored ownership could not finish cleanup", err)
			}
			if err := closeOwned(); err != nil {
				t.Fatal("completed cleanup was not idempotent", err)
			}
		})
	}
}
