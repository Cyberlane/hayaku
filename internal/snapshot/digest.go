package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// Digest binds all materialized source inputs, including empty directories,
// permissions and raw file bytes. No ignore policy or native normalization is
// applied. A changed digest after discovery invalidates that evidence.
// It observes filesystem state, not historical transient writes or services.
// Pre/post checks reject detectable drift; this is not an atomic tree snapshot.
func Digest(ctx context.Context, dir string) (string, error) {
	return DigestWithModules(ctx, dir, nil)
}

// FileDigest returns a raw SHA-256 identity for one bounded regular file using
// the same nonblocking, cancellable and drift-checked reader as tree digests.
func FileDigest(ctx context.Context, name string, limit int64) (string, error) {
	if limit <= 0 || limit > maxTree {
		return "", errors.New("invalid file digest limit")
	}
	before, err := os.Lstat(name)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := digestFileWithLimit(ctx, name, before, h, limit, limit); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// DigestWithModules binds source outside separately content-bound module trees.
// Only explicit node_modules directories can be excluded. Their bytes, internal
// links and modes must additionally be checked by the Node runtime provider.
func DigestWithModules(ctx context.Context, dir string, modules []string) (string, error) {
	excluded := map[string]bool{}
	for _, name := range modules {
		if err := validPath(name); err != nil || filepath.Base(name) != "node_modules" {
			return "", errors.New("invalid bound snapshot module directory")
		}
		info, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("bound snapshot modules must be real directories")
		}
		excluded[name] = true
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("snapshot digest root must be a real directory")
	}
	h := sha256.New()
	count, total := 0, int64(0)
	admitted := map[string]fs.FileInfo{}
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		name, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		name = filepath.ToSlash(name)
		if name != "." {
			if err := validPath(name); err != nil {
				return err
			}
		}
		count++
		if count > maxFiles {
			return errors.New("snapshot digest exceeds entry count limit")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return errors.New("snapshot digest contains symlink or nonregular input")
		}
		admitted[path] = info
		_, _ = fmt.Fprintf(h, "%d:%s:%o:", len(name), name, info.Mode())
		if info.IsDir() {
			_, _ = h.Write([]byte("directory\x00"))
			if excluded[name] {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Size() > maxBlob || info.Size() < 0 {
			return errors.New("snapshot digest input exceeds byte limit")
		}
		_, _ = fmt.Fprintf(h, "%d:", info.Size())
		read, err := digestFile(ctx, path, info, h, maxTree-total)
		if err != nil {
			return err
		}
		total += read
		return nil
	})
	if err != nil {
		return "", err
	}
	if err := verifyDigestEntries(ctx, admitted); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func verifyDigestEntries(ctx context.Context, admitted map[string]fs.FileInfo) error {
	for name, before := range admitted {
		if err := ctx.Err(); err != nil {
			return err
		}
		after, err := os.Lstat(name)
		if err != nil || !sameDigestEntry(before, after) {
			return errors.New("snapshot entry changed during digest")
		}
	}
	return ctx.Err()
}

// digestFile streams at most the preflight size plus one sentinel byte. Stable
// bytes keep their original digest framing; detectable replacement or metadata
// drift invalidates the whole digest, even when replacement bytes are identical.
// This is a bounded observation, not an atomic or historical filesystem view.
func digestFile(ctx context.Context, name string, before fs.FileInfo, destination io.Writer, remaining int64) (int64, error) {
	return digestFileWithLimit(ctx, name, before, destination, remaining, maxBlob)
}
func digestFileWithLimit(ctx context.Context, name string, before fs.FileInfo, destination io.Writer, remaining, limit int64) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if before == nil || !before.Mode().IsRegular() || before.Size() < 0 || before.Size() > limit || before.Size() > remaining {
		return 0, errors.New("snapshot input exceeds byte limit or is nonregular")
	}
	// A FIFO substituted after preflight must not block opening indefinitely.
	file, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return 0, errors.New("snapshot input unavailable")
	}
	defer file.Close()
	opened, err := file.Stat()
	current, pathErr := os.Lstat(name)
	if err != nil || pathErr != nil || !sameDigestEntry(before, opened) || !sameDigestEntry(before, current) {
		return 0, errors.New("snapshot input changed while opening")
	}
	read, err := io.CopyBuffer(destination, digestReader{ctx, io.LimitReader(file, before.Size()+1)}, make([]byte, 32<<10))
	if err != nil {
		return read, err
	}
	if err := ctx.Err(); err != nil {
		return read, err
	}
	after, err := file.Stat()
	current, pathErr = os.Lstat(name)
	if err != nil || pathErr != nil || read != before.Size() || !sameDigestEntry(before, after) || !sameDigestEntry(before, current) {
		return read, errors.New("snapshot input changed during read")
	}
	return read, ctx.Err()
}

func sameDigestEntry(before, after fs.FileInfo) bool {
	return os.SameFile(before, after) && before.Mode() == after.Mode() && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime())
}

type digestReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r digestReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
