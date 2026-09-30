package snapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDigestBindsBytesExistenceModesAndEmptyDirectories(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module fixture\n")
	base, err := Digest(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	checkChanged := func() {
		t.Helper()
		current, err := Digest(context.Background(), dir)
		if err != nil || current == base {
			t.Fatalf("input mutation did not invalidate: %s, %v", current, err)
		}
	}
	write(t, dir, "go.mod", "module different\n")
	checkChanged()
	write(t, dir, "go.mod", "module fixture\n")
	write(t, dir, "go.sum", "new module metadata\n")
	checkChanged()
	if err := os.Remove(filepath.Join(dir, "go.sum")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "go.mod"), 0700); err != nil {
		t.Fatal(err)
	}
	checkChanged()
	if err := os.Chmod(filepath.Join(dir, "go.mod"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "new-empty-directory"), 0700); err != nil {
		t.Fatal(err)
	}
	checkChanged()
	if err := os.Remove(filepath.Join(dir, "new-empty-directory")); err != nil {
		t.Fatal(err)
	}
	final, err := Digest(context.Background(), dir)
	if err != nil || final != base {
		t.Fatalf("restored inputs did not restore digest: %s, %v", final, err)
	}
}

func TestDigestRejectsSymlinksAndCanceledWalks(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a", "bytes")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Digest(ctx, dir); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled digest: %v", err)
	}
	if err := os.Symlink("a", filepath.Join(dir, "alias")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Digest(context.Background(), dir); err == nil {
		t.Fatal("symlink input accepted")
	}
	if _, err := Digest(context.Background(), filepath.Join(dir, "alias")); err == nil {
		t.Fatal("symlink root accepted")
	}
}
