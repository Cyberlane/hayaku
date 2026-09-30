package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/Cyberlane/hayaku/internal/adapter"
	goprovider "github.com/Cyberlane/hayaku/internal/adapter/golang"
	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
	gorunner "github.com/Cyberlane/hayaku/internal/runner/golang"
	"github.com/Cyberlane/hayaku/internal/snapshot"
)

type CommandResult struct {
	Workspace   string           `json:"workspace"`
	Command     model.Command    `json:"command"`
	ExitCode    int              `json:"exit_code"`
	Duration    time.Duration    `json:"duration_ns"`
	OutputBytes int              `json:"output_bytes"`
	Go          *gorunner.Result `json:"go,omitempty"`
	Complete    bool             `json:"complete"`
}
type Execution struct {
	Schema        int             `json:"schema"`
	Candidate     string          `json:"candidate"`
	ContextDigest string          `json:"context_digest"`
	Mode          string          `json:"mode"`
	Commands      []CommandResult `json:"commands"`
	Passed        bool            `json:"passed"`
}

func executionEnv(c model.Context) map[string]string {
	env := map[string]string{}
	for key, value := range c.Env {
		env[key] = value
	}
	for key, value := range map[string]string{"GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off", "GOVCS": "*:off", "GONOPROXY": "", "GOTELEMETRY": "off", "GOFLAGS": "", "GOENV": "off", "GOOS": c.OS, "GOARCH": c.Arch, "CARGO_NET_OFFLINE": "true", "RUSTUP_AUTO_INSTALL": "0"} {
		env[key] = value
	}
	return env
}

// Execute is used only after fresh plan reconstruction, and enforces identity
// again here so callers cannot accidentally execute an unvalidated saved plan.
func Execute(ctx context.Context, root string, c model.Config, p model.Plan) (Execution, error) {
	result := Execution{Schema: model.Schema, Candidate: p.Candidate, ContextDigest: p.ContextDigest, Mode: "full", Commands: []CommandResult{}}
	if p.Mode != "full-fallback" || config.Digest(c) != p.ConfigDigest {
		return result, errors.New("execution identities differ from plan")
	}
	if err := validateExecution(ctx, root, c, p); err != nil {
		return result, err
	}
	if err := runWorkspaces(ctx, root, c, p.Selected, false, false, &result, func() error { return validateExecution(ctx, root, c, p) }); err != nil {
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
		if w.Adapter == "go" {
			copyWorkspace := w
			copyWorkspace.Command = command
			command, err = goprovider.Execution(copyWorkspace)
			if err != nil {
				return err
			}
			if fresh {
				command.Args = append(command.Args, "-count=1")
			}
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
			var output process.Output
			var runErr error
			if w.Adapter == "go" {
				env, envErr := goprovider.ExecutionEnvironment(ctx, root, w, c.Context)
				if envErr != nil {
					return envErr
				}
				output, runErr = process.RunEnvironment(ctx, cmd, dir, env)
			} else {
				output, runErr = process.Run(ctx, cmd, dir, executionEnv(c.Context))
			}
			entry := CommandResult{Workspace: w.ID, Command: cmd, ExitCode: output.ExitCode, Duration: output.Duration, OutputBytes: len(output.Stdout) + len(output.Stderr), Complete: runErr == nil}
			if i == len(commands)-1 && w.Adapter == "go" {
				expected := []string{}
				for _, u := range workspaceUnits {
					if u.Kind == "go-package" {
						expected = append(expected, u.Selector)
					}
				}
				sort.Strings(expected)
				goResult, reconcileErr := gorunner.Reconcile(bytes.NewReader(output.Stdout), expected)
				entry.Go = &goResult
				entry.Complete = reconcileErr == nil && output.Completed && (output.ExitCode == 0 || goResult.Failed)
				if reconcileErr != nil {
					entry.Complete = false
					if runErr == nil {
						runErr = reconcileErr
					}
				}
				if goResult.Failed && runErr == nil {
					runErr = errors.New("Go results report failure")
				}
			}
			result.Commands = append(result.Commands, entry)
			if revalidate != nil {
				if err := revalidate(); err != nil {
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

// Shadow uses separate raw-tree copies and fresh Go tests. It is a challenge
// harness, not a production qualification certificate. Native Git metadata and
// external services are not isolated by these copies and remain contract gaps.
func Shadow(ctx context.Context, root string, c model.Config, p model.Plan) (result ShadowResult, returnedErr error) {
	result = ShadowResult{Schema: model.Schema, Candidate: p.Candidate, ContextDigest: p.ContextDigest, ObservedMisses: []string{}, Inconsistencies: []string{}, Assurance: "observational-only; no production omission authority"}
	start := time.Now()
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
		if w.Adapter != "go" {
			return result, errors.New("shadow outcome reconciliation currently requires Go workspaces")
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
	result.Proposal = Execution{Schema: model.Schema, Candidate: p.Candidate, ContextDigest: p.ContextDigest, Mode: "shadow-proposal", Commands: []CommandResult{}}
	result.Full = Execution{Schema: model.Schema, Candidate: p.Candidate, ContextDigest: p.ContextDigest, Mode: "shadow-full", Commands: []CommandResult{}}
	proposalErr := runWorkspaces(ctx, pair.BaseDir, c, p.Proposed, true, true, &result.Proposal, nil)
	result.Proposal.Passed = proposalErr == nil
	fullErr := runWorkspaces(ctx, pair.CandidateDir, c, p.Selected, false, true, &result.Full, nil)
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
		proposedPackages[u.Workspace+":"+u.Selector] = true
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
		if cmd.Go == nil {
			continue
		}
		for _, test := range cmd.Go.Tests {
			key := cmd.Workspace + ":" + test.Package
			if proposedPackages[key] {
				id := key + "/" + test.Test
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
			if command.Go == nil && !command.Complete {
				result.ComparisonValid = false
			}
		}
	}
	for _, u := range p.Selected {
		if _, ok := full[u.Workspace+":"+u.Selector]; !ok {
			result.ComparisonValid = false
		}
	}
	for _, u := range p.Proposed {
		if _, ok := selected[u.Workspace+":"+u.Selector]; !ok {
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
		if command.Go == nil {
			continue
		}
		for _, outcome := range command.Go.Packages {
			result[command.Workspace+":"+outcome.Package] = outcome.Action
		}
		for _, outcome := range command.Go.Tests {
			result[command.Workspace+":"+outcome.Package+"/"+outcome.Test] = outcome.Action
		}
	}
	return result
}

func validateExecution(ctx context.Context, root string, c model.Config, p model.Plan) error {
	effective, err := EffectiveContextDigest(ctx, root, c)
	if err != nil {
		return err
	}
	if effective != p.ContextDigest {
		return errors.New("execution context drifted")
	}
	tools, err := toolsIdentity(c, root)
	if err != nil {
		return err
	}
	if tools != p.ToolsDigest {
		return errors.New("installed tool bytes differ from plan")
	}
	return snapshot.ValidateCandidate(ctx, root, p.Candidate)
}
