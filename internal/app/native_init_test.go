package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Cyberlane/hayaku/internal/native"
)

func TestNativeInitGoPreservesExistingFilesAndOriginalSelectors(t *testing.T) {
	root := t.TempDir()
	var out, errout bytes.Buffer
	args := []string{"native-init", "--root", root, "--language", "go", "--package", "./alpha", "--package", "./beta"}
	if status := Run(context.Background(), args, &out, &errout); status != 0 {
		t.Fatal(status, errout.String())
	}
	path := filepath.Join(root, "native.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var c native.RunConfig
	if json.Unmarshal(data, &c) != nil || len(c.Workspaces) != 1 {
		t.Fatal("invalid enrollment config")
	}
	w := c.Workspaces[0]
	if w.Adapter != "go" || !slices.Equal(w.Command.Args, []string{"test", "-count=1", "./alpha", "./beta"}) || !slices.Equal(w.Patterns, []string{"./alpha", "./beta"}) {
		t.Fatal("original selectors changed", w)
	}
	if status := Run(context.Background(), args, &out, &errout); status == 0 {
		t.Fatal("existing config overwritten")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, after) {
		t.Fatal("existing config changed", err)
	}
}

func TestNativeInitRejectsInvalidEnrollmentBeforeWriting(t *testing.T) {
	for _, extra := range [][]string{
		{"--language", "unknown"}, {"--package", "../escape"}, {"--package", "./..."},
		{"--package", ".", "--config", "../escape.json"}, {"--package", ".", "--platform", "unknown"},
		{"--language", "vitest", "--config", native.VitestProfileFile}, {"--language", "vitest"},
	} {
		root := t.TempDir()
		var out, errout bytes.Buffer
		args := append([]string{"native-init", "--root", root}, extra...)
		if status := Run(context.Background(), args, &out, &errout); status == 0 {
			t.Fatal("invalid enrollment passed", extra)
		}
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 0 {
			t.Fatal("invalid enrollment left files", entries, err)
		}
	}
}

func TestNativeInitVitestWritesProfileWithoutLaunchingOrInstalling(t *testing.T) {
	root := t.TempDir()
	sdk := t.TempDir()
	modules := filepath.Join(sdk, "node_modules")
	if err := os.Mkdir(modules, 0755); err != nil {
		t.Fatal(err)
	}
	docker := filepath.Join(sdk, "docker")
	if err := os.WriteFile(docker, []byte("#!/bin/sh\nexit 99\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", sdk)
	for _, name := range []string{"host-node", "linux-node", "loader", "addon", "libdl.so.2", "libstdc++.so.6", "libm.so.6", "libgcc_s.so.1", "libpthread.so.0", "libc.so.6"} {
		if err := os.WriteFile(filepath.Join(sdk, name), []byte("unqualified enrollment input"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"native-init", "--root", root, "--language", "vitest", "--image", "sha256:" + string(bytes.Repeat([]byte{'a'}, 64)), "--node", filepath.Join(sdk, "host-node"), "--runtime-node", filepath.Join(sdk, "linux-node"), "--modules", modules, "--typescript-sdk", sdk, "--loader", filepath.Join(sdk, "loader"), "--library-dir", sdk, "--addon", filepath.Join(sdk, "addon")}
	var out, errout bytes.Buffer
	if status := Run(context.Background(), args, &out, &errout); status != 0 {
		t.Fatal("enrollment tried to launch an executable or rejected descriptive paths", status, errout.String())
	}
	for name, want := range map[string]string{native.VitestProfileFile: native.VitestProfile, "package.json": native.VitestPackage} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(data) != want {
			t.Fatal("profile differs", name, err)
		}
	}
	var c native.RunConfig
	data, err := os.ReadFile(filepath.Join(root, "native.json"))
	if err != nil || json.Unmarshal(data, &c) != nil {
		t.Fatal("invalid generated config", err)
	}
	if c.Platform != "linux" || c.Architecture != "arm64" || len(c.Workspaces) != 1 || len(c.Workspaces[0].Vitest.Libraries) != 6 {
		t.Fatal("runtime enrollment contract changed", c)
	}
}

func TestNativeInitRollbackPreservesForeignEditsAfterCreationFailure(t *testing.T) {
	for _, change := range []string{"untouched", "same-inode", "replacement", "symlink"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			owner, err := os.OpenRoot(root)
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Close()
			if err := owner.WriteFile("native.json", []byte("owned"), 0644); err != nil {
				t.Fatal(err)
			}
			info, err := owner.Lstat("native.json")
			if err != nil {
				t.Fatal(err)
			}
			retained := map[string]nativeEnrollmentFile{"native.json": {info, sha256.Sum256([]byte("owned"))}}
			switch change {
			case "same-inode":
				if err := owner.WriteFile("native.json", []byte("other"), 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(filepath.Join(root, "native.json"), info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
			case "replacement":
				if err := owner.Rename("native.json", "retained.json"); err != nil {
					t.Fatal(err)
				}
				if err := owner.WriteFile("native.json", []byte("other"), 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := owner.Rename("native.json", "retained.json"); err != nil {
					t.Fatal(err)
				}
				if err := owner.WriteFile("foreign.json", []byte("other"), 0644); err != nil {
					t.Fatal(err)
				}
				if err := owner.Symlink("foreign.json", "native.json"); err != nil {
					t.Fatal(err)
				}
			}
			if err := owner.WriteFile("blocked.json", []byte("foreign collision"), 0600); err != nil {
				t.Fatal(err)
			}
			if file, err := owner.OpenFile("blocked.json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644); !os.IsExist(err) {
				if file != nil {
					file.Close()
				}
				t.Fatal("later creation did not fail exclusively", err)
			}
			err = rollbackNativeEnrollment(owner, retained)
			if change == "untouched" {
				if err != nil {
					t.Fatal("owned unchanged rollback", err)
				}
				if _, err := owner.Lstat("native.json"); !os.IsNotExist(err) {
					t.Fatal("owned file left behind", err)
				}
			} else {
				data, readErr := owner.ReadFile("native.json")
				if err == nil || readErr != nil || string(data) != "other" {
					t.Fatal("rollback deleted foreign changes", err, readErr)
				}
			}
			if data, err := owner.ReadFile("blocked.json"); err != nil || string(data) != "foreign collision" {
				t.Fatal("rollback removed later existing file", err)
			}
		})
	}
}
