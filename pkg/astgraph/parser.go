// Package astgraph extracts Go declarations and supports pluggable Tree-sitter adapters.
package astgraph

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// SourceExtractor converts one source file into the shared graph IR.
type SourceExtractor interface {
	Extract(path string, source []byte) (Graph, error)
}

// TreeSitterAdapter lets language-specific Tree-sitter grammars normalize into the IR.
// Adapters can be backed by github.com/tree-sitter/go-tree-sitter without coupling the
// graph traversal layer to a particular grammar or query layout.
type TreeSitterAdapter interface {
	ExtractTreeSitter(path string, source []byte) (Graph, error)
}

// ParseSource uses a language parser when available and otherwise preserves text
// files as file-level nodes. The fallback keeps changes visible without implying
// that an unknown language received symbol-level analysis.
func ParseSource(path string, source []byte, adapters map[string]TreeSitterAdapter) (Graph, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".go" {
		return (GoExtractor{}).Extract(path, source)
	}
	if adapter := adapters[ext]; adapter != nil {
		return adapter.ExtractTreeSitter(path, source)
	}
	if !isText(source) {
		return Graph{}, fmt.Errorf("no AST adapter registered for %s files and content is not text", ext)
	}
	return (FileExtractor{}).Extract(path, source)
}

// FileExtractor provides conservative file-level graph coverage for formats
// without a registered parser.
type FileExtractor struct{}

func (FileExtractor) Extract(path string, source []byte) (Graph, error) {
	if !isText(source) {
		return Graph{}, fmt.Errorf("cannot create file-level graph node for binary file %s", path)
	}
	lines := strings.Count(string(source), "\n") + 1
	if len(source) == 0 {
		lines = 1
	}
	digest := sha256.Sum256(source)
	id := filepath.ToSlash(path) + "::(file)"
	g := NewGraph()
	g.AddDefinition(SymbolNode{ID: id, Name: filepath.Base(path), File: filepath.ToSlash(path), Kind: "file", StartLine: 1, EndLine: lines, IsTest: isLikelyTestFile(path), ContentHash: hex.EncodeToString(digest[:])})
	return g, nil
}

func isText(source []byte) bool {
	return utf8.Valid(source) && !strings.ContainsRune(string(source), '\x00')
}

func isLikelyTestFile(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") || strings.HasSuffix(base, "_test.go") || strings.HasSuffix(base, "_test.py") || strings.HasSuffix(base, "_test.rs") || strings.HasSuffix(base, "_spec.rb") {
		return true
	}
	for _, part := range strings.Split(filepath.ToSlash(filepath.Dir(path)), "/") {
		if part == "test" || part == "tests" || part == "__tests__" {
			return true
		}
	}
	return false
}

// GoExtractor uses go/parser and go/ast to create definition and call-reference edges.
type GoExtractor struct{}

func (GoExtractor) Extract(path string, source []byte) (Graph, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, source, parser.AllErrors)
	if err != nil && file == nil {
		return Graph{}, err
	}
	if file == nil {
		return Graph{}, fmt.Errorf("no Go syntax tree for %s", path)
	}
	g := NewGraph()
	testFile := strings.HasSuffix(path, "_test.go")
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		name := fn.Name.Name
		kind := "function"
		if fn.Recv != nil && len(fn.Recv.List) > 0 {
			kind = "method"
		}
		start, end := fset.Position(fn.Pos()), fset.Position(fn.End())
		idName := name
		if fn.Recv != nil && len(fn.Recv.List) > 0 {
			idName = receiverName(fn.Recv.List[0].Type) + "." + name
		}
		id := filepath.ToSlash(path) + "::" + idName
		startOffset, endOffset := fset.Position(fn.Pos()).Offset, fset.Position(fn.End()).Offset
		body := source[startOffset:endOffset]
		digest := sha256.Sum256(body)
		node := SymbolNode{ID: id, Name: name, File: filepath.ToSlash(path), Kind: kind, StartLine: start.Line, EndLine: end.Line, IsTest: testFile && strings.HasPrefix(fn.Name.Name, "Test"), ContentHash: hex.EncodeToString(digest[:])}
		g.AddDefinition(node)
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			target := ""
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				target = fun.Name
			case *ast.SelectorExpr:
				target = fun.Sel.Name
			}
			if target != "" && target != fn.Name.Name {
				g.AddReference(id, target)
			}
			return true
		})
	}
	return g, err
}

// LoadPath parses a supported source file or recursively loads supported files from a directory.
// Directory traversal is deterministic and skips generated/dependency/cache trees.
func LoadPath(path string) (Graph, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Graph{}, err
	}
	files := map[string][]byte{}
	if !info.IsDir() {
		data, err := os.ReadFile(path)
		if err != nil {
			return Graph{}, err
		}
		files[filepath.ToSlash(path)] = data
	} else {
		err = filepath.WalkDir(path, func(current string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if current != path && skippedDirectory(entry.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			data, err := os.ReadFile(current)
			if err != nil {
				return err
			}
			if !isText(data) {
				return nil
			}
			rel, err := filepath.Rel(path, current)
			if err != nil {
				return err
			}
			files[filepath.ToSlash(rel)] = data
			return nil
		})
		if err != nil {
			return Graph{}, err
		}
	}
	g, err := ParseFilesWithAdapters(files, DefaultTreeSitterAdapters())
	if err != nil {
		return Graph{}, err
	}
	sort.Slice(g.Definitions, func(i, j int) bool { return g.Definitions[i].NodeID < g.Definitions[j].NodeID })
	sort.Slice(g.References, func(i, j int) bool {
		if g.References[i].From == g.References[j].From {
			return g.References[i].To < g.References[j].To
		}
		return g.References[i].From < g.References[j].From
	})
	return g, nil
}

func skippedDirectory(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", ".astrix", ".venv", "venv", "__pycache__", ".next", ".cache", "target", "dist", "build", "coverage":
		return true
	}
	return false
}

// FilterSymbol returns nodes with an exact or substring name match and their adjacent edges.
func FilterSymbol(g Graph, name string) Graph {
	if name == "" {
		return g
	}
	out := NewGraph()
	for id, node := range g.Nodes {
		if strings.Contains(node.Name, name) {
			out.Nodes[id] = node
		}
	}
	for _, def := range g.Definitions {
		if _, ok := out.Nodes[def.NodeID]; ok {
			out.Definitions = append(out.Definitions, def)
		}
	}
	for _, ref := range g.References {
		if _, ok := out.Nodes[ref.From]; ok {
			out.References = append(out.References, ref)
		}
	}
	return out
}

func receiverName(expr ast.Expr) string {
	switch n := expr.(type) {
	case *ast.Ident:
		return n.Name
	case *ast.StarExpr:
		return receiverName(n.X)
	case *ast.IndexExpr:
		return receiverName(n.X)
	case *ast.IndexListExpr:
		return receiverName(n.X)
	}
	return "receiver"
}

// ParseFiles extracts all files with the native Go parser, then merges them.
func ParseFiles(files map[string][]byte) (Graph, error) {
	g := NewGraph()
	extractor := GoExtractor{}
	for path, source := range files {
		part, err := extractor.Extract(path, source)
		if err != nil {
			return Graph{}, err
		}
		g.Merge(part)
	}
	return g, nil
}

// ParseFilesWithAdapters extracts a mixed-language file set into one Graph IR.
func ParseFilesWithAdapters(files map[string][]byte, adapters map[string]TreeSitterAdapter) (Graph, error) {
	g := NewGraph()
	for path, source := range files {
		part, err := ParseSource(path, source, adapters)
		if err != nil {
			return Graph{}, err
		}
		digest := sha256.Sum256(source)
		part.FileHashes = map[string]string{filepath.ToSlash(path): hex.EncodeToString(digest[:])}
		g.Merge(part)
	}
	return g, nil
}
