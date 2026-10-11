package native

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/passcache"
	"github.com/Cyberlane/hayaku/internal/process"
)

// RunConfig retains the original packages-only Go configuration. Typed
// workspaces supply complete original commands and provider-specific operational
// inputs; those declarations never supply producer authority.
type RunConfig struct {
	Config
	Workspaces  []Workspace       `json:"workspaces,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
}

type Workspace struct {
	model.Workspace
	Vitest *VitestRuntimeInputs `json:"vitest,omitempty"`
}

// These paths name already provisioned inputs. Only a reviewed provider can
// admit their content, complete source scope and configured invocation.
type VitestRuntimeInputs struct {
	Node          string           `json:"node"`
	TypeScriptSDK string           `json:"typescript_sdk"`
	Loader        string           `json:"loader"`
	Libraries     []RuntimeLibrary `json:"libraries"`
	Addon         string           `json:"addon"`
}

type workspaceProvider func(context.Context, string, Workspace, RunConfig, ExecutionOptions) (Report, error)

// Registration is the sole ecosystem dispatch boundary. Selection, receipts,
// audits and aggregate completion stay shared. Unregistered scopes run in full.
var workspaceProviders = map[string]workspaceProvider{}

func RunWithOptions(ctx context.Context, root string, c RunConfig, options ExecutionOptions) (Report, error) {
	if len(c.Workspaces) == 0 {
		if len(c.Environment) != 0 {
			// Invalid audit requests still enter the audit-owning validation path,
			// which revokes this authority's history before rejecting zero scopes.
			return runWorkspaces(ctx, root, c, options, workspaceProviders)
		}
		return GoWithOptions(ctx, root, c.Config, options)
	}
	return runWorkspaces(ctx, root, c, options, workspaceProviders)
}

func validateWorkspaces(root string, c RunConfig) (string, error) {
	if c.Schema != 1 || len(c.Packages) != 0 || len(c.Workspaces) == 0 || len(c.Workspaces) > 256 ||
		(c.Platform != "linux" && c.Platform != "darwin") || (c.Architecture != "arm64" && c.Architecture != "amd64") || (c.Platform == "darwin" && c.Image != "") {
		return "", errors.New("invalid native workspace configuration")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("native source root must be a real directory")
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	workspaces := make([]model.Workspace, 0, len(c.Workspaces))
	for _, w := range c.Workspaces {
		if w.Vitest != nil && (w.Adapter != "vitest" || w.NodeRuntime == nil) {
			return "", errors.New("Vitest inputs require a Vitest workspace and Node runtime")
		}
		workspaces = append(workspaces, w.Workspace)
		path := filepath.Join(root, filepath.FromSlash(w.Root))
		physical, err := filepath.EvalSymlinks(path)
		if err != nil || physical != path {
			return "", errors.New("workspace roots require real directories without aliases")
		}
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() {
			return "", errors.New("workspace root unavailable")
		}
		cwd := filepath.Join(path, filepath.FromSlash(w.Command.Dir))
		physical, err = filepath.EvalSymlinks(cwd)
		if err != nil || physical != cwd {
			return "", errors.New("workspace command cwd requires a real directory")
		}
		for _, prerequisite := range w.Prerequisites {
			cwd := filepath.Join(path, filepath.FromSlash(prerequisite.Dir))
			physical, err := filepath.EvalSymlinks(cwd)
			if err != nil || physical != cwd {
				return "", errors.New("workspace prerequisite cwd requires a real directory")
			}
		}
	}
	if err := config.Validate(model.Config{Schema: 1, Context: model.Context{ID: "native", OS: runtime.GOOS, Arch: runtime.GOARCH, Env: c.Environment}, Workspaces: workspaces}); err != nil {
		return "", err
	}
	if err := validateNativeArguments(nil, process.Environment(c.Environment)); err != nil {
		return "", err
	}
	return root, nil
}

func runWorkspaces(ctx context.Context, root string, c RunConfig, options ExecutionOptions, providers map[string]workspaceProvider) (report Report, returnedErr error) {
	report = Report{Schema: 1, Contract: contractVersion, Groups: []Result{}}
	if options.Audit {
		owned := options.AuditHandle == nil
		audit, err := BeginAuditWithOptions(options)
		if err != nil {
			return report, err
		}
		options.AuditHandle = audit
		if owned {
			defer func() {
				if err := audit.Finish(returnedErr != nil || !report.Complete || !report.Passed); err != nil {
					report.Complete, report.Passed = false, false
					for i := range report.Groups {
						report.Groups[i].Complete, report.Groups[i].Passed, report.Groups[i].Qualified = false, false, false
					}
					returnedErr = errors.Join(returnedErr, err)
				}
			}()
		}
	}
	if c.Platform == "" {
		c.Platform = "linux"
	}
	root, err := validateWorkspaces(root, c)
	if err != nil {
		return report, err
	}
	if options.CacheDir != "" {
		cache, err := filepath.Abs(options.CacheDir)
		if err != nil {
			return report, err
		}
		rel, err := filepath.Rel(root, cache)
		if err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".git" && !strings.HasPrefix(rel, ".git"+string(filepath.Separator))) {
			return report, errors.New("native pass cache must be outside the source tree or inside .git")
		}
	}
	if options.Audit {
		audit := options.AuditHandle
		cache, err := passcache.OpenWithAuthority(options.CacheDir, "hayaku-native-pure-pass-v1", options.Authority)
		if err != nil {
			return report, err
		}
		err = errors.Join(audit.Observe(cache), cache.Close())
		if err != nil {
			return report, err
		}
		if err := audit.Revoke(); err != nil {
			return report, err
		}
	}
	if err := verifyWorkspaceInvocation(ctx, options); err != nil {
		return report, err
	}
	guards, err := retainScopeDirectories(ctx, root, c.Workspaces)
	if err != nil {
		return report, err
	}
	var finalizers []func(context.Context) error
	defer func() {
		for _, finalize := range finalizers {
			post, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			err := finalize(post)
			cancel()
			if err != nil {
				report.Complete, report.Passed = false, false
				returnedErr = errors.Join(returnedErr, err)
			}
		}
		if err := verifyWorkspaceInvocation(ctx, options); err != nil {
			report.Complete, report.Passed = false, false
			returnedErr = errors.Join(returnedErr, err)
		}
		if err := guards.verify(ctx); err != nil {
			report.Complete, report.Passed = false, false
			returnedErr = errors.Join(returnedErr, err)
		}
		if !report.Complete {
			report.Passed = false
			for i := range report.Groups {
				report.Groups[i].Complete, report.Groups[i].Passed, report.Groups[i].Qualified = false, false, false
			}
			returnedErr = errors.Join(returnedErr, RevokeWithOptions(options, report))
		}
	}()
	report.Complete, report.Passed = true, true
	seen := map[string]bool{}
	keys := map[string]bool{}
	for _, workspace := range c.Workspaces {
		if err := verifyWorkspaceInvocation(ctx, options); err != nil {
			returnedErr = errors.Join(returnedErr, err)
			report.Complete, report.Passed = false, false
			break
		}
		if err := guards.verify(ctx); err != nil {
			returnedErr = errors.Join(returnedErr, err)
			report.Complete, report.Passed = false, false
			break
		}
		provider := providers[workspace.Adapter]
		var value Report
		var err error
		if provider == nil {
			var group Result
			group, err = fullWorkspace(ctx, root, workspace, c.Environment, "workspace has no qualified native provider; executing complete original scope")
			value = Report{Complete: group.Complete, Passed: group.Passed, Groups: []Result{group}}
		} else {
			value, err = provider(ctx, root, workspace, c, options)
		}
		if value.finalize != nil {
			finalizers = append(finalizers, value.finalize)
		}
		if err := guards.verify(ctx); err != nil {
			value.Complete, value.Passed = false, false
			returnedErr = errors.Join(returnedErr, err)
		}
		if err != nil && value.Passed {
			value.Complete, value.Passed = false, false
		}
		if len(value.Groups) == 0 {
			value.Complete, value.Passed = false, false
			err = errors.Join(err, errors.New("workspace returned no required groups"))
		}
		for _, group := range value.Groups {
			identity := group.Name
			if !strings.HasPrefix(group.Name, workspace.ID+"::") || seen[identity] || (!group.Executed && !group.Reused) || (group.Executed && group.Reused) || (group.Qualified && (group.Key == "" || keys[group.Key] || value.finalize == nil)) || (group.Reused && (!group.Qualified || !group.Complete || !group.Passed || group.Key == "")) || (group.Passed && !group.Complete) {
				value.Complete, value.Passed = false, false
				err = errors.Join(err, errors.New("workspace group inventory or lifecycle incomplete"))
			}
			seen[identity] = true
			if group.Key != "" {
				keys[group.Key] = true
			}
			report.Groups = append(report.Groups, group)
			value.Complete = value.Complete && group.Complete
			value.Passed = value.Passed && group.Passed
		}
		report.Complete = report.Complete && value.Complete
		report.Passed = report.Passed && value.Passed
		returnedErr = errors.Join(returnedErr, err)
	}
	if err := verifyWorkspaceInvocation(ctx, options); err != nil {
		report.Complete, report.Passed = false, false
		returnedErr = errors.Join(returnedErr, err)
	}
	if !report.Complete {
		report.Passed = false
		returnedErr = errors.Join(returnedErr, RevokeWithOptions(options, report))
	}
	return report, returnedErr
}

func verifyWorkspaceInvocation(ctx context.Context, options ExecutionOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if options.Authority != nil {
		return options.Authority.Verify(ctx)
	}
	return nil
}

func fullWorkspace(ctx context.Context, root string, w Workspace, env map[string]string, reason string) (group Result, returnedErr error) {
	group = Result{Name: w.ID + "::suite", Context: "original-host", Executed: true, Cache: "disabled", Reason: reason}
	directory := filepath.Join(root, filepath.FromSlash(w.Root))
	guards, err := retainScopeDirectories(ctx, root, []Workspace{w})
	if err != nil {
		return group, err
	}
	defer func() {
		if err := guards.verify(ctx); err != nil {
			group.Complete, group.Passed, group.Outcomes.Complete = false, false, false
			returnedErr = errors.Join(returnedErr, err)
		}
	}()
	for _, prerequisite := range w.Prerequisites {
		if err := guards.verify(ctx); err != nil {
			return group, err
		}
		out, err := process.Run(ctx, prerequisite, filepath.Join(directory, filepath.FromSlash(prerequisite.Dir)), env)
		if err != nil || !out.Completed || out.ExitCode != 0 {
			return group, errors.Join(err, errors.New("original workspace prerequisite failed"))
		}
		if err := guards.verify(ctx); err != nil {
			return group, err
		}
	}
	if err := guards.verify(ctx); err != nil {
		return group, err
	}
	out, err := process.Run(ctx, w.Command, filepath.Join(directory, filepath.FromSlash(w.Command.Dir)), env)
	group.Complete = nativeExecutionCompleted(out, err)
	group.Passed = group.Complete && out.ExitCode == 0
	action := "fail"
	if group.Passed {
		action = "pass"
	}
	group.Outcomes = model.Outcomes{Complete: group.Complete, Failed: !group.Passed, Units: []model.Outcome{{Unit: group.Name, Action: action}}}
	return group, err
}

type scopeDirectory struct {
	path string
	info os.FileInfo
}
type scopeDirectories []scopeDirectory

func retainScopeDirectories(ctx context.Context, root string, workspaces []Workspace) (scopeDirectories, error) {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, errors.New("original scope root unavailable")
	}
	paths := []string{root}
	for _, w := range workspaces {
		dir := filepath.Join(root, filepath.FromSlash(w.Root))
		paths = append(paths, dir, filepath.Join(dir, filepath.FromSlash(w.Command.Dir)))
		for _, command := range w.Prerequisites {
			paths = append(paths, filepath.Join(dir, filepath.FromSlash(command.Dir)))
		}
	}
	slices.Sort(paths)
	paths = slices.Compact(paths)
	guards := make(scopeDirectories, 0, len(paths))
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		physical, err := filepath.EvalSymlinks(path)
		info, statErr := os.Lstat(path)
		if err != nil || statErr != nil || physical != path || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("original scope directory ownership unavailable")
		}
		guards = append(guards, scopeDirectory{path, info})
	}
	return guards, nil
}

func (guards scopeDirectories) verify(ctx context.Context) error {
	for _, guard := range guards {
		if err := ctx.Err(); err != nil {
			return err
		}
		physical, err := filepath.EvalSymlinks(guard.path)
		info, statErr := os.Lstat(guard.path)
		if err != nil || statErr != nil || physical != guard.path || !info.IsDir() || info.Mode() != guard.info.Mode() || !os.SameFile(info, guard.info) {
			return errors.New("original scope directory changed")
		}
	}
	return nil
}
