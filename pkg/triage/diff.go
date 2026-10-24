package triage

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ExtractUnifiedDiff extracts standard unified diff sections, removing prose and
// markdown fences. Replies without hunks require NormalizeModelResponse.
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

// NormalizeModelResponse converts supported model response shapes to a canonical
// unified diff. Full-file replacements are accepted only for one explicitly
// identified target file within root.
func NormalizeModelResponse(response, root string, targets []string) (string, error) {
	if patch, err := ExtractUnifiedDiff(response); err == nil {
		if strings.Count(patch, "\n--- a/") > 1 {
			return "", fmt.Errorf("multi-file model patches are ambiguous")
		}
		return checkPatchTargets(patch, targets)
	}
	response = strings.ReplaceAll(strings.TrimSpace(response), "\r\n", "\n")
	if patch, err := synthesizeHeaderless(response); err == nil {
		return checkPatchTargets(patch, targets)
	}
	if len(targets) != 1 || root == "" {
		return "", fmt.Errorf("LLM response is not a standard diff; full-file replacement requires one target file")
	}
	target := filepath.Clean(filepath.FromSlash(targets[0]))
	if filepath.IsAbs(target) || target == "." || target == ".." || strings.HasPrefix(target, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid replacement target %q", targets[0])
	}
	content, ok := fencedFileContent(response)
	if !ok {
		return "", fmt.Errorf("LLM response contains no valid unified diff hunk or fenced replacement")
	}
	path := filepath.Join(root, target)
	original, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read replacement target: %w", err)
	}
	return makeUnifiedDiff(filepath.ToSlash(target), string(original), content), nil
}

func checkPatchTargets(patch string, targets []string) (string, error) {
	if len(targets) == 0 {
		return patch, nil
	}
	allowed := map[string]bool{}
	for _, target := range targets {
		allowed[filepath.ToSlash(filepath.Clean(filepath.FromSlash(target)))] = true
	}
	for _, line := range strings.Split(patch, "\n") {
		if !strings.HasPrefix(line, "--- a/") {
			continue
		}
		name := filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimPrefix(line, "--- a/"))))
		if !allowed[name] {
			return "", fmt.Errorf("model patch targets %q outside the impacted source files", name)
		}
	}
	return patch, nil
}

func synthesizeHeaderless(raw string) (string, error) {
	lines := strings.Split(raw, "\n")
	oldName, newName := "", ""
	var removed, added []string
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "--- a/"):
			oldName = strings.TrimSpace(strings.TrimPrefix(line, "--- a/"))
		case strings.HasPrefix(line, "+++ b/"):
			newName = strings.TrimSpace(strings.TrimPrefix(line, "+++ b/"))
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			removed = append(removed, line[1:])
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			added = append(added, line[1:])
		}
	}
	if oldName == "" || newName == "" || oldName != newName || len(removed) == 0 || len(added) == 0 {
		return "", fmt.Errorf("not a headerless single-file diff")
	}
	return makeUnifiedDiff(oldName, strings.Join(removed, "\n"), strings.Join(added, "\n")), nil
}

func fencedFileContent(raw string) (string, bool) {
	lines := strings.Split(raw, "\n")
	start, end := -1, -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if start < 0 {
				start = i + 1
			} else {
				end = i
				break
			}
		}
	}
	if start < 0 || end < start {
		return "", false
	}
	for _, line := range lines[:start-1] {
		if strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "@@") {
			return "", false
		}
	}
	return strings.Join(lines[start:end], "\n") + "\n", true
}

// makeUnifiedDiff emits a compact whole-file hunk. Whole-file context is
// intentional: it guarantees exact line counts and lets the patch applier
// verify that the source has not drifted since the model saw it.
func makeUnifiedDiff(name, old, replacement string) string {
	oldLines := splitLines(old)
	newLines := splitLines(replacement)
	var b strings.Builder
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n@@ -1,%d +1,%d @@\n", name, name, len(oldLines), len(newLines))
	for _, line := range oldLines {
		b.WriteByte('-')
		b.WriteString(line)
		b.WriteByte('\n')
	}
	for _, line := range newLines {
		b.WriteByte('+')
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func validateExtractedDiff(lines []string) (string, error) {
	patch := strings.TrimSpace(strings.Join(lines, "\n"))
	if patch == "" || !strings.Contains(patch, "@@") || !strings.Contains(patch, "+++ ") {
		return "", fmt.Errorf("LLM response contains an incomplete unified diff")
	}
	return patch + "\n", nil
}
