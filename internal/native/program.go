package native

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Cyberlane/hayaku/internal/inputbundle"
	"github.com/Cyberlane/hayaku/internal/process"
)

// ProgramTimeout bounds operational execution. Program collection and execution
// do not qualify runtime effects, original semantics or receipt authority.
const ProgramTimeout = 30 * time.Second
const programPolicy = "native-static-primary-v1"

type ProgramRequest struct {
	Program string
	Args    []string
}

// Program is an immutable captured static Linux executable and initial argv.
// RunProgram takes its exact environment and resources solely from an Envelope.
// Dynamic loaders, JITs and provider qualification require separate contracts.
type Program struct {
	executable *capturedExecutable
	args       []string
	digest     string
}

type capturedExecutable struct {
	files                 *inputbundle.Bundle
	data                  []byte
	architecture, binding string
	source, resolved      string
	info, rootInfo        os.FileInfo
}

func captureStaticExecutable(ctx context.Context, name string) (*capturedExecutable, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(name) == 0 || len(name) > 4096 || !filepath.IsAbs(name) || !utf8.ValidString(name) || strings.ContainsAny(name, "\x00\r\n") {
		return nil, errors.New("invalid native static executable declaration")
	}
	source := filepath.Clean(name)
	before, err := os.Lstat(source)
	resolved, resolveErr := filepath.EvalSymlinks(source)
	rootInfo, rootErr := os.Lstat(filepath.Dir(source))
	if err != nil || resolveErr != nil || rootErr != nil || !before.Mode().IsRegular() {
		return nil, errors.New("native static executable source unavailable")
	}
	bundle, err := inputbundle.Capture(ctx, inputbundle.Request{
		Roots:  []inputbundle.Root{{Name: "helper", Path: filepath.Dir(source), Mount: "."}},
		Inputs: []inputbundle.Input{{Root: "helper", Path: filepath.Base(source)}},
	})
	if err != nil {
		return nil, err
	}
	data, err := readBounded(source, inputbundle.MaxFileBytes)
	if err != nil {
		return nil, err
	}
	architecture, err := staticChildArchitecture(data)
	if err != nil {
		return nil, err
	}
	match := false
	for _, entry := range bundle.Manifest().Entries {
		if entry.Path == filepath.Base(source) && entry.Kind == "file" && entry.SHA256 == digest(data) {
			match = true
		}
	}
	if !match {
		return nil, errors.New("native static executable changed during capture")
	}
	c := &capturedExecutable{files: bundle, data: data, architecture: architecture, source: source, resolved: resolved, info: before, rootInfo: rootInfo}
	c.binding = c.currentBinding()
	if err := c.verifySources(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *capturedExecutable) currentBinding() string {
	if c == nil || c.files == nil || c.info == nil || c.rootInfo == nil {
		return ""
	}
	return identity([]any{c.files.Manifest().Digest, digest(c.data), c.architecture, c.source, c.resolved, c.info.Mode(), c.info.Size(), c.info.ModTime(), c.rootInfo.Mode(), c.rootInfo.Size(), c.rootInfo.ModTime()})
}
func (c *capturedExecutable) verifySources(ctx context.Context) error {
	if c == nil || c.binding == "" || c.currentBinding() != c.binding {
		return errors.New("native static executable identity changed")
	}
	arch, err := staticChildArchitecture(c.data)
	if err != nil || arch != c.architecture {
		return errors.New("native static executable identity changed")
	}
	if !c.sourceUnchanged() {
		return errors.New("native static executable source changed")
	}
	if err := c.files.VerifySources(ctx); err != nil {
		return err
	}
	if !c.sourceUnchanged() {
		return errors.New("native static executable source changed")
	}
	return ctx.Err()
}
func (c *capturedExecutable) sourceUnchanged() bool {
	after, err := os.Lstat(c.source)
	resolved, resolveErr := filepath.EvalSymlinks(c.source)
	root, rootErr := os.Lstat(filepath.Dir(c.source))
	return err == nil && resolveErr == nil && rootErr == nil && resolved == c.resolved && sameProgramInfo(c.info, after) && sameProgramInfo(c.rootInfo, root)
}
func sameProgramInfo(a, b os.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) && a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func (c *capturedExecutable) materialize(ctx context.Context, name, architecture string) error {
	if err := c.verifySources(ctx); err != nil {
		return err
	}
	if c.architecture != architecture {
		return errors.New("native static executable platform mismatch")
	}
	if err := os.WriteFile(name, c.data, 0555); err != nil {
		return err
	}
	return c.verifyCopy(name)
}
func (c *capturedExecutable) verifyCopy(name string) error {
	info, err := os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0555 || info.Mode()&os.ModeType != 0 {
		return errors.New("native static executable copy changed")
	}
	data, err := readBounded(name, inputbundle.MaxFileBytes)
	if err != nil || !bytes.Equal(data, c.data) {
		return errors.New("native static executable copy changed")
	}
	return nil
}

func CaptureProgram(ctx context.Context, request ProgramRequest) (*Program, error) {
	if err := validateNativeArguments(request.Args, nil); err != nil {
		return nil, err
	}
	executable, err := captureStaticExecutable(ctx, request.Program)
	if err != nil {
		return nil, err
	}
	p := &Program{executable: executable, args: append([]string(nil), request.Args...)}
	p.digest = p.currentDigest()
	return p, nil
}
func (p *Program) currentDigest() string {
	if p == nil || p.executable == nil {
		return ""
	}
	return identity([]any{programPolicy, p.executable.currentBinding(), p.args, int64(ProgramTimeout), process.MaxOutput})
}
func (p *Program) verifySources(ctx context.Context) error {
	if p == nil || p.digest == "" || p.currentDigest() != p.digest || validateNativeArguments(p.args, nil) != nil {
		return errors.New("native program identity changed or unavailable")
	}
	return p.executable.verifySources(ctx)
}

// RunProgram returns bounded raw output from the operational boundary. It has
// no cache, decoder, proof or issuer arguments and supplies no receipt authority.
// Even exit zero cannot attest ignored denials or original runtime equivalence.
func RunProgram(ctx context.Context, b Backend, p *Program, e *Envelope) (result process.Output, runErr error) {
	if b.platform() != "linux" || b.Sandbox != "" {
		return result, errors.New("native static program requires a Linux backend")
	}
	if e == nil {
		return result, errors.New("native program requires an explicit input envelope")
	}
	if err := p.verifySources(ctx); err != nil {
		return result, err
	}
	if err := e.VerifySources(ctx); err != nil {
		return result, err
	}
	if validateNativeArguments(p.args, e.env) != nil || p.executable.architecture != b.Architecture || b.Identity == "" {
		return result, errors.New("native program launch context unavailable or mismatched")
	}
	inputBytes := e.files.Manifest().Bytes + p.executable.files.Manifest().Bytes
	if e.child != nil {
		inputBytes += e.child.files.Manifest().Bytes
	}
	if inputBytes > inputbundle.MaxBytes {
		return result, errors.New("native program inputs exceed aggregate byte bounds")
	}
	launchIdentity := identity([]string{programPolicy, p.digest, e.Digest(), b.Identity})
	run, cancel := context.WithTimeout(ctx, ProgramTimeout)
	defer cancel()
	if err := b.unchanged(run); err != nil {
		return result, err
	}
	defer func() {
		validation, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := p.verifySources(validation); err != nil {
			result.Completed, runErr = false, err
		} else if err := e.VerifySources(validation); err != nil {
			result.Completed, runErr = false, err
		} else if identity([]string{programPolicy, p.digest, e.Digest(), b.Identity}) != launchIdentity {
			result.Completed, runErr = false, errors.New("native program launch identity changed")
		} else if err := b.unchanged(validation); err != nil {
			result.Completed, runErr = false, err
		}
	}()
	return b.executeCaptured(run, Artifact{Args: p.args, Envelope: e}, p.executable)
}
