package native

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/inputbundle"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/noderuntime"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/snapshot"
)

const nodeSourcePolicy = "node-pure-source-candidate-v1"
const nodeParserVersion = "6.0.2"
const nodeSourceBytes = 8 << 20

//go:embed node_certifier.cjs
var nodeSourceHelper string

// NodeCertifierRequest selects a closed, explicitly declared module candidate.
// It does not assert that these sources cover a configured framework invocation.
// SHA pins bind caller-selected tools, not publisher trust or issuer authority.
type NodeCertifierRequest struct {
	Root, Node, TypeScriptSDK, NodeSHA256, CompilerSHA256 string
	Sources, Tests                                        []string
}

type NodeSourceCase struct {
	File  string `json:"file"`
	Label string `json:"label"`
}

// NodeSourceSummary describes bounded source semantics under the pinned API.
// ToolTrust remains "unqualified": runtime/framework trust is a separate gate.
type NodeSourceSummary struct {
	Policy, SourceDigest, ToolsDigest, ParserVersion, NodeVersion string
	SemanticDigest, EmittedDigest, EmitterDigest, ToolTrust       string
	Cases                                                         []NodeSourceCase
	Dependencies                                                  map[string][]string
	Cost, Nodes                                                   int
}

type nodeSourceResult struct {
	Schema         int                 `json:"schema"`
	Version        string              `json:"version"`
	ParserVersion  string              `json:"parserVersion"`
	NodeVersion    string              `json:"nodeVersion"`
	Cases          []NodeSourceCase    `json:"cases"`
	Dependencies   map[string][]string `json:"dependencies"`
	SemanticDigest string              `json:"semanticDigest"`
	EmittedDigest  string              `json:"emittedDigest"`
	EmitterDigest  string              `json:"emitterDigest"`
	Cost           int                 `json:"cost"`
	Nodes          int                 `json:"nodes"`
}

// NodeSourceCertificate owns its borrowed source/tool bindings and private
// parser inputs. It cannot create Artifact.authority, outcomes or receipts.
type NodeSourceCertificate struct {
	request                        NodeCertifierRequest
	summary                        NodeSourceSummary
	source, sdk, controls          *inputbundle.Bundle
	tools                          noderuntime.Fingerprint
	directory, sourceCopy, sdkCopy string
	controlSource, controlCopy     string
	fullGuards                     map[string]bool
	identities                     map[string]os.FileInfo
	resolutions                    map[string]string
	seal                           string
	closed                         bool
	cleanup                        *dynamicDirectory
}

func nodeSourceText(value string, limit int) bool {
	return value != "" && len(value) <= limit && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func nodeSourceSHA(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && value == strings.ToLower(value)
}

func validateNodeCertifierRequest(r NodeCertifierRequest) error {
	if len(r.Sources) == 0 || len(r.Sources) > 128 || len(r.Tests) == 0 || len(r.Tests) > len(r.Sources) {
		return errors.New("Node source declaration exceeds bounds")
	}
	for _, value := range []string{r.Root, r.Node, r.TypeScriptSDK} {
		if !nodeSourceText(value, 4096) || !filepath.IsAbs(value) || filepath.Clean(value) != value {
			return errors.New("Node certifier requires clean absolute paths")
		}
	}
	if !nodeSourceSHA(r.NodeSHA256) || !nodeSourceSHA(r.CompilerSHA256) {
		return errors.New("Node certifier requires explicit tool SHA-256 pins")
	}
	seen := map[string]bool{}
	for _, name := range r.Sources {
		if !nodeSourceText(name, 4096) || !fs.ValidPath(name) || strings.ContainsAny(name, "\\:") || (!strings.HasSuffix(name, ".js") && !strings.HasSuffix(name, ".ts")) || seen[name] {
			return errors.New("invalid Node source module declaration")
		}
		// The helper uses this exact portable spelling for import resolution.
		for _, ch := range name {
			if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_./-", ch) {
				return errors.New("Node source path outside portable candidate")
			}
		}
		seen[name] = true
	}
	tests := map[string]bool{}
	for _, name := range r.Tests {
		if !seen[name] || tests[name] {
			return errors.New("Node test entry absent or repeated")
		}
		tests[name] = true
	}
	return nil
}

func CertifyNodeSources(ctx context.Context, request NodeCertifierRequest) (certificate *NodeSourceCertificate, returnedErr error) {
	return certifyNodeSources(ctx, request, "")
}

// A reviewed private provider can require publisher-pinned SDK bytes before any
// parser code executes. Public source proposals retain their unqualified API.
func certifyNodeSources(ctx context.Context, request NodeCertifierRequest, requiredSDK string) (certificate *NodeSourceCertificate, returnedErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateNodeCertifierRequest(request); err != nil {
		return nil, err
	}
	request.Sources, request.Tests = slices.Clone(request.Sources), slices.Clone(request.Tests)
	slices.Sort(request.Sources)
	slices.Sort(request.Tests)
	c := &NodeSourceCertificate{request: request, identities: map[string]os.FileInfo{}, fullGuards: map[string]bool{}, resolutions: map[string]string{}}
	dir, err := os.MkdirTemp("", "hayaku-node-certifier-")
	if err != nil {
		return nil, err
	}
	c.directory, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, errors.Join(err, os.Remove(dir))
	}
	info, err := os.Lstat(c.directory)
	if err != nil {
		return nil, errors.Join(err, os.Remove(c.directory))
	}
	c.cleanup, err = retainDynamicDirectory(c.directory, info, []string{"source", "sdk", "bootstrap", "controls"})
	if err != nil {
		return nil, errors.Join(err, os.Remove(c.directory))
	}
	defer func() {
		if returnedErr != nil {
			returnedErr = errors.Join(returnedErr, c.Close(), c.cleanup.abandon())
			certificate = nil
		}
	}()
	inputs := make([]inputbundle.Input, len(request.Sources))
	for i, name := range request.Sources {
		inputs[i] = inputbundle.Input{Root: "source", Path: name}
	}
	c.source, err = inputbundle.Capture(ctx, inputbundle.Request{
		Roots: []inputbundle.Root{{Name: "source", Path: request.Root, Mount: "."}}, Inputs: inputs,
		Limits: inputbundle.Limits{Entries: 1024, FileBytes: 256 << 10, Bytes: nodeSourceBytes, Depth: 64},
	})
	if err != nil {
		return nil, err
	}
	for _, entry := range c.source.Manifest().Entries {
		if entry.Kind == "symlink" {
			return nil, errors.New("Node candidate modules require real files and directories")
		}
	}
	c.sdk, err = inputbundle.Capture(ctx, inputbundle.Request{
		Roots: []inputbundle.Root{{Name: "sdk", Path: request.TypeScriptSDK, Mount: "."}}, Inputs: []inputbundle.Input{{Root: "sdk", Path: "."}},
		Limits: inputbundle.Limits{Entries: 1024, FileBytes: 64 << 20, Bytes: 64 << 20, Depth: 64},
	})
	if err != nil {
		return nil, err
	}
	if requiredSDK != "" && c.sdk.Manifest().Digest != requiredSDK {
		return nil, errors.New("Node certifier SDK differs from required compiler profile")
	}
	compiler := filepath.Join(request.TypeScriptSDK, "lib", "typescript.js")
	compilerSHA, err := snapshot.FileDigest(ctx, compiler, 64<<20)
	if err != nil || compilerSHA != request.CompilerSHA256 {
		return nil, errors.Join(err, errors.New("Node certifier compiler pin mismatch"))
	}
	node, err := filepath.EvalSymlinks(request.Node)
	if err != nil {
		return nil, err
	}
	nodeSHA, err := snapshot.FileDigest(ctx, node, 256<<20)
	info, statErr := os.Stat(node)
	if err != nil || statErr != nil || nodeSHA != request.NodeSHA256 || info.Mode().Perm()&0111 == 0 {
		return nil, errors.Join(err, statErr, errors.New("Node certifier executable pin mismatch"))
	}
	for _, name := range []string{request.Root, request.Node, node, request.TypeScriptSDK} {
		if err := c.rememberPath(name); err != nil {
			return nil, err
		}
	}
	for _, group := range []struct {
		root  string
		files *inputbundle.Bundle
	}{{request.Root, c.source}, {request.TypeScriptSDK, c.sdk}} {
		for _, entry := range group.files.Manifest().Entries {
			if err := c.rememberPath(filepath.Join(group.root, filepath.FromSlash(entry.Path))); err != nil {
				return nil, err
			}
		}
	}
	c.tools, err = noderuntime.Identity(ctx, model.NodeRuntime{Node: request.Node, Modules: request.TypeScriptSDK})
	if err != nil {
		return nil, err
	}
	if err := c.verifyIdentities(); err != nil {
		return nil, err
	}
	c.sourceCopy, c.sdkCopy = filepath.Join(c.directory, "source"), filepath.Join(c.directory, "sdk")
	if err := c.source.Materialize(ctx, c.sourceCopy); err != nil {
		return nil, err
	}
	if err := c.sdk.Materialize(ctx, c.sdkCopy); err != nil {
		return nil, err
	}
	protocol := struct {
		Schema   int      `json:"schema"`
		Version  string   `json:"version"`
		Compiler string   `json:"compiler"`
		Tests    []string `json:"tests"`
		Sources  []struct {
			File string `json:"file"`
			Path string `json:"path"`
		} `json:"sources"`
	}{Schema: 1, Version: nodeSourcePolicy, Compiler: filepath.Join(c.sdkCopy, "lib", "typescript.js"), Tests: request.Tests}
	for _, name := range request.Sources {
		protocol.Sources = append(protocol.Sources, struct {
			File string `json:"file"`
			Path string `json:"path"`
		}{name, filepath.Join(c.sourceCopy, filepath.FromSlash(name))})
	}
	encoded, err := json.Marshal(protocol)
	if err != nil || len(encoded) > 1<<20 {
		return nil, errors.New("Node certifier protocol exceeds bounds")
	}
	c.controlSource, c.controlCopy = filepath.Join(c.directory, "bootstrap"), filepath.Join(c.directory, "controls")
	if err := os.Mkdir(c.controlSource, 0700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(c.controlSource, "certifier.cjs"), []byte(nodeSourceHelper), 0600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(c.controlSource, "request.json"), encoded, 0600); err != nil {
		return nil, err
	}
	c.controls, err = inputbundle.Capture(ctx, inputbundle.Request{Roots: []inputbundle.Root{{Name: "control", Path: c.controlSource, Mount: "."}}, Inputs: []inputbundle.Input{{Root: "control", Path: "."}}, Limits: inputbundle.Limits{Entries: 8, FileBytes: 1 << 20, Bytes: 2 << 20, Depth: 4}})
	if err != nil {
		return nil, err
	}
	if err := c.controls.Materialize(ctx, c.controlCopy); err != nil {
		return nil, err
	}
	for _, group := range []struct {
		root  string
		files *inputbundle.Bundle
	}{{c.sourceCopy, c.source}, {c.sdkCopy, c.sdk}, {c.controlSource, c.controls}, {c.controlCopy, c.controls}} {
		for _, entry := range group.files.Manifest().Entries {
			if err := c.rememberPath(filepath.Join(group.root, filepath.FromSlash(entry.Path))); err != nil {
				return nil, err
			}
		}
	}
	if err := c.rememberPath(c.directory); err != nil {
		return nil, err
	}
	if err := c.verifyInputs(ctx); err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	output, runErr := process.RunEnvironmentLimits(runCtx, model.Command{Executable: request.Node, Args: []string{"--no-addons", "--max-old-space-size=128", filepath.Join(c.controlCopy, "certifier.cjs"), filepath.Join(c.controlCopy, "request.json")}}, c.directory, []string{}, 1<<20, 64<<10)
	cancel()
	postCtx, postCancel := context.WithTimeout(context.Background(), 30*time.Second)
	postErr := c.verifyInputs(postCtx)
	postCancel()
	if runErr != nil || !output.Completed || output.ExitCode != 0 || len(output.Stderr) != 0 || postErr != nil {
		return nil, errors.Join(runErr, postErr, errors.New("Node source certification incomplete or rejected"))
	}
	result, err := decodeNodeSourceResult(output.Stdout, request.Tests)
	if err != nil {
		return nil, err
	}
	if err := validateNodeDependencies(result.Dependencies, request.Sources, request.Tests); err != nil {
		return nil, err
	}
	c.summary = NodeSourceSummary{Policy: nodeSourcePolicy, SourceDigest: c.source.Manifest().Digest, ToolsDigest: c.tools.Digest, ParserVersion: result.ParserVersion, NodeVersion: result.NodeVersion, SemanticDigest: result.SemanticDigest, EmittedDigest: result.EmittedDigest, EmitterDigest: result.EmitterDigest, ToolTrust: "unqualified", Cases: slices.Clone(result.Cases), Dependencies: cloneNodeDependencies(result.Dependencies), Cost: result.Cost, Nodes: result.Nodes}
	c.seal = c.currentBinding()
	if err := c.Verify(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

func decodeNodeSourceResult(data []byte, tests []string) (nodeSourceResult, error) {
	var result nodeSourceResult
	if len(data) == 0 || len(data) > 1<<20 {
		return result, errors.New("Node certifier result exceeds bounds")
	}
	if config.DecodeValue(bytes.NewReader(data), &result, 1<<20) != nil || result.Schema != 1 || result.Version != nodeSourcePolicy || result.ParserVersion != nodeParserVersion || !nodeSourceText(result.NodeVersion, 64) || !nodeSourceSHA(result.SemanticDigest) || !nodeSourceSHA(result.EmittedDigest) || !nodeSourceSHA(result.EmitterDigest) || len(result.Cases) == 0 || len(result.Cases) > 1000 || result.Cost < 1 || result.Cost > 100000 || result.Nodes < 1 || result.Nodes > 100000 {
		return nodeSourceResult{}, errors.New("invalid Node source candidate result")
	}
	seen := map[NodeSourceCase]bool{}
	owners := map[string]bool{}
	for _, item := range result.Cases {
		if !slices.Contains(tests, item.File) || !nodeSourceText(item.Label, 256) || seen[item] {
			return nodeSourceResult{}, errors.New("invalid Node source case identity")
		}
		seen[item], owners[item.File] = true, true
	}
	if len(owners) != len(tests) {
		return nodeSourceResult{}, errors.New("Node source test inventory incomplete")
	}
	return result, nil
}

func (c *NodeSourceCertificate) rememberPath(name string) error {
	if c.identities == nil {
		c.identities = map[string]os.FileInfo{}
	}
	if c.fullGuards == nil {
		c.fullGuards = map[string]bool{}
	}
	if c.resolutions == nil {
		c.resolutions = map[string]string{}
	}
	resolved, err := filepath.EvalSymlinks(name)
	if err != nil {
		return err
	}
	c.resolutions[name] = resolved
	for _, initial := range []string{name, resolved} {
		for current, depth := initial, 0; ; current, depth = filepath.Dir(current), depth+1 {
			if depth > 128 || len(c.identities) > 4096 {
				return errors.New("Node source identity paths exceed bounds")
			}
			info, err := os.Lstat(current)
			if err != nil {
				return err
			}
			full := c.fullGuards[current] || current == initial
			if old, exists := c.identities[current]; exists && !sameNodeSourceInfo(old, info, full) {
				return errors.New("Node source identity changed during capture")
			}
			c.identities[current] = info
			c.fullGuards[current] = full
			if filepath.Dir(current) == current {
				break
			}
		}
	}
	return nil
}

// Ancestors outside the selected roots bind routing/permission identity, not
// unrelated sibling bookkeeping. Selected entries and copy roots bind full metadata.
func sameNodeSourceInfo(a, b os.FileInfo, full bool) bool {
	if full {
		return sameProgramInfo(a, b)
	}
	return a != nil && b != nil && os.SameFile(a, b) && a.Mode() == b.Mode()
}

func (c *NodeSourceCertificate) verifyIdentities() error {
	for name, before := range c.identities {
		after, err := os.Lstat(name)
		if err != nil || !sameNodeSourceInfo(before, after, c.fullGuards[name]) {
			return errors.New("Node source or tool metadata changed")
		}
	}
	for name, before := range c.resolutions {
		after, err := filepath.EvalSymlinks(name)
		if err != nil || after != before {
			return errors.New("Node source or tool alias changed")
		}
	}
	return nil
}

func (c *NodeSourceCertificate) verifyInputs(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil || c.closed || c.source == nil || c.sdk == nil || c.controls == nil {
		return errors.New("Node source certificate is unavailable")
	}
	if err := c.verifyIdentities(); err != nil {
		return err
	}
	if err := c.verifyToolPins(ctx); err != nil {
		return err
	}
	tools, err := noderuntime.Identity(ctx, model.NodeRuntime{Node: c.request.Node, Modules: c.request.TypeScriptSDK})
	if err != nil || tools != c.tools {
		return errors.Join(err, errors.New("Node certifier tool context changed"))
	}
	for _, input := range []struct {
		bundle *inputbundle.Bundle
		copy   string
	}{{c.source, c.sourceCopy}, {c.sdk, c.sdkCopy}, {c.controls, c.controlCopy}} {
		if err := input.bundle.VerifySources(ctx); err != nil {
			return err
		}
		if err := input.bundle.VerifyMaterialization(ctx, input.copy); err != nil {
			return err
		}
	}
	if err := c.verifyToolPins(ctx); err != nil {
		return err
	}
	if err := c.verifyIdentities(); err != nil {
		return err
	}
	return ctx.Err()
}

func (c *NodeSourceCertificate) verifyToolPins(ctx context.Context) error {
	resolved, err := filepath.EvalSymlinks(c.request.Node)
	if err != nil {
		return err
	}
	node, err := snapshot.FileDigest(ctx, resolved, 256<<20)
	if err != nil || node != c.request.NodeSHA256 {
		return errors.Join(err, errors.New("Node certifier executable pin changed"))
	}
	compiler, err := snapshot.FileDigest(ctx, filepath.Join(c.request.TypeScriptSDK, "lib", "typescript.js"), 64<<20)
	if err != nil || compiler != c.request.CompilerSHA256 {
		return errors.Join(err, errors.New("Node certifier compiler pin changed"))
	}
	return ctx.Err()
}

func (c *NodeSourceCertificate) currentBinding() string {
	type fileIdentity struct {
		Path   string
		Mode   os.FileMode
		Size   int64
		Time   time.Time
		System any
		Full   bool
	}
	paths := make([]string, 0, len(c.identities))
	for name := range c.identities {
		paths = append(paths, name)
	}
	slices.Sort(paths)
	facts := make([]fileIdentity, 0, len(paths))
	for _, name := range paths {
		info := c.identities[name]
		facts = append(facts, fileIdentity{name, info.Mode(), info.Size(), info.ModTime(), info.Sys(), c.fullGuards[name]})
	}
	return identity([]any{nodeSourcePolicy, digest([]byte(nodeSourceHelper)), c.request, c.summary, c.tools, c.directory, c.sourceCopy, c.sdkCopy, c.controlSource, c.controlCopy, facts, c.resolutions, c.source.Manifest().Digest, c.sdk.Manifest().Digest, c.controls.Manifest().Digest})
}

func (c *NodeSourceCertificate) Summary() NodeSourceSummary {
	if c == nil || c.closed {
		return NodeSourceSummary{}
	}
	summary := c.summary
	summary.Cases = slices.Clone(summary.Cases)
	summary.Dependencies = cloneNodeDependencies(summary.Dependencies)
	return summary
}

func cloneNodeDependencies(graph map[string][]string) map[string][]string {
	result := make(map[string][]string, len(graph))
	for file, targets := range graph {
		result[file] = slices.Clone(targets)
	}
	return result
}

// The parser's projection is complete over captured modules, including dormant
// declarations. Entry modules cannot be dependencies of another lifecycle.
func validateNodeDependencies(graph map[string][]string, sources, tests []string) error {
	if len(graph) != len(sources) || len(sources) == 0 || len(sources) > 128 {
		return errors.New("Node module dependency inventory incomplete")
	}
	state := map[string]uint8{}
	var visit func(string, int) error
	visit = func(file string, depth int) error {
		if state[file] == 1 || depth > 64 {
			return errors.New("Node module dependency graph cyclic or exceeds bounds")
		}
		if state[file] == 2 {
			return nil
		}
		targets, ok := graph[file]
		if !ok || !slices.Contains(sources, file) || !slices.IsSorted(targets) {
			return errors.New("Node module dependency inventory differs from sources")
		}
		state[file] = 1
		for i, target := range targets {
			if (i > 0 && target == targets[i-1]) || slices.Contains(tests, target) {
				return errors.New("Node dependency duplicates or imports a test lifecycle")
			}
			if err := visit(target, depth+1); err != nil {
				return err
			}
		}
		state[file] = 2
		return nil
	}
	for _, file := range sources {
		if err := visit(file, 0); err != nil {
			return err
		}
	}
	return nil
}

func (c *NodeSourceCertificate) Verify(ctx context.Context) error {
	if c == nil || c.closed || c.seal == "" || c.currentBinding() != c.seal || c.summary.ToolTrust != "unqualified" {
		return errors.New("Node source candidate binding changed")
	}
	return c.verifyInputs(ctx)
}

func (c *NodeSourceCertificate) Close() error {
	if c == nil || c.directory == "" {
		return nil
	}
	c.closed = true
	if c.cleanup == nil {
		return errors.New("Node certifier private cleanup ownership unavailable")
	}
	if err := c.cleanup.close(); err != nil {
		return errors.Join(errors.New("Node certifier private cleanup failed"), err)
	}
	c.directory = ""
	return nil
}
