package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/Cyberlane/hayaku/internal/adapter"
	vitestprovider "github.com/Cyberlane/hayaku/internal/adapter/vitest"
	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/model"
	gorunner "github.com/Cyberlane/hayaku/internal/runner/golang"
	nativerunner "github.com/Cyberlane/hayaku/internal/runner/native"
	vitestRunner "github.com/Cyberlane/hayaku/internal/runner/vitest"
	"github.com/Cyberlane/hayaku/internal/snapshot"
)

type CommandResult struct {
	Workspace    string                `json:"workspace"`
	Command      model.Command         `json:"command"`
	ExitCode     int                   `json:"exit_code"`
	Duration     time.Duration         `json:"duration_ns"`
	OutputBytes  int                   `json:"output_bytes"`
	Go           *gorunner.Result      `json:"go,omitempty"`
	Vitest       *vitestRunner.Result  `json:"vitest,omitempty"`
	Native       *nativerunner.Result  `json:"native,omitempty"`
	Complete     bool                  `json:"complete"`
	RuntimeTrace *vitestprovider.Trace `json:"runtime_trace,omitempty"`
	Outcomes     *model.Outcomes       `json:"outcomes,omitempty"`
}
type Execution struct {
	Schema         int               `json:"schema"`
	Candidate      string            `json:"candidate"`
	ContextDigest  string            `json:"context_digest"`
	Mode           string            `json:"mode"`
	Commands       []CommandResult   `json:"commands"`
	Passed         bool              `json:"passed"`
	ConfigDigest   string            `json:"config_digest"`
	ToolsDigest    string            `json:"tools_digest"`
	RunnerVersions map[string]string `json:"runner_versions,omitempty"`
}

// Execute is used only after fresh plan reconstruction, and enforces identity
// again here so callers cannot accidentally execute an unvalidated saved plan.
func Execute(ctx context.Context, root string, c model.Config, p model.Plan) (Execution, error) {
	return executeMode(ctx, root, c, p, false)
}

// Observe is a full-inventory diagnostic run, separate from required execution.
func Observe(ctx context.Context, root string, c model.Config, p model.Plan) (Execution, error) {
	if len(c.Producers) != 0 {
		return Execution{}, errors.New("producer observation requires independent producer scopes; retain full execution")
	}
	for _, w := range c.Workspaces {
		if !adapter.SupportsObserve(w) {
			return Execution{}, errors.New("observe requires bound Vitest workspaces; preserve other full suite gates")
		}
	}
	return executeMode(ctx, root, c, p, true)
}

func executeMode(ctx context.Context, root string, c model.Config, p model.Plan, observe bool) (result Execution, returnedErr error) {
	result = Execution{Schema: model.Schema, Candidate: p.Candidate, ContextDigest: p.ContextDigest, Mode: "full", ConfigDigest: p.ConfigDigest, ToolsDigest: p.ToolsDigest, RunnerVersions: nativeVersions(p), Commands: []CommandResult{}}
	if observe {
		result.Mode = "runtime-observation"
	}
	if p.Mode != "full-fallback" || config.Digest(c) != p.ConfigDigest {
		return result, errors.New("execution identities differ from plan")
	}
	if (len(c.Producers) != 0) != (p.Generated != nil) {
		return result, errors.New("producer declarations differ from plan")
	}
	if err := validateExecution(ctx, root, c, p); err != nil {
		return result, err
	}
	executionRoot := root
	revalidate := func() error { return validateExecution(ctx, root, c, p) }
	if hasNodeRuntime(c) || hasApple(c) || len(c.Producers) != 0 {
		pair, err := snapshot.Capture(ctx, root, p.Candidate, p.Candidate)
		if err != nil {
			return result, err
		}
		defer pair.Close()
		executionRoot = pair.CandidateDir
		generated, err := prepareGenerated(ctx, executionRoot, p.Candidate, c)
		if err != nil {
			return result, err
		}
		if generated != nil {
			defer func() {
				if cleanupErr := generated.Close(); cleanupErr != nil {
					result.Passed = false
					returnedErr = errors.Join(returnedErr, cleanupErr)
				}
			}()
			if p.Generated == nil || config.Digest(generated.records) != config.Digest(p.Generated.Candidate) {
				return result, errors.New("fresh candidate producer inputs or outputs differ from plan")
			}
			executionRoot = generated.root
			previous := revalidate
			revalidate = func() error { return errors.Join(previous(), generated.verify(ctx)) }
		} else if p.Generated != nil {
			return result, errors.New("plan declares unconfigured producers")
		}
		if err := prepareNodeRuntimes(ctx, executionRoot, c); err != nil {
			return result, err
		}
		for _, w := range c.Workspaces {
			if err := adapter.PrepareSnapshot(ctx, executionRoot, w); err != nil {
				return result, err
			}
		}
		original, err := executionTreeDigest(ctx, executionRoot, c)
		if err != nil {
			return result, err
		}
		revalidate = treeValidator(ctx, executionRoot, c, original, revalidate)
	}
	if err := runWorkspaces(ctx, executionRoot, c, p.Selected, false, observe, &result, revalidate); err != nil {
		return result, err
	}
	if err := validateExecution(ctx, root, c, p); err != nil {
		return result, err
	}
	result.Passed = true
	return result, nil
}

func runWorkspaces(ctx context.Context, root string, c model.Config, units []model.Unit, proposal, fresh bool, result *Execution, revalidate func() error) error {
	var firstErr error
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	root = canonicalRoot
	for _, w := range c.Workspaces {
		workspaceUnits := []model.Unit{}
		for _, u := range units {
			if u.Workspace == w.ID {
				workspaceUnits = append(workspaceUnits, u)
			}
		}
		if proposal && len(workspaceUnits) == 0 {
			continue
		}
		commands := append([]model.Command(nil), w.Prerequisites...)
		command := w.Command
		var err error
		if proposal {
			command, err = adapter.Proposal(w, workspaceUnits)
			if err != nil {
				return err
			}
			command.Dir = w.Command.Dir
		}
		command, err = adapter.PrepareCommand(w, command, fresh)
		if err != nil {
			return err
		}
		commands = append(commands, command)
		for i, cmd := range commands {
			if revalidate != nil {
				if err := revalidate(); err != nil {
					return err
				}
			}
			cmd.Dir = filepath.ToSlash(filepath.Join(w.Root, cmd.Dir))
			if err := config.ValidateCommand(cmd); err != nil {
				return err
			}
			dir, err := filepath.EvalSymlinks(filepath.Join(root, cmd.Dir))
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, dir)
			if err != nil || !config.Relative(filepath.ToSlash(rel)) {
				return errors.New("execution cwd resolves outside repository")
			}
			run, runErr := adapter.Run(ctx, root, dir, w, c.Context, cmd, workspaceUnits, adapter.RunOptions{
				Proposal: proposal, Observe: result.Mode == "runtime-observation", Prerequisite: i != len(commands)-1, Version: result.RunnerVersions[w.ID],
			})
			entry := CommandResult{Workspace: w.ID, Command: cmd, ExitCode: run.Output.ExitCode, Duration: run.Output.Duration,
				OutputBytes: len(run.Output.Stdout) + len(run.Output.Stderr), Complete: run.Complete, Outcomes: run.Outcomes,
				Go: run.Go, Vitest: run.Vitest, Native: run.Native, RuntimeTrace: run.RuntimeTrace}
			result.Commands = append(result.Commands, entry)
			if revalidate != nil {
				if err := revalidate(); err != nil {
					result.Commands[len(result.Commands)-1].Complete = false
					return err
				}
			}
			if runErr != nil {
				err := fmt.Errorf("workspace %s execution failed or incomplete: %w", w.ID, runErr)
				if !fresh {
					return err
				}
				if firstErr == nil {
					firstErr = err
				}
				break
			}
		}
	}
	return firstErr
}

type ShadowResult struct {
	Schema          int           `json:"schema"`
	Candidate       string        `json:"candidate"`
	ContextDigest   string        `json:"context_digest"`
	ComparisonValid bool          `json:"comparison_valid"`
	ObservedMisses  []string      `json:"observed_misses"`
	Inconsistencies []string      `json:"inconsistencies"`
	Proposal        Execution     `json:"proposal"`
	Full            Execution     `json:"full"`
	TotalDuration   time.Duration `json:"total_duration_ns"`
	Assurance       string        `json:"assurance"`
}

// Shadow uses separate raw-tree copies and fresh native tests. It is a challenge
// harness, not a production qualification certificate. Native Git metadata and
// external services are not isolated by these copies and remain contract gaps.
func Shadow(ctx context.Context, root string, c model.Config, p model.Plan) (result ShadowResult, returnedErr error) {
	result = ShadowResult{Schema: model.Schema, Candidate: p.Candidate, ContextDigest: p.ContextDigest, ObservedMisses: []string{}, Inconsistencies: []string{}, Assurance: "observational-only; no production omission authority"}
	start := time.Now()
	if len(c.Producers) != 0 {
		return result, errors.New("producer shadow requires independent producer scopes; retain full execution")
	}
	defer func() { result.TotalDuration = time.Since(start) }()
	defer func() {
		if err := validateExecution(ctx, root, c, p); err != nil {
			result.ComparisonValid = false
			if returnedErr == nil {
				returnedErr = err
			}
		}
	}()
	for _, w := range c.Workspaces {
		if !adapter.SupportsShadow(w) {
			return result, errors.New("shadow requires a native outcome reconciler; keep other full gates")
		}
	}
	if config.Digest(c) != p.ConfigDigest {
		return result, errors.New("shadow identities differ from plan")
	}
	if err := validateExecution(ctx, root, c, p); err != nil {
		return result, err
	}
	pair, err := snapshot.Capture(ctx, root, p.Candidate, p.Candidate)
	if err != nil {
		return result, err
	}
	defer pair.Close()
	for _, dir := range []string{pair.BaseDir, pair.CandidateDir} {
		if err := prepareNodeRuntimes(ctx, dir, c); err != nil {
			return result, err
		}
	}
	proposalTree, err := executionTreeDigest(ctx, pair.BaseDir, c)
	if err != nil {
		return result, err
	}
	fullTree, err := executionTreeDigest(ctx, pair.CandidateDir, c)
	if err != nil {
		return result, err
	}
	result.Proposal = Execution{Schema: model.Schema, Candidate: p.Candidate, ContextDigest: p.ContextDigest, Mode: "shadow-proposal", RunnerVersions: nativeVersions(p), Commands: []CommandResult{}}
	result.Full = Execution{Schema: model.Schema, Candidate: p.Candidate, ContextDigest: p.ContextDigest, Mode: "shadow-full", RunnerVersions: nativeVersions(p), Commands: []CommandResult{}}
	checkOriginal := func() error { return validateExecution(ctx, root, c, p) }
	proposalErr := runWorkspaces(ctx, pair.BaseDir, c, p.Proposed, true, true, &result.Proposal, treeValidator(ctx, pair.BaseDir, c, proposalTree, checkOriginal))
	result.Proposal.Passed = proposalErr == nil
	fullErr := runWorkspaces(ctx, pair.CandidateDir, c, p.Selected, false, true, &result.Full, treeValidator(ctx, pair.CandidateDir, c, fullTree, checkOriginal))
	result.Full.Passed = fullErr == nil
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	result.TotalDuration = time.Since(start)
	selected := outcomes(result.Proposal)
	full := outcomes(result.Full)
	// Failed tests in omitted packages are observed misses. Missing individual
	// outcomes inside a proposed package invalidate comparison (failfast/filters).
	proposedPackages := map[string]bool{}
	for _, u := range p.Proposed {
		proposedPackages[unitOutcomeID(u)] = true
	}
	for id, state := range full {
		other, seen := selected[id]
		if state == "fail" && (!seen || other != "fail") {
			result.ObservedMisses = append(result.ObservedMisses, id)
		}
		if seen && other != state {
			result.Inconsistencies = append(result.Inconsistencies, id)
		}
	}
	for id := range selected {
		if _, seen := full[id]; !seen {
			result.Inconsistencies = append(result.Inconsistencies, id)
		}
	}
	for _, cmd := range result.Full.Commands {
		for _, test := range commandOutcomes(cmd).Tests {
			if proposedPackages[test.Unit] {
				id := test.Unit + "/" + test.Test
				if _, seen := selected[id]; !seen {
					result.Inconsistencies = append(result.Inconsistencies, id)
				}
			}
		}
	}

	sort.Strings(result.ObservedMisses)
	sort.Strings(result.Inconsistencies)
	result.ComparisonValid = len(result.Inconsistencies) == 0
	for _, execution := range []Execution{result.Proposal, result.Full} {
		for _, command := range execution.Commands {
			if !command.Complete {
				result.ComparisonValid = false
			}
			// Global native errors cannot be attributed to a stable file/test
			// identity. They fail the run and invalidate the comparison.
			if commandOutcomes(command).Errors > 0 {
				result.ComparisonValid = false
			}
		}
	}
	for _, u := range p.Selected {
		if _, ok := full[unitOutcomeID(u)]; !ok {
			result.ComparisonValid = false
		}
	}
	for _, u := range p.Proposed {
		if _, ok := selected[unitOutcomeID(u)]; !ok {
			result.ComparisonValid = false
		}
	}
	if err := validateExecution(ctx, root, c, p); err != nil {
		return result, err
	}
	if len(result.ObservedMisses) > 0 {
		return result, errors.New("shadow comparison observed missed failures")
	}
	if !result.ComparisonValid {
		return result, errors.New("shadow comparison invalid or inconsistent")
	}
	if proposalErr != nil {
		return result, proposalErr
	}
	if fullErr != nil {
		return result, fullErr
	}
	return result, nil
}

func outcomes(execution Execution) map[string]string {
	result := map[string]string{}
	for _, command := range execution.Commands {
		common := commandOutcomes(command)
		for _, item := range common.Units {
			result[item.Unit] = item.Action
		}
		for _, item := range common.Tests {
			result[item.Unit+"/"+item.Test] = item.Action
		}
	}
	return result
}

func unitOutcomeID(unit model.Unit) string { return unit.ID }

func hasApple(c model.Config) bool {
	for _, w := range c.Workspaces {
		if w.Adapter == "swift" || w.Adapter == "xcode" {
			return true
		}
	}
	return false
}

func nativeVersions(p model.Plan) map[string]string {
	versions := map[string]string{}
	for _, e := range p.Evidence {
		if version := adapter.ResultVersion(e.Adapter, e.Version); version != "" {
			versions[e.Workspace] = version
		}
	}
	return versions
}

func validateExecution(ctx context.Context, root string, c model.Config, p model.Plan) error {
	effective, err := EffectiveContextDigest(ctx, root, c)
	if err != nil {
		return err
	}
	if effective != p.ContextDigest {
		return errors.New("execution context drifted")
	}
	tools, err := toolsIdentity(ctx, c, root)
	if err != nil {
		return err
	}
	if tools != p.ToolsDigest {
		return errors.New("installed tool bytes differ from plan")
	}
	modules, err := checkoutModules(root, c)
	if err != nil {
		return err
	}
	return snapshot.ValidateCandidateWithModules(ctx, root, p.Candidate, modules)
}
