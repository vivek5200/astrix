// Package triage sanitizes failures and coordinates bounded repair attempts.
package triage

import (
	"regexp"
	"strings"
)

var sanitizers = []*regexp.Regexp{
	regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`),
	regexp.MustCompile(`\b\d{4}-\d\d-\d\d[T ]\d\d:\d\d:\d\d(?:\.\d+)?(?:Z|[+-]\d\d:\d\d)?\b`),
	regexp.MustCompile(`\b\d\d:\d\d:\d\d(?:\.\d+)?\b`),
	regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\b`),
	regexp.MustCompile(`\b0x[0-9a-fA-F]{8,}\b`),
}

// SanitizeLog removes common unstable terminal output while preserving stack text.
func SanitizeLog(input string) string {
	for _, pattern := range sanitizers {
		input = pattern.ReplaceAllString(input, "<volatile>")
	}
	lines := strings.Split(input, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t\r")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
