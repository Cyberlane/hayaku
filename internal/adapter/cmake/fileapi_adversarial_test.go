package cmake

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
)

// Real bound helper invocations exercise the shared producer lifecycle. These
// protocol fixtures neither compile C++ nor certify an installed test runner.
func TestFileAPIAdversarialGenerationBindsIdenticalNativeBytes(t *testing.T) {
	s := metadataSession(t, "healthy")
	names := []string{
		replyDir + "index-current.json", replyDir + "cache.json",
		replyDir + "cmakeFiles.json", replyDir + "codemodel.json",
		replyDir + "toolchains.json", replyDir + "target.json",
		replyDir + "directory.json", "suite", "generated.hpp",
	}
	beforeBytes, err := s.ReadFiles(t.Context(), "build", names, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	before, err := CollectFileAPI(t.Context(), s)
	if err != nil || len(before.Targets) != 1 || before.Targets[0].Name != "suite" || before.Digest == "" {
		t.Fatalf("healthy native observation unavailable: %+v %v", before, err)
	}
	oldRecords, oldBinding, err := s.Records(t.Context())
	if err != nil || len(oldRecords) != 1 || oldRecords[0].ID != "configure" {
		t.Fatalf("actual initial producer evidence unavailable: %+v %v", oldRecords, err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.RunProducer(t.Context(), "reconfigure", model.Command{
		Executable: exe, Args: []string{"--metadata", "{source}", "{build}", "healthy"}, Dir: "{build}",
	})
	if err != nil || !out.Completed || out.ExitCode != 0 {
		t.Fatalf("second identical native invocation failed: %+v %v", out, err)
	}
	afterBytes, err := s.ReadFiles(t.Context(), "build", names, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range names {
		if !bytes.Equal(beforeBytes[i], afterBytes[i]) {
			t.Fatalf("control failed: native bytes changed for %s", name)
		}
	}
	after, err := CollectFileAPI(t.Context(), s)
	if err != nil || !reflect.DeepEqual(before.Targets, after.Targets) || before.Generator != after.Generator || before.Configuration != after.Configuration {
		t.Fatalf("control failed: native settings changed: %+v %v", after, err)
	}
	records, binding, err := s.Records(t.Context())
	if err != nil || len(records) != 2 || records[0] != oldRecords[0] || records[1].ID != "reconfigure" ||
		records[1].InvocationDigest != records[0].InvocationDigest || records[1].OutputDigest != records[0].OutputDigest ||
		records[1].InputDigest != oldBinding || records[1].InputDigest == records[0].InputDigest || binding == oldBinding {
		t.Fatalf("actual producer transition was not bound: %+v %v", records, err)
	}
	if after.Digest == "" || after.Digest == before.Digest {
		t.Fatal("identical native bytes erased the observed producer generation")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("healthy producer cleanup failed: %v", err)
	}
}

func TestFileAPIAdversarialRestoredMetadataCannotHideByteDrift(t *testing.T) {
	for _, area := range []string{"source", "build"} {
		t.Run(area, func(t *testing.T) {
			s := metadataSession(t, "healthy")
			if api, err := CollectFileAPI(t.Context(), s); err != nil || api.Digest == "" {
				t.Fatalf("healthy observation unavailable: %+v %v", api, err)
			}
			root, name := s.Source(), "main.cpp"
			if area == "build" {
				root, name = s.Build(), "generated.hpp"
			}
			file := filepath.Join(root, name)
			before, err := os.Lstat(file)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(file) // Known tiny authored fixture, not a collector boundary.
			if err != nil || len(data) == 0 {
				t.Fatalf("authored bytes unavailable: %v", err)
			}
			data[0] ^= 1
			if err := os.Chmod(file, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(file, before.Mode().Perm()); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(file, before.ModTime(), before.ModTime()); err != nil {
				t.Fatal(err)
			}
			after, err := os.Lstat(file)
			if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
				t.Fatalf("control failed: drift changed admitted metadata: %v", err)
			}
			if api, err := CollectFileAPI(t.Context(), s); err == nil || api.Digest != "" || len(api.Targets) != 0 {
				t.Fatalf("restored metadata hid %s byte drift: %+v %v", area, api, err)
			}
		})
	}
}
