// Package astgraph normalizes language parsers into a shared symbol graph.
package astgraph

// SymbolNode is a source definition in the language-neutral graph IR.
type SymbolNode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	File        string `json:"file"`
	Kind        string `json:"kind"`
	StartLine   int    `json:"start_line"`
	EndLine     int    `json:"end_line"`
	IsTest      bool   `json:"is_test,omitempty"`
	ContentHash string `json:"content_hash,omitempty"`
}

// DefEdge associates a source-level symbol name with one definition node.
type DefEdge struct {
	Symbol string `json:"symbol"`
	NodeID string `json:"node_id"`
}

// RefEdge means From references the source-level symbol named by To.
type RefEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Graph is a normalized directed symbol graph.
type Graph struct {
	Nodes       map[string]SymbolNode `json:"nodes"`
	Definitions []DefEdge             `json:"definitions"`
	References  []RefEdge             `json:"references"`
	FileHashes  map[string]string     `json:"-"`
}

// NewGraph constructs an initialized graph.
func NewGraph() Graph { return Graph{Nodes: map[string]SymbolNode{}, FileHashes: map[string]string{}} }

// AddDefinition adds a node and its symbol index edge.
func (g *Graph) AddDefinition(node SymbolNode) {
	if g.Nodes == nil {
		g.Nodes = map[string]SymbolNode{}
	}
	g.Nodes[node.ID] = node
	g.Definitions = append(g.Definitions, DefEdge{Symbol: node.Name, NodeID: node.ID})
}

// AddReference adds a source-symbol reference edge.
func (g *Graph) AddReference(from, to string) {
	g.References = append(g.References, RefEdge{From: from, To: to})
}

// Merge adds all graph nodes and edges to the receiver.
func (g *Graph) Merge(other Graph) {
	if g.Nodes == nil {
		g.Nodes = map[string]SymbolNode{}
	}
	for id, node := range other.Nodes {
		g.Nodes[id] = node
	}
	if g.FileHashes == nil {
		g.FileHashes = map[string]string{}
	}
	for path, hash := range other.FileHashes {
		g.FileHashes[path] = hash
	}
	g.Definitions = append(g.Definitions, other.Definitions...)
	g.References = append(g.References, other.References...)
}
