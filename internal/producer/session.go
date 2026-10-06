// Package producer observes explicitly configured generation stages in private
// revision-specific trees. Its records are provenance, never runtime authority.
package producer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/inputbundle"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/snapshot"
)

const MaxStages = 32
const MaxRunDuration = 10 * time.Minute

// Stage declares a new repository-relative output tree. Dependencies must name
// earlier stages; only those outputs can be referenced with {producer:ID}.
// Arguments may reference {source} and {output}. No shell is introduced.
type Stage = model.ProducerStage

type Request struct {
	Source      string
	Revision    string
	Stages      []Stage
	Environment []string
	Limits      inputbundle.Limits
}

// Record contains identities and repository paths, without tool paths, raw
// environment values, command output or source bytes.
type Record = model.ProducerRecord

type observed struct {
	name     string
	resolved string
	info     os.FileInfo
}

type tool struct {
	alias    observed
	physical observed
	digest   string
}

// Session owns its copied source and every producer output. Methods serialize
// access; a failed run or verification permanently invalidates the session.
type Session struct {
	mu          sync.Mutex
	request     Request
	private     string
	source      *inputbundle.Bundle
	sourceCopy  string
	guards      []observed
	tools       []tool
	outputs     []*inputbundle.Bundle
	outputRoots []string
	records     []Record
	bytes       int64
	limit       int64
	entries     int
	ready       bool
	broken      bool
	closed      bool
}

func New(ctx context.Context, request Request) (_ *Session, err error) {
	if !validText(request.Source) || !validText(request.Revision) || !filepath.IsAbs(request.Source) || filepath.Clean(request.Source) != request.Source || len(request.Source) > 4096 || len(request.Revision) == 0 || len(request.Revision) > 256 || strings.ContainsRune(request.Revision, '\n') || len(request.Stages) == 0 || len(request.Stages) > MaxStages || len(request.Environment) > 4096 {
		return nil, errors.New("invalid producer source, revision or stage count")
	}
	request.Stages = append([]Stage(nil), request.Stages...)
	request.Environment = append(make([]string, 0, len(request.Environment)), request.Environment...)
	if err := config.ValidateProducerStages(request.Stages); err != nil {
		return nil, err
	}
	for i := range request.Stages {
		stage := &request.Stages[i]
		stage.Command.Args = append([]string(nil), stage.Command.Args...)
		stage.DependsOn = append([]string(nil), stage.DependsOn...)
		if _, err := os.Lstat(filepath.Join(request.Source, filepath.FromSlash(stage.Output))); !os.IsNotExist(err) {
			return nil, errors.New("producer output must be absent from original source")
		}
	}
	keys := map[string]bool{}
	environmentBytes := 0
	for _, value := range request.Environment {
		environmentBytes += len(value)
		key, _, ok := strings.Cut(value, "=")
		if !ok || !validID(key) || keys[key] || !validText(value) || len(value) > 32768 || environmentBytes > 1<<20 {
			return nil, errors.New("invalid explicit producer environment")
		}
		keys[key] = true
	}
	sort.Strings(request.Environment)
	limit := request.Limits.Bytes
	if limit == 0 {
		limit = inputbundle.MaxBytes
	}
	if limit < 1 || limit > inputbundle.MaxBytes {
		return nil, errors.New("invalid producer aggregate limit")
	}
	s := &Session{request: request, limit: limit}
	defer func() {
		if err != nil && s.private != "" {
			err = errors.Join(err, os.RemoveAll(s.private))
		}
	}()
	s.source, err = capture(ctx, request.Source, request.Limits, false)
	if err != nil {
		return nil, err
	}
	s.bytes = s.source.Manifest().Bytes
	s.entries = len(s.source.Manifest().Entries)
	s.guards, err = observeBundle(request.Source, s.source)
	if err != nil {
		return nil, err
	}
	sourceRoot, err := filepath.EvalSymlinks(request.Source)
	if err != nil {
		return nil, errors.New("cannot resolve producer source")
	}
	for _, stage := range request.Stages {
		alias, e := observe(stage.Command.Executable)
		if e != nil {
			return nil, e
		}
		physical, e := observe(alias.resolved)
		if e != nil || !physical.info.Mode().IsRegular() || physical.info.Mode().Perm()&0111 == 0 {
			return nil, errors.New("producer executable must be a regular executable file")
		}
		if within(sourceRoot, physical.name) || within(request.Source, alias.name) {
			return nil, errors.New("producer executable must be external; pass revision generator source through {source}")
		}
		var digest string
		for _, prior := range s.tools {
			if os.SameFile(prior.physical.info, physical.info) && prior.physical.info.Mode() == physical.info.Mode() && prior.physical.info.Size() == physical.info.Size() && prior.physical.info.ModTime().Equal(physical.info.ModTime()) {
				digest = prior.digest
				break
			}
		}
		if digest == "" && physical.info.Size() > limit-s.bytes {
			return nil, errors.New("producer aggregate limit exceeded")
		}
		if digest == "" {
			digest, e = snapshot.FileDigest(ctx, physical.name, limit-s.bytes)
			if e != nil {
				return nil, e
			}
			s.bytes += physical.info.Size()
		}
		s.tools = append(s.tools, tool{alias: alias, physical: physical, digest: digest})
	}
	s.private, err = os.MkdirTemp("", "hayaku-producer-")
	if err != nil {
		return nil, errors.New("cannot reserve private producer tree")
	}
	resolvedPrivate, err := filepath.EvalSymlinks(s.private)
	if err != nil {
		return nil, errors.New("cannot resolve private producer tree")
	}
	s.private = resolvedPrivate
	s.sourceCopy = filepath.Join(s.private, "source")
	if err = s.source.Materialize(ctx, s.sourceCopy); err != nil {
		return nil, err
	}
	copyGuards, err := observeBundle(s.sourceCopy, s.source)
	if err != nil {
		return nil, err
	}
	s.guards = append(s.guards, copyGuards...)
	if err = s.verify(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Session) Run(ctx context.Context) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, MaxRunDuration)
	defer cancel()
	if s.closed || s.broken || s.ready {
		return errors.New("producer session cannot run")
	}
	defer func() {
		if err != nil {
			s.broken = true
			s.records = nil
		}
	}()
	for i, stage := range s.request.Stages {
		if err = s.verify(ctx); err != nil {
			return err
		}
		output := filepath.Join(s.private, "output-"+stage.ID)
		if err = os.Mkdir(output, 0700); err != nil {
			return errors.New("cannot reserve producer output")
		}
		outputGuard, e := observe(output)
		if e != nil {
			return e
		}
		command := stage.Command
		command.Args = make([]string, len(stage.Command.Args))
		argumentBytes := 0
		for n, argument := range stage.Command.Args {
			command.Args[n], err = s.expand(stage, argument, output)
			if err != nil {
				return err
			}
			argumentBytes += len(command.Args[n])
			if argumentBytes > 1<<20 {
				return errors.New("expanded producer arguments exceed size limit")
			}
		}
		cwd := filepath.Join(s.sourceCopy, filepath.FromSlash(command.Dir))
		resolved, e := filepath.EvalSymlinks(cwd)
		if e != nil || !within(s.sourceCopy, resolved) {
			return errors.New("producer working directory escapes copied source")
		}
		out, runErr := process.RunEnvironmentLimits(ctx, command, cwd, s.request.Environment, 1<<20, 1<<20)
		// Verify even when the producer fails; failed or partial output is never
		// published. The private output root may change membership, not identity.
		verifyErr := s.verify(ctx)
		current, rootErr := observe(output)
		if rootErr != nil || !os.SameFile(outputGuard.info, current.info) || current.info.Mode() != outputGuard.info.Mode() || current.resolved != outputGuard.resolved {
			return errors.New("producer replaced its owned output root")
		}
		if runErr != nil || !out.Completed || out.ExitCode != 0 {
			return errors.Join(errors.New("producer command did not complete successfully"), ctx.Err(), verifyErr)
		}
		if verifyErr != nil {
			return verifyErr
		}
		limits := s.request.Limits
		limits.Bytes = s.limit - s.bytes
		entryLimit := limits.Entries
		if entryLimit == 0 {
			entryLimit = inputbundle.MaxEntries
		}
		limits.Entries = entryLimit - s.entries
		if limits.Bytes < 1 || limits.Entries < 1 {
			return errors.New("producer aggregate limit exceeded")
		}
		bundle, e := capture(ctx, output, limits, true)
		if e != nil {
			return e
		}
		guards, e := observeBundle(output, bundle)
		if e != nil {
			return e
		}
		s.outputs = append(s.outputs, bundle)
		s.outputRoots = append(s.outputRoots, output)
		s.guards = append(s.guards, guards...)
		s.bytes += bundle.Manifest().Bytes
		s.entries += len(bundle.Manifest().Entries)
		dependencies := make([]Record, 0, len(stage.DependsOn))
		for _, id := range stage.DependsOn {
			for _, r := range s.records {
				if r.ID == id {
					dependencies = append(dependencies, r)
				}
			}
		}
		invocation := config.Digest(struct {
			Stage        Stage
			Tool         string
			Environment  []string
			Dependencies []Record
		}{stage, s.tools[i].digest, s.request.Environment, dependencies})
		s.records = append(s.records, Record{ID: stage.ID, Output: stage.Output, Revision: s.request.Revision, InputsDigest: s.source.Manifest().Digest, InvocationDigest: invocation, OutputDigest: config.Digest(outputEntries(bundle, ".")), DependsOn: append([]string(nil), stage.DependsOn...)})
		if err = s.verify(ctx); err != nil {
			return err
		}
	}
	s.ready = true
	return nil
}

// Materialize produces a fresh consumer tree from the independently captured
// revision and sealed outputs. It never overlays an existing source tree.
func (s *Session) Materialize(ctx context.Context, destination string) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ready || s.closed || s.broken {
		return errors.New("producer output is unavailable")
	}
	if err = s.verify(ctx); err != nil {
		s.broken = true
		return err
	}
	if !filepath.IsAbs(destination) || filepath.Clean(destination) != destination {
		return errors.New("producer destination must be a clean absolute path")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(destination))
	if err != nil {
		return errors.New("producer destination parent must exist")
	}
	destination = filepath.Join(parent, filepath.Base(destination))
	original, sourceErr := filepath.EvalSymlinks(s.request.Source)
	if sourceErr != nil || within(original, destination) {
		return errors.New("producer destination must remain outside original source")
	}
	reserved := false
	defer func() {
		if err != nil {
			if reserved {
				err = errors.Join(err, os.RemoveAll(destination))
			}
			s.broken = true
		}
	}()
	request := inputbundle.Request{Roots: []inputbundle.Root{{Name: "source", Path: s.sourceCopy, Mount: "."}}, Inputs: []inputbundle.Input{{Root: "source", Path: "."}}, Limits: s.request.Limits}
	for i, stage := range s.request.Stages {
		parent := filepath.Dir(filepath.Join(s.sourceCopy, filepath.FromSlash(stage.Output)))
		resolved, e := filepath.EvalSymlinks(parent)
		if e != nil || resolved != parent || !within(s.sourceCopy, resolved) {
			return errors.New("producer output parent must be a real source directory")
		}
		role := "producer-" + stage.ID
		request.Roots = append(request.Roots, inputbundle.Root{Name: role, Path: s.outputRoots[i], Mount: stage.Output})
		request.Inputs = append(request.Inputs, inputbundle.Input{Root: role, Path: ".", Generated: true})
	}
	composed, err := inputbundle.Capture(ctx, request)
	if err != nil {
		return err
	}
	if err = s.verify(ctx); err != nil {
		return err
	}
	if err = composed.Materialize(ctx, destination); err != nil {
		return err
	}
	reserved = true
	if err = s.verify(ctx); err != nil {
		return err
	}
	return composed.VerifyMaterialization(ctx, destination)
}

func (s *Session) Records(ctx context.Context) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ready || s.closed || s.broken {
		return nil, errors.New("producer output is unavailable")
	}
	if err := s.verify(ctx); err != nil {
		s.broken = true
		return nil, err
	}
	result := append([]Record(nil), s.records...)
	for i := range result {
		result[i].DependsOn = append([]string(nil), result[i].DependsOn...)
	}
	return result, nil
}

// Entries returns a copied, repository-relative generated inventory. Only
// captured output-local link roles are normalized, never arbitrary path text.
func (s *Session) Entries(ctx context.Context) ([]inputbundle.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ready || s.closed || s.broken {
		return nil, errors.New("producer output is unavailable")
	}
	if err := s.verify(ctx); err != nil {
		s.broken = true
		return nil, err
	}
	var entries []inputbundle.Entry
	for i, output := range s.outputs {
		entries = append(entries, outputEntries(output, s.request.Stages[i].Output)...)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func outputEntries(bundle *inputbundle.Bundle, mount string) []inputbundle.Entry {
	entries := bundle.Manifest().Entries
	for i := range entries {
		entries[i].Path = path.Join(mount, entries[i].Path)
		if entries[i].Kind == "symlink" {
			entries[i].Link = entries[i].MaterializedLink
			digest := sha256.Sum256([]byte(entries[i].Link))
			entries[i].LinkSHA256 = hex.EncodeToString(digest[:])
		}
	}
	return entries
}

func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed && s.private == "" {
		return nil
	}
	s.closed = true
	s.records = nil
	err := os.RemoveAll(s.private)
	if err == nil {
		s.private = ""
	}
	return err
}

func (s *Session) verify(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.closed || s.broken {
		return errors.New("producer session is invalid")
	}
	for _, guard := range s.guards {
		if err := guard.verify(); err != nil {
			return err
		}
	}
	if err := s.source.VerifySources(ctx); err != nil {
		return err
	}
	if err := s.source.VerifyMaterialization(ctx, s.sourceCopy); err != nil {
		return err
	}
	for _, t := range s.tools {
		if err := t.alias.verify(); err != nil {
			return err
		}
		if err := t.physical.verify(); err != nil {
			return err
		}
		digest, err := snapshot.FileDigest(ctx, t.physical.name, s.limit)
		if err != nil || digest != t.digest {
			return errors.New("producer executable changed")
		}
	}
	for _, b := range s.outputs {
		if err := b.VerifySources(ctx); err != nil {
			return err
		}
	}
	for _, guard := range s.guards {
		if err := guard.verify(); err != nil {
			return err
		}
	}
	for _, t := range s.tools {
		if err := t.alias.verify(); err != nil {
			return err
		}
		if err := t.physical.verify(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) expand(stage Stage, value, output string) (string, error) {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "{source}", s.sourceCopy), "{output}", output)
	for _, id := range stage.DependsOn {
		for i, previous := range s.request.Stages {
			if previous.ID == id {
				value = strings.ReplaceAll(value, "{producer:"+id+"}", s.outputRoots[i])
			}
		}
	}
	if strings.Contains(value, "{producer:") || len(value) > 32768 || strings.ContainsRune(value, 0) {
		return "", errors.New("invalid producer argument reference")
	}
	return value, nil
}

func capture(ctx context.Context, root string, limits inputbundle.Limits, generated bool) (*inputbundle.Bundle, error) {
	return inputbundle.Capture(ctx, inputbundle.Request{Roots: []inputbundle.Root{{Name: "tree", Path: root, Mount: "."}}, Inputs: []inputbundle.Input{{Root: "tree", Path: ".", Generated: generated}}, Limits: limits})
}

func observeBundle(root string, bundle *inputbundle.Bundle) ([]observed, error) {
	result := make([]observed, 0, len(bundle.Manifest().Entries))
	for _, entry := range bundle.Manifest().Entries {
		guard, err := observe(filepath.Join(root, filepath.FromSlash(entry.Path)))
		if err != nil {
			return nil, err
		}
		result = append(result, guard)
	}
	return result, nil
}

func observe(name string) (observed, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return observed{}, errors.New("cannot inspect producer input")
	}
	resolved, err := filepath.EvalSymlinks(name)
	if err != nil {
		return observed{}, errors.New("cannot resolve producer input")
	}
	return observed{name: name, resolved: resolved, info: info}, nil
}

func (o observed) verify() error {
	current, err := observe(o.name)
	if err != nil || current.resolved != o.resolved || !os.SameFile(o.info, current.info) || current.info.Mode() != o.info.Mode() || current.info.Size() != o.info.Size() || !current.info.ModTime().Equal(o.info.ModTime()) {
		return errors.New("producer input identity changed")
	}
	return nil
}

func validID(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func validText(value string) bool { return utf8.ValidString(value) && !strings.ContainsRune(value, 0) }

func within(root, name string) bool {
	rel, err := filepath.Rel(root, name)
	return err == nil && config.Relative(filepath.ToSlash(rel))
}
