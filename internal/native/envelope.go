package native

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Cyberlane/hayaku/internal/inputbundle"
)

const envelopeVersion = "native-normalized-inputs-v1"
const MaxScratchBytes int64 = 64 << 20

// EnvelopeRequest describes a closed, normalized resource filesystem. It does
// not promise original host metadata or grant authority to skip execution.
// Absent paths are relative to the inputbundle mounts and must lie beneath a
// completely selected real directory. ScratchBytes enables fresh empty /scratch;
// zero denies writes. Only Env is passed to the program, without host merging.
type EnvelopeRequest struct {
	Files        inputbundle.Request
	Absent       []string
	Env          []string
	ScratchBytes int64
}

// Envelope retains captured bytes and values privately. Original modes, link
// identity and selection declarations remain bound even though runtime resources
// use fixed readonly modes. Capture/verification is not runtime qualification.
// A Generated flag describes selection; it does not establish its producer.
type Envelope struct {
	files        *inputbundle.Bundle
	absent       []string
	env          []string
	scratchBytes int64
	digest       string
}

func CaptureEnvelope(ctx context.Context, request EnvelopeRequest) (*Envelope, error) {
	if request.ScratchBytes < 0 || request.ScratchBytes > MaxScratchBytes || len(request.Env) > 128 || len(request.Absent) > inputbundle.MaxEntries {
		return nil, errors.New("native envelope policy exceeds bounds")
	}
	seenEnv := map[string]bool{}
	bytes := 0
	for _, entry := range request.Env {
		if len(entry) > 65536-bytes {
			return nil, errors.New("native envelope policy exceeds bounds")
		}
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" || !utf8.ValidString(entry) || strings.ContainsRune(entry, 0) || seenEnv[key] {
			return nil, errors.New("invalid native envelope environment")
		}
		seenEnv[key] = true
		bytes += len(entry)
	}
	for _, name := range request.Absent {
		if len(name) > 65536-bytes {
			return nil, errors.New("native envelope policy exceeds bounds")
		}
		bytes += len(name)
		if !envelopePath(name) || name == "." {
			return nil, errors.New("invalid native envelope absence policy")
		}
	}
	e := &Envelope{absent: append([]string(nil), request.Absent...), env: append([]string(nil), request.Env...), scratchBytes: request.ScratchBytes}
	sort.Strings(e.absent)
	for i := 1; i < len(e.absent); i++ {
		if e.absent[i] == e.absent[i-1] {
			return nil, errors.New("duplicate native envelope absence policy")
		}
	}
	var err error
	e.files, err = inputbundle.Capture(ctx, request.Files)
	if err != nil {
		return nil, err
	}
	manifest := e.files.Manifest()
	entries := map[string]inputbundle.Entry{}
	for _, entry := range manifest.Entries {
		entries[entry.Path] = entry
		// Rewriting link text would change readlink and path-resolution semantics.
		if entry.Kind == "symlink" && (entry.Link == "" || entry.Link != entry.MaterializedLink) {
			return nil, errors.New("native envelope requires unchanged relative links")
		}
	}
	mounts := map[string]string{}
	for _, root := range manifest.Roots {
		mounts[root.Name] = root.Path
	}
	completeDirectories := map[string]bool{}
	for _, input := range manifest.Inputs {
		name := path.Join(mounts[input.Root], input.Path)
		if entries[name].Kind == "directory" {
			completeDirectories[name] = true
		}
	}
	for _, name := range e.absent {
		if _, exists := entries[name]; exists {
			return nil, errors.New("native envelope absent input exists")
		}
		covered := false
		for parent, depth := path.Dir(name), 0; ; parent, depth = path.Dir(parent), depth+1 {
			if depth > inputbundle.MaxDepth {
				return nil, errors.New("native envelope absence exceeds depth bounds")
			}
			if entry, exists := entries[parent]; exists && entry.Kind != "directory" {
				return nil, errors.New("native envelope absence traverses a non-directory")
			}
			if completeDirectories[parent] {
				covered = true
			}
			if parent == "." {
				break
			}
		}
		if !covered {
			return nil, errors.New("native envelope absence lacks complete directory capture")
		}
	}
	e.digest = e.currentDigest()
	if err := e.VerifySources(ctx); err != nil {
		return nil, err
	}
	return e, nil
}

func envelopePath(name string) bool {
	return len(name) <= 4096 && utf8.ValidString(name) && fs.ValidPath(name) && !strings.ContainsAny(name, "\\:\x00\r\n")
}

func (e *Envelope) currentDigest() string {
	return identity(struct {
		Contract, Files string
		Absent, Env     []string
		ScratchBytes    int64
	}{envelopeVersion, e.files.Manifest().Digest, e.absent, e.env, e.scratchBytes})
}

// Digest is identity data, never a provider proof or execution receipt.
func (e *Envelope) Digest() string {
	if e == nil {
		return ""
	}
	return e.digest
}

func (e *Envelope) VerifySources(ctx context.Context) error {
	if e == nil || e.files == nil || e.digest == "" || e.currentDigest() != e.digest {
		return errors.New("native envelope identity changed or unavailable")
	}
	return e.files.VerifySources(ctx)
}

// materialize copies only captured resources and verifies the normalized copy.
// The caller must enforce its read boundary and fresh scratch policy; this
// helper alone cannot qualify a program or detect ignored denied access.
func (e *Envelope) materialize(ctx context.Context, destination string) (err error) {
	if err = e.VerifySources(ctx); err != nil {
		return err
	}
	if err = e.files.Materialize(ctx, destination); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = removeEnvelopeCopy(destination)
		}
	}()
	entries := e.files.Manifest().Entries
	for i := len(entries) - 1; i >= 0; i-- {
		if err = ctx.Err(); err != nil {
			return err
		}
		entry := entries[i]
		if entry.Kind == "symlink" {
			continue
		}
		mode := os.FileMode(0444)
		if entry.Kind == "directory" {
			mode = 0555
		}
		if err = os.Chmod(filepath.Join(destination, filepath.FromSlash(entry.Path)), mode); err != nil {
			return errors.New("cannot normalize native envelope mode")
		}
	}
	if err = e.verifyMaterialization(ctx, destination); err != nil {
		return err
	}
	return e.VerifySources(ctx)
}

func (e *Envelope) verifyMaterialization(ctx context.Context, destination string) error {
	if e == nil || e.files == nil || e.currentDigest() != e.digest || !filepath.IsAbs(destination) {
		return errors.New("native envelope copy identity unavailable")
	}
	expected := map[string]inputbundle.Entry{}
	for _, entry := range e.files.Manifest().Entries {
		expected[entry.Path] = entry
	}
	seen := 0
	err := filepath.WalkDir(destination, func(name string, item os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("native envelope copy unavailable")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(destination, name)
		if err != nil {
			return errors.New("native envelope copy path invalid")
		}
		entry, exists := expected[filepath.ToSlash(rel)]
		if !exists {
			return errors.New("native envelope copy contains unexpected input")
		}
		seen++
		info, err := item.Info()
		if err != nil || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return errors.New("native envelope copy metadata differs")
		}
		switch entry.Kind {
		case "directory":
			if !info.IsDir() || info.Mode().Perm() != 0555 {
				return errors.New("native envelope copy directory differs")
			}
		case "file":
			if !info.Mode().IsRegular() || info.Mode().Perm() != 0444 || info.Size() != entry.Size {
				return errors.New("native envelope copy file differs")
			}
			data, err := readBounded(name, inputbundle.MaxFileBytes)
			if err != nil || digest(data) != entry.SHA256 {
				return errors.New("native envelope copy bytes differ")
			}
		case "symlink":
			link, err := os.Readlink(name)
			if err != nil || info.Mode()&os.ModeSymlink == 0 || filepath.ToSlash(link) != entry.Link {
				return errors.New("native envelope copy link differs")
			}
		default:
			return errors.New("native envelope copy type differs")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if seen != len(expected) {
		return errors.New("native envelope copy is incomplete")
	}
	return nil
}

// Copies have readonly directories. Restore private owner permissions before
// removing them; guest access is already over when the backend calls cleanup.
func removeEnvelopeCopy(destination string) error {
	_ = filepath.WalkDir(destination, func(name string, item os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if item.IsDir() {
			return os.Chmod(name, 0700)
		}
		return nil
	})
	return os.RemoveAll(destination)
}
