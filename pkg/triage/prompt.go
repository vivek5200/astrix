package triage

import (
	"fmt"
	"strings"

	"github.com/vivek5200/astrix/pkg/astgraph"
)

// FailureContext contains the structural and sanitized data sent to a repair model.
type FailureContext struct {
	Impact           astgraph.ImpactSet
	SourceLocations  []SourceLocation
	SanitizedLog     string
	PreviousPatch    string
	PreviousError    string
	Attempt          int
	MaxAttempts      int
	AllowTestChanges bool
}

// SourceLocation identifies a non-test source definition in the impact graph.
type SourceLocation struct {
	Path, Symbol       string
	StartLine, EndLine int
	Source             string
}

// BuildPrompt produces a bounded, explicit repair prompt with test-file restrictions.
func BuildPrompt(context FailureContext) string {
	var b strings.Builder
	maxAttempts := context.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = 3
	}
	fmt.Fprintf(&b, "SYSTEM REQUIREMENTS: Repair attempt %d of %d. Return ONLY a valid unified diff.\n", context.Attempt, maxAttempts)
	b.WriteString("Use exact repository-relative paths in --- a/<path> and +++ b/<path> headers, followed by valid @@ hunk headers. Do not include prose or markdown fences. Do not edit any *_test.go file, test directory, or test assertion.\n")
	b.WriteString("The source excerpt is the literal current on-disk code, not an earlier baseline. Make every removed hunk line match that excerpt exactly; do not reconstruct or revert to older code.\n")
	b.WriteString("Example format (including the required hunk header):\n```diff\n--- a/calc.py\n+++ b/calc.py\n@@ -1,3 +1,3 @@\n def calculate(a, b):\n-    return a - b\n+    return a + b\n\n```\n")
	if context.AllowTestChanges {
		b.WriteString("Test-file changes were explicitly permitted; do not weaken assertions.\n")
	} else {
		b.WriteString("Do not modify tests or weaken assertions.\n")
	}
	b.WriteString("Impacted symbols:\n")
	for _, symbol := range context.Impact.ImpactedSymbols {
		fmt.Fprintf(&b, "- %s\n", symbol)
	}
	b.WriteString("Impacted source locations (repository-relative path and line range):\n")
	for _, location := range context.SourceLocations {
		fmt.Fprintf(&b, "- %s:%d-%d (%s)\n", location.Path, location.StartLine, location.EndLine, location.Symbol)
		if location.Source != "" {
			b.WriteString("  Source excerpt:\n  ```\n")
			for i, line := range strings.Split(location.Source, "\n") {
				fmt.Fprintf(&b, "  %d | %s\n", location.StartLine+i, line)
			}
			b.WriteString("  ```\n")
		}
	}
	b.WriteString("Impacted test targets:\n")
	for _, test := range context.Impact.Tests {
		fmt.Fprintf(&b, "- %s\n", test)
	}
	b.WriteString("Sanitized failure output:\n```\n")
	b.WriteString(context.SanitizedLog)
	b.WriteString("\n```\n")
	if context.PreviousPatch != "" {
		b.WriteString("The previous patch did not pass validation or isolated checks. Improve it while preserving the policy constraints:\n```diff\n")
		b.WriteString(context.PreviousPatch)
		b.WriteString("\n```\n")
		if context.PreviousError != "" {
			errorText := context.PreviousError
			if len(errorText) > 2000 {
				errorText = errorText[:2000] + "…"
			}
			b.WriteString("Previous rejection reason (fix this exact issue):\n````\n")
			b.WriteString(errorText)
			b.WriteString("\n````\n")
		}
	}
	return b.String()
}
