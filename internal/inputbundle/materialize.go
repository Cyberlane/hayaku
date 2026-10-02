package inputbundle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// Materialize reserves a new private directory, never overwrites existing source
// or destinations, and returns only after verifying the complete copy. Callers
// must not execute from a directory until this method succeeds. Directory
// publication is not atomic; incomplete copies are removed on failure.
func (b *Bundle) Materialize(ctx context.Context, destination string) (err error) {
	if b == nil {
		return errors.New("missing input envelope")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !filepath.IsAbs(destination) || filepath.Clean(destination) != destination {
		return errors.New("input destination must be an absolute clean path")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(destination))
	if err != nil {
		return errors.New("input destination parent must exist")
	}
	destination = filepath.Join(parent, filepath.Base(destination))
	for _, root := range b.request.Roots {
		if within(root.Path, destination) {
			return errors.New("input destination must be outside source roots")
		}
	}
	if err := b.VerifySources(ctx); err != nil {
		return err
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		return errors.New("input destination already exists or cannot be reserved")
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(destination)
		}
	}()
	for _, entry := range b.manifest.Entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		target := filepath.Join(destination, filepath.FromSlash(entry.Path))
		switch entry.Kind {
		case "directory":
			if entry.Path != "." {
				if err := os.Mkdir(target, 0700); err != nil {
					return errors.New("cannot create input directory")
				}
			}
		case "file":
			data := b.data[entry.Path]
			digest := sha256.Sum256(data)
			if int64(len(data)) != entry.Size || hex.EncodeToString(digest[:]) != entry.SHA256 {
				return errors.New("captured input bytes are invalid")
			}
			f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return errors.New("cannot reserve input file")
			}
			_, writeErr := io.Copy(f, contextReader{ctx: ctx, reader: bytes.NewReader(data)})
			modeErr := f.Chmod(os.FileMode(entry.Mode))
			closeErr := f.Close()
			if writeErr != nil {
				return writeErr
			}
			if modeErr != nil || closeErr != nil {
				return errors.New("cannot finalize input file")
			}
		case "symlink":
			if err := os.Symlink(filepath.FromSlash(entry.MaterializedLink), target); err != nil {
				return errors.New("cannot create bound input symlink")
			}
		default:
			return errors.New("invalid captured input type")
		}
	}
	// Finalize children before parents. The envelope root always stays private.
	for i := len(b.manifest.Entries) - 1; i >= 0; i-- {
		entry := b.manifest.Entries[i]
		if entry.Kind == "directory" {
			if err := os.Chmod(filepath.Join(destination, filepath.FromSlash(entry.Path)), os.FileMode(entry.Mode)); err != nil {
				return errors.New("cannot preserve input directory mode")
			}
		}
	}
	if err := b.VerifyMaterialization(ctx, destination); err != nil {
		return err
	}
	return b.VerifySources(ctx)
}

// VerifyMaterialization checks exact membership, bytes, modes and rewritten link
// identities. Unexpected files and all symlink escapes fail verification.
func (b *Bundle) VerifyMaterialization(ctx context.Context, destination string) error {
	if b == nil {
		return errors.New("missing input envelope")
	}
	if !filepath.IsAbs(destination) {
		return errors.New("input destination must be absolute")
	}
	info, err := os.Lstat(destination)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("input destination must be a real directory")
	}
	expected := map[string]Entry{}
	for _, entry := range b.manifest.Entries {
		expected[entry.Path] = entry
	}
	seen := map[string]bool{}
	err = filepath.WalkDir(destination, func(name string, item os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("cannot enumerate materialized inputs")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(destination, name)
		if err != nil {
			return errors.New("cannot resolve materialized input")
		}
		rel = filepath.ToSlash(rel)
		entry, ok := expected[rel]
		if !ok {
			return errors.New("materialized input contains unexpected entry")
		}
		seen[rel] = true
		info, err := item.Info()
		if err != nil {
			return errors.New("cannot inspect materialized input")
		}
		if uint32(info.Mode().Perm()) != entry.Mode || supportedMode(info.Mode()) != nil {
			return errors.New("materialized input mode differs")
		}
		switch entry.Kind {
		case "directory":
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("materialized input type differs")
			}
		case "file":
			if !info.Mode().IsRegular() || info.Size() != entry.Size {
				return errors.New("materialized input type or size differs")
			}
			c := collector{ctx: ctx, b: &Bundle{request: b.request, manifest: Manifest{}}}
			data, err := c.readFile(name, info)
			if err != nil {
				return err
			}
			digest := sha256.Sum256(data)
			if hex.EncodeToString(digest[:]) != entry.SHA256 {
				return errors.New("materialized input bytes differ")
			}
		case "symlink":
			if info.Mode()&os.ModeSymlink == 0 {
				return errors.New("materialized input type differs")
			}
			link, err := os.Readlink(name)
			if err != nil || filepath.ToSlash(link) != entry.MaterializedLink || filepath.IsAbs(link) {
				return errors.New("materialized input link differs")
			}
			resolved, err := filepath.EvalSymlinks(name)
			if err != nil || !within(destination, resolved) {
				return errors.New("materialized input link escapes or is broken")
			}
		default:
			return errors.New("invalid captured input type")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(expected) {
		return errors.New("materialized input is missing an entry")
	}
	return nil
}

// EntryPaths gives the sorted complete materialized inventory without source
// bytes or machine-specific original root paths.
func (b *Bundle) EntryPaths() []string {
	if b == nil {
		return nil
	}
	paths := make([]string, 0, len(b.manifest.Entries))
	for _, entry := range b.manifest.Entries {
		paths = append(paths, entry.Path)
	}
	sort.Strings(paths)
	return paths
}
