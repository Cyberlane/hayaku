// Package graph computes deterministic conservative influence closure.
package graph

import (
	"fmt"
	"sort"

	"github.com/Cyberlane/hayaku/internal/model"
)

const MaxNodes = 100000
const MaxEdges = 1000000

type Graph struct {
	nodes      map[string]struct{}
	dependents map[string][]string
}

// New requires explicit nodes: an edge may not silently invent an owner.
func New(nodes []string, edges []model.Edge) (*Graph, error) {
	if len(nodes) > MaxNodes || len(edges) > MaxEdges {
		return nil, fmt.Errorf("graph exceeds node or edge limit")
	}
	g := &Graph{nodes: make(map[string]struct{}, len(nodes)), dependents: make(map[string][]string)}
	for _, node := range nodes {
		if node == "" {
			return nil, fmt.Errorf("empty graph node")
		}
		if _, exists := g.nodes[node]; exists {
			return nil, fmt.Errorf("duplicate graph node %q", node)
		}
		g.nodes[node] = struct{}{}
	}
	for _, edge := range edges {
		if !g.Has(edge.From) || !g.Has(edge.To) {
			return nil, fmt.Errorf("edge references unknown node %q -> %q", edge.From, edge.To)
		}
		if edge.Reason == "" {
			return nil, fmt.Errorf("edge %q -> %q has no reason", edge.From, edge.To)
		}
		g.dependents[edge.From] = append(g.dependents[edge.From], edge.To)
	}
	for node, targets := range g.dependents {
		sort.Strings(targets)
		g.dependents[node] = unique(targets)
	}
	return g, nil
}

func (g *Graph) Has(node string) bool { _, ok := g.nodes[node]; return ok }

// Closure returns one shortest deterministic influence path for each reached
// node. Visited nodes bound traversal even when dependencies contain cycles.
func (g *Graph) Closure(seeds []string) (map[string]string, error) {
	queue := append([]string(nil), seeds...)
	sort.Strings(queue)
	queue = unique(queue)
	parent := make(map[string]string)
	for _, seed := range queue {
		if !g.Has(seed) {
			return nil, fmt.Errorf("unknown influence seed %q", seed)
		}
		parent[seed] = ""
	}
	for cursor := 0; cursor < len(queue); cursor++ {
		for _, next := range g.dependents[queue[cursor]] {
			if _, visited := parent[next]; visited {
				continue
			}
			parent[next] = queue[cursor]
			queue = append(queue, next)
		}
	}
	return parent, nil
}

// Path expands a predecessor tree returned by Closure. It is only needed for
// explanations, so closure itself stays linear rather than storing every path.
func Path(parents map[string]string, node string) []string {
	if _, ok := parents[node]; !ok {
		return nil
	}
	var reverse []string
	for current := node; current != ""; current = parents[current] {
		reverse = append(reverse, current)
	}
	for left, right := 0, len(reverse)-1; left < right; left, right = left+1, right-1 {
		reverse[left], reverse[right] = reverse[right], reverse[left]
	}
	return reverse
}

func unique(values []string) []string {
	if len(values) == 0 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}
