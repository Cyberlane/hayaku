package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/Cyberlane/hayaku/internal/config"
	"github.com/Cyberlane/hayaku/internal/model"
)

const groupVersion = "conservative-invocation-groups-v1"

// InvocationDomain binds every original scope and its prerequisites/context.
// It is private input: groups expose its identity, never raw environment values.
// The caller must include all scopes sharing this invocation lifecycle.
type InvocationDomain struct {
	ID         string
	Context    model.Context
	Workspaces []model.Workspace
}

// Coupling only broadens scheduling boundaries. Owners are inventory unit IDs.
// lifecycle/shared-state require at least two distinct known owners. unknown and
// unbounded merge the entire invocation, including when Owners is empty.
type Coupling struct {
	ID     string
	Kind   string
	Owners []string
}

// ExecutionGroup is a descriptive original-command envelope, not skip authority.
// Absent couplings do not prove independence. Runtime qualification, complete
// influence proofs and exact execution reconciliation remain separate obligations.
// IDs bind scheduling metadata; they do not replace source/input/proof digests.
type ExecutionGroup struct {
	ID        string                   `json:"id"`
	Domain    string                   `json:"domain"`
	Inventory model.ExecutionInventory `json:"inventory"`
}

// FormGroups keeps every workspace intact and applies only merges. All input
// unit/case ordering survives inside groups; groups follow their first unit.
// Malformed or incomplete data returns no partial groups, so callers broaden.
func FormGroups(domain InvocationDomain, inventory model.ExecutionInventory, couplings []Coupling) ([]ExecutionGroup, error) {
	if err := ValidateInventory(inventory); err != nil {
		return nil, err
	}
	if !identity(domain.ID) || !identity(domain.Context.ID) || !identity(domain.Context.OS) || !identity(domain.Context.Arch) || len(domain.Workspaces) == 0 || len(domain.Workspaces) > len(inventory.Units) || len(couplings) > 100000 {
		return nil, errors.New("invalid invocation domain or coupling size")
	}
	// Bound strings and collection nodes before JSON creates an encoded copy.
	if !groupMetadataBounded(domain, inventory, couplings) {
		return nil, errors.New("invocation group metadata exceeds bounds")
	}
	for key, value := range domain.Context.Env {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return nil, errors.New("invalid invocation environment")
		}
	}
	scopes := make(map[string]bool, len(domain.Workspaces))
	for _, workspace := range domain.Workspaces {
		if !identity(workspace.ID) || !identity(workspace.Adapter) || !config.Relative(workspace.Root) || scopes[workspace.ID] {
			return nil, errors.New("invalid or duplicate original workspace scope")
		}
		scopes[workspace.ID] = true
		if err := config.ValidateCommand(workspace.Command); err != nil {
			return nil, errors.New("invalid original workspace command")
		}
		for _, command := range workspace.Prerequisites {
			if err := config.ValidateCommand(command); err != nil {
				return nil, errors.New("invalid original workspace prerequisite")
			}
		}
	}
	parent := make([]int, len(inventory.Units))
	owners := make(map[string]int, len(inventory.Units))
	first := make(map[string]int, len(scopes))
	for index, unit := range inventory.Units {
		if !scopes[unit.Workspace] || (unit.Selector != "" && !identity(unit.Selector)) || (unit.Kind != "" && !identity(unit.Kind)) {
			return nil, errors.New("unit absent from original invocation scopes or invalid metadata")
		}
		parent[index], owners[unit.ID] = index, index
		if previous, exists := first[unit.Workspace]; exists {
			parent[index] = previous
		} else {
			first[unit.Workspace] = index
		}
	}
	if len(first) != len(scopes) {
		return nil, errors.New("original invocation scope has no complete case inventory")
	}
	find := func(index int) int {
		for parent[index] != index {
			parent[index] = parent[parent[index]]
			index = parent[index]
		}
		return index
	}
	merge := func(left, right int) {
		left, right = find(left), find(right)
		if left < right {
			parent[right] = left
		} else {
			parent[left] = right
		}
	}
	seen := make(map[string]bool, len(couplings))
	endpoints := 0
	allMerged := false
	for _, coupling := range couplings {
		if !identity(coupling.ID) || seen[coupling.ID] {
			return nil, errors.New("invalid or duplicate lifecycle coupling")
		}
		seen[coupling.ID] = true
		global := coupling.Kind == "unknown" || coupling.Kind == "unbounded"
		if !global && coupling.Kind != "lifecycle" && coupling.Kind != "shared-state" {
			return nil, errors.New("unsupported lifecycle coupling kind")
		}
		endpoints += len(coupling.Owners)
		if endpoints > 200000 || (!global && len(coupling.Owners) < 2) {
			return nil, errors.New("incomplete or oversized lifecycle coupling")
		}
		members := make(map[string]bool, len(coupling.Owners))
		for index, owner := range coupling.Owners {
			position, exists := owners[owner]
			if !exists || members[owner] {
				return nil, errors.New("unknown or duplicate lifecycle owner")
			}
			members[owner] = true
			if index > 0 {
				merge(owners[coupling.Owners[0]], position)
			}
		}
		if global && !allMerged {
			for index := 1; index < len(parent); index++ {
				merge(0, index)
			}
			allMerged = true
		}
	}
	groups := make([]ExecutionGroup, 0, len(scopes))
	byRoot := make(map[int]int, len(scopes))
	for index, unit := range inventory.Units {
		root := find(index)
		position, exists := byRoot[root]
		if !exists {
			position = len(groups)
			byRoot[root] = position
			groups = append(groups, ExecutionGroup{Domain: domain.ID, Inventory: model.ExecutionInventory{Schema: model.ExecutionSchema}})
		}
		groups[position].Inventory.Units = append(groups[position].Inventory.Units, unit)
	}
	for _, item := range inventory.Cases {
		position := byRoot[find(owners[item.Unit])]
		groups[position].Inventory.Cases = append(groups[position].Inventory.Cases, item)
	}
	// Bind ALL original scopes, even for a group in another workspace: a changed
	// prerequisite can produce/reset shared state without changing its case list.
	scope, err := json.Marshal(struct {
		Version   string
		Domain    InvocationDomain
		Couplings []Coupling
	}{groupVersion, domain, couplings})
	if err != nil {
		return nil, errors.New("invocation metadata cannot be encoded")
	}
	scopeDigest := sha256.Sum256(scope)
	for index := range groups {
		if err := ValidateInventory(groups[index].Inventory); err != nil {
			return nil, err
		}
		data, err := json.Marshal(struct {
			Scope     [sha256.Size]byte
			Inventory model.ExecutionInventory
		}{scopeDigest, groups[index].Inventory})
		if err != nil {
			return nil, errors.New("group inventory cannot be encoded")
		}
		digest := sha256.Sum256(data)
		groups[index].ID = hex.EncodeToString(digest[:])
	}
	return groups, nil
}

// ValidateGroups requires the complete canonical partition, including ordering
// and identity. It rejects splits, lost members and modified scope descriptors.
// Validation does not establish collection completeness or execution authority.
func ValidateGroups(domain InvocationDomain, inventory model.ExecutionInventory, couplings []Coupling, groups []ExecutionGroup) error {
	expected, err := FormGroups(domain, inventory, couplings)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(groups, expected) {
		return errors.New("groups differ from complete original invocation partition")
	}
	return nil
}

// These are fixed acyclic model values, not arbitrary caller-defined objects.
// Count nodes as well as bytes so huge collections of empty strings are bounded.
func groupMetadataBounded(values ...any) bool {
	nodes, bytes := 1000000, 16<<20
	var visit func(reflect.Value) bool
	visit = func(value reflect.Value) bool {
		nodes--
		if nodes < 0 {
			return false
		}
		switch value.Kind() {
		case reflect.String:
			if !utf8.ValidString(value.String()) {
				return false
			}
			bytes -= value.Len()
		case reflect.Pointer, reflect.Interface:
			if !value.IsNil() && !visit(value.Elem()) {
				return false
			}
		case reflect.Struct:
			for index := 0; index < value.NumField(); index++ {
				if !visit(value.Field(index)) {
					return false
				}
			}
		case reflect.Slice, reflect.Array:
			if value.Len() > nodes {
				return false
			}
			for index := 0; index < value.Len(); index++ {
				if !visit(value.Index(index)) {
					return false
				}
			}
		case reflect.Map:
			if value.Len() > nodes/2 {
				return false
			}
			entries := value.MapRange()
			for entries.Next() {
				if !visit(entries.Key()) || !visit(entries.Value()) {
					return false
				}
			}
		}
		return bytes >= 0
	}
	for _, value := range values {
		if !visit(reflect.ValueOf(value)) {
			return false
		}
	}
	return true
}
