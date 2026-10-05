package runner

import (
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
)

func TestNormalizedRunnerContract(t *testing.T) {
	for name, mutate := range map[string]func(*model.Outcomes, *[]model.Unit, *bool, *int){
		"pass":         func(*model.Outcomes, *[]model.Unit, *bool, *int) {},
		"missing-unit": func(r *model.Outcomes, _ *[]model.Unit, _ *bool, _ *int) { r.Units = r.Units[:1] },
		"extra-unit": func(r *model.Outcomes, _ *[]model.Unit, _ *bool, _ *int) {
			r.Units = append(r.Units, model.Outcome{Unit: "other", Action: "pass"})
		},
		"duplicate-unit":      func(r *model.Outcomes, _ *[]model.Unit, _ *bool, _ *int) { r.Units = append(r.Units, r.Units[0]) },
		"duplicate-case":      func(r *model.Outcomes, _ *[]model.Unit, _ *bool, _ *int) { r.Tests = append(r.Tests, r.Tests[0]) },
		"unknown-case-owner":  func(r *model.Outcomes, _ *[]model.Unit, _ *bool, _ *int) { r.Tests[0].Unit = "unknown" },
		"nonterminal":         func(r *model.Outcomes, _ *[]model.Unit, _ *bool, _ *int) { r.Units[0].Action = "running" },
		"unhandled-error":     func(r *model.Outcomes, _ *[]model.Unit, _ *bool, _ *int) { r.Errors = 1 },
		"incomplete":          func(r *model.Outcomes, _ *[]model.Unit, _ *bool, _ *int) { r.Complete = false },
		"killed":              func(_ *model.Outcomes, _ *[]model.Unit, c *bool, _ *int) { *c = false },
		"unexplained-exit":    func(_ *model.Outcomes, _ *[]model.Unit, _ *bool, e *int) { *e = 1 },
		"duplicate-inventory": func(_ *model.Outcomes, w *[]model.Unit, _ *bool, _ *int) { *w = append(*w, (*w)[0]) },
		"empty-inventory":     func(_ *model.Outcomes, w *[]model.Unit, _ *bool, _ *int) { *w = nil },
		"invalid-case-id":     func(r *model.Outcomes, _ *[]model.Unit, _ *bool, _ *int) { r.Tests[0].Test = "bad\x00identity" },
	} {
		t.Run(name, func(t *testing.T) {
			expected := []model.Unit{{ID: "a", Workspace: "w"}, {ID: "b", Workspace: "w"}}
			result := model.Outcomes{Complete: true, Units: []model.Outcome{{Unit: "a", Action: "pass"}, {Unit: "b", Action: "skip"}}, Tests: []model.Outcome{{Unit: "a", Test: "same", Action: "pass"}, {Unit: "b", Test: "same", Action: "skip"}}}
			completed, exit := true, 0
			mutate(&result, &expected, &completed, &exit)
			err := Validate(result, expected, completed, exit)
			if (err == nil) != (name == "pass") {
				t.Fatalf("contract result err=%v", err)
			}
		})
	}
}

func TestCompleteFailuresRemainFailures(t *testing.T) {
	want := []model.Unit{{ID: "a", Workspace: "w"}}
	r := model.Outcomes{Complete: true, Units: []model.Outcome{{Unit: "a", Action: "fail"}}}
	if err := Validate(r, want, true, 1); err != nil || !Failed(r) {
		t.Fatalf("complete failure lost: err=%v", err)
	}
	if err := Validate(r, want, true, 0); err != nil || !Failed(r) {
		t.Fatal("zero exit status hid a reported failure")
	}
}
