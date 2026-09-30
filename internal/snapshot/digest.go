package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Digest binds all materialized source inputs, including empty directories,
// permissions and raw file bytes. No ignore policy or native normalization is
// applied. A changed digest after discovery invalidates that evidence.
// It observes filesystem state, not historical transient writes or services.
func Digest(ctx context.Context, dir string) (string, error) {
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("snapshot digest root must be a real directory")
	}
	h := sha256.New()
	count, total := 0, int64(0)
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
		_, _ = fmt.Fprintf(h, "%d:%s:%o:", len(name), name, info.Mode())
		if info.IsDir() {
			_, _ = h.Write([]byte("directory\x00"))
			return nil
		}
		if info.Size() > maxBlob || info.Size() < 0 {
			return errors.New("snapshot digest input exceeds byte limit")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		total += int64(len(data))
		if len(data) > maxBlob || total > maxTree {
			return errors.New("snapshot digest exceeds byte limit")
		}
		_, _ = fmt.Fprintf(h, "%d:", len(data))
		_, _ = h.Write(data)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
