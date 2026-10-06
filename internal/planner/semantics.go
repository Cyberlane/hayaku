package planner

import (
	"errors"

	"github.com/Cyberlane/hayaku/internal/graph"
)

// semanticChanges seeds influence whose meaning changed independently of raw
// file changes. Sets ignore discovery order and duplicate equivalent edges.
// Case, context and enforced-input identities additionally belong to the exact
// execution binding; this descriptive planner never grants receipt authority.
func semanticChanges(before, after inventory) (map[string]bool, error) {
	for _, items := range []inventory{before, after} {
		count := len(items.nodes) + len(items.units) + len(items.edges) + len(items.effects)
		for _, owners := range items.inputs {
			count += len(owners)
			if count > graph.MaxEdges {
				return nil, errors.New("semantic influence inventory exceeds limit")
			}
		}
		if count > graph.MaxEdges {
			return nil, errors.New("semantic influence inventory exceeds limit")
		}
	}
	old, current := semanticOwners(before), semanticOwners(after)
	changed := map[string]bool{}
	for key, owner := range old {
		if _, exists := current[key]; !exists {
			changed[owner] = true
		}
	}
	for key, owner := range current {
		if _, exists := old[key]; !exists {
			changed[owner] = true
		}
	}
	return changed, nil
}

func semanticOwners(items inventory) map[[5]string]string {
	owners := map[[5]string]string{}
	for node, workspace := range items.nodes {
		owners[[5]string{"node", node, workspace}] = node
	}
	for _, unit := range items.units {
		owners[[5]string{"unit", unit.ID, unit.Workspace, unit.Kind, unit.Selector}] = unit.ID
	}
	for _, edge := range items.edges {
		owners[[5]string{"edge", edge.From, edge.To, edge.Reason}] = edge.To
	}
	for input, targets := range items.inputs {
		for _, owner := range targets {
			owners[[5]string{"input", input, owner}] = owner
		}
	}
	for _, effect := range items.effects {
		owners[[5]string{"effect", effect.Owner, effect.Kind, effect.Path}] = effect.Owner
	}
	return owners
}
