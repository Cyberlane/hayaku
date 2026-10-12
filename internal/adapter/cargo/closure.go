package cargo

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
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/inputbundle"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/runner"
	"github.com/Cyberlane/hayaku/internal/snapshot"
)

const ClosureVersion = "cargo-build-closure-v2"

// Closure describes a configured build and observed inventory. Metadata does
// not expose harness semantics; native --list output cannot certify a custom
// harness or Cargo doctests. No field grants runtime qualification or omission.
type Closure struct {
	Version, Digest, SourceDigest, ToolchainDigest, ContextDigest, RuntimeDigest string
	Packages                                                                     []ClosurePackage
	Dependencies                                                                 []ClosureDependency
	Scopes                                                                       []model.Unit
	Inventory                                                                    model.ExecutionInventory
	InventoryComplete                                                            bool
	Artifacts                                                                    []ClosureArtifact
	Producers                                                                    []ClosureProducer
	Effects                                                                      []model.Effect
	Gaps                                                                         []model.Gap
	evidence                                                                     model.Evidence
	metadata                                                                     metadata
	root, dir, temp                                                              string
	workspace                                                                    model.Workspace
	execution                                                                    model.Context
	env                                                                          []string
	bindings                                                                     []closureBinding
	bytes                                                                        int64
	cargoInvocation                                                              string
	invocations                                                                  []closureInvocation
	toolInvocations                                                              map[string]string
	stateDigest                                                                  string
	libraryDirectory, libraryQuery                                               string
	directories                                                                  []closureDirectory
	linkedDirectories                                                            []string
	directoryBytes                                                               int64
	requestDigest                                                                string
}
type ClosurePackage struct {
	ID, Name, Version, Manifest string
	Features                    []string
	Targets                     []ClosureTarget
}
type ClosureTarget struct {
	Name, Source                       string
	Kind, CrateTypes, RequiredFeatures []string
	Test, Doctest                      bool
}
type ClosureDependency struct{ From, To, Name, Kind, Platform string }
type closureDirectory struct {
	path, resolved, digest string
	alias, root            os.FileInfo
	bytes                  int64
}
type closureBinary struct {
	Owner, Name string
	Target      target
}
type ClosureArtifact struct {
	ContextDigest             string
	Owner, Name, Kind, Digest string
	Cases                     []string
}
type ClosureProducer struct{ Owner, RecordDigest, OutputsDigest string }
type closureInvocation struct {
	path, resolved string
	before         fs.FileInfo
}
type closureBinding struct {
	source string
	bundle *inputbundle.Bundle
}

func closureIdentity(v any) string {
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func closureText(value string) bool {
	return value != "" && len(value) <= 8192 && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

// ValidateClosureRequest bounds configured identities before hashing/copying
// them. It is shared by Cargo and nextest, and supplies no qualification.
func ValidateClosureRequest(w model.Workspace, execution model.Context) error {
	if len(w.Command.Args) > 1024 || len(w.Prerequisites) > 128 || len(w.Patterns)+len(w.BuildFlags)+len(w.Inputs) > 50000 || len(execution.Env) > 1024 {
		return errors.New("Rust configured context exceeds bounds")
	}
	remaining := 1 << 20
	check := func(value string) bool {
		if len(value) > 8192 || len(value) > remaining || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return false
		}
		remaining -= len(value)
		return true
	}
	for _, value := range []string{w.ID, w.Root, w.Adapter, w.Command.Dir, w.Command.Executable, execution.ID, execution.OS, execution.Arch} {
		if !check(value) {
			return errors.New("invalid Rust configured context")
		}
	}
	for _, values := range [][]string{w.Command.Args, w.Patterns, w.BuildFlags, w.Inputs} {
		for _, value := range values {
			if !check(value) {
				return errors.New("invalid Rust configured context")
			}
		}
	}
	for _, command := range w.Prerequisites {
		if len(command.Args) > 1024 || !check(command.Dir) || !check(command.Executable) {
			return errors.New("invalid Rust configured context")
		}
		for _, value := range command.Args {
			if !check(value) {
				return errors.New("invalid Rust configured context")
			}
		}
	}
	for key, value := range execution.Env {
		if key == "" || strings.ContainsRune(key, '=') || !check(key) || !check(value) {
			return errors.New("invalid Rust configured environment")
		}
	}
	if w.NodeRuntime != nil && (!check(w.NodeRuntime.Node) || !check(w.NodeRuntime.Modules)) || w.SwiftRuntime != nil && !check(w.SwiftRuntime.Dependencies) {
		return errors.New("invalid Rust configured context")
	}
	return nil
}

// Close releases private build products. Public digests and observations remain
// descriptive data; revalidation after Close is intentionally unavailable.
func (c *Closure) Close() error {
	if c.temp == "" {
		return nil
	}
	var validation error
	if c.Digest != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		validation = c.Verify(ctx)
		cancel()
	}
	if err := os.RemoveAll(c.temp); err != nil {
		return errors.New("Cargo private artifact cleanup failed")
	}
	c.temp = ""
	return validation
}

func (c *Closure) Verify(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil || c.temp == "" || c.Digest == "" || c.currentDigest() != c.Digest || c.privateStateDigest() != c.stateDigest {
		return errors.New("Cargo closure identity unavailable or changed")
	}
	now, err := snapshot.Digest(ctx, c.root)
	if err != nil || now != c.SourceDigest {
		return errors.New("Cargo source changed during collection")
	}
	return c.verifyBindings(ctx)
}

func (c *Closure) verifyBindings(ctx context.Context) error {
	if c.requestDigest != closureIdentity([]any{c.workspace, c.execution}) {
		return errors.New("Cargo caller context changed during collection")
	}
	if err := c.verifyToolBindings(ctx); err != nil {
		return err
	}
	if c.libraryQuery != "" {
		out, err := process.RunEnvironmentLimits(ctx, model.Command{Executable: environmentValue(c.env, "RUSTC"), Args: []string{"--print", "target-libdir"}}, c.dir, c.env, 8192, 1<<20)
		if err != nil || strings.TrimSpace(string(out.Stdout)) != c.libraryQuery {
			return errors.New("Cargo compiler runtime directory changed")
		}
	}
	if err := c.verifyToolBindings(ctx); err != nil {
		return err
	}
	for _, directory := range c.directories {
		alias, err := os.Lstat(directory.path)
		resolved, resolveErr := filepath.EvalSymlinks(directory.path)
		root, rootErr := os.Lstat(directory.resolved)
		if err != nil || resolveErr != nil || rootErr != nil || resolved != directory.resolved || !sameClosureInfo(directory.alias, alias) || !sameClosureInfo(directory.root, root) {
			return errors.New("Cargo runtime directory alias changed")
		}
		now, err := snapshot.Digest(ctx, directory.resolved)
		if err != nil || now != directory.digest {
			return errors.New("Cargo runtime directory changed")
		}
		aliasAfter, aliasErr := os.Lstat(directory.path)
		resolvedAfter, resolveErr := filepath.EvalSymlinks(directory.path)
		rootAfter, rootErr := os.Lstat(directory.resolved)
		if aliasErr != nil || resolveErr != nil || rootErr != nil || resolvedAfter != directory.resolved || !sameClosureInfo(directory.alias, aliasAfter) || !sameClosureInfo(directory.root, rootAfter) {
			return errors.New("Cargo runtime directory alias changed")
		}
	}
	return ctx.Err()
}

func (c *Closure) verifyToolBindings(ctx context.Context) error {
	for _, invocation := range c.invocations {
		after, err := os.Lstat(invocation.path)
		resolved, resolveErr := filepath.EvalSymlinks(invocation.path)
		if err != nil || resolveErr != nil || resolved != invocation.resolved || !os.SameFile(invocation.before, after) || invocation.before.Mode() != after.Mode() || invocation.before.Size() != after.Size() || !invocation.before.ModTime().Equal(after.ModTime()) {
			return errors.New("Cargo tool invocation changed during collection")
		}
	}
	for _, binding := range c.bindings {
		if err := binding.bundle.VerifySources(ctx); err != nil {
			return errors.New("Cargo tool or artifact changed during collection")
		}
	}
	return ctx.Err()
}

func sameClosureInfo(a, b os.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) && a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func (c *Closure) directoryIdentities() []string {
	ids := make([]string, 0, len(c.directories))
	for _, d := range c.directories {
		ids = append(ids, closureIdentity([]any{d.path, d.resolved, d.digest, d.bytes}))
	}
	return ids
}

func (c *Closure) captureRuntimeDirectory(ctx context.Context, name string) error {
	if !filepath.IsAbs(name) || !closureText(name) || len(c.directories) >= 128 {
		return errors.New("invalid Cargo runtime directory")
	}
	alias, err := os.Lstat(name)
	resolved, resolveErr := filepath.EvalSymlinks(name)
	if err != nil || resolveErr != nil {
		return errors.New("Cargo runtime directory unavailable")
	}
	root, err := os.Lstat(resolved)
	if err != nil || !root.IsDir() {
		return errors.New("Cargo runtime directory unavailable")
	}
	sum, err := snapshot.Digest(ctx, resolved)
	if err != nil {
		return errors.Join(errors.New("Cargo runtime tree unavailable or exceeds bounds"), err)
	}
	var size int64
	count := 0
	err = filepath.WalkDir(resolved, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		count++
		if count > 50000 {
			return errors.New("runtime tree entry limit")
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > inputbundle.MaxBytes-c.bytes-c.directoryBytes-size {
			return errors.New("runtime tree byte limit")
		}
		size += info.Size()
		return nil
	})
	after, afterErr := os.Lstat(name)
	rootAfter, rootErr := os.Lstat(resolved)
	resolvedAfter, resolveAfterErr := filepath.EvalSymlinks(name)
	if err != nil || afterErr != nil || rootErr != nil || resolveAfterErr != nil || resolvedAfter != resolved || !sameClosureInfo(alias, after) || !sameClosureInfo(root, rootAfter) {
		return errors.New("Cargo runtime tree changed or exceeds bounds")
	}
	c.directoryBytes += size
	c.directories = append(c.directories, closureDirectory{name, resolved, sum, alias, root, size})
	return nil
}

func (c *Closure) prepareNativeLibraries(ctx context.Context) error {
	if c.privateStateDigest() != c.stateDigest {
		return errors.New("Cargo private launch context changed")
	}
	if err := c.verifyBindings(ctx); err != nil {
		return err
	}
	out, err := process.RunEnvironmentLimits(ctx, model.Command{Executable: environmentValue(c.env, "RUSTC"), Args: []string{"--print", "target-libdir"}}, c.dir, c.env, 8192, 1<<20)
	if err != nil || !out.Completed {
		return errors.New("Cargo compiler runtime directory unavailable")
	}
	query := strings.TrimSpace(string(out.Stdout))
	if !filepath.IsAbs(query) || !closureText(query) || strings.ContainsAny(query, "\r\n") {
		return errors.New("invalid Cargo compiler runtime directory")
	}
	if err := c.verifyBindings(ctx); err != nil {
		return err
	}
	if err := c.captureRuntimeDirectory(ctx, query); err != nil {
		return err
	}
	if err := c.captureRuntimeDirectory(ctx, c.PrivateTargetDirectory()); err != nil {
		return err
	}
	c.libraryQuery = query
	c.libraryDirectory = c.directories[0].resolved
	c.RuntimeDigest = closureIdentity(c.directoryIdentities())
	c.stateDigest = c.privateStateDigest()
	c.Gaps = append(c.Gaps, model.Gap{Code: "cargo-native-list-context-unqualified", Workspace: c.workspace.ID, Detail: "Observed native cases use captured private target and compiler library search context; system loader, native dependencies, macOS fallback defaults and original runtime equivalence remain unresolved"})
	return c.verifyBindings(ctx)
}

func (c *Closure) nativeListEnvironment(program string) ([]string, error) {
	key := "LD_LIBRARY_PATH"
	if runtime.GOOS == "darwin" {
		key = "DYLD_FALLBACK_LIBRARY_PATH"
	}
	paths := []string{filepath.Dir(program), filepath.Dir(filepath.Dir(program)), c.libraryDirectory}
	paths = append(paths, c.linkedDirectories...)
	for _, value := range c.env {
		name, inherited, ok := strings.Cut(value, "=")
		if !ok || name != key {
			continue
		}
		if inherited == "" {
			continue
		}
		for _, path := range filepath.SplitList(inherited) {
			if !filepath.IsAbs(path) {
				return nil, errors.New("Cargo inherited runtime search is unresolved")
			}
			actual, err := filepath.EvalSymlinks(path)
			if err != nil || actual != filepath.Clean(path) || !within(c.PrivateTargetDirectory(), actual) && !within(c.libraryDirectory, actual) {
				return nil, errors.New("Cargo inherited runtime search is outside captured context")
			}
			paths = append(paths, path)
		}
	}
	if len(paths) > 1024 {
		return nil, errors.New("Cargo runtime search exceeds bounds")
	}
	searchBytes := 0
	for _, path := range paths {
		searchBytes += len(path) + 1
		if !closureText(path) || searchBytes > 1<<20 {
			return nil, errors.New("Cargo runtime search exceeds bounds")
		}
		actual, err := filepath.EvalSymlinks(path)
		if err != nil || actual != filepath.Clean(path) || !within(c.PrivateTargetDirectory(), actual) && !within(c.libraryDirectory, actual) {
			return nil, errors.New("Cargo runtime search escapes captured context")
		}
	}
	envBytes := searchBytes
	for _, entry := range c.env {
		envBytes += len(entry)
		if envBytes > 1<<20 {
			return nil, errors.New("Cargo native environment exceeds bounds")
		}
	}
	return pinEnvironment(append([]string(nil), c.env...), key, strings.Join(paths, string(os.PathListSeparator))), nil
}
func (c *Closure) currentDigest() string {
	bindingDigests := make([]string, 0, len(c.bindings))
	for _, binding := range c.bindings {
		bindingDigests = append(bindingDigests, binding.bundle.Manifest().Digest)
	}
	return closureIdentity([]any{bindingDigests, c.evidence.Inputs, c.evidence.Nodes, c.evidence.Edges, struct {
		Version, Source, Toolchain, Context, Runtime string
		Packages                                     []ClosurePackage
		Dependencies                                 []ClosureDependency
		Scopes                                       []model.Unit
		Inventory                                    model.ExecutionInventory
		Complete                                     bool
		Artifacts                                    []ClosureArtifact
		Producers                                    []ClosureProducer
		Effects                                      []model.Effect
		Gaps                                         []model.Gap
	}{c.Version, c.SourceDigest, c.ToolchainDigest, c.ContextDigest, c.RuntimeDigest, c.Packages, c.Dependencies, c.Scopes, c.Inventory, c.InventoryComplete, c.Artifacts, c.Producers, c.Effects, c.Gaps}})
}
func (c *Closure) finish(ctx context.Context) error {
	if c.stateDigest != "" && c.privateStateDigest() != c.stateDigest {
		return errors.New("Cargo private launch context changed")
	}
	sort.Slice(c.Artifacts, func(i, j int) bool {
		a, b := c.Artifacts[i], c.Artifacts[j]
		if a.Owner != b.Owner {
			return a.Owner < b.Owner
		}
		return a.Name < b.Name
	})
	sort.Slice(c.Producers, func(i, j int) bool {
		a, b := c.Producers[i], c.Producers[j]
		if a.Owner != b.Owner {
			return a.Owner < b.Owner
		}
		return a.RecordDigest < b.RecordDigest
	})
	sort.Slice(c.Inventory.Units, func(i, j int) bool { return c.Inventory.Units[i].ID < c.Inventory.Units[j].ID })
	sort.Slice(c.Inventory.Cases, func(i, j int) bool {
		a, b := c.Inventory.Cases[i], c.Inventory.Cases[j]
		if a.Unit != b.Unit {
			return a.Unit < b.Unit
		}
		return a.ID < b.ID
	})
	c.Digest = c.currentDigest()
	c.stateDigest = c.privateStateDigest()
	return c.Verify(ctx)
}

// Evidence retains original package scopes, including zero-case packages. Its
// unknown effects broaden even an empty change set. Inventory data cannot remove
// package nodes or manufacture placeholder cases for shared runner validation.
func (c *Closure) Evidence() model.Evidence {
	e := c.evidence
	e.Nodes = append([]string(nil), e.Nodes...)
	e.Units = append([]model.Unit(nil), e.Units...)
	e.Edges = append([]model.Edge(nil), e.Edges...)
	e.Inputs = map[string][]string{}
	for name, owners := range c.evidence.Inputs {
		e.Inputs[name] = append([]string(nil), owners...)
	}
	e.Version = ClosureVersion + ":" + c.Digest
	e.Effects = append([]model.Effect(nil), c.Effects...)
	e.Gaps = append([]model.Gap(nil), c.Gaps...)
	return e
}
func Discover(ctx context.Context, root string, w model.Workspace, execution model.Context) (model.Evidence, error) {
	c, err := ResolveMetadata(ctx, root, w, execution)
	if err != nil {
		return c.evidence, err
	}
	evidence := c.Evidence()
	if err := c.Close(); err != nil {
		return model.Evidence{}, err
	}
	return evidence, nil
}

// ResolveMetadata is shared by Cargo and nextest. It never compiles or executes
// test binaries, so nextest retains its own target/filter/listing semantics.
func ResolveMetadata(ctx context.Context, root string, w model.Workspace, execution model.Context) (c Closure, err error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	defer func() {
		if err != nil && ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	if err = ctx.Err(); err != nil {
		return c, err
	}
	if err = ValidateClosureRequest(w, execution); err != nil {
		return c, err
	}
	c = Closure{Version: ClosureVersion, Inventory: model.ExecutionInventory{Schema: model.ExecutionSchema}, workspace: w, execution: execution}
	c.requestDigest = closureIdentity([]any{w, execution})
	defer func() {
		if err != nil {
			_ = c.Close()
		}
	}()
	if path, ok := execution.Env["PATH"]; ok && path != os.Getenv("PATH") {
		return c, errors.New("Cargo PATH overrides are unsupported")
	}
	if _, err = parse(w); err != nil {
		return c, err
	}
	if !closureText(w.ID) || !config.Relative(w.Root) || !config.Relative(w.Command.Dir) {
		return c, errors.New("invalid Cargo closure scope")
	}
	c.root, err = filepath.Abs(root)
	if err != nil {
		return c, errors.New("Cargo source unavailable")
	}
	c.root, err = filepath.EvalSymlinks(c.root)
	if err != nil {
		return c, errors.New("Cargo source unavailable")
	}
	c.dir = filepath.Join(c.root, filepath.FromSlash(w.Root), filepath.FromSlash(w.Command.Dir))
	c.dir, err = filepath.EvalSymlinks(c.dir)
	if err != nil || !within(c.root, c.dir) {
		return c, errors.New("Cargo cwd escapes snapshot")
	}
	c.SourceDigest, err = snapshot.Digest(ctx, c.root)
	if err != nil {
		return c, errors.New("Cargo requires a bounded regular-file snapshot")
	}
	c.temp, err = os.MkdirTemp("", "hayaku-cargo-closure-")
	if err != nil {
		return c, err
	}
	c.temp, err = filepath.EvalSymlinks(c.temp)
	if err != nil {
		return c, err
	}
	overrides := map[string]string{}
	for k, v := range execution.Env {
		overrides[k] = v
	}
	overrides["CARGO_NET_OFFLINE"] = "true"
	overrides["RUSTUP_AUTO_INSTALL"] = "0"
	overrides["CARGO_TERM_COLOR"] = "never"
	overrides["CARGO_TARGET_DIR"] = filepath.Join(c.temp, "target")
	c.env = process.Environment(overrides)
	if len(c.env) > 1024 {
		return c, errors.New("Cargo environment exceeds bounds")
	}
	remaining := 1 << 20
	for _, v := range c.env {
		if len(v) > remaining || !utf8.ValidString(v) || strings.ContainsRune(v, 0) {
			return c, errors.New("invalid Cargo environment")
		}
		remaining -= len(v)
	}
	tools := map[string]string{}
	unboundActiveTool := false
	compiler, doc := environmentValue(c.env, "RUSTC"), environmentValue(c.env, "RUSTDOC")
	if compiler == "" {
		compiler = "rustc"
	}
	if doc == "" {
		doc = "rustdoc"
	}
	for _, name := range []string{w.Command.Executable, compiler, doc} {
		invocation, sum, err := c.bindInvocation(ctx, name)
		if err != nil {
			return c, err
		}
		if name == w.Command.Executable {
			c.cargoInvocation = invocation
			c.env = pinEnvironment(c.env, "CARGO", invocation)
		}
		if err := c.verifyBindings(ctx); err != nil {
			return c, err
		}
		version, err := process.RunEnvironmentLimits(ctx, model.Command{Executable: invocation, Args: []string{"--version"}}, c.dir, c.env, 8192, 1<<20)
		if err != nil || !version.Completed || !closureText(strings.TrimSpace(string(version.Stdout))) {
			return c, errors.New("installed Cargo toolchain identity unavailable")
		}
		tools[name] = closureIdentity([]string{sum, strings.TrimSpace(string(version.Stdout))})
		if name == compiler {
			c.env = pinEnvironment(c.env, "RUSTC", invocation)
		}
		if name == doc {
			c.env = pinEnvironment(c.env, "RUSTDOC", invocation)
		}
		if filepath.Base(c.invocations[len(c.invocations)-1].resolved) == "rustup" {
			unboundActiveTool = true
		}
	}

	for _, key := range []string{"RUSTC_WRAPPER", "RUSTC_WORKSPACE_WRAPPER"} {
		if name := environmentValue(c.env, key); name != "" {
			invocation, sum, err := c.bindInvocation(ctx, name)
			if err != nil {
				return c, err
			}
			tools[key] = sum
			c.env = pinEnvironment(c.env, key, invocation)
		}
	}

	c.ToolchainDigest = closureIdentity(tools)
	// Private target path bytes are randomized and not semantic context identity.
	boundEnv := append([]string(nil), c.env...)
	for i, v := range boundEnv {
		if strings.HasPrefix(v, "CARGO_TARGET_DIR=") {
			boundEnv[i] = "CARGO_TARGET_DIR=<private-build>"
		}
	}
	c.ContextDigest = closureIdentity(struct {
		Workspace        model.Workspace
		Execution        model.Context
		Environment      []string
		HostOS, HostArch string
	}{w, execution, boundEnv, runtime.GOOS, runtime.GOARCH})
	if err = c.verifyBindings(ctx); err != nil {
		return c, err
	}
	c.evidence, c.metadata, err = discoverMetadata(ctx, c.root, w, execution, c.env, c.cargoInvocation)
	if err != nil {
		return c, err
	}
	c.Scopes = append([]model.Unit(nil), c.evidence.Units...)
	c.Gaps = append([]model.Gap(nil), c.evidence.Gaps...)
	if unboundActiveTool {
		c.Gaps = append(c.Gaps, model.Gap{Code: "cargo-active-tool-unqualified", Workspace: w.ID, Detail: "A captured rustup proxy does not bind its selected active toolchain executable bytes"})
	}
	c.Gaps = append(c.Gaps, model.Gap{Code: "cargo-build-context-unqualified", Workspace: w.ID, Detail: "Ambient Cargo configuration, compiler wrappers, sysroot, linker and native extensions require a complete input and producer model"}, model.Gap{Code: "cargo-inventory-unqualified", Workspace: w.ID, Detail: "Cargo metadata has no harness field; native list output and documentation-test inventory do not establish complete original cases"}, model.Gap{Code: "collection-context-modified", Workspace: w.ID, Detail: "Offline locked collection uses a private target and OUT_DIR; original required execution remains unchanged"})
	for _, unit := range c.Scopes {
		c.Effects = append(c.Effects, model.Effect{Owner: unit.ID, Kind: "unknown"})
	}
	if err = c.describeMetadata(); err != nil {
		return c, err
	}
	owners := []string{}
	for _, unit := range c.Scopes {
		owners = append(owners, unit.ID)
	}
	count := 0
	err = filepath.WalkDir(c.root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		count++
		if count > 50000 {
			return errors.New("Cargo source inventory exceeds bounds")
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("Cargo source contains nonregular input")
		}
		rel, err := filepath.Rel(c.root, name)
		if err != nil || !config.Relative(filepath.ToSlash(rel)) {
			return errors.New("invalid Cargo source identity")
		}
		if len(owners) > 0 && len(c.evidence.Inputs) >= 1000000/len(owners) {
			return errors.New("Cargo source ownership exceeds bounds")
		}
		c.evidence.Inputs[filepath.ToSlash(rel)] = append([]string(nil), owners...)
		return nil
	})
	if err != nil {
		return c, err
	}
	err = c.finish(ctx)
	return c, err
}

func (c *Closure) bindFile(ctx context.Context, name string) (string, error) {
	if len(c.bindings) >= 50000 || c.bytes+c.directoryBytes >= inputbundle.MaxBytes {
		return "", errors.New("Cargo artifact inventory exceeds bounds")
	}
	info, err := os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > inputbundle.MaxFileBytes || info.Size() > inputbundle.MaxBytes-c.bytes-c.directoryBytes {
		return "", errors.New("Cargo artifact is unavailable or exceeds bounds")
	}
	b, err := inputbundle.Capture(ctx, inputbundle.Request{Roots: []inputbundle.Root{{Name: "artifact", Path: filepath.Dir(name), Mount: "."}}, Inputs: []inputbundle.Input{{Root: "artifact", Path: filepath.Base(name)}}, Limits: inputbundle.Limits{Bytes: inputbundle.MaxBytes - c.bytes - c.directoryBytes}})
	if err != nil {
		return "", errors.New("Cargo artifact capture failed")
	}
	m := b.Manifest()
	c.bytes += m.Bytes
	c.bindings = append(c.bindings, closureBinding{name, b})
	for _, entry := range m.Entries {
		if entry.Kind == "file" && entry.Path == filepath.Base(name) {
			return entry.SHA256, nil
		}
	}
	return "", errors.New("Cargo artifact identity missing")
}
func (c *Closure) describeMetadata() error {
	ids := map[string]string{}
	indices := map[string]int{}
	for _, p := range c.metadata.Packages {
		if len(ids) >= 50000 || !closureText(p.ID) || !closureText(p.Name) || !closureText(p.Version) || len(p.Targets) > 50000 {
			return errors.New("invalid Cargo package metadata")
		}
		id := packageIdentity(c.root, c.workspace.ID, p)
		ids[p.ID] = id
		item := ClosurePackage{ID: id, Name: p.Name, Version: p.Version, Manifest: c.sourceName(p.ManifestPath)}
		for _, t := range p.Targets {
			if !closureText(t.Name) || !closureText(t.Source) || len(t.Kind) == 0 || len(t.Kind) > 128 || len(t.CrateTypes) > 128 || len(t.RequiredFeatures) > 10000 {
				return errors.New("invalid Cargo target metadata")
			}
			for _, list := range [][]string{t.Kind, t.CrateTypes, t.RequiredFeatures} {
				for _, s := range list {
					if !closureText(s) {
						return errors.New("invalid Cargo target identity")
					}
				}
			}
			item.Targets = append(item.Targets, ClosureTarget{t.Name, c.sourceName(t.Source), append([]string(nil), t.Kind...), append([]string(nil), t.CrateTypes...), append([]string(nil), t.RequiredFeatures...), t.Test, t.Doctest})
		}
		sort.Slice(item.Targets, func(i, j int) bool {
			a, b := item.Targets[i], item.Targets[j]
			if a.Name != b.Name {
				return a.Name < b.Name
			}
			return strings.Join(a.Kind, ",") < strings.Join(b.Kind, ",")
		})
		for _, t := range item.Targets {
			if strings.HasPrefix(t.Source, "external:") {
				c.Gaps = append(c.Gaps, model.Gap{Code: "cargo-external-target-source", Workspace: c.workspace.ID, Detail: "A target source is outside the bounded snapshot"})
			}
		}
		indices[id] = len(c.Packages)
		c.Packages = append(c.Packages, item)
	}
	edges := 0
	for _, node := range c.metadata.Resolve.Nodes {
		id := ids[node.ID]
		if id == "" || len(node.Features) > 10000 {
			return errors.New("invalid Cargo resolved features")
		}
		for _, s := range node.Features {
			if !closureText(s) {
				return errors.New("invalid Cargo feature identity")
			}
		}
		i := indices[id]
		c.Packages[i].Features = append([]string(nil), node.Features...)
		sort.Strings(c.Packages[i].Features)

		for _, dep := range node.Deps {
			if ids[dep.Package] == "" || !closureText(dep.Name) || len(dep.Kinds) == 0 {
				return errors.New("invalid Cargo renamed dependency metadata")
			}
			for _, kind := range dep.Kinds {
				edges++
				if edges > 1000000 {
					return errors.New("Cargo dependency metadata exceeds bounds")
				}
				k, p := "normal", ""
				if kind.Kind != nil {
					k = *kind.Kind
				}
				if kind.Platform != nil {
					p = *kind.Platform
				}
				if k != "normal" && k != "dev" && k != "build" || p != "" && !closureText(p) {
					return errors.New("invalid Cargo dependency kind/platform")
				}
				c.Dependencies = append(c.Dependencies, ClosureDependency{ids[dep.Package], id, dep.Name, k, p})
			}
		}
	}
	sort.Slice(c.Packages, func(i, j int) bool { return c.Packages[i].ID < c.Packages[j].ID })
	sort.Slice(c.Dependencies, func(i, j int) bool {
		a, b := c.Dependencies[i], c.Dependencies[j]
		if a.From != b.From {
			return a.From < b.From
		}
		if a.To != b.To {
			return a.To < b.To
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Platform < b.Platform
	})
	return nil
}
func packageIdentity(root, workspace string, p pkg) string {
	if p.Source == nil && within(root, p.ManifestPath) {
		rel, _ := filepath.Rel(root, filepath.Dir(p.ManifestPath))
		return workspace + ":" + filepath.ToSlash(rel) + ":" + p.Name + "@" + p.Version
	}
	sum := sha256.Sum256([]byte(p.ID))
	return workspace + ":external:" + hex.EncodeToString(sum[:])
}
func (c *Closure) sourceName(name string) string {
	if within(c.root, name) {
		rel, _ := filepath.Rel(c.root, name)
		return filepath.ToSlash(rel)
	}
	return "external:" + closureIdentity(name)
}

// ResolveClosure compiles in a private target and observes binary lists. Listing
// can execute arbitrary custom harnesses and producers; it supplies no sandbox,
// network denial, native-extension qualification or doctest completeness.
func ResolveClosure(ctx context.Context, root string, w model.Workspace, execution model.Context) (c Closure, err error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	defer func() {
		if err != nil && ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	c, err = ResolveMetadata(ctx, root, w, execution)
	if err != nil {
		return c, err
	}
	defer func() {
		if err != nil {
			_ = c.Close()
		}
	}()
	args := append([]string{"test", "--no-run", "--message-format=json", "--target-dir", filepath.Join(c.temp, "target")}, w.Command.Args[1:]...)
	flags := map[string]bool{}
	for _, arg := range w.Command.Args {
		flags[arg] = true
	}
	if !flags["--offline"] && !flags["--frozen"] {
		args = append(args, "--offline")
	}
	if !flags["--locked"] && !flags["--frozen"] {
		args = append(args, "--locked")
	}
	output, err := c.RunNative(ctx, args)
	if err != nil || !output.Completed || output.ExitCode != 0 {
		return c, errors.New("offline locked Cargo closure build failed; diagnostics withheld")
	}
	if err = c.consumeBuild(ctx, output.Stdout); err != nil {
		return c, err
	}
	err = c.finish(ctx)
	return c, err
}
func (c *Closure) consumeBuild(ctx context.Context, data []byte) error {
	ids := map[string]string{}
	targets := map[string]map[string]bool{}
	selected := map[string]model.Unit{}
	for _, p := range c.metadata.Packages {
		ids[p.ID] = packageIdentity(c.root, c.workspace.ID, p)
		targets[p.ID] = map[string]bool{}
		for _, t := range p.Targets {
			targets[p.ID][closureIdentity([]any{t.Name, t.Kind, t.CrateTypes, t.Source})] = true
		}
	}
	for _, u := range c.Scopes {
		selected[u.ID] = u
	}
	binaries := []closureBinary{}
	seenFiles := map[string]bool{}
	cases := map[[2]string]bool{}
	caseOwners := map[string]bool{}
	decoder := json.NewDecoder(bytes.NewReader(data))
	records := 0
	finished := false
	for {
		var record struct {
			Reason  string `json:"reason"`
			Package string `json:"package_id"`
			Target  target `json:"target"`
			Profile struct {
				Test bool `json:"test"`
			} `json:"profile"`
			Files      []string `json:"filenames"`
			Executable *string  `json:"executable"`
			OutDir     string   `json:"out_dir"`
			Linked     []string `json:"linked_paths"`
			Success    bool     `json:"success"`
		}
		var raw json.RawMessage
		err := decoder.Decode(&raw)
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("invalid Cargo build protocol")
		}
		records++
		if records > 100000 || json.Unmarshal(raw, &record) != nil {
			return errors.New("Cargo build protocol exceeds bounds")
		}
		if record.Reason == "build-finished" {
			if finished || !record.Success {
				return errors.New("Cargo build did not finish successfully")
			}
			finished = true
			continue
		}
		if record.Reason != "compiler-artifact" && record.Reason != "build-script-executed" {
			continue
		}
		owner := ids[record.Package]
		if owner == "" {
			return errors.New("Cargo artifact has unknown package owner")
		}
		if record.Reason == "build-script-executed" {
			if len(record.Linked) > 1024-len(c.linkedDirectories) {
				return errors.New("Cargo linked-path inventory exceeds bounds")
			}
			for _, link := range record.Linked {
				_, name, has := strings.Cut(link, "=")
				if !has {
					name = link
				}
				if !closureText(name) {
					return errors.New("invalid Cargo linked search path")
				}
				if !filepath.IsAbs(name) || !within(c.PrivateTargetDirectory(), name) {
					c.Gaps = append(c.Gaps, model.Gap{Code: "cargo-external-native-link-path", Workspace: c.workspace.ID, Detail: "Native link search outside the private target remains unresolved"})
					continue
				}
				c.linkedDirectories = append(c.linkedDirectories, name)
			}
			if _, err := c.CaptureProducer(ctx, owner, record.OutDir, raw); err != nil {
				return err
			}
			continue
		}

		if !closureText(record.Target.Name) || len(record.Files) > 50000 || !targets[record.Package][closureIdentity([]any{record.Target.Name, record.Target.Kind, record.Target.CrateTypes, record.Target.Source})] {
			return errors.New("invalid Cargo artifact identity")
		}
		for _, name := range record.Files {
			if !filepath.IsAbs(name) || !within(filepath.Join(c.temp, "target"), name) {
				return errors.New("Cargo artifact escapes private target")
			}
			if seenFiles[name] {
				continue
			}
			seenFiles[name] = true
			sum, err := c.CaptureArtifact(ctx, name)
			if err != nil {
				return err
			}
			kind := "compiler-output"
			if record.Profile.Test {
				kind = "test-binary"
			}
			c.Artifacts = append(c.Artifacts, ClosureArtifact{Owner: owner, Name: record.Target.Name + ":" + filepath.Base(name), Kind: kind, Digest: sum})
		}
		if !record.Profile.Test || record.Executable == nil || selected[owner].ID == "" {
			continue
		}
		name := *record.Executable
		if !seenFiles[name] {
			return errors.New("Cargo test executable is not a captured artifact")
		}
		binaries = append(binaries, closureBinary{owner, name, record.Target})
	}
	if !finished {
		return errors.New("incomplete Cargo build protocol")
	}
	cross := c.execution.OS != "" && c.execution.OS != runtime.GOOS || c.execution.Arch != "" && c.execution.Arch != runtime.GOARCH
	for _, arg := range c.workspace.Command.Args {
		if arg == "--target" || strings.HasPrefix(arg, "--target=") {
			cross = true
		}
	}
	c.stateDigest = c.privateStateDigest()
	if len(binaries) > 0 && !cross {
		if err := c.prepareNativeLibraries(ctx); err != nil {
			return err
		}
	}
	for _, record := range binaries {
		owner, name := record.Owner, record.Name

		if cross {
			continue
		}
		if err := c.verifyBindings(ctx); err != nil {
			return err
		}
		listEnv, err := c.nativeListEnvironment(name)
		if err != nil {
			return err
		}
		out, err := process.RunEnvironment(ctx, model.Command{Executable: name, Args: []string{"--list", "--format=terse"}}, c.dir, listEnv)
		if err != nil || !out.Completed || out.ExitCode != 0 {
			return errors.New("Cargo native case collection failed; diagnostics withheld")
		}
		listed, err := parseCases(out.Stdout)
		if err != nil {
			return err
		}
		binaryID := record.Target.Name + ":" + strings.Join(record.Target.Kind, ",")
		for _, test := range listed {
			id := binaryID + "::" + test
			key := [2]string{owner, id}
			if cases[key] || len(cases) >= 100000 {
				return errors.New("duplicate Cargo native case or inventory limit")
			}
			cases[key] = true
			caseOwners[owner] = true
			c.Inventory.Cases = append(c.Inventory.Cases, model.Case{Unit: owner, ID: id})
		}
		for i := range c.Artifacts {
			if c.Artifacts[i].Owner == owner && c.Artifacts[i].Name == record.Target.Name+":"+filepath.Base(name) {
				c.Artifacts[i].Cases = listed
				c.Artifacts[i].ContextDigest = closureIdentity([]any{c.dir, listEnv, c.RuntimeDigest})
			}
		}
	}
	for _, unit := range c.Scopes {
		if caseOwners[unit.ID] {
			c.Inventory.Units = append(c.Inventory.Units, unit)
		} else {
			c.Gaps = append(c.Gaps, model.Gap{Code: "cargo-zero-observed-cases", Workspace: c.workspace.ID, Detail: "A selected package has no observed native cases; its source and dependency scope remains retained"})
		}
	}
	if len(c.Inventory.Units) > 0 && runner.ValidateInventory(c.Inventory) != nil {
		return errors.New("invalid shared Cargo case inventory")
	}
	// Even plausible --list output cannot prove a custom harness complete, and
	// --no-run supplies no configured rustdoc/doctest list. Never set true here.
	c.InventoryComplete = false
	return nil
}
func parseCases(data []byte) ([]string, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("invalid Cargo native list encoding")
	}
	result := []string{}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		name, ok := strings.CutSuffix(line, ": test")
		if !ok {
			name, ok = strings.CutSuffix(line, ": benchmark")
		}
		if !ok {
			return nil, errors.New("unsupported Cargo native harness list format")
		}
		if !closureText(name) || seen[name] || len(result) >= 100000 {
			return nil, errors.New("invalid Cargo native case identity")
		}
		seen[name] = true
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

func environmentValue(env []string, key string) string {
	for _, v := range env {
		if k, value, ok := strings.Cut(v, "="); ok && k == key {
			return value
		}
	}
	return ""
}

// PrivateTargetDirectory is for the internal native adapter boundary only. It
// is never serialized or an original-path equivalence claim.
func (c *Closure) PrivateTargetDirectory() string { return filepath.Join(c.temp, "target") }

// RunNative keeps every stage on the same captured environment and Cargo CLI.
func (c *Closure) RunNative(ctx context.Context, args []string) (process.Output, error) {
	if c.stateDigest != "" && c.privateStateDigest() != c.stateDigest {
		return process.Output{}, errors.New("Cargo private launch context changed")
	}
	if err := c.verifyBindings(ctx); err != nil {
		return process.Output{}, err
	}
	return process.RunEnvironment(ctx, model.Command{Executable: c.cargoInvocation, Args: args}, c.dir, c.env)
}
func (c *Closure) CaptureArtifact(ctx context.Context, name string) (string, error) {
	if err := c.privateOutput(name); err != nil {
		return "", err
	}
	return c.bindFile(ctx, name)
}
func (c *Closure) privateOutput(name string) error {
	if c.temp == "" || !filepath.IsAbs(name) || !within(c.PrivateTargetDirectory(), name) {
		return errors.New("Cargo output escapes private target")
	}
	resolved, err := filepath.EvalSymlinks(name)
	if err != nil || resolved != filepath.Clean(name) || !within(c.PrivateTargetDirectory(), resolved) {
		return errors.New("Cargo output aliases outside private target")
	}
	return nil
}
func (c *Closure) CaptureProducer(ctx context.Context, owner, name string, record []byte) (ClosureProducer, error) {
	result := ClosureProducer{}
	if err := c.privateOutput(name); err != nil {
		return result, err
	}
	found := false
	for _, p := range c.Packages {
		found = found || p.ID == owner
	}
	if !found || len(record) > process.MaxOutput || len(c.bindings) >= 50000 || c.bytes+c.directoryBytes >= inputbundle.MaxBytes {
		return result, errors.New("Cargo producer identity or bounds invalid")
	}
	b, err := inputbundle.Capture(ctx, inputbundle.Request{Roots: []inputbundle.Root{{Name: "producer", Path: name, Mount: "."}}, Inputs: []inputbundle.Input{{Root: "producer", Path: "."}}, Limits: inputbundle.Limits{Bytes: inputbundle.MaxBytes - c.bytes - c.directoryBytes}})
	if err != nil {
		return result, errors.New("Cargo producer outputs unavailable or exceed bounds")
	}
	c.bytes += b.Manifest().Bytes
	c.bindings = append(c.bindings, closureBinding{name, b})
	result = ClosureProducer{owner, closureIdentity(record), b.Manifest().Digest}
	c.Producers = append(c.Producers, result)
	return result, nil
}

// FinalizeObservations binds additional descriptive artifacts from nextest. It
// never supplies provider qualification or enables InventoryComplete.
func (c *Closure) FinalizeObservations(ctx context.Context) error {
	c.InventoryComplete = false
	return c.finish(ctx)
}

// CaptureTool binds an installed runner extension without admitting its effects.
func (c *Closure) CaptureTool(ctx context.Context, name string) (string, error) {
	_, sum, err := c.bindInvocation(ctx, name)
	return sum, err
}
func (c *Closure) bindInvocation(ctx context.Context, name string) (string, string, error) {
	if c.stateDigest != "" && c.privateStateDigest() != c.stateDigest {
		return "", "", errors.New("Cargo private launch context changed")
	}
	path := name
	if !filepath.IsAbs(path) && strings.ContainsAny(path, "/\\") {
		path = filepath.Join(c.dir, path)
	} else if !filepath.IsAbs(path) {
		if environmentValue(c.env, "PATH") != os.Getenv("PATH") {
			return "", "", errors.New("Cargo tool resolution PATH changed")
		}
		var err error
		path, err = exec.LookPath(name)
		if err != nil {
			return "", "", errors.New("installed Cargo tool unavailable")
		}
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", "", errors.New("installed Cargo tool unavailable")
	}
	before, err := os.Lstat(path)
	if err != nil {
		return "", "", errors.New("installed Cargo tool unavailable")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", "", errors.New("installed Cargo tool unavailable")
	}
	sum, err := c.bindFile(ctx, resolved)
	if err != nil {
		return "", "", err
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return "", "", errors.New("Cargo tool invocation changed during capture")
	}
	c.invocations = append(c.invocations, closureInvocation{path, resolved, before})
	if c.toolInvocations == nil {
		c.toolInvocations = map[string]string{}
	}
	c.toolInvocations[name] = path
	if c.stateDigest != "" {
		c.stateDigest = c.privateStateDigest()
	}
	return path, closureIdentity([]string{path, resolved, sum}), nil
}

// NativeOwner reconciles a native package/target/cwd with retained Cargo data.
// It returns only the sanitized owner identity, never a host path.
func (c *Closure) NativeOwner(packageID, name, targetName, kind, cwd string) (string, error) {
	for _, p := range c.metadata.Packages {
		if p.ID != packageID {
			continue
		}
		if p.Name != name || filepath.Clean(cwd) != filepath.Dir(p.ManifestPath) {
			return "", errors.New("native Rust package/cwd identity mismatch")
		}
		for _, t := range p.Targets {
			if t.Name == targetName {
				for _, k := range t.Kind {
					if k == kind {
						return packageIdentity(c.root, c.workspace.ID, p), nil
					}
				}
			}
		}
	}
	return "", errors.New("native Rust target identity mismatch")
}
func (c *Closure) PackageOwner(packageID string) string {
	for _, p := range c.metadata.Packages {
		if p.ID == packageID {
			return packageIdentity(c.root, c.workspace.ID, p)
		}
	}
	return ""
}

func pinEnvironment(env []string, key, value string) []string {
	for i, v := range env {
		if k, _, ok := strings.Cut(v, "="); ok && k == key {
			env[i] = key + "=" + value
			return env
		}
	}
	env = append(env, key+"="+value)
	sort.Strings(env)
	return env
}
func (c *Closure) privateStateDigest() string {
	return closureIdentity(struct {
		Root, Dir, Temp, Cargo string
		Env                    []string
		Invocations            map[string]string
		Workspace              model.Workspace
		Execution              model.Context
		Request                string
		Library, Query         string
		Linked                 []string
		Directories            []string
	}{c.root, c.dir, c.temp, c.cargoInvocation, c.env, c.toolInvocations, c.workspace, c.execution, c.requestDigest, c.libraryDirectory, c.libraryQuery, c.linkedDirectories, c.directoryIdentities()})
}

// RunExtension uses a captured plugin directly with Cargo's documented external
// subcommand argv protocol. It does not re-resolve a plugin through PATH.
func (c *Closure) RunExtension(ctx context.Context, name string, args []string) (process.Output, error) {
	if c.stateDigest != "" && c.privateStateDigest() != c.stateDigest {
		return process.Output{}, errors.New("Cargo private launch context changed")
	}
	invocation := c.toolInvocations[name]
	if invocation == "" {
		return process.Output{}, errors.New("Cargo extension invocation unavailable")
	}
	if err := c.verifyBindings(ctx); err != nil {
		return process.Output{}, err
	}
	return process.RunEnvironment(ctx, model.Command{Executable: invocation, Args: args}, c.dir, c.env)
}
