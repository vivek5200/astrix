package astgraph_test

import (
	"strconv"
	"testing"

	"github.com/vivek5200/astrix/pkg/astgraph"
	"github.com/vivek5200/astrix/pkg/cache"
	"github.com/vivek5200/astrix/pkg/ingress"
)

func TestPolyglotBlastRadiusCrossesGoTypeScriptTSXAndPython(t *testing.T) {
	files := map[string][]byte{
		"a.go":       []byte("package p\nfunc FuncA() { FuncB() }\n"),
		"b.ts":       []byte("function FuncB() { FuncA(); }\n"),
		"c.tsx":      []byte("function FuncC() { FuncB(); return <div />; }\n"),
		"c_test.py":  []byte("def TestC():\n    FuncC()\n"),
		"d.tsx":      []byte("function FuncD() { return <span />; }\n"),
		"d.test.tsx": []byte("function TestD() { FuncD(); }\n"),
	}
	graph, err := astgraph.ParseFilesWithAdapters(files, astgraph.DefaultTreeSitterAdapters())
	if err != nil {
		t.Fatal(err)
	}
	impact := astgraph.BlastRadius(graph, []ingress.FileChange{{Path: "a.go", Ranges: []ingress.LineRange{{Start: 2, End: 2}}}})
	for _, id := range []string{"a.go::FuncA", "b.ts::FuncB", "c.tsx::FuncC", "c_test.py::TestC"} {
		if !contains(impact.ImpactedSymbols, id) {
			t.Errorf("blast radius missing %s: %+v", id, impact)
		}
	}
	for _, id := range []string{"d.tsx::FuncD", "d.test.tsx::TestD"} {
		if contains(impact.ImpactedSymbols, id) {
			t.Errorf("isolated symbol %s entered blast radius: %+v", id, impact)
		}
	}
	if len(impact.Tests) != 1 || impact.Tests[0] != "c_test.py::TestC" {
		t.Fatalf("impacted tests=%v, want only Python TestC", impact.Tests)
	}
}

func TestCyclicGraphTraversalAndMerkleHashTerminate(t *testing.T) {
	polyglotFiles := map[string][]byte{
		"ping.ts": []byte("import { Pong } from './pong'\nexport function Ping() { Pong() }\n"),
		"pong.py": []byte("from ping import Ping\ndef Pong():\n    Ping()\n"),
	}
	polyglot, err := astgraph.ParseFilesWithAdapters(polyglotFiles, astgraph.DefaultTreeSitterAdapters())
	if err != nil {
		t.Fatal(err)
	}
	polyglotImpact := astgraph.BlastRadius(polyglot, []ingress.FileChange{{Path: "ping.ts", Ranges: []ingress.LineRange{{Start: 2, End: 2}}}})
	for _, id := range []string{"ping.ts::Ping", "pong.py::Pong"} {
		if !contains(polyglotImpact.ImpactedSymbols, id) {
			t.Errorf("mutually recursive cross-language function missing from closure: %s (%+v)", id, polyglotImpact)
		}
	}

	graph := astgraph.NewGraph()
	const count = 5000
	for i := 0; i < count; i++ {
		id := symbolID(i)
		graph.AddDefinition(astgraph.SymbolNode{ID: id, Name: id, File: id + ".go", StartLine: 1, EndLine: 1})
		graph.AddReference(id, symbolID((i+1)%count))
	}
	graph.AddDefinition(astgraph.SymbolNode{ID: "test::TestCycle", Name: "TestCycle", File: "cycle_test.go", IsTest: true})
	graph.AddReference("test::TestCycle", symbolID(0))
	impact := astgraph.BlastRadius(graph, []ingress.FileChange{{Path: symbolID(0) + ".go", Ranges: []ingress.LineRange{{Start: 1, End: 1}}}})
	if len(impact.ImpactedSymbols) != count+1 || len(impact.Tests) != 1 {
		t.Fatalf("cyclic closure sizes: impacted=%d tests=%d", len(impact.ImpactedSymbols), len(impact.Tests))
	}
	first := cache.SymbolSubgraphHash(graph, []string{symbolID(0)})
	second := cache.SymbolSubgraphHash(graph, []string{symbolID(count / 2)})
	if first != second {
		t.Fatal("cycle subgraph hashes should include the same complete strongly connected component")
	}
}

func TestMerkleHashIgnoresOutsideCommentsButTracksFunctionLogic(t *testing.T) {
	base := []byte("package p\n// package docs\nfunc Value() int { return 1 }\n")
	commentEdit := []byte("package p\n// different package docs\nfunc Value() int { return 1 }\n")
	logicEdit := []byte("package p\n// package docs\nfunc Value() int { return 2 }\n")
	parse := func(source []byte) astgraph.Graph {
		t.Helper()
		g, err := (astgraph.GoExtractor{}).Extract("value.go", source)
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	baseHash := cache.SymbolSubgraphHash(parse(base), []string{"value.go::Value"})
	if got := cache.SymbolSubgraphHash(parse(commentEdit), []string{"value.go::Value"}); got != baseHash {
		t.Fatalf("outside comment changed symbol hash: %s != %s", got, baseHash)
	}
	if got := cache.SymbolSubgraphHash(parse(logicEdit), []string{"value.go::Value"}); got == baseHash {
		t.Fatal("function logic edit did not invalidate symbol hash")
	}
}

func contains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func symbolID(index int) string { return "node::" + strconv.Itoa(index) }
