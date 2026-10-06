package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestBoundedDigestPreservesOriginalFraming(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("a\x00bc"), 0600); err != nil {
		t.Fatal(err)
	}
	// This is the pre-existing stable format: lexical entry order, raw modes,
	// directory markers, then exact byte length and bytes with no separator.
	framing := fmt.Sprintf("1:.:%o:directory\x005:empty:%o:directory\x004:file:%o:4:a\x00bc", os.ModeDir|0700, os.ModeDir|0700, os.FileMode(0600))
	expected := sha256.Sum256([]byte(framing))
	actual, err := Digest(context.Background(), root)
	if err != nil || actual != hex.EncodeToString(expected[:]) {
		t.Fatalf("stable digest format changed: %s %v", actual, err)
	}
}

func TestBoundedCandidatePreservesGitObjectFormats(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			root := t.TempDir()
			gitTest(t, root, "init", "--initial-branch=main", "--object-format="+format)
			gitTest(t, root, "config", "core.autocrlf", "false")
			write(t, root, "empty", "")
			write(t, root, "raw", "a\x00bc")
			id := commit(t, root)
			if err := validateRawFiles(context.Background(), root, id); err != nil {
				t.Fatalf("healthy committed raw bytes rejected: %v", err)
			}
			write(t, root, "raw", "d\x00ef")
			if err := validateRawFiles(context.Background(), root, id); err == nil {
				t.Fatal("different equal-length bytes accepted")
			}
			write(t, root, "raw", "a\x00bc")
			if err := os.Chmod(filepath.Join(root, "raw"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := validateRawFiles(context.Background(), root, id); err == nil {
				t.Fatal("different executable mode accepted")
			}
		})
	}
}

func TestBoundedDigestDirectoryDriftDuringRead(t *testing.T) {
	for _, fault := range []string{"addition", "deletion", "mode", "replacement"} {
		t.Run(fault, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			write(t, root, "input", "fixture")
			write(t, root, "other", "other")
			directory, err := os.Lstat(root)
			if err != nil {
				t.Fatal(err)
			}
			input, err := os.Lstat(filepath.Join(root, "input"))
			if err != nil {
				t.Fatal(err)
			}
			before := map[string]fs.FileInfo{root: directory}
			if err := verifyDigestEntries(context.Background(), before); err != nil {
				t.Fatalf("healthy directory rejected: %v", err)
			}
			writer := &directoryMutationWriter{mutate: func() {
				switch fault {
				case "addition":
					write(t, root, "added", "added")
				case "deletion":
					if err := os.Remove(filepath.Join(root, "other")); err != nil {
						t.Fatal(err)
					}
				case "mode":
					if err := os.Chmod(root, 0755); err != nil {
						t.Fatal(err)
					}
				case "replacement":
					if err := os.Rename(root, root+"-old"); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.RemoveAll(root + "-old") })
					if err := os.Mkdir(root, directory.Mode().Perm()); err != nil {
						t.Fatal(err)
					}
				}
			}}
			_, _ = digestFile(context.Background(), filepath.Join(root, "input"), input, writer, maxTree)
			if !writer.called || verifyDigestEntries(context.Background(), before) == nil {
				t.Fatal("detectable directory drift during actual file read accepted")
			}
		})
	}
}

type directoryMutationWriter struct {
	mutate func()
	called bool
}

func (w *directoryMutationWriter) Write(data []byte) (int, error) {
	if !w.called {
		w.called = true
		w.mutate()
	}
	return len(data), nil
}
