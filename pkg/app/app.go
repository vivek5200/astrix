// Package app contains shared orchestration used by the Astrix CLI and daemon.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vivek5200/astrix/pkg/astgraph"
	"github.com/vivek5200/astrix/pkg/ingress"
	"github.com/vivek5200/astrix/pkg/policy"
	"github.com/vivek5200/astrix/pkg/runner"
	"github.com/vivek5200/axlang/ast"
	"github.com/vivek5200/axlang/parser"
	"github.com/vivek5200/axlang/sema"
	"github.com/vivek5200/axlang/targets/rego"
	"github.com/vivek5200/axlang/token"
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
	graph, err := astgraph.LoadPath(root)
	if err != nil {
		return astgraph.ImpactSet{}, changes, err
	}
	return astgraph.BlastRadius(graph, changes), changes, nil
}

// ResolveProjectProfile applies environment overrides, then .ax stage values,
// then manifest-based project defaults.
func ResolveProjectProfile(root, astrixfile string) (runner.ProjectProfile, error) {
	profile, err := runner.DetectProjectProfile(root)
	if err != nil {
		return profile, err
	}
	if data, readErr := os.ReadFile(astrixfile); readErr == nil {
		file, diags := parser.New(astrixfile, string(data)).Parse()
		if len(diags) > 0 {
			return profile, formatDiagnostics(diags, string(data))
		}
		if semaDiags := sema.Check(file); len(semaDiags) > 0 {
			return profile, formatDiagnostics(semaDiags, string(data))
		}
		for _, decl := range file.Decls {
			stage, ok := decl.(*ast.StageDecl)
			if !ok || stage.Name != "verify" {
				continue
			}
			for _, field := range stage.Fields {
				switch field.Name {
				case "image":
					if value, ok := field.Value.(*ast.LiteralExpr); ok && value.Kind == token.String {
						profile.Image = value.Value
					}
				case "command":
					if value, ok := field.Value.(*ast.LiteralExpr); ok && value.Kind == token.String {
						profile.TestCommand = strings.Fields(value.Value)
						profile.CommandOverridden = true
					}
				case "mounts":
					profile.Mounts = nil
					if list, ok := field.Value.(*ast.ListExpr); ok {
						for _, item := range list.Elements {
							if value, ok := item.(*ast.LiteralExpr); ok {
								parts := strings.Split(value.Value, ":")
								host := parts[0]
								if strings.HasPrefix(host, "~/") {
									home, _ := os.UserHomeDir()
									host = filepath.Join(home, strings.TrimPrefix(host, "~/"))
								} else if strings.HasPrefix(host, "./") || strings.HasPrefix(host, ".") {
									host = filepath.Join(root, host)
								}
								dest := parts[1]
								mode := "ro"
								if len(parts) == 3 {
									mode = parts[2]
								}
								profile.Mounts = append(profile.Mounts, runner.ProfileMount{Source: host, Destination: dest, Mode: mode})
							}
						}
					}
				}
			}
		}
	}
	if image := strings.TrimSpace(os.Getenv("ASTRIX_SANDBOX_IMAGE")); image != "" {
		profile.Image = image
	}
	if command := strings.TrimSpace(os.Getenv("ASTRIX_TEST_COMMAND")); command != "" {
		var argv []string
		if json.Unmarshal([]byte(command), &argv) == nil && len(argv) > 0 {
			profile.TestCommand = argv
		} else {
			profile.TestCommand = strings.Fields(command)
		}
	}
	return profile, nil
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
