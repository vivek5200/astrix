package astgraph

import (
	"embed"
	"path/filepath"
	"strings"
)

//go:embed queries/*.scm
var embeddedTagQueries embed.FS

// QueryLexicalAdapter applies custom Tree-sitter tag capture intent through the
// dependency-free structural extractor when no grammar is registered.
type QueryLexicalAdapter struct{ QuerySource string }

func (a QueryLexicalAdapter) ExtractTreeSitter(path string, source []byte) (Graph, error) {
	return (PolyglotExtractor{QuerySource: a.QuerySource}).Extract(path, source)
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
