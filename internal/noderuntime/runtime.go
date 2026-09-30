// Package noderuntime binds explicit, user-installed Node runtime inputs and
// copies their dependencies into disposable checkouts without installing them.
package noderuntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Cyberlane/hayaku/internal/model"
)

const MaxEntries = 250000
const MaxBytes int64 = 2 << 30

type Fingerprint struct {
	Digest        string `json:"digest"`
	NodeDigest    string `json:"node_digest"`
	ModulesDigest string `json:"modules_digest"`
	Files         int    `json:"files"`
	Bytes         int64  `json:"bytes"`
}

type budget struct {
	entries int
	bytes   int64
}

func Identity(ctx context.Context, runtime model.NodeRuntime) (Fingerprint, error) {
	var result Fingerprint
	if !filepath.IsAbs(runtime.Node) || !filepath.IsAbs(runtime.Modules) {
		return result, errors.New("runtime paths must be absolute")
	}
	b := &budget{}
	node := sha256.New()
	info, err := os.Stat(runtime.Node)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return result, errors.New("Node runtime must be an existing executable regular file")
	}
	if err := digestFile(ctx, runtime.Node, info, node, b); err != nil {
		return result, err
	}
	modules := sha256.New()
	if err := walk(ctx, runtime.Modules, modules, b, ""); err != nil {
		return result, err
	}
	result.NodeDigest = hex.EncodeToString(node.Sum(nil))
	result.ModulesDigest = hex.EncodeToString(modules.Sum(nil))
	combined := sha256.Sum256([]byte(result.NodeDigest + ":" + result.ModulesDigest))
	result.Digest = hex.EncodeToString(combined[:])
	result.Files, result.Bytes = b.entries, b.bytes
	return result, nil
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func walk(ctx context.Context, root string, h hash.Hash, b *budget, destination string) error {
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("modules must be an existing nonsymlink directory")
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return errors.New("cannot resolve installed module root")
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return errors.New("cannot read installed module tree")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		b.entries++
		if b.entries > MaxEntries {
			return errors.New("runtime entry limit exceeded")
		}
		info, err := entry.Info()
		if err != nil {
			return errors.New("cannot inspect installed module")
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		size := info.Size()
		if info.IsDir() {
			size = 0
		}
		fmt.Fprintf(h, "%q:%o:%d\n", filepath.ToSlash(rel), info.Mode(), size)
		var target string
		if destination != "" {
			target = filepath.Join(destination, rel)
		}
		switch {
		case info.IsDir():
			// Directory sizes vary by filesystem and are intentionally excluded.
			if destination != "" && rel != "." {
				if err := os.Mkdir(target, info.Mode().Perm()|0700); err != nil {
					return err
				}
			}
		case info.Mode().IsRegular():
			if err := digestFile(ctx, path, info, h, b); err != nil {
				return err
			}
			if destination != "" {
				if err := copyFile(ctx, path, target, info.Mode().Perm(), info.Size()); err != nil {
					return err
				}
			}
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil || filepath.IsAbs(link) {
				return errors.New("module links must be relative and internal")
			}
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil || !within(canonicalRoot, resolved) {
				return errors.New("module symlink escapes installed dependency tree or is broken")
			}
			fmt.Fprintf(h, "link:%q\n", filepath.ToSlash(link))
			if destination != "" {
				if err := os.Symlink(link, target); err != nil {
					return err
				}
			}
		default:
			return errors.New("special files are unsupported runtime inputs")
		}
		return nil
	})
}

func digestFile(ctx context.Context, path string, before os.FileInfo, h hash.Hash, b *budget) error {
	if before.Size() < 0 || before.Size() > MaxBytes-b.bytes {
		return errors.New("runtime byte limit exceeded")
	}
	b.bytes += before.Size()
	f, err := os.Open(path)
	if err != nil {
		return errors.New("cannot open runtime input")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() {
		return errors.New("runtime input changed while opening")
	}
	fmt.Fprintf(h, "file:%o:%d\n", before.Mode(), before.Size())
	n, err := io.Copy(h, &contextReader{ctx: ctx, reader: io.LimitReader(f, before.Size()+1)})
	if err != nil {
		return err
	}
	after, err := f.Stat()
	if err != nil || n != before.Size() || !same(before, after) {
		return errors.New("runtime input changed while hashing")
	}
	return nil
}

func same(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.Mode() == b.Mode() && a.ModTime() == b.ModTime()
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func copyFile(ctx context.Context, source, target string, mode os.FileMode, size int64) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(out, &contextReader{ctx: ctx, reader: io.LimitReader(in, size+1)})
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n != size {
		return errors.New("runtime file changed during copy")
	}
	return os.Chmod(target, mode)
}

// CopyModules verifies both sides of a controlled copy before atomically making
// it visible. It refuses to overwrite any tracked or previously copied input.
func CopyModules(ctx context.Context, runtime model.NodeRuntime, destination string, expected Fingerprint) error {
	if !filepath.IsAbs(destination) {
		return errors.New("runtime copy destination must be absolute")
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		return errors.New("runtime copy destination already exists or is inaccessible")
	}
	before, err := Identity(ctx, runtime)
	if err != nil {
		return err
	}
	if before != expected {
		return errors.New("runtime identity changed before copy")
	}
	staging, err := os.MkdirTemp(filepath.Dir(destination), ".hayaku-modules-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := walk(ctx, runtime.Modules, sha256.New(), &budget{}, staging); err != nil {
		return err
	}
	// Apply directory modes after populating children so read-only packages work.
	if err := filepath.WalkDir(runtime.Modules, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(runtime.Modules, path)
		if err != nil {
			return err
		}
		return os.Chmod(filepath.Join(staging, rel), info.Mode().Perm())
	}); err != nil {
		return err
	}
	after, err := Identity(ctx, runtime)
	if err != nil {
		return err
	}
	if after != expected {
		return errors.New("runtime identity changed during copy")
	}
	copied := runtime
	copied.Modules = staging
	actual, err := Identity(ctx, copied)
	if err != nil {
		return err
	}
	if actual != expected {
		return errors.New("copied runtime differs from declared runtime")
	}
	return os.Rename(staging, destination)
}
