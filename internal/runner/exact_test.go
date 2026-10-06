package runner

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
)

func TestExecutionBindingRejectsLossyJSONIdentities(t *testing.T) {
	left, _ := exactFixture()
	right, _ := exactFixture()
	left.Units[0].Selector, right.Units[0].Selector = string([]byte{0xff}), string([]byte{0xfe})
	a, _ := json.Marshal(left)
	b, _ := json.Marshal(right)
	if !bytes.Equal(a, b) {
		t.Fatal("fixture did not demonstrate lossy JSON normalization")
	}
	for _, field := range []string{"unit", "workspace", "case", "selector", "kind", "group"} {
		t.Run(field, func(t *testing.T) {
			inventory, _ := exactFixture()
			value := strings.Repeat("a", 64)
			binding := model.ExecutionBinding{Schema: model.ExecutionSchema, Group: "group", ProgramDigest: value, InputsDigest: value, ProofDigest: value, PolicyDigest: value, ContextDigest: value, Inventory: inventory}
			bad := string([]byte{0xff})
			switch field {
			case "unit":
				binding.Inventory.Units[0].ID, binding.Inventory.Cases[0].Unit = bad, bad
			case "workspace":
				binding.Inventory.Units[0].Workspace = bad
			case "case":
				binding.Inventory.Cases[0].ID = bad
			case "selector":
				binding.Inventory.Units[0].Selector = bad
			case "kind":
				binding.Inventory.Units[0].Kind = bad
			case "group":
				binding.Group = bad
			}
			if _, err := BindingDigest(binding); err == nil {
				t.Fatal("lossy JSON string gained a reusable identity")
			}
		})
	}
}

func exactFixture() (model.ExecutionInventory, model.Outcomes) {
	inventory := model.ExecutionInventory{
		Schema: model.ExecutionSchema,
		Units:  []model.Unit{{ID: "left", Workspace: "fixture"}, {ID: "right", Workspace: "fixture"}},
		Cases:  []model.Case{{Unit: "left", ID: "same"}, {Unit: "right", ID: "same"}},
	}
	outcomes := model.Outcomes{Complete: true,
		Units: []model.Outcome{{Unit: "right", Action: "pass"}, {Unit: "left", Action: "pass"}},
		Tests: []model.Outcome{{Unit: "right", Test: "same", Action: "pass"}, {Unit: "left", Test: "same", Action: "pass"}},
	}
	return inventory, outcomes
}

func TestExactCasesCannotBeInferredFromPassingUnits(t *testing.T) {
	for name, mutate := range map[string]func(*model.ExecutionInventory, *model.Outcomes){
		"missing-all-cases":        func(_ *model.ExecutionInventory, o *model.Outcomes) { o.Tests = nil },
		"missing-one-case":         func(_ *model.ExecutionInventory, o *model.Outcomes) { o.Tests = o.Tests[:1] },
		"replacement-case":         func(_ *model.ExecutionInventory, o *model.Outcomes) { o.Tests[0].Test = "other" },
		"duplicate-case":           func(_ *model.ExecutionInventory, o *model.Outcomes) { o.Tests[1] = o.Tests[0] },
		"wrong-ownership":          func(_ *model.ExecutionInventory, o *model.Outcomes) { o.Tests[0].Unit = "outside" },
		"empty-inventory":          func(i *model.ExecutionInventory, _ *model.Outcomes) { i.Cases = nil },
		"unsupported-schema":       func(i *model.ExecutionInventory, _ *model.Outcomes) { i.Schema++ },
		"duplicate-inventory":      func(i *model.ExecutionInventory, _ *model.Outcomes) { i.Cases = append(i.Cases, i.Cases[0]) },
		"empty-inventory-name":     func(i *model.ExecutionInventory, _ *model.Outcomes) { i.Cases[0].ID = "" },
		"invalid-inventory-name":   func(i *model.ExecutionInventory, _ *model.Outcomes) { i.Cases[0].ID = "bad\nname" },
		"unowned-inventory":        func(i *model.ExecutionInventory, _ *model.Outcomes) { i.Cases[0].Unit = "outside" },
		"empty-unit-inventory":     func(i *model.ExecutionInventory, _ *model.Outcomes) { i.Cases = i.Cases[:1] },
		"duplicate-unit-inventory": func(i *model.ExecutionInventory, _ *model.Outcomes) { i.Units = append(i.Units, i.Units[0]) },
		"invalid-workspace":        func(i *model.ExecutionInventory, _ *model.Outcomes) { i.Units[0].Workspace = "bad\x00workspace" },
		"incomplete":               func(_ *model.ExecutionInventory, o *model.Outcomes) { o.Complete = false },
	} {
		t.Run(name, func(t *testing.T) {
			inventory, outcomes := exactFixture()
			mutate(&inventory, &outcomes)
			if ValidateExact(outcomes, inventory, true, 0) == nil || AllPassed(outcomes, inventory) {
				t.Fatal("incomplete case evidence accepted")
			}
		})
	}
}

func TestExactTerminalOutcomesPreserveFailuresAndSkips(t *testing.T) {
	for _, action := range []string{"pass", "fail", "skip", "bench"} {
		t.Run(action, func(t *testing.T) {
			inventory, outcomes := exactFixture()
			outcomes.Tests[0].Action = action
			exit := 0
			if action == "fail" {
				exit = 1
			}
			if err := ValidateExact(outcomes, inventory, true, exit); err != nil {
				t.Fatalf("complete terminal outcome lost: %v", err)
			}
			if AllPassed(outcomes, inventory) != (action == "pass") {
				t.Fatal("nonpassing case became reusable")
			}
		})
	}
	inventory, outcomes := exactFixture()
	if ValidateExact(outcomes, inventory, false, 0) == nil || ValidateExact(outcomes, inventory, true, 1) == nil {
		t.Fatal("process completion or exit status lost")
	}
}

func TestExecutionBindingInvalidatesEveryBoundIdentity(t *testing.T) {
	inventory, _ := exactFixture()
	digest := strings.Repeat("a", 64)
	binding := model.ExecutionBinding{Schema: model.ExecutionSchema, Group: "two-files", ProgramDigest: digest,
		InputsDigest: digest, ProofDigest: digest, PolicyDigest: digest, ContextDigest: digest, Inventory: inventory}
	key, err := BindingDigest(binding)
	if err != nil || len(key) != 64 {
		t.Fatalf("binding: %q %v", key, err)
	}
	for _, field := range []string{"program", "inputs", "proof", "policy", "context", "group", "inventory"} {
		t.Run(field, func(t *testing.T) {
			changed := binding
			next := strings.Repeat("b", 64)
			switch field {
			case "program":
				changed.ProgramDigest = next
			case "inputs":
				changed.InputsDigest = next
			case "proof":
				changed.ProofDigest = next
			case "policy":
				changed.PolicyDigest = next
			case "context":
				changed.ContextDigest = next
			case "group":
				changed.Group = "other-group"
			case "inventory":
				changed.Inventory.Cases = append(append([]model.Case{}, inventory.Cases...), model.Case{Unit: "left", ID: "added"})
			}
			other, err := BindingDigest(changed)
			if err != nil || other == key {
				t.Fatalf("changed %s did not invalidate key: %v", field, err)
			}
		})
	}
	for _, invalid := range []string{"", "not-a-digest", strings.Repeat("A", 64), strings.Repeat("a", 62)} {
		changed := binding
		changed.ProofDigest = invalid
		if _, err := BindingDigest(changed); err == nil {
			t.Fatal("malformed proof identity accepted")
		}
	}
	changed := binding
	changed.Schema++
	if _, err := BindingDigest(changed); err == nil {
		t.Fatal("unsupported binding schema accepted")
	}
}
