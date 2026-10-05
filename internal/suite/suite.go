// Package suite coordinates independent groups within the enforced WASI
// contract. Native graphs and native passing reports are not valid receipts.
package suite

import (
	"context"
	"errors"
	"time"

	"github.com/Cyberlane/hayaku/internal/capsule"
)

const MaxGroups = 256
const MaxBatchBytes = 512 << 20

type Group struct {
	Request capsule.Request
	Limits  capsule.Limits
}
type Options struct {
	CacheDir string
	NoReuse  bool
}
type Result struct {
	Schema   int              `json:"schema"`
	Contract string           `json:"contract"`
	Complete bool             `json:"complete"`
	Passed   bool             `json:"passed"`
	Executed int              `json:"executed"`
	Reused   int              `json:"reused"`
	Duration time.Duration    `json:"duration_ns"`
	Groups   []capsule.Result `json:"groups"`
}

// Run visits every configured group, including after a test failure. A cancelled
// or operationally incomplete batch never reports a complete successful suite.
// Each group independently binds its module, full input envelope and context;
// graph absence cannot remove an input from that envelope.
func Run(ctx context.Context, groups []Group, options Options) (result Result, returnedErr error) {
	result = Result{Schema: 1, Contract: capsule.BackendVersion, Groups: []capsule.Result{}}
	start := time.Now()
	defer func() { result.Duration = time.Since(start) }()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if len(groups) == 0 || len(groups) > MaxGroups {
		return result, errors.New("suite group inventory outside bounds")
	}
	names := map[string]bool{}
	bytes := 0
	for _, group := range groups {
		if names[group.Request.Name] {
			return result, errors.New("duplicate suite group identity")
		}
		names[group.Request.Name] = true
		bytes += len(group.Request.Module)
		for _, file := range group.Request.Files {
			bytes += len(file.Data)
		}
		if bytes > MaxBatchBytes {
			return result, errors.New("suite input envelope exceeds bound")
		}
		if err := capsule.ValidateRequest(ctx, group.Request, group.Limits); err != nil {
			return result, err
		}
	}
	result.Complete, result.Passed = true, true
	for _, group := range groups {
		if err := ctx.Err(); err != nil {
			result.Complete, result.Passed = false, false
			return result, err
		}
		outcome, err := capsule.Run(ctx, group.Request, capsule.Options{CacheDir: options.CacheDir, Limits: group.Limits, NoReuse: options.NoReuse})
		result.Groups = append(result.Groups, outcome)
		if outcome.Mode == "executed" {
			result.Executed++
		}
		if outcome.Mode == "reused" {
			result.Reused++
		}
		if err != nil || !outcome.Complete || !outcome.Qualified {
			result.Complete, result.Passed = false, false
			if returnedErr == nil {
				returnedErr = errors.New("suite group execution incomplete or unqualified")
			}
		}
		if !outcome.Passed {
			result.Passed = false
		}
	}
	if ctx.Err() != nil {
		result.Complete, result.Passed = false, false
		return result, ctx.Err()
	}
	return result, returnedErr
}
