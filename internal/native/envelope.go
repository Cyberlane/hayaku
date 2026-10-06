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
	"syscall"
	"unicode/utf8"

	"github.com/Cyberlane/hayaku/internal/inputbundle"
)

const envelopeVersion = "native-normalized-inputs-v3"
const MaxScratchBytes int64 = 64 << 20

// EnvelopeRequest describes a closed, normalized resource filesystem. It does
// not promise original host metadata or grant authority to skip execution.
// Absent paths are relative to the inputbundle mounts and must lie beneath a
// completely selected real directory. ScratchBytes enables fresh empty /scratch;
// zero denies writes. Only Env is passed to the program, without host merging.
// Capture is descriptive: materialization can reject filesystem aliases that
// route a declared absence through a different, incompletely selected mount.
type EnvelopeRequest struct {
	Files        inputbundle.Request
	Absent       []string
	Env          []string
	ScratchBytes int64
	Child        *ChildRequest
}

// Envelope retains captured bytes and values privately. Original modes, link
// identity and selection declarations remain bound even though runtime resources
// use fixed readonly modes. Capture/verification is not runtime qualification.
// A Generated flag describes selection; it does not establish its producer.
type Envelope struct {
	files        *inputbundle.Bundle
	sources      []envelopeSource
	absent       []string
	env          []string
	scratchBytes int64
	digest       string
	child        *capturedChild
}

// Source paths stay private; only their identity digest leaves the envelope.
type envelopeSource struct {
	Name, Path, Mount string
}

func CaptureEnvelope(ctx context.Context, request EnvelopeRequest) (*Envelope, error) {
	if request.ScratchBytes < 0 || request.ScratchBytes > MaxScratchBytes || len(request.Env) > 128 || len(request.Absent) > inputbundle.MaxEntries {
		return nil, errors.New("native envelope policy exceeds bounds")
	}
	if len(request.Files.Roots) == 0 || len(request.Files.Roots) > 128 || len(request.Files.Inputs) == 0 || len(request.Files.Inputs) > inputbundle.MaxEntries {
		return nil, errors.New("native envelope source declarations exceed bounds")
	}
	for _, root := range request.Files.Roots {
		if !envelopePath(root.Name) || !envelopePath(root.Mount) || len(root.Path) > 4096 || !utf8.ValidString(root.Path) || strings.ContainsAny(root.Path, "\x00\r\n") || !filepath.IsAbs(root.Path) {
			return nil, errors.New("invalid native envelope source mapping")
		}
	}
	for _, input := range request.Files.Inputs {
		if !envelopePath(input.Root) || !envelopePath(input.Path) {
			return nil, errors.New("invalid native envelope source selection")
		}
	}
	request.Files.Roots = append([]inputbundle.Root(nil), request.Files.Roots...)
	request.Files.Inputs = append([]inputbundle.Input(nil), request.Files.Inputs...)
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
	for _, root := range request.Files.Roots {
		e.sources = append(e.sources, envelopeSource{root.Name, root.Path, root.Mount})
	}
	sort.Slice(e.sources, func(i, j int) bool { return e.sources[i].Name < e.sources[j].Name })
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
	e.child, err = captureChild(ctx, request.Child)
	if err != nil {
		return nil, err
	}
	manifest := e.files.Manifest()
	if e.child != nil && manifest.Bytes > inputbundle.MaxBytes-int64(len(e.child.data)) {
		return nil, errors.New("native envelope and managed child exceed byte bounds")
	}
	entries := map[string]inputbundle.Entry{}
	for _, entry := range manifest.Entries {
		entries[entry.Path] = entry
		// Rewriting link text would change readlink and path-resolution semantics.
		if entry.Kind == "symlink" && (entry.Link == "" || entry.Link != entry.MaterializedLink) {
			return nil, errors.New("native envelope requires unchanged relative links")
		}
	}
	completeDirectories := envelopeCompleteDirectories(manifest, entries)
	for _, name := range e.absent {
		if err := e.validateAbsenceCoverage(name, entries, completeDirectories); err != nil {
			return nil, err
		}
	}
	e.digest = e.currentDigest()
	if err := e.VerifySources(ctx); err != nil {
		return nil, err
	}
	return e, nil
}

func envelopeCompleteDirectories(manifest inputbundle.Manifest, entries map[string]inputbundle.Entry) map[string]bool {
	mounts := map[string]string{}
	for _, root := range manifest.Roots {
		mounts[root.Name] = root.Path
	}
	complete := map[string]bool{}
	for _, input := range manifest.Inputs {
		name := path.Join(mounts[input.Root], input.Path)
		if entries[name].Kind == "directory" {
			complete[input.Root+":"+name] = true
		}
	}
	return complete
}

func (e *Envelope) validateAbsenceCoverage(name string, entries map[string]inputbundle.Entry, complete map[string]bool) error {
	if _, exists := entries[name]; exists {
		return errors.New("native envelope absent input exists")
	}
	source, _, found := e.absenceSource(name)
	if !found {
		return errors.New("native envelope absence lacks declared source")
	}
	covered := false
	for parent, depth := path.Dir(name), 0; ; parent, depth = path.Dir(parent), depth+1 {
		if depth > inputbundle.MaxDepth {
			return errors.New("native envelope absence exceeds depth bounds")
		}
		if entry, exists := entries[parent]; exists && entry.Kind != "directory" {
			return errors.New("native envelope absence traverses a non-directory")
		}
		if complete[source.Name+":"+parent] {
			covered = true
		}
		if parent == "." {
			break
		}
	}
	if !covered {
		return errors.New("native envelope absence lacks complete directory capture")
	}
	return nil
}

func envelopePath(name string) bool {
	return len(name) <= 4096 && utf8.ValidString(name) && fs.ValidPath(name) && !strings.ContainsAny(name, "\\:\x00\r\n")
}

func (e *Envelope) currentDigest() string {
	return identity(struct {
		Contract, Files string
		Sources         []envelopeSource
		Absent, Env     []string
		ScratchBytes    int64
		Child           string
	}{envelopeVersion, e.files.Manifest().Digest, e.sources, e.absent, e.env, e.scratchBytes, e.child.currentDigest()})
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
	if err := e.files.VerifySources(ctx); err != nil {
		return err
	}
	if e.child != nil {
		if err := e.child.verifySources(ctx); err != nil {
			return err
		}
	}
	for _, name := range e.absent {
		source, relative, found := e.absenceSource(name)
		if !found {
			return errors.New("native envelope absence lacks declared source")
		}
		if err := verifyEnvelopeAbsence(ctx, source.Path, relative); err != nil {
			return err
		}
	}
	return nil
}

// The most specific mount owns a logical path, even when another root at "."
// supplies its synthetic parents. That other root cannot prove its absence.
func (e *Envelope) absenceSource(name string) (envelopeSource, string, bool) {
	var selected envelopeSource
	found := false
	for _, source := range e.sources {
		if source.Mount == "." || name == source.Mount || strings.HasPrefix(name, source.Mount+"/") {
			if !found || (source.Mount != "." && (selected.Mount == "." || len(source.Mount) > len(selected.Mount))) {
				selected, found = source, true
			}
		}
	}
	relative := name
	if selected.Mount != "." {
		relative = strings.TrimPrefix(strings.TrimPrefix(name, selected.Mount), "/")
		if relative == "" {
			relative = "."
		}
	}
	return selected, relative, found
}

// Probe the real filesystem rather than folding identities. Lstat detects
// case/Unicode aliases and prevents an absent leaf from concealing traversal
// through a symlink or file. Only ENOENT establishes absence.
func verifyEnvelopeAbsence(ctx context.Context, root, relative string) error {
	_, err := resolveEnvelopeAbsence(ctx, root, relative, nil)
	return err
}

// Existing copied directories determine the canonical mount spelling through
// real inode identity. This prevents an alias of a different mount from borrowing
// complete-directory coverage from the root at ".". No identities are folded.
func resolveEnvelopeAbsence(ctx context.Context, root, relative string, directories map[string][]string) (string, error) {
	if !filepath.IsAbs(root) || !envelopePath(relative) || relative == "." || strings.Count(relative, "/") >= inputbundle.MaxDepth {
		return "", errors.New("invalid native envelope absence mapping")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("native envelope absence root unavailable")
	}
	destination, canonical := root, "."
	components := strings.Split(relative, "/")
	for i, component := range components {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		root = filepath.Join(root, component)
		info, err = os.Lstat(root)
		if errors.Is(err, syscall.ENOENT) {
			return path.Join(canonical, path.Join(components[i:]...)), nil
		}
		if err != nil {
			return "", errors.New("native envelope absence lookup unavailable")
		}
		if i == len(components)-1 {
			return "", errors.New("native envelope absent input exists")
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("native envelope absence traverses a non-directory")
		}
		candidate := path.Join(canonical, component)
		_, captured := directories[candidate]
		if directories != nil && !captured {
			candidate = ""
			for _, name := range directories[canonical] {
				if err := ctx.Err(); err != nil {
					return "", err
				}
				other, err := os.Lstat(filepath.Join(destination, filepath.FromSlash(name)))
				if err != nil {
					return "", errors.New("native envelope absence directory unavailable")
				}
				if os.SameFile(info, other) {
					candidate = name
					break
				}
			}
			if candidate == "" {
				return "", errors.New("native envelope absence directory unmapped")
			}
		}
		canonical = candidate
	}
	return "", errors.New("native envelope absent input exists")
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
	complete := envelopeCompleteDirectories(e.files.Manifest(), expected)
	directories := map[string][]string{}
	for name, entry := range expected {
		if entry.Kind != "directory" {
			continue
		}
		if _, exists := directories[name]; !exists {
			directories[name] = nil
		}
		if name != "." {
			parent := path.Dir(name)
			directories[parent] = append(directories[parent], name)
		}
	}
	for _, name := range e.absent {
		canonical, err := resolveEnvelopeAbsence(ctx, destination, name, directories)
		if err != nil {
			return err
		}
		if err := e.validateAbsenceCoverage(canonical, expected, complete); err != nil {
			return err
		}
		source, relative, _ := e.absenceSource(canonical)
		if err := verifyEnvelopeAbsence(ctx, source.Path, relative); err != nil {
			return err
		}
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
