package native

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
)

func expectedNative() []model.Unit {
	return []model.Unit{{ID: "w:a", Workspace: "w", Selector: "a.js", Kind: "node-test-file"}, {ID: "w:b", Workspace: "w", Selector: "b.js", Kind: "node-test-file"}}
}
func TestReconcileNativeTerminals(t *testing.T) {
	report := Report{Schema: 1, Runner: "node-test", Version: "22.18.0", Complete: true, Units: []Terminal{{Selector: "b.js", Action: "skip"}, {Selector: "a.js", Action: "pass"}}, Tests: []Terminal{{Selector: "a.js", Test: "case", Action: "fail"}}}
	data, _ := json.Marshal(report)
	result, err := Reconcile(bytes.NewReader(data), "node-test", "22.18.0", expectedNative())
	if err != nil || !result.Failed || result.Units[0].Unit != "w:a" {
		t.Fatal("native reconciliation lost failure or order", err)
	}
	for _, mutate := range []func(*Report){func(r *Report) { r.Complete = false }, func(r *Report) { r.Version = "26.0.0" }, func(r *Report) { r.Units = r.Units[:1] }, func(r *Report) { r.Units = append(r.Units, r.Units[0]) }, func(r *Report) { r.Units[0].Selector = "outside" }, func(r *Report) { r.Units[0].Action = "running" }, func(r *Report) { r.Tests = append(r.Tests, r.Tests[0]) }, func(r *Report) { r.Errors = -1 }} {
		copy := report
		copy.Units = append([]Terminal(nil), report.Units...)
		copy.Tests = append([]Terminal(nil), report.Tests...)
		mutate(&copy)
		data, _ := json.Marshal(copy)
		if _, err := Reconcile(bytes.NewReader(data), "node-test", "22.18.0", expectedNative()); err == nil {
			t.Fatal("incomplete native report accepted")
		}
	}
	report.Errors = 1
	data, _ = json.Marshal(report)
	result, err = Reconcile(bytes.NewReader(data), "node-test", "22.18.0", expectedNative())
	if err == nil || result.UnhandledErrors != 1 || !result.Failed {
		t.Fatal("unhandled native failure accepted")
	}
}
func TestMetadataJSONRejectsAmbiguityAndBounds(t *testing.T) {
	for _, text := range []string{`{"schema":1,"schema":2}`, `{"schema":1} {}`, `{"schema":1,"unknown":2}`, strings.Repeat("[", 34) + strings.Repeat("]", 34), strings.Repeat(" ", MaxResultBytes+1)} {
		var r Report
		if DecodeJSON([]byte(text), &r) == nil {
			t.Fatal("ambiguous/unbounded protocol accepted")
		}
	}
	units := expectedNative()
	units[1].ID = units[0].ID
	if _, err := Reconcile(strings.NewReader(`{}`), "node-test", "22.18.0", units); err == nil {
		t.Fatal("duplicate expected ID accepted")
	}
}
