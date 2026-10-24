package astgraph

import (
	"github.com/vivek5200/astrix/pkg/ingress"
	"os"
	"path/filepath"
	"testing"
)

func TestLeafChangeSelectsDependentTestOnly(t *testing.T) {
	files := map[string][]byte{
		"pkg/service.go":      []byte("package pkg\nfunc leaf() {}\nfunc middle() { leaf() }\nfunc unrelated() {}\n"),
		"pkg/service_test.go": []byte("package pkg\nfunc TestLeaf(t *testing.T) { leaf() }\nfunc TestUnrelated(t *testing.T) { unrelated() }\n"),
	}
	graph, err := ParseFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	impact := BlastRadius(graph, []ingress.FileChange{{Path: "pkg/service.go", Ranges: []ingress.LineRange{{Start: 2, End: 2}}}})
	if len(impact.ChangedSymbols) != 1 {
		t.Fatalf("changed=%v", impact.ChangedSymbols)
	}
	if len(impact.Tests) != 1 || impact.Tests[0] != "pkg/service_test.go::TestLeaf" {
		t.Fatalf("tests=%v", impact.Tests)
	}
	if len(impact.ImpactedSymbols) != 3 {
		t.Fatalf("impact=%v", impact.ImpactedSymbols)
	}
}

func TestLoadPathWalksSourcesAndSkipsDependencyTrees(t *testing.T) {
	root := t.TempDir()
	for name, src := range map[string]string{
		"pkg/math.go":             "package pkg\nfunc Add(a, b int) int { return a+b }\n",
		"web/app.ts":              "function boot() {}\n",
		"tool/main.py":            "def run():\n    return 1\n",
		"vendor/ignored.go":       "not valid go",
		"node_modules/ignored.ts": "not valid ts",
		".git/ignored.py":         "not valid python",
		".astrix/ignored.go":      "not valid go",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	g, err := LoadPath(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := g.Nodes["pkg/math.go::Add"]; !ok {
		t.Fatalf("Go source missing: %#v", g.Nodes)
	}
	if _, ok := g.Nodes["web/app.ts::boot"]; !ok {
		t.Fatalf("TypeScript source missing: %#v", g.Nodes)
	}
	if _, ok := g.Nodes["tool/main.py::run"]; !ok {
		t.Fatalf("Python source missing: %#v", g.Nodes)
	}
	for _, id := range []string{"vendor/ignored.go::ignored", "node_modules/ignored.ts::ignored", ".git/ignored.py::ignored", ".astrix/ignored.go::ignored"} {
		if _, ok := g.Nodes[id]; ok {
			t.Errorf("skipped path was loaded: %s", id)
		}
	}
}

func TestGoExtractorNormalizesMethodAndTestNodes(t *testing.T) {
	g, err := (GoExtractor{}).Extract("x_test.go", []byte("package p\ntype T struct{}\nfunc (t *T) M() {}\nfunc TestM(t *testing.T) { (&T{}).M() }\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := g.Nodes["x_test.go::T.M"]; !ok {
		t.Fatalf("method missing: %#v", g.Nodes)
	}
	if node, ok := g.Nodes["x_test.go::TestM"]; !ok || !node.IsTest {
		t.Fatalf("test node=%+v, found=%v", node, ok)
	}
}

func TestTreeSitterAdaptersNormalizeTypeScriptAndPython(t *testing.T) {
	files := map[string][]byte{
		"src/service.ts":        []byte("function leaf() {}\nfunction caller() { leaf(); }\n"),
		"tests/service.test.ts": []byte("function testLeaf() { leaf(); }\n"),
		"src/service.py":        []byte("def leaf():\n    return 1\ndef caller():\n    return leaf()\n"),
	}
	graph, err := ParseFilesWithAdapters(files, DefaultTreeSitterAdapters())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := graph.Nodes["src/service.ts::leaf"]; !ok {
		t.Fatalf("TypeScript function missing: %#v", graph.Nodes)
	}
	if node, ok := graph.Nodes["tests/service.test.ts::testLeaf"]; !ok || !node.IsTest {
		t.Fatalf("TypeScript test node=%+v found=%v", node, ok)
	}
	if _, ok := graph.Nodes["src/service.py::caller"]; !ok {
		t.Fatalf("Python function missing: %#v", graph.Nodes)
	}
	impact := BlastRadius(graph, []ingress.FileChange{{Path: "src/service.ts", Ranges: []ingress.LineRange{{Start: 1, End: 1}}}})
	if len(impact.Tests) != 1 || impact.Tests[0] != "tests/service.test.ts::testLeaf" {
		t.Fatalf("TypeScript impacted tests=%v", impact.Tests)
	}
}
