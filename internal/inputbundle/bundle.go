// Package inputbundle captures explicitly declared filesystem inputs in a
// bounded immutable envelope. Binding inputs is not execution isolation: callers
// must independently enforce that a runner cannot read outside this envelope.
package inputbundle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	MaxEntries         = 50000
	MaxFileBytes int64 = 64 << 20
	MaxBytes     int64 = 512 << 20
	MaxDepth           = 128
)

// Root permits reads beneath Path and defines their location in the envelope.
// Source roots and mounts must not overlap. Paths are never emitted in a manifest.
type Root struct {
	Name  string
	Path  string
	Mount string
	alias string
}

// Input selects one file or complete directory tree. Generated marks provenance,
// not an ignore exemption. Missing inputs always fail capture.
type Input struct {
	Root      string `json:"root"`
	Path      string `json:"path"`
	Generated bool   `json:"generated"`
}

type Limits struct {
	Entries   int
	FileBytes int64
	Bytes     int64
	Depth     int
}

type Request struct {
	Roots  []Root
	Inputs []Input
	Limits Limits
}

type Mount struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Mode uint32 `json:"mode"`
}

type Entry struct {
	Path             string `json:"path"`
	Kind             string `json:"kind"`
	Mode             uint32 `json:"mode"`
	Size             int64  `json:"size,omitempty"`
	SHA256           string `json:"sha256,omitempty"`
	Link             string `json:"link,omitempty"`
	LinkSHA256       string `json:"link_sha256,omitempty"`
	MaterializedLink string `json:"materialized_link,omitempty"`
}

type Manifest struct {
	Schema  int     `json:"schema"`
	Digest  string  `json:"digest"`
	Roots   []Mount `json:"roots"`
	Inputs  []Input `json:"inputs"`
	Entries []Entry `json:"entries"`
	Bytes   int64   `json:"bytes"`
}

// Bundle keeps captured source bytes private. Returned manifests are copies.
type Bundle struct {
	request  Request
	manifest Manifest
	data     map[string][]byte
}

func (b *Bundle) Manifest() Manifest {
	if b == nil {
		return Manifest{}
	}
	m := b.manifest
	m.Roots = append([]Mount(nil), m.Roots...)
	m.Inputs = append([]Input(nil), m.Inputs...)
	m.Entries = append([]Entry(nil), m.Entries...)
	return m
}

// Capture inventories twice and rejects observable mutation. This does not
// detect historical transient writes; a qualified runner needs containment.
func Capture(ctx context.Context, request Request) (*Bundle, error) {
	normalized, err := normalize(request)
	if err != nil {
		return nil, err
	}
	first, err := capture(ctx, normalized)
	if err != nil {
		return nil, err
	}
	second, err := capture(ctx, normalized)
	if err != nil {
		return nil, err
	}
	if first.manifest.Digest != second.manifest.Digest {
		return nil, errors.New("declared input envelope changed during capture")
	}
	return first, nil
}

func (b *Bundle) VerifySources(ctx context.Context) error {
	if b == nil {
		return errors.New("missing input envelope")
	}
	current, err := Capture(ctx, b.request)
	if err != nil {
		return err
	}
	if current.manifest.Digest != b.manifest.Digest {
		return errors.New("declared input envelope changed")
	}
	return nil
}

func normalize(request Request) (Request, error) {
	r := Request{Roots: append([]Root(nil), request.Roots...), Inputs: append([]Input(nil), request.Inputs...), Limits: request.Limits}
	if len(r.Roots) == 0 || len(r.Roots) > 128 || len(r.Inputs) == 0 || len(r.Inputs) > MaxEntries {
		return Request{}, errors.New("input envelope requires bounded declared roots and inputs")
	}
	if r.Limits.Entries == 0 {
		r.Limits.Entries = MaxEntries
	}
	if r.Limits.FileBytes == 0 {
		r.Limits.FileBytes = MaxFileBytes
	}
	if r.Limits.Bytes == 0 {
		r.Limits.Bytes = MaxBytes
	}
	if r.Limits.Depth == 0 {
		r.Limits.Depth = MaxDepth
	}
	if r.Limits.Entries < 1 || r.Limits.Entries > MaxEntries || r.Limits.FileBytes < 1 || r.Limits.FileBytes > MaxFileBytes || r.Limits.Bytes < 1 || r.Limits.Bytes > MaxBytes || r.Limits.Depth < 1 || r.Limits.Depth > MaxDepth {
		return Request{}, errors.New("invalid input envelope limits")
	}
	names := map[string]bool{}
	for i := range r.Roots {
		root := &r.Roots[i]
		if !validPath(root.Name, false) || strings.Contains(root.Name, "/") || names[root.Name] || !validPath(root.Mount, true) || !filepath.IsAbs(root.Path) {
			return Request{}, errors.New("invalid or duplicate input root")
		}
		names[root.Name] = true
		info, err := os.Lstat(root.Path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return Request{}, errors.New("input roots must be existing real directories")
		}
		if root.alias == "" {
			root.alias = filepath.Clean(root.Path)
		}
		root.Path, err = filepath.EvalSymlinks(root.Path)
		if err != nil {
			return Request{}, errors.New("cannot resolve input root")
		}
		aliasInfo, aliasErr := os.Lstat(root.alias)
		resolvedAlias, resolveErr := filepath.EvalSymlinks(root.alias)
		if aliasErr != nil || resolveErr != nil || !aliasInfo.IsDir() || aliasInfo.Mode()&os.ModeSymlink != 0 || resolvedAlias != root.Path {
			return Request{}, errors.New("declared input root alias changed")
		}
		for j := 0; j < i; j++ {
			other := r.Roots[j]
			if within(root.Path, other.Path) || within(other.Path, root.Path) || root.Mount == other.Mount || (root.Mount != "." && other.Mount != "." && (nested(root.Mount, other.Mount) || nested(other.Mount, root.Mount))) {
				return Request{}, errors.New("input roots or mounts overlap")
			}
		}
	}
	seen := map[string]bool{}
	for _, input := range r.Inputs {
		if !names[input.Root] || !validPath(input.Path, true) || seen[input.Root+":"+input.Path] {
			return Request{}, errors.New("invalid or duplicate declared input")
		}
		seen[input.Root+":"+input.Path] = true
	}
	sort.Slice(r.Roots, func(i, j int) bool { return r.Roots[i].Name < r.Roots[j].Name })
	sort.Slice(r.Inputs, func(i, j int) bool {
		if r.Inputs[i].Root != r.Inputs[j].Root {
			return r.Inputs[i].Root < r.Inputs[j].Root
		}
		return r.Inputs[i].Path < r.Inputs[j].Path
	})
	return r, nil
}

func validPath(value string, dot bool) bool {
	return value != "" && len(value) <= 4096 && utf8.ValidString(value) && !strings.ContainsAny(value, "\\\x00\r\n:") && !strings.HasPrefix(value, "/") && path.Clean(value) == value && value != ".." && !strings.HasPrefix(value, "../") && (dot || value != ".")
}

func within(root, value string) bool {
	rel, err := filepath.Rel(root, value)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func nested(root, value string) bool { return value == root || strings.HasPrefix(value, root+"/") }

type collector struct {
	ctx      context.Context
	b        *Bundle
	entries  map[string]Entry
	visiting map[string]bool
	visited  map[string]bool
}

func capture(ctx context.Context, request Request) (*Bundle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b := &Bundle{request: request, data: map[string][]byte{}, manifest: Manifest{Schema: 1, Inputs: append([]Input(nil), request.Inputs...)}}
	c := &collector{ctx: ctx, b: b, entries: map[string]Entry{".": {Path: ".", Kind: "directory", Mode: 0700}}, visiting: map[string]bool{}, visited: map[string]bool{}}
	for _, root := range request.Roots {
		info, err := os.Lstat(root.Path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("input root changed")
		}
		b.manifest.Roots = append(b.manifest.Roots, Mount{Name: root.Name, Path: root.Mount, Mode: uint32(info.Mode().Perm())})
	}
	for _, input := range request.Inputs {
		for i, root := range request.Roots {
			if root.Name == input.Root {
				if err := c.collect(i, input.Path, 0); err != nil {
					return nil, err
				}
			}
		}
	}
	for _, entry := range c.entries {
		b.manifest.Entries = append(b.manifest.Entries, entry)
	}
	sort.Slice(b.manifest.Entries, func(i, j int) bool { return b.manifest.Entries[i].Path < b.manifest.Entries[j].Path })
	encoded, err := json.Marshal(b.manifest)
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaxManifestBytes {
		return nil, errors.New("input envelope manifest byte limit exceeded")
	}
	digest := sha256.Sum256(encoded)
	b.manifest.Digest = hex.EncodeToString(digest[:])
	if err := ValidateManifest(b.manifest); err != nil {
		return nil, err
	}
	return b, nil
}

func (c *collector) add(entry Entry) error {
	if !validPath(entry.Path, true) {
		return errors.New("unsupported materialized input path")
	}
	if existing, ok := c.entries[entry.Path]; ok {
		if existing != entry {
			return errors.New("ambiguous input envelope path")
		}
		return nil
	}
	if len(c.entries) >= c.b.request.Limits.Entries {
		return errors.New("input envelope entry limit exceeded")
	}
	c.entries[entry.Path] = entry
	return nil
}

// ancestors forbids implicit traversal through a symlink. Such a link must be
// explicitly captured at its own path so its identity cannot disappear.
func (c *collector) ancestors(index int, rel string) error {
	root := c.b.request.Roots[index]
	for mountParent := path.Dir(root.Mount); mountParent != "."; mountParent = path.Dir(mountParent) {
		if err := c.add(Entry{Path: mountParent, Kind: "directory", Mode: 0700}); err != nil {
			return err
		}
	}
	parts := []string{"."}
	if rel != "." {
		for parent := path.Dir(rel); parent != "."; parent = path.Dir(parent) {
			parts = append(parts, parent)
		}
	}
	sort.Strings(parts)
	for _, parent := range parts {
		info, err := os.Lstat(filepath.Join(root.Path, filepath.FromSlash(parent)))
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeType != os.ModeDir {
			return errors.New("input ancestors must be real directories")
		}
		if err := supportedMode(info.Mode()); err != nil {
			return err
		}
		mode := uint32(info.Mode().Perm())
		if root.Mount == "." && parent == "." {
			mode = 0700
		}
		if err := c.add(Entry{Path: path.Join(root.Mount, parent), Kind: "directory", Mode: mode}); err != nil {
			return err
		}
	}
	return nil
}

func supportedMode(mode os.FileMode) error {
	if mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return errors.New("special input permission bits are unsupported")
	}
	return nil
}

func (c *collector) collect(index int, rel string, depth int) error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	if depth > c.b.request.Limits.Depth || strings.Count(rel, "/")+1 > c.b.request.Limits.Depth {
		return errors.New("input envelope depth limit exceeded")
	}
	root := c.b.request.Roots[index]
	key := root.Name + ":" + rel
	if c.visiting[key] {
		return errors.New("input envelope contains a symlink cycle")
	}
	if c.visited[key] {
		return nil
	}
	c.visiting[key] = true
	defer delete(c.visiting, key)
	if err := c.ancestors(index, rel); err != nil {
		return err
	}
	host := filepath.Join(root.Path, filepath.FromSlash(rel))
	info, err := os.Lstat(host)
	if err != nil {
		return errors.New("declared input is missing or unreadable")
	}
	if err := supportedMode(info.Mode()); err != nil {
		return err
	}
	entry := Entry{Path: path.Join(root.Mount, rel), Mode: uint32(info.Mode().Perm())}
	if entry.Path == "." {
		entry.Mode = 0700
	}
	switch {
	case info.IsDir():
		entry.Kind = "directory"
		if err := c.add(entry); err != nil {
			return err
		}
		children, err := os.ReadDir(host)
		if err != nil {
			return errors.New("cannot enumerate declared input directory")
		}
		for _, child := range children {
			if !validPath(child.Name(), false) {
				return errors.New("unsupported input filename")
			}
			if err := c.collect(index, path.Join(rel, child.Name()), depth+1); err != nil {
				return err
			}
		}
	case info.Mode().IsRegular():
		entry.Kind = "file"
		data, err := c.readFile(host, info)
		if err != nil {
			return err
		}
		entry.Size = int64(len(data))
		digest := sha256.Sum256(data)
		entry.SHA256 = hex.EncodeToString(digest[:])
		if err := c.add(entry); err != nil {
			return err
		}
		c.b.data[entry.Path] = data
	case info.Mode()&os.ModeSymlink != 0:
		entry.Kind = "symlink"
		link, linkErr := os.Readlink(host)
		if linkErr != nil || link == "" || len(link) > 4096 || !utf8.ValidString(link) || strings.ContainsAny(link, "\x00\r\n\\:") || filepath.Clean(link) != link {
			return errors.New("unsupported input symlink")
		}
		linkDigest := sha256.Sum256([]byte(link))
		entry.LinkSHA256 = hex.EncodeToString(linkDigest[:])
		if !filepath.IsAbs(link) {
			entry.Link = filepath.ToSlash(link)
		}
		target := link
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(host), target)
		}
		target = filepath.Clean(target)
		targetIndex := -1
		for i, allowed := range c.b.request.Roots {
			if within(allowed.Path, target) {
				targetIndex = i
				break
			}
			// The caller's declared root may use an OS-level parent alias (for
			// example /var on macOS). Map only that exact declared alias; do
			// not implicitly resolve arbitrary symlink parents outside roots.
			if within(allowed.alias, target) {
				relative, err := filepath.Rel(allowed.alias, target)
				if err != nil {
					return errors.New("cannot resolve declared root alias")
				}
				target = filepath.Join(allowed.Path, relative)
				targetIndex = i
				break
			}
		}
		if targetIndex < 0 {
			return errors.New("input symlink escapes declared roots")
		}
		targetRoot := c.b.request.Roots[targetIndex]
		targetRel, err := filepath.Rel(targetRoot.Path, target)
		if err != nil {
			return errors.New("cannot resolve input symlink")
		}
		targetRel = filepath.ToSlash(targetRel)
		targetMount := path.Join(targetRoot.Mount, targetRel)
		materialized, err := filepath.Rel(filepath.FromSlash(path.Dir(entry.Path)), filepath.FromSlash(targetMount))
		if err != nil {
			return errors.New("cannot bind input symlink")
		}
		entry.MaterializedLink = filepath.ToSlash(materialized)
		if err := c.add(entry); err != nil {
			return err
		}
		if err := c.collect(targetIndex, targetRel, depth+1); err != nil {
			return err
		}
	default:
		return errors.New("special files are unsupported inputs")
	}
	c.visited[key] = true
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func (c *collector) readFile(name string, before os.FileInfo) ([]byte, error) {
	limits := c.b.request.Limits
	if before.Size() < 0 || before.Size() > limits.FileBytes || before.Size() > limits.Bytes-c.b.manifest.Bytes {
		return nil, errors.New("input envelope byte limit exceeded")
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, errors.New("cannot open declared input")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) || before.Mode() != opened.Mode() || before.Size() != opened.Size() {
		return nil, errors.New("input changed while opening")
	}
	data, err := io.ReadAll(contextReader{ctx: c.ctx, reader: io.LimitReader(f, before.Size()+1)})
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	current, pathErr := os.Lstat(name)
	if err != nil || pathErr != nil || !os.SameFile(before, after) || !os.SameFile(before, current) || current.Mode() != before.Mode() || after.Mode() != before.Mode() || current.Size() != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) || !current.ModTime().Equal(before.ModTime()) || int64(len(data)) != before.Size() {
		return nil, errors.New("input changed during read")
	}
	c.b.manifest.Bytes += int64(len(data))
	return data, nil
}
