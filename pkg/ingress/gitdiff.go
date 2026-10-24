// Package ingress parses repository changes before policy and analysis stages.
package ingress

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
)

// LineRange is an inclusive range of changed lines in the new file.
type LineRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// FileChange records changed line ranges and file operation metadata.
type FileChange struct {
	Path    string      `json:"path"`
	OldPath string      `json:"old_path,omitempty"`
	NewFile bool        `json:"new_file,omitempty"`
	Deleted bool        `json:"deleted,omitempty"`
	Ranges  []LineRange `json:"ranges"`
}

// ParseUnifiedDiff extracts new-file line ranges from a unified diff.
func ParseUnifiedDiff(diff string) ([]FileChange, error) {
	scanner := bufio.NewScanner(strings.NewReader(diff))
	scanner.Buffer(make([]byte, 4096), 4*1024*1024)
	var files []FileChange
	var current *FileChange
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "diff --git ") {
			files = append(files, FileChange{})
			current = &files[len(files)-1]
			parts := strings.SplitN(strings.TrimPrefix(line, "diff --git "), " ", 2)
			if len(parts) == 2 {
				current.OldPath = trimDiffPrefix(parts[0])
				current.Path = trimDiffPrefix(parts[1])
			}
			continue
		}
		if current == nil {
			continue
		}
		if strings.HasPrefix(line, "new file mode ") {
			current.NewFile = true
			continue
		}
		if strings.HasPrefix(line, "deleted file mode ") {
			current.Deleted = true
			continue
		}
		if strings.HasPrefix(line, "rename from ") {
			current.OldPath = strings.TrimPrefix(line, "rename from ")
			continue
		}
		if strings.HasPrefix(line, "rename to ") {
			current.Path = strings.TrimPrefix(line, "rename to ")
			continue
		}
		if strings.HasPrefix(line, "+++ ") {
			name := strings.TrimPrefix(line, "+++ ")
			if name == "/dev/null" {
				current.Deleted = true
			} else {
				current.Path = trimDiffPrefix(name)
			}
			continue
		}
		if strings.HasPrefix(line, "@@ ") {
			newStart, newCount, err := parseNewHunkRange(line)
			if err != nil {
				return nil, err
			}
			if newCount == 0 {
				point := newStart
				if point > 1 {
					point--
				}
				current.Ranges = append(current.Ranges, LineRange{Start: point, End: point})
			} else {
				current.Ranges = append(current.Ranges, LineRange{Start: newStart, End: newStart + newCount - 1})
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read unified diff: %w", err)
	}
	for i := range files {
		if files[i].Path == "" {
			files[i].Path = files[i].OldPath
		}
		files[i].Ranges = mergeRanges(files[i].Ranges)
	}
	return files, nil
}

func parseNewHunkRange(line string) (int, int, error) {
	closeRel := strings.Index(line[2:], "@@")
	if closeRel < 0 {
		return 0, 0, fmt.Errorf("malformed hunk header %q", line)
	}
	close := closeRel + 2
	parts := strings.Fields(line[2:close])
	if len(parts) < 2 || !strings.HasPrefix(parts[1], "+") {
		return 0, 0, fmt.Errorf("malformed hunk header %q", line)
	}
	newRange := strings.TrimPrefix(parts[1], "+")
	bounds := strings.SplitN(newRange, ",", 2)
	start, err := strconv.Atoi(bounds[0])
	if err != nil {
		return 0, 0, fmt.Errorf("malformed hunk line %q", line)
	}
	count := 1
	if len(bounds) == 2 {
		count, err = strconv.Atoi(bounds[1])
		if err != nil || count < 0 {
			return 0, 0, fmt.Errorf("malformed hunk count in %q", line)
		}
	}
	return start, count, nil
}
func trimDiffPrefix(path string) string {
	if path == "/dev/null" {
		return path
	}
	return strings.TrimPrefix(strings.TrimPrefix(path, "a/"), "b/")
}
func mergeRanges(in []LineRange) []LineRange {
	if len(in) < 2 {
		return in
	}
	out := []LineRange{in[0]}
	for _, r := range in[1:] {
		last := &out[len(out)-1]
		if r.Start <= last.End+1 {
			if r.End > last.End {
				last.End = r.End
			}
		} else {
			out = append(out, r)
		}
	}
	return out
}
