package native

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"slices"
	"strings"
	"time"

	vitestadapter "github.com/Cyberlane/hayaku/internal/adapter/vitest"
	"github.com/Cyberlane/hayaku/internal/inputbundle"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
)

const vitestIssuerVersion = "native-vitest-file-issuer-v1"

// The owned launcher retains the full actual request as a live producer guard.
// Its functional descriptor binds this selected file instead of unrelated tests.
func vitestFileArguments(args []string, unit model.Unit) ([]string, error) {
	if len(args) != 3 || args[0] != "--openssl-config=/resources/.hayaku/openssl.cnf" || args[1] != "/resources/.hayaku/runtime.mjs" {
		return nil, errors.New("Vitest owned invocation differs from fixed implementation")
	}
	var request struct {
		Schema    int      `json:"schema"`
		Profile   string   `json:"profile"`
		Compiler  string   `json:"compiler"`
		Workspace string   `json:"workspace"`
		Mode      string   `json:"mode"`
		Tests     []string `json:"tests"`
		Selected  []string `json:"selected"`
	}
	decoder := json.NewDecoder(strings.NewReader(args[2]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return nil, err
	}
	if decoder.Decode(new(any)) != io.EOF || request.Schema != 1 || request.Profile != vitestadapter.RuntimeProfile || request.Compiler != vitestadapter.RuntimeCompilerProfile || request.Workspace != unit.Workspace || request.Mode != "run" || !slices.Equal(request.Selected, []string{unit.ID}) || !slices.Contains(request.Tests, unit.Selector) {
		return nil, errors.New("Vitest owned invocation has no matching selected file")
	}
	request.Tests = []string{unit.Selector}
	data, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	result := slices.Clone(args)
	result[2] = string(data)
	return result, nil
}

// This digest is descriptive until the private issuer checks the complete live
// source/tool/runtime and configured workspace owners. Only reachable entries
// and their resolved edges belong in an independently reusable file proof.
func vitestFileProof(summary NodeSourceSummary, manifest inputbundle.Manifest, scope vitestScope, unit model.Unit) (string, error) {
	if unit.Kind != "vitest-file" || unit.ID != unit.Workspace+"::"+unit.Selector || !slices.Contains(scope.tests, unit.Selector) {
		return "", errors.New("Vitest file proof has no matching test entry")
	}
	if err := validateNodeDependencies(summary.Dependencies, scope.sources, scope.tests); err != nil {
		return "", err
	}
	entries := map[string]inputbundle.Entry{}
	for _, entry := range manifest.Entries {
		if entry.Kind == "file" {
			if _, duplicate := entries[entry.Path]; duplicate {
				return "", errors.New("Vitest source entry duplicated")
			}
			entries[entry.Path] = entry
		}
	}
	seen := map[string]bool{}
	var names []string
	var visit func(string)
	visit = func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		names = append(names, name)
		for _, target := range summary.Dependencies[name] {
			visit(target)
		}
	}
	visit(unit.Selector)
	slices.Sort(names)
	type source struct {
		Entry        inputbundle.Entry
		Dependencies []string
	}
	closure := make([]source, 0, len(names))
	for _, name := range names {
		entry, found := entries[name]
		file := slices.IndexFunc(scope.files, func(file vitestScopeFile) bool { return file.name == name })
		if !found || file < 0 || scope.files[file].info == nil || !nodeSourceSHA(entry.SHA256) || entry.SHA256 != scope.files[file].digest || entry.Mode != uint32(scope.files[file].info.Mode().Perm()) || entry.Size != scope.files[file].info.Size() || entry.Link != "" || entry.MaterializedLink != "" {
			return "", errors.New("Vitest reachable source differs from captured workspace")
		}
		closure = append(closure, source{entry, slices.Clone(summary.Dependencies[name])})
	}
	return identity(closure), nil
}

// Full source and resource digests stay live guards. The stable tool context
// binds their fixed implementation/SDK/role bytes without unrelated user bytes,
// source aggregate semantics, case lists, temporary paths or inode identities.
func vitestToolContext(p *PreparedVitestRuntime, envelope *Envelope) (string, error) {
	var implementation []inputbundle.Entry
	entries := envelope.files.Manifest().Entries
	for _, name := range []string{".hayaku/runtime.mjs", ".hayaku/openssl.cnf", "package.json"} {
		index := slices.IndexFunc(entries, func(entry inputbundle.Entry) bool { return entry.Path == name && entry.Kind == "file" })
		if index < 0 {
			return "", errors.New("Vitest fixed implementation missing")
		}
		implementation = append(implementation, entries[index])
	}
	summary := p.request.Source.Summary()
	return identity(struct {
		Issuer, Profile, CompilerProfile, SourcePolicy             string
		Parser, NodeVersion, CertifierNode, Compiler, SDK, Emitter string
		SourceHelper, Modules, Runtime, Node                       string
		NodeMode                                                   uint32
		Implementation                                             []inputbundle.Entry
	}{vitestIssuerVersion, vitestadapter.RuntimeProfile, vitestadapter.RuntimeCompilerProfile, summary.Policy,
		summary.ParserVersion, summary.NodeVersion, p.request.Source.request.NodeSHA256, p.request.Source.request.CompilerSHA256,
		p.request.Source.sdk.Manifest().Digest, summary.EmitterDigest, digest([]byte(nodeSourceHelper)), p.prepared.ModulesDigest(),
		nodeRuntimeFunctionalDigest(p.request.Runtime), p.node.digest, uint32(p.node.file.Mode().Perm()), implementation}), nil
}

func verifyVitestIssuerScope(ctx context.Context, p *PreparedVitestRuntime, proof *vitestWorkspaceProof, inventory model.ExecutionInventory) error {
	if err := verifyVitestController(runtime.GOOS, runtime.GOARCH); err != nil {
		return err
	}
	if proof == nil || p == nil || p.prepared == nil || p.request.Source == nil {
		return errors.New("Vitest private workspace and runtime owners required")
	}
	if err := proof.verify(ctx); err != nil {
		return err
	}
	if proof.scope.root != p.request.Files.Root || !slices.Equal(proof.scope.sources, p.request.Files.Sources) || !slices.Equal(proof.scope.tests, p.request.Files.Tests) {
		return errors.New("Vitest configured complete scope differs from producer")
	}
	if err := vitestadapter.MatchCases(inventory, p.expected); err != nil {
		return err
	}
	if p.backend.Image != vitestOriginalImage || p.request.Source.request.Root != proof.scope.root || !slices.Equal(p.request.Source.request.Sources, proof.scope.sources) || !slices.Equal(p.request.Source.request.Tests, proof.scope.tests) || p.prepared.ModulesDigest() != vitestModulesDigest {
		return errors.New("Vitest original and native producer contexts differ")
	}
	if err := verifyVitestSourceToolchain(p.request.Source); err != nil {
		return err
	}
	if err := p.verify(ctx); err != nil {
		return err
	}
	return nil
}

// This whole-producer snapshot is an invocation guard, never a cache key. It
// detects changes to any borrowed owner, complete inventory or private request.
func vitestIssuerGuard(p *PreparedVitestRuntime, proof *vitestWorkspaceProof) string {
	return identity([]any{p.backend, p.request.Files, p.request.Node, p.digest, p.expected,
		p.node.binding(), p.request.Runtime.currentDigest(), p.request.Source.currentBinding(),
		p.prepared.Digest(), proof.currentBinding()})
}

// Only the configured complete-workspace checker calls this producer. Public
// source summaries, runtime captures and discovery inventories grant no seal.
// Registration/activation requires separate reviewed composed acceptance.
func issueVitestArtifacts(ctx context.Context, p *PreparedVitestRuntime, proof *vitestWorkspaceProof, inventory model.ExecutionInventory) ([]Artifact, error) {
	if p == nil {
		return nil, errors.New("Vitest runtime owner required")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	inventory = cloneInventory(inventory)
	if err := verifyVitestIssuerScope(ctx, p, proof, inventory); err != nil {
		return nil, err
	}
	addon, err := p.request.Runtime.AddonPath(ctx, "rolldown")
	if err != nil {
		return nil, err
	}
	_, env, err := p.prepared.Invocation(ctx, "run", nil, addon)
	if err != nil {
		return nil, err
	}
	envelope, err := CaptureEnvelope(ctx, EnvelopeRequest{Files: p.prepared.Files(), Env: env, ScratchBytes: MaxScratchBytes})
	if err != nil {
		return nil, err
	}
	tools, err := vitestToolContext(p, envelope)
	if err != nil {
		return nil, err
	}
	guard := vitestIssuerGuard(p, proof)
	verify := func(ctx context.Context) error {
		p.mu.Lock()
		defer p.mu.Unlock()
		if err := verifyVitestIssuerScope(ctx, p, proof, inventory); err != nil {
			return err
		}
		if guard != vitestIssuerGuard(p, proof) {
			return errors.New("Vitest whole producer changed after issue")
		}
		return envelope.VerifySources(ctx)
	}
	artifacts := make([]Artifact, 0, len(inventory.Units))
	for _, unit := range inventory.Units {
		want, err := selectNodeInventory(inventory, []model.Unit{unit})
		if err != nil {
			return nil, err
		}
		fileProof, err := vitestFileProof(p.request.Source.Summary(), p.request.Source.source.Manifest(), proof.scope, unit)
		if err != nil {
			return nil, err
		}
		args, actualEnv, err := p.prepared.Invocation(ctx, "run", []model.Unit{unit}, addon)
		if err != nil || !slices.Equal(env, actualEnv) {
			return nil, errors.Join(err, errors.New("Vitest fixed environment changed"))
		}
		functionalArgs, err := vitestFileArguments(args, unit)
		if err != nil {
			return nil, err
		}
		a := Artifact{Name: unit.ID, Program: p.request.Node, Proof: fileProof, Args: functionalArgs, Env: slices.Clone(env), Envelope: envelope}
		for _, item := range want.Cases {
			a.Cases = append(a.Cases, item.ID)
		}
		contextDigest := identity([]any{vitestIssuerVersion, proof.profile, tools, fileProof, want})
		a.authority = &artifactAuthority{location: authorityLocation(p.backend, a), producer: identity([]string{vitestIssuerVersion, tools}), verify: verify, inventory: &want,
			decode: func(out process.Output, _ Artifact) (model.Outcomes, error) {
				return decodeVitestRuntimeOutcomes(out, want)
			},
			launch: &providerLaunch{context: contextDigest, envelope: envelope, execute: func(ctx context.Context) (out process.Output, returnedErr error) {
				if err := verify(ctx); err != nil {
					return out, err
				}
				p.mu.Lock()
				defer p.mu.Unlock()
				defer func() {
					post, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					defer cancel()
					if err := errors.Join(proof.verify(post), envelope.VerifySources(post)); err != nil {
						out.Completed = false
						returnedErr = errors.Join(returnedErr, err)
					}
				}()
				return p.invoke(ctx, "run", []model.Unit{unit})
			}},
		}
		a.authority.binding, err = artifactBinding(p.backend, a, p.node.digest, a.authority.producer)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, a)
	}
	if err := verifyVitestIssuerScope(ctx, p, proof, inventory); err != nil || guard != vitestIssuerGuard(p, proof) {
		return nil, errors.Join(err, errors.New("Vitest producer changed during issue"))
	}
	return artifacts, nil
}
