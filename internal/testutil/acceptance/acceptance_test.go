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

func originalOracle(mixed bool) Expectation {
	w := Expectation{Inventory: []model.Unit{{ID: "original", Workspace: "fixture"}},
		Outcomes:  model.Outcomes{Complete: true, Units: []model.Outcome{{Unit: "original", Action: "pass"}}},
		Completed: true, Passed: true, OriginalScopes: []string{"original"},
		Groups: []Group{{Name: "original", Mode: "fallback"}}}
	if mixed {
		w.Inventory = append(w.Inventory, model.Unit{ID: "native", Workspace: "fixture"})
		w.Outcomes.Units = append(w.Outcomes.Units, model.Outcome{Unit: "native", Action: "pass"})
		w.Outcomes.Tests = []model.Outcome{{Unit: "native", Test: "case", Action: "pass"}}
		w.Groups = append(w.Groups, Group{Name: "native", Mode: "reuse", Qualified: true})
		w.Authorized = []string{"native"}
	}
	return w
}

func TestExplicitOriginalScopesRetainExactNativeEvidence(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		for _, failed := range []bool{false, true} {
			w := originalOracle(mixed)
			if failed {
				w.Passed, w.Error, w.ExitCode, w.Outcomes.Failed = false, true, 1, true
				w.Outcomes.Units[0].Action = "fail"
			}
			o := Observation{Outcomes: w.Outcomes, Completed: true, Passed: !failed, ExitCode: w.ExitCode, Groups: w.Groups, Authorized: w.Authorized}
			var invokeErr error
			if failed {
				invokeErr = errors.New("original full command failed")
			}
			r := Run(context.Background(), Scenario{Name: "original-and-native", Steps: []Step{{Name: "complete", Want: w,
				Invoke: func(context.Context) (Observation, error) { return o, invokeErr }}}})
			if r.State != "passed" {
				t.Fatalf("opaque original mixed=%t failed=%t rejected: %+v", mixed, failed, r)
			}
		}
	}
	for _, test := range []struct {
		name   string
		change func(*Observation)
	}{
		{"missing-original-unit", func(o *Observation) { o.Outcomes.Units = o.Outcomes.Units[1:] }},
		{"unexpected-unit", func(o *Observation) {
			o.Outcomes.Units = append(o.Outcomes.Units, model.Outcome{Unit: "unknown", Action: "pass"})
		}},
		{"original-case", func(o *Observation) {
			o.Outcomes.Tests = append(o.Outcomes.Tests, model.Outcome{Unit: "original", Test: "invented", Action: "pass"})
		}},
		{"original-authority", func(o *Observation) { o.Authorized = append(o.Authorized, "original") }},
		{"original-qualification", func(o *Observation) { o.Groups[0].Qualified = true }},
		{"original-reuse", func(o *Observation) { o.Groups[0] = Group{Name: "original", Mode: "reuse", Qualified: true} }},
		{"native-missing-case", func(o *Observation) { o.Outcomes.Tests = nil }},
		{"native-extra-case", func(o *Observation) {
			o.Outcomes.Tests = append(o.Outcomes.Tests, model.Outcome{Unit: "native", Test: "extra", Action: "pass"})
		}},
		{"native-duplicate-case", func(o *Observation) { o.Outcomes.Tests = append(o.Outcomes.Tests, o.Outcomes.Tests[0]) }},
		{"unknown-case-owner", func(o *Observation) { o.Outcomes.Tests[0].Unit = "unknown" }},
		{"unreconciled-exit", func(o *Observation) { o.ExitCode = 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := originalOracle(true)
			o := Observation{Outcomes: originalOracle(true).Outcomes, Completed: true, Passed: true,
				Groups: originalOracle(true).Groups, Authorized: []string{"native"}}
			test.change(&o)
			if issues := Check(o, nil, w); len(issues) == 0 {
				t.Fatal("invalid original/native evidence accepted")
			}
		})
	}
}

func TestOriginalScopeDeclarationsFailBeforeCallbacksAndInDirectChecks(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Expectation)
	}{
		{"duplicate", func(w *Expectation) { w.OriginalScopes = []string{"original", "original"} }},
		{"unknown", func(w *Expectation) { w.OriginalScopes = []string{"unknown"} }},
		{"missing-group", func(w *Expectation) { w.Groups = w.Groups[1:] }},
		{"qualified", func(w *Expectation) { w.Groups[0].Qualified = true }},
		{"reuse", func(w *Expectation) { w.Groups[0].Mode = "reuse" }},
		{"execute", func(w *Expectation) { w.Groups[0].Mode = "execute" }},
		{"authority", func(w *Expectation) { w.Authorized = append(w.Authorized, "original") }},
		{"authored-original-case", func(w *Expectation) {
			w.Outcomes.Tests = append(w.Outcomes.Tests, model.Outcome{Unit: "original", Test: "invented", Action: "pass"})
		}},
		{"undeclared-empty-native", func(w *Expectation) { w.OriginalScopes = nil }},
		{"native-empty-case", func(w *Expectation) { w.Outcomes.Tests = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := originalOracle(true)
			test.change(&w)
			called := false
			r := Run(context.Background(), Scenario{Name: "bad-late-oracle", Steps: []Step{
				{Name: "healthy-first", Want: cleanOracle(), Invoke: func(context.Context) (Observation, error) { called = true; return cleanObservation(), nil }},
				{Name: "invalid-later", Want: w, Invoke: func(context.Context) (Observation, error) { called = true; return Observation{}, nil }},
			}})
			if r.State != "failed" || called || len(r.Steps) != 1 || !slices.Equal(r.Steps[0].Issues, []string{"invalid_scenario"}) {
				t.Fatalf("invalid late declaration reached callbacks: %+v", r)
			}
			o := Observation{Outcomes: w.Outcomes, Completed: true, Passed: true, Groups: w.Groups, Authorized: w.Authorized}
			if issues := Check(o, nil, w); len(issues) == 0 {
				t.Fatal("direct Check accepted malformed declarations or empty native cases")
			}
		})
	}
}
