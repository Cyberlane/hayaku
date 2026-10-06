// Package buildclosure binds observed build stages without granting runtime or
// omission authority. Language bridges supply build and collection semantics.
package buildclosure

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/Cyberlane/hayaku/internal/inputbundle"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/snapshot"
)

const Version = "observed-build-closure-v2-native-stages"

// ErrNativeFailure identifies a completed native nonzero exit. Only when the
// session's integrity checks also pass does its output generation remain usable.
// Observed native execution never supplies runtime or omission authority.
var ErrNativeFailure = errors.New("observed native execution failed")

type Request struct {
	Source, Revision string
	Environment      []string
	Tools            []string
	// Files are bounded, caller-owned build setup inputs such as protocol queries.
	Files map[string][]byte
}

// Record contains identities, never diagnostics, environment values or paths.
type Record struct {
	ID, InputDigest, InvocationDigest, OutputDigest string
}

type tool struct {
	path, resolved, digest string
	alias, file            os.FileInfo
}

type guard struct {
	path, resolved string
	info           os.FileInfo
}

// Session is a serial, private build lifecycle. Producers may change only the
// owned build tree; read-only collection may change neither source nor build.
type Session struct {
	mu                     sync.Mutex
	request                Request
	private, source, build string
	sourceBundle           *inputbundle.Bundle
	buildBundle            *inputbundle.Bundle
	inputs                 []*inputbundle.Bundle
	tools                  []tool
	records                []Record
	immutable, outputs     []guard
	deadline               time.Time
	queries                []string
	owner                  *os.Root
	privateInfo            os.FileInfo
	closed, broken         bool
}

func New(ctx context.Context, request Request) (_ *Session, err error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if !text(request.Source, 4096) || !filepath.IsAbs(request.Source) || filepath.Clean(request.Source) != request.Source || !text(request.Revision, 256) || len(request.Tools) == 0 || len(request.Tools) > 64 || request.Environment == nil || len(request.Environment) > 4096 {
		return nil, errors.New("invalid explicit build context")
	}
	request.Tools = append([]string(nil), request.Tools...)
	request.Environment = append([]string{}, request.Environment...)
	if len(request.Files) > 1024 {
		return nil, errors.New("build setup input count exceeds bounds")
	}
	files, setupBytes := map[string][]byte{}, 0
	for name, data := range request.Files {
		setupBytes += len(data)
		if !text(name, 4096) || filepath.IsAbs(name) || filepath.ToSlash(filepath.Clean(name)) != name || name == "." || strings.HasPrefix(name, "../") || len(data) > 64<<10 || setupBytes > 1<<20 {
			return nil, errors.New("invalid bounded build setup input")
		}
		files[name] = append([]byte(nil), data...)
	}
	request.Files = files
	keys, bytes := map[string]bool{}, 0
	for _, pair := range request.Environment {
		key, _, ok := strings.Cut(pair, "=")
		bytes += len(pair)
		if !ok || key == "" || keys[key] || !text(pair, 32768) || bytes > 1<<20 {
			return nil, errors.New("invalid bounded build environment")
		}
		keys[key] = true
	}
	sort.Strings(request.Environment)
	deadline, _ := ctx.Deadline()
	s := &Session{request: request, deadline: deadline}
	defer func() {
		if err != nil && s.private != "" {
			err = errors.Join(err, s.cleanup())
		}
	}()
	s.sourceBundle, err = capture(ctx, request.Source)
	if err != nil {
		return nil, err
	}
	s.immutable, err = observe(ctx, request.Source)
	if err != nil {
		return nil, err
	}
	s.private, err = os.MkdirTemp("", "hayaku-build-closure-")
	if err != nil {
		return nil, err
	}
	s.privateInfo, err = os.Lstat(s.private)
	if err != nil {
		return nil, err
	}
	s.owner, err = os.OpenRoot(s.private)
	if err != nil {
		return nil, err
	}
	resolvedPrivate, err := filepath.EvalSymlinks(s.private)
	if err != nil {
		return nil, err
	}
	s.private = resolvedPrivate
	s.source, s.build = filepath.Join(s.private, "source"), filepath.Join(s.private, "build")
	if err = s.sourceBundle.Materialize(ctx, s.source); err != nil {
		return nil, err
	}
	copyGuards, err := observe(ctx, s.source)
	if err != nil {
		return nil, err
	}
	s.immutable = append(s.immutable, copyGuards...)
	if err = os.Mkdir(s.build, 0700); err != nil {
		return nil, err
	}
	for name, data := range request.Files {
		target := filepath.Join(s.build, filepath.FromSlash(name))
		if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return nil, err
		}
		var f *os.File
		f, err = os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		_, writeErr := f.Write(data)
		err = errors.Join(writeErr, f.Close())
		if err != nil {
			return nil, err
		}
	}
	s.buildBundle, err = capture(ctx, s.build)
	if err != nil {
		return nil, err
	}
	s.outputs, err = observe(ctx, s.build)
	if err != nil {
		return nil, err
	}
	for _, name := range request.Tools {
		if err = s.captureTool(ctx, name, false); err != nil {
			return nil, err
		}
	}
	s.privateInfo, err = os.Lstat(s.private)
	if err != nil {
		return nil, err
	}
	err = s.verify(ctx, true)
	return s, err
}

func text(s string, limit int) bool {
	return s != "" && len(s) <= limit && utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n")
}

func identity(v any) string {
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func capture(ctx context.Context, root string) (*inputbundle.Bundle, error) {
	return inputbundle.Capture(ctx, inputbundle.Request{Roots: []inputbundle.Root{{Name: "input", Path: root, Mount: "."}}, Inputs: []inputbundle.Input{{Root: "input", Path: "."}}})
}

func same(a, b os.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) && a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func (s *Session) Source() string { s.mu.Lock(); defer s.mu.Unlock(); return s.source }
func (s *Session) Build() string  { s.mu.Lock(); defer s.mu.Unlock(); return s.build }

func (s *Session) CaptureTool(ctx context.Context, name string) error {
	return s.admitTool(ctx, name, false)
}

// CaptureArtifact admits an executable from the owned sealed build generation.
// It retains all ordinary input/output guards and grants no runtime authority.
func (s *Session) CaptureArtifact(ctx context.Context, name string) error {
	return s.admitTool(ctx, name, true)
}

func (s *Session) admitTool(ctx context.Context, name string, artifact bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithDeadline(ctx, s.deadline)
	defer cancel()
	if err := s.available(ctx); err != nil {
		return err
	}
	if err := s.verify(ctx, true); err != nil {
		s.broken = true
		return err
	}
	err := s.captureTool(ctx, name, artifact)
	if err == nil {
		err = s.verify(ctx, true)
	}
	if err != nil {
		s.broken = true
	}
	return err
}

func (s *Session) captureTool(ctx context.Context, name string, artifact bool) error {
	if !text(name, 4096) || !filepath.IsAbs(name) || filepath.Clean(name) != name || len(s.tools) >= 64 {
		return errors.New("invalid bounded installed tool")
	}
	alias, err := os.Lstat(name)
	resolved, resolveErr := filepath.EvalSymlinks(name)
	source, sourceErr := filepath.EvalSymlinks(s.request.Source)
	if err != nil || resolveErr != nil || sourceErr != nil || within(source, resolved) ||
		(!artifact && within(s.private, resolved)) ||
		(artifact && (!within(s.build, name) || !within(s.build, resolved))) {
		return errors.New("tool is outside its installed or owned artifact scope")
	}
	file, err := os.Lstat(resolved)
	if err != nil || !file.Mode().IsRegular() || file.Mode().Perm()&0111 == 0 {
		return errors.New("installed tool must be a regular executable")
	}
	for _, old := range s.tools {
		if old.path == name {
			return nil
		}
	}
	digest, err := snapshot.FileDigest(ctx, resolved, 256<<20)
	if err != nil {
		return err
	}
	s.tools = append(s.tools, tool{name, resolved, digest, alias, file})
	return s.budget()
}

// BindInputs retains independently declared immutable dependency bytes. It does
// not infer hidden inputs or prevent the native process accessing other paths.
func (s *Session) BindInputs(ctx context.Context, request inputbundle.Request) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithDeadline(ctx, s.deadline)
	defer cancel()
	if err := s.available(ctx); err != nil {
		return err
	}
	if err := s.verify(ctx, true); err != nil {
		s.broken = true
		return err
	}
	bundle, err := inputbundle.Capture(ctx, request)
	if err == nil {
		// Guard selected paths and root routing, rather than unrelated SDK
		// directories. The bundle verifies exact declared membership/bytes.
		var guards []guard
		guards, err = observeInputs(ctx, request, bundle.Manifest())
		s.immutable = append(s.immutable, guards...)
	}
	if err == nil {
		s.inputs = append(s.inputs, bundle)
		err = s.verify(ctx, true)
	}
	if err != nil {
		s.broken = true
	}
	return err
}

func (s *Session) budget() error {
	bytes, entries := int64(0), 0
	for _, b := range append([]*inputbundle.Bundle{s.sourceBundle, s.buildBundle}, s.inputs...) {
		if b != nil {
			m := b.Manifest()
			bytes += m.Bytes
			entries += len(m.Entries)
		}
	}
	seen := map[string]bool{}
	for _, t := range s.tools {
		if !seen[t.resolved] {
			bytes += t.file.Size()
			seen[t.resolved] = true
		}
	}
	if bytes > inputbundle.MaxBytes || entries > inputbundle.MaxEntries {
		return errors.New("aggregate build closure exceeds bounds")
	}
	return nil
}

func (s *Session) available(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.closed || s.broken {
		return errors.New("build closure is unavailable")
	}
	if !time.Now().Before(s.deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

func (s *Session) Verify(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithDeadline(ctx, s.deadline)
	defer cancel()
	if err := s.available(ctx); err != nil {
		return err
	}
	err := s.verify(ctx, true)
	if err != nil {
		s.broken = true
	}
	return err
}

func (s *Session) verify(ctx context.Context, build bool) error {
	private, err := os.Lstat(s.private)
	if err != nil || !same(private, s.privateInfo) {
		return errors.New("private build root identity changed")
	}
	if err := s.budget(); err != nil {
		return err
	}
	for _, b := range append([]*inputbundle.Bundle{s.sourceBundle}, s.inputs...) {
		if err := b.VerifySources(ctx); err != nil {
			return err
		}
	}
	if err := s.sourceBundle.VerifyMaterialization(ctx, s.source); err != nil {
		return err
	}
	if build {
		if err := s.buildBundle.VerifySources(ctx); err != nil {
			return err
		}
	}
	// Hash all selected executables, then repeat the metadata/alias checks. A
	// later-file read must not conceal lasting drift in an earlier tool.
	for _, t := range s.tools {
		if err := t.verify(); err != nil {
			return err
		}
		digest, err := snapshot.FileDigest(ctx, t.resolved, 256<<20)
		if err != nil || digest != t.digest {
			return errors.Join(err, errors.New("installed build tool bytes changed"))
		}
	}
	for _, t := range s.tools {
		if err := t.verify(); err != nil {
			return err
		}
	}
	for _, g := range s.immutable {
		if err := g.verify(); err != nil {
			return err
		}
	}
	if build {
		for _, g := range s.outputs {
			if err := g.verify(); err != nil {
				return err
			}
		}
	}
	private, err = os.Lstat(s.private)
	if err != nil || !same(private, s.privateInfo) {
		return errors.New("private build root identity changed during verification")
	}
	return ctx.Err()
}

func observe(ctx context.Context, root string) ([]guard, error) {
	var result []guard
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(result) >= inputbundle.MaxEntries {
			return errors.New("build metadata entry bound exceeded")
		}
		g, err := observePath(name)
		if err == nil {
			result = append(result, g)
		}
		return err
	})
	return result, err
}

func observePath(name string) (guard, error) {
	info, err := os.Lstat(name)
	resolved, resolveErr := filepath.EvalSymlinks(name)
	if err != nil || resolveErr != nil {
		return guard{}, errors.Join(err, resolveErr)
	}
	return guard{name, resolved, info}, nil
}

func observeInputs(ctx context.Context, request inputbundle.Request, manifest inputbundle.Manifest) ([]guard, error) {
	var result []guard
	seen := map[string]bool{}
	add := func(name string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if seen[name] {
			return nil
		}
		if len(result) >= inputbundle.MaxEntries {
			return errors.New("build dependency metadata bound exceeded")
		}
		g, err := observePath(name)
		if err == nil {
			result = append(result, g)
			seen[name] = true
		}
		return err
	}
	for _, root := range request.Roots {
		if err := add(root.Path); err != nil {
			return nil, err
		}
		for _, entry := range manifest.Entries {
			mount := filepath.FromSlash(root.Mount)
			if mount == "" {
				mount = "."
			}
			name := filepath.FromSlash(entry.Path)
			if within(mount, name) {
				rel, _ := filepath.Rel(mount, name)
				if err := add(filepath.Join(root.Path, rel)); err != nil {
					return nil, err
				}
			}
		}
	}
	return result, nil
}

func (g guard) verify() error {
	info, err := os.Lstat(g.path)
	resolved, resolveErr := filepath.EvalSymlinks(g.path)
	if err != nil || resolveErr != nil || resolved != g.resolved || !same(g.info, info) {
		return errors.New("build input or output metadata identity changed")
	}
	return nil
}

func (t tool) verify() error {
	a, err := os.Lstat(t.path)
	r, resolveErr := filepath.EvalSymlinks(t.path)
	f, fileErr := os.Lstat(t.resolved)
	if err != nil || resolveErr != nil || fileErr != nil || r != t.resolved || !same(a, t.alias) || !same(f, t.file) {
		return errors.New("installed build tool identity changed")
	}
	return nil
}

func within(root, name string) bool {
	r, err := filepath.Rel(root, name)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
}

// ReadFile reads one item through the shared private metadata boundary.
func (s *Session) ReadFile(ctx context.Context, area, name string, limit int64) ([]byte, error) {
	data, err := s.ReadFiles(ctx, area, []string{name}, limit)
	if err != nil {
		return nil, err
	}
	return data[0], nil
}

// ReadFiles verifies a complete metadata batch with one pair of fresh barriers.
// Limits cover aggregate bytes; language bridges do not need their own readers.
func (s *Session) ReadFiles(ctx context.Context, area string, names []string, limit int64) (data [][]byte, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithDeadline(ctx, s.deadline)
	defer cancel()
	if err = s.available(ctx); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 16<<20 || len(names) == 0 || len(names) > inputbundle.MaxEntries {
		return nil, errors.New("invalid bounded private metadata batch")
	}
	names = append([]string(nil), names...)
	seen := map[string]bool{}
	for _, name := range names {
		if !text(name, 4096) || filepath.IsAbs(name) || filepath.ToSlash(filepath.Clean(name)) != name || name == "." || strings.HasPrefix(name, "../") || seen[name] {
			return nil, errors.New("invalid private metadata name")
		}
		seen[name] = true
	}
	root := s.build
	if area == "source" {
		root = s.source
	} else if area != "build" {
		return nil, errors.New("unknown private metadata area")
	}
	if err = s.verify(ctx, true); err != nil {
		s.broken = true
		return nil, err
	}
	defer func() {
		check, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		err = errors.Join(err, s.verify(check, true), ctx.Err())
		if err != nil {
			s.broken = true
			data = nil
		}
	}()
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, r.Close()) }()
	for _, name := range names {
		item, readErr := readMetadata(ctx, r, filepath.FromSlash(name), limit)
		if readErr != nil {
			return nil, readErr
		}
		limit -= int64(len(item))
		data = append(data, item)
	}
	return data, nil
}

func readMetadata(ctx context.Context, r *os.Root, name string, limit int64) (data []byte, err error) {
	before, err := r.Lstat(name)
	if err != nil {
		return nil, err
	}
	return readAdmittedMetadata(ctx, r, name, before, limit)
}

func readAdmittedMetadata(ctx context.Context, r *os.Root, name string, before os.FileInfo, limit int64) (data []byte, err error) {
	var buffer bytes.Buffer
	if _, err := readMetadataTo(ctx, r, name, before, limit, &buffer); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func readMetadataTo(ctx context.Context, r *os.Root, name string, before os.FileInfo, limit int64, destination io.Writer) (count int64, err error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if before == nil || !before.Mode().IsRegular() || before.Size() < 0 || before.Size() > limit {
		return 0, errors.New("private metadata must be an admitted regular file")
	}
	f, err := r.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return 0, err
	}
	defer func() {
		err = errors.Join(err, f.Close(), ctx.Err())
	}()
	opened, err := f.Stat()
	if err != nil || !same(before, opened) {
		return 0, errors.Join(err, errors.New("private metadata changed before open"))
	}
	count, err = io.Copy(destination, io.LimitReader(contextReader{ctx, f}, before.Size()+1))
	after, afterErr := f.Stat()
	named, namedErr := r.Lstat(name)
	if err != nil || afterErr != nil || namedErr != nil || count != before.Size() || !same(before, after) || !same(before, named) {
		return count, errors.Join(err, afterErr, namedErr, errors.New("private metadata changed while reading"))
	}
	return count, nil
}

// Manifest exposes copied logical entry metadata for selecting a bounded batch.
// It supplies no bytes, execution proof or physical input paths.
func (s *Session) Manifest(ctx context.Context, area string) (inputbundle.Manifest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithDeadline(ctx, s.deadline)
	defer cancel()
	if err := s.available(ctx); err != nil {
		return inputbundle.Manifest{}, err
	}
	if err := s.verify(ctx, true); err != nil {
		s.broken = true
		return inputbundle.Manifest{}, err
	}
	if area == "source" {
		return s.sourceBundle.Manifest(), nil
	}
	if area == "build" {
		return s.buildBundle.Manifest(), nil
	}
	return inputbundle.Manifest{}, errors.New("unknown private metadata area")
}

type contextReader struct {
	ctx context.Context
	io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func (s *Session) Run(ctx context.Context, command model.Command) (process.Output, error) {
	return s.invoke(ctx, "", command, false, 32<<20, 1<<20)
}

func (s *Session) RunProducer(ctx context.Context, id string, command model.Command) (process.Output, error) {
	if !text(id, 64) {
		return process.Output{}, errors.New("invalid build stage identity")
	}
	return s.invoke(ctx, id, command, false, 32<<20, 1<<20)
}

// ObserveNative records a native invocation and its report products even when
// its completed exit is nonzero. Unlike successful build stages, assertion
// failures are valid observations. Interruption, overflow or any integrity
// failure invalidates the session and cannot return completed native evidence.
func (s *Session) ObserveNative(ctx context.Context, id string, command model.Command, stdoutLimit, stderrLimit int) (process.Output, error) {
	if id == "" || !text(id, 64) || stdoutLimit <= 0 || stderrLimit <= 0 || stdoutLimit > 32<<20 || stderrLimit > 32<<20 {
		return process.Output{}, errors.New("invalid bounded native observation")
	}
	return s.invoke(ctx, id, command, true, stdoutLimit, stderrLimit)
}

func (s *Session) invoke(ctx context.Context, id string, command model.Command, observeNative bool, stdoutLimit, stderrLimit int) (out process.Output, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithDeadline(ctx, s.deadline)
	defer cancel()
	if err = s.available(ctx); err != nil {
		return out, err
	}
	if len(command.Args) > 1024 || !text(command.Executable, 4096) || !text(command.Dir, 4096) {
		return out, errors.New("invalid bounded build invocation")
	}
	found, bytes := false, 0
	for _, t := range s.tools {
		found = found || t.path == command.Executable
	}
	if !found {
		return out, errors.New("build invocation requires an admitted executable")
	}
	command.Args = append([]string(nil), command.Args...)
	for _, v := range append([]string{command.Dir}, command.Args...) {
		bytes += len(v)
		if len(v) > 32768 || bytes > 1<<20 || !utf8.ValidString(v) || strings.ContainsRune(v, 0) {
			return out, errors.New("invalid bounded build arguments")
		}
	}
	for _, old := range s.records {
		if id != "" && old.ID == id {
			return out, errors.New("build stage identity already used")
		}
	}
	if id != "" && len(s.records) >= 32 {
		return out, errors.New("build stage count exceeds bounds")
	}
	if id == "" && len(s.queries) >= 128 {
		return out, errors.New("build collection invocation count exceeds bounds")
	}
	expand := func(v string) string {
		v = strings.ReplaceAll(v, "{source}", s.source)
		return strings.ReplaceAll(v, "{build}", s.build)
	}
	original := command
	original.Args = append([]string(nil), command.Args...)
	command.Dir = expand(command.Dir)
	for i := range command.Args {
		command.Args[i] = expand(command.Args[i])
	}
	bytes = 0
	for _, value := range append([]string{command.Dir}, command.Args...) {
		bytes += len(value)
		if len(value) > 32768 || bytes > 1<<20 {
			return out, errors.New("expanded build arguments exceed bounds")
		}
	}
	dir, resolveErr := filepath.EvalSymlinks(command.Dir)
	if resolveErr != nil || dir != command.Dir || (!within(s.source, dir) && !within(s.build, dir)) {
		return out, errors.New("build working directory must stay in its private view")
	}
	if err = s.verify(ctx, true); err != nil {
		s.broken = true
		return out, err
	}
	inputDigest := s.bindingDigest()
	sealed := false
	var nativeFailure error
	defer func() {
		check, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		err = errors.Join(err, s.verify(check, id == "" || sealed), ctx.Err())
		if err != nil {
			s.broken = true
			out.Completed = false
		}
		// Join this after deciding whether guards failed. A completed nonzero
		// exit alone leaves observed reports readable; a joined guard never does.
		if err == nil {
			err = nativeFailure
		} else if nativeFailure != nil {
			err = errors.Join(err, nativeFailure)
		}
	}()
	out, err = process.RunEnvironmentLimits(ctx, command, dir, s.request.Environment, stdoutLimit, stderrLimit)
	if observeNative && out.Completed && out.ExitCode > 0 && ctx.Err() == nil {
		nativeFailure, err = ErrNativeFailure, nil
	}
	if err != nil || !out.Completed || (!observeNative && out.ExitCode != 0) {
		if err == nil {
			err = errors.New("build stage did not complete successfully")
		}
		return out, err
	}
	invocationDigest := identity(original)
	if observeNative {
		stdout, stderr := sha256.Sum256(out.Stdout), sha256.Sum256(out.Stderr)
		invocationDigest = identity(struct {
			Mode                       string
			Command                    model.Command
			StdoutLimit, StderrLimit   int
			Exit                       int
			StdoutDigest, StderrDigest string
		}{"observed-native-v1", original, stdoutLimit, stderrLimit, out.ExitCode, hex.EncodeToString(stdout[:]), hex.EncodeToString(stderr[:])})
	}
	if id != "" {
		var next *inputbundle.Bundle
		next, err = capture(ctx, s.build)
		if err != nil {
			return out, err
		}
		s.buildBundle = next
		s.outputs, err = observe(ctx, s.build)
		if err != nil {
			return out, err
		}
		sealed = true
		s.records = append(s.records, Record{id, inputDigest, invocationDigest, next.Manifest().Digest})
	} else {
		s.queries = append(s.queries, identity([]any{original, inputDigest, identity(out.Stdout), identity(out.Stderr)}))
	}
	return out, nil
}

func (s *Session) Records(ctx context.Context) ([]Record, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithDeadline(ctx, s.deadline)
	defer cancel()
	if err := s.available(ctx); err != nil {
		return nil, "", err
	}
	if err := s.verify(ctx, true); err != nil {
		s.broken = true
		return nil, "", err
	}
	return append([]Record(nil), s.records...), s.bindingDigest(), nil
}

func (s *Session) bindingDigest() string {
	digests := []string{s.sourceBundle.Manifest().Digest, s.buildBundle.Manifest().Digest}
	for _, b := range s.inputs {
		digests = append(digests, b.Manifest().Digest)
	}
	for _, t := range s.tools {
		digests = append(digests, t.digest)
	}
	return identity([]any{Version, s.request.Revision, s.request.Environment, s.request.Files, digests, s.records, s.queries})
}

func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.private == "" {
		return nil
	}
	s.closed = true
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := s.verify(ctx, !s.broken)
	return errors.Join(err, s.cleanup())
}

// Cleanup retains ownership across directory renames and never recursively
// removes a replacement tree found at the old temporary pathname.
func (s *Session) cleanup() error {
	var err error
	if s.owner != nil {
		err = errors.Join(s.owner.RemoveAll("source"), s.owner.RemoveAll("build"))
	}
	info, statErr := os.Lstat(s.private)
	if statErr != nil || s.privateInfo == nil || !os.SameFile(info, s.privateInfo) || info.Mode() != s.privateInfo.Mode() {
		err = errors.Join(err, errors.New("private build cleanup pathname identity changed"))
	} else {
		// An unexpected sibling or replacement cannot be recursively deleted.
		err = errors.Join(err, os.Remove(s.private))
	}
	if err == nil {
		if s.owner != nil {
			err = s.owner.Close()
			s.owner = nil
		}
		s.private = ""
	}
	return err
}
