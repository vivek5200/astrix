package triage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractUnifiedDiffFromFencedProse(t *testing.T) {
	raw := "Here is the repair:\n```diff\ndiff --git a/pkg/calc.go b/pkg/calc.go\n--- a/pkg/calc.go\n+++ b/pkg/calc.go\n@@ -7,9 +7,3 @@\n-return a - b\n+return a + b\n```\nThis fixes the function."
	got, err := ExtractUnifiedDiff(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "diff --git a/pkg/calc.go b/pkg/calc.go\n") || strings.Contains(got, "Here is") || strings.Contains(got, "```diff") || strings.Contains(got, "This fixes") {
		t.Fatalf("extracted patch=%q", got)
	}
}

func TestExtractUnifiedDiffWithoutGitPreamble(t *testing.T) {
	got, err := ExtractUnifiedDiff("--- a/pkg/f.go\n+++ b/pkg/f.go\n@@ -1 +1 @@\n-a\n+b")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "+b\n") {
		t.Fatalf("patch missing normalized final newline: %q", got)
	}
}

func TestBuildPromptIncludesSourceLinesAndStrictDiffContract(t *testing.T) {
	got := BuildPrompt(FailureContext{SourceLocations: []SourceLocation{{Path: "pkg/calc/calc.go", Symbol: "Multiply", StartLine: 4, EndLine: 6, Source: "func Multiply(a, b int) int { return a + b }"}}, SanitizedLog: "failure", PreviousPatch: "bad diff", PreviousError: "hunk context did not apply"})
	for _, value := range []string{"pkg/calc/calc.go:4-6 (Multiply)", "4 | func Multiply(a, b int) int { return a + b }", "ONLY a valid unified diff", "--- a/<path>", "@@ -1,3 +1,3 @@", "Do not edit any *_test.go", "Sanitized failure output", "hunk context did not apply"} {
		if !strings.Contains(got, value) {
			t.Errorf("prompt missing %q", value)
		}
	}
}

func TestNormalizeModelResponseShapes(t *testing.T) {
	t.Run("standard diff", func(t *testing.T) {
		got, err := NormalizeModelResponse("--- a/x.py\n+++ b/x.py\n@@ -1 +1 @@\n-a\n+b", "", nil)
		if err != nil || !strings.Contains(got, "@@ -1 +1 @@") {
			t.Fatalf("got %q, err %v", got, err)
		}
	})
	t.Run("headerless", func(t *testing.T) {
		got, err := NormalizeModelResponse("--- a/x.py\n+++ b/x.py\n-a = 1\n+a = 2", "", nil)
		if err != nil || !strings.Contains(got, "@@ -1,1 +1,1 @@") || !strings.Contains(got, "-a = 1\n+a = 2") {
			t.Fatalf("got %q, err %v", got, err)
		}
	})
	t.Run("fenced full file", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "src"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "src/calc.py"), []byte("def add(a, b):\n    return a - b\n"), 0644); err != nil {
			t.Fatal(err)
		}
		got, err := NormalizeModelResponse("```python\ndef add(a, b):\n    return a + b\n```", root, []string{"src/calc.py"})
		if err != nil || !strings.Contains(got, "@@ -1,2 +1,2 @@") || !strings.Contains(got, "-def add(a, b):\n-    return a - b\n+def add(a, b):\n+    return a + b") {
			t.Fatalf("got %q, err %v", got, err)
		}
	})
	for _, response := range []string{"The answer is to fix the math.", "--- a/a.py\n+++ b/a.py\n@@ -1 +1 @@\n-a\n+b\n--- a/b.py\n+++ b/b.py\n@@ -1 +1 @@\n-c\n+d"} {
		if got, err := NormalizeModelResponse(response, t.TempDir(), []string{"a.py"}); err == nil {
			t.Errorf("accepted ambiguous response %q as %q", response, got)
		}
	}
}
