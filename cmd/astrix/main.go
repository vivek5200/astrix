// Command astrix exposes repository analysis, policy, repair and MCP operations.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vivek5200/astrix/pkg/app"
	"github.com/vivek5200/astrix/pkg/astgraph"
	"github.com/vivek5200/astrix/pkg/cache"
	"github.com/vivek5200/astrix/pkg/ingress"
	"github.com/vivek5200/astrix/pkg/mcp"
	"github.com/vivek5200/astrix/pkg/policy"
	"github.com/vivek5200/astrix/pkg/runner"
	"github.com/vivek5200/astrix/pkg/triage"
	"github.com/vivek5200/axlang/ast"
	"github.com/vivek5200/axlang/parser"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
func run(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: astrix <check|graph|verify|init|mcp serve|mcp config> [options]")
		return 2
	}
	ctx := context.Background()
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := triage.LoadDotEnv(filepath.Join(root, ".env")); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	switch args[0] {
	case "check":
		path := configPath(root)
		if len(args) > 1 {
			path = args[1]
		}
		if _, err = app.ValidateAstrixfile(path); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "%s: valid\n", path)
		return 0
	case "graph":
		graphPath, diffPath, symbol, err := parseGraphArgs(args[1:])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		graph, err := astgraph.LoadPath(graphPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if diffPath != "" {
			diff, readErr := os.ReadFile(diffPath)
			if readErr != nil {
				fmt.Fprintln(stderr, readErr)
				return 1
			}
			changes, parseErr := ingress.ParseUnifiedDiff(string(diff))
			if parseErr != nil {
				fmt.Fprintln(stderr, parseErr)
				return 1
			}
			impact := astgraph.BlastRadius(graph, changes)
			return writeJSON(stdout, impact, stderr)
		}
		return writeJSON(stdout, astgraph.FilterSymbol(graph, symbol), stderr)
	case "verify":
		fix := false
		for _, arg := range args[1:] {
			if arg == "--fix" {
				fix = true
			} else {
				fmt.Fprintf(stderr, "unknown verify option %q\n", arg)
				return 2
			}
		}
		diff, diffErr := app.CurrentDiff(ctx, root)
		if diffErr == nil {
			if findings := ingress.ScanUnifiedDiffSecrets(diff); len(findings) > 0 {
				_ = json.NewEncoder(stderr).Encode(map[string]any{"error": "credential detected", "findings": findings})
				return 2
			}
		}
		if diffErr == nil && strings.TrimSpace(diff) == "" {
			store, key, cacheErr := verificationCache(root)
			if cacheErr == nil {
				outcome, hit, getErr := store.Get(key)
				if getErr == nil && hit && outcome.Passed {
					fmt.Fprintln(stdout, "verification passed (cache hit: 100%; tests executed: 0)")
					return 0
				}
			}
		}
		testCommand, commandErr := projectTestCommand(root)
		if strings.TrimSpace(os.Getenv("ASTRIX_TEST_COMMAND")) == "" {
			if profile, profileErr := app.ResolveProjectProfile(root, configPath(root)); profileErr == nil && profile.CommandOverridden && len(profile.TestCommand) > 0 {
				testCommand = profile.TestCommand
				commandErr = nil
			}
		}
		if commandErr != nil {
			fmt.Fprintln(stderr, commandErr)
			return 2
		}
		initial := exec.CommandContext(ctx, testCommand[0], testCommand[1:]...)
		initial.Dir = root
		output, err := initial.CombinedOutput()
		if err == nil {
			storeVerificationCache(root, stderr)
			fmt.Fprintln(stdout, "verification passed")
			return 0
		}
		if !fix {
			_, _ = stderr.Write(output)
			fmt.Fprintln(stderr, err)
			return 1
		}
		currentDiff, e := app.CurrentDiff(ctx, root)
		if e != nil {
			fmt.Fprintln(stderr, e)
			return 1
		}
		diff = currentDiff
		impact, changes, e := app.Impact(root, diff)
		if e != nil {
			fmt.Fprintln(stderr, e)
			return 1
		}
		patcher, e := configuredPatcher(root, impact.Tests)
		if e != nil {
			fmt.Fprintln(stderr, e)
			return 1
		}
		testChanges := false
		for _, c := range changes {
			if strings.HasSuffix(c.Path, "_test.go") || strings.Contains(c.Path, "/tests/") {
				testChanges = true
			}
		}
		gate, e := app.PolicyGate(configPath(root))
		if e != nil {
			fmt.Fprintln(stderr, e)
			return 1
		}
		patcher.Policy = func(ctx context.Context, patch string) error {
			patchFiles, err := patchPaths(patch)
			if err != nil {
				return err
			}
			intent := policy.ActionIntent{Actor: policy.Actor{Type: "autonomous_llm"}, Target: policy.Target{Env: "development"}, ModifiedFiles: patchFiles, Diff: policy.Diff{ModifiesTests: boolString(testChanges)}}
			decision, err := gate.Evaluate(ctx, intent)
			if err != nil {
				return err
			}
			if decision.RequiresApproval && !intent.Approved {
				return &triage.ApprovalRequiredError{Reason: decision.Reason}
			}
			if !decision.Allowed {
				return fmt.Errorf("policy denied patch: %s", decision.Reason)
			}
			return nil
		}
		sourceGraph, e := astgraph.LoadPath(root)
		if e != nil {
			fmt.Fprintln(stderr, e)
			return 1
		}
		var sourceLocations []triage.SourceLocation
		for _, id := range impact.ImpactedSymbols {
			if node, ok := sourceGraph.Nodes[id]; ok && !node.IsTest {
				sourceLocations = append(sourceLocations, triage.SourceLocation{Path: node.File, Symbol: node.Name, StartLine: node.StartLine, EndLine: node.EndLine, Source: sourceExcerpt(root, node.File, node.StartLine, node.EndLine)})
			}
		}
		result, e := patcher.Repair(ctx, triage.FailureContext{Impact: impact, SourceLocations: sourceLocations, SanitizedLog: triage.SanitizeLog(string(output))})
		if e != nil {
			fmt.Fprintln(stderr, e)
			return 1
		}
		if e = applyPatch(ctx, root, result.Patch); e != nil {
			fmt.Fprintln(stderr, e)
			return 1
		}
		storeVerificationCache(root, stderr)
		fmt.Fprintf(stdout, "repair patch passed isolated tests after %d attempt(s) and was applied\n", result.Attempts)
		return 0
	case "init":
		force := false
		dir := ""
		for i := 1; i < len(args); i++ {
			arg := args[i]
			switch {
			case arg == "--force":
				force = true
			case strings.HasPrefix(arg, "-"):
				fmt.Fprintf(stderr, "unknown init option %q\nusage: astrix init [--force] [dir]\n", arg)
				return 2
			case dir != "":
				fmt.Fprintln(stderr, "usage: astrix init [--force] [dir]")
				return 2
			default:
				dir = arg
			}
		}
		if dir == "" {
			dir = "."
		}
		if err := app.InitProject(dir, force, stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "mcp":
		if len(args) < 2 {
			fmt.Fprintln(stderr, "usage: astrix mcp <serve|config>")
			return 2
		}
		switch args[1] {
		case "serve":
			if len(args) != 2 {
				fmt.Fprintln(stderr, "usage: astrix mcp serve")
				return 2
			}
			return serveMCP(ctx, root, os.Stdin, stdout, stderr)
		case "config":
			if len(args) != 2 {
				fmt.Fprintln(stderr, "usage: astrix mcp config")
				return 2
			}
			return printMCPConfig(stdout, stderr)
		default:
			fmt.Fprintf(stderr, "unknown mcp subcommand %q\nusage: astrix mcp <serve|config>\n", args[1])
			return 2
		}
	default:
		fmt.Fprintf(stderr, "unknown command %q\nusage: astrix <check|graph|verify|init|mcp serve|mcp config> [options]\n", args[0])
		return 2
	}
}

func verificationCache(root string) (cache.Cache, string, error) {
	graph, err := astgraph.LoadPath(root)
	if err != nil {
		return cache.Cache{}, "", err
	}
	ids := make([]string, 0, len(graph.Nodes))
	for id := range graph.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	graphKey := cache.SymbolSubgraphHash(graph, ids)
	fileData, err := json.Marshal(graph.FileHashes)
	if err != nil {
		return cache.Cache{}, "", err
	}
	sum := sha256.Sum256(append([]byte(graphKey), fileData...))
	store := cache.Cache{Root: filepath.Join(root, ".astrix", "cache")}
	return store, hex.EncodeToString(sum[:]), nil
}

func storeVerificationCache(root string, stderr *os.File) {
	store, key, err := verificationCache(root)
	if err == nil {
		err = store.Put(key, cache.TestOutcome{Passed: true})
	}
	if err != nil {
		fmt.Fprintf(stderr, "warning: could not update verification cache: %v\n", err)
	}
}

func parseGraphArgs(args []string) (path, diff, symbol string, err error) {
	path = "."
	pathSet := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--diff":
			if i+1 >= len(args) {
				return "", "", "", fmt.Errorf("--diff requires a git diff file")
			}
			i++
			diff = args[i]
		case "--symbol":
			if i+1 >= len(args) {
				return "", "", "", fmt.Errorf("--symbol requires a symbol name")
			}
			i++
			symbol = args[i]
		default:
			if strings.HasPrefix(args[i], "-") {
				return "", "", "", fmt.Errorf("unknown graph option %q", args[i])
			}
			if pathSet {
				return "", "", "", fmt.Errorf("usage: astrix graph [path] [--diff file | --symbol name]")
			}
			path = args[i]
			pathSet = true
		}
	}
	if diff != "" && symbol != "" {
		return "", "", "", fmt.Errorf("--diff and --symbol cannot be combined")
	}
	return path, diff, symbol, nil
}
func configPath(root string) string {
	path := filepath.Join(root, "Astrixfile.ax")
	if _, err := os.Stat(path); err == nil {
		return path
	}
	return filepath.Join(filepath.Dir(root), "Astrixfile.ax")
}
func configuredPatcher(root string, impactedTests []string) (*triage.Patcher, error) {
	if _, err := app.ValidateAstrixfile(configPath(root)); err != nil {
		return nil, err
	}
	modelFromFile := configuredModel(configPath(root))
	llm := triage.ResolveLLMConfig(os.Getenv, modelFromFile)
	command, err := projectTestCommand(root)
	if err != nil {
		return nil, err
	}
	profile, profileErr := app.ResolveProjectProfile(root, configPath(root))
	if profileErr == nil {
		if strings.TrimSpace(os.Getenv("ASTRIX_TEST_COMMAND")) == "" && len(profile.TestCommand) > 0 {
			var scoped []string
			for _, id := range impactedTests {
				if file, _, ok := strings.Cut(id, "::"); ok {
					scoped = append(scoped, file)
				}
			}
			command = profile.ScopedTestCommand(scoped)
		}
	} else {
		profile = runner.ProjectProfile{}
	}
	image := envOr("ASTRIX_SANDBOX_IMAGE", profile.Image)
	if image == "" {
		image = defaultSandboxImage(command)
	}
	return &triage.Patcher{Model: triage.OpenAICompatModel{Endpoint: llm.Endpoint, APIKey: llm.APIKey, Model: llm.Model}, Sandbox: runner.DockerSandbox{Root: root, Image: image, TestCommand: command, Profile: &profile}, Root: root, MaxAttempts: 3}, nil
}

// projectTestCommand resolves an argument-safe test command. Repositories with
// custom build systems can provide a JSON argv array in ASTRIX_TEST_COMMAND.
func projectTestCommand(root string) ([]string, error) {
	if value := strings.TrimSpace(os.Getenv("ASTRIX_TEST_COMMAND")); value != "" {
		var command []string
		if err := json.Unmarshal([]byte(value), &command); err != nil || len(command) == 0 || strings.TrimSpace(command[0]) == "" {
			return nil, fmt.Errorf("ASTRIX_TEST_COMMAND must be a JSON array of command arguments, for example [\"npm\",\"test\"]")
		}
		return command, nil
	}
	if _, err := os.Stat(filepath.Join(root, "astrix", "go.mod")); err == nil {
		return []string{"go", "test", "./astrix/...", "./axlang/..."}, nil
	}
	if _, err := os.Stat(filepath.Join(root, "mvnw")); err == nil {
		return []string{"./mvnw", "test"}, nil
	}
	if _, err := os.Stat(filepath.Join(root, "gradlew")); err == nil {
		return []string{"./gradlew", "test"}, nil
	}
	checks := []struct {
		files   []string
		command []string
	}{
		{[]string{"go.mod"}, []string{"go", "test", "./..."}},
		{[]string{"package.json"}, []string{"npm", "test"}},
		{[]string{"pyproject.toml", "pytest.ini", "tox.ini"}, []string{"python", "-m", "pytest"}},
		{[]string{"Cargo.toml"}, []string{"cargo", "test"}},
		{[]string{"pom.xml"}, []string{"mvn", "test"}},
		{[]string{"build.gradle", "build.gradle.kts"}, []string{"gradle", "test"}},
		{[]string{"*.sln", "*.csproj"}, []string{"dotnet", "test"}},
		{[]string{"composer.json"}, []string{"composer", "test"}},
		{[]string{"Gemfile"}, []string{"bundle", "exec", "rake", "test"}},
		{[]string{"Makefile"}, []string{"make", "test"}},
	}
	for _, check := range checks {
		for _, pattern := range check.files {
			matches, _ := filepath.Glob(filepath.Join(root, pattern))
			if len(matches) > 0 {
				command := append([]string(nil), check.command...)
				return command, nil
			}
		}
	}
	return nil, fmt.Errorf("cannot detect a project test command in %s; set ASTRIX_TEST_COMMAND to a JSON argv array (for example [\"make\",\"test\"])", root)
}

func defaultSandboxImage(command []string) string {
	if len(command) == 0 {
		return "golang:1.27"
	}
	switch strings.TrimPrefix(filepath.Base(command[0]), "./") {
	case "npm", "node", "yarn", "pnpm":
		return "node:22"
	case "python", "python3", "pytest":
		return "python:3.13"
	case "cargo", "rustc":
		return "rust:1.89"
	case "mvn", "gradle", "mvnw", "gradlew":
		return "maven:3-eclipse-temurin-21"
	case "dotnet":
		return "mcr.microsoft.com/dotnet/sdk:9.0"
	default:
		return "golang:1.27"
	}
}

func configuredModel(path string) string {
	source, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	file, diags := parser.New(path, string(source)).Parse()
	if len(diags) != 0 {
		return ""
	}
	for _, decl := range file.Decls {
		heal, ok := decl.(*ast.HealDecl)
		if !ok {
			continue
		}
		for _, field := range heal.Fields {
			if field.Name == "model" {
				if literal, ok := field.Value.(*ast.LiteralExpr); ok {
					return literal.Value
				}
			}
		}
	}
	return ""
}

func sourceExcerpt(root, path string, start, end int) string {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil || start < 1 || end < start {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	if start > len(lines) {
		return ""
	}
	if end > len(lines) {
		end = len(lines)
	}
	if end-start > 199 {
		end = start + 199
	}
	return strings.Join(lines[start-1:end], "\n")
}

func applyPatch(ctx context.Context, root, diff string) error {
	check := exec.CommandContext(ctx, "git", "apply", "--recount", "--check", "-")
	check.Dir = root
	check.Stdin = strings.NewReader(diff)
	if output, err := check.CombinedOutput(); err != nil {
		return fmt.Errorf("patch apply check failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	cmd := exec.CommandContext(ctx, "git", "apply", "--recount", "-")
	cmd.Dir = root
	cmd.Stdin = strings.NewReader(diff)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("apply accepted patch: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
func patchPaths(diff string) ([]string, error) {
	if err := triage.ValidatePatch(diff, false); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var paths []string
	for _, line := range strings.Split(diff, "\n") {
		for _, prefix := range []string{"+++ b/", "--- a/"} {
			if strings.HasPrefix(line, prefix) {
				path := strings.TrimPrefix(line, prefix)
				if path != "/dev/null" && !seen[path] {
					seen[path] = true
					paths = append(paths, path)
				}
			}
		}
	}
	return paths, nil
}
func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
func writeJSON(out *os.File, value any, stderr *os.File) int {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// printMCPConfig writes copy-pasteable MCP client setup instructions.
func printMCPConfig(stdout, stderr *os.File) int {
	type serverEntry struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	type cursorConfig struct {
		MCPServers map[string]serverEntry `json:"mcpServers"`
	}
	type vscodeServerEntry struct {
		Type    string   `json:"type"`
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	type vscodeConfig struct {
		Servers map[string]vscodeServerEntry `json:"servers"`
	}
	entry := serverEntry{Command: "astrix", Args: []string{"mcp", "serve"}}
	vscodeEntry := vscodeServerEntry{Type: "stdio", Command: "astrix", Args: []string{"mcp", "serve"}}
	cursor, err := json.MarshalIndent(cursorConfig{MCPServers: map[string]serverEntry{"astrix": entry}}, "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	vscode, err := json.MarshalIndent(vscodeConfig{Servers: map[string]vscodeServerEntry{"astrix": vscodeEntry}}, "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprint(stdout, strings.Join([]string{
		"Astrix MCP server setup",
		"",
		"Install Astrix first (see https://github.com/vivek5200/astrix#installation) and ensure the astrix binary is on the PATH inherited by your editor.",
		"The server uses the editor's workspace as its working directory, so open the target repository in the client before connecting.",
		"",
		"1. Claude Code",
		"   Run this in a terminal from your repository root:",
		"",
		"   claude mcp add astrix -- astrix mcp serve",
		"",
		"2. Cursor",
		"   Add the following block to ~/.cursor/mcp.json:",
		"",
		"```json",
		string(cursor),
		"```",
		"",
		"3. VS Code / Antigravity",
		"   Add the following block to .vscode/mcp.json (VS Code 1.99+ or the \"MCP: Add Server\" command):",
		"",
		"```json",
		string(vscode),
		"```",
		"",
	}, "\n"))
	return 0
}

func serveMCP(ctx context.Context, root string, input *os.File, output, stderr *os.File) int {
	handlers := map[string]mcp.Handler{}
	handlers["astrix_get_blast_radius"] = func(ctx context.Context, _ map[string]any) (any, error) {
		diff, err := app.CurrentDiff(ctx, root)
		if err != nil {
			return nil, err
		}
		impact, changes, err := app.Impact(root, diff)
		if err != nil {
			return nil, err
		}
		return map[string]any{"impact": impact, "changes": changes}, nil
	}
	handlers["astrix_evaluate_policy"] = func(ctx context.Context, args map[string]any) (any, error) {
		raw, ok := args["intent"]
		if !ok {
			return nil, fmt.Errorf("intent argument is required")
		}
		data, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		var intent policy.ActionIntent
		if err = json.Unmarshal(data, &intent); err != nil {
			return nil, err
		}
		gate, err := app.PolicyGate(configPath(root))
		if err != nil {
			return nil, err
		}
		return gate.Evaluate(ctx, intent)
	}
	handlers["astrix_run_impacted_tests"] = func(ctx context.Context, _ map[string]any) (any, error) {
		diff, err := app.CurrentDiff(ctx, root)
		if err != nil {
			return nil, err
		}
		impact, _, err := app.Impact(root, diff)
		if err != nil {
			return nil, err
		}
		if err = app.RunTests(ctx, root, impact); err != nil {
			return nil, err
		}
		return map[string]any{"tests": impact.Tests, "passed": true}, nil
	}
	server := mcp.NewServer(handlers)
	if err := server.Serve(ctx, input, output); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
