// hayaku-pilot measures one benign change in disposable local clones of Hayaku.
// It does not authorize production test omission or access remote repositories.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	goprovider "github.com/Cyberlane/hayaku/internal/adapter/golang"
	"github.com/Cyberlane/hayaku/internal/app"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
	gorunner "github.com/Cyberlane/hayaku/internal/runner/golang"
	"github.com/Cyberlane/hayaku/internal/snapshot"
)

const (
	mutationPath   = "internal/qualification/qualification.go"
	beforeMutation = `return outcome == "fail" || outcome == "timeout" || outcome == "build-fail"`
	afterMutation  = `return outcome == "build-fail" || outcome == "fail" || outcome == "timeout"`
)

type pairMeasurement struct {
	Index                    int              `json:"index"`
	CacheMode                string           `json:"cache_mode"`
	Passed                   bool             `json:"passed"`
	BaselineContext          string           `json:"baseline_context_digest"`
	ProposalContext          string           `json:"proposal_context_digest"`
	CloneDuration            time.Duration    `json:"baseline_clone_duration_ns"`
	WarmupDuration           time.Duration    `json:"warmup_duration_ns"`
	BaselineDuration         time.Duration    `json:"baseline_wall_duration_ns"`
	BaselineProcessDuration  time.Duration    `json:"baseline_process_duration_ns"`
	BaselineExitCode         int              `json:"baseline_exit_code"`
	Baseline                 gorunner.Result  `json:"baseline_outcomes"`
	PlanDuration             time.Duration    `json:"plan_build_duration_ns"`
	ShadowDuration           time.Duration    `json:"shadow_wall_duration_ns"`
	ProposalCommandsDuration time.Duration    `json:"proposal_command_duration_ns"`
	FullCommandsDuration     time.Duration    `json:"shadow_full_command_duration_ns"`
	ShadowOverhead           time.Duration    `json:"shadow_copy_validation_overhead_ns"`
	ExploratorySelectedCost  time.Duration    `json:"exploratory_selected_cost_ns"`
	Savings                  time.Duration    `json:"exploratory_net_savings_ns"`
	StorageDuration          time.Duration    `json:"artifact_storage_duration_ns"`
	PairDuration             time.Duration    `json:"pair_wall_duration_ns"`
	SelectedUnits            int              `json:"required_full_units"`
	ProposedUnits            int              `json:"proposed_units"`
	Plan                     model.Plan       `json:"plan"`
	Shadow                   app.ShadowResult `json:"shadow"`
}

type pilotReport struct {
	Schema              int               `json:"schema"`
	Version             string            `json:"version"`
	Project             string            `json:"project"`
	Base                string            `json:"base"`
	Candidate           string            `json:"candidate"`
	Change              string            `json:"change"`
	HostOS              string            `json:"host_os"`
	HostArch            string            `json:"host_arch"`
	CacheMode           string            `json:"cache_mode"`
	Iterations          int               `json:"iterations"`
	Passed              bool              `json:"passed"`
	Error               string            `json:"error,omitempty"`
	SetupDuration       time.Duration     `json:"setup_duration_ns"`
	CleanupDuration     time.Duration     `json:"cleanup_duration_ns"`
	TotalDuration       time.Duration     `json:"observed_process_duration_ns"`
	MedianBaseline      time.Duration     `json:"median_baseline_wall_duration_ns"`
	MedianSelected      time.Duration     `json:"median_exploratory_selected_cost_ns"`
	MedianSavings       time.Duration     `json:"median_paired_net_savings_ns"`
	MedianShadow        time.Duration     `json:"median_shadow_wall_duration_ns"`
	Pairs               []pairMeasurement `json:"pairs"`
	Assurance           string            `json:"assurance"`
	MeasurementBoundary string            `json:"measurement_boundary"`
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer) error {
	start := time.Now()
	flags := flag.NewFlagSet("hayaku-pilot", flag.ContinueOnError)
	rootFlag := flags.String("root", ".", "clean committed Hayaku repository root")
	destFlag := flags.String("output", "", "nonexisting report directory outside source")
	iterations := flags.Int("iterations", 2, "measurement pairs, between 1 and 20")
	cacheMode := flags.String("cache-mode", "warm", "warm or fresh private Go build caches")
	timeout := flags.Duration("timeout", time.Hour, "total pilot timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *destFlag == "" || *iterations < 1 || *iterations > 20 || (*cacheMode != "warm" && *cacheMode != "fresh") || *timeout <= 0 {
		return errors.New("usage: hayaku-pilot --root PATH --output NEW-DIRECTORY [--iterations 2] [--cache-mode warm|fresh]")
	}
	root, dest, err := locations(*rootFlag, *destFlag)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	query, err := localGit(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return err
	}
	base := strings.TrimSpace(string(query.Stdout))
	if err := snapshot.ValidateCandidate(ctx, root, base); err != nil {
		return err
	}
	private, err := os.MkdirTemp("", "hayaku-pilot-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(private)
	working := filepath.Join(private, "candidate")
	if err := clone(ctx, root, working, base); err != nil {
		return err
	}
	if err := mutate(working); err != nil {
		return err
	}
	if _, err := localGit(ctx, working, "add", "--", mutationPath); err != nil {
		return err
	}
	if _, err := localGit(ctx, working, "commit", "-m", "pilot: reorder pure terminal-outcome comparisons"); err != nil {
		return err
	}
	query, err = localGit(ctx, working, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	candidate := strings.TrimSpace(string(query.Stdout))
	if err := os.Mkdir(dest, 0700); err != nil {
		return err
	}
	report := pilotReport{Schema: 1, Version: app.Version, Project: "Hayaku", Base: base, Candidate: candidate, Change: "pure isFailure expression reorder in " + mutationPath, HostOS: runtime.GOOS, HostArch: runtime.GOARCH, CacheMode: *cacheMode, Iterations: *iterations, Pairs: []pairMeasurement{}, Assurance: "synthetic local pilot observations only; no production omission authority", MeasurementBoundary: "Private cache paths give baseline/proposal distinct context digests. Fresh means initially empty Go build cache, not cold OS/toolchain caches. Exploratory selected cost charges all plan-building plus proposal commands and combined shadow copy/validation overhead; full audit commands are reported separately. Setup/cleanup and archive storage are recorded; final report serialization and publication are outside measured totals."}
	report.SetupDuration = time.Since(start)
	var pilotErr error
	for i := 0; i < *iterations; i++ {
		measurement, err := measurePair(ctx, working, private, dest, base, candidate, i+1, *cacheMode)
		report.Pairs = append(report.Pairs, measurement)
		if err != nil {
			pilotErr = err
			break
		}
	}
	if err := snapshot.ValidateCandidate(ctx, root, base); err != nil && pilotErr == nil {
		pilotErr = fmt.Errorf("source changed during pilot: %w", err)
	}
	cleanupStart := time.Now()
	if err := os.RemoveAll(private); err != nil && pilotErr == nil {
		pilotErr = err
	}
	report.CleanupDuration = time.Since(cleanupStart)
	report.TotalDuration = time.Since(start)
	report.Passed = pilotErr == nil && len(report.Pairs) == *iterations
	if pilotErr != nil {
		report.Error = pilotErr.Error()
	}
	for _, field := range []struct {
		pick func(pairMeasurement) time.Duration
		dest *time.Duration
	}{{func(p pairMeasurement) time.Duration { return p.BaselineDuration }, &report.MedianBaseline}, {func(p pairMeasurement) time.Duration { return p.ExploratorySelectedCost }, &report.MedianSelected}, {func(p pairMeasurement) time.Duration { return p.Savings }, &report.MedianSavings}, {func(p pairMeasurement) time.Duration { return p.ShadowDuration }, &report.MedianShadow}} {
		values := []time.Duration{}
		for _, pair := range report.Pairs {
			if pair.Passed {
				values = append(values, field.pick(pair))
			}
		}
		*field.dest = median(values)
	}
	if err := writeJSON(filepath.Join(dest, "report.json"), report); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "Pilot report: %s; pairs=%d passed=%v\n", filepath.Join(dest, "report.json"), len(report.Pairs), report.Passed); err != nil {
		return err
	}
	return pilotErr
}

func measurePair(ctx context.Context, working, private, dest, base, candidate string, index int, cacheMode string) (measurement pairMeasurement, returnedErr error) {
	start := time.Now()
	measurement = pairMeasurement{Index: index, CacheMode: cacheMode}
	defer func() { measurement.PairDuration = time.Since(start) }()
	baselineRoot := filepath.Join(private, fmt.Sprintf("baseline-%d", index))
	cloneStart := time.Now()
	if err := clone(ctx, working, baselineRoot, candidate); err != nil {
		return measurement, err
	}
	measurement.CloneDuration = time.Since(cloneStart)
	baselineConfig := pilotConfig(filepath.Join(private, fmt.Sprintf("baseline-cache-%d", index)))
	proposalConfig := pilotConfig(filepath.Join(private, fmt.Sprintf("proposal-cache-%d", index)))
	for _, c := range []model.Config{baselineConfig, proposalConfig} {
		if err := os.Mkdir(c.Context.Env["GOCACHE"], 0700); err != nil {
			return measurement, err
		}
	}
	if cacheMode == "warm" {
		warmStart := time.Now()
		for _, arm := range []struct {
			root string
			c    model.Config
		}{{baselineRoot, baselineConfig}, {working, proposalConfig}} {
			if _, _, _, err := baseline(ctx, arm.root, arm.c, nil); err != nil {
				return measurement, fmt.Errorf("cache warmup failed: %w", err)
			}
		}
		measurement.WarmupDuration = time.Since(warmStart)
	}
	for _, dir := range []string{baselineRoot, working} {
		if err := snapshot.ValidateCandidate(ctx, dir, candidate); err != nil {
			return measurement, fmt.Errorf("warmup changed source: %w", err)
		}
	}
	planStart := time.Now()
	p, err := app.Build(ctx, working, base, candidate, proposalConfig, false)
	measurement.PlanDuration = time.Since(planStart)
	measurement.Plan = p
	if err != nil {
		return measurement, err
	}
	measurement.ProposalContext = p.ContextDigest
	measurement.BaselineContext, err = app.EffectiveContextDigest(ctx, baselineRoot, baselineConfig)
	if err != nil {
		return measurement, err
	}
	expected := []string{}
	for _, unit := range p.Selected {
		if unit.Kind == "go-package" {
			expected = append(expected, unit.Selector)
		}
	}
	sort.Strings(expected)
	measurement.SelectedUnits, measurement.ProposedUnits = len(p.Selected), len(p.Proposed)
	baselineStart := time.Now()
	measurement.Baseline, measurement.BaselineProcessDuration, measurement.BaselineExitCode, err = baseline(ctx, baselineRoot, baselineConfig, expected)
	if validationErr := snapshot.ValidateCandidate(ctx, baselineRoot, candidate); validationErr != nil && err == nil {
		err = fmt.Errorf("baseline changed source: %w", validationErr)
	}
	measurement.BaselineDuration = time.Since(baselineStart)
	if err != nil {
		return measurement, err
	}
	shadowStart := time.Now()
	measurement.Shadow, err = app.Shadow(ctx, working, proposalConfig, p)
	measurement.ShadowDuration = time.Since(shadowStart)
	for _, command := range measurement.Shadow.Proposal.Commands {
		measurement.ProposalCommandsDuration += command.Duration
	}
	for _, command := range measurement.Shadow.Full.Commands {
		measurement.FullCommandsDuration += command.Duration
	}
	measurement.ShadowOverhead = measurement.ShadowDuration - measurement.ProposalCommandsDuration - measurement.FullCommandsDuration
	if measurement.ShadowOverhead < 0 {
		return measurement, errors.New("shadow duration accounting is inconsistent")
	}
	measurement.ExploratorySelectedCost = measurement.PlanDuration + measurement.ProposalCommandsDuration + measurement.ShadowOverhead
	measurement.Savings = measurement.BaselineDuration - measurement.ExploratorySelectedCost
	measurement.Passed = err == nil && measurement.Shadow.ComparisonValid && measurement.Shadow.Proposal.Passed && measurement.Shadow.Full.Passed && len(measurement.Shadow.ObservedMisses) == 0
	storeStart := time.Now()
	for _, artifact := range []struct {
		name  string
		value any
	}{{fmt.Sprintf("plan-%02d.json", index), p}, {fmt.Sprintf("shadow-%02d.json", index), measurement.Shadow}, {fmt.Sprintf("baseline-%02d.json", index), measurement.Baseline}} {
		if storeErr := writeJSON(filepath.Join(dest, artifact.name), artifact.value); storeErr != nil {
			return measurement, storeErr
		}
	}
	measurement.StorageDuration = time.Since(storeStart)
	measurement.ExploratorySelectedCost += measurement.StorageDuration
	measurement.Savings = measurement.BaselineDuration - measurement.ExploratorySelectedCost
	if err != nil {
		return measurement, err
	}
	if !measurement.Passed {
		return measurement, errors.New("pilot comparison failed or was incomplete")
	}
	return measurement, nil
}

func baseline(ctx context.Context, root string, c model.Config, expected []string) (gorunner.Result, time.Duration, int, error) {
	command, err := goprovider.Execution(c.Workspaces[0])
	if err != nil {
		return gorunner.Result{}, 0, -1, err
	}
	env, err := goprovider.ExecutionEnvironment(ctx, root, c.Workspaces[0], c.Context)
	if err != nil {
		return gorunner.Result{}, 0, -1, err
	}
	output, runErr := process.RunEnvironment(ctx, command, root, env)
	if len(expected) == 0 {
		if runErr != nil || !output.Completed {
			return gorunner.Result{}, output.Duration, output.ExitCode, errors.New("full-suite cache warmup did not complete successfully")
		}
		return gorunner.Result{}, output.Duration, output.ExitCode, nil
	}
	result, reconcileErr := gorunner.Reconcile(bytes.NewReader(output.Stdout), expected)
	if runErr != nil || reconcileErr != nil || !output.Completed || output.ExitCode != 0 || result.Failed {
		return result, output.Duration, output.ExitCode, errors.New("full-suite baseline failed or was incomplete")
	}
	return result, output.Duration, output.ExitCode, nil
}

func pilotConfig(cache string) model.Config {
	return model.Config{Schema: model.Schema, Context: model.Context{ID: "hayaku-local-pilot", OS: runtime.GOOS, Arch: runtime.GOARCH, Env: map[string]string{"GOCACHE": cache}}, Workspaces: []model.Workspace{{ID: "hayaku", Root: ".", Adapter: "go", Command: model.Command{Dir: ".", Executable: "go", Args: []string{"test", "-count=1", "./..."}}}}}
}

func localGit(ctx context.Context, root string, args ...string) (process.Output, error) {
	env := []string{}
	for _, pair := range process.Environment(nil) {
		if !strings.HasPrefix(pair, "GIT_") {
			env = append(env, pair)
		}
	}
	env = append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_OPTIONAL_LOCKS=0")
	argv := []string{"--no-pager", "-C", root, "-c", "core.hooksPath=" + os.DevNull, "-c", "init.templateDir=", "-c", "commit.gpgsign=false", "-c", "user.name=Hayaku Pilot", "-c", "user.email=pilot@example.invalid"}
	return process.RunEnvironment(ctx, model.Command{Executable: "git", Args: append(argv, args...)}, root, env)
}

func clone(ctx context.Context, source, destination, id string) error {
	if !filepath.IsAbs(source) || !filepath.IsAbs(destination) {
		return errors.New("pilot clones require absolute local paths")
	}
	if _, err := localGit(ctx, source, "clone", "--no-hardlinks", "--no-local", "--no-checkout", "--no-tags", "--", source, destination); err != nil {
		return err
	}
	if _, err := localGit(ctx, destination, "checkout", "--detach", id); err != nil {
		return err
	}
	return snapshot.ValidateCandidate(ctx, destination, id)
}

func mutate(root string) error {
	path := filepath.Join(root, filepath.FromSlash(mutationPath))
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	module, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil || !bytes.Contains(module, []byte("module github.com/Cyberlane/hayaku\n")) {
		return errors.New("pilot only accepts the Hayaku fixture source")
	}
	if bytes.Count(data, []byte(beforeMutation)) != 1 {
		return errors.New("pilot mutation source token changed; review the fixture before running")
	}
	return os.WriteFile(path, bytes.Replace(data, []byte(beforeMutation), []byte(afterMutation), 1), 0600)
}

func locations(root, destination string) (string, string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", err
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return "", "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(destination))
	if err != nil {
		return "", "", errors.New("report parent must already exist")
	}
	destination = filepath.Join(parent, filepath.Base(destination))
	if relative, err := filepath.Rel(root, destination); err != nil || relative == "." || filepath.IsLocal(relative) {
		return "", "", errors.New("pilot output must be outside the source checkout")
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return "", "", errors.New("pilot output already exists or cannot be inspected")
	}
	return root, destination, nil
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(append(data, '\n'))
	return errors.Join(writeErr, file.Close())
}

func median(values []time.Duration) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	middle := len(values) / 2
	if len(values)%2 == 1 {
		return values[middle]
	}
	return values[middle-1] + (values[middle]-values[middle-1])/2
}
