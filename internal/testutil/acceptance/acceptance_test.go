package acceptance

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
)

func cleanOracle() Expectation {
	return Expectation{
		Inventory: []model.Unit{{ID: "suite", Workspace: "fixture"}},
		Outcomes:  model.Outcomes{Complete: true, Units: []model.Outcome{{Unit: "suite", Action: "pass"}}, Tests: []model.Outcome{{Unit: "suite", Test: "one", Action: "pass"}, {Unit: "suite", Test: "two", Action: "pass"}}},
		Completed: true, Passed: true, Groups: []Group{{Name: "suite", Mode: "execute", Qualified: true}}, Authorized: []string{"suite"},
	}
}

func cleanObservation() Observation {
	w := cleanOracle()
	return Observation{Outcomes: w.Outcomes, Completed: true, Passed: true, Groups: w.Groups, Authorized: w.Authorized}
}

func TestIndependentCaseOracleDetectsInvalidGreen(t *testing.T) {
	for _, test := range []struct {
		name, issue string
		change      func(*Observation)
	}{
		{"missing-case", "terminal_outcome_mismatch", func(o *Observation) { o.Outcomes.Tests = o.Outcomes.Tests[:1] }},
		{"duplicate-case", "invalid_terminal_inventory", func(o *Observation) { o.Outcomes.Tests = append(o.Outcomes.Tests, o.Outcomes.Tests[0]) }},
		{"unexpected-case", "terminal_outcome_mismatch", func(o *Observation) { o.Outcomes.Tests[0].Test = "unlisted" }},
		{"startup-error", "unsafe_aggregate_pass", func(o *Observation) { o.Outcomes.Errors = 1 }},
		{"incomplete", "unsafe_aggregate_pass", func(o *Observation) { o.Outcomes.Complete = false }},
		{"failed-case", "unsafe_aggregate_pass", func(o *Observation) { o.Outcomes.Tests[0].Action = "fail" }},
		{"skip-cached", "terminal_outcome_mismatch", func(o *Observation) { o.Outcomes.Tests[0].Action = "skip" }},
		{"unqualified-reuse", "execution_decision_mismatch", func(o *Observation) { o.Groups = []Group{{Name: "suite", Mode: "reuse"}} }},
		{"forged-authority", "receipt_authority_mismatch", func(o *Observation) { o.Authorized = []string{"other"} }},
		{"retained-revocation", "receipt_revocation_mismatch", func(o *Observation) { o.Revoked = []string{"other"} }},
		{"duplicate-authority", "receipt_authority_mismatch", func(o *Observation) { o.Authorized = []string{"suite", "suite"} }},
		{"reuse-without-authority", "reuse_without_authority", func(o *Observation) { o.Groups[0].Mode = "reuse"; o.Authorized = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := cleanObservation()
			test.change(&o)
			if issues := Check(o, nil, cleanOracle()); !slices.Contains(issues, test.issue) {
				t.Fatalf("missing expected issue %s: %v", test.issue, issues)
			}
		})
	}
}

func TestScenarioOrderingFailureAndUnavailable(t *testing.T) {
	invoked := []string{}
	step := func(name string) Step {
		return Step{Name: name, Want: cleanOracle(), Prepare: func(context.Context) error { invoked = append(invoked, "prepare-"+name); return nil }, Invoke: func(context.Context) (Observation, error) {
			invoked = append(invoked, name)
			return cleanObservation(), nil
		}}
	}
	first, second := step("cold"), step("warm")
	second.Prepare = func(context.Context) error { return errors.New("private fixture diagnostic") }
	r := Run(context.Background(), Scenario{Name: "lifecycle", Steps: []Step{first, second, step("dependent")}})
	if r.State != "failed" || len(r.Steps) != 2 || !slices.Equal(invoked, []string{"prepare-cold", "cold"}) || !slices.Equal(r.Steps[1].Issues, []string{"fixture_preparation_failed"}) {
		t.Fatalf("unexpected lifecycle evidence: %+v %v", r, invoked)
	}
	r = Run(context.Background(), Scenario{Name: "missing-platform", Unavailable: "no installed backend"})
	if r.State != "unavailable" || len(r.Steps) != 0 {
		t.Fatalf("missing context passed: %+v", r)
	}
	// Entire scenario validates before a callback can mutate the fixture.
	invoked = nil
	r = Run(context.Background(), Scenario{Name: "duplicate", Steps: []Step{first, first}})
	if r.State != "failed" || len(invoked) != 0 {
		t.Fatalf("invalid scenario invoked callbacks: %+v", r)
	}
}

func TestCompleteFailureIsValidAcceptanceEvidence(t *testing.T) {
	w := cleanOracle()
	w.Passed, w.Error, w.ExitCode = false, true, 1
	w.Outcomes.Failed = true
	w.Outcomes.Units[0].Action = "fail"
	w.Outcomes.Tests[0].Action = "fail"
	w.Authorized = nil
	o := Observation{Outcomes: w.Outcomes, Completed: true, ExitCode: 1, Groups: w.Groups}
	r := Run(context.Background(), Scenario{Name: "failure", Steps: []Step{{Name: "assertion", Want: w, Invoke: func(context.Context) (Observation, error) { return o, errors.New("assertion failed") }}}})
	if r.State != "passed" {
		t.Fatalf("complete expected failure rejected: %+v", r)
	}
}

func TestOrderingIsIrrelevantButOracleContradictionsAreRejected(t *testing.T) {
	o := cleanObservation()
	slices.Reverse(o.Outcomes.Tests)
	if issues := Check(o, nil, cleanOracle()); len(issues) != 0 {
		t.Fatal(issues)
	}
	w := cleanOracle()
	w.Outcomes.Tests = append(w.Outcomes.Tests, w.Outcomes.Tests[0])
	r := Run(context.Background(), Scenario{Name: "bad-oracle", Steps: []Step{{Name: "step", Want: w, Invoke: func(context.Context) (Observation, error) {
		t.Fatal("invalid oracle was used")
		return Observation{}, nil
	}}}})
	if r.State != "failed" {
		t.Fatal("invalid independent oracle accepted")
	}
}

func TestExpectedUnstartedScopeCannotRetainAuthority(t *testing.T) {
	w := cleanOracle()
	w.Completed, w.Passed, w.Error = false, false, true
	w.Outcomes = model.Outcomes{}
	w.Groups = []Group{{Name: "suite", Mode: "incomplete"}}
	w.Authorized = nil
	o := Observation{Groups: w.Groups}
	r := Run(context.Background(), Scenario{Name: "startup-interruption", Steps: []Step{{Name: "unstarted", Want: w, Invoke: func(context.Context) (Observation, error) { return o, context.Canceled }}}})
	if r.State != "passed" {
		t.Fatalf("expected incomplete evidence rejected: %+v", r)
	}
	o.Authorized = []string{"suite"}
	if issues := Check(o, context.Canceled, w); !slices.Contains(issues, "unsafe_unstarted_scope") {
		t.Fatalf("unstarted scope retained authority: %v", issues)
	}
}
