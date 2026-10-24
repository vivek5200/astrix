package astgraph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vivek5200/astrix/pkg/ingress"
)

func TestTC_AST_01_RustAttributesSpansAndCalls(t *testing.T) {
	src := []byte("pub fn compute_tax() { round_cents(); }\nfn round_cents() {}\n#[test]\nfn test_compute_tax() { compute_tax(); }\n#[tokio::test]\nasync fn test_async_tax() { compute_tax(); }\n")
	g, err := ParseSource("src/lib.rs", src, nil)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string][2]int{"src/lib.rs::compute_tax": {1, 1}, "src/lib.rs::round_cents": {2, 2}, "src/lib.rs::test_compute_tax": {4, 4}, "src/lib.rs::test_async_tax": {6, 6}} {
		n, ok := g.Nodes[id]
		if !ok || n.StartLine != want[0] || n.EndLine != want[1] {
			t.Errorf("%s node=%+v found=%v", id, n, ok)
		}
	}
	if !g.Nodes["src/lib.rs::test_compute_tax"].IsTest || !g.Nodes["src/lib.rs::test_async_tax"].IsTest {
		t.Fatalf("attribute tests not detected")
	}
	for _, edge := range []RefEdge{{"src/lib.rs::test_compute_tax", "compute_tax"}, {"src/lib.rs::compute_tax", "round_cents"}} {
		if !hasRef(g, edge) {
			t.Errorf("missing call edge %+v", edge)
		}
	}
}

func TestTC_AST_02_04_MultilanguageMethodsAndTests(t *testing.T) {
	java, _ := ParseFilesWithAdapters(map[string][]byte{
		"InvoiceTest.java":    []byte("class InvoiceTest {\n @Test void shouldCalculateInvoice() { InvoiceService.calculate(); }\n}"),
		"InvoiceService.java": []byte("class InvoiceService {\n void calculate() {}\n}"),
		"UserService.java":    []byte("class UserService {\n void login() {}\n}"),
	}, nil)
	if n, ok := java.Nodes["InvoiceTest.java::shouldCalculateInvoice"]; !ok || !n.IsTest {
		t.Fatalf("java test method: %+v %v", n, ok)
	}
	if !hasRef(java, RefEdge{"InvoiceTest.java::shouldCalculateInvoice", "calculate"}) {
		t.Fatal("Java method reference missing")
	}
	impact := BlastRadius(java, []ingress.FileChange{{Path: "InvoiceService.java", Ranges: []ingress.LineRange{{Start: 2, End: 2}}}})
	if !containsString(impact.ImpactedSymbols, "InvoiceTest.java::shouldCalculateInvoice") || containsString(impact.ImpactedSymbols, "UserService.java::login") {
		t.Fatalf("Java method impact did not isolate calculate callers: %+v", impact)
	}
	for path, src := range map[string]string{"billing_spec.rb": "class BillingSpec\n  def test_charge\n    charge()\n  end\nend\n", "BillingTest.php": "class BillingTest { public function testCharge() { charge(); } }"} {
		g, err := ParseSource(path, []byte(src), nil)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, d := range g.Definitions {
			if d.Symbol == "test_charge" || d.Symbol == "testCharge" {
				found = true
				if !g.Nodes[d.NodeID].IsTest {
					t.Errorf("%s not marked test", path)
				}
			}
		}
		if !found {
			t.Errorf("test method absent in %s: %+v", path, g.Nodes)
		}
	}
	cs, _ := ParseSource("Things.cs", []byte("public class Things { [Fact] public void ShouldWork() { Work(); } }"), nil)
	if !cs.Nodes["Things.cs::ShouldWork"].IsTest {
		t.Errorf("C# Fact annotation missed: %+v", cs.Nodes)
	}
}

func TestTC_AST_03_CPPHeaderToGoogleTestGraph(t *testing.T) {
	files := map[string][]byte{"math.hpp": []byte("inline int multiply(int a,int b) { return a*b; }"), "math.cpp": []byte("int multiply_impl() { return multiply(2,3); }"), "math_test.cpp": []byte("TEST(MathSuite, Multiply) { multiply_impl(); }\nTEST_F(MathFixture, Divide) { divide(); }")}
	g, err := ParseFilesWithAdapters(files, nil)
	if err != nil {
		t.Fatal(err)
	}
	impact := BlastRadius(g, []ingress.FileChange{{Path: "math.hpp", Ranges: []ingress.LineRange{{Start: 1, End: 1}}}})
	if !containsString(impact.Tests, "math_test.cpp::Multiply") {
		t.Fatalf("header impact lacks test: %+v nodes=%+v", impact, g.Nodes)
	}
}

func TestTC_AST_05_CommentsAndStringsDoNotCreateSymbolsOrCalls(t *testing.T) {
	src := []byte("/* fn fake() { real_fn(); } */\n// def also_fake():\nfn target_fn() {}\nfn caller() { let s = \"func not_a_func() { target_fn() }\"; target_fn(); }\n")
	g, _ := ParseSource("src/main.rs", src, nil)
	if _, ok := g.Nodes["src/main.rs::fake"]; ok {
		t.Fatal("comment created fake definition")
	}
	if _, ok := g.Nodes["src/main.rs::not_a_func"]; ok {
		t.Fatal("string created fake definition")
	}
	if countRefs(g, "src/main.rs::caller", "target_fn") != 1 {
		t.Fatalf("unexpected references: %+v", g.References)
	}
}

func TestTC_AST_07_SchemaFileLinksToMentioningSource(t *testing.T) {
	files := map[string][]byte{"schema.sql": []byte("CREATE TABLE users(id integer);"), "repository.go": []byte("package app\nfunc LoadUsers() { _ = \"users\" }"), "unrelated.go": []byte("package app\nfunc Other() {}")}
	g, err := ParseFilesWithAdapters(files, nil)
	if err != nil {
		t.Fatal(err)
	}
	impact := BlastRadius(g, []ingress.FileChange{{Path: "schema.sql", Ranges: []ingress.LineRange{{Start: 1, End: 1}}}})
	if !containsString(impact.ImpactedSymbols, "repository.go::LoadUsers") || containsString(impact.ImpactedSymbols, "unrelated.go::Other") {
		t.Fatalf("schema impact=%+v", impact)
	}
}

func TestTC_AST_06_CustomTreeSitterQueryOverridesAdapter(t *testing.T) {
	root := t.TempDir()
	qdir := filepath.Join(root, ".astrix", "queries")
	if err := os.MkdirAll(qdir, 0o755); err != nil {
		t.Fatal(err)
	}
	query := "(function_declaration name: (identifier) @name) @definition.custom_fn\n(call_expression function: (identifier) @reference.call)"
	if err := os.WriteFile(filepath.Join(qdir, "ts.scm"), []byte(query), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file.ts"), []byte("function unusual() { helper(); }\nfunction helper() {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := LoadPath(root)
	if err != nil {
		t.Fatal(err)
	}
	if n, ok := g.Nodes["file.ts::unusual"]; !ok || n.Kind != "custom_fn" {
		t.Fatalf("custom query not applied: %+v", g.Nodes)
	}
}

func TestTC_AST_06_CustomQueryForUnknownExtensionUsesLexicalExtractor(t *testing.T) {
	root := t.TempDir()
	qdir := filepath.Join(root, ".astrix", "queries")
	if err := os.MkdirAll(qdir, 0o755); err != nil {
		t.Fatal(err)
	}
	query := "(function_declaration name: (identifier) @name) @definition.function\n(call_expression function: (identifier) @reference.call)"
	if err := os.WriteFile(filepath.Join(qdir, "custom.scm"), []byte(query), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "module.custom"), []byte("fn calculate() { helper(); }\nfn helper() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	g, err := LoadPath(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := g.Nodes["module.custom::calculate"]; !ok {
		t.Fatalf("custom query did not extract definition: %+v", g.Nodes)
	}
	if !hasRef(g, RefEdge{"module.custom::calculate", "helper"}) {
		t.Fatalf("custom query did not extract call edge: %+v", g.References)
	}
}

func TestPolyglotLexicalExtractorCoversLanguageFamilies(t *testing.T) {
	fixtures := map[string]string{"a.rs": "fn run() { work(); }", "a.java": "class A { void run() { work(); } }", "a.c": "int run() { work(); }", "a.cpp": "int run() { work(); }", "a.cs": "class A { void Run() { Work(); } }", "a.rb": "def run\n work()\nend", "a.php": "function run() { work(); }", "a.kt": "fun run() { work() }", "a.swift": "func run() { work() }"}
	for path, src := range fixtures {
		t.Run(path, func(t *testing.T) {
			g, err := ParseSource(path, []byte(src), nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(g.Definitions) == 0 {
				t.Fatalf("no symbols: %s", strings.TrimSpace(src))
			}
		})
	}
}

func hasRef(g Graph, want RefEdge) bool {
	for _, r := range g.References {
		if r == want {
			return true
		}
	}
	return false
}
func countRefs(g Graph, from, to string) int {
	n := 0
	for _, r := range g.References {
		if r.From == from && r.To == to {
			n++
		}
	}
	return n
}
func containsString(v []string, s string) bool {
	for _, x := range v {
		if x == s {
			return true
		}
	}
	return false
}
