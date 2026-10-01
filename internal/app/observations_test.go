package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	vitestprovider "github.com/Cyberlane/hayaku/internal/adapter/vitest"
	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/report"
)

func TestRuntimeObservationsOnlyBroadenStrictSelection(t *testing.T) {
	for _, test := range []struct{ name, operation, input, changed string }{
		{"dynamic-read", "read", "value.ts", "value.ts"},
		{"missing-file-created", "exists", "optional.json", "optional.json"},
		{"missing-file-deleted", "metadata", "optional.json", "optional.json"},
		{"directory-membership", "directory", "fixtures", "fixtures/new.json"},
		{"directory-metadata", "metadata", "fixtures", "fixtures/new.json"},
		{"directory-existence-envelope", "exists", "fixtures", "fixtures/new.json"},
		{"root-directory", "directory", ".", "new.ts"},
		{"symlink-probe", "readlink", "alias", "alias"},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := model.Config{Schema: 1, Context: model.Context{ID: "test", OS: "linux", Arch: "amd64"}, Workspaces: []model.Workspace{{ID: "js", Root: ".", Adapter: "vitest", Command: model.Command{Dir: ".", Executable: "vitest", Args: []string{"run"}}, NodeRuntime: &model.NodeRuntime{Node: "/node", Modules: "/node_modules"}}}}
			a := model.Unit{ID: "js::a.test.ts", Workspace: "js", Selector: "a.test.ts", Kind: "vitest-file"}
			b := model.Unit{ID: "js::b.test.ts", Workspace: "js", Selector: "b.test.ts", Kind: "vitest-file"}
			p := model.Plan{Schema: 1, Mode: "full-fallback", Base: "base", Candidate: "candidate", ContextDigest: "context", ConfigDigest: config.Digest(c), ToolsDigest: "tools", Selected: []model.Unit{a, b}, Proposed: []model.Unit{a}, Changes: []model.Change{{Path: test.changed, Status: "M"}}, Commands: []model.Command{c.Workspaces[0].Command}}
			beforeSelected := append([]model.Unit{}, p.Selected...)
			beforeCommands := append([]model.Command{}, p.Commands...)
			observed := Execution{Schema: 1, Mode: "runtime-observation", Candidate: "base", ContextDigest: p.ContextDigest, ConfigDigest: p.ConfigDigest, ToolsDigest: p.ToolsDigest, Passed: true, Commands: []CommandResult{{Workspace: "js", Complete: true, RuntimeTrace: &vitestprovider.Trace{Schema: 1, Complete: true, Owners: []string{"a.test.ts", "b.test.ts"}, Observations: []vitestprovider.Observation{{Operation: test.operation, Path: test.input, Owner: "b.test.ts"}}}}}}
			file := filepath.Join(t.TempDir(), "observations.json")
			data, err := json.Marshal(observed)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := BroadenFromObservations(context.Background(), &p, c, file); err != nil {
				t.Fatal(err)
			}
			if len(p.Proposed) != 2 || p.Mode != "full-fallback" || !reflect.DeepEqual(p.Selected, beforeSelected) || !reflect.DeepEqual(p.Commands, beforeCommands) {
				t.Fatalf("runtime influence narrowed required execution: %+v", p)
			}
		})
	}
}

func TestRuntimeObservationsRejectForgedAuthority(t *testing.T) {
	c := model.Config{Schema: 1}
	p := model.Plan{Schema: 1, Mode: "full-fallback", Base: "base", ConfigDigest: config.Digest(c), ContextDigest: "ctx", ToolsDigest: "tools"}
	for _, mode := range []string{"affected", "full", "runtime-observation"} {
		t.Run(mode, func(t *testing.T) {
			r := Execution{Schema: 1, Mode: mode, Candidate: "wrong-source", ConfigDigest: p.ConfigDigest, ContextDigest: p.ContextDigest, ToolsDigest: p.ToolsDigest, Passed: true}
			b, _ := json.Marshal(r)
			file := filepath.Join(t.TempDir(), "forged.json")
			if err := os.WriteFile(file, b, 0600); err != nil {
				t.Fatal(err)
			}
			if err := BroadenFromObservations(context.Background(), &p, c, file); err == nil {
				t.Fatal("forged runtime authority accepted")
			}
		})
	}
}

func TestUnqualifiedRuntimeEffectsRetainEveryRequiredTest(t *testing.T) {
	for _, gap := range []string{"network", "subprocess", "worker", "native-addon", "file-descriptor", "outside-snapshot", "clock", "random", "filesystem-write", "record-limit"} {
		t.Run(gap, func(t *testing.T) {
			c := model.Config{Schema: 1, Workspaces: []model.Workspace{{ID: "js", Root: ".", Adapter: "vitest", NodeRuntime: &model.NodeRuntime{}, Command: model.Command{Dir: ".", Executable: "vitest", Args: []string{"run"}}}}}
			units := []model.Unit{{ID: "js::a.test.ts", Workspace: "js", Kind: "vitest-file", Selector: "a.test.ts"}, {ID: "js::b.test.ts", Workspace: "js", Kind: "vitest-file", Selector: "b.test.ts"}}
			p := model.Plan{Schema: 1, Mode: "full-fallback", Base: "base", ConfigDigest: config.Digest(c), ContextDigest: "ctx", ToolsDigest: "tools", Selected: units, Proposed: units[:1], Commands: []model.Command{c.Workspaces[0].Command}}
			r := Execution{Schema: 1, Mode: "runtime-observation", Candidate: "base", ConfigDigest: p.ConfigDigest, ContextDigest: p.ContextDigest, ToolsDigest: p.ToolsDigest, Passed: true, Commands: []CommandResult{{Workspace: "js", Complete: true, RuntimeTrace: &vitestprovider.Trace{Schema: 1, Complete: true, Owners: []string{"a.test.ts", "b.test.ts"}, Gaps: []string{gap}}}}}
			data, _ := json.Marshal(r)
			file := filepath.Join(t.TempDir(), "effects.json")
			if err := os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := BroadenFromObservations(context.Background(), &p, c, file); err != nil {
				t.Fatal(err)
			}
			if len(p.Proposed) != 2 || len(p.Selected) != 2 || p.Mode != "full-fallback" || !reflect.DeepEqual(p.Commands, []model.Command{c.Workspaces[0].Command}) {
				t.Fatalf("unsupported effect enabled omission: %+v", p)
			}
		})
	}
}

func observationFailureFixture(t *testing.T) (model.Config, model.Plan, Execution) {
	t.Helper()
	c := model.Config{Schema: model.Schema, Context: model.Context{ID: "observed-ci", OS: "linux", Arch: "amd64"}, Workspaces: []model.Workspace{{ID: "js", Root: ".", Adapter: "vitest", Command: model.Command{Dir: ".", Executable: "vitest", Args: []string{"run"}}, NodeRuntime: &model.NodeRuntime{Node: "/node", Modules: "/node_modules"}}}}
	a := model.Unit{ID: "js::a.test.ts", Workspace: "js", Kind: "vitest-file", Selector: "a.test.ts"}
	b := model.Unit{ID: "js::b.test.ts", Workspace: "js", Kind: "vitest-file", Selector: "b.test.ts"}
	p := model.Plan{Schema: model.Schema, Version: Version, Mode: "full-fallback", Base: "base", Candidate: "candidate", ContextDigest: "context", ConfigDigest: config.Digest(c), ToolsDigest: "tools", Selected: []model.Unit{a, b}, Proposed: []model.Unit{a}, Changes: []model.Change{{Path: "changed.ts", Status: "M"}}, Commands: []model.Command{c.Workspaces[0].Command}, Reasons: []model.Reason{{Unit: a.ID, Code: "static-import-influence"}}}
	r := Execution{Schema: model.Schema, Mode: "runtime-observation", Candidate: p.Base, ContextDigest: p.ContextDigest, ConfigDigest: p.ConfigDigest, ToolsDigest: p.ToolsDigest, Passed: true, Commands: []CommandResult{{Workspace: "js", Complete: true, RuntimeTrace: &vitestprovider.Trace{Schema: 1, Complete: true, Owners: []string{"a.test.ts", "b.test.ts"}, Observations: []vitestprovider.Observation{{Operation: "read", Path: "changed.ts", Owner: "b.test.ts"}}}}}}
	return c, p, r
}

func writeObservationFixture(t *testing.T, observed any) string {
	t.Helper()
	data, err := json.Marshal(observed)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "observations.json")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestRuntimeObservationFailuresBroadenWithoutChangingRequiredCommands(t *testing.T) {
	for _, test := range []struct {
		name, reason string
		mutate       func(*Execution)
	}{
		{"failed-run", "runtime-observation-incomplete", func(r *Execution) { r.Passed = false }},
		{"failed-capture", "runtime-observation-incomplete", func(r *Execution) { r.Commands[0].Complete = false }},
		{"missing-trace", "runtime-observation-incomplete", func(r *Execution) { r.Commands[0].RuntimeTrace = nil }},
		{"incomplete-trace", "runtime-observation-incomplete", func(r *Execution) { r.Commands[0].RuntimeTrace.Complete = false }},
		{"missing-workspace", "runtime-observation-missing-workspace", func(r *Execution) { r.Commands = nil }},
		{"incomplete-worker-coverage", "runtime-observation-incomplete-worker-coverage", func(r *Execution) {
			r.Commands[0].RuntimeTrace.Owners = []string{"a.test.ts"}
			r.Commands[0].RuntimeTrace.Observations = nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			c, p, observed := observationFailureFixture(t)
			expectedCommands := append([]model.Command(nil), p.Commands...)
			expectedSelected := append([]model.Unit(nil), p.Selected...)
			test.mutate(&observed)
			if err := BroadenFromObservations(context.Background(), &p, c, writeObservationFixture(t, observed)); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(p.Proposed, expectedSelected) || !reflect.DeepEqual(p.Selected, expectedSelected) || !reflect.DeepEqual(p.Commands, expectedCommands) || p.Mode != "full-fallback" {
				t.Fatalf("failed observation changed required authority or omitted proposal: %+v", p)
			}
			found := false
			for _, reason := range p.Reasons {
				if reason.Unit == "js::b.test.ts" && reason.Code == test.reason {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing explicit broadening reason %q: %+v", test.reason, p.Reasons)
			}
		})
	}
}

func TestRuntimeObservationMalformedReportsCannotInjectAuthority(t *testing.T) {
	for _, kind := range []string{"unknown-field", "trailing-document", "enforced-proof"} {
		t.Run(kind, func(t *testing.T) {
			c, p, observed := observationFailureFixture(t)
			before := p
			data, err := json.Marshal(observed)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "unknown-field":
				data = append(data[:len(data)-1], []byte(",\"unexpected\":true}")...)
			case "trailing-document":
				data = append(data, []byte("\n{\"mode\":\"affected\"}")...)
			case "enforced-proof":
				var payload map[string]any
				if err := json.Unmarshal(data, &payload); err != nil {
					t.Fatal(err)
				}
				commands := payload["commands"].([]any)
				trace := commands[0].(map[string]any)["runtime_trace"].(map[string]any)
				trace["enforced"] = true
				data, err = json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
			}
			file := filepath.Join(t.TempDir(), "untrusted.json")
			if err := os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := BroadenFromObservations(context.Background(), &p, c, file); err == nil {
				t.Fatal("malformed authority-bearing observation accepted")
			}
			if !report.Equal(before, p) {
				t.Fatalf("rejected report mutated plan: %v", report.Differences(before, p))
			}
		})
	}
}

func TestUnrelatedRuntimeObservationRetainsStaticProposal(t *testing.T) {
	c, p, observed := observationFailureFixture(t)
	expectedProposed := append([]model.Unit(nil), p.Proposed...)
	expectedSelected := append([]model.Unit(nil), p.Selected...)
	expectedCommands := append([]model.Command(nil), p.Commands...)
	observed.Commands[0].RuntimeTrace.Observations[0].Path = "unchanged.ts"
	if err := BroadenFromObservations(context.Background(), &p, c, writeObservationFixture(t, observed)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.Proposed, expectedProposed) || !reflect.DeepEqual(p.Selected, expectedSelected) || !reflect.DeepEqual(p.Commands, expectedCommands) || p.Mode != "full-fallback" {
		t.Fatalf("unrelated observation narrowed existing influence or changed required execution: %+v", p)
	}
	if !reflect.DeepEqual(p.Reasons, []model.Reason{{Unit: "js::a.test.ts", Code: "static-import-influence"}}) {
		t.Fatalf("static reasoning was lost: %+v", p.Reasons)
	}
}

func TestRuntimeObservationFreshPlanReconstructionIsDeterministic(t *testing.T) {
	c, expected, observed := observationFailureFixture(t)
	extraUnits := []model.Unit{{ID: "js::c.test.ts", Workspace: "js", Kind: "vitest-file", Selector: "c.test.ts"}, {ID: "js::d.test.ts", Workspace: "js", Kind: "vitest-file", Selector: "d.test.ts"}}
	expected.Selected = append(expected.Selected, extraUnits...)
	observed.Commands[0].RuntimeTrace.Owners = append(observed.Commands[0].RuntimeTrace.Owners, "c.test.ts", "d.test.ts")
	observed.Commands[0].RuntimeTrace.Observations[0].Owner = "*"
	observed.Commands[0].RuntimeTrace.Owners = append(observed.Commands[0].RuntimeTrace.Owners, "*")
	file := writeObservationFixture(t, observed)
	if err := BroadenFromObservations(context.Background(), &expected, c, file); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 25; attempt++ {
		freshConfig, fresh, _ := observationFailureFixture(t)
		fresh.Selected = append(fresh.Selected, extraUnits...)
		if err := BroadenFromObservations(context.Background(), &fresh, freshConfig, file); err != nil {
			t.Fatal(err)
		}
		if !report.Equal(expected, fresh) {
			t.Fatalf("fresh observation reconstruction %d differs: %v", attempt, report.Differences(expected, fresh))
		}
	}
	if len(expected.Proposed) != 4 || len(expected.Selected) != 4 || expected.Mode != "full-fallback" || !reflect.DeepEqual(expected.Commands, []model.Command{c.Workspaces[0].Command}) {
		t.Fatalf("deterministic reconstruction changed required authority: %+v", expected)
	}
}

func TestRuntimeObservationAnalysisLimitBroadensAndCancellationFails(t *testing.T) {
	c, p, r := observationFailureFixture(t)
	trace := r.Commands[0].RuntimeTrace
	trace.Observations = nil
	p.Changes = nil
	for i := 0; i < 1001; i++ {
		trace.Observations = append(trace.Observations, vitestprovider.Observation{Operation: "read", Path: fmt.Sprintf("observed/%d.ts", i), Owner: "b.test.ts"})
		p.Changes = append(p.Changes, model.Change{Path: fmt.Sprintf("changed/%d.ts", i), Status: "M"})
	}
	file := writeObservationFixture(t, r)
	if err := BroadenFromObservations(context.Background(), &p, c, file); err != nil {
		t.Fatal(err)
	}
	if len(p.Proposed) != len(p.Selected) || p.Mode != "full-fallback" {
		t.Fatal("comparison limit omitted proposals")
	}
	found := false
	for _, reason := range p.Reasons {
		if reason.Code == "runtime-observation-analysis-limit" {
			found = true
		}
	}
	if !found {
		t.Fatal("comparison limit not explained")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := BroadenFromObservations(ctx, &p, c, file); err == nil {
		t.Fatal("canceled observation analysis succeeded")
	}
}
