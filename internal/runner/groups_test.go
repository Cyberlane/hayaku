package runner

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
)

func groupFixture() (InvocationDomain, model.ExecutionInventory) {
	domain := InvocationDomain{ID: "original-invocation", Context: model.Context{ID: "fixture", OS: "fixture-os", Arch: "fixture-arch", Env: map[string]string{"PRIVATE": "PRIVATE_ENV_SENTINEL"}}}
	for _, id := range []string{"one", "two", "three"} {
		domain.Workspaces = append(domain.Workspaces, model.Workspace{ID: id, Root: ".", Adapter: "arbitrary-runner", Command: model.Command{Dir: ".", Executable: "original", Args: []string{"run", id}}, Prerequisites: []model.Command{{Dir: ".", Executable: "prepare", Args: []string{id}}}})
	}
	// Deliberately interleave units/cases: sorting or rebuilding cases from unit
	// order would erase original collection/lifecycle order.
	inventory := model.ExecutionInventory{Schema: model.ExecutionSchema,
		Units: []model.Unit{
			{ID: "a", Workspace: "one", Selector: "first", Kind: "file"},
			{ID: "b", Workspace: "two", Selector: "second", Kind: "package"},
			{ID: "c", Workspace: "one", Selector: "third", Kind: "file"},
			{ID: "d", Workspace: "three", Selector: "fourth", Kind: "suite"},
			{ID: "e", Workspace: "two", Selector: "fifth", Kind: "package"},
		},
		Cases: []model.Case{{Unit: "c", ID: "same"}, {Unit: "e", ID: "same"}, {Unit: "a", ID: "same"}, {Unit: "d", ID: "only"}, {Unit: "b", ID: "same"}, {Unit: "c", ID: "later"}},
	}
	return domain, inventory
}

func TestGroupsKeepOriginalWorkspaceAndCaseOrder(t *testing.T) {
	domain, inventory := groupFixture()
	groups, err := FormGroups(domain, inventory, nil)
	if err != nil {
		t.Fatal(err)
	}
	expected := []model.ExecutionInventory{
		{Schema: 1, Units: []model.Unit{inventory.Units[0], inventory.Units[2]}, Cases: []model.Case{inventory.Cases[0], inventory.Cases[2], inventory.Cases[5]}},
		{Schema: 1, Units: []model.Unit{inventory.Units[1], inventory.Units[4]}, Cases: []model.Case{inventory.Cases[1], inventory.Cases[4]}},
		{Schema: 1, Units: []model.Unit{inventory.Units[3]}, Cases: []model.Case{inventory.Cases[3]}},
	}
	if len(groups) != len(expected) {
		t.Fatalf("expected three original scopes, got %d", len(groups))
	}
	for index, group := range groups {
		if !reflect.DeepEqual(group.Inventory, expected[index]) || group.Domain != domain.ID || len(group.ID) != 64 {
			t.Fatalf("original ordered scope changed: %+v", group)
		}
	}
	if err := ValidateGroups(domain, inventory, nil, groups); err != nil {
		t.Fatal(err)
	}
	repeated, err := FormGroups(domain, inventory, nil)
	if err != nil || !reflect.DeepEqual(groups, repeated) {
		t.Fatal("unchanged invocation formed different ordered identities")
	}
	encoded, err := json.Marshal(groups)
	if err != nil || strings.Contains(string(encoded), "PRIVATE_ENV_SENTINEL") || strings.Contains(string(encoded), "prepare") {
		t.Fatal("private invocation metadata leaked into groups")
	}
	groups[0].Inventory.Units[0].Selector = "changed-result"
	groups[0].Inventory.Cases[0].ID = "changed-result"
	if inventory.Units[0].Selector != "first" || inventory.Cases[0].ID != "same" {
		t.Fatal("output aliases independent input inventory")
	}
}

func TestGroupsRepeatedUnknownCouplingsRemainBounded(t *testing.T) {
	domain, fixture := groupFixture()
	domain.Workspaces = nil
	inventory := model.ExecutionInventory{Schema: model.ExecutionSchema}
	var couplings []Coupling
	for index := 0; index < 2000; index++ {
		name := fmt.Sprintf("scope-%d", index)
		domain.Workspaces = append(domain.Workspaces, model.Workspace{ID: name, Root: ".", Adapter: "runner", Command: model.Command{Dir: ".", Executable: "original"}})
		unit := fixture.Units[0]
		unit.ID, unit.Workspace = name, name
		inventory.Units = append(inventory.Units, unit)
		inventory.Cases = append(inventory.Cases, model.Case{Unit: name, ID: "case"})
		couplings = append(couplings, Coupling{ID: name, Kind: "unknown"})
	}
	groups, err := FormGroups(domain, inventory, couplings)
	if err != nil || len(groups) != 1 || !reflect.DeepEqual(groups[0].Inventory, inventory) {
		t.Fatalf("repeated global uncertainty lost complete inventory: %v", err)
	}
}

func TestGroupsCouplingsOnlyMergeCompleteScopes(t *testing.T) {
	for _, kind := range []string{"lifecycle", "shared-state"} {
		t.Run(kind, func(t *testing.T) {
			domain, inventory := groupFixture()
			couplings := []Coupling{{ID: "shared-startup", Kind: kind, Owners: []string{"c", "e"}}}
			groups, err := FormGroups(domain, inventory, couplings)
			if err != nil || len(groups) != 2 {
				t.Fatalf("merge failed: %v %+v", err, groups)
			}
			want := model.ExecutionInventory{Schema: 1, Units: []model.Unit{inventory.Units[0], inventory.Units[1], inventory.Units[2], inventory.Units[4]}, Cases: []model.Case{inventory.Cases[0], inventory.Cases[1], inventory.Cases[2], inventory.Cases[4], inventory.Cases[5]}}
			if !reflect.DeepEqual(groups[0].Inventory, want) {
				t.Fatal("shared owner merge lost sibling members or original order")
			}
			couplings = append(couplings, Coupling{ID: "transitive-service", Kind: kind, Owners: []string{"b", "d"}})
			groups, err = FormGroups(domain, inventory, couplings)
			if err != nil || len(groups) != 1 || !reflect.DeepEqual(groups[0].Inventory, inventory) {
				t.Fatal("transitive coupling did not merge complete invocation")
			}
		})
	}
	for _, kind := range []string{"unknown", "unbounded"} {
		for _, owners := range [][]string{nil, {"d"}} {
			domain, inventory := groupFixture()
			groups, err := FormGroups(domain, inventory, []Coupling{{ID: "uncertain", Kind: kind, Owners: owners}})
			if err != nil || len(groups) != 1 || !reflect.DeepEqual(groups[0].Inventory, inventory) {
				t.Fatalf("%s uncertainty narrowed to listed owners: %v", kind, err)
			}
		}
	}
}

func TestGroupIdentityBindsCompleteInvocationAndOrderedInventory(t *testing.T) {
	for _, mutation := range []string{"domain", "context", "environment", "sibling-command", "sibling-prerequisite", "scope-order", "build-flags", "inputs", "runtime", "unit-order", "case-order", "selector", "kind", "added-case", "deleted-case", "coupling"} {
		t.Run(mutation, func(t *testing.T) {
			domain, inventory := groupFixture()
			groups, err := FormGroups(domain, inventory, nil)
			if err != nil {
				t.Fatal(err)
			}
			var couplings []Coupling
			switch mutation {
			case "domain":
				domain.ID = "another-invocation"
			case "context":
				domain.Context.ID = "another-context"
			case "environment":
				domain.Context.Env["PRIVATE"] = "changed-environment"
			case "sibling-command":
				domain.Workspaces[2].Command.Args = append(domain.Workspaces[2].Command.Args, "changed-command")
			case "sibling-prerequisite":
				domain.Workspaces[2].Prerequisites[0].Args = []string{"reset-shared-service"}
			case "scope-order":
				domain.Workspaces[0], domain.Workspaces[2] = domain.Workspaces[2], domain.Workspaces[0]
			case "build-flags":
				domain.Workspaces[2].BuildFlags = []string{"changed"}
			case "inputs":
				domain.Workspaces[2].Inputs = []string{"shared-state"}
			case "runtime":
				domain.Workspaces[2].NodeRuntime = &model.NodeRuntime{Node: "/private/runtime", Modules: "/private/modules"}
			case "unit-order":
				inventory.Units[0], inventory.Units[2] = inventory.Units[2], inventory.Units[0]
			case "case-order":
				inventory.Cases[0], inventory.Cases[2] = inventory.Cases[2], inventory.Cases[0]
			case "selector":
				inventory.Units[0].Selector = "different-selection"
			case "kind":
				inventory.Units[0].Kind = "different-granularity"
			case "added-case":
				inventory.Cases = append(inventory.Cases, model.Case{Unit: "a", ID: "added"})
			case "deleted-case":
				inventory.Cases = inventory.Cases[:len(inventory.Cases)-1]
			case "coupling":
				couplings = []Coupling{{ID: "same-workspace-hook", Kind: "lifecycle", Owners: []string{"a", "c"}}}
			}
			changed, err := FormGroups(domain, inventory, couplings)
			if err != nil || changed[0].ID == groups[0].ID {
				t.Fatalf("changed %s retained group identity: %v", mutation, err)
			}
			if ValidateGroups(domain, inventory, couplings, groups) == nil {
				t.Fatal("stale original-command groups validated")
			}
		})
	}
}

func TestGroupsRejectIncompleteMalformedAndUnboundedDescriptors(t *testing.T) {
	for _, mutation := range []string{"empty-domain", "invalid-domain", "missing-context", "missing-scope", "duplicate-scope", "extra-scope", "unknown-workspace", "invalid-command", "invalid-prerequisite", "invalid-environment", "schema", "empty-cases", "duplicate-case", "empty-unit-cases", "duplicate-unit", "unknown-owner", "duplicate-owner", "empty-owners", "duplicate-coupling", "unknown-kind", "unknown-global-owner", "many-couplings", "many-owners", "large-metadata", "utf8-domain", "utf8-unit", "utf8-case", "utf8-argument", "utf8-environment-key", "utf8-environment-value"} {
		t.Run(mutation, func(t *testing.T) {
			domain, inventory := groupFixture()
			couplings := []Coupling{{ID: "hook", Kind: "lifecycle", Owners: []string{"a", "c"}}}
			switch mutation {
			case "empty-domain":
				domain.ID = ""
			case "invalid-domain":
				domain.ID = "bad\nidentity"
			case "missing-context":
				domain.Context.ID = ""
			case "missing-scope":
				domain.Workspaces = domain.Workspaces[:2]
			case "duplicate-scope":
				domain.Workspaces[2] = domain.Workspaces[0]
			case "extra-scope":
				domain.Workspaces = append(domain.Workspaces, model.Workspace{ID: "outside", Root: ".", Adapter: "runner", Command: model.Command{Dir: ".", Executable: "run"}})
			case "unknown-workspace":
				inventory.Units[0].Workspace = "outside"
			case "invalid-command":
				domain.Workspaces[0].Command.Executable = ""
			case "invalid-prerequisite":
				domain.Workspaces[0].Prerequisites[0].Dir = "../outside"
			case "invalid-environment":
				domain.Context.Env["INVALID=KEY"] = "value"
			case "schema":
				inventory.Schema++
			case "empty-cases":
				inventory.Cases = nil
			case "duplicate-case":
				inventory.Cases = append(inventory.Cases, inventory.Cases[0])
			case "empty-unit-cases":
				inventory.Cases = inventory.Cases[1:]
				inventory.Cases = inventory.Cases[:len(inventory.Cases)-1]
			case "duplicate-unit":
				inventory.Units[1] = inventory.Units[0]
			case "unknown-owner":
				couplings[0].Owners[1] = "outside"
			case "duplicate-owner":
				couplings[0].Owners[1] = "a"
			case "empty-owners":
				couplings[0].Owners = nil
			case "duplicate-coupling":
				couplings = append(couplings, couplings[0])
			case "unknown-kind":
				couplings[0].Kind = "asserted-isolated"
			case "unknown-global-owner":
				couplings[0] = Coupling{ID: "unknown", Kind: "unknown", Owners: []string{"outside"}}
			case "many-couplings":
				couplings = make([]Coupling, 100001)
			case "many-owners":
				couplings[0].Owners = make([]string, 200001)
			case "large-metadata":
				domain.Context.Env["PRIVATE"] = strings.Repeat("x", 16<<20)
			case "utf8-domain":
				domain.ID = "raw\xff"
			case "utf8-unit":
				inventory.Units[0].Selector = "raw\xff"
			case "utf8-case":
				inventory.Cases[0].ID = "raw\xff"
			case "utf8-argument":
				domain.Workspaces[0].Command.Args[0] = "raw\xff"
			case "utf8-environment-key":
				domain.Context.Env["raw\xff"] = "valid"
			case "utf8-environment-value":
				domain.Context.Env["PRIVATE"] = "raw\xff"
			}
			groups, err := FormGroups(domain, inventory, couplings)
			if err == nil || groups != nil {
				t.Fatal("malformed domain returned a usable partial partition")
			}
		})
	}
}

func TestValidateGroupsRejectsMembershipOrderAndIdentityDrift(t *testing.T) {
	for _, mutation := range []string{"missing-group", "duplicate-group", "split-workspace", "missing-unit", "missing-case", "extra-case", "reordered-case", "reordered-group", "forged-id", "wrong-domain"} {
		t.Run(mutation, func(t *testing.T) {
			domain, inventory := groupFixture()
			groups, err := FormGroups(domain, inventory, nil)
			if err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "missing-group":
				groups = groups[:2]
			case "duplicate-group":
				groups = append(groups, groups[0])
			case "split-workspace":
				groups = append(groups, groups[0])
				groups[0].Inventory.Units = groups[0].Inventory.Units[:1]
				groups[3].Inventory.Units = groups[3].Inventory.Units[1:]
			case "missing-unit":
				groups[0].Inventory.Units = groups[0].Inventory.Units[:1]
			case "missing-case":
				groups[0].Inventory.Cases = groups[0].Inventory.Cases[:2]
			case "extra-case":
				groups[0].Inventory.Cases = append(groups[0].Inventory.Cases, model.Case{Unit: "a", ID: "outside"})
			case "reordered-case":
				groups[0].Inventory.Cases[0], groups[0].Inventory.Cases[1] = groups[0].Inventory.Cases[1], groups[0].Inventory.Cases[0]
			case "reordered-group":
				groups[0], groups[1] = groups[1], groups[0]
			case "forged-id":
				groups[0].ID = strings.Repeat("0", 64)
			case "wrong-domain":
				groups[0].Domain = "another-invocation"
			}
			if ValidateGroups(domain, inventory, nil, groups) == nil {
				t.Fatal("incomplete or forged partition accepted")
			}
		})
	}
}
