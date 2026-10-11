package native

import (
	"context"
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
	"github.com/Cyberlane/hayaku/internal/runner"
)

func TestProducerInventoryBindsUnitsAndCases(t *testing.T) {
	b, a, _, _ := syntheticAuthorityFixture(t)
	inventory := model.ExecutionInventory{Schema: model.ExecutionSchema,
		Units: []model.Unit{{ID: "ts::alpha.test.ts", Workspace: "ts"}, {ID: "ts::beta.test.ts", Workspace: "ts"}},
		Cases: []model.Case{{Unit: "ts::alpha.test.ts", ID: "same label"}, {Unit: "ts::beta.test.ts", ID: "same label"}},
	}
	if err := runner.ValidateInventory(inventory); err != nil {
		t.Fatal(err)
	}
	a.authority.inventory = &inventory
	program, err := fileDigestContext(context.Background(), a.Program)
	if err != nil {
		t.Fatal(err)
	}
	a.authority.binding, err = artifactBinding(b, a, program, a.authority.producer)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := verifyAuthority(context.Background(), b, a); err != nil {
		t.Fatal(err)
	}
	returned := a.inventory()
	returned.Units[0].ID, returned.Cases[0].ID = "changed", "changed"
	if _, _, err := verifyAuthority(context.Background(), b, a); err != nil {
		t.Fatal("inventory getter exposed sealed slices", err)
	}
	for _, mutate := range []func(){
		func() { inventory.Units[0].Workspace = "other" },
		func() { inventory.Cases[0].Unit = inventory.Units[1].ID },
		func() { inventory.Cases[0].ID = "new case" },
	} {
		before := cloneInventory(inventory)
		mutate()
		if _, _, err := verifyAuthority(context.Background(), b, a); err == nil {
			t.Fatal("changed private inventory retained authority")
		}
		inventory = before
	}
	passingOutcomes := model.Outcomes{Complete: true,
		Units: []model.Outcome{{Unit: inventory.Units[0].ID, Action: "pass"}, {Unit: inventory.Units[1].ID, Action: "pass"}},
		Tests: []model.Outcome{{Unit: inventory.Units[0].ID, Test: "same label", Action: "pass"}, {Unit: inventory.Units[1].ID, Test: "same label", Action: "pass"}},
	}
	if !passing(passingOutcomes, a) {
		t.Fatal("complete producer inventory was rejected")
	}
	passingOutcomes.Tests = passingOutcomes.Tests[:1]
	if passing(passingOutcomes, a) {
		t.Fatal("missing same-label case in second unit passed")
	}
	public := a
	public.authority = nil
	if public.inventory().Units[0].Workspace != "native" {
		t.Fatal("public artifact acquired private inventory")
	}
}
