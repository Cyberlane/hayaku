package graph

import (
	"reflect"
	"testing"

	"github.com/Cyberlane/hayaku/internal/model"
)

func TestDirectionCyclesAndDeterministicPaths(t *testing.T) {
	nodes := []string{"source", "b", "a", "suite", "unrelated"}
	edges := []model.Edge{{From: "source", To: "b", Reason: "import"}, {From: "source", To: "a", Reason: "import"}, {From: "a", To: "suite", Reason: "import"}, {From: "b", To: "suite", Reason: "import"}, {From: "suite", To: "source", Reason: "cycle"}}
	g, err := New(nodes, edges)
	if err != nil {
		t.Fatal(err)
	}
	parents, err := g.Closure([]string{"source"})
	if err != nil {
		t.Fatal(err)
	}
	if got := Path(parents, "suite"); !reflect.DeepEqual(got, []string{"source", "a", "suite"}) {
		t.Fatalf("path=%v", got)
	}
	if _, found := parents["unrelated"]; found {
		t.Fatal("closure includes unrelated node")
	}
	parents, err = g.Closure([]string{"unrelated"})
	if err != nil {
		t.Fatal(err)
	}
	if len(parents) != 1 {
		t.Fatal("edges were traversed backwards")
	}
}

func TestGraphRejectsMissingInventory(t *testing.T) {
	for _, test := range []struct {
		nodes []string
		edges []model.Edge
	}{
		{[]string{"a", "a"}, nil},
		{[]string{"a"}, []model.Edge{{From: "a", To: "missing", Reason: "import"}}},
		{[]string{"a"}, []model.Edge{{From: "a", To: "a"}}},
	} {
		if _, err := New(test.nodes, test.edges); err == nil {
			t.Fatal("accepted malformed graph")
		}
	}
	g, _ := New([]string{"a"}, nil)
	if _, err := g.Closure([]string{"missing"}); err == nil {
		t.Fatal("accepted unknown seed")
	}
}

// The oracle scans every edge to a fixed point, independent of the queue-based
// implementation. Mutating direction or dropping a transitive edge fails this.
func FuzzClosureOracle(f *testing.F) {
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7})
	f.Add([]byte{0, 0, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 256 {
			data = data[:256]
		}
		nodes := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
		var edges []model.Edge
		for i := 0; i+1 < len(data); i += 2 {
			edges = append(edges, model.Edge{From: nodes[int(data[i])%len(nodes)], To: nodes[int(data[i+1])%len(nodes)], Reason: "fixture"})
		}
		g, err := New(nodes, edges)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := g.Closure([]string{"a"})
		if err != nil {
			t.Fatal(err)
		}
		expected := map[string]bool{"a": true}
		for changed := true; changed; {
			changed = false
			for _, edge := range edges {
				if expected[edge.From] && !expected[edge.To] {
					expected[edge.To] = true
					changed = true
				}
			}
		}
		if len(actual) != len(expected) {
			t.Fatalf("closure=%v expected=%v", actual, expected)
		}
		for node := range expected {
			if _, ok := actual[node]; !ok {
				t.Fatalf("missing %q", node)
			}
		}
		for node := range actual {
			p := Path(actual, node)
			if len(p) == 0 || p[0] != "a" || p[len(p)-1] != node {
				t.Fatalf("invalid path %v", p)
			}
		}
	})
}
