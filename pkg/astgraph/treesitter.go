package astgraph

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	python "github.com/tree-sitter/tree-sitter-python/bindings/go"
	typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

// NativeTreeSitterAdapter parses Python or TypeScript through the official Go bindings.
type NativeTreeSitterAdapter struct {
	LanguageName string
	Language     *sitter.Language
}

// PythonTreeSitter creates the Python grammar adapter.
func PythonTreeSitter() TreeSitterAdapter {
	return NativeTreeSitterAdapter{LanguageName: "python", Language: sitter.NewLanguage(python.Language())}
}

// TypeScriptTreeSitter creates the TypeScript grammar adapter.
func TypeScriptTreeSitter() TreeSitterAdapter {
	return NativeTreeSitterAdapter{LanguageName: "typescript", Language: sitter.NewLanguage(typescript.LanguageTypescript())}
}

// TSXTreeSitter creates the TSX grammar adapter.
func TSXTreeSitter() TreeSitterAdapter {
	return NativeTreeSitterAdapter{LanguageName: "typescript", Language: sitter.NewLanguage(typescript.LanguageTSX())}
}

// DefaultTreeSitterAdapters returns standard .py and .ts extractors.
func DefaultTreeSitterAdapters() map[string]TreeSitterAdapter {
	return map[string]TreeSitterAdapter{".py": PythonTreeSitter(), ".ts": TypeScriptTreeSitter(), ".mts": TypeScriptTreeSitter(), ".cts": TypeScriptTreeSitter(), ".tsx": TSXTreeSitter()}
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
	return strings.HasSuffix(base, "_test.py") || strings.HasSuffix(base, ".test.ts") || strings.HasSuffix(base, ".spec.ts") || strings.HasSuffix(base, ".test.tsx") || strings.HasSuffix(base, ".spec.tsx") || strings.HasSuffix(base, ".test.mts") || strings.HasSuffix(base, ".spec.mts")
}
func looksLikeTest(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasPrefix(lower, "test") || strings.HasPrefix(lower, "it_") || strings.HasPrefix(lower, "describe")
}
