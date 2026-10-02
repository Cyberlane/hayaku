// Package pilot measures Hayaku's own separate Go/WASI suite contract. It never
// asserts equivalence with the original native Go suite.
package pilot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Cyberlane/hayaku/internal/capsule"
	"github.com/Cyberlane/hayaku/internal/metrics"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/process"
	"github.com/Cyberlane/hayaku/internal/snapshot"
)

type Result struct {
	Schema          int                `json:"schema"`
	Contract        string             `json:"contract"`
	CompilerCache   string             `json:"compiler_cache"`
	Baseline        metrics.Run        `json:"baseline"`
	Candidate       metrics.Run        `json:"candidate"`
	Comparison      metrics.Comparison `json:"comparison"`
	BaselineResult  capsule.Result     `json:"baseline_result"`
	CandidateResult capsule.Result     `json:"candidate_result"`
}

// Run compiles both measurements from independent immutable snapshots, counting
// snapshotting, identity checks and compilation in each measured wall duration.
// Existing upstream Go compilation-cache states are not attributed to Hayaku.
func Run(ctx context.Context, root, packagePath, cache string) (Result, error) {
	result := Result{Schema: 1, Contract: "separate-deterministic-go-wasi-suite-v1", CompilerCache: "independent-fresh"}
	if !strings.HasPrefix(packagePath, "./") || packagePath == "./" || strings.Contains(packagePath, "...") || strings.ContainsAny(packagePath, "\\\x00\n") || !filepath.IsLocal(strings.TrimPrefix(packagePath, "./")) || filepath.ToSlash(filepath.Clean(packagePath)) != strings.TrimPrefix(packagePath, "./") {
		return result, errors.New("pilot requires one normalized repository-relative Go package")
	}
	if cache == "" {
		return result, errors.New("pilot requires an explicit private cache directory")
	}
	if !filepath.IsAbs(cache) {
		cache = filepath.Join(root, cache)
	}
	for index := 0; index < 2; index++ {
		start := time.Now()
		goPath, err := exec.LookPath("go")
		if err != nil {
			return result, errors.New("pilot requires an installed Go compiler")
		}
		compilerDigest, err := compilerIdentity(ctx, goPath)
		if err != nil {
			return result, err
		}
		version, err := process.Run(ctx, model.Command{Executable: goPath, Args: []string{"version"}}, root, offlineGoEnvironment())
		if err != nil || version.ExitCode != 0 {
			return result, errors.New("pilot compiler version is unavailable")
		}
		pair, err := snapshot.Capture(ctx, root, "HEAD", "HEAD")
		if err != nil {
			return result, err
		}
		measurement, guest, err := runMeasurement(ctx, root, pair, goPath, compilerDigest, strings.TrimSpace(string(version.Stdout)), packagePath, cache, index == 0, start)
		closeErr := pair.Close()
		if err != nil {
			return result, err
		}
		if closeErr != nil {
			return result, closeErr
		}
		end := time.Since(start).Nanoseconds()
		measurement.Phases = append(measurement.Phases, metrics.Phase{ID: "final-validation-and-cleanup", Kind: "hayaku", StartNanos: measurement.WallNanos, DurationNanos: end - measurement.WallNanos})
		measurement.WallNanos = end
		if index == 0 {
			result.Baseline, result.BaselineResult = measurement, guest
		} else {
			result.Candidate, result.CandidateResult = measurement, guest
		}
	}
	comparison, err := metrics.Compare(result.Baseline, result.Candidate)
	result.Comparison = comparison
	return result, err
}

func runMeasurement(ctx context.Context, root string, pair *snapshot.Pair, compiler, compilerDigest, version, packagePath, cache string, audit bool, start time.Time) (metrics.Run, capsule.Result, error) {
	var measurement metrics.Run
	var guest capsule.Result
	if err := snapshot.ValidateCandidate(ctx, root, pair.Candidate); err != nil {
		return measurement, guest, err
	}
	source, err := snapshot.Digest(ctx, pair.CandidateDir)
	if err != nil {
		return measurement, guest, err
	}
	dir, err := os.MkdirTemp("", "hayaku-wasi-pilot-")
	if err != nil {
		return measurement, guest, err
	}
	defer os.RemoveAll(dir)
	modulePath := filepath.Join(dir, "suite.wasm")
	flags := []string{"test", "-c", "-trimpath", "-buildvcs=false", "-o", modulePath, packagePath}
	env := offlineGoEnvironment()
	for k, v := range map[string]string{"GOOS": "wasip1", "GOARCH": "wasm", "CGO_ENABLED": "0", "GOFLAGS": "", "GOTOOLDIR": "", "GOCACHE": filepath.Join(dir, "fresh-go-cache")} {
		env[k] = v
	}
	build, err := process.Run(ctx, model.Command{Executable: compiler, Args: flags}, pair.CandidateDir, env)
	if err != nil || build.ExitCode != 0 {
		return measurement, guest, errors.New("pilot WASI compilation failed; native suite remains required")
	}
	currentCompiler, identityErr := compilerIdentity(ctx, compiler)
	if identityErr != nil || currentCompiler != compilerDigest {
		return measurement, guest, errors.New("pilot compiler changed during compilation")
	}
	currentSource, identityErr := snapshot.Digest(ctx, pair.CandidateDir)
	if identityErr != nil || currentSource != source {
		return measurement, guest, errors.New("pilot immutable source changed during compilation")
	}
	files, err := capsule.LoadFiles(ctx, dir, []string{"suite.wasm"})
	if err != nil {
		return measurement, guest, err
	}
	identity, _ := json.Marshal([]string{source, compilerDigest, version, packagePath, "go test -c -trimpath -buildvcs=false;wasip1/wasm;GOWORK=off;offline"})
	runnerStart := time.Since(start).Nanoseconds()
	guest, err = capsule.Run(ctx, capsule.Request{Name: packagePath, Module: files[0].Data, Args: []string{"suite.wasm", "-test.v=false"}, ProducerIdentity: hash(identity)}, capsule.Options{CacheDir: cache, NoReuse: audit})
	end := time.Since(start).Nanoseconds()
	if err != nil {
		return measurement, guest, err
	}
	execution := "executed"
	phase := "runner"
	if guest.Mode == "reused" {
		execution = "hayaku-reused"
		phase = "hayaku"
	}
	outcome := "incomplete"
	if guest.Complete && guest.Qualified {
		outcome = "failed"
		if guest.Passed {
			outcome = "passed"
		}
	}
	measurement = metrics.Run{Context: metrics.Context{SourceDigest: source, InputsDigest: guest.InputDigest, RunnerDigest: hash([]byte(guest.ModuleDigest + guest.BackendDigest)), EnvironmentDigest: hash([]byte("explicit-empty-WASI-env;logical-clock;seed-zero;independent-fresh-Go-cache;compiler=" + compilerDigest)), Platform: runtime.GOOS + "/" + runtime.GOARCH, SuiteSetDigest: hash([]byte(packagePath))}, WallNanos: end, Phases: []metrics.Phase{{ID: "capture-and-compile", Kind: "hayaku", StartNanos: 0, DurationNanos: runnerStart}, {ID: "capsule", Kind: phase, StartNanos: runnerStart, DurationNanos: end - runnerStart}}, Suites: []metrics.SuiteEvidence{{ID: packagePath, Execution: execution, Outcome: outcome}}}
	if err := snapshot.ValidateCandidate(ctx, root, pair.Candidate); err != nil {
		return measurement, guest, err
	}
	return measurement, guest, nil
}

func offlineGoEnvironment() map[string]string {
	return map[string]string{"GOENV": "off", "GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off", "GOWORK": "off", "GONOPROXY": "none", "GOPRIVATE": "", "GOVCS": "*:off", "GOCACHEPROG": ""}
}

func compilerIdentity(ctx context.Context, name string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f, err := os.Open(name)
	if err != nil {
		return "", errors.New("pilot compiler identity is unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 256<<20 {
		return "", errors.New("pilot compiler identity is unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(f, (256<<20)+1))
	if err != nil || len(data) > 256<<20 {
		return "", errors.New("pilot compiler identity is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return hash(data), nil
}

func hash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
