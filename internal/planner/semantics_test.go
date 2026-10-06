package planner

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Cyberlane/hayaku/internal/graph"
	"github.com/Cyberlane/hayaku/internal/model"
)

func TestSemanticInfluenceBoundsRepeatedOwners(t *testing.T) {
	configuration, evidence := fixture()
	owners := make([]string, graph.MaxEdges)
	for i := range owners {
		owners[i] = "library"
	}
	evidence.Inputs["lib.go"] = owners
	if _, err := Build([]model.Evidence{evidence}, []model.Evidence{evidence}, nil, configuration); err == nil {
		t.Fatal("unbounded repeated metadata associations accepted")
	}
}

func TestSemanticDriftInvalidatesWithoutRawChanges(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		mutate func(*model.Evidence)
		want   []string
	}{
		{"same-id-selector", func(e *model.Evidence) { e.Units[0].Selector = "./different" }, []string{"test"}},
		{"same-id-kind", func(e *model.Evidence) { e.Units[0].Kind = "suite" }, []string{"test"}},
		{"removed-edge", func(e *model.Evidence) { e.Edges = nil }, []string{"test"}},
		{"changed-edge-reason", func(e *model.Evidence) { e.Edges[0].Reason = "generated" }, []string{"test"}},
		{"changed-edge-target", func(e *model.Evidence) { e.Edges[0].To = "other" }, []string{"other", "test"}},
		{"removed-input-ownership", func(e *model.Evidence) { delete(e.Inputs, "lib.go") }, []string{"test"}},
		{"moved-input-ownership", func(e *model.Evidence) { e.Inputs["lib.go"] = []string{"other"} }, []string{"other", "test"}},
		{"changed-read-policy", func(e *model.Evidence) { e.Effects[0].Path = "new.json" }, []string{"test"}},
		{"removed-read-policy", func(e *model.Evidence) { e.Effects = nil }, []string{"test"}},
		{"new-influence-node", func(e *model.Evidence) {
			e.Nodes = append(e.Nodes, "generated")
			e.Edges = append(e.Edges, model.Edge{From: "generated", To: "test", Reason: "generated"})
		}, []string{"test"}},
		{"same-semantics-reordered", func(e *model.Evidence) {
			e.Units[0], e.Units[1] = e.Units[1], e.Units[0]
			e.Nodes[0], e.Nodes[2] = e.Nodes[2], e.Nodes[0]
			e.Edges = append(e.Edges, e.Edges[0])
			e.Inputs["lib.go"] = append(e.Inputs["lib.go"], "library")
		}, []string{}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			configuration, before := fixture()
			before.Effects = []model.Effect{{Owner: "library", Kind: "read", Path: "old.json"}}
			data, err := json.Marshal(before)
			if err != nil {
				t.Fatal(err)
			}
			var after model.Evidence
			if err := json.Unmarshal(data, &after); err != nil {
				t.Fatal(err)
			}
			scenario.mutate(&after)
			plan, err := Build([]model.Evidence{before}, []model.Evidence{after}, nil, configuration)
			if err != nil || !reflect.DeepEqual(ids(plan.Proposed), scenario.want) {
				t.Fatalf("semantic change lost: proposed=%v want=%v err=%v", ids(plan.Proposed), scenario.want, err)
			}
			if len(plan.Selected) != 2 || plan.Mode != "full-fallback" || len(plan.ProposalCommands) != 0 {
				t.Fatal("semantic evidence enabled native omission")
			}
		})
	}
}

func TestReadAncestorChangeRetainsInfluenceWithKnownOtherOwner(t *testing.T) {
	configuration, evidence := fixture()
	evidence.Inputs["optional"] = []string{"other"}
	evidence.Effects = []model.Effect{{Owner: "library", Kind: "read", Path: "optional/nested/value.json"}}
	plan, err := Build([]model.Evidence{evidence}, []model.Evidence{evidence}, []model.Change{{Path: "optional", Status: "T"}}, configuration)
	if err != nil || !reflect.DeepEqual(ids(plan.Proposed), []string{"other", "test"}) {
		t.Fatalf("ancestor resolution influence lost: proposed=%v err=%v", ids(plan.Proposed), err)
	}
	for _, gap := range plan.Gaps {
		if gap.Code == "unknown-input" {
			t.Fatal("unknown-input fallback concealed missing read influence")
		}
	}
}
