package native

import (
	"context"
	"encoding/hex"
	"errors"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/runner"
)

// artifactAuthority is issued only by a reviewed in-process producer. Public
// artifact fields and JSON never carry it. The seal binds an immutable execution
// binding and producer-owned decoder; verify rechecks the live producer inputs.
// A digest by itself is descriptive data, not an issuer.
type artifactAuthority struct {
	binding   string
	location  string
	producer  string
	verify    func(context.Context) error
	decode    func(process.Output, Artifact) (model.Outcomes, error)
	launch    *providerLaunch
	inventory *model.ExecutionInventory
}

// providerLaunch is private producer authority, never a public callback. Context
// binds the stable functional inputs; the envelope and producer verifier retain
// the fresh physical identities that must remain live for this invocation.
type providerLaunch struct {
	context  string
	envelope *Envelope
	execute  func(context.Context) (process.Output, error)
}

func verifyProviderLaunch(ctx context.Context, a Artifact) error {
	if a.authority == nil || a.authority.launch == nil {
		if a.Envelope != nil {
			return errors.New("native input envelope has no qualified provider")
		}
		return ctx.Err()
	}
	launch := a.authority.launch
	decoded, err := hex.DecodeString(launch.context)
	if err != nil || len(decoded) != 32 || launch.execute == nil || launch.envelope == nil || a.Envelope != launch.envelope {
		return errors.New("native provider launch changed or incomplete")
	}
	return launch.envelope.VerifySources(ctx)
}

func artifactBinding(b Backend, a Artifact, program, producer string) (string, error) {
	inputs := identity(struct {
		Args, Env []string
		Effects   []model.Effect
	}{a.Args, a.Env, a.Effects})
	if a.authority != nil && a.authority.launch != nil {
		inputs = identity([]string{inputs, a.authority.launch.context})
	}
	return runner.BindingDigest(model.ExecutionBinding{
		Schema: model.ExecutionSchema, Group: a.Name, ProgramDigest: program,
		InputsDigest: inputs,
		ProofDigest:  identity([]string{a.Proof, producer}),
		PolicyDigest: identity([]string{observablePolicyDigest(), capabilityPolicyDigest()}), ContextDigest: b.Identity,
		Inventory: a.inventory(),
	})
}

func authorityLocation(b Backend, a Artifact) string {
	return identity(struct {
		Backend Backend
		Program string
	}{b, a.Program})
}

func verifyAuthority(ctx context.Context, b Backend, a Artifact) (string, string, error) {
	seal := a.authority
	if seal == nil || seal.verify == nil || seal.decode == nil || seal.producer == "" || seal.location != authorityLocation(b, a) {
		return "", "", errors.New("native artifact has no matching producer authority")
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if err := verifyProviderLaunch(ctx, a); err != nil {
		return "", "", err
	}
	program, err := fileDigestContext(ctx, a.Program)
	if err != nil {
		return "", "", err
	}
	key, err := artifactBinding(b, a, program, seal.producer)
	if err != nil || key != seal.binding {
		return "", "", errors.New("native artifact changed after qualification")
	}
	if err := seal.verify(ctx); err != nil {
		return "", "", err
	}
	return key, program, nil
}

func executeArtifact(ctx context.Context, b Backend, a Artifact) (process.Output, error) {
	if a.authority != nil && a.authority.launch != nil {
		return a.authority.launch.execute(ctx)
	}
	return b.execute(ctx, a)
}
