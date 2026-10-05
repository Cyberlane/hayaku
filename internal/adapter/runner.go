package adapter

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Cyberlane/hayaku/internal/adapter/cargo"
	goanalysis "github.com/Cyberlane/hayaku/internal/adapter/golang"
	nativeanalysis "github.com/Cyberlane/hayaku/internal/adapter/native"
	"github.com/Cyberlane/hayaku/internal/adapter/nextest"
	"github.com/Cyberlane/hayaku/internal/adapter/pytest"
	swiftanalysis "github.com/Cyberlane/hayaku/internal/adapter/swift"
	vitestanalysis "github.com/Cyberlane/hayaku/internal/adapter/vitest"
	xcodeanalysis "github.com/Cyberlane/hayaku/internal/adapter/xcode"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/runner"
	goresults "github.com/Cyberlane/hayaku/internal/runner/golang"
	nativeresults "github.com/Cyberlane/hayaku/internal/runner/native"
	vitestresults "github.com/Cyberlane/hayaku/internal/runner/vitest"
)

// The composition table is the only runner dispatch. Analysis and execution
// translate native semantics; graph traversal and assurance stay in the core.
type provider struct {
	analyze       func(context.Context, string, model.Workspace, model.Context) (model.Evidence, error)
	selectCommand func(model.Workspace, []model.Unit) (model.Command, error)
	runner        bridge
}
type bridge struct {
	prepare         func(model.Workspace, model.Command, bool) (model.Command, error)
	environment     func(context.Context, string, model.Workspace, model.Context) ([]string, error)
	invoke          func(context.Context, invocation) (RunResult, error)
	decode          func(process.Output, invocation) (RunResult, error)
	prepareSnapshot func(context.Context, string, model.Workspace) error
	bound           bool
	observe         bool
	nodeEntry       string
	nodeDirect      bool
}

var providers = map[string]provider{
	"go":         {analyze: goanalysis.Discover, selectCommand: goanalysis.Proposal, runner: bridge{prepare: prepareGo, environment: goanalysis.ExecutionEnvironment, decode: decodeGo}},
	"cargo":      {analyze: cargo.Discover, selectCommand: cargo.Proposal},
	"nextest":    {analyze: nextest.Discover, selectCommand: nextest.Proposal},
	"pytest":     {analyze: pytest.Discover, selectCommand: pytest.Proposal, runner: bridge{invoke: invokeNative, decode: decodeNative}},
	"vitest":     {analyze: vitestanalysis.Discover, selectCommand: vitestanalysis.Proposal, runner: bridge{invoke: invokeVitest, decode: decodeVitest, bound: true, observe: true, nodeEntry: "vitest/vitest.mjs"}},
	"swift":      {analyze: swiftanalysis.Describe, selectCommand: swiftanalysis.Proposal, runner: bridge{invoke: invokeSwift}},
	"xcode":      {runner: bridge{invoke: invokeXcode, prepareSnapshot: xcodeanalysis.PrepareSnapshot}},
	"node-test":  {analyze: nativeanalysis.Discover, selectCommand: nativeanalysis.Proposal, runner: bridge{invoke: invokeNative, decode: decodeNative, nodeDirect: true}},
	"unittest":   {analyze: nativeanalysis.Discover, selectCommand: nativeanalysis.Proposal, runner: bridge{invoke: invokeNative, decode: decodeNative}},
	"jest":       {analyze: nativeanalysis.Discover, selectCommand: nativeanalysis.Proposal, runner: bridge{invoke: invokeNative, decode: decodeNative, nodeEntry: "jest/bin/jest.js"}},
	"playwright": {analyze: nativeanalysis.Discover, selectCommand: nativeanalysis.Proposal, runner: bridge{invoke: invokeNative, decode: decodeNative, nodeEntry: "@playwright/test/cli.js"}},
	"command":    {runner: bridge{nodeDirect: true}},
}

type RunOptions struct {
	Proposal     bool
	Observe      bool
	Prerequisite bool
	Version      string
}
type invocation struct {
	root, directory string
	workspace       model.Workspace
	context         model.Context
	command         model.Command
	units           []model.Unit
	options         RunOptions
}

// Legacy fields preserve existing report consumers. Only Outcomes and Complete
// are used by the shared execution coordinator for new runner decisions.
type RunResult struct {
	Output       process.Output
	Outcomes     *model.Outcomes
	Complete     bool
	Go           *goresults.Result
	Vitest       *vitestresults.Result
	Native       *nativeresults.Result
	RuntimeTrace *vitestanalysis.Trace
}

func PrepareCommand(w model.Workspace, command model.Command, fresh bool) (model.Command, error) {
	if prepare := providers[w.Adapter].runner.prepare; prepare != nil {
		return prepare(w, command, fresh)
	}
	return command, nil
}

func PrepareSnapshot(ctx context.Context, root string, w model.Workspace) error {
	if prepare := providers[w.Adapter].runner.prepareSnapshot; prepare != nil {
		return prepare(ctx, root, w)
	}
	return nil
}

func SupportsShadow(w model.Workspace) bool {
	b := providers[w.Adapter].runner
	return (b.decode != nil || (b.invoke != nil && b.prepareSnapshot == nil)) && (!b.bound || w.NodeRuntime != nil)
}
func SupportsObserve(w model.Workspace) bool {
	b := providers[w.Adapter].runner
	return b.observe && (!b.bound || w.NodeRuntime != nil)
}

// ValidateNodeRuntime checks the entrypoint declared by each Node bridge. The
// shared snapshot coordinator does not need framework names or CLI layouts.
func ValidateNodeRuntime(root string, w model.Workspace) error {
	if w.NodeRuntime == nil {
		return errors.New("Node runtime binding required")
	}
	b := providers[w.Adapter].runner
	if !b.nodeDirect && b.nodeEntry == "" {
		return errors.New("runner has no reviewed Node entrypoint binding")
	}
	name := w.Command.Executable
	if !filepath.IsAbs(name) {
		if strings.ContainsAny(name, "/\\") {
			name = filepath.Join(root, w.Root, w.Command.Dir, name)
		} else {
			var err error
			name, err = exec.LookPath(name)
			if err != nil {
				return errors.New("configured Vitest runner is unavailable")
			}
		}
	}
	actual, err := filepath.EvalSymlinks(name)
	if err != nil {
		return errors.New("configured Vitest runner is unavailable")
	}
	entry := filepath.Join(w.NodeRuntime.Modules, filepath.FromSlash(b.nodeEntry))
	if b.nodeDirect {
		entry = w.NodeRuntime.Node
	}
	expected, err := filepath.EvalSymlinks(entry)
	if err != nil || actual != expected {
		if b.nodeDirect {
			return errors.New("configured Node runner differs from bound runtime")
		}
		return errors.New("configured Vitest runner differs from the bound installation")
	}
	return nil
}

// EnvironmentIdentity returns private runner context for hashing, never output.
func EnvironmentIdentity(ctx context.Context, root string, w model.Workspace, c model.Context) ([]string, error) {
	environment := providers[w.Adapter].runner.environment
	if environment == nil {
		return nil, nil
	}
	env, err := environment(ctx, root, w, c)
	if err != nil {
		return nil, err
	}
	for i, pair := range env {
		key, value, _ := strings.Cut(pair, "=")
		if key == "GOWORK" && strings.HasPrefix(value, root+string(filepath.Separator)) {
			env[i] = key + "=<repository>" + strings.TrimPrefix(value, root)
		}
	}
	return env, nil
}

func ResultVersion(id, evidenceVersion string) string {
	// The native bridge's wire protocol namespaces its producer version.
	if providers[id].runner.decode == nil {
		return ""
	}
	for _, part := range strings.Split(evidenceVersion, ";") {
		if value, ok := strings.CutPrefix(part, id+"="); ok {
			return value
		}
	}
	return ""
}

// Run owns process completion and normalized validation once for all bridges.
// It never selects scopes or grants native omission authority.
func Run(ctx context.Context, root, directory string, w model.Workspace, c model.Context, command model.Command, units []model.Unit, options RunOptions) (RunResult, error) {
	input := invocation{root, directory, w, c, command, units, options}
	b := providers[w.Adapter].runner
	var result RunResult
	var err error
	if !options.Prerequisite && b.invoke != nil {
		result, err = b.invoke(ctx, input)
	} else if b.environment != nil {
		var env []string
		env, err = b.environment(ctx, root, w, c)
		if err == nil {
			result.Output, err = process.RunEnvironment(ctx, command, directory, env)
		}
	} else {
		result.Output, err = process.Run(ctx, command, directory, executionEnvironment(c))
	}
	if !options.Prerequisite && b.decode != nil {
		decoded, decodeErr := b.decode(result.Output, input)
		decoded.Output, decoded.RuntimeTrace = result.Output, result.RuntimeTrace
		result = decoded
		if decodeErr != nil {
			err = decodeErr
		}
	}
	result.Complete = err == nil && result.Output.Completed
	if result.Outcomes != nil {
		validation := runner.Validate(*result.Outcomes, units, result.Output.Completed, result.Output.ExitCode)
		result.Complete = validation == nil && (err == nil || (result.Output.ExitCode != 0 && runner.Failed(*result.Outcomes)))
		if validation != nil {
			err = validation
		} else if runner.Failed(*result.Outcomes) {
			err = errors.New("runner results report failure")
		}
	}
	if options.Observe && !options.Prerequisite && (result.RuntimeTrace == nil || !result.RuntimeTrace.Complete) {
		result.Complete = false
		err = errors.New("runtime observation transport incomplete")
	}
	return result, err
}

func prepareGo(w model.Workspace, command model.Command, fresh bool) (model.Command, error) {
	w.Command = command
	command, err := goanalysis.Execution(w)
	if err == nil && fresh {
		command.Args = append(command.Args, "-count=1")
	}
	return command, err
}
func decodeGo(output process.Output, input invocation) (RunResult, error) {
	selectors := make([]string, 0, len(input.units))
	identities := map[string]string{}
	for _, unit := range input.units {
		selectors = append(selectors, unit.Selector)
		identities[unit.Selector] = unit.ID
	}
	sort.Strings(selectors)
	native, err := goresults.Reconcile(bytes.NewReader(output.Stdout), selectors)
	common := &model.Outcomes{Complete: err == nil, Failed: native.Failed}
	for _, item := range native.Packages {
		common.Units = append(common.Units, model.Outcome{Unit: identities[item.Package], Action: item.Action})
	}
	for _, item := range native.Tests {
		common.Tests = append(common.Tests, model.Outcome{Unit: identities[item.Package], Test: item.Test, Action: item.Action})
	}
	return RunResult{Go: &native, Outcomes: common}, err
}
func decodeVitest(output process.Output, input invocation) (RunResult, error) {
	native, err := vitestresults.Reconcile(bytes.NewReader(output.Stdout), input.units)
	common := &model.Outcomes{Complete: err == nil, Failed: native.Failed, Errors: native.UnhandledErrors}
	for _, item := range native.Files {
		common.Units = append(common.Units, model.Outcome{Unit: item.Unit, Action: item.Action})
	}
	for _, item := range native.Tests {
		common.Tests = append(common.Tests, model.Outcome{Unit: item.Unit, Test: item.Test, Action: item.Action})
	}
	return RunResult{Vitest: &native, Outcomes: common}, err
}
func decodeNative(output process.Output, input invocation) (RunResult, error) {
	native, err := nativeresults.Reconcile(bytes.NewReader(output.Stdout), input.workspace.Adapter, input.options.Version, input.units)
	return nativeResult(native, err == nil), err
}
func nativeResult(native nativeresults.Result, complete bool) RunResult {
	common := &model.Outcomes{Complete: complete, Failed: native.Failed, Errors: native.UnhandledErrors}
	for _, item := range native.Units {
		common.Units = append(common.Units, model.Outcome{Unit: item.Unit, Action: item.Action})
	}
	for _, item := range native.Tests {
		common.Tests = append(common.Tests, model.Outcome{Unit: item.Unit, Test: item.Test, Action: item.Action})
	}
	return RunResult{Native: &native, Outcomes: common}
}
func selected(input invocation) []model.Unit {
	if input.options.Proposal {
		return input.units
	}
	return nil
}
func invokeNative(ctx context.Context, input invocation) (RunResult, error) {
	out, err := nativeanalysis.Execute(ctx, input.root, input.workspace, input.context, selected(input))
	return RunResult{Output: out}, err
}
func invokeVitest(ctx context.Context, input invocation) (RunResult, error) {
	if input.options.Observe {
		out, trace, err := vitestanalysis.Observe(ctx, input.root, input.workspace, input.context)
		return RunResult{Output: out, RuntimeTrace: &trace}, err
	}
	out, err := vitestanalysis.Execute(ctx, input.root, input.workspace, input.context, selected(input))
	return RunResult{Output: out}, err
}
func invokeSwift(ctx context.Context, input invocation) (RunResult, error) {
	out, native, err := swiftanalysis.Execute(ctx, input.root, input.workspace, input.context, input.units, input.options.Proposal)
	result := nativeResult(native, native.Complete)
	result.Output = out
	return result, err
}
func invokeXcode(ctx context.Context, input invocation) (RunResult, error) {
	out, native, err := xcodeanalysis.Execute(ctx, input.root, input.workspace, input.context)
	result := nativeResult(native, native.Complete)
	// Xcode is a full-command integration; its independently reconciled cases
	// belong to the original suite rather than a claimed target inventory.
	if len(input.units) == 1 {
		action := "pass"
		if native.Failed {
			action = "fail"
		}
		result.Outcomes.Units = []model.Outcome{{Unit: input.units[0].ID, Action: action}}
		for i := range result.Outcomes.Tests {
			item := &result.Outcomes.Tests[i]
			item.Test, item.Unit = item.Unit+"/"+item.Test, input.units[0].ID
		}
	}
	result.Output = out
	return result, err
}

func executionEnvironment(c model.Context) map[string]string {
	env := map[string]string{}
	for key, value := range c.Env {
		env[key] = value
	}
	for key, value := range map[string]string{"GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off", "GOVCS": "*:off", "GONOPROXY": "", "GOTELEMETRY": "off", "GOFLAGS": "", "GOENV": "off", "GOOS": c.OS, "GOARCH": c.Arch, "CARGO_NET_OFFLINE": "true", "RUSTUP_AUTO_INSTALL": "0"} {
		env[key] = value
	}
	return env
}
