package capsule

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Cyberlane/hayaku/internal/inputbundle"
)

// CaptureBundle captures generated and linked workspace inputs and normalizes
// their verified immutable view into regular-file aliases for this WASI-only
// contract. The manifest digest must also be bound into ProducerIdentity, so
// changes to original link identities/modes/provenance always invalidate reuse.
// Original symlink/readlink semantics are deliberately not qualified.
func CaptureBundle(ctx context.Context, request inputbundle.Request) ([]File, string, error) {
	bundle, err := inputbundle.Capture(ctx, request)
	if err != nil {
		return nil, "", err
	}
	files, err := CapsuleFiles(ctx, bundle)
	if err != nil {
		return nil, "", err
	}
	return files, bundle.Manifest().Digest, nil
}

// CapsuleFiles copies only a verified private materialization. It preserves
// virtual alias names, complete directory membership (including empty dirs),
// and bytes, while normalizing metadata to the fixed capsule host semantics.
func CapsuleFiles(ctx context.Context, bundle *inputbundle.Bundle) ([]File, error) {
	if bundle == nil || !validDigest(bundle.Manifest().Digest) {
		return nil, fmt.Errorf("capsule requires a captured input envelope")
	}
	if err := bundle.VerifySources(ctx); err != nil {
		return nil, err
	}
	temporary, err := os.MkdirTemp("", "hayaku-capsule-inputs-")
	if err != nil {
		return nil, fmt.Errorf("capsule input staging is unavailable")
	}
	defer removePrivateStage(temporary)
	view := filepath.Join(temporary, "view")
	if err := bundle.Materialize(ctx, view); err != nil {
		return nil, err
	}
	view, err = filepath.EvalSymlinks(view)
	if err != nil {
		return nil, fmt.Errorf("capsule input staging cannot be resolved")
	}
	files, err := flattenEnvelope(ctx, view)
	if err != nil {
		return nil, err
	}
	if err := bundle.VerifyMaterialization(ctx, view); err != nil {
		return nil, err
	}
	if err := bundle.VerifySources(ctx); err != nil {
		return nil, err
	}
	return files, nil
}

func removePrivateStage(directory string) {
	// Materialized directory modes can be readonly. Only directories in this
	// freshly created private stage are made traversable/removable; WalkDir
	// does not follow symlinks into any original source or external root.
	_ = filepath.WalkDir(directory, func(name string, item fs.DirEntry, _ error) error {
		if item != nil && item.IsDir() && item.Type()&fs.ModeSymlink == 0 {
			_ = os.Chmod(name, 0700)
		}
		return nil
	})
	_ = os.RemoveAll(directory)
}

func flattenEnvelope(ctx context.Context, view string) ([]File, error) {
	var files []File
	var total int64
	visiting := make(map[string]bool)
	var visit func(string, string, int) error
	visit = func(virtual, physical string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > inputbundle.MaxDepth || len(files) >= MaxInputFiles {
			return fmt.Errorf("capsule normalized inputs exceed depth or entry bound")
		}
		resolved, err := filepath.EvalSymlinks(physical)
		if err != nil {
			return fmt.Errorf("capsule normalized input has a broken or cyclic link")
		}
		relative, err := filepath.Rel(view, resolved)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			return fmt.Errorf("capsule normalized input escapes its captured view")
		}
		if visiting[resolved] {
			return fmt.Errorf("capsule normalized inputs contain a directory cycle")
		}
		info, err := os.Lstat(resolved)
		if err != nil || info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("capsule normalized input cannot be inspected")
		}
		if info.Mode().IsRegular() {
			captured, err := LoadFiles(ctx, view, []string{filepath.ToSlash(relative)})
			if err != nil {
				return err
			}
			total += int64(len(captured[0].Data))
			if total > MaxInputBytes || !validInputPath(virtual) {
				return fmt.Errorf("capsule normalized input exceeds byte or path bound")
			}
			files = append(files, File{Path: virtual, Data: captured[0].Data})
			return nil
		}
		if !info.IsDir() {
			return fmt.Errorf("capsule normalized input is not a regular file or directory")
		}
		visiting[resolved] = true
		defer delete(visiting, resolved)
		if virtual != "." {
			if !validInputPath(virtual) {
				return fmt.Errorf("capsule normalized directory path is invalid")
			}
			files = append(files, File{Path: virtual, Directory: true})
		}
		entries, err := os.ReadDir(resolved)
		if err != nil {
			return fmt.Errorf("capsule normalized directory cannot be enumerated")
		}
		for _, entry := range entries {
			if err := visit(path.Join(virtual, entry.Name()), filepath.Join(resolved, entry.Name()), depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(".", view, 0); err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}
