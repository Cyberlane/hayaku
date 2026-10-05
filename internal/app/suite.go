package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"sort"

	"github.com/Cyberlane/hayaku/internal/capsule"
	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/inputbundle"
)

type suiteConfig struct {
	Schema int             `json:"schema"`
	Groups []capsuleConfig `json:"groups"`
}
type loadedCapsule struct {
	config  capsuleConfig
	request capsule.Request
	bundle  *inputbundle.Bundle
}

// Loading and revalidation are shared by single-capsule and group execution.
// Everything guests can observe is captured before execution; no source or
// environment values are copied into reports.
func loadCapsule(ctx context.Context, root string, c capsuleConfig) (loadedCapsule, error) {
	item := loadedCapsule{config: c}
	if c.Schema != 1 || !config.Relative(c.Module) || c.Module == "." {
		return item, errors.New("invalid capsule configuration")
	}
	module, err := capsule.LoadFiles(ctx, root, []string{c.Module})
	if err != nil {
		return item, err
	}
	producer := c.ProducerIdentity
	var files []capsule.File
	if c.InputEnvelope != nil {
		if len(c.Inputs) != 0 || c.InputEnvelope.Schema != 1 {
			return item, errors.New("capsule input envelope and regular input list are mutually exclusive")
		}
		decoded, err := hex.DecodeString(producer)
		if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != producer {
			return item, errors.New("invalid capsule producer identity")
		}
		item.bundle, err = inputbundle.Capture(ctx, inputRequest(root, *c.InputEnvelope))
		if err != nil {
			return item, err
		}
		files, err = capsule.CapsuleFiles(ctx, item.bundle)
		if err != nil {
			return item, err
		}
		digest := sha256.Sum256([]byte(producer + ":" + item.bundle.Manifest().Digest))
		producer = hex.EncodeToString(digest[:])
	} else {
		files, err = capsule.LoadFiles(ctx, root, c.Inputs)
		if err != nil {
			return item, err
		}
	}
	env := make([]capsule.Variable, 0, len(c.Env))
	for key, value := range c.Env {
		env = append(env, capsule.Variable{Key: key, Value: value})
	}
	sort.Slice(env, func(i, j int) bool { return env[i].Key < env[j].Key })
	item.request = capsule.Request{Name: c.Name, Module: module[0].Data, Files: files, Args: c.Args, Env: env, ProducerIdentity: producer, Seed: c.Seed}
	return item, capsule.ValidateRequest(ctx, item.request, c.Limits)
}

func (item loadedCapsule) verify(ctx context.Context, root string) error {
	module, err := capsule.LoadFiles(ctx, root, []string{item.config.Module})
	if err != nil || len(module) != 1 || !bytes.Equal(module[0].Data, item.request.Module) {
		return errors.New("capsule module changed")
	}
	if item.bundle != nil {
		return item.bundle.VerifySources(ctx)
	}
	files, err := capsule.LoadFiles(ctx, root, item.config.Inputs)
	if err != nil || !reflect.DeepEqual(files, item.request.Files) {
		return errors.New("capsule input envelope changed")
	}
	return nil
}

func invalidateCapsuleResult(result *capsule.Result) {
	result.Complete, result.Qualified, result.Passed = false, false, false
	result.Reason = "live-inputs-changed"
	result.Gaps = append(result.Gaps, "live-inputs-changed")
}
