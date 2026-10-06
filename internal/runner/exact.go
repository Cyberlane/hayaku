package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/Cyberlane/hayaku/internal/model"
)

// ValidateInventory checks a closed case inventory's shape. It does not establish
// discovery completeness or runtime influence; those remain provider obligations.
func ValidateInventory(inventory model.ExecutionInventory) error {
	if inventory.Schema != model.ExecutionSchema || len(inventory.Units) == 0 || len(inventory.Units) > 50000 || len(inventory.Cases) == 0 || len(inventory.Cases) > 100000 {
		return errors.New("invalid execution inventory schema or size")
	}
	units := make(map[string]int, len(inventory.Units))
	for _, unit := range inventory.Units {
		if !identity(unit.ID) || !identity(unit.Workspace) || !utf8.ValidString(unit.Selector) || !utf8.ValidString(unit.Kind) {
			return errors.New("invalid execution unit identity")
		}
		if _, exists := units[unit.ID]; exists {
			return errors.New("duplicate execution unit")
		}
		units[unit.ID] = 0
	}
	cases := make(map[[2]string]bool, len(inventory.Cases))
	for _, item := range inventory.Cases {
		key := [2]string{item.Unit, item.ID}
		if _, exists := units[item.Unit]; !exists || !identity(item.ID) || cases[key] {
			return errors.New("invalid, duplicate or unowned execution case")
		}
		cases[key] = true
		units[item.Unit]++
	}
	for _, count := range units {
		if count == 0 {
			return errors.New("execution unit has no independent case inventory")
		}
	}
	return nil
}

// ValidateExact supplements ordinary normalized validation with exact CASE
// ownership and membership. Complete failures/skips remain valid outcomes, but
// cannot become authenticated all-cases-passed results.
func ValidateExact(result model.Outcomes, inventory model.ExecutionInventory, completed bool, exit int) error {
	if err := ValidateInventory(inventory); err != nil {
		return err
	}
	if err := Validate(result, inventory.Units, completed, exit); err != nil {
		return err
	}
	if len(result.Tests) != len(inventory.Cases) {
		return errors.New("missing or unexpected execution case outcomes")
	}
	want := make(map[[2]string]bool, len(inventory.Cases))
	for _, item := range inventory.Cases {
		want[[2]string{item.Unit, item.ID}] = true
	}
	for _, item := range result.Tests {
		if !want[[2]string{item.Unit, item.Test}] {
			return errors.New("execution case outcome absent from independent inventory")
		}
	}
	return nil
}

// AllPassed is necessary for a reusable receipt, never sufficient qualification.
func AllPassed(result model.Outcomes, inventory model.ExecutionInventory) bool {
	if ValidateExact(result, inventory, true, 0) != nil || Failed(result) {
		return false
	}
	for _, items := range [][]model.Outcome{result.Units, result.Tests} {
		for _, item := range items {
			if item.Action != "pass" {
				return false
			}
		}
	}
	return true
}

// BindingDigest validates and fingerprints evidence from trusted producer code.
// A caller-created binding still has no skip authority. Ordering is retained:
// changes may conservatively invalidate reuse rather than erase lifecycle order.
func BindingDigest(binding model.ExecutionBinding) (string, error) {
	if binding.Schema != model.ExecutionSchema || !identity(binding.Group) {
		return "", errors.New("invalid execution binding identity or schema")
	}
	if err := ValidateInventory(binding.Inventory); err != nil {
		return "", err
	}
	for _, value := range []string{binding.ProgramDigest, binding.InputsDigest, binding.ProofDigest, binding.PolicyDigest, binding.ContextDigest} {
		data, err := hex.DecodeString(value)
		if err != nil || len(data) != sha256.Size || hex.EncodeToString(data) != value {
			return "", errors.New("invalid execution binding digest")
		}
	}
	data, err := json.Marshal(binding)
	if err != nil {
		return "", errors.New("execution binding cannot be encoded")
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
