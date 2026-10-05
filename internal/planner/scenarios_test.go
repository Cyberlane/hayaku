package planner

import (
	"reflect"
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
)

// The scenario assertions are independent of language syntax. Each integration
// also has native fixtures, so passing this harness cannot certify a frontend.
func TestSharedInfluenceScenariosAcrossRunnerIdentities(t *testing.T) {
	for _, adapter := range []string{"go", "vitest", "jest", "node-test", "unittest", "pytest", "cargo", "swift"} {
		t.Run(adapter, func(t *testing.T) {
			for name, scenario := range map[string]struct {
				effect model.Effect
				change model.Change
				want   []string
			}{
				"literal-resource":     {model.Effect{Owner: "library", Kind: "read", Path: "data.json"}, model.Change{Path: "data.json", Status: "M"}, []string{"test"}},
				"new-directory-member": {model.Effect{Owner: "library", Kind: "directory", Path: "fixtures"}, model.Change{Path: "fixtures/new.json", Status: "A"}, []string{"test"}},
				"missing-file-appears": {model.Effect{Owner: "library", Kind: "exists", Path: "optional.json"}, model.Change{Path: "optional.json", Status: "A"}, []string{"test"}},
				"deleted-parent":       {model.Effect{Owner: "library", Kind: "directory", Path: "fixtures/nested"}, model.Change{Path: "fixtures", Status: "D"}, []string{"test"}},
				"renamed-input":        {model.Effect{Owner: "library", Kind: "read", Path: "old.json"}, model.Change{Path: "other.go", OldPath: "old.json", Status: "R"}, []string{"other", "test"}},
				"unknown-dynamic":      {model.Effect{Owner: "library", Kind: "unknown"}, model.Change{}, []string{"other", "test"}},
				"network":              {model.Effect{Owner: "library", Kind: "network"}, model.Change{}, []string{"other", "test"}},
				"subprocess":           {model.Effect{Owner: "library", Kind: "subprocess"}, model.Change{}, []string{"other", "test"}},
				"clock":                {model.Effect{Owner: "library", Kind: "clock"}, model.Change{}, []string{"other", "test"}},
				"random":               {model.Effect{Owner: "library", Kind: "random"}, model.Change{}, []string{"other", "test"}},
				"shared-state":         {model.Effect{Owner: "library", Kind: "shared-state"}, model.Change{}, []string{"other", "test"}},
			} {
				t.Run(name, func(t *testing.T) {
					configuration, before := fixture()
					configuration.Workspaces[0].Adapter = adapter
					before.Adapter = adapter
					after := before
					after.Effects = []model.Effect{scenario.effect}
					var changes []model.Change
					if scenario.change.Path != "" {
						changes = append(changes, scenario.change)
					}
					plan, err := Build([]model.Evidence{before}, []model.Evidence{after}, changes, configuration)
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(ids(plan.Proposed), scenario.want) {
						t.Fatalf("lost influence: got=%v want=%v", ids(plan.Proposed), scenario.want)
					}
					if len(plan.Selected) != 2 || plan.Mode != "full-fallback" {
						t.Fatal("effect metadata enabled native skipping")
					}
				})
			}
		})
	}
}

func TestRemovedEffectsStillInfluenceCandidate(t *testing.T) {
	c, before := fixture()
	before.Effects = []model.Effect{{Owner: "library", Kind: "read", Path: "resource.json"}}
	after := before
	after.Effects = nil
	p, err := Build([]model.Evidence{before}, []model.Evidence{after}, []model.Change{{Path: "resource.json", Status: "D"}}, c)
	if err != nil || !reflect.DeepEqual(ids(p.Proposed), []string{"test"}) {
		t.Fatalf("old effect lost: proposed=%v err=%v", ids(p.Proposed), err)
	}
}

func TestEffectOwnershipAndTypesValidated(t *testing.T) {
	for _, effect := range []model.Effect{{Owner: "unknown", Kind: "read", Path: "resource"}, {Owner: "library", Kind: "typo"}, {Owner: "library", Kind: "read", Path: "../outside"}} {
		c, e := fixture()
		e.Effects = []model.Effect{effect}
		if _, err := Build([]model.Evidence{e}, []model.Evidence{e}, nil, c); err == nil {
			t.Fatalf("invalid effect accepted: %+v", effect)
		}
	}
}
