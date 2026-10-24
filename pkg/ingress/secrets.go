package ingress

import (
	"math"
	"regexp"
	"strings"
)

// SecretFinding describes a likely credential in source text.
type SecretFinding struct {
	Kind  string `json:"kind"`
	Line  int    `json:"line"`
	Value string `json:"value"`
}

var credentialPatterns = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{"private_key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`)},
	{"aws_access_key", regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)},
	{"github_token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9_]{30,}\b`)},
	{"credential_assignment", regexp.MustCompile(`(?i)(?:api[_-]?key|secret|token|password)\s*[:=]\s*["']?([A-Za-z0-9/+=_-]{20,})`)},
}

// ScanSecrets returns concrete credential-pattern hits and high-entropy tokens.
// The function avoids returning surrounding source context, only the matched value.
func ScanSecrets(source []byte) []SecretFinding {
	var findings []SecretFinding
	seen := map[string]bool{}
	lines := strings.Split(string(source), "\n")
	for lineNo, line := range lines {
		for _, rule := range credentialPatterns {
			for _, loc := range rule.pattern.FindAllStringIndex(line, -1) {
				value := line[loc[0]:loc[1]]
				addFinding(&findings, seen, SecretFinding{Kind: rule.name, Line: lineNo + 1, Value: value})
			}
		}
		for _, candidate := range highEntropyCandidates.FindAllString(line, -1) {
			if len(candidate) >= 24 && shannonEntropy(candidate) >= 4.2 {
				addFinding(&findings, seen, SecretFinding{Kind: "high_entropy", Line: lineNo + 1, Value: candidate})
			}
		}
	}
	return findings
}

// ScanUnifiedDiffSecrets checks added lines only, skipping unified-diff metadata.
func ScanUnifiedDiffSecrets(diff string) []SecretFinding {
	var added strings.Builder
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			added.WriteString(line[1:])
			added.WriteByte('\n')
		}
	}
	return ScanSecrets([]byte(added.String()))
}

var highEntropyCandidates = regexp.MustCompile(`[A-Za-z0-9_+/=-]{24,}`)

func shannonEntropy(s string) float64 {
	counts := map[rune]int{}
	for _, r := range s {
		counts[r]++
	}
	n := float64(len(s))
	entropy := 0.0
	for _, count := range counts {
		p := float64(count) / n
		entropy -= p * math.Log2(p)
	}
	return entropy
}
func addFinding(out *[]SecretFinding, seen map[string]bool, f SecretFinding) {
	key := f.Kind + "\x00" + string(rune(f.Line)) + "\x00" + f.Value
	if !seen[key] {
		seen[key] = true
		f.Value = "[REDACTED]"
		*out = append(*out, f)
	}
}
