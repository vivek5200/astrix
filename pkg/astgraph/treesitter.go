package astgraph

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	python "github.com/tree-sitter/tree-sitter-python/bindings/go"
	typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

//go:embed queries/*.scm
var embeddedTagQueries embed.FS

// NativeTreeSitterAdapter parses Python or TypeScript through the official Go bindings.
type NativeTreeSitterAdapter struct {
	LanguageName string
	Language     *sitter.Language
	QuerySource  string
}

// QueryLexicalAdapter applies custom Tree-sitter tag capture intent through the
// dependency-free structural extractor when no grammar is registered.
type QueryLexicalAdapter struct{ QuerySource string }

func (a QueryLexicalAdapter) ExtractTreeSitter(path string, source []byte) (Graph, error) {
	return (PolyglotExtractor{QuerySource: a.QuerySource}).Extract(path, source)
}

// PythonTreeSitter creates the Python grammar adapter.
func PythonTreeSitter() TreeSitterAdapter {
	return NativeTreeSitterAdapter{LanguageName: "python", Language: sitter.NewLanguage(python.Language()), QuerySource: embeddedQuery("python.scm")}
}

// TypeScriptTreeSitter creates the TypeScript grammar adapter.
func TypeScriptTreeSitter() TreeSitterAdapter {
	return NativeTreeSitterAdapter{LanguageName: "typescript", Language: sitter.NewLanguage(typescript.LanguageTypescript()), QuerySource: embeddedQuery("ts.scm")}
}

// TSXTreeSitter creates the TSX grammar adapter.
func TSXTreeSitter() TreeSitterAdapter {
	return NativeTreeSitterAdapter{LanguageName: "typescript", Language: sitter.NewLanguage(typescript.LanguageTSX()), QuerySource: embeddedQuery("ts.scm")}
}

func embeddedQuery(name string) string {
	data, err := embeddedTagQueries.ReadFile("queries/" + name)
	if err != nil {
		return ""
	}
	return string(data)
}

// DefaultTreeSitterAdapters returns built-in Go, JavaScript/TypeScript, and Python extractors.
func DefaultTreeSitterAdapters() map[string]TreeSitterAdapter {
	return map[string]TreeSitterAdapter{
		".py": PythonTreeSitter(),
		".js": TypeScriptTreeSitter(), ".mjs": TypeScriptTreeSitter(), ".cjs": TypeScriptTreeSitter(),
		".jsx": TSXTreeSitter(),
		".ts":  TypeScriptTreeSitter(), ".mts": TypeScriptTreeSitter(), ".cts": TypeScriptTreeSitter(), ".tsx": TSXTreeSitter(),
	}
}

// ExtractTreeSitter normalizes Python or TypeScript definitions and call sites.
func (a NativeTreeSitterAdapter) ExtractTreeSitter(path string, source []byte) (Graph, error) {
	if a.Language == nil {
		return Graph{}, fmt.Errorf("Tree-sitter language is not configured")
	}
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(a.Language); err != nil {
		return Graph{}, err
	}
	tree := parser.Parse(source, nil)
	if tree == nil {
		return Graph{}, fmt.Errorf("Tree-sitter returned no parse tree for %s", path)
	}
	defer tree.Close()
	root := tree.RootNode()
	if root == nil {
		return Graph{}, fmt.Errorf("Tree-sitter returned an empty syntax tree for %s", path)
	}
	if a.QuerySource != "" {
		return extractWithQuery(path, source, root, a.Language, a.QuerySource)
	}
	g := NewGraph()
	type definition struct {
		node           *sitter.Node
		name, kind, id string
		test           bool
	}
	var defs []definition
	testFile := isTestFile(path)
	var collect func(*sitter.Node)
	collect = func(node *sitter.Node) {
		if isDefinitionNode(a.LanguageName, node.Kind()) {
			nameNode := node.ChildByFieldName("name")
			if nameNode != nil {
				name := nodeText(nameNode, source)
				if name != "" {
					lineStart := node.StartPosition().Row + 1
					lineEnd := node.EndPosition().Row + 1
					id := filepath.ToSlash(path) + "::" + name
					isTest := testFile && looksLikeTest(name)
					body := nodeText(node, source)
					digest := sha256.Sum256([]byte(body))
					defs = append(defs, definition{node: node, name: name, kind: node.Kind(), id: id, test: isTest})
					g.AddDefinition(SymbolNode{ID: id, Name: name, File: filepath.ToSlash(path), Kind: node.Kind(), StartLine: int(lineStart), EndLine: int(lineEnd), IsTest: isTest, ContentHash: hex.EncodeToString(digest[:])})
				}
			}
		}
		for i := uint(0); i < node.NamedChildCount(); i++ {
			child := node.NamedChild(i)
			if child != nil {
				collect(child)
			}
		}
	}
	collect(root)
	for _, def := range defs {
		body := def.node.ChildByFieldName("body")
		if body == nil {
			body = def.node
		}
		var walk func(*sitter.Node)
		walk = func(node *sitter.Node) {
			if node != body && isDefinitionNode(a.LanguageName, node.Kind()) {
				return
			}
			if isCallNode(a.LanguageName, node.Kind()) {
				if target := callName(node, source); target != "" {
					g.AddReference(def.id, target)
				}
			}
			for i := uint(0); i < node.NamedChildCount(); i++ {
				child := node.NamedChild(i)
				if child != nil {
					walk(child)
				}
			}
		}
		walk(body)
	}
	return g, nil
}

func extractWithQuery(path string, source []byte, root *sitter.Node, language *sitter.Language, sourceQuery string) (Graph, error) {
	query, qerr := sitter.NewQuery(language, sourceQuery)
	if qerr != nil {
		return Graph{}, fmt.Errorf("compile Tree-sitter query: %s", qerr.Message)
	}
	defer query.Close()
	cursor := sitter.NewQueryCursor()
	defer cursor.Close()
	matches := cursor.Matches(query, root, source)
	names := query.CaptureNames()
	g := NewGraph()
	type defRange struct {
		id, name   string
		start, end uint
	}
	var defs []defRange
	var refs []struct {
		start uint
		name  string
	}
	for match := matches.Next(); match != nil; match = matches.Next() {
		for _, capture := range match.Captures {
			if int(capture.Index) >= len(names) {
				continue
			}
			name := names[capture.Index]
			node := capture.Node
			value := nodeText(&node, source)
			if strings.HasPrefix(name, "definition.") {
				sym := value
				for _, inner := range match.Captures {
					if int(inner.Index) < len(names) && names[inner.Index] == "name" {
						sym = nodeText(&inner.Node, source)
						break
					}
				}
				if sym == "" {
					continue
				}
				id := filepath.ToSlash(path) + "::" + sym
				start, end := node.StartPosition().Row+1, node.EndPosition().Row+1
				digest := sha256.Sum256([]byte(value))
				g.AddDefinition(SymbolNode{ID: id, Name: sym, File: filepath.ToSlash(path), Kind: strings.TrimPrefix(name, "definition."), StartLine: int(start), EndLine: int(end), IsTest: isTestFile(path) && looksLikeTest(sym), ContentHash: hex.EncodeToString(digest[:])})
				defs = append(defs, defRange{id, sym, node.StartByte(), node.EndByte()})
			} else if name == "reference.call" {
				refs = append(refs, struct {
					start uint
					name  string
				}{node.StartByte(), value})
			}
		}
	}
	for _, ref := range refs {
		owner := ""
		width := uint(^uint(0))
		for _, d := range defs {
			if ref.start >= d.start && ref.start <= d.end && d.end-d.start < width {
				owner = d.id
				width = d.end - d.start
			}
		}
		if owner != "" {
			g.AddReference(owner, ref.name)
		}
	}
	return g, nil
}

func isDefinitionNode(language, kind string) bool {
	if language == "python" {
		return kind == "function_definition"
	}
	return kind == "function_declaration" || kind == "method_definition"
}
func isCallNode(language, kind string) bool {
	if language == "python" {
		return kind == "call"
	}
	return kind == "call_expression"
}
func nodeText(node *sitter.Node, source []byte) string {
	start, end := node.StartByte(), node.EndByte()
	if start > end || end > uint(len(source)) {
		return ""
	}
	return string(source[start:end])
}
func callName(call *sitter.Node, source []byte) string {
	function := call.ChildByFieldName("function")
	if function == nil {
		return ""
	}
	if function.Kind() == "attribute" || function.Kind() == "member_expression" {
		for _, field := range []string{"attribute", "property"} {
			if n := function.ChildByFieldName(field); n != nil {
				return nodeText(n, source)
			}
		}
	}
	if function.Kind() == "identifier" || function.Kind() == "property_identifier" {
		return nodeText(function, source)
	}
	return ""
}
func isTestFile(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py") || strings.HasSuffix(base, ".test.js") || strings.HasSuffix(base, ".spec.js") || strings.HasSuffix(base, ".test.jsx") || strings.HasSuffix(base, ".spec.jsx") || strings.HasSuffix(base, ".test.ts") || strings.HasSuffix(base, ".spec.ts") || strings.HasSuffix(base, ".test.tsx") || strings.HasSuffix(base, ".spec.tsx") || strings.HasSuffix(base, ".test.mjs") || strings.HasSuffix(base, ".spec.mjs") || strings.HasSuffix(base, ".test.mts") || strings.HasSuffix(base, ".spec.mts") {
		return true
	}
	for _, part := range strings.Split(filepath.ToSlash(filepath.Dir(path)), "/") {
		if part == "test" || part == "tests" || part == "__tests__" {
			return true
		}
	}
	return false
}
func looksLikeTest(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasPrefix(lower, "test") || strings.HasPrefix(lower, "it_") || strings.HasPrefix(lower, "describe")
}
