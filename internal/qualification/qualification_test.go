package qualification

import (
	"math"
	"reflect"
	"testing"
	"time"
)

func observation(ids ...string) Run {
	run := Run{Schema: Schema, Source: "source-id", Context: "context-id", Complete: true, Expected: ids, Duration: time.Second}
	for _, id := range ids {
		run.Outcomes = append(run.Outcomes, TestOutcome{ID: id, Outcome: "pass"})
	}
	return run
}

func TestCompareFindsOmittedFailureAndMatchesSelectedFailure(t *testing.T) {
	selected, full := observation("selected"), observation("selected", "omitted", "passing")
	selected.Outcomes[0].Outcome = "fail"
	full.Outcomes[0].Outcome = "fail"
	full.Outcomes[1].Outcome = "timeout"
	result := Compare(selected, full)
	if !result.Valid || !reflect.DeepEqual(result.ObservedMisses, []string{"omitted"}) || !reflect.DeepEqual(result.MatchingFailures, []string{"selected"}) {
		t.Fatalf("comparison = %#v", result)
	}
}

func TestCompareRejectsInvalidEvidence(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Run)
		code   string
	}{
		{"wrong schema", func(r *Run) { r.Schema++ }, "unsupported_schema"},
		{"source drift", func(r *Run) { r.Source = "different" }, "source_mismatch"},
		{"context drift", func(r *Run) { r.Context = "different" }, "context_mismatch"},
		{"partial execution", func(r *Run) { r.Complete = false }, "incomplete_run"},
		{"missing terminal", func(r *Run) { r.Outcomes = nil }, "missing_terminal_outcome"},
		{"duplicate terminal", func(r *Run) { r.Outcomes = append(r.Outcomes, r.Outcomes[0]) }, "invalid_or_duplicate_outcome_id"},
		{"duplicate inventory", func(r *Run) { r.Expected = append(r.Expected, r.Expected[0]) }, "invalid_or_duplicate_inventory_id"},
		{"not terminal", func(r *Run) { r.Outcomes[0].Outcome = "running" }, "invalid_terminal_outcome"},
		{"negative duration", func(r *Run) { r.Duration = -1 }, "invalid_duration"},
		{"empty inventory", func(r *Run) { r.Expected = nil; r.Outcomes = nil }, "empty_inventory"},
		{"undeclared outcome", func(r *Run) { r.Outcomes = append(r.Outcomes, TestOutcome{ID: "extra", Outcome: "pass"}) }, "unexpected_outcome_id"},
	} {
		t.Run(test.name, func(t *testing.T) {
			selected, full := observation("test"), observation("test")
			test.mutate(&selected)
			result := Compare(selected, full)
			if result.Valid {
				t.Fatalf("accepted invalid comparison %#v", result)
			}
			for _, issue := range result.Issues {
				if issue.Code == test.code {
					return
				}
			}
			t.Fatalf("missing issue %s: %#v", test.code, result.Issues)
		})
	}
}

func TestCompareOutcomeDisagreementIsUnresolved(t *testing.T) {
	selected, full := observation("test"), observation("test")
	full.Outcomes[0].Outcome = "fail"
	result := Compare(selected, full)
	if result.Valid || !reflect.DeepEqual(result.OutcomeMismatches, []string{"test"}) || len(result.ObservedMisses) != 0 {
		t.Fatalf("outcome mismatch misclassified: %#v", result)
	}
}

func TestCompareNeverTreatsCleanObservationAsQualification(t *testing.T) {
	result := Compare(observation("test"), observation("test", "omitted-pass"))
	if !result.Valid || len(result.ObservedMisses) != 0 {
		t.Fatalf("clean finite observation = %#v", result)
	}
}

func TestMeasureIncludesPlanningAndShadowOverhead(t *testing.T) {
	baseline := Costs{Startup: time.Second, Execution: 9 * time.Second}
	selected := Costs{Snapshot: time.Second, Discovery: time.Second, Planning: time.Second, Storage: time.Second, Startup: time.Second, Execution: 2 * time.Second}
	performance, err := Measure(baseline, selected, 10*time.Second)
	if err != nil || performance.Selected != 7*time.Second || performance.Savings != 3*time.Second || performance.SavingsFraction != .3 || performance.ShadowTotal != 17*time.Second {
		t.Fatalf("performance = %#v, %v", performance, err)
	}
	slower, err := Measure(baseline, Costs{Discovery: 12 * time.Second}, 0)
	if err != nil || slower.Savings != -2*time.Second {
		t.Fatalf("negative savings = %#v, %v", slower, err)
	}
	for _, costs := range []Costs{{}, {Execution: -1}, {Execution: time.Duration(math.MaxInt64), Startup: 1}} {
		if _, err := Measure(costs, selected, 0); err == nil {
			t.Fatalf("accepted invalid baseline %#v", costs)
		}
	}
	if _, err := Measure(baseline, Costs{Execution: time.Duration(math.MaxInt64)}, 1); err == nil {
		t.Fatal("shadow total overflow accepted")
	}
}

func FuzzCompareIncompleteRunsCannotPass(f *testing.F) {
	f.Add("test", "pass")
	f.Fuzz(func(t *testing.T, id, outcome string) {
		selected, full := observation(id), observation(id)
		selected.Complete = false
		selected.Outcomes[0].Outcome = outcome
		if result := Compare(selected, full); result.Valid {
			t.Fatalf("incomplete run became comparable: %#v", result)
		}
	})
}
