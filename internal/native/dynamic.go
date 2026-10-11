package native

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/Cyberlane/hayaku/internal/inputbundle"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/snapshot"
)

// DynamicFileLimit is separate from the unchanged 64 MiB static/resource limit.
// Runtime capture supplies operational inputs, never runtime-equivalence proof.
const DynamicFileLimit int64 = 256 << 20
const dynamicPolicy = "native-dynamic-manual-loader-v3"

// This private profile trusts the captured compiler's generated code. It does
// not prove code provenance or grant test qualification or receipt authority.
const managedCompilerProfile = "trusted-managed-compiler-v1"
const dynamicLibraryLimit = 128

type RuntimeLibrary struct{ Name, Path string }
type RuntimeRequest struct {
	Loader    string
	Libraries []RuntimeLibrary
	Addons    []RuntimeAddon
}
type DynamicProgramRequest struct {
	Program string
	Args    []string
	Runtime *Runtime
}

// Runtime owns bounded private copies of one loader, closed named DSOs and
// separately declared native addons. Keep it open while its programs are in use.
// Library names are exact SONAME identities; default searches, loader caches,
// origin expansion and preload are not closure.
type Runtime struct {
	dir       string
	root      os.FileInfo
	loader    *dynamicFile
	libraries map[string]*dynamicFile
	addons    map[string]*dynamicFile
	bytes     int64
	seal      string
	closed    atomic.Bool
	cleanup   *dynamicDirectory
}

// DynamicProgram binds an initial argv and a borrowed Runtime. A future manual
// loader launch changes PT_INTERP/auxv/startup context; this is not the original
// command or a JIT provenance contract. It cannot mint cache/receipt authority.
type DynamicProgram struct {
	dir     string
	root    os.FileInfo
	primary *dynamicFile
	runtime *Runtime
	args    []string
	profile string
	seal    string
	closed  atomic.Bool
	cleanup *dynamicDirectory
}

// Keep cleanup tied to the created directory, even if its pathname is retargeted.
// Only declared private children are removed; unknown siblings are preserved.
type dynamicDirectory struct {
	mu       sync.Mutex
	name     string
	info     os.FileInfo
	owner    *os.Root
	children []string
}

func retainDynamicDirectory(name string, info os.FileInfo, children []string) (*dynamicDirectory, error) {
	owner, err := os.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	current, err := owner.Stat(".")
	if err != nil || info == nil || !info.IsDir() || !os.SameFile(info, current) || info.Mode() != current.Mode() {
		return nil, errors.Join(err, errors.New("dynamic cleanup directory ownership changed"), owner.Close())
	}
	return &dynamicDirectory{name: name, info: info, owner: owner, children: append([]string(nil), children...)}, nil
}

func (d *dynamicDirectory) close() error {
	if d == nil {
		return errors.New("dynamic cleanup ownership unavailable")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.owner == nil {
		return nil
	}
	var err error
	for _, name := range d.children {
		err = errors.Join(err, d.owner.RemoveAll(name))
	}
	current, statErr := os.Lstat(d.name)
	if statErr != nil || d.info == nil || !os.SameFile(d.info, current) || d.info.Mode() != current.Mode() {
		err = errors.Join(err, statErr, errors.New("dynamic cleanup pathname ownership changed"))
	} else {
		err = errors.Join(err, os.Remove(d.name))
	}
	if err == nil {
		err = d.owner.Close()
		d.owner = nil
	}
	return err
}

// Failed capture has no returned owner to retry cleanup, so release its handle.
func (d *dynamicDirectory) abandon() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.owner == nil {
		return nil
	}
	err := d.owner.Close()
	d.owner = nil
	return err
}

type dynamicFile struct {
	source, resolved, copy, digest, aliasText string
	file, alias, parent, copied               os.FileInfo
	ancestors                                 []dynamicAncestor
	image                                     dynamicELF
}
type dynamicAncestor struct {
	path, link string
	info       os.FileInfo
}
type dynamicELF struct {
	architecture, interpreter, soname string
	needed                            []string
	kind                              uint16
	entry                             uint64
}

func dynamicText(s string) bool {
	return len(s) > 0 && len(s) <= 4096 && utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n")
}
func dynamicName(s string) bool {
	if len(s) == 0 || len(s) > 255 || s == "." || s == ".." {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_.+-", c)) {
			return false
		}
	}
	return true
}
func preflightRuntime(request RuntimeRequest) error {
	if !dynamicText(request.Loader) || !filepath.IsAbs(request.Loader) || len(request.Libraries) > dynamicLibraryLimit {
		return errors.New("invalid dynamic runtime declaration")
	}
	text := len(request.Loader)
	names := map[string]bool{}
	for _, library := range request.Libraries {
		text += len(library.Name) + len(library.Path)
		if !dynamicName(library.Name) || names[library.Name] || !dynamicText(library.Path) || !filepath.IsAbs(library.Path) || text > 1<<20 {
			return errors.New("invalid dynamic library declaration")
		}
		names[library.Name] = true
	}
	return preflightAddons(request, text)
}

func preflightDynamicFile(name string) (*dynamicFile, error) {
	if !dynamicText(name) || !filepath.IsAbs(name) || filepath.Clean(name) != name {
		return nil, errors.New("invalid dynamic file path")
	}
	source := filepath.Clean(name)
	alias, err := os.Lstat(source)
	resolved, resolveErr := filepath.EvalSymlinks(source)
	if err != nil || resolveErr != nil || !dynamicText(resolved) {
		return nil, errors.New("dynamic source unavailable")
	}
	info, err := os.Lstat(resolved)
	parent, parentErr := os.Lstat(filepath.Dir(source))
	if err != nil || parentErr != nil || !info.Mode().IsRegular() || info.Size() < 64 || info.Size() > DynamicFileLimit {
		return nil, errors.New("dynamic source exceeds bounds or is nonregular")
	}
	f := &dynamicFile{source: source, resolved: resolved, file: info, alias: alias, parent: parent}
	if alias.Mode()&os.ModeSymlink != 0 {
		f.aliasText, err = os.Readlink(source)
		if err != nil || !dynamicText(f.aliasText) {
			return nil, errors.New("dynamic source alias unavailable")
		}
	}
	seen := map[string]bool{}
	// Bind ancestor alias spelling/identity without importing unrelated directory
	// membership. The immediate declared parent retains full metadata separately.
	for _, leaf := range []string{source, resolved} {
		for path := filepath.Dir(leaf); ; path = filepath.Dir(path) {
			if !seen[path] {
				ancestor, err := os.Lstat(path)
				if err != nil {
					return nil, err
				}
				link := ""
				if ancestor.Mode()&os.ModeSymlink != 0 {
					link, err = os.Readlink(path)
					if err != nil || !dynamicText(link) {
						return nil, errors.New("dynamic ancestor alias unavailable")
					}
				}
				if len(f.ancestors) >= 128 {
					return nil, errors.New("dynamic ancestor declaration exceeds bounds")
				}
				f.ancestors = append(f.ancestors, dynamicAncestor{path, link, ancestor})
				seen[path] = true
			}
			if path == filepath.Dir(path) {
				break
			}
		}
	}
	return f, nil
}
func physicalDynamicInfo(info os.FileInfo) any {
	if info == nil {
		return nil
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	return []any{uint64(stat.Dev), uint64(stat.Ino), info.Mode(), info.Size(), info.ModTime()}
}
func (f *dynamicFile) binding() any {
	if f == nil {
		return nil
	}
	ancestors := []any{}
	for _, a := range f.ancestors {
		ancestors = append(ancestors, identity([]any{a.path, a.link, physicalDynamicInfo(a.info)}))
	}
	return []any{f.source, f.resolved, f.copy, f.digest, f.aliasText, physicalDynamicInfo(f.file), physicalDynamicInfo(f.alias), physicalDynamicInfo(f.parent), physicalDynamicInfo(f.copied), ancestors, []any{f.image.architecture, f.image.interpreter, f.image.soname, f.image.needed, f.image.kind, f.image.entry}}
}
func (f *dynamicFile) verifyIdentity() error {
	alias, err := os.Lstat(f.source)
	resolved, resolveErr := filepath.EvalSymlinks(f.source)
	info, infoErr := os.Lstat(f.resolved)
	parent, parentErr := os.Lstat(filepath.Dir(f.source))
	if err != nil || resolveErr != nil || infoErr != nil || parentErr != nil || resolved != f.resolved || !sameProgramInfo(alias, f.alias) || !sameProgramInfo(info, f.file) || !sameProgramInfo(parent, f.parent) {
		return errors.New("dynamic source identity changed")
	}
	if f.alias.Mode()&os.ModeSymlink != 0 {
		link, err := os.Readlink(f.source)
		if err != nil || link != f.aliasText {
			return errors.New("dynamic source alias changed")
		}
	}
	for _, a := range f.ancestors {
		current, err := os.Lstat(a.path)
		if err != nil || !os.SameFile(current, a.info) || current.Mode() != a.info.Mode() {
			return errors.New("dynamic source ancestor changed")
		}
		if a.info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(a.path)
			if err != nil || link != a.link || !sameProgramInfo(current, a.info) {
				return errors.New("dynamic source alias changed")
			}
		}
	}
	return nil
}

type dynamicReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r dynamicReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
func (f *dynamicFile) capture(ctx context.Context, destination string) (err error) {
	if err = f.verifyIdentity(); err != nil {
		return err
	}
	if err = streamDynamicCopy(ctx, f.resolved, f.file, destination); err != nil {
		return err
	}
	if err = f.verifyIdentity(); err != nil {
		return err
	}
	if err = os.Chmod(destination, 0400); err != nil {
		return err
	}
	f.copied, err = os.Lstat(destination)
	if err != nil {
		return err
	}
	private, err := os.OpenFile(destination, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	opened, err := private.Stat()
	if err != nil || !sameProgramInfo(opened, f.copied) {
		private.Close()
		return errors.New("dynamic private copy changed before parsing")
	}
	f.image, err = readDynamicELF(ctx, private, f.file.Size())
	after, statErr := private.Stat()
	closeErr := private.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if statErr != nil || !sameProgramInfo(after, f.copied) {
		return errors.New("dynamic private copy changed during parsing")
	}
	// Parser facts are from this admitted private view, not another live read.
	f.digest, err = snapshot.FileDigest(ctx, destination, DynamicFileLimit)
	if err != nil {
		return err
	}
	original, err := snapshot.FileDigest(ctx, f.resolved, DynamicFileLimit)
	if err != nil {
		return err
	}
	if original != f.digest {
		return errors.New("dynamic copied bytes differ from source")
	}
	f.copy = destination
	return f.verify(ctx)
}
func (f *dynamicFile) verify(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f == nil || f.copy == "" || f.digest == "" {
		return errors.New("dynamic file unavailable")
	}
	if err := f.verifyIdentity(); err != nil {
		return err
	}
	copied, err := os.Lstat(f.copy)
	if err != nil || !sameProgramInfo(copied, f.copied) || !copied.Mode().IsRegular() || copied.Mode().Perm() != 0400 {
		return errors.New("dynamic private copy changed")
	}
	for _, name := range []string{f.copy, f.resolved} {
		hash, err := snapshot.FileDigest(ctx, name, DynamicFileLimit)
		if err != nil {
			return err
		}
		if hash != f.digest {
			return errors.New("dynamic captured bytes changed")
		}
	}
	if err := f.verifyIdentity(); err != nil {
		return err
	}
	copied, err = os.Lstat(f.copy)
	if err != nil || !sameProgramInfo(copied, f.copied) {
		return errors.New("dynamic private copy changed")
	}
	return ctx.Err()
}

// Final identity sweeps catch lasting drift after an earlier byte read. These
// gates are not an atomic snapshot or historical transient-write attestation.
func verifyDynamicIdentities(ctx context.Context, files []*dynamicFile) error {
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if f == nil {
			return errors.New("dynamic file unavailable")
		}
		if err := f.verifyIdentity(); err != nil {
			return err
		}
		info, err := os.Lstat(f.copy)
		if err != nil || !sameProgramInfo(info, f.copied) || !info.Mode().IsRegular() || info.Mode().Perm() != 0400 {
			return errors.New("dynamic private copy changed")
		}
	}
	return ctx.Err()
}
func verifyDynamicDirectoryIdentity(ctx context.Context, name string, before os.FileInfo, files []*dynamicFile) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(name)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !sameProgramInfo(info, before) {
		return errors.New("dynamic private directory changed")
	}
	dir, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	entries, readErr := dir.ReadDir(len(files) + 1)
	closeErr := dir.Close()
	if readErr != nil && readErr != io.EOF {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if len(entries) != len(files) {
		return errors.New("dynamic private membership changed")
	}
	expected := map[string]bool{}
	for _, f := range files {
		expected[filepath.Base(f.copy)] = true
	}
	for _, entry := range entries {
		if !expected[entry.Name()] || !entry.Type().IsRegular() {
			return errors.New("dynamic private membership changed")
		}
	}
	after, err := os.Lstat(name)
	if err != nil || !sameProgramInfo(before, after) {
		return errors.New("dynamic private directory changed")
	}
	return ctx.Err()
}
func verifyDynamicDirectory(ctx context.Context, name string, before os.FileInfo, files []*dynamicFile) error {
	if err := verifyDynamicDirectoryIdentity(ctx, name, before, files); err != nil {
		return err
	}
	for _, f := range files {
		if err := f.verify(ctx); err != nil {
			return err
		}
	}
	if err := verifyDynamicIdentities(ctx, files); err != nil {
		return err
	}
	return verifyDynamicDirectoryIdentity(ctx, name, before, files)
}

func CaptureRuntime(ctx context.Context, request RuntimeRequest) (result *Runtime, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = preflightRuntime(request); err != nil {
		return nil, err
	}
	request.Libraries = append([]RuntimeLibrary(nil), request.Libraries...)
	request.Addons = append([]RuntimeAddon(nil), request.Addons...)
	if err = preflightRuntime(request); err != nil {
		return nil, err
	}
	loader, err := preflightDynamicFile(request.Loader)
	if err != nil {
		return nil, err
	}
	libraries := map[string]*dynamicFile{}
	total := loader.file.Size()
	for _, declared := range request.Libraries {
		f, e := preflightDynamicFile(declared.Path)
		if e != nil {
			return nil, e
		}
		total += f.file.Size()
		if total > inputbundle.MaxBytes {
			return nil, errors.New("dynamic runtime exceeds aggregate byte bounds")
		}
		libraries[declared.Name] = f
	}
	addons := map[string]*dynamicFile{}
	for _, declared := range request.Addons {
		f, e := preflightDynamicFile(declared.Path)
		if e != nil {
			return nil, e
		}
		total += f.file.Size()
		if total > inputbundle.MaxBytes {
			return nil, errors.New("dynamic runtime exceeds aggregate byte bounds")
		}
		addons[declared.Name] = f
	}
	dir, err := os.MkdirTemp("", "hayaku-runtime-")
	if err != nil {
		return nil, err
	}
	created, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	children := []string{"#loader"}
	for name := range libraries {
		children = append(children, name)
	}
	for _, name := range addonNames(addons) {
		children = append(children, addonFileName(name))
	}
	sort.Strings(children)
	cleanup, err := retainDynamicDirectory(dir, created, children)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			if cleanupErr := cleanup.close(); cleanupErr != nil {
				err = errors.Join(err, errors.New("dynamic runtime capture cleanup failed"), cleanupErr)
			}
			err = errors.Join(err, cleanup.abandon())
		}
	}()
	if err = loader.capture(ctx, filepath.Join(dir, "#loader")); err != nil {
		return nil, err
	}
	if loader.image.kind != uint16(elf.ET_DYN) || loader.image.interpreter != "" || loader.image.entry == 0 || loader.file.Mode().Perm()&0111 == 0 {
		return nil, errors.New("dynamic loader is unsupported")
	}
	keys := []string{}
	for name := range libraries {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		f := libraries[name]
		if err = f.capture(ctx, filepath.Join(dir, name)); err != nil {
			return nil, err
		}
		if f.image.kind != uint16(elf.ET_DYN) || (f.image.interpreter != "" && filepath.Base(f.image.interpreter) != rLoaderName(loader)) || f.image.soname != name || f.image.architecture != loader.image.architecture {
			return nil, errors.New("dynamic library role or architecture mismatch")
		}
	}
	for _, name := range addonNames(addons) {
		if err = addons[name].capture(ctx, filepath.Join(dir, addonFileName(name))); err != nil {
			return nil, err
		}
	}
	r := &Runtime{dir: dir, loader: loader, libraries: libraries, addons: addons, bytes: total, cleanup: cleanup}
	r.root, err = os.Lstat(dir)
	if err == nil && (!r.root.IsDir() || r.root.Mode() != created.Mode() || !os.SameFile(created, r.root)) {
		err = errors.New("dynamic runtime private directory replaced")
	}
	if err != nil {
		return nil, err
	}
	if err = r.validateClosure(); err != nil {
		return nil, err
	}
	r.seal = r.currentDigest()
	if err = r.Verify(ctx); err != nil {
		return nil, err
	}
	return r, nil
}
func rLoaderName(loader *dynamicFile) string {
	if loader.image.soname != "" {
		return loader.image.soname
	}
	return filepath.Base(loader.source)
}
func (r *Runtime) loaderName() string { return rLoaderName(r.loader) }
func (r *Runtime) validateClosure() error {
	if r == nil || r.loader == nil || !dynamicName(r.loaderName()) || r.bytes > inputbundle.MaxBytes || len(r.libraries)+len(r.addons) > dynamicLibraryLimit {
		return errors.New("dynamic runtime identity unavailable")
	}
	if _, exists := r.libraries[r.loaderName()]; exists {
		return errors.New("dynamic loader name collision")
	}
	if err := r.validateAddons(); err != nil {
		return err
	}
	for _, f := range r.files() {
		for _, name := range f.image.needed {
			if name != r.loaderName() {
				if _, found := r.libraries[name]; !found {
					return errors.New("dynamic runtime has an unclosed needed name")
				}
			}
		}
	}
	return nil
}
func (r *Runtime) files() []*dynamicFile {
	files := []*dynamicFile{r.loader}
	keys := []string{}
	for name := range r.libraries {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		files = append(files, r.libraries[name])
	}
	for _, name := range addonNames(r.addons) {
		files = append(files, r.addons[name])
	}
	return files
}
func (r *Runtime) currentDigest() string {
	if r == nil || r.loader == nil || len(r.libraries)+len(r.addons) > dynamicLibraryLimit {
		return ""
	}
	data := []any{dynamicPolicy, r.dir, physicalDynamicInfo(r.root), r.bytes, r.closed.Load(), r.loader.binding()}
	keys := []string{}
	for name := range r.libraries {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		data = append(data, []any{name, r.libraries[name].binding()})
	}
	for _, name := range addonNames(r.addons) {
		data = append(data, []any{"native-addon", name, r.addons[name].binding()})
	}
	return identity(data)
}
func (r *Runtime) Verify(ctx context.Context) error {
	if r == nil || r.closed.Load() || len(r.libraries)+len(r.addons) > dynamicLibraryLimit || r.seal == "" || r.currentDigest() != r.seal {
		return errors.New("dynamic runtime private identity changed")
	}
	if err := r.validateClosure(); err != nil {
		return err
	}
	return verifyDynamicDirectory(ctx, r.dir, r.root, r.files())
}
func (r *Runtime) Close() error {
	if r == nil || r.dir == "" {
		return nil
	}
	r.closed.Store(true)
	if err := r.cleanup.close(); err != nil {
		return errors.Join(errors.New("dynamic runtime cleanup failed"), err)
	}
	return nil
}

func CaptureDynamicProgram(ctx context.Context, request DynamicProgramRequest) (result *DynamicProgram, err error) {
	return captureDynamicProgram(ctx, request, "")
}

// Only an internal producer can select this operational compiler profile. The
// eventual issuer must independently admit the exact compiler/framework closure
// and closed user grammar; public capture and caller hashes cannot select it.
func captureManagedCompilerProgram(ctx context.Context, request DynamicProgramRequest) (*DynamicProgram, error) {
	return captureDynamicProgram(ctx, request, managedCompilerProfile)
}

func captureDynamicProgram(ctx context.Context, request DynamicProgramRequest, profile string) (result *DynamicProgram, err error) {
	if profile != "" && profile != managedCompilerProfile {
		return nil, errors.New("unknown dynamic compiler profile")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = validateNativeArguments(request.Args, nil); err != nil {
		return nil, err
	}
	request.Args = append([]string(nil), request.Args...)
	if err = validateNativeArguments(request.Args, nil); err != nil {
		return nil, err
	}
	if request.Runtime == nil || request.Runtime.closed.Load() {
		return nil, errors.New("dynamic runtime unavailable")
	}
	primary, err := preflightDynamicFile(request.Program)
	if err != nil {
		return nil, err
	}
	if primary.file.Size() > inputbundle.MaxBytes-request.Runtime.bytes {
		return nil, errors.New("dynamic program exceeds aggregate byte bounds")
	}
	if err = request.Runtime.Verify(ctx); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "hayaku-dynamic-program-")
	if err != nil {
		return nil, err
	}
	created, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	cleanup, err := retainDynamicDirectory(dir, created, []string{"program"})
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			if cleanupErr := cleanup.close(); cleanupErr != nil {
				err = errors.Join(err, errors.New("dynamic program capture cleanup failed"), cleanupErr)
			}
			err = errors.Join(err, cleanup.abandon())
		}
	}()
	if err = primary.capture(ctx, filepath.Join(dir, "program")); err != nil {
		return nil, err
	}
	runtime := request.Runtime
	if primary.file.Mode().Perm()&0111 == 0 || primary.image.entry == 0 || primary.image.interpreter == "" || primary.image.architecture != runtime.loader.image.architecture || filepath.Base(primary.image.interpreter) != runtime.loaderName() {
		return nil, errors.New("dynamic primary role or interpreter mismatch")
	}
	for _, name := range primary.image.needed {
		if name != runtime.loaderName() {
			if _, found := runtime.libraries[name]; !found {
				return nil, errors.New("dynamic program has an unclosed needed name")
			}
		}
	}
	p := &DynamicProgram{dir: dir, primary: primary, runtime: runtime, args: append([]string(nil), request.Args...), profile: profile, cleanup: cleanup}
	p.root, err = os.Lstat(dir)
	if err == nil && (!p.root.IsDir() || p.root.Mode() != created.Mode() || !os.SameFile(created, p.root)) {
		err = errors.New("dynamic program private directory replaced")
	}
	if err != nil {
		return nil, err
	}
	p.seal = p.currentDigest()
	if err = p.Verify(ctx); err != nil {
		return nil, err
	}
	return p, nil
}
func (p *DynamicProgram) currentDigest() string {
	if p == nil || p.primary == nil || p.runtime == nil {
		return ""
	}
	data := []any{dynamicPolicy, p.dir, physicalDynamicInfo(p.root), p.primary.binding(), p.runtime.currentDigest(), p.args, p.closed.Load()}
	if p.profile != "" {
		data = append(data, p.profile)
	}
	return identity(data)
}
func (p *DynamicProgram) Verify(ctx context.Context) error {
	if p == nil || p.closed.Load() || p.seal == "" || (p.profile != "" && p.profile != managedCompilerProfile) || p.currentDigest() != p.seal || validateNativeArguments(p.args, nil) != nil {
		return errors.New("dynamic program private identity changed")
	}
	if err := p.runtime.Verify(ctx); err != nil {
		return err
	}
	if err := verifyDynamicDirectory(ctx, p.dir, p.root, []*dynamicFile{p.primary}); err != nil {
		return err
	}
	files := append(p.runtime.files(), p.primary)
	if p.runtime.closed.Load() || p.currentDigest() != p.seal {
		return errors.New("dynamic program private identity changed")
	}
	if err := verifyDynamicIdentities(ctx, files); err != nil {
		return err
	}
	if err := verifyDynamicDirectoryIdentity(ctx, p.runtime.dir, p.runtime.root, p.runtime.files()); err != nil {
		return err
	}
	return verifyDynamicDirectoryIdentity(ctx, p.dir, p.root, []*dynamicFile{p.primary})
}
func (p *DynamicProgram) Close() error {
	if p == nil || p.dir == "" {
		return nil
	}
	p.closed.Store(true)
	if err := p.cleanup.close(); err != nil {
		return errors.Join(errors.New("dynamic program cleanup failed"), err)
	}
	return nil
}

// This parser never asks debug/elf to allocate from untrusted section counts.
// Only bounded ELF64 program/dynamic tables and a bounded dynamic string table
// are consumed; all translations must have one unambiguous file-backed load.
func readDynamicELF(ctx context.Context, reader io.ReaderAt, size int64) (image dynamicELF, err error) {
	fail := func() (dynamicELF, error) { return dynamicELF{}, errors.New("unsupported or malformed dynamic ELF") }
	read := func(offset uint64, data []byte) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if offset > uint64(size) || uint64(len(data)) > uint64(size)-offset {
			return io.ErrUnexpectedEOF
		}
		_, e := reader.ReadAt(data, int64(offset))
		return e
	}
	if size < 64 || size > DynamicFileLimit {
		return fail()
	}
	header := make([]byte, 64)
	if err = read(0, header); err != nil {
		return image, err
	}
	if string(header[:4]) != "\x7fELF" || header[4] != 2 || header[5] != 1 || header[6] != 1 || (header[7] != 0 && header[7] != 3) || binary.LittleEndian.Uint32(header[20:24]) != 1 || binary.LittleEndian.Uint16(header[52:54]) != 64 || binary.LittleEndian.Uint16(header[54:56]) != 56 {
		return fail()
	}
	image.kind = binary.LittleEndian.Uint16(header[16:18])
	if image.kind != uint16(elf.ET_DYN) && image.kind != uint16(elf.ET_EXEC) {
		return fail()
	}
	switch elf.Machine(binary.LittleEndian.Uint16(header[18:20])) {
	case elf.EM_AARCH64:
		image.architecture = "arm64"
	case elf.EM_X86_64:
		image.architecture = "amd64"
	default:
		return fail()
	}
	image.entry = binary.LittleEndian.Uint64(header[24:32])
	phoff := binary.LittleEndian.Uint64(header[32:40])
	count := int(binary.LittleEndian.Uint16(header[56:58]))
	if count < 1 || count > 128 || phoff < 64 || phoff > uint64(size) || uint64(count)*56 > uint64(size)-phoff {
		return fail()
	}
	type segment struct {
		offset, address, file, memory uint64
		flags                         uint32
	}
	loads := []segment{}
	var dynamic, interp *segment
	stack, code := false, false
	for i := 0; i < count; i++ {
		raw := make([]byte, 56)
		if err = read(phoff+uint64(i)*56, raw); err != nil {
			return image, err
		}
		kind := elf.ProgType(binary.LittleEndian.Uint32(raw[:4]))
		p := segment{binary.LittleEndian.Uint64(raw[8:16]), binary.LittleEndian.Uint64(raw[16:24]), binary.LittleEndian.Uint64(raw[32:40]), binary.LittleEndian.Uint64(raw[40:48]), binary.LittleEndian.Uint32(raw[4:8])}
		if p.offset > uint64(size) || p.file > uint64(size)-p.offset || p.memory < p.file || p.address > ^uint64(0)-p.memory {
			return fail()
		}
		if kind == elf.PT_LOAD {
			if p.flags&3 == 3 {
				return fail()
			}
			align := binary.LittleEndian.Uint64(raw[48:56])
			if align > 1 && (align&(align-1) != 0 || p.offset%align != p.address%align) {
				return fail()
			}
			for _, old := range loads {
				if p.memory > 0 && old.memory > 0 && p.address < old.address+old.memory && old.address < p.address+p.memory {
					return fail()
				}
			}
			loads = append(loads, p)
			if p.flags&1 != 0 {
				code = true
			}
		}
		switch kind {
		case elf.PT_DYNAMIC:
			if dynamic != nil {
				return fail()
			}
			copy := p
			dynamic = &copy
		case elf.PT_INTERP:
			if interp != nil || p.file < 2 || p.file > 4096 {
				return fail()
			}
			copy := p
			interp = &copy
		case elf.PT_GNU_STACK:
			if stack || p.flags&1 != 0 {
				return fail()
			}
			stack = true
		}
	}
	if !code || !stack || dynamic == nil || dynamic.file == 0 || dynamic.file%16 != 0 || dynamic.file/16 > 4096 {
		return fail()
	}
	if interp != nil {
		data := make([]byte, int(interp.file))
		if err = read(interp.offset, data); err != nil {
			return image, err
		}
		if data[len(data)-1] != 0 || strings.IndexByte(string(data[:len(data)-1]), 0) >= 0 {
			return fail()
		}
		image.interpreter = string(data[:len(data)-1])
		if !dynamicText(image.interpreter) || !filepath.IsAbs(image.interpreter) {
			return fail()
		}
	}
	translate := func(address, length uint64) (uint64, bool) {
		var offset uint64
		matches := 0
		for _, p := range loads {
			if address >= p.address && address-p.address <= p.file && length <= p.file-(address-p.address) {
				offset = p.offset + address - p.address
				matches++
			}
		}
		return offset, matches == 1
	}
	if offset, ok := translate(dynamic.address, dynamic.file); !ok || offset != dynamic.offset {
		return fail()
	}
	if image.entry != 0 {
		valid := false
		for _, p := range loads {
			if p.flags&1 != 0 && image.entry >= p.address && image.entry-p.address < p.memory {
				valid = true
			}
		}
		if !valid {
			return fail()
		}
	}
	tags := map[uint64]uint64{}
	needed := []uint64{}
	terminated := false
	for offset := uint64(0); offset < dynamic.file; offset += 16 {
		raw := make([]byte, 16)
		if err = read(dynamic.offset+offset, raw); err != nil {
			return image, err
		}
		tag, value := binary.LittleEndian.Uint64(raw[:8]), binary.LittleEndian.Uint64(raw[8:])
		if tag == 0 {
			terminated = true
			break
		}
		switch elf.DynTag(tag) {
		case elf.DT_NEEDED:
			if len(needed) >= dynamicLibraryLimit {
				return fail()
			}
			needed = append(needed, value)
		case elf.DT_RPATH, elf.DT_RUNPATH, elf.DT_TEXTREL, elf.DT_AUXILIARY, elf.DT_FILTER, elf.DT_AUDIT, elf.DT_DEPAUDIT, elf.DT_CONFIG:
			return fail()
		default:
			if _, duplicate := tags[tag]; duplicate {
				return fail()
			}
			tags[tag] = value
		}
	}
	if !terminated {
		return fail()
	}
	address, hasAddress := tags[uint64(elf.DT_STRTAB)]
	length, hasLength := tags[uint64(elf.DT_STRSZ)]
	if !hasAddress || !hasLength || length == 0 || length > uint64(DynamicFileLimit) {
		return fail()
	}
	offset, ok := translate(address, length)
	if !ok {
		return fail()
	}
	if flags := tags[uint64(elf.DT_FLAGS)]; flags&(uint64(elf.DF_TEXTREL)|uint64(elf.DF_ORIGIN)) != 0 {
		return fail()
	}
	if flags := tags[uint64(elf.DT_FLAGS_1)]; flags&uint64(elf.DF_1_ORIGIN) != 0 {
		return fail()
	}
	// Symbol string tables can be large (the pinned Node executable is >7MiB).
	// Validate their full file-backed range above, then read only bounded names.
	name := func(index uint64) (string, error) {
		if index >= length {
			return "", errors.New("dynamic name offset exceeds table")
		}
		size := length - index
		if size > 256 {
			size = 256
		}
		data := make([]byte, int(size))
		if err := read(offset+index, data); err != nil {
			return "", err
		}
		end := bytes.IndexByte(data, 0)
		if end < 0 {
			return "", errors.New("dynamic name exceeds bounds or is unterminated")
		}
		text := string(data[:end])
		if !dynamicName(text) {
			return "", errors.New("unsupported dynamic name")
		}
		return text, nil
	}
	if offset, present := tags[uint64(elf.DT_SONAME)]; present {
		value, nameErr := name(offset)
		if nameErr != nil {
			return image, nameErr
		}
		image.soname = value
	}
	seen := map[string]bool{}
	for _, offset := range needed {
		value, nameErr := name(offset)
		if nameErr != nil {
			return image, nameErr
		}
		if seen[value] {
			return fail()
		}
		image.needed = append(image.needed, value)
		seen[value] = true
	}
	return image, ctx.Err()
}

// streamDynamicCopy is an admitted-size copy, not an atomic source snapshot.
// FileDigest below independently binds the stable copied/source byte identities.
func streamDynamicCopy(ctx context.Context, name string, before os.FileInfo, destination string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if before == nil || !before.Mode().IsRegular() || before.Size() < 0 || before.Size() > DynamicFileLimit {
		return errors.New("dynamic copy exceeds bounds")
	}
	source, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	opened, err := source.Stat()
	if err != nil || !sameProgramInfo(opened, before) {
		source.Close()
		return errors.New("dynamic source changed before copying")
	}
	destinationFile, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		source.Close()
		return err
	}
	n, readErr := io.CopyBuffer(destinationFile, dynamicReader{ctx, io.LimitReader(source, before.Size()+1)}, make([]byte, 32<<10))
	writeClose := destinationFile.Close()
	after, statErr := source.Stat()
	sourceClose := source.Close()
	if err := errors.Join(readErr, writeClose, sourceClose, ctx.Err()); err != nil {
		return err
	}
	pathAfter, pathErr := os.Lstat(name)
	if statErr != nil || pathErr != nil || n != before.Size() || !sameProgramInfo(after, before) || !sameProgramInfo(pathAfter, before) {
		return errors.New("dynamic source changed during copying")
	}
	return nil
}

type dynamicTransportFile struct {
	file      *dynamicFile
	path      string
	info      os.FileInfo
	directory os.FileInfo
	mode      os.FileMode
}

func (p *DynamicProgram) materialize(ctx context.Context, directory, architecture string) ([]dynamicTransportFile, error) {
	if err := p.Verify(ctx); err != nil {
		return nil, err
	}
	if p.primary.image.architecture != architecture {
		return nil, errors.New("dynamic architecture mismatch")
	}
	if err := os.Mkdir(directory, 0755); err != nil {
		return nil, err
	}
	files := []dynamicTransportFile{{file: p.primary, path: filepath.Join(directory, "#program")}, {file: p.runtime.loader, path: filepath.Join(directory, p.runtime.loaderName())}}
	for _, f := range p.runtime.files()[1 : 1+len(p.runtime.libraries)] {
		files = append(files, dynamicTransportFile{file: f, path: filepath.Join(directory, f.image.soname)})
	}
	for _, name := range addonNames(p.runtime.addons) {
		files = append(files, dynamicTransportFile{file: p.runtime.addons[name], path: filepath.Join(directory, addonFileName(name)), mode: 0444})
	}
	for i := range files {
		f := &files[i]
		if err := streamDynamicCopy(ctx, f.file.copy, f.file.copied, f.path); err != nil {
			return nil, err
		}
		mode := f.mode
		if mode == 0 {
			mode = 0555
		}
		if err := os.Chmod(f.path, mode); err != nil {
			return nil, err
		}
		var err error
		f.info, err = os.Lstat(f.path)
		if err != nil {
			return nil, err
		}
	}
	var err error
	files[0].directory, err = os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if err := verifyDynamicTransport(ctx, files); err != nil {
		return nil, err
	}
	return files, p.Verify(ctx)
}
func verifyDynamicTransport(ctx context.Context, files []dynamicTransportFile) error {
	if len(files) < 2 || len(files) > dynamicLibraryLimit+2 {
		return errors.New("dynamic transport unavailable")
	}
	directory := filepath.Dir(files[0].path)
	before := files[0].directory
	checkDirectory := func() error {
		current, err := os.Lstat(directory)
		if err != nil || !sameProgramInfo(current, before) {
			return errors.New("dynamic transport directory changed")
		}
		dir, err := os.OpenFile(directory, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		entries, readErr := dir.ReadDir(len(files) + 1)
		closeErr := dir.Close()
		if (readErr != nil && readErr != io.EOF) || closeErr != nil {
			return errors.Join(readErr, closeErr)
		}
		if len(entries) != len(files) {
			return errors.New("dynamic transport membership changed")
		}
		expected := map[string]bool{}
		for _, f := range files {
			if filepath.Dir(f.path) != directory {
				return errors.New("dynamic transport escaped")
			}
			expected[filepath.Base(f.path)] = true
		}
		for _, entry := range entries {
			if !expected[entry.Name()] || !entry.Type().IsRegular() {
				return errors.New("dynamic transport membership changed")
			}
		}
		return nil
	}
	if err := checkDirectory(); err != nil {
		return err
	}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := os.Lstat(f.path)
		if err != nil || !sameProgramInfo(current, f.info) {
			return errors.New("dynamic transport copy changed")
		}
		hash, err := snapshot.FileDigest(ctx, f.path, DynamicFileLimit)
		if err != nil {
			return err
		}
		if hash != f.file.digest {
			return errors.New("dynamic transport bytes changed")
		}
	}
	for _, f := range files {
		current, err := os.Lstat(f.path)
		if err != nil || !sameProgramInfo(current, f.info) {
			return errors.New("dynamic transport copy changed")
		}
	}
	if err := checkDirectory(); err != nil {
		return err
	}
	return ctx.Err()
}

type dynamicResource struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Size   int64  `json:"size,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Link   string `json:"link,omitempty"`
}
type dynamicResources struct {
	Entries []dynamicResource `json:"entries"`
	Absent  []string          `json:"absent"`
	Bytes   int64             `json:"bytes"`
}

func dynamicResourceManifest(e *Envelope) ([]byte, error) {
	m := e.files.Manifest()
	if m.Bytes > MaxScratchBytes || len(m.Entries) > inputbundle.MaxEntries {
		return nil, errors.New("dynamic resources exceed guest bounds")
	}
	declaration := dynamicResources{Bytes: m.Bytes, Absent: append([]string(nil), e.absent...)}
	text := len(e.absent) * 4
	for _, name := range e.absent {
		text += 6 * len(name)
	}
	for _, entry := range m.Entries {
		text += 6*(len(entry.Path)+len(entry.Kind)+len(entry.SHA256)+len(entry.Link)) + 256
		if text > 8<<20 {
			return nil, errors.New("dynamic resource manifest exceeds bounds")
		}
		declaration.Entries = append(declaration.Entries, dynamicResource{entry.Path, entry.Kind, entry.Size, entry.SHA256, entry.Link})
	}
	data, err := json.Marshal(declaration)
	if len(data) > 8<<20 {
		return nil, errors.New("dynamic resource manifest exceeds bounds")
	}
	return data, err
}

// RunDynamicProgram is a modified manual-loader operational profile. Captured
// runtime files may supply executable code, including native extensions. It
// provides no JIT provenance, original startup equivalence or receipt authority.
// Permission/ownership/timestamp/xattr/file-attribute mutators are denied,
// including in scratch. Only ioctl FIONBIO is admitted on descriptors 0..2:
// zero/nonzero int values toggle nonblocking mode, including after descriptor
// remapping. Other ioctls stay denied. Ordinary scratch writes change metadata.
func RunDynamicProgram(ctx context.Context, b Backend, p *DynamicProgram, e *Envelope) (result process.Output, runErr error) {
	if b.platform() != "linux" || b.Sandbox != "" || e == nil || e.child != nil {
		return result, errors.New("dynamic program requires Linux resources without managed children")
	}
	if err := p.Verify(ctx); err != nil {
		return result, err
	}
	if err := e.VerifySources(ctx); err != nil {
		return result, err
	}
	if validateNativeArguments(p.args, e.env) != nil || b.Identity == "" || p.primary.image.architecture != b.Architecture {
		return result, errors.New("dynamic launch context unavailable")
	}
	for _, entry := range e.env {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "LD_") || key == "GLIBC_TUNABLES" {
			return result, errors.New("ambient loader environment is unsupported")
		}
	}
	if p.runtime.bytes+p.primary.file.Size()+e.files.Manifest().Bytes > inputbundle.MaxBytes {
		return result, errors.New("dynamic inputs exceed aggregate bounds")
	}
	binding := identity([]string{dynamicPolicy, p.seal, e.Digest(), b.Identity})
	run, cancel := context.WithTimeout(ctx, ProgramTimeout)
	defer cancel()
	if err := b.unchanged(run); err != nil {
		return result, err
	}
	defer func() {
		validation, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err := errors.Join(p.Verify(validation), e.VerifySources(validation), b.unchanged(validation))
		if identity([]string{dynamicPolicy, p.seal, e.Digest(), b.Identity}) != binding {
			err = errors.Join(err, errors.New("dynamic launch identity changed"))
		}
		if err != nil {
			result.Completed = false
			runErr = errors.Join(runErr, err)
		}
	}()
	return b.executePrograms(run, Artifact{Args: p.args, Envelope: e}, nil, p)
}

type dynamicInvocation struct {
	Loader          string `json:"loader"`
	ResourcesDigest string `json:"resources_digest"`
	CompilerProfile string `json:"compiler_profile,omitempty"`
}

// Copied controller/configuration files are part of the same launch context,
// even when their original source identities remain unchanged.
type transportControl struct {
	path, digest string
	info         os.FileInfo
	limit        int64
}

func captureTransportControl(ctx context.Context, name, expected string, limit int64) (transportControl, error) {
	info, err := os.Lstat(name)
	control := transportControl{path: name, digest: expected, info: info, limit: limit}
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit || info.Size() < 0 {
		return transportControl{}, errors.New("native copied control unavailable")
	}
	if err := verifyTransportControls(ctx, []transportControl{control}); err != nil {
		return transportControl{}, err
	}
	return control, nil
}
func verifyTransportControls(ctx context.Context, controls []transportControl) error {
	for _, c := range controls {
		before, err := os.Lstat(c.path)
		if err != nil || !sameProgramInfo(before, c.info) {
			return errors.New("native copied control changed")
		}
		hash, err := snapshot.FileDigest(ctx, c.path, c.limit)
		if err != nil {
			return err
		}
		if hash != c.digest {
			return errors.New("native copied control bytes changed")
		}
	}
	for _, c := range controls {
		if err := ctx.Err(); err != nil {
			return err
		}
		after, err := os.Lstat(c.path)
		if err != nil || !sameProgramInfo(after, c.info) {
			return errors.New("native copied control changed")
		}
	}
	return ctx.Err()
}
