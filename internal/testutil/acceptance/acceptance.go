// Package acceptance runs independently authored lifecycle scenarios against
// real runner/backend callbacks. Its results are finite acceptance observations,
// never qualification proofs or permission to omit tests.
package acceptance

import (
	"context"
	"errors"
	"reflect"
	"slices"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/runner"
)

// Group reports actual execution decisions, not proposed selections. Mode is
// execute, reuse, fallback, or incomplete (an unstarted scope). A fallback
// executes the complete original scope.
type Group struct {
	Name      string
	Mode      string
	Qualified bool
}

type Observation struct {
	Outcomes            model.Outcomes
	Completed, Passed   bool
	ExitCode            int
	Groups              []Group
	Authorized, Revoked []string
}

// Expectation belongs to the fixture author, independently of runner output and
// the selection algorithm. Inventory must include startup/build scopes where
// applicable. Outcomes includes exact expected unit and test terminal identities.
// Authorized and Revoked must come from inspecting actual receipt authority in
// the callback, not from copying a report's claimed cache labels.
type Expectation struct {
	Inventory           []model.Unit
	Outcomes            model.Outcomes
	Completed, Passed   bool
	Error               bool
	ExitCode            int
	Groups              []Group
	Authorized, Revoked []string
}

type Step struct {
	Name    string
	Prepare func(context.Context) error
	Invoke  func(context.Context) (Observation, error)
	Want    Expectation
}

type Scenario struct {
	Name string
	// Unavailable records a missing installed context. It never yields a pass.
	// Supply no steps for an unavailable scenario.
	Unavailable string
	Steps       []Step
}

type Result struct {
	Name, State string // passed, failed, or unavailable
	Reason      string // Fixture-authored reason for an unavailable context.
	Steps       []StepResult
}

type StepResult struct {
	Name   string
	Issues []string // Stable codes; callback diagnostics and input values stay private.
}

// Run stops after a failed step: subsequent lifecycle steps depend on its state.
// Prepare executes only after the entire scenario has been structurally checked.
func Run(ctx context.Context, scenario Scenario) Result {
	r := Result{Name: scenario.Name, State: "failed"}
	if err := validate(scenario); err != nil {
		r.Steps = append(r.Steps, StepResult{Issues: []string{"invalid_scenario"}})
		return r
	}
	if scenario.Unavailable != "" {
		r.State = "unavailable"
		r.Reason = scenario.Unavailable
		return r
	}
	for _, step := range scenario.Steps {
		s := StepResult{Name: step.Name}
		if ctx.Err() != nil {
			s.Issues = append(s.Issues, "scenario_context_cancelled")
		} else if step.Prepare != nil && step.Prepare(ctx) != nil {
			s.Issues = append(s.Issues, "fixture_preparation_failed")
		} else {
			observation, err := step.Invoke(ctx)
			s.Issues = Check(observation, err, step.Want)
		}
		r.Steps = append(r.Steps, s)
		if len(s.Issues) != 0 {
			return r
		}
	}
	r.State = "passed"
	return r
}

func validate(s Scenario) error {
	if s.Name == "" || (s.Unavailable != "" && len(s.Steps) != 0) || (s.Unavailable == "" && len(s.Steps) == 0) {
		return errors.New("missing or contradictory scenario context")
	}
	seen := map[string]bool{}
	for _, step := range s.Steps {
		if step.Name == "" || seen[step.Name] || step.Invoke == nil || len(step.Want.Inventory) == 0 {
			return errors.New("invalid step")
		}
		seen[step.Name] = true
		if step.Want.Passed && (!step.Want.Completed || !step.Want.Outcomes.Complete || runner.Failed(step.Want.Outcomes) || step.Want.Error || step.Want.ExitCode != 0) {
			return errors.New("contradictory expected pass")
		}
		// A complete oracle must itself have valid exact unit/test identities.
		if step.Want.Completed && runner.ValidateExact(step.Want.Outcomes, independentInventory(step.Want), true, step.Want.ExitCode) != nil {
			return errors.New("invalid independent terminal oracle")
		}
		if !validGroups(step.Want.Groups) || !validNames(step.Want.Authorized) || !validNames(step.Want.Revoked) {
			return errors.New("invalid independent group or receipt oracle")
		}
		if step.Want.Completed && (len(step.Want.Groups) == 0 || reuseWithoutAuthority(step.Want.Groups, step.Want.Authorized)) {
			return errors.New("complete scope requires execution or authenticated reuse evidence")
		}
		if unsafeIncomplete(step.Want.Groups, step.Want.Completed, step.Want.Passed, step.Want.Authorized) {
			return errors.New("unstarted scope cannot complete or retain current authority")
		}
	}
	return nil
}

// Check compares actual evidence with the independent oracle. In particular,
// The shared exact validator checks independently expected case membership;
// outcome comparison additionally checks each fixture-authored terminal action.
func Check(got Observation, err error, want Expectation) []string {
	issues := []string{}
	if (err != nil) != want.Error {
		issues = append(issues, "invocation_error_mismatch")
	}
	if got.Completed != want.Completed || got.Outcomes.Complete != want.Outcomes.Complete {
		issues = append(issues, "completion_mismatch")
	}
	if got.Passed != want.Passed || got.ExitCode != want.ExitCode {
		issues = append(issues, "aggregate_mismatch")
	}
	validation := runner.ValidateExact(got.Outcomes, independentInventory(want), got.Completed, got.ExitCode)
	if got.Completed && validation != nil {
		issues = append(issues, "invalid_terminal_inventory")
	}
	if got.Passed && (err != nil || validation != nil || runner.Failed(got.Outcomes) || got.ExitCode != 0) {
		issues = append(issues, "unsafe_aggregate_pass")
	}
	if !reflect.DeepEqual(normalize(got.Outcomes), normalize(want.Outcomes)) {
		issues = append(issues, "terminal_outcome_mismatch")
	}
	if !validGroups(got.Groups) || !reflect.DeepEqual(sortedGroups(got.Groups), sortedGroups(want.Groups)) {
		issues = append(issues, "execution_decision_mismatch")
	}
	if unsafeIncomplete(got.Groups, got.Completed, got.Passed, got.Authorized) {
		issues = append(issues, "unsafe_unstarted_scope")
	}
	if got.Completed && reuseWithoutAuthority(got.Groups, got.Authorized) {
		issues = append(issues, "reuse_without_authority")
	}
	if !validNames(got.Authorized) || !slices.Equal(sortedNames(got.Authorized), sortedNames(want.Authorized)) {
		issues = append(issues, "receipt_authority_mismatch")
	}
	if !validNames(got.Revoked) || !slices.Equal(sortedNames(got.Revoked), sortedNames(want.Revoked)) {
		issues = append(issues, "receipt_revocation_mismatch")
	}
	return issues
}

func independentInventory(want Expectation) model.ExecutionInventory {
	inventory := model.ExecutionInventory{Schema: model.ExecutionSchema, Units: want.Inventory}
	for _, item := range want.Outcomes.Tests {
		inventory.Cases = append(inventory.Cases, model.Case{Unit: item.Unit, ID: item.Test})
	}
	return inventory
}

func normalize(o model.Outcomes) model.Outcomes {
	less := func(a, b model.Outcome) int {
		for _, pair := range [][2]string{{a.Unit, b.Unit}, {a.Test, b.Test}, {a.Action, b.Action}} {
			if pair[0] < pair[1] {
				return -1
			}
			if pair[0] > pair[1] {
				return 1
			}
		}
		return 0
	}
	o.Units = append([]model.Outcome{}, o.Units...)
	o.Tests = append([]model.Outcome{}, o.Tests...)
	slices.SortFunc(o.Units, less)
	slices.SortFunc(o.Tests, less)
	return o
}

func validGroups(groups []Group) bool {
	names := []string{}
	for _, g := range groups {
		if g.Mode != "execute" && g.Mode != "reuse" && g.Mode != "fallback" && g.Mode != "incomplete" {
			return false
		}
		if (g.Mode == "reuse" && !g.Qualified) || (g.Mode == "fallback" && g.Qualified) {
			return false
		}
		names = append(names, g.Name)
	}
	return validNames(names)
}

func unsafeIncomplete(groups []Group, completed, passed bool, authorized []string) bool {
	for _, group := range groups {
		if group.Mode == "incomplete" && (completed || passed || slices.Contains(authorized, group.Name)) {
			return true
		}
	}
	return false
}

func reuseWithoutAuthority(groups []Group, authorized []string) bool {
	for _, group := range groups {
		if group.Mode == "reuse" && !slices.Contains(authorized, group.Name) {
			return true
		}
	}
	return false
}

func validNames(names []string) bool {
	seen := map[string]bool{}
	for _, name := range names {
		if name == "" || seen[name] {
			return false
		}
		seen[name] = true
	}
	return true
}

func sortedNames(names []string) []string {
	copy := append([]string{}, names...)
	slices.Sort(copy)
	return copy
}

func sortedGroups(groups []Group) []Group {
	copy := append([]Group{}, groups...)
	slices.SortFunc(copy, func(a, b Group) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return copy
}
