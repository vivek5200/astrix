package astgraph

import (
	"path/filepath"
	"sort"

	"github.com/vivek5200/astrix/pkg/ingress"
)

// ImpactSet is the transitive closure of changed definitions and dependent tests.
type ImpactSet struct {
	ChangedSymbols  []string `json:"changed_symbols"`
	ImpactedSymbols []string `json:"impacted_symbols"`
	Tests           []string `json:"tests"`
}

// BlastRadius computes reverse-reference closure for symbols intersecting changed lines.
func BlastRadius(graph Graph, changes []ingress.FileChange) ImpactSet {
	changedFiles := map[string][]ingress.LineRange{}
	for _, change := range changes {
		changedFiles[filepath.ToSlash(change.Path)] = append(changedFiles[filepath.ToSlash(change.Path)], change.Ranges...)
	}
	changed := map[string]bool{}
	for id, node := range graph.Nodes {
		ranges, ok := changedFiles[filepath.ToSlash(node.File)]
		if !ok {
			continue
		}
		if len(ranges) == 0 {
			changed[id] = true
			continue
		}
		for _, r := range ranges {
			if node.StartLine <= r.End && node.EndLine >= r.Start {
				changed[id] = true
				break
			}
		}
	}
	byName := map[string][]string{}
	for _, def := range graph.Definitions {
		byName[def.Symbol] = append(byName[def.Symbol], def.NodeID)
	}
	reverse := map[string][]string{}
	for _, ref := range graph.References {
		for _, target := range byName[ref.To] {
			reverse[target] = append(reverse[target], ref.From)
		}
	}
	impacted := map[string]bool{}
	queue := make([]string, 0, len(changed))
	for id := range changed {
		impacted[id] = true
		queue = append(queue, id)
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, caller := range reverse[id] {
			if !impacted[caller] {
				impacted[caller] = true
				queue = append(queue, caller)
			}
		}
	}
	result := ImpactSet{}
	for id := range changed {
		result.ChangedSymbols = append(result.ChangedSymbols, id)
	}
	for id := range impacted {
		result.ImpactedSymbols = append(result.ImpactedSymbols, id)
		if n, ok := graph.Nodes[id]; ok && n.IsTest {
			result.Tests = append(result.Tests, id)
		}
	}
	sort.Strings(result.ChangedSymbols)
	sort.Strings(result.ImpactedSymbols)
	sort.Strings(result.Tests)
	return result
}
