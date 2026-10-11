package native

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"path/filepath"
	"slices"
	"sync"
	"time"

	vitestadapter "github.com/Cyberlane/hayaku/internal/adapter/vitest"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/runner"
	vitestrunner "github.com/Cyberlane/hayaku/internal/runner/vitest"
)

const vitestNodeSHA256 = "fd04c01498102c8c11a57e727e16c3fa389d522af541ec98604d0b3fc8cd75da"
const vitestRolldownSHA256 = "61e64e24a3d0511281d73af01b42f6e6384330550c6b7baa2af2ff74941f871c"
const vitestCertifierDarwinNodeSHA256 = "9187ad22c98cea5b635a79db52fa32ab3f6aa9d41e3abf5da71437cfef1ca9de"
const vitestCompilerSHA256 = "630f808ac32d968a49a392c42cc06fd72abd939aaa7edfe3302810c067934653"
const vitestSDKDigest = "913f47a6b39e92ba5a385aea30ad2438a11503b3d78e9392dd7d54ac1b71ef74"
const vitestModulesDigest = "bfae7e5fa439c3a00ffe6dd3b7c6de2792ceb18921290b41eaf04374c8155409"

// NodeRuntimeRequest borrows source and runtime owners. A source candidate has
// no authority: this operational context likewise cannot issue native receipts.
// Qualification still requires the complete reviewed SDK/compiler/framework
// closure and actual Rolldown transform semantics; version labels and the source
// candidate's TypeScript transpile digest alone are not execution equivalence.
type NodeRuntimeRequest struct {
	Files   vitestadapter.RuntimeRequest
	Node    string
	Runtime *Runtime
	Source  *NodeSourceCertificate
}

type PreparedVitestRuntime struct {
	mu       sync.Mutex
	backend  Backend
	request  NodeRuntimeRequest
	prepared *vitestadapter.PreparedRuntime
	node     *dynamicFile
	expected model.ExecutionInventory
	digest   string
	closed   bool
}

func PrepareVitestRuntime(ctx context.Context, backend Backend, request NodeRuntimeRequest) (_ *PreparedVitestRuntime, returnedErr error) {
	if backend.platform() != "linux" || backend.Architecture != "arm64" || backend.Sandbox != "" || backend.Identity == "" || request.Runtime == nil || request.Source == nil || !filepath.IsAbs(request.Node) || vitestadapter.RuntimeCompilerProfile != managedCompilerProfile {
		return nil, errors.New("restricted Vitest requires its explicit Linux ARM64 runtime profile")
	}
	if err := request.Source.Verify(ctx); err != nil {
		return nil, err
	}
	if err := verifyVitestSourceToolchain(request.Source); err != nil {
		return nil, err
	}
	if err := request.Runtime.Verify(ctx); err != nil {
		return nil, err
	}
	if err := verifyVitestRuntimeClosure(request.Runtime); err != nil {
		return nil, err
	}
	sources, tests := slices.Clone(request.Files.Sources), slices.Clone(request.Files.Tests)
	slices.Sort(sources)
	slices.Sort(tests)
	request.Files.Sources, request.Files.Tests = sources, tests
	if request.Source.request.Root != request.Files.Root || !slices.Equal(request.Source.request.Sources, sources) || !slices.Equal(request.Source.request.Tests, tests) {
		return nil, errors.New("restricted Vitest source ownership differs from candidate")
	}
	node, err := preflightDynamicFile(request.Node)
	if err != nil {
		return nil, err
	}
	node.digest, err = fileDigestContext(ctx, node.resolved)
	if err != nil || node.digest != vitestNodeSHA256 || node.file.Mode().Perm()&0111 == 0 {
		return nil, errors.Join(err, errors.New("restricted Vitest native inputs differ from accepted profile"))
	}
	if err = verifyNodeRuntimeExecutable(ctx, node); err != nil {
		return nil, err
	}
	return prepareVitestRuntimeResources(ctx, &PreparedVitestRuntime{backend: backend, request: request, node: node}, nil)
}

func prepareVitestRuntimeResources(ctx context.Context, p *PreparedVitestRuntime, created func(*vitestadapter.PreparedRuntime) error) (_ *PreparedVitestRuntime, returnedErr error) {
	defer func() {
		if returnedErr != nil {
			returnedErr = errors.Join(returnedErr, p.Close())
			if p.prepared != nil {
				returnedErr = errors.Join(returnedErr, p.prepared.Abandon())
			}
		}
	}()
	var err error
	p.prepared, err = vitestadapter.PrepareRuntime(ctx, p.request.Files)
	if err != nil {
		return nil, err
	}
	if created != nil {
		if err = created(p.prepared); err != nil {
			return nil, err
		}
	}
	if p.prepared.ModulesDigest() != vitestModulesDigest {
		return nil, errors.New("restricted Vitest complete framework closure differs from fixed profile")
	}
	p.expected = model.ExecutionInventory{Schema: model.ExecutionSchema, Units: p.prepared.Units()}
	for _, item := range p.request.Source.Summary().Cases {
		caseID, err := json.Marshal([]any{item.Label, 0})
		if err != nil {
			return nil, err
		}
		p.expected.Cases = append(p.expected.Cases, model.Case{Unit: p.request.Files.Workspace + "::" + item.File, ID: string(caseID)})
	}
	slices.SortFunc(p.expected.Cases, func(a, b model.Case) int {
		if a.Unit != b.Unit {
			if a.Unit < b.Unit {
				return -1
			}
			return 1
		}
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	if err = runner.ValidateInventory(p.expected); err != nil {
		return nil, err
	}
	p.digest = identity(struct {
		Profile, Resources, Runtime, Node string
		Source                            NodeSourceSummary
		Inventory                         model.ExecutionInventory
	}{vitestadapter.RuntimeProfile, p.prepared.Digest(), nodeRuntimeFunctionalDigest(p.request.Runtime), p.node.digest, p.request.Source.Summary(), p.expected})
	if err = p.verify(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

func verifyVitestSourceToolchain(source *NodeSourceCertificate) error {
	if source == nil || source.sdk == nil || source.sdk.Manifest().Digest != vitestSDKDigest || source.request.CompilerSHA256 != vitestCompilerSHA256 || (source.request.NodeSHA256 != vitestNodeSHA256 && source.request.NodeSHA256 != vitestCertifierDarwinNodeSHA256) || source.summary.ParserVersion != "6.0.2" || source.summary.NodeVersion != "22.18.0" {
		return errors.New("restricted Vitest complete source toolchain differs from fixed profile")
	}
	return nil
}

// These role bytes were independently exported from the immutable Linux ARM64
// image 0ea54978ec8888bd907c0b0d552b4ac12dd5951ddc37fead577d17d7252e2f91.
// Generic ELF compatibility and a caller's captured digest do not confer trust.
// Runtime.Verify separately seals and rechecks the actual captured bytes.
func verifyVitestRuntimeClosure(r *Runtime) error {
	if r == nil || r.closed.Load() || r.loader == nil || r.loaderName() != "ld-linux-aarch64.so.1" || r.loader.digest != "a2db553caf40402c0db144234c5d146b2cf7a69d97508341724639661b226025" || len(r.libraries) != 6 {
		return errors.New("restricted Vitest loader or library roles differ from fixed profile")
	}
	for _, role := range [...]struct{ name, digest string }{
		{"libdl.so.2", "e97c2f373f3495f135eaf302041d9f24f3ce2ac44c15ca2743488b396c81a9a7"},
		{"libstdc++.so.6", "532f3a12d7b8fef6cc7a064ea106b6c4ab2365674b73e743e4a385d4ffb6c8ab"},
		{"libm.so.6", "c36873edf7004eac309f7bea0b99930e050cba6b4c28482cc9178ec6ed1d5b2b"},
		{"libgcc_s.so.1", "046856f95f4636f1fc7c3a12bf4f3cd5634c2fc5145c3fdf7395d4f349fa69c7"},
		{"libpthread.so.0", "9b60c3aa448ef2b7f22c8538962b0f4d7c944b7f08aafd34b563ac5ec51daf9d"},
		{"libc.so.6", "50227f7a4dcb8b2a40b84aedff1c101f7e0aa5a4fc180ba882b52e53be226427"},
	} {
		if file := r.libraries[role.name]; file == nil || file.image.soname != role.name || file.digest != role.digest {
			return errors.New("restricted Vitest library bytes differ from fixed profile")
		}
	}
	if len(r.addons) != 1 || r.addons["rolldown"] == nil || r.addons["rolldown"].digest != vitestRolldownSHA256 {
		return errors.New("restricted Vitest native addon differs from fixed profile")
	}
	return nil
}

// The actual Linux runtime executable has a separate physical guard from the
// possibly different host Node used by the source certifier. Never key reuse on
// these invocation-local aliases, inodes or ancestor identities.
func verifyNodeRuntimeExecutable(ctx context.Context, node *dynamicFile) error {
	if node == nil || node.digest == "" {
		return errors.New("restricted Vitest Node executable ownership unavailable")
	}
	if err := node.verifyIdentity(); err != nil {
		return err
	}
	digest, err := fileDigestContext(ctx, node.resolved)
	if err != nil || digest != node.digest {
		return errors.Join(err, errors.New("restricted Vitest Node executable changed"))
	}
	return node.verifyIdentity()
}

// Fresh capture paths/inodes remain live guards, not warm-cache identity.
func nodeRuntimeFunctionalDigest(r *Runtime) string {
	type role struct{ Name, Digest string }
	roles := []role{{"loader:" + r.loaderName(), r.loader.digest}}
	for _, name := range slices.Sorted(maps.Keys(r.libraries)) {
		roles = append(roles, role{name, r.libraries[name].digest})
	}
	for _, name := range addonNames(r.addons) {
		roles = append(roles, role{"addon:" + name, r.addons[name].digest})
	}
	return identity(struct {
		Policy string
		Roles  []role
	}{dynamicPolicy, roles})
}

func (p *PreparedVitestRuntime) verify(ctx context.Context) error {
	if p == nil || p.closed || p.prepared == nil || p.request.Runtime == nil || p.request.Source == nil {
		return errors.New("restricted Vitest runtime unavailable")
	}
	if err := verifyNodeRuntimeExecutable(ctx, p.node); err != nil {
		return err
	}
	return errors.Join(p.prepared.Verify(ctx), p.request.Runtime.Verify(ctx), verifyVitestRuntimeClosure(p.request.Runtime), p.request.Source.Verify(ctx), p.backend.unchanged(ctx))
}

func (p *PreparedVitestRuntime) Verify(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.verify(ctx)
}

func (p *PreparedVitestRuntime) Digest() string { return p.digest }

func (p *PreparedVitestRuntime) invoke(ctx context.Context, mode string, selected []model.Unit) (out process.Output, returnedErr error) {
	if err := p.verify(ctx); err != nil {
		return out, err
	}
	addon, err := p.request.Runtime.AddonPath(ctx, "rolldown")
	if err != nil {
		return out, err
	}
	args, env, err := p.prepared.Invocation(ctx, mode, selected, addon)
	if err != nil {
		return out, err
	}
	program, err := captureManagedCompilerProgram(ctx, DynamicProgramRequest{Program: p.request.Node, Args: args, Runtime: p.request.Runtime})
	if err != nil {
		return out, err
	}
	defer func() {
		post, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err := errors.Join(p.verify(post), program.Verify(post), program.Close())
		if err != nil {
			out.Completed = false
			returnedErr = errors.Join(returnedErr, err)
		}
	}()
	envelope, err := CaptureEnvelope(ctx, EnvelopeRequest{Files: p.prepared.Files(), Env: env, ScratchBytes: MaxScratchBytes})
	if err != nil {
		return out, err
	}
	return RunDynamicProgram(ctx, p.backend, program, envelope)
}

// Collect requires independently admitted source cases to match real native
// collection. Every call launches a fresh process and framework instance.
func (p *PreparedVitestRuntime) Collect(ctx context.Context) (model.ExecutionInventory, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out, err := p.invoke(ctx, "collect", nil)
	if err != nil {
		return model.ExecutionInventory{}, err
	}
	inventory, err := p.prepared.DecodeCollection(out)
	if err != nil {
		return model.ExecutionInventory{}, err
	}
	if err = vitestadapter.MatchCases(inventory, p.expected); err != nil {
		return model.ExecutionInventory{}, err
	}
	return inventory, nil
}

// Run preserves real assertion failures and requires the exact selected native
// case inventory. Complete evidence here still grants no cache authority.
func (p *PreparedVitestRuntime) Run(ctx context.Context, inventory model.ExecutionInventory, selected []model.Unit) (process.Output, model.Outcomes, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := vitestadapter.MatchCases(inventory, p.expected); err != nil {
		return process.Output{}, model.Outcomes{}, err
	}
	want, err := selectNodeInventory(inventory, selected)
	if err != nil {
		return process.Output{}, model.Outcomes{}, err
	}
	out, runErr := p.invoke(ctx, "run", selected)
	outcomes, err := decodeVitestRuntimeOutcomes(out, want)
	return out, outcomes, errors.Join(runErr, err)
}

func decodeVitestRuntimeOutcomes(out process.Output, want model.ExecutionInventory) (model.Outcomes, error) {
	result, err := vitestrunner.Reconcile(bytes.NewReader(out.Stdout), want.Units)
	outcomes := model.Outcomes{Complete: err == nil && out.Completed, Failed: result.Failed, Errors: result.UnhandledErrors}
	for _, unit := range result.Files {
		outcomes.Units = append(outcomes.Units, model.Outcome{Unit: unit.Unit, Action: unit.Action})
	}
	for _, test := range result.Tests {
		outcomes.Tests = append(outcomes.Tests, model.Outcome{Unit: test.Unit, Test: test.Test, Action: test.Action})
	}
	if err == nil {
		err = runner.ValidateExact(outcomes, want, out.Completed, out.ExitCode)
	}
	if err != nil {
		outcomes.Complete = false
	}
	return outcomes, err
}

func selectNodeInventory(inventory model.ExecutionInventory, selected []model.Unit) (model.ExecutionInventory, error) {
	if selected == nil {
		return inventory, runner.ValidateInventory(inventory)
	}
	result := model.ExecutionInventory{Schema: inventory.Schema, Units: slices.Clone(selected)}
	seen := map[string]bool{}
	for _, unit := range selected {
		if !slices.Contains(inventory.Units, unit) || seen[unit.ID] {
			return result, errors.New("invalid restricted Vitest selected inventory")
		}
		seen[unit.ID] = true
	}
	for _, item := range inventory.Cases {
		if seen[item.Unit] {
			result.Cases = append(result.Cases, item)
		}
	}
	return result, runner.ValidateInventory(result)
}

func (p *PreparedVitestRuntime) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	if p.prepared == nil {
		return nil
	}
	return p.prepared.Close()
}
