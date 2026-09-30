// Package qualification evaluates finite shadow observations. These observations
// cannot establish complete runtime influence or authorize test omission.
package qualification

import (
	"errors"
	"sort"
	"time"
)

const Schema = 1

type TestOutcome struct {
	ID      string `json:"id"`
	Outcome string `json:"outcome"`
}

// Expected is an independently collected inventory of terminal outcomes. Include
// build/startup scopes as explicit IDs when they can fail before test discovery.
type Run struct {
	Schema   int           `json:"schema"`
	Source   string        `json:"source"`
	Context  string        `json:"context"`
	Complete bool          `json:"complete"`
	Expected []string      `json:"expected"`
	Outcomes []TestOutcome `json:"outcomes"`
	Duration time.Duration `json:"duration_ns"`
}

type Issue struct {
	Code string `json:"code"`
	Run  string `json:"run,omitempty"`
	Test string `json:"test,omitempty"`
}

type Comparison struct {
	Schema            int      `json:"schema"`
	Valid             bool     `json:"valid"`
	Issues            []Issue  `json:"issues"`
	ObservedMisses    []string `json:"observed_misses"`
	MatchingFailures  []string `json:"matching_failures"`
	OutcomeMismatches []string `json:"outcome_mismatches"`
}

// Compare requires exact source and context identities and complete inventories.
// Valid means comparable observations, never a qualified omission algorithm.
// An omitted full-run failure is an observed miss. Conflicting shared outcomes
// invalidate comparison until clean independent reruns resolve possible flakiness.
func Compare(selected, full Run) Comparison {
	result := Comparison{Schema: Schema, Valid: true, Issues: []Issue{}, ObservedMisses: []string{}, MatchingFailures: []string{}, OutcomeMismatches: []string{}}
	small, smallIssues := validateRun("selected", selected)
	all, allIssues := validateRun("full", full)
	result.Issues = append(result.Issues, smallIssues...)
	result.Issues = append(result.Issues, allIssues...)
	if selected.Source == "" || selected.Source != full.Source {
		result.Issues = append(result.Issues, Issue{Code: "source_mismatch"})
	}
	if selected.Context == "" || selected.Context != full.Context {
		result.Issues = append(result.Issues, Issue{Code: "context_mismatch"})
	}
	if len(result.Issues) != 0 {
		result.Valid = false
		return result
	}
	for id, outcome := range small {
		fullOutcome, exists := all[id]
		if !exists {
			result.Issues = append(result.Issues, Issue{Code: "selected_id_absent_from_full", Test: id})
		} else if outcome != fullOutcome {
			result.OutcomeMismatches = append(result.OutcomeMismatches, id)
			result.Issues = append(result.Issues, Issue{Code: "unresolved_outcome_mismatch", Test: id})
		}
	}
	for id, outcome := range all {
		if !isFailure(outcome) {
			continue
		}
		if observed, exists := small[id]; !exists {
			result.ObservedMisses = append(result.ObservedMisses, id)
		} else if observed == outcome {
			result.MatchingFailures = append(result.MatchingFailures, id)
		}
	}
	result.Valid = len(result.Issues) == 0
	sort.Strings(result.ObservedMisses)
	sort.Strings(result.MatchingFailures)
	sort.Strings(result.OutcomeMismatches)
	sort.Slice(result.Issues, func(i, j int) bool {
		if result.Issues[i].Code != result.Issues[j].Code {
			return result.Issues[i].Code < result.Issues[j].Code
		}
		return result.Issues[i].Test < result.Issues[j].Test
	})
	return result
}

func validateRun(name string, run Run) (map[string]string, []Issue) {
	outcomes := map[string]string{}
	issues := []Issue{}
	if run.Schema != Schema {
		issues = append(issues, Issue{Code: "unsupported_schema", Run: name})
	}
	if !run.Complete {
		issues = append(issues, Issue{Code: "incomplete_run", Run: name})
	}
	if run.Duration < 0 {
		issues = append(issues, Issue{Code: "invalid_duration", Run: name})
	}
	if len(run.Expected) == 0 {
		issues = append(issues, Issue{Code: "empty_inventory", Run: name})
	}
	expected := map[string]bool{}
	for _, id := range run.Expected {
		if id == "" || expected[id] {
			issues = append(issues, Issue{Code: "invalid_or_duplicate_inventory_id", Run: name, Test: id})
		}
		expected[id] = true
	}
	for _, outcome := range run.Outcomes {
		if outcome.ID == "" || outcomes[outcome.ID] != "" {
			issues = append(issues, Issue{Code: "invalid_or_duplicate_outcome_id", Run: name, Test: outcome.ID})
		}
		switch outcome.Outcome {
		case "pass", "fail", "skip", "timeout", "build-fail":
		default:
			issues = append(issues, Issue{Code: "invalid_terminal_outcome", Run: name, Test: outcome.ID})
		}
		if !expected[outcome.ID] {
			issues = append(issues, Issue{Code: "unexpected_outcome_id", Run: name, Test: outcome.ID})
		}
		outcomes[outcome.ID] = outcome.Outcome
	}
	for _, id := range run.Expected {
		if _, exists := outcomes[id]; !exists {
			issues = append(issues, Issue{Code: "missing_terminal_outcome", Run: name, Test: id})
		}
	}
	return outcomes, issues
}

func isFailure(outcome string) bool {
	return outcome == "fail" || outcome == "timeout" || outcome == "build-fail"
}

type Costs struct {
	Snapshot  time.Duration `json:"snapshot_ns"`
	Discovery time.Duration `json:"discovery_ns"`
	Planning  time.Duration `json:"planning_ns"`
	Storage   time.Duration `json:"storage_ns"`
	Startup   time.Duration `json:"startup_ns"`
	Execution time.Duration `json:"execution_ns"`
}

type Performance struct {
	Schema           int           `json:"schema"`
	Baseline         time.Duration `json:"baseline_ns"`
	Selected         time.Duration `json:"selected_ns"`
	Savings          time.Duration `json:"savings_ns"`
	SavingsFraction  float64       `json:"savings_fraction"`
	ShadowAdditional time.Duration `json:"shadow_additional_ns"`
	ShadowTotal      time.Duration `json:"shadow_total_ns"`
}

// Measure reports arithmetic over supplied observations. Negative savings are
// valid. A shadow full run is rollout overhead, not a production speedup.
func Measure(baseline, selected Costs, shadowCost time.Duration) (Performance, error) {
	b, err := total(baseline)
	if err != nil || b == 0 {
		return Performance{}, errors.New("baseline costs must be nonnegative with a positive total")
	}
	s, err := total(selected)
	if err != nil || shadowCost < 0 || s > time.Duration(1<<63-1)-shadowCost {
		return Performance{}, errors.New("selected or shadow costs are invalid or overflow")
	}
	return Performance{Schema: Schema, Baseline: b, Selected: s, Savings: b - s, SavingsFraction: float64(b-s) / float64(b), ShadowAdditional: shadowCost, ShadowTotal: s + shadowCost}, nil
}

func total(costs Costs) (time.Duration, error) {
	var sum time.Duration
	for _, value := range []time.Duration{costs.Snapshot, costs.Discovery, costs.Planning, costs.Storage, costs.Startup, costs.Execution} {
		if value < 0 || value > time.Duration(1<<63-1)-sum {
			return 0, errors.New("negative or overflowing cost")
		}
		sum += value
	}
	return sum, nil
}
