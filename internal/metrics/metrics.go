// Package metrics compares measured test runs without attributing upstream cache
// hits to Hayaku. Measurements do not establish execution qualification.
package metrics

import (
	"errors"
	"sort"
	"strings"
)

const (
	MaxDurationNanos int64 = 24 * 60 * 60 * 1e9
	MaxSuites              = 50000
	MaxPhases              = 10000
)

// Context identifies the exact experiment. A full baseline for a different
// source, input envelope, runner or environment cannot substantiate savings.
type Context struct {
	SourceDigest      string `json:"source_digest"`
	InputsDigest      string `json:"inputs_digest"`
	RunnerDigest      string `json:"runner_digest"`
	EnvironmentDigest string `json:"environment_digest"`
	Platform          string `json:"platform"`
	SuiteSetDigest    string `json:"suite_set_digest"`
}

// Phase is a measured nonoverlapping wall-clock interval. Kind is hayaku,
// runner or other. Suite timings are intentionally not summed: tests may run
// concurrently, while the measured wall duration includes every phase.
type Phase struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	StartNanos    int64  `json:"start_nanos"`
	DurationNanos int64  `json:"duration_nanos"`
}

// SuiteEvidence describes an observed result. Reuse eligibility must have been
// checked by the execution backend; this package cannot authorize reuse.
type SuiteEvidence struct {
	ID        string `json:"id"`
	Execution string `json:"execution"`
	Outcome   string `json:"outcome"`
}

type Run struct {
	Context   Context         `json:"context"`
	WallNanos int64           `json:"wall_nanos"`
	Phases    []Phase         `json:"phases"`
	Suites    []SuiteEvidence `json:"suites"`
}

type Summary struct {
	WallNanos           int64 `json:"wall_nanos"`
	HayakuOverheadNanos int64 `json:"hayaku_overhead_nanos"`
	RunnerNanos         int64 `json:"runner_nanos"`
	Executed            int   `json:"executed"`
	HayakuReused        int   `json:"hayaku_reused"`
	UpstreamCached      int   `json:"upstream_cached"`
	Passed              int   `json:"passed"`
	Failed              int   `json:"failed"`
	Incomplete          int   `json:"incomplete"`
}

type Comparison struct {
	Valid                   bool    `json:"valid"`
	Reason                  string  `json:"reason,omitempty"`
	Baseline                Summary `json:"baseline"`
	Candidate               Summary `json:"candidate"`
	NetSavingsNanos         int64   `json:"net_savings_nanos"`
	GrossRunnerSavingsNanos int64   `json:"gross_runner_savings_nanos"`
}

func boundedIdentity(value string) bool {
	return value != "" && len(value) <= 1024 && !strings.ContainsAny(value, "\x00\r\n")
}

// Summarize validates evidence and preserves failures, overhead and cache states.
// The intervals may leave unclassified wall time, which still counts in cost.
func Summarize(run Run) (Summary, error) {
	if run.WallNanos < 0 || run.WallNanos > MaxDurationNanos || len(run.Phases) > MaxPhases || len(run.Phases) == 0 || len(run.Suites) == 0 || len(run.Suites) > MaxSuites {
		return Summary{}, errors.New("invalid measurement bounds")
	}
	for _, identity := range []string{run.Context.SourceDigest, run.Context.InputsDigest, run.Context.RunnerDigest, run.Context.EnvironmentDigest, run.Context.Platform, run.Context.SuiteSetDigest} {
		if !boundedIdentity(identity) {
			return Summary{}, errors.New("measurement context must be complete")
		}
	}
	result := Summary{WallNanos: run.WallNanos}
	phases := append([]Phase(nil), run.Phases...)
	sort.Slice(phases, func(i, j int) bool {
		if phases[i].StartNanos != phases[j].StartNanos {
			return phases[i].StartNanos < phases[j].StartNanos
		}
		return phases[i].ID < phases[j].ID
	})
	phaseNames := map[string]bool{}
	end := int64(0)
	for _, phase := range phases {
		if !boundedIdentity(phase.ID) || phaseNames[phase.ID] || phase.StartNanos < 0 || phase.StartNanos > run.WallNanos || phase.DurationNanos < 0 || phase.DurationNanos > run.WallNanos-phase.StartNanos || phase.StartNanos < end {
			return Summary{}, errors.New("invalid, overlapping or duplicate measurement phase")
		}
		phaseNames[phase.ID] = true
		end = phase.StartNanos + phase.DurationNanos
		switch phase.Kind {
		case "hayaku":
			result.HayakuOverheadNanos += phase.DurationNanos
		case "runner":
			result.RunnerNanos += phase.DurationNanos
		case "other":
		default:
			return Summary{}, errors.New("unknown measurement phase kind")
		}
	}
	suiteNames := map[string]bool{}
	for _, suite := range run.Suites {
		if !boundedIdentity(suite.ID) || suiteNames[suite.ID] {
			return Summary{}, errors.New("invalid or duplicate measurement suite")
		}
		suiteNames[suite.ID] = true
		switch suite.Execution {
		case "executed":
			result.Executed++
		case "hayaku-reused":
			result.HayakuReused++
		case "upstream-cached":
			result.UpstreamCached++
		default:
			return Summary{}, errors.New("unknown suite execution evidence")
		}
		switch suite.Outcome {
		case "passed":
			result.Passed++
		case "failed":
			result.Failed++
		case "incomplete":
			result.Incomplete++
		default:
			return Summary{}, errors.New("unknown suite outcome evidence")
		}
		if suite.Execution == "hayaku-reused" && suite.Outcome != "passed" {
			return Summary{}, errors.New("Hayaku reuse requires a passing result")
		}
	}
	return result, nil
}

// Compare reports savings only for the same qualified experimental context and
// upstream cache state. Negative savings are kept, exposing planning regressions.
// Valid means comparable measurements, not a proof that reuse was qualified.
func Compare(baseline, candidate Run) (Comparison, error) {
	before, err := Summarize(baseline)
	if err != nil {
		return Comparison{}, err
	}
	after, err := Summarize(candidate)
	if err != nil {
		return Comparison{}, err
	}
	result := Comparison{Baseline: before, Candidate: after}
	if baseline.Context != candidate.Context {
		result.Reason = "context-mismatch"
		return result, nil
	}
	if before.HayakuReused != 0 {
		result.Reason = "baseline-has-Hayaku-reuse"
		return result, nil
	}
	if before.Failed != 0 || before.Incomplete != 0 || after.Failed != 0 || after.Incomplete != 0 {
		result.Reason = "nonpassing-or-incomplete-result"
		return result, nil
	}
	if len(baseline.Suites) != len(candidate.Suites) {
		result.Reason = "suite-inventory-mismatch"
		return result, nil
	}
	states := map[string]string{}
	for _, suite := range baseline.Suites {
		states[suite.ID] = suite.Execution
	}
	for _, suite := range candidate.Suites {
		state, ok := states[suite.ID]
		if !ok {
			result.Reason = "suite-inventory-mismatch"
			return result, nil
		}
		if (state == "upstream-cached") != (suite.Execution == "upstream-cached") {
			result.Reason = "upstream-cache-state-mismatch"
			return result, nil
		}
	}
	result.Valid = true
	result.NetSavingsNanos = before.WallNanos - after.WallNanos
	result.GrossRunnerSavingsNanos = before.RunnerNanos - after.RunnerNanos
	return result, nil
}
