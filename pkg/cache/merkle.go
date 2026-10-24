// Package cache stores content-addressed analysis artifacts on disk.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/vivek5200/astrix/pkg/astgraph"
)

// TestOutcome is the cached result for one test target.
type TestOutcome struct {
	Passed         bool   `json:"passed"`
	DurationMillis int64  `json:"duration_ms,omitempty"`
	OutputHash     string `json:"output_hash,omitempty"`
}

// Cache is a filesystem-backed content-addressable cache.
type Cache struct{ Root string }

// SymbolSubgraphHash returns a stable SHA-256 digest of selected nodes and their edges.
func SymbolSubgraphHash(graph astgraph.Graph, symbolIDs []string) string {
	byName := map[string][]string{}
	for _, def := range graph.Definitions {
		byName[def.Symbol] = append(byName[def.Symbol], def.NodeID)
	}
	forward := make(map[string][]string, len(graph.Nodes))
	for _, ref := range graph.References {
		for _, target := range byName[ref.To] {
			forward[ref.From] = append(forward[ref.From], target)
		}
	}
	selected := map[string]bool{}
	queue := append([]string(nil), symbolIDs...)
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if selected[id] {
			continue
		}
		selected[id] = true
		queue = append(queue, forward[id]...)
	}
	ids := make([]string, 0, len(selected))
	for id := range selected {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var nodes []astgraph.SymbolNode
	for _, id := range ids {
		if n, ok := graph.Nodes[id]; ok {
			nodes = append(nodes, n)
		}
	}
	var refs []astgraph.RefEdge
	for _, e := range graph.References {
		if selected[e.From] {
			refs = append(refs, e)
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].From == refs[j].From {
			return refs[i].To < refs[j].To
		}
		return refs[i].From < refs[j].From
	})
	data, _ := json.Marshal(struct {
		Nodes      []astgraph.SymbolNode
		References []astgraph.RefEdge
	}{nodes, refs})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Get reads a cached outcome, returning hit=false when the key has no entry.
func (c Cache) Get(key string) (TestOutcome, bool, error) {
	if !validKey(key) {
		return TestOutcome{}, false, fmt.Errorf("invalid cache key")
	}
	data, err := os.ReadFile(c.path(key))
	if os.IsNotExist(err) {
		return TestOutcome{}, false, nil
	}
	if err != nil {
		return TestOutcome{}, false, err
	}
	var out TestOutcome
	if err = json.Unmarshal(data, &out); err != nil {
		return TestOutcome{}, false, fmt.Errorf("decode cache entry: %w", err)
	}
	return out, true, nil
}

// Put atomically stores a test outcome under its content key.
func (c Cache) Put(key string, out TestOutcome) error {
	if !validKey(key) {
		return fmt.Errorf("invalid cache key")
	}
	path := c.path(key)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(out)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
func (c Cache) path(key string) string { return filepath.Join(c.Root, key[:2], key+".json") }
func validKey(key string) bool {
	if len(key) != 64 {
		return false
	}
	_, err := hex.DecodeString(key)
	return err == nil
}
