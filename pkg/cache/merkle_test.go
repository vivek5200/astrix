package cache

import (
	"github.com/vivek5200/astrix/pkg/astgraph"
	"testing"
)

func TestCacheRoundTrip(t *testing.T) {
	cache := Cache{Root: t.TempDir()}
	key := SymbolSubgraphHash(astgraph.NewGraph(), nil)
	if err := cache.Put(key, TestOutcome{Passed: true, DurationMillis: 42}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := cache.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || !got.Passed || got.DurationMillis != 42 {
		t.Fatalf("got=%+v hit=%v", got, ok)
	}
}

func TestSymbolHashStableAcrossSelectionOrder(t *testing.T) {
	g := astgraph.NewGraph()
	g.AddDefinition(astgraph.SymbolNode{ID: "b", Name: "b"})
	g.AddDefinition(astgraph.SymbolNode{ID: "a", Name: "a"})
	g.AddReference("a", "b")
	if a, b := SymbolSubgraphHash(g, []string{"a", "b"}), SymbolSubgraphHash(g, []string{"b", "a"}); a != b {
		t.Fatalf("hashes differ: %s %s", a, b)
	}
}

func TestSymbolHashIncludesTransitiveDependencyNodes(t *testing.T) {
	first := astgraph.NewGraph()
	first.AddDefinition(astgraph.SymbolNode{ID: "a", Name: "a", StartLine: 1})
	first.AddDefinition(astgraph.SymbolNode{ID: "b", Name: "b", StartLine: 2})
	first.AddReference("a", "b")
	second := astgraph.NewGraph()
	second.AddDefinition(astgraph.SymbolNode{ID: "a", Name: "a", StartLine: 1})
	second.AddDefinition(astgraph.SymbolNode{ID: "b", Name: "b", StartLine: 3})
	second.AddReference("a", "b")
	if SymbolSubgraphHash(first, []string{"a"}) == SymbolSubgraphHash(second, []string{"a"}) {
		t.Fatal("dependency change did not alter the transitive subgraph hash")
	}
}

func TestSymbolHashIncludesFunctionContent(t *testing.T) {
	before, err := astgraph.ParseFiles(map[string][]byte{"pkg/math.go": []byte("package pkg\nfunc Add(a, b int) int { return a - b }\n")})
	if err != nil {
		t.Fatal(err)
	}
	after, err := astgraph.ParseFiles(map[string][]byte{"pkg/math.go": []byte("package pkg\nfunc Add(a, b int) int { return a + b }\n")})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"pkg/math.go::Add"}
	if SymbolSubgraphHash(before, ids) == SymbolSubgraphHash(after, ids) {
		t.Fatal("changing a symbol body did not invalidate its Merkle key")
	}
}
