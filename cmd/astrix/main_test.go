package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vivek5200/astrix/pkg/app"
	"github.com/vivek5200/axlang/formatter"
	"github.com/vivek5200/axlang/parser"
)

func TestParseGraphArgs(t *testing.T) {
	tests := []struct {
		args               []string
		path, diff, symbol string
	}{
		{args: []string{"."}, path: "."},
		{args: []string{"src", "--symbol", "Add"}, path: "src", symbol: "Add"},
		{args: []string{".", "--diff", "change.diff"}, path: ".", diff: "change.diff"},
	}
	for _, test := range tests {
		path, diff, symbol, err := parseGraphArgs(test.args)
		if err != nil {
			t.Fatalf("parseGraphArgs(%v): %v", test.args, err)
		}
		if path != test.path || diff != test.diff || symbol != test.symbol {
			t.Errorf("parseGraphArgs(%v)=(%q,%q,%q)", test.args, path, diff, symbol)
		}
	}
	if _, _, _, err := parseGraphArgs([]string{".", "--diff", "x", "--symbol", "Add"}); err == nil {
		t.Fatal("expected mutually exclusive graph options to fail")
	}
}

func TestProjectTestCommandDetectsNodeAndSupportsCustomArgv(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"scripts":{"test":"vitest"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ASTRIX_TEST_COMMAND", "")
	command, err := projectTestCommand(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"npm", "test"}; !reflect.DeepEqual(command, want) {
		t.Fatalf("projectTestCommand()=%v, want %v", command, want)
	}
	t.Setenv("ASTRIX_TEST_COMMAND", `["./ci/check.sh","--ci"]`)
	command, err = projectTestCommand(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"./ci/check.sh", "--ci"}; !reflect.DeepEqual(command, want) {
		t.Fatalf("custom projectTestCommand()=%v, want %v", command, want)
	}
	t.Setenv("ASTRIX_TEST_COMMAND", `"npm test"`)
	if _, err := projectTestCommand(root); err == nil {
		t.Fatal("invalid string command should be rejected; expected a JSON argv array")
	}
}

// invokeCLI executes run() capturing stdout and stderr into temp files.
func invokeCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	stdout, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	stderr, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	code := run(args, stdout, stderr)
	stdoutData, err := os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatal(err)
	}
	stderrData, err := os.ReadFile(stderr.Name())
	if err != nil {
		t.Fatal(err)
	}
	return code, string(stdoutData), string(stderrData)
}

func TestCLI_InitGeneratesValidAstrixfile(t *testing.T) {
	t.Setenv("ASTRIX_TEST_COMMAND", "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Cargo.toml"), []byte("[package]\nname = \"demo\"\nversion = \"0.1.0\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := invokeCLI(t, "init", root)
	if code != 0 {
		t.Fatalf("init exit code=%d; stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, filepath.Join(root, "Astrixfile.ax")) {
		t.Errorf("init confirmation should name the created file, got: %s", stdout)
	}
	configPath := filepath.Join(root, "Astrixfile.ax")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	if !strings.Contains(source, fmt.Sprintf("workspace %q {", filepath.Base(root))) {
		t.Errorf("workspace name should be the directory base name:\n%s", source)
	}
	if want := "  languages = [\"javascript\", \"rust\"]"; !strings.Contains(source, want) {
		t.Errorf("missing %q (deterministic language order) in:\n%s", want, source)
	}
	if want := "  cache     = \".astrix/cache\""; !strings.Contains(source, want) {
		t.Errorf("missing %q in:\n%s", want, source)
	}
	if want := "  image   = \"node:22-alpine\""; !strings.Contains(source, want) {
		t.Errorf("missing detected sandbox image %q in:\n%s", want, source)
	}
	if want := "  command = \"npm test -- --run\""; !strings.Contains(source, want) {
		t.Errorf("missing detected test command %q in:\n%s", want, source)
	}
	for _, want := range []string{
		"guardrail \"security_boundaries\" {",
		"    diff.modifies_tests(\"true\")",
		"heal \"heal\" on failure(stage.verify) {",
		"  max_attempts = 3",
		"    actor.type == \"autonomous_llm\" and diff.modifies_tests(\"false\")",
	} {
		if !strings.Contains(source, want) {
			t.Errorf("missing %q in:\n%s", want, source)
		}
	}
	file, diags := parser.New(configPath, source).Parse()
	if len(diags) != 0 {
		t.Fatalf("generated file does not parse: %v", diags)
	}
	if reformatted := formatter.Format(file); reformatted != source {
		t.Errorf("init output is not canonical formatter output:\n%s\n--- want ---\n%s", source, reformatted)
	}
	if code, _, stderr := invokeCLI(t, "check", configPath); code != 0 {
		t.Fatalf("astrix check on generated file exit code=%d; stderr: %s", code, stderr)
	}
}

func TestCLI_InitRefusesOverwriteWithoutForce(t *testing.T) {
	t.Setenv("ASTRIX_TEST_COMMAND", "")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := invokeCLI(t, "init", root); code != 0 {
		t.Fatalf("first init exit code=%d; stderr: %s", code, stderr)
	}
	configPath := filepath.Join(root, "Astrixfile.ax")
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	code, _, stderr := invokeCLI(t, "init", root)
	if code == 0 {
		t.Fatal("second init without --force should fail")
	}
	if !strings.Contains(stderr, configPath) || !strings.Contains(stderr, "already exists") {
		t.Errorf("overwrite refusal should name the file, got: %s", stderr)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed init must not clobber the existing Astrixfile.ax")
	}
	if code, _, stderr := invokeCLI(t, "init", "--force", root); code != 0 {
		t.Fatalf("init --force exit code=%d; stderr: %s", code, stderr)
	}
}

func TestCLI_MCPConfig(t *testing.T) {
	code, stdout, stderr := invokeCLI(t, "mcp", "config")
	if code != 0 {
		t.Fatalf("mcp config exit code=%d; stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "claude mcp add astrix -- astrix mcp serve") {
		t.Errorf("mcp config should mention the Claude Code command, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, ".cursor/mcp.json") || !strings.Contains(stdout, ".vscode/mcp.json") {
		t.Errorf("mcp config should reference cursor and vscode mcp.json paths, got:\n%s", stdout)
	}
	sawCursor, sawVSCode := false, false
	for i, block := range strings.Split(stdout, "```") {
		if i%2 == 0 {
			continue // only fenced segments are code blocks
		}
		trimmed := strings.TrimSpace(block)
		at := strings.Index(trimmed, "{")
		if at < 0 {
			continue // not a JSON block (e.g. a shell fence or prose)
		}
		trimmed = trimmed[at:] // strip a fence language tag such as "json"
		var parsed map[string]any
		if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
			t.Errorf("fenced block is not valid JSON: %v\n%s", err, trimmed)
			continue
		}
		checkServerEntry := func(container any) {
			servers, ok := container.(map[string]any)
			if !ok {
				t.Errorf("expected a servers object, got: %v", container)
				return
			}
			entry, ok := servers["astrix"].(map[string]any)
			if !ok {
				t.Errorf("expected an astrix server entry, got: %v", servers)
				return
			}
			if entry["command"] != "astrix" {
				t.Errorf("astrix server command=%v, want astrix", entry["command"])
			}
			if parsed["servers"] != nil && entry["type"] != "stdio" {
				t.Errorf("VS Code Astrix server type=%v, want stdio", entry["type"])
			}
			args, err := json.Marshal(entry["args"])
			if err != nil || string(args) != "[\"mcp\",\"serve\"]" {
				t.Errorf("astrix server args=%v, want [mcp serve]", entry["args"])
			}
		}
		if container, ok := parsed["mcpServers"]; ok {
			sawCursor = true
			checkServerEntry(container)
		}
		if container, ok := parsed["servers"]; ok {
			sawVSCode = true
			checkServerEntry(container)
		}
	}
	if !sawCursor || !sawVSCode {
		t.Fatalf("expected parseable mcpServers (Cursor) and servers (VS Code) JSON blocks; cursor=%v vscode=%v\n%s", sawCursor, sawVSCode, stdout)
	}
	if code, _, stderr := invokeCLI(t, "mcp", "config", "unexpected"); code == 0 || !strings.Contains(stderr, "usage: astrix mcp config") {
		t.Errorf("unexpected mcp config argument should fail with usage, code=%d stderr=%q", code, stderr)
	}
}

func TestDetectLanguagesFindsNestedPolyglotSourcesAndSkipsDependencies(t *testing.T) {
	root := t.TempDir()
	for path, content := range map[string]string{
		"src/main.go":                      "package main\n",
		"services/api/app.py":              "",
		"frontend/src/view.tsx":            "",
		"native/lib.rs":                    "",
		"node_modules/ignored/ignore.java": "",
		"vendor/ignored/ignore.rb":         "",
	} {
		fullPath := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"go", "typescript", "python", "rust"}
	if got := app.DetectLanguages(root); !reflect.DeepEqual(got, want) {
		t.Fatalf("DetectLanguages()=%v, want %v", got, want)
	}
}

func TestCLI_InitIsDeterministic(t *testing.T) {
	t.Setenv("ASTRIX_TEST_COMMAND", "")
	generate := func() string {
		t.Helper()
		root := t.TempDir()
		files := map[string]string{"package.json": "{}\n", "requirements.txt": "pytest\n", "app.ts": "", "util.js": ""}
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if code, _, stderr := invokeCLI(t, "init", root); code != 0 {
			t.Fatalf("init exit code=%d; stderr: %s", code, stderr)
		}
		data, err := os.ReadFile(filepath.Join(root, "Astrixfile.ax"))
		if err != nil {
			t.Fatal(err)
		}
		source := string(data)
		if !strings.Contains(source, "  languages = [\"typescript\", \"javascript\", \"python\"]") {
			t.Errorf("all active language markers should be detected and ordered:\n%s", source)
		}
		// Temp dir names differ; normalize the workspace name before comparing.
		start := strings.Index(source, "workspace \"") + len("workspace \"")
		end := strings.Index(source[start:], "\"") + start
		return source[:start] + "NAME" + source[end:]
	}
	if first, second := generate(), generate(); first != second {
		t.Fatalf("init output is not stable across runs:\n%s\n--- second run ---\n%s", first, second)
	}
}
