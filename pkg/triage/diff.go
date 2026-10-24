package triage

import (
	"fmt"
	"strings"
)

// ExtractUnifiedDiff extracts one or more unified-diff file sections from a
// model response, removing leading prose and markdown fences.
func ExtractUnifiedDiff(rawLLMResponse string) (string, error) {
	raw := strings.ReplaceAll(strings.TrimSpace(rawLLMResponse), "\r\n", "\n")
	lines := strings.Split(raw, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "diff --git ") || strings.HasPrefix(line, "--- a/") || strings.HasPrefix(line, "--- /dev/null") {
			start = i
			break
		}
	}
	if start < 0 {
		return "", fmt.Errorf("LLM response contains no unified diff file header")
	}
	var out []string
	inHunk := false
	for _, line := range lines[start:] {
		if strings.HasPrefix(line, "```") {
			if len(out) > 0 {
				break
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, "diff --git "), strings.HasPrefix(line, "index "), strings.HasPrefix(line, "new file mode "), strings.HasPrefix(line, "deleted file mode "), strings.HasPrefix(line, "similarity index "), strings.HasPrefix(line, "rename from "), strings.HasPrefix(line, "rename to "):
			inHunk = false
			out = append(out, line)
		case !inHunk && (strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ")):
			inHunk = false
			out = append(out, line)
		case strings.HasPrefix(line, "@@"):
			inHunk = true
			out = append(out, line)
		case inHunk && (line == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") || strings.HasPrefix(line, "\\")):
			out = append(out, line)
		default:
			if len(out) > 0 {
				return validateExtractedDiff(out)
			}
		}
	}
	return validateExtractedDiff(out)
}

func validateExtractedDiff(lines []string) (string, error) {
	patch := strings.TrimSpace(strings.Join(lines, "\n"))
	if patch == "" || !strings.Contains(patch, "@@") || !strings.Contains(patch, "+++ ") {
		return "", fmt.Errorf("LLM response contains an incomplete unified diff")
	}
	return patch + "\n", nil
}
