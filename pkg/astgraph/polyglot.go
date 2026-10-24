package astgraph

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
)

// PolyglotExtractor is a dependency-free structural fallback for languages for
// which Astrix does not have a native parser adapter. It tokenizes source while
// discarding comments and string contents, then recognizes common declaration
// and call shapes. Its output is intentionally conservative and is not a full
// language parser.
type PolyglotExtractor struct{ QuerySource string }

type polyToken struct {
	text       string
	line       int
	start, end int
	ident      bool
}

type polySymbol struct {
	name, kind          string
	first, last         int
	bodyFirst, bodyLast int
	test                bool
}

func (e PolyglotExtractor) Extract(path string, source []byte) (Graph, error) {
	ext := strings.ToLower(filepath.Ext(path))
	lang := strings.TrimPrefix(ext, ".")
	tokens := lexPolyglot(source, lang)
	if len(tokens) == 0 {
		return (FileExtractor{}).Extract(path, source)
	}
	bracePairs := matchingPolyBraces(tokens)
	var classes []polySymbol
	for i := 0; i+1 < len(tokens); i++ {
		if !isClassKeyword(tokens[i].text) || !tokens[i+1].ident {
			continue
		}
		open := findPolyToken(tokens, i+2, "{")
		if close, ok := bracePairs[open]; ok {
			classes = append(classes, polySymbol{name: tokens[i+1].text, kind: "class", first: i, last: close, bodyFirst: open, bodyLast: close})
		}
	}

	var symbols []polySymbol
	for i := 0; i < len(tokens); i++ {
		if isFunctionKeyword(tokens[i].text) {
			nameIndex := i + 1
			for nameIndex < len(tokens) && (tokens[nameIndex].text == "&" || tokens[nameIndex].text == "mut") {
				nameIndex++
			}
			if nameIndex >= len(tokens) || !tokens[nameIndex].ident {
				continue
			}
			if lang == "rb" {
				end := rubyEnd(tokens, i)
				if end <= i {
					continue
				}
				symbols = append(symbols, polySymbol{name: tokens[nameIndex].text, kind: "function", first: i, last: end, bodyFirst: nameIndex + 1, bodyLast: end, test: isTestName(tokens[nameIndex].text) || isTestPath(path)})
				continue
			}
			openParen := findPolyToken(tokens, nameIndex+1, "(")
			closeParen, ok := matchingPolyDelimiter(tokens, openParen, "(", ")")
			if !ok {
				continue
			}
			bodyOpen := findFunctionBody(tokens, closeParen+1, bracePairs)
			if bodyOpen < 0 {
				continue
			}
			end := bracePairs[bodyOpen]
			test := isTestName(tokens[nameIndex].text) || isTestPath(path) || hasTestAnnotation(tokens, i, lang)
			kind := "function"
			if insidePolyClass(i, classes) {
				kind = "method"
			}
			symbols = append(symbols, polySymbol{name: tokens[nameIndex].text, kind: kind, first: i, last: end, bodyFirst: bodyOpen + 1, bodyLast: end, test: test})
			i = nameIndex
			continue
		}

		// C-family, Java, and C# declarations use a return type before the name,
		// while calls also have the shape name(...). Only accept a body following
		// the argument list and reject control-flow and common expression forms.
		if !tokens[i].ident || i+1 >= len(tokens) || tokens[i+1].text != "(" || isControlWord(tokens[i].text) {
			continue
		}
		if i > 0 && (tokens[i-1].text == "." || tokens[i-1].text == "->" || tokens[i-1].text == "::") {
			continue
		}
		closeParen, ok := matchingPolyDelimiter(tokens, i+1, "(", ")")
		if !ok {
			continue
		}
		bodyOpen := findFunctionBody(tokens, closeParen+1, bracePairs)
		if bodyOpen < 0 || !looksLikeDeclaration(tokens, i, closeParen) {
			continue
		}
		end := bracePairs[bodyOpen]
		kind := "function"
		if insidePolyClass(i, classes) {
			kind = "method"
		}
		test := isTestName(tokens[i].text) || isTestPath(path) || hasTestAnnotation(tokens, i, lang)
		symbols = append(symbols, polySymbol{name: tokens[i].text, kind: kind, first: i, last: end, bodyFirst: bodyOpen + 1, bodyLast: end, test: test})
		i = closeParen
	}

	// GoogleTest and Catch-style macros have no language-level function keyword.
	for i := 0; i+5 < len(tokens); i++ {
		if tokens[i].text != "TEST" && tokens[i].text != "TEST_F" {
			continue
		}
		if tokens[i+1].text != "(" {
			continue
		}
		closeParen, ok := matchingPolyDelimiter(tokens, i+1, "(", ")")
		if !ok || closeParen <= i+3 {
			continue
		}
		comma := findPolyToken(tokens, i+2, ",")
		if comma < 0 || comma >= closeParen || comma+1 >= closeParen {
			continue
		}
		open := findPolyToken(tokens, closeParen+1, "{")
		end, ok := bracePairs[open]
		if !ok {
			continue
		}
		name := tokens[comma+1].text
		symbols = append(symbols, polySymbol{name: name, kind: "test", first: i, last: end, bodyFirst: open + 1, bodyLast: end, test: true})
	}

	g := NewGraph()
	for _, class := range classes {
		addPolyDefinition(&g, path, source, tokens, class)
	}
	for _, symbol := range symbols {
		if e.QuerySource == "" || containsCapturePrefix(e.QuerySource, "definition.") {
			addPolyDefinition(&g, path, source, tokens, symbol)
		}
	}
	for _, symbol := range symbols {
		if e.QuerySource != "" && !strings.Contains(e.QuerySource, "@reference.call") {
			break
		}
		id := filepath.ToSlash(path) + "::" + symbol.name
		for i := symbol.bodyFirst; i+1 < symbol.bodyLast; i++ {
			if !tokens[i].ident || tokens[i+1].text != "(" || isControlWord(tokens[i].text) {
				continue
			}
			if i > 0 && (tokens[i-1].text == "fn" || tokens[i-1].text == "function" || tokens[i-1].text == "def" || tokens[i-1].text == "func" || tokens[i-1].text == "fun") {
				continue
			}
			g.AddReference(id, tokens[i].text)
		}
	}
	return g, nil
}

func containsCapturePrefix(query, prefix string) bool {
	for _, field := range strings.Fields(query) {
		if strings.HasPrefix(strings.Trim(field, "()[]{}"), "@"+prefix) {
			return true
		}
	}
	return false
}

func addPolyDefinition(g *Graph, path string, source []byte, tokens []polyToken, symbol polySymbol) {
	if symbol.first < 0 || symbol.last < symbol.first || symbol.last >= len(tokens) {
		return
	}
	start, end := tokens[symbol.first], tokens[symbol.last]
	content := source[start.start:end.end]
	digest := sha256.Sum256(content)
	id := filepath.ToSlash(path) + "::" + symbol.name
	g.AddDefinition(SymbolNode{ID: id, Name: symbol.name, File: filepath.ToSlash(path), Kind: symbol.kind, StartLine: start.line, EndLine: end.line, IsTest: symbol.test, ContentHash: hex.EncodeToString(digest[:])})
}

func lexPolyglot(source []byte, language string) []polyToken {
	data := string(source)
	var out []polyToken
	line := 1
	for i := 0; i < len(data); {
		c := data[i]
		if c == '\n' {
			line++
			i++
			continue
		}
		if c == ' ' || c == '\t' || c == '\r' || c == '\f' {
			i++
			continue
		}
		if i+1 < len(data) && c == '/' && data[i+1] == '/' {
			i = skipLine(data, i, &line)
			if i < len(data) && data[i] == '\n' {
				line++
				i++
			}
			continue
		}
		if i+1 < len(data) && c == '/' && data[i+1] == '*' {
			i += 2
			for i < len(data) {
				if data[i] == '\n' {
					line++
				}
				if i+1 < len(data) && data[i] == '*' && data[i+1] == '/' {
					i += 2
					break
				}
				i++
			}
			continue
		}
		if c == '#' && (language == "rb" || language == "php" || language == "py") {
			i = skipLine(data, i, &line)
			if i < len(data) && data[i] == '\n' {
				line++
				i++
			}
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			quote := c
			triple := quote != '`' && i+2 < len(data) && data[i+1] == quote && data[i+2] == quote
			if triple {
				i += 3
			} else {
				i++
			}
			for i < len(data) {
				if data[i] == '\n' {
					line++
				}
				if data[i] == '\\' {
					i += 2
					continue
				}
				if triple && i+2 < len(data) && data[i] == quote && data[i+1] == quote && data[i+2] == quote {
					i += 3
					break
				}
				if !triple && data[i] == quote {
					i++
					break
				}
				i++
			}
			continue
		}
		start, startLine := i, line
		if isPolyIdentStart(c) {
			i++
			for i < len(data) && isPolyIdentPart(data[i]) {
				i++
			}
			out = append(out, polyToken{text: data[start:i], line: startLine, start: start, end: i, ident: true})
			continue
		}
		if i+1 < len(data) && ((c == ':' && data[i+1] == ':') || (c == '-' && data[i+1] == '>') || (c == '=' && data[i+1] == '>')) {
			i += 2
		} else {
			i++
		}
		out = append(out, polyToken{text: data[start:i], line: startLine, start: start, end: i})
	}
	return out
}

func skipLine(data string, i int, line *int) int {
	for i < len(data) && data[i] != '\n' {
		i++
	}
	return i
}
func isPolyIdentStart(b byte) bool {
	return b == '_' || b == '$' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}
func isPolyIdentPart(b byte) bool { return isPolyIdentStart(b) || b >= '0' && b <= '9' }
func isFunctionKeyword(s string) bool {
	return s == "fn" || s == "function" || s == "def" || s == "func" || s == "fun"
}
func isClassKeyword(s string) bool {
	return s == "class" || s == "struct" || s == "trait" || s == "interface" || s == "enum"
}
func isControlWord(s string) bool {
	switch s {
	case "if", "for", "while", "switch", "catch", "return", "sizeof", "new", "delete", "throw", "assert", "TEST", "TEST_F", "match":
		return true
	}
	return false
}
func isTestName(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasPrefix(lower, "test") || strings.HasSuffix(lower, "test") || strings.HasPrefix(lower, "should")
}
func isTestPath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") || strings.HasSuffix(base, "_test.go") || strings.HasSuffix(base, "_test.py") || strings.HasSuffix(base, "_test.rs") || strings.HasSuffix(base, "_test.cpp") || strings.HasSuffix(base, "_test.c") || strings.HasSuffix(base, "_test.rb") || strings.HasSuffix(base, "test.php") || strings.HasSuffix(base, "test.java") || strings.HasSuffix(base, "spec.kt") {
		return true
	}
	for _, part := range strings.Split(filepath.ToSlash(filepath.Dir(path)), "/") {
		if part == "test" || part == "tests" || part == "__tests__" {
			return true
		}
	}
	return false
}
func findPolyToken(tokens []polyToken, from int, value string) int {
	for i := from; i < len(tokens); i++ {
		if tokens[i].text == value {
			return i
		}
		if tokens[i].text == ";" || tokens[i].text == "}" {
			break
		}
	}
	return -1
}
func matchingPolyDelimiter(tokens []polyToken, start int, open, close string) (int, bool) {
	if start < 0 || start >= len(tokens) || tokens[start].text != open {
		return 0, false
	}
	depth := 0
	for i := start; i < len(tokens); i++ {
		if tokens[i].text == open {
			depth++
		} else if tokens[i].text == close {
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}
func matchingPolyBraces(tokens []polyToken) map[int]int {
	pairs := map[int]int{}
	var stack []int
	for i, token := range tokens {
		switch token.text {
		case "{":
			stack = append(stack, i)
		case "}":
			if len(stack) > 0 {
				open := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				pairs[open] = i
			}
		}
	}
	return pairs
}
func findFunctionBody(tokens []polyToken, from int, pairs map[int]int) int {
	paren, bracket := 0, 0
	for i := from; i < len(tokens); i++ {
		switch tokens[i].text {
		case "(":
			paren++
		case ")":
			if paren > 0 {
				paren--
			}
		case "[":
			bracket++
		case "]":
			if bracket > 0 {
				bracket--
			}
		case "{":
			if paren == 0 && bracket == 0 {
				if _, ok := pairs[i]; ok {
					return i
				}
			}
		case ";", "=", "=>":
			if paren == 0 && bracket == 0 {
				return -1
			}
		case "}":
			if paren == 0 && bracket == 0 {
				return -1
			}
		}
	}
	return -1
}
func looksLikeDeclaration(tokens []polyToken, name, closeParen int) bool {
	if name == 0 || closeParen+1 >= len(tokens) {
		return false
	}
	for i := closeParen + 1; i < len(tokens) && i <= closeParen+8; i++ {
		if tokens[i].text == "{" {
			return true
		}
		if tokens[i].text == ";" || tokens[i].text == "=" {
			return false
		}
	}
	return false
}
func insidePolyClass(index int, classes []polySymbol) bool {
	for _, class := range classes {
		if index > class.bodyFirst && index < class.bodyLast {
			return true
		}
	}
	return false
}
func hasTestAnnotation(tokens []polyToken, index int, language string) bool {
	start := index - 10
	if start < 0 {
		start = 0
	}
	for i := start; i < index; i++ {
		if tokens[i].text == "test" || tokens[i].text == "Test" || tokens[i].text == "Fact" || tokens[i].text == "Theory" {
			return true
		}
	}
	return false
}
func rubyEnd(tokens []polyToken, start int) int {
	depth := 1
	for i := start + 1; i < len(tokens); i++ {
		switch tokens[i].text {
		case "def", "class", "module", "case", "begin", "do":
			depth++
		case "end":
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}
