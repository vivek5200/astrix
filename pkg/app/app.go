// Package app contains shared orchestration used by the Astrix CLI and daemon.
package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vivek5200/astrix/pkg/astgraph"
	"github.com/vivek5200/astrix/pkg/ingress"
	"github.com/vivek5200/astrix/pkg/policy"
	"github.com/vivek5200/axlang/parser"
	"github.com/vivek5200/axlang/sema"
	"github.com/vivek5200/axlang/targets/rego"
)

// ValidateAstrixfile parses, checks and compiles the configured policy module.
func ValidateAstrixfile(path string) (string, error) {
	source, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	file, parseDiags := parser.New(path, string(source)).Parse()
	if len(parseDiags) > 0 {
		return "", formatDiagnostics(parseDiags, string(source))
	}
	semanticDiags := sema.Check(file)
	if len(semanticDiags) > 0 {
		return "", formatDiagnostics(semanticDiags, string(source))
	}
	return rego.Compile(file)
}

// PolicyGate loads and compiles an Astrixfile into an embedded OPA gate.
func PolicyGate(path string) (*policy.Gate, error) {
	module, err := ValidateAstrixfile(path)
	if err != nil {
		return nil, err
	}
	return policy.NewGate(module)
}

// CurrentDiff returns the staged and unstaged unified diff against HEAD.
func CurrentDiff(ctx context.Context, root string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "diff", "--unified=0", "HEAD")
	data, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git diff: %w: %s", err, strings.TrimSpace(string(data)))
	}
	return string(data), nil
}

// Impact computes the Go graph blast radius represented by a unified diff.
func Impact(root, diff string) (astgraph.ImpactSet, []ingress.FileChange, error) {
	changes, err := ingress.ParseUnifiedDiff(diff)
	if err != nil {
		return astgraph.ImpactSet{}, nil, err
	}
	sources := map[string][]byte{}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if rel != "." && (entry.Name() == ".git" || entry.Name() == ".astrix" || entry.Name() == "vendor" || entry.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".go" && ext != ".py" && ext != ".ts" && ext != ".mts" && ext != ".cts" && ext != ".tsx" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sources[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		return astgraph.ImpactSet{}, nil, err
	}
	graph, err := astgraph.ParseFilesWithAdapters(sources, astgraph.DefaultTreeSitterAdapters())
	if err != nil {
		return astgraph.ImpactSet{}, changes, err
	}
	return astgraph.BlastRadius(graph, changes), changes, nil
}

// RunTests runs either all Go tests or the packages containing the selected test nodes.
func RunTests(ctx context.Context, root string, impact astgraph.ImpactSet) error {
	packages := map[string]bool{}
	for _, id := range impact.Tests {
		file, _, ok := strings.Cut(id, "::")
		if !ok {
			continue
		}
		dir := filepath.ToSlash(filepath.Dir(file))
		if dir == "." {
			dir = "."
		}
		packages["./"+strings.TrimPrefix(dir, "./")] = true
	}
	args := []string{"test"}
	if len(packages) == 0 {
		if _, err := os.Stat(filepath.Join(root, "astrix", "go.mod")); err == nil {
			args = append(args, "./astrix/...", "./axlang/...")
		} else {
			args = append(args, "./...")
		}
	} else {
		ordered := make([]string, 0, len(packages))
		for pkg := range packages {
			ordered = append(ordered, pkg)
		}
		sort.Strings(ordered)
		args = append(args, ordered...)
	}
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func formatDiagnostics(diags []parser.Diagnostic, source string) error {
	var messages []string
	for _, d := range diags {
		messages = append(messages, d.Format(source))
	}
	return fmt.Errorf("%s", strings.Join(messages, "\n"))
}
