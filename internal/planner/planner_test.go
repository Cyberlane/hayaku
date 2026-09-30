package planner

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
)

func fixture() (model.Config, model.Evidence) {
	config := model.Config{Schema: model.Schema, Workspaces: []model.Workspace{{ID: "go", Root: "backend", Adapter: "go", Prerequisites: []model.Command{{Executable: "prepare", Args: []string{"--flag"}}}, Command: model.Command{Dir: "subdir", Executable: "go", Args: []string{"test", "-race", "./..."}}}}}
	evidence := model.Evidence{Workspace: "go", Adapter: "go", Version: "1", Nodes: []string{"library", "test", "other"}, Inputs: map[string][]string{"lib.go": {"library"}, "test.go": {"test"}, "other.go": {"other"}}, Edges: []model.Edge{{From: "library", To: "test", Reason: "import"}}, Units: []model.Unit{{ID: "test", Workspace: "go", Selector: "./test", Kind: "package"}, {ID: "other", Workspace: "go", Selector: "./other", Kind: "package"}}, Gaps: []model.Gap{{Code: "runtime-unqualified", Workspace: "go", Detail: "external inputs unbounded"}}}
	return config, evidence
}

func ids(units []model.Unit) []string {
	result := []string{}
	for _, unit := range units {
		result = append(result, unit.ID)
	}
	return result
}

func TestProposalSeparateFromRequiredCommands(t *testing.T) {
	config, evidence := fixture()
	plan, err := Build([]model.Evidence{evidence}, []model.Evidence{evidence}, []model.Change{{Path: "lib.go", Status: "M"}}, config)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids(plan.Proposed), []string{"test"}) {
		t.Fatalf("proposal=%v", plan.Proposed)
	}
	if !reflect.DeepEqual(ids(plan.Selected), []string{"other", "test"}) || plan.Mode != "full-fallback" {
		t.Fatal("required selection narrowed")
	}
	if len(plan.Commands) != 2 || plan.Commands[0].Dir != "backend" || plan.Commands[1].Dir != "backend/subdir" || !reflect.DeepEqual(plan.Commands[1].Args, config.Workspaces[0].Command.Args) {
		t.Fatalf("changed original commands: %v", plan.Commands)
	}
	if len(plan.ProposalCommands) != 0 {
		t.Fatal("planner authorized unqualified commands")
	}
	omittedExplained := false
	for _, reason := range plan.Reasons {
		if reason.Unit == "other" && reason.Code == "proposal-omitted-no-graph-influence" {
			omittedExplained = true
		}
	}
	if !omittedExplained {
		t.Fatal("missing experimental omission explanation")
	}
}

func TestUnionPreservesRemovedEdgesAndDeletedInputs(t *testing.T) {
	config, before := fixture()
	after := before
	after.Edges = nil
	after.Inputs = map[string][]string{"renamed.go": {"library"}, "test.go": {"test"}, "other.go": {"other"}}
	plan, err := Build([]model.Evidence{before}, []model.Evidence{after}, []model.Change{{Path: "renamed.go", OldPath: "lib.go", Status: "R"}}, config)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids(plan.Proposed), []string{"test"}) {
		t.Fatal("discarded old ownership or old influence")
	}
	after.Nodes = []string{"library", "other"}
	after.Units = after.Units[1:]
	after.Inputs = map[string][]string{"other.go": {"other"}}
	plan, err = Build([]model.Evidence{before}, []model.Evidence{after}, []model.Change{{Path: "test.go", Status: "D"}}, config)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids(plan.Selected), []string{"other"}) {
		t.Fatal("deleted unit remains runnable")
	}
}

func TestNewTestsUnknownInputsAndLostEvidence(t *testing.T) {
	config, before := fixture()
	after := before
	after.Nodes = append(append([]string(nil), before.Nodes...), "new")
	after.Units = append(append([]model.Unit(nil), before.Units...), model.Unit{ID: "new", Workspace: "go", Selector: "./new", Kind: "package"})
	plan, err := Build([]model.Evidence{before}, []model.Evidence{after}, nil, config)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids(plan.Proposed), []string{"new"}) {
		t.Fatalf("new unit omitted: %v", plan.Proposed)
	}
	plan, err = Build([]model.Evidence{before}, []model.Evidence{after}, []model.Change{{Path: "unrecorded config", Status: "M"}}, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Proposed) != len(plan.Selected) {
		t.Fatal("unknown input did not broaden")
	}
	plan, err = Build(nil, []model.Evidence{after}, nil, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Proposed) != len(plan.Selected) {
		t.Fatal("missing base evidence did not broaden")
	}
}

func TestCrossLanguageContract(t *testing.T) {
	config, backend := fixture()
	config.Workspaces = append(config.Workspaces, model.Workspace{ID: "web", Root: "web", Adapter: "vitest", Command: model.Command{Executable: "vitest", Args: []string{"run"}}})
	config.Contracts = []model.Contract{{Input: "schema.json", Consumers: []string{"web"}, Reason: "generated client"}}
	frontend := model.Evidence{Adapter: "vitest", Version: "1", Workspace: "web", Nodes: []string{"web-suite"}, Units: []model.Unit{{ID: "web-suite", Workspace: "web", Selector: "run", Kind: "suite"}}}
	items := []model.Evidence{backend, frontend}
	plan, err := Build(items, items, []model.Change{{Path: "schema.json", Status: "M"}}, config)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids(plan.Proposed), []string{"web-suite"}) {
		t.Fatalf("cross-language proposal=%v", plan.Proposed)
	}
	if len(plan.Selected) != 3 || len(plan.Commands) != 3 {
		t.Fatal("contract declaration enabled production omissions")
	}
	config.Contracts[0].Consumers = []string{"unknown"}
	if _, err := Build(items, items, nil, config); err == nil {
		t.Fatal("accepted unknown consumer")
	}
}

func TestRejectMalformedEvidence(t *testing.T) {
	for name, mutate := range map[string]func(*model.Evidence){
		"duplicate unit":      func(e *model.Evidence) { e.Units = append(e.Units, e.Units[0]) },
		"unknown input owner": func(e *model.Evidence) { e.Inputs = map[string][]string{"x": {"missing"}} },
		"unknown edge":        func(e *model.Evidence) { e.Edges = []model.Edge{{From: "library", To: "missing", Reason: "import"}} },
		"wrong workspace":     func(e *model.Evidence) { e.Units[0].Workspace = "unknown" },
		"empty discovery":     func(e *model.Evidence) { e.Units = nil },
		"unsafe path":         func(e *model.Evidence) { e.Inputs = map[string][]string{"../x": {"library"}} },
	} {
		t.Run(name, func(t *testing.T) {
			config, evidence := fixture()
			mutate(&evidence)
			if _, err := Build(nil, []model.Evidence{evidence}, nil, config); err == nil {
				t.Fatal("accepted malformed evidence")
			}
		})
	}
}

func TestStableIndependentOfInventoryOrderAndNoMutation(t *testing.T) {
	config, evidence := fixture()
	before, _ := json.Marshal(evidence)
	first, err := Build([]model.Evidence{evidence}, []model.Evidence{evidence}, []model.Change{{Path: "lib.go", Status: "M"}}, config)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(evidence)
	if string(before) != string(after) {
		t.Fatal("planner mutated provider evidence")
	}
	evidence.Nodes = []string{"other", "test", "library"}
	evidence.Units[0], evidence.Units[1] = evidence.Units[1], evidence.Units[0]
	second, err := Build([]model.Evidence{evidence}, []model.Evidence{evidence}, []model.Change{{Path: "lib.go", Status: "M"}}, config)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if string(a) != string(b) {
		t.Fatalf("nondeterministic plan\n%s\n%s", a, b)
	}
}

func FuzzProposalMonotonicity(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 5, 8})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 100 {
			data = data[:100]
		}
		config, evidence := fixture()
		evidence.Edges = nil
		for i := 0; i+1 < len(data); i += 2 {
			evidence.Edges = append(evidence.Edges, model.Edge{From: evidence.Nodes[int(data[i])%3], To: evidence.Nodes[int(data[i+1])%3], Reason: "fixture"})
		}
		changes := []model.Change{{Path: "lib.go", Status: "M"}}
		small, err := Build([]model.Evidence{evidence}, []model.Evidence{evidence}, changes, config)
		if err != nil {
			t.Fatal(err)
		}
		larger := evidence
		larger.Edges = append(append([]model.Edge(nil), evidence.Edges...), model.Edge{From: "library", To: "other", Reason: "additional influence"})
		big, err := Build([]model.Evidence{evidence}, []model.Evidence{larger}, changes, config)
		if err != nil {
			t.Fatal(err)
		}
		present := map[string]bool{}
		for _, unit := range big.Proposed {
			present[unit.ID] = true
		}
		for _, unit := range small.Proposed {
			if !present[unit.ID] {
				t.Fatal("adding influence shrank proposal")
			}
		}
		larger.Gaps = append(append([]model.Gap(nil), larger.Gaps...), model.Gap{Code: "lost-evidence", Workspace: "go", Detail: "collection incomplete"})
		fallback, err := Build([]model.Evidence{evidence}, []model.Evidence{larger}, changes, config)
		if err != nil {
			t.Fatal(err)
		}
		if len(fallback.Proposed) != len(fallback.Selected) {
			t.Fatal("losing evidence did not broaden proposal")
		}
		if len(small.Selected) != 2 || len(big.Selected) != 2 || len(fallback.Selected) != 2 {
			t.Fatal("required set narrowed")
		}
	})
}
