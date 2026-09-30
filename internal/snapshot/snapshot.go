// Package snapshot reads immutable Git commits without mutating the checkout.
package snapshot

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Cyberlane/hayaku/internal/model"
)

const (
	maxMetadata = 16 << 20
	maxBlob     = 64 << 20
	maxTree     = 512 << 20
	maxFiles    = 50000
)

// Pair owns private materializations of exact commits. Close it after discovery.
// The checkout's index and worktree are never used as source bytes.
type Pair struct {
	Base         string
	Candidate    string
	BaseDir      string
	CandidateDir string
	Changes      []model.Change
	privateDir   string
}

func (p *Pair) Close() error {
	if p == nil || p.privateDir == "" {
		return nil
	}
	err := os.RemoveAll(p.privateDir)
	if err == nil {
		p.privateDir = ""
	}
	return err
}

// Capture resolves both revisions once, inventories raw byte changes and copies
// their blobs. Missing objects, unsupported entry types and partial reads fail.
// Staged and worktree comparisons are deliberately not accepted as commit IDs.
func Capture(ctx context.Context, root, base, candidate string) (_ *Pair, err error) {
	root, err = repositoryRoot(ctx, root)
	if err != nil {
		return nil, err
	}
	unmerged, err := git(ctx, root, maxMetadata, "ls-files", "--unmerged", "-z")
	if err != nil {
		return nil, err
	}
	if len(unmerged) != 0 {
		return nil, errors.New("unmerged index: resolve conflicts before planning")
	}
	b, err := resolve(ctx, root, base)
	if err != nil {
		return nil, fmt.Errorf("base revision: %w", err)
	}
	c, err := resolve(ctx, root, candidate)
	if err != nil {
		return nil, fmt.Errorf("candidate revision: %w", err)
	}
	diff, err := git(ctx, root, maxMetadata, "diff", "--no-ext-diff", "--no-textconv", "--name-status", "-z", "--find-renames", b, c, "--")
	if err != nil {
		return nil, err
	}
	changes, err := parseChanges(diff)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "hayaku-snapshots-")
	if err != nil {
		return nil, err
	}
	p := &Pair{Base: b, Candidate: c, Changes: changes, privateDir: dir, BaseDir: filepath.Join(dir, "base"), CandidateDir: filepath.Join(dir, "candidate")}
	defer func() {
		if err != nil {
			_ = p.Close()
		}
	}()
	for _, tree := range []struct{ id, dir string }{{b, p.BaseDir}, {c, p.CandidateDir}} {
		if err = materialize(ctx, root, tree.id, tree.dir); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// ValidateCandidate is an execution precondition, not a runtime-isolation proof.
// It requires an exact HEAD and no tracked, untracked or ignored worktree inputs.
// Git cannot observe services, environment changes or files outside this root.
func ValidateCandidate(ctx context.Context, root, id string) error {
	root, err := repositoryRoot(ctx, root)
	if err != nil {
		return err
	}
	head, err := resolve(ctx, root, "HEAD")
	if err != nil {
		return err
	}
	if head != id {
		return errors.New("checkout HEAD differs from planned candidate")
	}
	state, err := git(ctx, root, maxMetadata, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignored=matching")
	if err != nil {
		return err
	}
	if len(state) != 0 {
		return errors.New("checkout contains tracked, untracked or ignored changes")
	}
	return validateRawFiles(ctx, root, id)
}

// Git status normalizes clean/smudge filters and line endings. It is insufficient
// for binding execution to raw committed bytes, so compare blob identities too.
func validateRawFiles(ctx context.Context, root, id string) error {
	tree, err := git(ctx, root, maxMetadata, "ls-tree", "-r", "-z", "--full-tree", id)
	if err != nil {
		return err
	}
	for _, entry := range bytes.Split(bytes.TrimSuffix(tree, []byte{0}), []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		meta, name, ok := bytes.Cut(entry, []byte{'\t'})
		fields := bytes.Fields(meta)
		if !ok || len(fields) != 3 || string(fields[1]) != "blob" {
			return errors.New("unsupported execution tree entry")
		}
		if err := validPath(string(name)); err != nil {
			return err
		}
		path := filepath.Join(root, filepath.FromSlash(string(name)))
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxBlob {
			return errors.New("execution input is missing or unsupported")
		}
		if (string(fields[0]) != "100644" && string(fields[0]) != "100755") || (info.Mode().Perm()&0111 != 0) != (string(fields[0]) == "100755") {
			return errors.New("execution file mode differs from candidate tree")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if len(data) > maxBlob {
			return errors.New("execution input exceeds byte limit")
		}
		var h hash.Hash = sha1.New()
		if len(id) == 64 {
			h = sha256.New()
		}
		_, _ = fmt.Fprintf(h, "blob %d\x00", len(data))
		_, _ = h.Write(data)
		if fmt.Sprintf("%x", h.Sum(nil)) != string(fields[2]) {
			return errors.New("execution input bytes differ from candidate tree")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

func repositoryRoot(ctx context.Context, root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	data, err := git(ctx, abs, maxMetadata, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	found := strings.TrimSuffix(string(data), "\n")
	found, err = filepath.EvalSymlinks(found)
	if err != nil {
		return "", err
	}
	if found != abs {
		return "", errors.New("root must be the Git repository top level")
	}
	return abs, nil
}

func resolve(ctx context.Context, root, revision string) (string, error) {
	if revision == "" || len(revision) > 1024 || strings.ContainsRune(revision, 0) {
		return "", errors.New("invalid revision")
	}
	data, err := git(ctx, root, maxMetadata, "rev-parse", "--verify", "--end-of-options", revision+"^{commit}")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(data))
	if len(id) != 40 && len(id) != 64 {
		return "", errors.New("invalid commit object identity")
	}
	for _, c := range id {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return "", errors.New("invalid commit object identity")
		}
	}
	return id, nil
}

func parseChanges(data []byte) ([]model.Change, error) {
	result := []model.Change{}
	if len(data) == 0 {
		return result, nil
	}
	if data[len(data)-1] != 0 {
		return nil, errors.New("truncated Git change inventory")
	}
	fields := bytes.Split(data[:len(data)-1], []byte{0})
	for i := 0; i < len(fields); {
		status := string(fields[i])
		i++
		if status == "" || i >= len(fields) {
			return nil, errors.New("malformed Git change inventory")
		}
		change := model.Change{Status: status[:1], Path: string(fields[i])}
		i++
		switch status[0] {
		case 'A', 'M', 'D', 'T':
			if len(status) != 1 {
				return nil, errors.New("malformed Git change status")
			}
		case 'R', 'C':
			score, scoreErr := strconv.Atoi(status[1:])
			if scoreErr != nil || score < 0 || score > 100 || strings.ContainsAny(status[1:], "+-") {
				return nil, errors.New("malformed Git rename score")
			}
			if i >= len(fields) {
				return nil, errors.New("truncated Git rename inventory")
			}
			change.OldPath, change.Path = change.Path, string(fields[i])
			i++
		default:
			return nil, errors.New("unsupported Git change status")
		}
		if err := validPath(change.Path); err != nil {
			return nil, err
		}
		if change.OldPath != "" {
			if err := validPath(change.OldPath); err != nil {
				return nil, err
			}
		}
		result = append(result, change)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result, nil
}

func validPath(path string) error {
	if path == "" || !utf8.ValidString(path) || strings.ContainsRune(path, 0) || !filepath.IsLocal(filepath.FromSlash(path)) || filepath.ToSlash(filepath.Clean(path)) != path {
		return errors.New("unsupported or unsafe Git path")
	}
	// Git metadata in a snapshot could redirect native discovery to another tree.
	for _, part := range strings.Split(path, "/") {
		if strings.EqualFold(part, ".git") || strings.Contains(part, "\\") {
			return errors.New("unsupported Git metadata or platform-specific path")
		}
	}
	return nil
}

func materialize(ctx context.Context, root, id, dir string) error {
	data, err := git(ctx, root, maxMetadata, "ls-tree", "-r", "-z", "--full-tree", id)
	if err != nil {
		return err
	}
	if len(data) != 0 && data[len(data)-1] != 0 {
		return errors.New("truncated Git tree inventory")
	}
	entries := bytes.Split(bytes.TrimSuffix(data, []byte{0}), []byte{0})
	if len(data) == 0 {
		entries = nil
	}
	if len(entries) > maxFiles {
		return errors.New("Git tree exceeds file count limit")
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	total := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		meta, path, ok := bytes.Cut(entry, []byte{'\t'})
		fields := bytes.Fields(meta)
		if !ok || len(fields) != 3 {
			return errors.New("malformed Git tree inventory")
		}
		mode := string(fields[0])
		if string(fields[1]) != "blob" || (mode != "100644" && mode != "100755") {
			return errors.New("snapshot contains unsupported symlink or submodule")
		}
		name := string(path)
		if err := validPath(name); err != nil {
			return err
		}
		blob, err := git(ctx, root, maxBlob, "cat-file", "blob", string(fields[2]))
		if err != nil {
			return err
		}
		total += len(blob)
		if total > maxTree {
			return errors.New("Git tree exceeds byte limit")
		}
		destination := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			return err
		}
		permission := os.FileMode(0600)
		if mode == "100755" {
			permission = 0700
		}
		file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, permission)
		if err != nil {
			return err
		}
		_, writeErr := file.Write(blob)
		closeErr := file.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			return err
		}
	}
	return nil
}

type boundedBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
	full  bool
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(data) > b.limit-b.buf.Len() {
		b.full = true
		return 0, errors.New("Git output exceeds limit")
	}
	return b.buf.Write(data)
}

func git(ctx context.Context, root string, limit int, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-pager", "-c", "core.fsmonitor=false", "-C", root}, args...)...)
	cmd.WaitDelay = 2 * time.Second
	// Inherited GIT_DIR/WORK_TREE/index/object replacement variables must not
	// silently redirect the repository or substitute committed object bytes.
	for _, variable := range os.Environ() {
		key, _, _ := strings.Cut(variable, "=")
		if !strings.HasPrefix(key, "GIT_") {
			cmd.Env = append(cmd.Env, variable)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_OPTIONAL_LOCKS=0")
	stdout := &boundedBuffer{limit: limit}
	stderr := &boundedBuffer{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if stdout.full || stderr.full {
			return nil, errors.New("Git output exceeds limit")
		}
		return nil, fmt.Errorf("Git %s failed: %w", args[0], err)
	}
	return stdout.buf.Bytes(), nil
}

var _ io.Writer = (*boundedBuffer)(nil)
