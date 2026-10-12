package swift

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/inputbundle"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/runner"
	"github.com/Cyberlane/hayaku/internal/runner/native"
	"github.com/Cyberlane/hayaku/internal/snapshot"
)

const AppleClosureVersion = "apple-observed-closure-v4-owned-imports"

// maxAppleToolBytes bounds the total size of distinct producer executables
// hashed for one closure. Each tool still has to fit inputbundle.MaxBytes.
const maxAppleToolBytes int64 = 4 << 30

type appleTool struct {
	invocation, resolved, digest string
	alias, file                  os.FileInfo
}
type appleCopy struct {
	source, destination string
	bytes               int64
}

type appleTree struct {
	bundle                 *inputbundle.Bundle
	path, resolved, digest string
	alias, root            os.FileInfo
	observed, semantic     string
}

// AppleClosure binds observed producer inputs. It never proves native influence
// completeness, a historical write absence, or permission to omit tests.
// Paths and effective environment remain private; only digest identities escape.
type AppleClosure struct {
	copy                       *appleCopy
	outputs                    map[string]appleOutput
	producers                  map[string]bool
	stages                     []string
	unusable                   bool
	sdkAlias, sdkRoot          os.FileInfo
	sourceAlias, sourceRoot    os.FileInfo
	sourceInvocation           string
	sdkResolved                string
	root, dir, source, request string
	sdk                        string
	workspace                  model.Workspace
	execution                  model.Context
	env                        []string
	tools                      map[string]appleTool
	trees                      map[string]appleTree
	bytes, toolBytes           int64
	state                      string
}

func appleDigest(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func appleText(value string) bool {
	return len(value) <= 4096 && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func CaptureAppleClosure(ctx context.Context, root string, w model.Workspace, c model.Context) (*AppleClosure, error) {
	if err := preflightAppleRequest(w, c); err != nil {
		return nil, err
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	sourceAlias, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	dir, err := filepath.EvalSymlinks(filepath.Join(resolved, w.Root, w.Command.Dir))
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(resolved, dir)
	if err != nil || !config.Relative(filepath.ToSlash(rel)) {
		return nil, errors.New("Apple working directory escapes source")
	}
	sourceRoot, err := os.Lstat(resolved)
	if err != nil {
		return nil, err
	}
	source, err := snapshot.Digest(ctx, resolved)
	if err != nil {
		return nil, err
	}
	// JSON cloning prevents retained caller slices and maps from changing launch.
	data, _ := json.Marshal(w)
	var private model.Workspace
	if json.Unmarshal(data, &private) != nil {
		return nil, errors.New("invalid Apple workspace")
	}
	data, _ = json.Marshal(c)
	var execution model.Context
	if json.Unmarshal(data, &execution) != nil {
		return nil, errors.New("invalid Apple context")
	}
	env := process.Environment(c.Env)
	count := 0
	for _, pair := range env {
		count += len(pair)
		if !utf8.ValidString(pair) || strings.ContainsRune(pair, 0) {
			return nil, errors.New("invalid effective Apple environment")
		}
	}
	if len(env) > 4096 || count > 1<<20 {
		return nil, errors.New("effective Apple environment exceeds bounds")
	}
	closure := &AppleClosure{root: resolved, dir: dir, source: source, workspace: private, execution: execution, env: env, tools: map[string]appleTool{}, trees: map[string]appleTree{}, outputs: map[string]appleOutput{}, producers: map[string]bool{}}
	closure.sourceInvocation, closure.sourceAlias, closure.sourceRoot = root, sourceAlias, sourceRoot
	closure.request = closure.requestDigest()
	closure.state = closure.Digest()
	if err := closure.Verify(ctx); err != nil {
		return nil, err
	}
	return closure, nil
}
func (c *AppleClosure) requestDigest() string {
	return appleDigest([]any{AppleClosureVersion, c.root, c.dir, c.workspace, c.execution, c.env, c.sdk})
}
func sameAppleFile(a, b os.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) && a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// ResolveTool uses the frozen PATH, preserving the lexical basename required by
// Apple's dispatch tools. Every returned path must subsequently be captured.
func (c *AppleClosure) ResolveTool(name string) (string, error) {
	if filepath.IsAbs(name) {
		return filepath.Clean(name), nil
	}
	if strings.ContainsAny(name, "/\\") || !appleText(name) || name == "" {
		return "", errors.New("invalid Apple tool")
	}
	path := ""
	for _, pair := range c.env {
		if strings.HasPrefix(pair, "PATH=") {
			path = strings.TrimPrefix(pair, "PATH=")
		}
	}
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			return candidate, nil
		}
	}
	return "", errors.New("installed Apple tool is unavailable")
}
func (c *AppleClosure) CaptureTool(ctx context.Context, name string) (string, error) {
	if err := c.Verify(ctx); err != nil {
		return "", err
	}
	invocation, err := c.ResolveTool(name)
	if err != nil {
		return "", err
	}
	if _, ok := c.tools[invocation]; ok {
		return invocation, nil
	}
	if len(c.tools) >= 32 {
		return "", errors.New("Apple tool count exceeds bounds")
	}
	alias, err := os.Lstat(invocation)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(invocation)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return "", errors.New("Apple producer is not an executable file")
	}
	seen := false
	for _, tool := range c.tools {
		if tool.resolved == resolved {
			seen = true
		}
	}
	// Tools are stream-hashed, never retained, so they have their own budget
	// and do not consume the in-memory tree and copy allowance in c.bytes.
	if !seen && (info.Size() < 0 || info.Size() > inputbundle.MaxBytes || info.Size() > maxAppleToolBytes-c.toolBytes) {
		return "", errors.New("Apple producer byte closure exceeds bounds")
	}
	digest, err := snapshot.FileDigest(ctx, resolved, inputbundle.MaxBytes)
	if err != nil {
		return "", err
	}
	c.tools[invocation] = appleTool{invocation, resolved, digest, alias, info}
	if !seen {
		c.toolBytes += info.Size()
	}
	c.state = c.Digest()
	if err := c.Verify(ctx); err != nil {
		return "", err
	}
	return invocation, nil
}
func (c *AppleClosure) verifyTools(ctx context.Context) error {
	return verifyAppleTools(ctx, c.tools)
}
func verifyAppleTools(ctx context.Context, tools map[string]appleTool) error {
	checked := map[string]string{}
	for _, tool := range tools {
		if err := tool.verifyIdentity(); err != nil {
			return err
		}
		digest, checkedBefore := checked[tool.resolved]
		if !checkedBefore {
			var err error
			digest, err = snapshot.FileDigest(ctx, tool.resolved, inputbundle.MaxBytes)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return errors.New("Apple producer bytes changed")
			}
			checked[tool.resolved] = digest
		}
		if digest != tool.digest {
			return errors.New("Apple producer bytes changed")
		}
	}
	for _, tool := range tools {
		if err := tool.verifyIdentity(); err != nil {
			return err
		}
	}
	return ctx.Err()
}
func (t appleTool) verifyIdentity() error {
	alias, err := os.Lstat(t.invocation)
	resolved, resolveErr := filepath.EvalSymlinks(t.invocation)
	info, infoErr := os.Lstat(t.resolved)
	if err != nil || resolveErr != nil || infoErr != nil || resolved != t.resolved || !sameAppleFile(alias, t.alias) || !sameAppleFile(info, t.file) {
		return errors.New("Apple producer identity changed")
	}
	return nil
}
func (c *AppleClosure) Verify(ctx context.Context) error { return c.verify(ctx, nil) }
func (c *AppleClosure) verify(ctx context.Context, produced map[string]bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil || c.unusable || c.request != c.requestDigest() || c.state != c.Digest() {
		return errors.New("Apple private request changed")
	}
	sourceAlias, err := os.Lstat(c.sourceInvocation)
	sourceResolved, resolveErr := filepath.EvalSymlinks(c.sourceInvocation)
	sourceRoot, infoErr := os.Lstat(c.root)
	if err != nil || resolveErr != nil || infoErr != nil || sourceResolved != c.root || !sameAppleFile(sourceAlias, c.sourceAlias) || !sameAppleFile(sourceRoot, c.sourceRoot) {
		return errors.New("Apple source root identity changed")
	}
	dir, err := filepath.EvalSymlinks(filepath.Join(c.root, c.workspace.Root, c.workspace.Command.Dir))
	if err != nil || dir != c.dir {
		return errors.New("Apple working directory changed")
	}
	source, err := snapshot.Digest(ctx, c.root)
	if err != nil || source != c.source {
		return errors.New("Apple source inputs changed")
	}
	if c.sdk == "" {
		if err := c.verifyTools(ctx); err != nil {
			return err
		}
	} else {
		dispatcher, captured := c.tools["/usr/bin/xcrun"]
		if !captured {
			return errors.New("Apple SDK dispatcher was not captured")
		}
		// Only this fixed internal query runs before the full tool gate. Its
		// exact dispatcher bytes are checked now; every role is checked after
		// the query and before any requested native command can launch.
		if err := verifyAppleTools(ctx, map[string]appleTool{dispatcher.invocation: dispatcher}); err != nil {
			return err
		}
		alias, err := os.Lstat(c.sdk)
		resolved, resolveErr := filepath.EvalSymlinks(c.sdk)
		info, infoErr := os.Lstat(c.sdkResolved)
		if err != nil || resolveErr != nil || infoErr != nil || resolved != c.sdkResolved || !sameAppleFile(alias, c.sdkAlias) || !sameAppleFile(info, c.sdkRoot) {
			return errors.New("Apple selected SDK identity changed")
		}
		// Directly use the already verified dispatcher to avoid recursive Verify.
		out, err := process.RunEnvironmentLimits(ctx, model.Command{Executable: "/usr/bin/xcrun", Args: []string{"--show-sdk-path"}}, c.dir, c.env, 8192, 8192)
		if err != nil || strings.TrimSpace(string(out.Stdout)) != c.sdk {
			return errors.New("Apple selected SDK changed")
		}
		if err := c.verifyTools(ctx); err != nil {
			return err
		}
		afterAlias, aliasErr := os.Lstat(c.sdk)
		afterResolved, resolveErr := filepath.EvalSymlinks(c.sdk)
		afterRoot, rootErr := os.Lstat(c.sdkResolved)
		if aliasErr != nil || resolveErr != nil || rootErr != nil || afterResolved != c.sdkResolved || !sameAppleFile(afterAlias, c.sdkAlias) || !sameAppleFile(afterRoot, c.sdkRoot) {
			return errors.New("Apple SDK query changed selected identity")
		}

		source, err := snapshot.Digest(ctx, c.root)
		if err != nil || source != c.source {
			return errors.New("Apple SDK query changed source")
		}
	}

	for name, tree := range c.trees {
		if produced[name] {
			continue
		}
		alias, err := os.Lstat(tree.path)
		resolved, resolveErr := filepath.EvalSymlinks(tree.path)
		info, infoErr := os.Lstat(tree.resolved)
		digestErr := tree.bundle.VerifySources(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		digest := tree.bundle.Manifest().Digest
		if tree.observed != "" {
			scan, scanErr := scanAppleArtifact(ctx, tree.resolved, inputbundle.MaxBytes)
			if scanErr != nil {
				return scanErr
			}
			if scan.physical != tree.observed {
				return errors.New("Apple observed artifact topology changed")
			}
			digest = appleDigest([]any{digest, scan.physical})
		}
		if err != nil || resolveErr != nil || infoErr != nil || digestErr != nil || resolved != tree.resolved || !sameAppleFile(alias, tree.alias) || !sameAppleFile(info, tree.root) || digest != tree.digest {
			return errors.New("Apple captured tree changed")
		}
	}
	if c.copy != nil {
		tree, exists := c.trees[c.copy.source]
		if !exists || tree.observed != "" || c.outputPath(c.copy.destination) != nil {
			return errors.New("Apple pending input copy changed")
		}
		if err := tree.bundle.VerifyMaterialization(ctx, c.copy.destination); err != nil {
			return err
		}
	}
	for _, output := range c.outputs {
		if err := output.verify(); err != nil {
			return err
		}
	}
	return ctx.Err()
}
func (c *AppleClosure) Run(ctx context.Context, tool string, args []string) (process.Output, error) {
	if _, ok := c.tools[tool]; !ok {
		return process.Output{}, errors.New("Apple invocation is not captured")
	}
	if len(args) > 8192 {
		return process.Output{}, errors.New("Apple launch exceeds bounds")
	}
	size := 0
	for _, arg := range args {
		size += len(arg)
		if !appleText(arg) {
			return process.Output{}, errors.New("invalid Apple launch argument")
		}
	}
	if size > 1<<20 {
		return process.Output{}, errors.New("Apple launch exceeds bounds")
	}
	if err := c.Verify(ctx); err != nil {
		return process.Output{}, err
	}
	out, runErr := process.RunEnvironment(ctx, model.Command{Executable: tool, Args: append([]string(nil), args...)}, c.dir, c.env)
	if err := c.Verify(ctx); err != nil {
		out.Completed = false
		return out, err
	}
	return out, runErr
}

// CaptureTree retains generated producer bytes or an admitted external runtime
// tree. An oversized SDK is an explicit gap at the adapter, never a version proof.
func (c *AppleClosure) CaptureTree(ctx context.Context, path string) error {
	if err := c.Verify(ctx); err != nil {
		return err
	}
	if _, exists := c.trees[path]; exists {
		return nil
	}
	if err := c.captureTree(ctx, path); err != nil {
		return err
	}
	return c.Verify(ctx)
}

// captureTree is used only between an enclosing pre/post identity gate. Native
// producer completion gets a full final Verify after every new tree is bound.
func (c *AppleClosure) captureTree(ctx context.Context, path string) error {
	if len(c.trees) >= 16 || !filepath.IsAbs(path) || !appleText(path) {
		return errors.New("Apple tree request exceeds bounds")
	}
	alias, err := os.Lstat(path)
	if err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.IsDir() {
		return errors.New("Apple tree must be a real directory")
	}
	count, total := 0, int64(0)
	err = filepath.WalkDir(resolved, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		count++
		if count > inputbundle.MaxEntries {
			return errors.New("Apple tree count exceeds bounds")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			if info.Size() < 0 || info.Size() > inputbundle.MaxFileBytes || info.Size() > inputbundle.MaxBytes-c.bytes-total {
				return errors.New("Apple aggregate tree bytes exceed bounds")
			}
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		return err
	}
	remaining := inputbundle.MaxBytes - c.bytes
	if remaining == 0 {
		remaining = 1
	}
	declaredRoot := path
	if alias.Mode()&os.ModeSymlink != 0 {
		declaredRoot = resolved
	}
	// Preserve a real declared root's OS parent alias so inputbundle can bind
	// absolute internal native links through its existing exact alias mapping.
	bundle, err := inputbundle.Capture(ctx, inputbundle.Request{Roots: []inputbundle.Root{{Name: "producer", Path: declaredRoot, Mount: "."}}, Inputs: []inputbundle.Input{{Root: "producer", Path: ".", Generated: true}}, Limits: inputbundle.Limits{Bytes: remaining}})
	if err != nil {
		return err
	}
	if bundle.Manifest().Bytes != total {
		return errors.New("Apple generated tree changed during capture")
	}
	c.bytes += total
	c.trees[path] = appleTree{bundle: bundle, path: path, resolved: resolved, digest: bundle.Manifest().Digest, alias: alias, root: info}
	c.state = c.Digest()
	return nil
}
func (c *AppleClosure) Digest() string {
	tools := map[string]string{}
	for name, tool := range c.tools {
		tools[name] = appleDigest([]any{tool.invocation, tool.resolved, tool.digest, appleInfo(tool.alias), appleInfo(tool.file)})
	}
	trees := map[string]string{}
	for name, tree := range c.trees {
		trees[name] = appleDigest([]any{tree.path, tree.resolved, tree.digest, tree.observed, tree.semantic, tree.bundle.Manifest().Digest, appleInfo(tree.alias), appleInfo(tree.root)})
	}
	outputs := map[string]any{}
	for name, output := range c.outputs {
		outputs[name] = []any{output.resolved, appleInfo(output.info)}
	}
	var copied any
	if c.copy != nil {
		copied = []any{c.copy.source, c.copy.destination, c.copy.bytes}
	}
	return appleDigest([]any{c.request, c.source, tools, trees, outputs, copied, c.producers, c.stages, c.unusable, c.bytes, c.toolBytes, c.sdkResolved, appleInfo(c.sdkAlias), appleInfo(c.sdkRoot), c.sourceInvocation, appleInfo(c.sourceAlias), appleInfo(c.sourceRoot)})
}

// ReadAppleFile reuses immutable bounded input capture, then reads its private
// verified copy with a sentinel limit. Source and copy are checked independently.
func ReadAppleFile(ctx context.Context, name string, limit int64) (data []byte, err error) {
	if !filepath.IsAbs(name) || limit <= 0 || limit > inputbundle.MaxFileBytes {
		return nil, errors.New("invalid Apple document limit")
	}
	bundle, err := inputbundle.Capture(ctx, inputbundle.Request{Roots: []inputbundle.Root{{Name: "document", Path: filepath.Dir(name), Mount: "."}}, Inputs: []inputbundle.Input{{Root: "document", Path: filepath.Base(name)}}, Limits: inputbundle.Limits{FileBytes: limit, Bytes: limit}})
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "hayaku-apple-document-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if removeErr := os.RemoveAll(tmp); removeErr != nil {
			data = nil
			err = errors.New("Apple private document cleanup failed")
		}
	}()
	copy := filepath.Join(tmp, "view")
	if err := bundle.Materialize(ctx, copy); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(copy, filepath.Base(name)), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	admitted := int64(0)
	for _, entry := range bundle.Manifest().Entries {
		if entry.Kind == "file" {
			admitted = entry.Size
		}
	}
	data, err = io.ReadAll(io.LimitReader(file, admitted+1))
	if err != nil || int64(len(data)) != admitted {
		return nil, errors.New("Apple document exceeds bounds")
	}
	if err := bundle.VerifyMaterialization(ctx, copy); err != nil {
		return nil, err
	}
	if err := bundle.VerifySources(ctx); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

// SelectAppleTool binds Apple's dispatcher and the selected producer invocation.
// SDK/framework and secondary producer discovery is descriptive, never complete.
func (c *AppleClosure) SelectAppleTool(ctx context.Context, name string) (string, error) {
	dispatcher := "/usr/bin/xcrun"
	if _, captured := c.tools[dispatcher]; !captured {
		var err error
		dispatcher, err = c.CaptureTool(ctx, dispatcher)
		if err != nil {
			return "", err
		}
	}
	// Run checks all retained invocation and source identities before/after
	// dispatch; an existing role needs no separate capture with duplicate hashes.
	out, err := c.Run(ctx, dispatcher, []string{"--find", name})
	if err != nil {
		return "", errors.New("Apple selected tool discovery failed")
	}
	selected := strings.TrimSpace(string(out.Stdout))
	if !filepath.IsAbs(selected) || !appleText(selected) {
		return "", errors.New("invalid Apple selected tool path")
	}
	return c.CaptureTool(ctx, selected)
}

// CaptureAppleToolchain binds the concrete compiler/frontend/linker bytes and
// selected SDK identity. Full SDK/framework closure can exceed capture limits;
// that error remains an explicit completeness gap rather than a fabricated hash.
func (c *AppleClosure) CaptureAppleToolchain(ctx context.Context) error {
	for _, pair := range c.env {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || value == "" {
			continue
		}
		switch key {
		case "SWIFT_EXEC", "SWIFT_DRIVER_SWIFT_FRONTEND_EXEC", "SWIFT_DRIVER_SWIFT_SCAN_EXEC", "CC", "CXX", "LD":
			if _, err := c.CaptureTool(ctx, value); err != nil {
				return errors.New("configured Apple producer override cannot be captured")
			}
		}
	}

	for _, name := range []string{"swift-frontend", "clang", "ld"} {
		if _, err := c.SelectAppleTool(ctx, name); err != nil {
			return err
		}
	}
	dispatcher, _ := c.ResolveTool("/usr/bin/xcrun")
	out, err := c.Run(ctx, dispatcher, []string{"--show-sdk-path"})
	if err != nil {
		return errors.New("Apple SDK discovery failed")
	}
	sdk := strings.TrimSpace(string(out.Stdout))
	if !filepath.IsAbs(sdk) || !appleText(sdk) {
		return errors.New("invalid Apple SDK path")
	}
	// Retain the dispatcher-selected SDK path in the sealed request identity. Its
	// unresolved external bytes are separately reported as unknown by adapters.
	alias, err := os.Lstat(sdk)
	if err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(sdk)
	if err != nil {
		return err
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.IsDir() {
		return errors.New("Apple selected SDK is not a directory")
	}
	c.sdk = sdk
	c.sdkAlias, c.sdkRoot, c.sdkResolved = alias, info, resolved
	c.request = c.requestDigest()
	c.state = c.Digest()
	return c.Verify(ctx)
}

func appleInfo(info os.FileInfo) any {
	if info == nil {
		return nil
	}
	var device, inode any
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		device, inode = stat.Dev, stat.Ino
	}
	return []any{info.Mode(), info.Size(), info.ModTime(), device, inode}
}
func preflightAppleRequest(w model.Workspace, c model.Context) error {
	bytes := 0
	text := func(value string) error {
		bytes += len(value)
		if len(value) > 1<<20 || bytes > 1<<20 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return errors.New("Apple request text exceeds bounds")
		}
		return nil
	}
	list := func(values []string) error {
		if len(values) > 4096 {
			return errors.New("Apple request list exceeds bounds")
		}
		for _, value := range values {
			if err := text(value); err != nil {
				return err
			}
		}
		return nil
	}
	command := func(cmd model.Command) error {
		if err := text(cmd.Dir); err != nil {
			return err
		}
		if err := text(cmd.Executable); err != nil {
			return err
		}
		return list(cmd.Args)
	}
	if len(w.ID) > 4096 || len(c.ID) > 4096 || len(c.OS) > 4096 || len(c.Arch) > 4096 {
		return errors.New("Apple request identity exceeds bounds")
	}
	if len(w.Prerequisites) > 128 || len(c.Env) > 4096 || !config.Relative(w.Root) || !config.Relative(w.Command.Dir) {
		return errors.New("invalid Apple request")
	}
	for _, value := range []string{w.ID, w.Root, w.Adapter, c.ID, c.OS, c.Arch} {
		if err := text(value); err != nil {
			return err
		}
	}
	if err := command(w.Command); err != nil {
		return err
	}
	for _, cmd := range w.Prerequisites {
		if err := command(cmd); err != nil {
			return err
		}
	}
	for _, values := range [][]string{w.Patterns, w.BuildFlags, w.Inputs} {
		if err := list(values); err != nil {
			return err
		}
	}
	if w.NodeRuntime != nil {
		for _, value := range []string{w.NodeRuntime.Node, w.NodeRuntime.Modules} {
			if err := text(value); err != nil {
				return err
			}
		}
	}
	if w.SwiftRuntime != nil {
		if err := text(w.SwiftRuntime.Dependencies); err != nil {
			return err
		}
	}
	for key, value := range c.Env {
		if key == "" || strings.ContainsAny(key, "=\r\n") {
			return errors.New("invalid Apple environment key")
		}
		if err := text(key); err != nil {
			return err
		}
		if err := text(value); err != nil {
			return err
		}
	}
	return nil
}

func (c *AppleClosure) TreeDigest(path string) string {
	if tree, ok := c.trees[path]; ok {
		return tree.digest
	}
	return ""
}

// AppleInventory projects native syntax into the existing shared contract.
// Shape validity supplies no completeness or qualification authority.
func AppleInventory(units []model.Unit, cases []native.Case) (model.ExecutionInventory, error) {
	inventory := model.ExecutionInventory{Schema: model.ExecutionSchema}
	if len(units) > 50000 || len(cases) > 100000 {
		return inventory, errors.New("Apple inventory exceeds bounds")
	}
	inventory.Units = append([]model.Unit(nil), units...)
	for _, item := range cases {
		inventory.Cases = append(inventory.Cases, model.Case{Unit: item.Unit, ID: item.ID})
	}
	if err := boundAppleProjection(model.Evidence{}, inventory); err != nil {
		return model.ExecutionInventory{}, err
	}
	if err := runner.ValidateInventory(inventory); err != nil {
		return model.ExecutionInventory{}, err
	}
	return inventory, nil
}

// Closure retains independently compiled XCTest cases and descriptive evidence.
// InventoryComplete is always false; unresolved native context broadens planning.
type Closure struct {
	Inventory         model.ExecutionInventory
	InventoryComplete bool
	Evidence          model.Evidence
	session           *session
	seal              string
}

func CaptureClosure(ctx context.Context, root string, w model.Workspace, c model.Context) (*Closure, error) {
	s, err := open(ctx, root, w, c)
	if err != nil {
		return nil, err
	}
	evidence, err := describe(ctx, root, w, c, s)
	if err != nil {
		s.close()
		return nil, err
	}
	cases, err := collect(ctx, s, evidence.Units)
	if err != nil {
		s.close()
		return nil, err
	}
	inventory, err := AppleInventory(evidence.Units, cases)
	if err != nil {
		s.close()
		return nil, err
	}
	closure := &Closure{Inventory: inventory, Evidence: evidence, session: s}
	closure.seal = closure.currentDigest()
	if err := closure.Verify(ctx); err != nil {
		closure.Close()
		return nil, err
	}
	return closure, nil
}
func (c *Closure) currentDigest() string {
	return appleDigest([]any{c.Inventory, c.InventoryComplete, c.Evidence, c.session.contextDigest()})
}
func (c *Closure) Verify(ctx context.Context) error {
	if c == nil || c.session == nil || c.InventoryComplete {
		return errors.New("Swift observed closure changed")
	}
	if err := boundAppleProjection(c.Evidence, c.Inventory); err != nil {
		return err
	}
	if c.seal != c.currentDigest() {
		return errors.New("Swift observed closure changed")
	}
	return c.session.verify(ctx)
}
func (c *Closure) Close() error {
	if c == nil || c.session == nil {
		return nil
	}
	return c.session.close()
}
func (c *Closure) Digest() string { return c.seal }

// Bound descriptive DTOs before hashing/cloning caller-visible state. A larger
// native collection falls back; public summaries cannot authorize omissions.
func boundAppleProjection(e model.Evidence, inventory model.ExecutionInventory) error {
	if len(e.Nodes) > 50000 || len(e.Units) > 50000 || len(e.Inputs) > 50000 || len(e.Edges) > 200000 || len(e.Effects) > 50000 || len(e.Gaps) > 50000 || len(inventory.Units) > 50000 || len(inventory.Cases) > 100000 {
		return errors.New("Apple observed projection exceeds bounds")
	}
	total := 0
	add := func(values ...string) error {
		for _, value := range values {
			total += len(value)
			if len(value) > 8192 || total > 8<<20 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
				return errors.New("Apple observed projection text exceeds bounds")
			}
		}
		return nil
	}
	for _, units := range [][]model.Unit{e.Units, inventory.Units} {
		for _, unit := range units {
			if err := add(unit.ID, unit.Workspace, unit.Selector, unit.Kind); err != nil {
				return err
			}
		}
	}
	for _, item := range inventory.Cases {
		if err := add(item.Unit, item.ID); err != nil {
			return err
		}
	}
	for _, node := range e.Nodes {
		if err := add(node); err != nil {
			return err
		}
	}
	for name, owners := range e.Inputs {
		if len(owners) > 50000 {
			return errors.New("Apple observed ownership exceeds bounds")
		}
		if err := add(name); err != nil {
			return err
		}
		for _, owner := range owners {
			if err := add(owner); err != nil {
				return err
			}
		}
	}
	for _, edge := range e.Edges {
		if err := add(edge.From, edge.To, edge.Reason); err != nil {
			return err
		}
	}
	for _, effect := range e.Effects {
		if err := add(effect.Owner, effect.Kind, effect.Path); err != nil {
			return err
		}
	}
	for _, gap := range e.Gaps {
		if err := add(gap.Code, gap.Workspace, gap.Detail); err != nil {
			return err
		}
	}
	return add(e.Adapter, e.Version, e.Workspace)
}

type appleOutput struct {
	path, resolved string
	info           os.FileInfo
}

func (o appleOutput) verify() error {
	info, err := os.Lstat(o.path)
	resolved, resolveErr := filepath.EvalSymlinks(o.path)
	if err != nil || resolveErr != nil || resolved != o.resolved || !info.IsDir() || !os.SameFile(info, o.info) || info.Mode() != o.info.Mode() {
		return errors.New("Apple owned output directory identity changed")
	}
	return nil
}

// NewOutputRoot creates a private output namespace; it supplies no proof of
// writer attribution, historical writes or native runtime equivalence.
func (c *AppleClosure) NewOutputRoot(ctx context.Context, prefix string) (string, error) {
	if err := c.Verify(ctx); err != nil {
		return "", err
	}
	if len(c.outputs) >= 16 || len(prefix) > 64 || !appleText(prefix) || strings.ContainsAny(prefix, "/\\") {
		return "", errors.New("Apple output namespace exceeds bounds")
	}
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	rel, err := filepath.Rel(c.root, resolved)
	if err != nil || config.Relative(filepath.ToSlash(rel)) {
		os.RemoveAll(dir)
		return "", errors.New("Apple outputs overlap source")
	}
	info, err := os.Lstat(dir)
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	c.outputs[dir] = appleOutput{dir, resolved, info}
	c.state = c.Digest()
	if err := c.Verify(ctx); err != nil {
		c.invalidate()
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}
func (c *AppleClosure) outputPath(name string) error {
	if !filepath.IsAbs(name) || !appleText(name) {
		return errors.New("invalid Apple producer path")
	}
	for _, output := range c.outputs {
		rel, err := filepath.Rel(output.path, name)
		if err != nil || !config.Relative(filepath.ToSlash(rel)) {
			continue
		}
		if err := output.verify(); err != nil {
			return err
		}
		current := output.path
		for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
			if part == "." {
				continue
			}
			current = filepath.Join(current, part)
			info, err := os.Lstat(current)
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("Apple producer path has an alias or non-directory ancestor")
			}
		}
		return nil
	}
	return errors.New("Apple producer output is not privately owned")
}
func (c *AppleClosure) invalidate() { c.unusable = true; c.state = c.Digest() }

// CopyInputTree imports one strict captured input into a fresh owned output.
// The original stays readonly and bound; observed artifact projections cannot
// be materialized. The pending copy is verified until its first owned stage.
func (c *AppleClosure) CopyInputTree(ctx context.Context, source, destination string) (err error) {
	if err := c.Verify(ctx); err != nil {
		return err
	}
	if c.copy != nil || !appleText(source) || !filepath.IsAbs(source) || !appleText(destination) || filepath.Clean(destination) != destination {
		return errors.New("Apple input copy already pending or invalid")
	}
	if err := c.outputPath(destination); err != nil {
		return err
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return errors.New("Apple input destination must be fresh")
	}
	defer func() {
		if err != nil {
			c.invalidate()
			if cleanupErr := os.RemoveAll(destination); cleanupErr != nil {
				err = errors.Join(err, errors.New("Apple partial input copy cleanup failed"))
			}
		}
	}()
	if info, err := os.Lstat(source); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("Apple imported input root must be a real directory")
	}
	if err := c.CaptureTree(ctx, source); err != nil {
		return err
	}
	tree := c.trees[source]
	if tree.observed != "" {
		return errors.New("Apple observed artifacts cannot become input copies")
	}
	size := tree.bundle.Manifest().Bytes
	if size > inputbundle.MaxBytes-c.bytes {
		return errors.New("Apple source and copy exceed aggregate bounds")
	}
	c.copy = &appleCopy{source, destination, size}
	c.bytes += size
	c.state = c.Digest()
	if err := tree.bundle.Materialize(ctx, destination); err != nil {
		return err
	}
	if err := tree.bundle.VerifyMaterialization(ctx, destination); err != nil {
		return err
	}
	if err := tree.bundle.VerifySources(ctx); err != nil {
		return err
	}
	return c.Verify(ctx)
}

// RunProducer admits a new bounded generation only for explicitly named private
// outputs. All old outputs are verified before launch; every other captured tree
// and source/tool/context identity is checked after launch. Mixed compile/test
// writes remain unqualified and are never attributed to the compiler alone.
func (c *AppleClosure) RunProducer(ctx context.Context, tool string, args []string, outputs []string) (process.Output, error) {
	if err := c.Verify(ctx); err != nil {
		return process.Output{}, err
	}
	if _, ok := c.tools[tool]; !ok || len(outputs) == 0 || len(outputs) > 16 || len(c.stages) >= 128 || len(args) > 8192 {
		return process.Output{}, errors.New("invalid Apple producer stage")
	}
	size := 0
	for _, arg := range args {
		size += len(arg)
		if !appleText(arg) || size > 1<<20 {
			return process.Output{}, errors.New("Apple producer arguments exceed bounds")
		}
	}
	produced := map[string]bool{}
	old := map[string]string{}
	for _, name := range outputs {
		if produced[name] {
			return process.Output{}, errors.New("duplicate Apple producer output")
		}
		if err := c.outputPath(name); err != nil {
			return process.Output{}, err
		}
		if !c.producers[name] {
			if _, exists := c.trees[name]; exists {
				return process.Output{}, errors.New("Apple captured input cannot become producer output")
			}
			// The prelaunch Verify checked the exact pending copy. Consume its
			// reservation only for this destination; failed capture invalidates it.
			consumedCopy := c.copy != nil && c.copy.destination == name
			if consumedCopy {
				c.bytes -= c.copy.bytes
				c.copy = nil
				c.state = c.Digest()
			}
			if _, err := os.Lstat(name); err == nil {
				if err := c.CaptureArtifactTree(ctx, name); err != nil {
					c.invalidate()
					return process.Output{}, err
				}
			} else if consumedCopy || !errors.Is(err, os.ErrNotExist) {
				c.invalidate()
				return process.Output{}, errors.New("Apple pending output capture failed")
			}
			c.producers[name] = true
			c.state = c.Digest()
		}
		produced[name] = true
		old[name] = c.TreeDigest(name)
	}

	nested := map[string]appleTree{}
	skipped := map[string]bool{}
	for name := range produced {
		skipped[name] = true
	}
	for name, tree := range c.trees {
		if produced[name] || tree.observed == "" || !tree.alias.IsDir() || tree.alias.Mode()&os.ModeSymlink != 0 {
			continue
		}
		for parent := range produced {
			lexical, err := filepath.Rel(parent, name)
			if err != nil || lexical == "." || !config.Relative(filepath.ToSlash(lexical)) {
				continue
			}
			physical, err := filepath.EvalSymlinks(parent)
			if err != nil {
				continue
			}
			relative, err := filepath.Rel(physical, tree.resolved)
			if err != nil || relative == "." || !config.Relative(filepath.ToSlash(relative)) {
				continue
			}
			nested[name] = tree
			skipped[name] = true
			old[name] = tree.digest
			break
		}
	}
	stage := appleDigest([]any{"mixed-compile-test-producer-v2-observed-metadata", c.Digest(), tool, args, c.env, c.dir, old})
	out, runErr := process.RunEnvironment(ctx, model.Command{Executable: tool, Args: append([]string(nil), args...)}, c.dir, c.env)
	if !out.Completed {
		c.invalidate()
		if runErr == nil {
			runErr = errors.New("Apple producer stage did not complete")
		}
		return out, runErr
	}
	if err := c.verify(ctx, skipped); err != nil {
		c.invalidate()
		out.Completed = false
		return out, err
	}
	for _, name := range outputs {
		if err := c.outputPath(name); err != nil {
			c.invalidate()
			out.Completed = false
			return out, err
		}
	}

	// Build all candidates without changing retained bindings. Observed nested
	// Products may acquire new physical metadata only with exactly stable bytes,
	// full modes, membership and alias semantics inside this owned native stage.
	replaced := map[string]bool{}
	for name := range produced {
		replaced[name] = true
	}
	for name := range nested {
		replaced[name] = true
	}
	retained := c.bytes
	for name := range replaced {
		if tree, ok := c.trees[name]; ok {
			retained -= tree.bundle.Manifest().Bytes
		}
	}
	count := len(c.trees)
	for name := range replaced {
		if _, exists := c.trees[name]; !exists {
			count++
		}
	}
	if count > 16 {
		c.invalidate()
		out.Completed = false
		return out, errors.New("Apple stage artifact count exceeds bounds")
	}
	candidates := map[string]appleTree{}
	next := map[string]string{}
	for name := range replaced {
		if err := c.outputPath(name); err != nil {
			c.invalidate()
			out.Completed = false
			return out, err
		}
		if previous, ok := nested[name]; ok {
			alias, err := os.Lstat(name)
			resolved, resolveErr := filepath.EvalSymlinks(name)
			if err != nil || resolveErr != nil || !alias.IsDir() || alias.Mode()&os.ModeSymlink != 0 || resolved != previous.resolved {
				c.invalidate()
				out.Completed = false
				return out, errors.New("Apple nested product root alias changed")
			}
		}
		tree, err := readAppleArtifact(ctx, name, inputbundle.MaxBytes-retained)
		if err != nil {
			c.invalidate()
			out.Completed = false
			return out, err
		}
		if previous, ok := nested[name]; ok && tree.semantic != previous.semantic {
			c.invalidate()
			out.Completed = false
			return out, errors.New("Apple nested product semantics changed")
		}
		retained += tree.bundle.Manifest().Bytes
		candidates[name] = tree
		next[name] = tree.digest
	}
	for name, tree := range candidates {
		c.trees[name] = tree
	}
	c.bytes = retained
	c.stages = append(c.stages, appleDigest([]any{stage, next, out.ExitCode}))
	c.state = c.Digest()
	if err := c.Verify(ctx); err != nil {
		c.invalidate()
		out.Completed = false
		return out, err
	}
	return out, runErr
}

// CaptureArtifactTree observes Apple's native artifact topology and real file
// bytes, including framework aliases through Versions/Current. It cannot be
// materialized as a complete-directory runtime envelope and grants no authority.
func (c *AppleClosure) CaptureArtifactTree(ctx context.Context, path string) error {
	if err := c.Verify(ctx); err != nil {
		return err
	}
	if tree, exists := c.trees[path]; exists {
		if tree.observed == "" {
			return errors.New("Apple strict input cannot become observed artifact")
		}
		return nil
	}
	if err := c.captureArtifactTree(ctx, path); err != nil {
		return err
	}
	return c.Verify(ctx)
}

func (c *AppleClosure) captureArtifactTree(ctx context.Context, path string) error {
	if len(c.trees) >= 16 {
		return errors.New("Apple artifact count exceeds bounds")
	}
	tree, err := readAppleArtifact(ctx, path, inputbundle.MaxBytes-c.bytes)
	if err != nil {
		return err
	}
	c.bytes += tree.bundle.Manifest().Bytes
	c.trees[path] = tree
	c.state = c.Digest()
	return nil
}
func readAppleArtifact(ctx context.Context, path string, remaining int64) (appleTree, error) {
	if !filepath.IsAbs(path) || !appleText(path) || remaining < 0 {
		return appleTree{}, errors.New("Apple artifact request exceeds bounds")
	}
	alias, err := os.Lstat(path)
	if err != nil {
		return appleTree{}, err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return appleTree{}, err
	}
	root, err := os.Lstat(resolved)
	if err != nil || !root.IsDir() {
		return appleTree{}, errors.New("Apple artifact must be a directory")
	}
	scan, err := scanAppleArtifact(ctx, resolved, remaining)
	if err != nil {
		return appleTree{}, err
	}
	declared := path
	if alias.Mode()&os.ModeSymlink != 0 {
		declared = resolved
	}
	budget := remaining
	if budget == 0 {
		budget = 1
	}
	bundle, err := inputbundle.Capture(ctx, inputbundle.Request{Roots: []inputbundle.Root{{Name: "artifact", Path: declared, Mount: "."}}, Inputs: scan.inputs, Limits: inputbundle.Limits{Bytes: budget}})
	if err != nil {
		return appleTree{}, err
	}
	after, err := scanAppleArtifact(ctx, resolved, remaining)
	if err != nil {
		return appleTree{}, err
	}
	if after.physical != scan.physical || after.semantic != scan.semantic || after.bytes != scan.bytes || bundle.Manifest().Bytes != scan.bytes {
		return appleTree{}, errors.New("Apple observed artifacts changed during capture")
	}
	afterAlias, aliasErr := os.Lstat(path)
	afterResolved, resolveErr := filepath.EvalSymlinks(path)
	afterRoot, rootErr := os.Lstat(resolved)
	if aliasErr != nil || resolveErr != nil || rootErr != nil || afterResolved != resolved || !sameAppleFile(alias, afterAlias) || !sameAppleFile(root, afterRoot) {
		return appleTree{}, errors.New("Apple artifact root changed during capture")
	}
	return appleTree{bundle: bundle, path: path, resolved: resolved, digest: appleDigest([]any{bundle.Manifest().Digest, scan.physical}), alias: alias, root: root, observed: scan.physical, semantic: appleDigest([]any{bundle.Manifest().Digest, scan.semantic})}, nil
}

// The walk never follows directory aliases. Only canonical regular paths and
// genuinely empty real directories become shared inputbundle inputs; the entire
// directory/link topology is independently retained and checked around capture.
type appleArtifactScan struct {
	physical, semantic string
	inputs             []inputbundle.Input
	bytes              int64
}

func scanAppleArtifact(ctx context.Context, root string, limit int64) (appleArtifactScan, error) {
	type entryIdentity struct {
		Path         string
		Info         any
		Link, Target string
	}
	entries := []entryIdentity{}
	semantics := []any{}
	inputs := []inputbundle.Input{}
	directories := []string{}
	children := map[string]bool{}
	total := int64(0)
	textBytes := 0
	err := filepath.WalkDir(root, func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(entries) >= inputbundle.MaxEntries {
			return errors.New("Apple artifact count exceeds bounds")
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !appleText(rel) || !config.Relative(rel) {
			return errors.New("Apple artifact path exceeds bounds")
		}
		info, err := os.Lstat(name)
		if err != nil {
			return err
		}
		if info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return errors.New("Apple artifact mode unsupported")
		}
		item := entryIdentity{Path: rel, Info: appleInfo(info)}
		if rel != "." {
			children[filepath.ToSlash(filepath.Dir(rel))] = true
		}
		switch {
		case info.Mode().IsRegular():
			if info.Size() < 0 || info.Size() > inputbundle.MaxFileBytes || info.Size() > limit-total {
				return errors.New("Apple aggregate artifact bytes exceed bounds")
			}
			total += info.Size()
			inputs = append(inputs, inputbundle.Input{Root: "artifact", Path: rel, Generated: true})
		case info.IsDir():
			directories = append(directories, rel)
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(name)
			if err != nil || link == "" || !appleText(link) || strings.Contains(link, "\\") || filepath.Clean(link) != link {
				return errors.New("Apple artifact link unsupported")
			}
			target, err := filepath.EvalSymlinks(name)
			if err != nil {
				return errors.New("Apple artifact link is broken or cyclic")
			}
			targetRel, err := filepath.Rel(root, target)
			if err != nil || !config.Relative(filepath.ToSlash(targetRel)) {
				return errors.New("Apple artifact link escapes observed root")
			}
			targetInfo, err := os.Lstat(target)
			if err != nil {
				return err
			}
			if targetInfo.IsDir() {
				ancestor, err := filepath.Rel(target, filepath.Dir(name))
				if err == nil && config.Relative(filepath.ToSlash(ancestor)) {
					return errors.New("Apple artifact link is recursive")
				}
			} else if !targetInfo.Mode().IsRegular() {
				return errors.New("Apple artifact link targets special file")
			}
			item.Link = link
			item.Target = filepath.ToSlash(targetRel)
		default:
			return errors.New("Apple artifact special file unsupported")
		}
		textBytes += len(item.Path) + len(item.Link) + len(item.Target)
		if textBytes > 8<<20 {
			return errors.New("Apple artifact topology text exceeds bounds")
		}
		entries = append(entries, item)
		semanticSize := int64(0)
		if info.Mode().IsRegular() {
			semanticSize = info.Size()
		}
		semantics = append(semantics, []any{item.Path, uint32(info.Mode()), semanticSize, item.Link, item.Target})
		return nil
	})
	if err != nil {
		return appleArtifactScan{}, err
	}
	for _, name := range directories {
		if !children[name] {
			inputs = append(inputs, inputbundle.Input{Root: "artifact", Path: name, Generated: true})
		}
	}
	if len(inputs) == 0 {
		return appleArtifactScan{}, errors.New("Apple artifact has no bounded real inputs")
	}
	return appleArtifactScan{appleDigest(entries), appleDigest(semantics), inputs, total}, ctx.Err()
}
