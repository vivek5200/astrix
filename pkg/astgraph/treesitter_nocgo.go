//go:build !cgo

package astgraph

// NativeTreeSitterAdapter mirrors the cgo adapter's metadata surface so
// parser.go's custom-query override logic compiles without Tree-sitter.
type NativeTreeSitterAdapter struct {
	LanguageName string
	QuerySource  string
}

// PythonTreeSitter falls back to the dependency-free structural extractor.
func PythonTreeSitter() TreeSitterAdapter {
	return NativeTreeSitterAdapter{LanguageName: "python", QuerySource: embeddedQuery("python.scm")}
}

// TypeScriptTreeSitter falls back to the dependency-free structural extractor.
func TypeScriptTreeSitter() TreeSitterAdapter {
	return NativeTreeSitterAdapter{LanguageName: "typescript", QuerySource: embeddedQuery("ts.scm")}
}

// TSXTreeSitter falls back to the dependency-free structural extractor.
func TSXTreeSitter() TreeSitterAdapter {
	return NativeTreeSitterAdapter{LanguageName: "typescript", QuerySource: embeddedQuery("ts.scm")}
}

// ExtractTreeSitter routes Python and TypeScript through the lexical extractor
// because the native grammars require cgo.
func (a NativeTreeSitterAdapter) ExtractTreeSitter(path string, source []byte) (Graph, error) {
	return (PolyglotExtractor{QuerySource: a.QuerySource}).Extract(path, source)
}
