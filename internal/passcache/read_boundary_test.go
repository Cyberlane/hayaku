package passcache

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestPrivateReadRejectsStalePreflight(t *testing.T) {
	for _, fault := range []string{"healthy", "fifo", "same-bytes-new-inode", "permissions", "growth", "timestamp", "symlink"} {
		t.Run(fault, func(t *testing.T) {
			dir := t.TempDir()
			name := filepath.Join(dir, "receipt")
			data := []byte("private-pass")
			if err := os.WriteFile(name, data, 0600); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			before, err := root.Lstat("receipt")
			if err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "fifo":
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Mkfifo(name, 0600); err != nil {
					t.Fatal(err)
				}
			case "same-bytes-new-inode":
				other := filepath.Join(dir, "replacement")
				if err := os.WriteFile(other, data, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(other, before.ModTime(), before.ModTime()); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(other, name); err != nil {
					t.Fatal(err)
				}
				current, err := root.Lstat("receipt")
				if err != nil || os.SameFile(before, current) || before.Mode() != current.Mode() || before.Size() != current.Size() || !before.ModTime().Equal(current.ModTime()) {
					t.Fatal("replacement must preserve metadata while changing inode")
				}
			case "permissions":
				if err := os.Chmod(name, 0400); err != nil {
					t.Fatal(err)
				}
			case "growth":
				if err := os.WriteFile(name, bytes.Repeat([]byte("x"), MaxBytes+1), 0600); err != nil {
					t.Fatal(err)
				}
			case "timestamp":
				if err := os.Chtimes(name, before.ModTime().Add(time.Hour), before.ModTime().Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				other := filepath.Join(dir, "target")
				if err := os.WriteFile(other, data, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("target", name); err != nil {
					t.Fatal(err)
				}
			}
			type outcome struct {
				data []byte
				err  error
			}
			done := make(chan outcome, 1)
			go func() { data, err := readPrivateEntry(root, "receipt", before); done <- outcome{data, err} }()
			select {
			case r := <-done:
				if fault == "healthy" {
					if r.err != nil || !bytes.Equal(r.data, data) {
						t.Fatalf("healthy read: %v", r.err)
					}
				} else if r.err == nil || r.data != nil {
					t.Fatal("stale private object admitted")
				}
			case <-time.After(3 * time.Second):
				// Release a broken blocking FIFO reader so the failure cannot leak.
				if fault == "fifo" {
					if f, err := os.OpenFile(name, os.O_RDWR|syscall.O_NONBLOCK, 0); err == nil {
						f.Close()
					}
				}
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Error("private reader failed to join after release")
				}
				t.Fatal("private object read blocked")
			}
		})
	}
}

func TestPrivateReadPostReadDrift(t *testing.T) {
	for _, fault := range []string{"healthy", "inode", "mode", "size", "timestamp", "symlink"} {
		t.Run(fault, func(t *testing.T) {
			dir := t.TempDir()
			name := filepath.Join(dir, "receipt")
			data := []byte("authenticated-candidate")
			if err := os.WriteFile(name, data, 0600); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			before, err := root.Lstat("receipt")
			if err != nil {
				t.Fatal(err)
			}
			f, err := root.OpenFile("receipt", os.O_RDONLY|syscall.O_NONBLOCK, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			read, err := io.ReadAll(io.LimitReader(f, before.Size()+1))
			if err != nil || !bytes.Equal(read, data) {
				t.Fatal("post-read oracle requires a successful real read", err)
			}
			if err := validatePrivateRead(root, "receipt", f, before, int64(len(read))); err != nil {
				t.Fatal("healthy post-read control", err)
			}
			switch fault {
			case "inode":
				other := filepath.Join(dir, "other")
				if err := os.WriteFile(other, data, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(other, before.ModTime(), before.ModTime()); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(other, name); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.Chmod(name, 0400); err != nil {
					t.Fatal(err)
				}
			case "size":
				if err := os.WriteFile(name, append(data, 'x'), 0600); err != nil {
					t.Fatal(err)
				}
			case "timestamp":
				if err := os.Chtimes(name, before.ModTime().Add(time.Hour), before.ModTime().Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(name, filepath.Join(dir, "other")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("other", name); err != nil {
					t.Fatal(err)
				}
			}
			err = validatePrivateRead(root, "receipt", f, before, int64(len(read)))
			if (err == nil) != (fault == "healthy") {
				t.Fatal("post-read drift disposition incorrect", err)
			}
		})
	}
}

func TestPrivateReadBoundsAndSpecialFiles(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.WriteFile(filepath.Join(dir, "object"), []byte("1234"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int64{-1, 3, MaxBytes + 1} {
		if data, err := readPrivate(root, "object", limit); err == nil || data != nil {
			t.Fatal("invalid size admission")
		}
	}
	if data, err := readPrivate(root, "object", 4); err != nil || string(data) != "1234" {
		t.Fatal("exact-size read failed", err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	if data, err := readPrivate(root, "fifo", MaxBytes); err == nil || data != nil {
		t.Fatal("FIFO preflight admitted")
	}
}
