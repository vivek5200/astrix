package triage

import (
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
	for _, value := range []string{"pkg/calc/calc.go:4-6 (Multiply)", "func Multiply(a, b int) int { return a + b }", "ONLY a valid unified diff", "--- a/<path>", "Do not edit any *_test.go", "Sanitized failure output", "hunk context did not apply"} {
		if !strings.Contains(got, value) {
			t.Errorf("prompt missing %q", value)
		}
	}
}
